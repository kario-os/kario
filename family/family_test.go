package family_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/family"
	"github.com/kario-os/kario/family/familytest"
	"github.com/kario-os/kario/keyset"
)

func rules() family.Rules {
	return family.Rules{Contacts: []family.Contact{{Name: "Mum", Number: "+86 139 0000 1111"}}, Apps: []string{"com.phone.calc"},
		Limits: map[string]int{"com.phone.calc": 30}, Downtime: family.Downtime{On: true, Start: 21 * 60, End: 7 * 60, Apps: []string{"com.phone.messages"}}}
}

// A policy verifies only with the server's key, for its phone, newer than
// the one in force and before it expires; a byte changed breaks it.
func TestVerify(t *testing.T) {
	edPub, key, _ := ed25519.GenerateKey(rand.Reader)
	pub := keyset.Set{edPub}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_800_000_000, 0)
	p := family.Policy{Device: "fd1", Version: 3, Issued: now.Unix(), Expires: now.Add(family.PolicyTTL).Unix(), Parent: "mum@example.com", Rules: rules()}
	s, err := family.Sign(key, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := family.Verify(pub, s, "fd1", 2, now)
	if err != nil || got.Version != 3 || got.Parent != "mum@example.com" || got.Limits["com.phone.calc"] != 30 || got.Contacts[0].Number != "+86 139 0000 1111" {
		t.Fatalf("verify: %+v, %v", got, err)
	}
	raw, _ := base64.StdEncoding.DecodeString(s.Policy)
	tampered := s
	tampered.Policy = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(raw), `"com.phone.calc":30`, `"com.phone.calc":300`, 1)))
	for name, c := range map[string]struct {
		pub    keyset.Set
		s      family.Signed
		device string
		after  int
		now    time.Time
		want   error
	}{
		"another key":         {keyset.Set{other}, s, "fd1", 2, now, family.ErrSignature},
		"tampered":            {pub, tampered, "fd1", 2, now, family.ErrSignature},
		"no signature":        {pub, family.Signed{Policy: s.Policy}, "fd1", 2, now, family.ErrSignature},
		"another phone":       {pub, s, "fd2", 2, now, family.ErrDevice},
		"the same version":    {pub, s, "fd1", 3, now, family.ErrReplay},
		"an older version":    {pub, s, "fd1", 7, now, family.ErrReplay},
		"expired":             {pub, s, "fd1", 2, now.Add(family.PolicyTTL), family.ErrExpired},
		"long after its time": {pub, s, "fd1", 0, now.Add(365 * 24 * time.Hour), family.ErrExpired},
	} {
		if _, err := family.Verify(c.pub, c.s, c.device, c.after, c.now); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	long := p
	long.Expires = now.Add(family.MaxTTL + time.Hour).Unix()
	if s, _ := family.Sign(key, long); !errors.Is(func() error { _, err := family.Verify(pub, s, "fd1", 0, now); return err }(), family.ErrExpired) {
		t.Error("a policy good for longer than MaxTTL was taken")
	}
}

// What a phone takes is bounded; repeated entries go before signing.
func TestRules(t *testing.T) {
	r := rules()
	r.Apps = append(r.Apps, " com.phone.calc ")
	r.Contacts = append(r.Contacts, family.Contact{Name: "Mum again", Number: "+86 139 0000 1111"})
	if n := r.Normalize(); len(n.Apps) != 1 || len(n.Contacts) != 1 || n.Contacts[0].Name != "Mum" {
		t.Fatalf("normalized: %+v", n)
	}
	for name, change := range map[string]func(*family.Rules){
		"a limit of 0":       func(r *family.Rules) { r.Limits["com.phone.calc"] = 0 },
		"a limit past a day": func(r *family.Rules) { r.Limits["com.phone.calc"] = family.MaxLimit + 1 },
		"not an app":         func(r *family.Rules) { r.Apps = append(r.Apps, "../etc") },
		"not a number":       func(r *family.Rules) { r.Contacts[0].Number = "mum" },
		"a control char":     func(r *family.Rules) { r.Contacts[0].Name = "M\x07um" },
		"downtime minute":    func(r *family.Rules) { r.Downtime.End = 24 * 60 },
		"too many contacts": func(r *family.Rules) {
			for i := 0; i <= family.MaxContacts; i++ {
				r.Contacts = append(r.Contacts, family.Contact{Number: "1"})
			}
		},
	} {
		r := rules()
		change(&r)
		if r.Check() == nil {
			t.Errorf("%s: taken", name)
		}
	}
	if err := rules().Check(); err != nil {
		t.Fatal(err)
	}
}

// The phone's client against the server in memory: pair, the parent's
// claim, allow, a policy that verifies with the server's key, a report, a
// location answer, and the parent's unbinding seen as ErrGone.
func TestClient(t *testing.T) {
	srv := familytest.New()
	var pushes []family.PushData
	srv.Push = func(token string, d family.PushData) error {
		if token != "tok" {
			t.Errorf("push to %q", token)
		}
		pushes = append(pushes, d)
		return nil
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx := context.Background()
	c := &family.Client{Server: ts.URL}
	p, err := c.Pair(ctx, family.Pair{PushToken: "tok"})
	if err != nil || len(p.Code) != family.CodeLen || c.Token != p.Token {
		t.Fatalf("pair: %+v %v", p, err)
	}
	if st, err := c.State(ctx); err != nil || st.State != family.StatePairing {
		t.Fatalf("state: %+v %v", st, err)
	}
	if _, err := srv.Claim("00000000"+"0", "mum@example.com"); err == nil {
		t.Fatal("a wrong code claimed")
	}
	id, err := srv.Claim(p.Code, "mum@example.com")
	if err != nil || id != p.Device {
		t.Fatal(err)
	}
	if st, _ := c.State(ctx); st.State != family.StateClaimed || st.Parent != "mum@example.com" {
		t.Fatalf("claimed: %+v", st)
	}
	if _, err := srv.Claim(p.Code, "eve@example.com"); err == nil {
		t.Fatal("the code claimed twice")
	}
	if err := c.Confirm(ctx, true); err != nil {
		t.Fatal(err)
	}
	if s, err := c.Policy(ctx); err != nil || s != nil {
		t.Fatalf("a policy before the parent set one: %v %v", s, err)
	}
	if _, err := srv.SetRules(id, rules()); err != nil {
		t.Fatal(err)
	}
	s, err := c.Policy(ctx)
	if err != nil || s == nil {
		t.Fatal(err)
	}
	pol, err := family.Verify(keyset.Set{srv.Pub}, *s, p.Device, 0, time.Now())
	if err != nil || pol.Version != 1 || pol.Parent != "mum@example.com" {
		t.Fatalf("policy: %+v %v", pol, err)
	}
	now := time.Now().Unix()
	rep := family.Report{At: now, Until: now + 3600, Version: 1, Apps: []family.AppUse{{App: "com.phone.calc", Name: "Calculator", MS: 60000}}, Rules: pol.Rules}
	if err := c.Report(ctx, rep); err != nil {
		t.Fatal(err)
	}
	bad := rep
	bad.Until = now + 3*86400
	var se *family.StatusError
	if err := c.Report(ctx, bad); !errors.As(err, &se) || se.Code != 400 {
		t.Fatalf("a report kept past a day: %v", err)
	}
	req, _ := srv.Locate(id)
	if st, _ := c.State(ctx); st.Locate != req {
		t.Fatalf("locate: %+v", st)
	}
	if err := c.Location(ctx, family.Location{Request: req, Lat: 31.23, Lon: 121.47, AccuracyM: 5, At: now}); err != nil {
		t.Fatal(err)
	}
	if st, _ := srv.Device(id); st.Location == nil || st.Location.Lat != 31.23 || st.Report == nil || st.Consent != family.ConsentVersion {
		t.Fatalf("server: %+v", st)
	}
	if err := srv.Unbind(id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.State(ctx); !errors.Is(err, family.ErrGone) {
		t.Fatalf("after unbinding: %v", err)
	}
	if err := c.Unbind(ctx); err != nil {
		t.Fatalf("unbinding what is gone: %v", err)
	}
	var kinds []string
	for _, d := range pushes {
		kinds = append(kinds, d.T)
	}
	if got := strings.Join(kinds, " "); got != "claimed policy locate unbound" {
		t.Fatalf("pushes: %s", got)
	}
	raw, _ := json.Marshal(pushes[1])
	if strings.Contains(string(raw), "calc") {
		t.Fatalf("a push carries the rules: %s", raw)
	}
}

// A grant verifies only with the server's key, as a grant, for its phone,
// on its day and before it expires; a reset likewise; a signature of one
// kind never passes for another.
func TestGrantAndReset(t *testing.T) {
	edPub, key, _ := ed25519.GenerateKey(rand.Reader)
	pub := keyset.Set{edPub}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_800_000_000, 0)
	today := now.Format(time.DateOnly)
	g := family.Grant{Device: "fd1", Request: "a1b2c3d4e5f6", App: "com.phone.calc", Date: today, Minutes: 20, Issued: now.Unix(), Expires: now.Add(family.GrantTTL).Unix()}
	s, err := family.SignGrant(key, g)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := family.VerifyGrant(pub, s, "fd1", today, now); err != nil || got != g {
		t.Fatalf("grant: %+v %v", got, err)
	}
	raw, _ := base64.StdEncoding.DecodeString(s.Grant)
	tampered := s
	tampered.Grant = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(raw), `"minutes":20`, `"minutes":200`, 1)))
	long := g
	long.Expires = now.Add(family.MaxGrantTTL + time.Hour).Unix()
	longS, _ := family.SignGrant(key, long)
	policy, _ := family.Sign(key, family.Policy{Device: "fd1", Version: 1, Issued: now.Unix(), Expires: now.Add(time.Hour).Unix(), Rules: rules()})
	for name, c := range map[string]struct {
		pub    keyset.Set
		s      family.SignedGrant
		device string
		today  string
		now    time.Time
		want   error
	}{
		"another key":       {keyset.Set{other}, s, "fd1", today, now, family.ErrSignature},
		"tampered":          {pub, tampered, "fd1", today, now, family.ErrSignature},
		"a policy's":        {pub, family.SignedGrant{Grant: policy.Policy, Sig: policy.Sig}, "fd1", today, now, family.ErrSignature},
		"another phone":     {pub, s, "fd2", today, now, family.ErrDevice},
		"tomorrow":          {pub, s, "fd1", now.AddDate(0, 0, 1).Format(time.DateOnly), now, family.ErrDate},
		"expired":           {pub, s, "fd1", today, now.Add(family.GrantTTL), family.ErrExpired},
		"good for too long": {pub, longS, "fd1", today, now, family.ErrExpired},
	} {
		if _, err := family.VerifyGrant(c.pub, c.s, c.device, c.today, c.now); !errors.Is(err, c.want) {
			t.Errorf("grant, %s: %v, want %v", name, err, c.want)
		}
	}
	if _, err := family.Verify(pub, family.Signed{Policy: s.Grant, Sig: s.Sig}, "fd1", 0, now); !errors.Is(err, family.ErrSignature) {
		t.Errorf("a grant taken as a policy: %v", err)
	}
	for _, bad := range []family.Grant{{Device: "fd1", Request: "x", App: "com.phone.calc", Date: today, Minutes: 5},
		{Device: "fd1", Request: "a1b2c3d4e5f6", App: "com.phone.calc", Date: today, Minutes: family.MaxGrant + 1}} {
		if _, err := family.SignGrant(key, bad); err == nil {
			t.Errorf("signed %+v", bad)
		}
	}

	r := family.Reset{Device: "fd1", ID: "r1b2c3d4e5f6", Issued: now.Unix(), Expires: now.Add(family.ResetTTL).Unix()}
	rs, err := family.SignReset(key, r)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := family.VerifyReset(pub, rs, "fd1", now); err != nil || got != r {
		t.Fatalf("reset: %+v %v", got, err)
	}
	longR := r
	longR.Expires = now.Add(family.MaxResetTTL + time.Minute).Unix()
	longRS, _ := family.SignReset(key, longR)
	for name, c := range map[string]struct {
		pub    keyset.Set
		s      family.SignedReset
		device string
		now    time.Time
		want   error
	}{
		"another key":       {keyset.Set{other}, rs, "fd1", now, family.ErrSignature},
		"a grant's":         {pub, family.SignedReset{Reset: s.Grant, Sig: s.Sig}, "fd1", now, family.ErrSignature},
		"a policy's":        {pub, family.SignedReset{Reset: policy.Policy, Sig: policy.Sig}, "fd1", now, family.ErrSignature},
		"another phone":     {pub, rs, "fd2", now, family.ErrDevice},
		"expired":           {pub, rs, "fd1", now.Add(family.ResetTTL), family.ErrExpired},
		"good for too long": {pub, longRS, "fd1", now, family.ErrExpired},
	} {
		if _, err := family.VerifyReset(c.pub, c.s, c.device, c.now); !errors.Is(err, c.want) {
			t.Errorf("reset, %s: %v, want %v", name, err, c.want)
		}
	}
	if _, err := family.VerifyGrant(pub, family.SignedGrant{Grant: rs.Reset, Sig: rs.Sig}, "fd1", today, now); !errors.Is(err, family.ErrSignature) {
		t.Errorf("a reset taken as a grant: %v", err)
	}
}

// A request is bounded: its ID, the minutes a child may ask for, the note,
// why, and the phone's day.
func TestAskCheck(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ok := family.Ask{ID: "a1b2c3d4e5f6", App: "com.phone.calc", Name: "Calculator", Minutes: 30, Note: "homework done", Why: family.WhyLimit,
		Date: now.Format(time.DateOnly), Until: now.Add(5 * time.Hour).Unix()}
	if err := ok.Check(now); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*family.Ask){
		"a short ID":      func(a *family.Ask) { a.ID = "a1" },
		"20 minutes":      func(a *family.Ask) { a.Minutes = 20 },
		"a long note":     func(a *family.Ask) { a.Note = strings.Repeat("x", family.MaxNote+1) },
		"a control char":  func(a *family.Ask) { a.Note = "a\nb" },
		"no reason":       func(a *family.Ask) { a.Why = "" },
		"not an app":      func(a *family.Ask) { a.App = "calc" },
		"a past day":      func(a *family.Ask) { a.Until = now.Unix() - 1 },
		"days ahead":      func(a *family.Ask) { a.Until = now.Add(48 * time.Hour).Unix() },
		"not a date":      func(a *family.Ask) { a.Date = "today" },
		"no name for app": func(a *family.Ask) { a.Name = strings.Repeat("n", 65) },
	} {
		a := ok
		change(&a)
		if a.Check(now) == nil {
			t.Errorf("%s: taken", name)
		}
	}
}

// The phone's client asks for more time, sees the answer and verifies the
// grant; it asks for a PIN reset, fetches the confirmed one, verifies it
// and reports it done; the server limits requests an hour; the pushes say
// only what kind of news waits.
func TestClientAsksAndResets(t *testing.T) {
	srv := familytest.New()
	var pushes []family.PushData
	srv.Push = func(token string, d family.PushData) error {
		pushes = append(pushes, d)
		return nil
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx := context.Background()
	c := &family.Client{Server: ts.URL}
	p, _ := c.Pair(ctx, family.Pair{PushToken: "tok"})
	id, _ := srv.Claim(p.Code, "mum@example.com")
	if err := c.Confirm(ctx, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	today := now.Format(time.DateOnly)
	ask := family.Ask{ID: "a1b2c3d4e5f6", App: "com.phone.calc", Name: "Calculator", Minutes: 30, Note: "homework done", Why: family.WhyLimit,
		Date: today, Until: now.Add(time.Hour).Unix()}
	if err := c.Ask(ctx, ask); err != nil {
		t.Fatal(err)
	}
	if err := c.Ask(ctx, ask); err != nil {
		t.Fatalf("the same request again: %v", err)
	}
	if qs, _ := srv.Asks(id); len(qs) != 1 || qs[0].Note != "homework done" || qs[0].State != family.AskPending {
		t.Fatalf("requests: %+v", qs)
	}
	if err := srv.Answer(id, ask.ID, true, 20); err != nil {
		t.Fatal(err)
	}
	if err := srv.Answer(id, ask.ID, false, 0); err == nil {
		t.Fatal("answered twice")
	}
	st, _ := c.State(ctx)
	answers, err := c.Asks(ctx)
	if err != nil || len(st.Answered) != 1 || st.Answered[0] != ask.ID || len(answers) != 1 || answers[0].State != family.AskGranted || answers[0].Grant == nil {
		t.Fatalf("answers: %+v %+v %v", st, answers, err)
	}
	g, err := family.VerifyGrant(keyset.Set{srv.Pub}, *answers[0].Grant, p.Device, today, time.Now())
	if err != nil || g.Request != ask.ID || g.Minutes != 20 || g.App != "com.phone.calc" {
		t.Fatalf("grant: %+v %v", g, err)
	}
	for i := 1; i < family.AsksPerHour; i++ {
		a := ask
		a.ID = fmt.Sprintf("a1b2c3d4e5f%d", i)
		if err := c.Ask(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	a := ask
	a.ID = "a1b2c3d4e5ff"
	var se *family.StatusError
	if err := c.Ask(ctx, a); !errors.As(err, &se) || se.Code != 429 || se.RetryAfter <= 0 {
		t.Fatalf("past %d an hour: %v", family.AsksPerHour, err)
	}

	if s, err := c.Reset(ctx); s != nil || err != nil {
		t.Fatalf("a reset before any: %v %v", s, err)
	}
	if err := c.AskReset(ctx, "r1b2c3d4e5f6"); err != nil {
		t.Fatal(err)
	}
	if st, _ := srv.Device(id); st.Reset != "asked" {
		t.Fatalf("reset: %+v", st)
	}
	if err := srv.ConfirmReset(id); err != nil {
		t.Fatal(err)
	}
	st, _ = c.State(ctx)
	rs, err := c.Reset(ctx)
	if err != nil || rs == nil || st.Reset != "r1b2c3d4e5f6" {
		t.Fatalf("confirmed: %+v %v %v", st, rs, err)
	}
	if r, err := family.VerifyReset(keyset.Set{srv.Pub}, *rs, p.Device, time.Now()); err != nil || r.ID != "r1b2c3d4e5f6" {
		t.Fatalf("reset: %+v %v", r, err)
	}
	if err := c.ResetDone(ctx, "r1b2c3d4e5f6"); err != nil {
		t.Fatal(err)
	}
	if st, _ := srv.Device(id); st.Reset != "done" {
		t.Fatalf("done: %+v", st)
	}
	var kinds []string
	for _, d := range pushes {
		kinds = append(kinds, d.T+":"+d.ID)
	}
	if got := strings.Join(kinds, " "); got != "claimed: answer:a1b2c3d4e5f6 reset:r1b2c3d4e5f6" {
		t.Fatalf("pushes: %s", got)
	}
}

// The phone refuses: the pairing ends; and the token goes only over HTTPS
// or to this machine and private networks.
func TestRefuseAndServerOK(t *testing.T) {
	srv := familytest.New()
	ts := httptest.NewServer(srv)
	defer ts.Close()
	ctx := context.Background()
	c := &family.Client{Server: ts.URL}
	p, _ := c.Pair(ctx, family.Pair{})
	if _, err := srv.Claim(p.Code, "eve@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := c.Confirm(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.State(ctx); !errors.Is(err, family.ErrGone) || len(srv.Devices()) != 0 {
		t.Fatalf("refused: %v %v", err, srv.Devices())
	}
	for s, ok := range map[string]bool{"https://account.example.cn": true, "http://127.0.0.1:8796": true, "http://192.168.1.5": true,
		"http://account.example.cn": false, "ftp://127.0.0.1": false, "": false} {
		if got := family.ServerOK(s) == nil; got != ok {
			t.Errorf("%q: %v", s, got)
		}
	}
}
