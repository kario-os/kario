package shell

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kario-os/kario/family"
	"github.com/kario-os/kario/family/familytest"
	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/location"
	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// famTest is minor mode on (PIN 1357) with phone-keystored's parental PIN
// and sealed secrets, and an account server in memory; down makes it
// answer 503.
type famTest struct {
	*locTest
	apps map[string]*App
	srv  *familytest.Server
	ks   *keystore.Store
	down atomic.Bool
}

func newFamTest(t *testing.T) *famTest {
	lt, apps := stCore(t)
	c := lt.c
	c.cfg.RunDir = t.TempDir()
	c.broker.clients = map[string]*wire.Client{}
	ks, err := keystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	go wire.Serve(filepath.Join(c.cfg.RunDir, svc.KeystoredSock), func(method string, params json.RawMessage) (any, error) {
		var p struct{ Old, PIN, Purpose, Data, Sealed string }
		_ = json.Unmarshal(params, &p)
		switch method {
		case keystore.MethodParentStatus:
			return ks.ParentStatus(), nil
		case keystore.MethodParentSetPIN:
			return ks.SetParentPIN(p.Old, p.PIN)
		case keystore.MethodParentClearPIN:
			return ks.ClearParentPIN(p.Old)
		case keystore.MethodParentVerify:
			return ks.VerifyParentPIN(p.PIN)
		case keystore.MethodParentResetPIN:
			return ks.ResetParentPIN(p.PIN)
		case keystore.MethodSecretSeal:
			plain, _ := base64.StdEncoding.DecodeString(p.Data)
			sealed, err := ks.SealSecret(p.Purpose, plain)
			return map[string]string{"sealed": sealed}, err
		case keystore.MethodSecretOpen:
			plain, err := ks.OpenSecret(p.Purpose, p.Sealed)
			return map[string]string{"data": base64.StdEncoding.EncodeToString(plain)}, err
		}
		return nil, errors.New("unknown method " + method)
	})
	ft := &famTest{locTest: lt, apps: apps, srv: familytest.New(), ks: ks}
	off := c.clockSkew
	ft.srv.Now = func() time.Time { return time.Now().Add(off) }
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ft.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		ft.srv.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	key := filepath.Join(t.TempDir(), "family.pub")
	_ = os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(ft.srv.Pub)+"\n"), 0o644)
	c.cfg.AccountURL, c.cfg.FamilyKey = ts.URL, key
	c.setForeground(apps[appSettings])
	ft.set(ui.SettingMinorMode, 1, "")
	enterPIN(lt, "1357")
	enterPIN(lt, "1357")
	if !c.minorOn() {
		t.Fatal("minor mode is off")
	}
	return ft
}

// set is settings.set as Settings sends it; the answer comes back when the
// change (and the request it makes) is done.
func (ft *famTest) set(key string, v float64, text string) error {
	ft.t.Helper()
	var got []error
	ft.c.minorSetting(ui.SettingArgs{Key: key, Value: v, Text: text}, func(err error) { got = append(got, err) })
	if _, ok := overlayOf[*parentPadOverlay](ft.c); ok {
		return nil
	}
	ft.pump(key, func() bool { return len(got) > 0 })
	return got[0]
}

// tick asks the server now (as a push or the job make the phone do) and
// waits for the answer.
func (ft *famTest) tick() {
	ft.t.Helper()
	f := ft.c.family()
	f.due = ft.c.now()
	ft.c.familyTick()
	ft.pump("the account server", func() bool { return !f.busy })
}

// report sends the report now and waits for it.
func (ft *famTest) report() {
	ft.t.Helper()
	f := ft.c.family()
	f.due, f.reportDue = ft.c.now().Add(time.Hour), ft.c.now()
	ft.c.familyTick()
	ft.pump("the report", func() bool { return !f.busy })
}

// bind links the phone to mum@example.com: the code, the parent's claim,
// the phone's allowing it.
func (ft *famTest) bind() string {
	ft.t.Helper()
	c, f := ft.c, ft.c.family()
	if err := ft.set(ui.SettingParentLink, 1, ""); err != nil || f.pair == nil || len(f.pair.code) != family.CodeLen {
		ft.t.Fatalf("link: %v", err)
	}
	if v := c.familyView(); v.State != family.StatePairing || !strings.HasSuffix(v.URL, "/family/#code="+f.pair.code) {
		ft.t.Fatalf("pairing: %+v", v)
	}
	id, err := ft.srv.Claim(f.pair.code, "mum@example.com")
	if err != nil {
		ft.t.Fatal(err)
	}
	f.due = c.now()
	ft.tick()
	if v := c.familyView(); v.State != family.StateClaimed || v.Parent != "mum@example.com" {
		ft.t.Fatalf("claimed: %+v", v)
	}
	if err := ft.set(ui.SettingParentConfirm, 1, ""); err != nil || f.file.Bound == nil {
		ft.t.Fatalf("allow: %v", err)
	}
	if st, _ := ft.srv.Device(id); st.State != family.StateBound || st.Consent != family.ConsentVersion {
		ft.t.Fatalf("server: %+v", st)
	}
	return id
}

func famRules() family.Rules {
	return family.Rules{Contacts: []family.Contact{{Name: "Mum", Number: "13900001111"}}, Apps: []string{"com.phone.calc"},
		Limits: map[string]int{"com.phone.calc": 15, appDialer: 5}, Downtime: family.Downtime{On: true, Start: 21 * 60, End: 7 * 60, Apps: []string{appMessages}}}
}

// Binding takes the PIN and the phone's own yes; the parent's signed rules
// then become minor mode's and screen time's (never the phone's own
// limit: it is never held back), the phone reports them with today's
// times, and the token is kept only sealed.
func TestFamilyBindAndPolicy(t *testing.T) {
	ft := newFamTest(t)
	c, f := ft.c, ft.c.family()
	id := ft.bind()
	raw, _ := os.ReadFile(c.familyPath())
	// Base64url has "_": the sealed token may hold TokenPrefix by chance.
	token, err := ft.ks.OpenSecret("family:"+f.file.Bound.Device, f.file.Bound.Sealed)
	if err != nil || !strings.HasPrefix(string(token), family.TokenPrefix) {
		t.Fatalf("the token sealed: %q %v", token, err)
	}
	if !strings.Contains(string(raw), `"token_sealed": "s1.`) || strings.Contains(string(raw), string(token)) {
		t.Fatalf("family.json: %s", raw)
	}
	if _, err := ft.srv.SetRules(id, famRules()); err != nil {
		t.Fatal(err)
	}
	c.ChargeScreenTime("com.phone.calc", 3*time.Minute)
	ft.tick()
	m, st := c.minor(), c.screenTime()
	if !c.minorOn() || len(m.Contacts) != 1 || m.Contacts[0].Number != "13900001111" || c.minorHidden(ft.apps["com.phone.calc"]) || !c.minorHidden(ft.apps[appMessages]) {
		t.Fatalf("minor mode: %+v", m)
	}
	if st.Limits["com.phone.calc"] != 15 || st.Limits[appDialer] != 0 || !st.Downtime.On || st.Downtime.Start != 21*60 || f.file.Bound.Version != 1 {
		t.Fatalf("screen time: %+v %+v", st.Limits, st.Downtime)
	}
	ft.report()
	rep := func() *family.Report { s, _ := ft.srv.Device(id); return s.Report }
	if r := rep(); r == nil || r.Version != 1 || r.Rules.Limits["com.phone.calc"] != 15 || len(r.Apps) != len(c.apps) {
		t.Fatalf("report: %+v", r)
	}
	for _, a := range rep().Apps {
		if a.App == "com.phone.calc" && a.MS < 3*60000 {
			t.Fatalf("calculator's time: %+v", a)
		}
	}
	if v := c.familyView(); v.State != family.StateBound || v.Version != 1 || v.Applied == "" || c.minorView()["remote"].(familyStatus).Parent != "mum@example.com" {
		t.Fatalf("view: %+v", v)
	}
}

// A policy the phone must not take changes nothing: another key's, another
// phone's, an old one again, an expired one; and a push on the family
// channel is the Shell's, never an app's.
func TestFamilyRefusesPolicies(t *testing.T) {
	ft := newFamTest(t)
	c, f := ft.c, ft.c.family()
	id := ft.bind()
	ft.srv.SetRules(id, famRules())
	ft.tick()
	rec := f.file.Bound
	old := *rec.Policy
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	now := c.now()
	loose := family.Rules{Apps: []string{"com.phone.snake"}, Limits: map[string]int{}}
	sign := func(key ed25519.PrivateKey, device string, version int, issued time.Time) family.Signed {
		s, err := family.Sign(key, family.Policy{Device: device, Version: version, Issued: issued.Unix(), Expires: issued.Add(family.PolicyTTL).Unix(), Parent: "eve@example.com", Rules: loose})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	for name, s := range map[string]family.Signed{
		"another key":   sign(other, id, 2, now),
		"another phone": sign(ft.srv.Key, "fd0000", 2, now),
		"replayed":      old,
		"expired":       sign(ft.srv.Key, id, 2, now.Add(-family.PolicyTTL-time.Hour)),
	} {
		f.err = ""
		c.familyApply(rec, s)
		if c.minorHidden(ft.apps["com.phone.calc"]) || !c.minorHidden(ft.apps["com.phone.snake"]) || rec.Version != 1 || rec.Parent != "mum@example.com" || f.err == "" {
			t.Fatalf("%s: taken (version %d, %q)", name, rec.Version, f.err)
		}
	}
	f.due = c.now().Add(time.Hour)
	c.pushMsg(push.Message{ID: "m1", App: family.PushApp, Data: `{"t":"policy","v":2}`})
	if f.due.After(c.now()) {
		t.Fatal("the push did not make the phone look")
	}
}

// A change on the phone with the PIN after a policy was issued stands over
// that policy and is reported; a policy issued later replaces it.
func TestFamilyLocalChangeWins(t *testing.T) {
	ft := newFamTest(t)
	c, f := ft.c, ft.c.family()
	id := ft.bind()
	ft.srv.SetRules(id, famRules())
	ft.tick()
	now := ft.srv.Now
	ft.srv.Now = func() time.Time { return now().Add(-time.Hour) }
	r := famRules()
	r.Limits["com.phone.calc"] = 60
	ft.srv.SetRules(id, r)
	ft.srv.Now = now
	if err := ft.set(ui.SettingMinorApp, 1, "com.phone.snake"); err != nil || f.file.Bound.Local == 0 {
		t.Fatalf("changed on the phone: %v", err)
	}
	ft.tick()
	if c.screenTime().Limits["com.phone.calc"] != 15 || c.minorHidden(ft.apps["com.phone.snake"]) || f.file.Bound.Version != 2 {
		t.Fatalf("an older policy took over the phone's change: %v", c.screenTime().Limits)
	}
	ft.report()
	if s, _ := ft.srv.Device(id); s.Report == nil || s.Report.Local == 0 || !strings.Contains(strings.Join(s.Report.Rules.Apps, " "), "com.phone.snake") {
		t.Fatalf("report: %+v", s.Report)
	}
	ft.srv.SetRules(id, r)
	ft.tick()
	if c.screenTime().Limits["com.phone.calc"] != 60 || !c.minorHidden(ft.apps["com.phone.snake"]) || f.file.Bound.Local != 0 {
		t.Fatal("a newer policy not applied")
	}
}

// Offline the phone keeps the last good policy and tries again later; the
// parent's unbinding ends the binding and minor mode keeps its rules; minor
// mode off (the PIN) ends it from the phone, told to the server when it
// can be reached.
func TestFamilyOfflineAndUnbinding(t *testing.T) {
	ft := newFamTest(t)
	c, f := ft.c, ft.c.family()
	id := ft.bind()
	ft.srv.SetRules(id, famRules())
	ft.tick()
	ft.down.Store(true)
	ft.tick()
	if f.err == "" || !f.due.After(c.now()) || c.screenTime().Limits["com.phone.calc"] != 15 || f.file.Bound == nil {
		t.Fatalf("offline: %q", f.err)
	}
	ft.down.Store(false)
	ft.srv.Unbind(id)
	f.due = c.now()
	ft.tick()
	if f.file.Bound != nil || !c.minorOn() || c.screenTime().Limits["com.phone.calc"] != 15 || len(c.minor().Contacts) != 1 {
		t.Fatal("after the parent unbound")
	}
	if _, err := os.Stat(c.familyPath()); !os.IsNotExist(err) {
		t.Fatalf("family.json stays: %v", err)
	}
	notified := false
	for _, n := range c.notifs {
		notified = notified || n.n.ID == familyNotif && strings.Contains(n.n.Body, "mum@example.com")
	}
	if !notified {
		t.Fatal("no notification of the end")
	}

	id = ft.bind()
	ft.down.Store(true)
	ft.set(ui.SettingMinorMode, 0, "")
	enterPIN(ft.locTest, "1357")
	if c.minorOn() || f.file.Bound != nil || len(f.file.Leaving) != 1 {
		t.Fatal("minor mode off did not end the binding")
	}
	ft.c.familyTick()
	ft.pump("the unlinking", func() bool { return !f.busy })
	if len(f.file.Leaving) != 1 {
		t.Fatal("forgotten before the server heard")
	}
	ft.down.Store(false)
	f.leaveDue = c.now()
	ft.c.familyTick()
	ft.pump("the unlinking", func() bool { return !f.busy })
	if _, ok := ft.srv.Device(id); ok || len(f.file.Leaving) != 0 {
		t.Fatal("the server still holds the binding")
	}
}

// The parent asks where the phone is: the phone says so in a notification,
// turns the receiver on and answers once with the fix; with location off it
// answers so.
func TestFamilyLocate(t *testing.T) {
	ft := newFamTest(t)
	c, f := ft.c, ft.c.family()
	id := ft.bind()
	ft.tick()
	req, _ := ft.srv.Locate(id)
	ft.tick()
	if f.locate != req || !c.loc.running {
		t.Fatalf("not locating: %q, receiver %v", f.locate, c.loc.running)
	}
	notice := false
	for _, n := range c.notifs {
		notice = notice || n.n.ID == familyLocNotif && strings.Contains(n.n.Body, "mum@example.com")
	}
	if !notice {
		t.Fatal("no notice to the child")
	}
	ft.fix(location.Fix{Lat: 31.2304, Lon: 121.4737, AccuracyM: 8})
	ft.pump("the answer", func() bool { s, _ := ft.srv.Device(id); return s.Location != nil })
	if s, _ := ft.srv.Device(id); s.Location.Lat != 31.2304 || s.Location.Request != req || f.locate != "" {
		t.Fatalf("location: %+v", s.Location)
	}
	c.settings.LocationOff = true
	req, _ = ft.srv.Locate(id)
	ft.tick()
	ft.pump("the answer", func() bool { s, _ := ft.srv.Device(id); return s.Location.Request == req })
	if s, _ := ft.srv.Device(id); s.Location.Error != family.LocOff {
		t.Fatalf("location off: %+v", s.Location)
	}
}

// heldChoose runs the held-back (or asking) screen's item as Enter would.
func heldChoose(t *testing.T, c *Core, id string) {
	t.Helper()
	v := c.sys.top()
	i := v.indexOf(id)
	if i < 0 {
		t.Fatalf("%s: no %s on %s", t.Name(), id, v.S.ID)
	}
	n := v.S.Body[i]
	c.systemAction(v, &n, n.Actions[0])
}

func heldItem(c *Core, id string) ui.Node {
	if v := c.sys.top(); v != nil && v.indexOf(id) >= 0 {
		return v.S.Body[v.indexOf(id)]
	}
	return ui.Node{}
}

func notified(c *Core, id, text string) bool {
	for _, n := range c.notifs {
		if n.n.ID == id && strings.Contains(n.n.Title+" "+n.n.Body, text) {
			return true
		}
	}
	return false
}

// The screen holding an app back offers to ask the parent: unlinked it
// says there is no one to ask; linked, the child picks the minutes and
// adds a note, the request goes with the device token, and asking again
// waits for the answer. The parent's yes, signed for that request, gives
// the minutes today, once, and opens the app; a grant that is not this
// phone's, not for a request it sent, not for that app, not today's or
// not the server's gives nothing; a no is told; offline nothing is sent
// and nothing counts; the phone keeps to the requests an hour.
// A phone whose clock is hours off still takes a grant: its time is
// checked against the account server's clock (its answers' Date), not the
// phone's. At 22:30 on the server the phone, 2 hours ahead, is already in
// its next day, and its day still ends within the server's day.
func TestFamilyGrantWithAClockOff(t *testing.T) {
	ft := newFamTest(t)
	c, f := ft.c, ft.c.family()
	now := c.now()
	y, mo, d := now.Date()
	off := time.Date(y, mo, d, 22, 30, 0, 0, now.Location()).Sub(now)
	ft.srv.Now = func() time.Time { return time.Now().Add(off) }
	c.clockSkew = off
	calc := ft.apps["com.phone.calc"]
	id := ft.bind()
	c.clockSkew = off + 2*time.Hour
	ft.tick()
	c.screenTime().Limits[calc.M.ID] = 15
	c.ChargeScreenTime(calc.M.ID, 16*time.Minute)
	c.Launch(calc, "")
	heldChoose(t, c, "held-ask")
	heldChoose(t, c, "ask-30")
	ft.pump("the request", func() bool { return !f.asking })
	qs, _ := ft.srv.Asks(id)
	if len(qs) != 1 {
		t.Fatalf("asked: %+v", qs)
	}
	if err := ft.srv.Answer(id, qs[0].ID, true, 20); err != nil {
		t.Fatal(err)
	}
	ft.tick()
	if c.held(calc) != "" || c.screenTime().More[calc.M.ID] < 20 {
		t.Fatalf("the grant with the clock 2 hours ahead: held %q, more %v", c.held(calc), c.screenTime().More)
	}
}

func TestFamilyAskForMoreTime(t *testing.T) {
	defer i18n.SetLang(i18n.Lang())
	i18n.SetLang(i18n.English)
	ft := newFamTest(t)
	c, f := ft.c, ft.c.family()
	calc := ft.apps["com.phone.calc"]
	id := ft.bind()
	c.screenTime().Limits[calc.M.ID] = 15
	c.ChargeScreenTime(calc.M.ID, 16*time.Minute)
	c.Launch(calc, "")
	if sub := heldItem(c, "held-ask").Subtitle; c.fg != nil || c.sysTop() != "sys.held" || !strings.Contains(sub, "mum@example.com answers") {
		t.Fatalf("linked: %q, %q", c.sysTop(), sub)
	}
	heldChoose(t, c, "held-ask")
	if c.sysTop() != "sys.ask" {
		t.Fatalf("no screen to ask on: %q", c.sysTop())
	}
	c.sys.top().inputs["note"] = "homework\ndone"
	heldChoose(t, c, "ask-30")
	ft.pump("the request", func() bool { return !f.asking })
	qs, _ := ft.srv.Asks(id)
	if c.sysTop() != "sys.held" || len(qs) != 1 || qs[0].Minutes != 30 || qs[0].Note != "homework done" || qs[0].App != calc.M.ID || qs[0].Why != family.WhyLimit {
		t.Fatalf("asked: %q %+v", c.sysTop(), qs)
	}
	if sub := heldItem(c, "held-ask").Subtitle; !strings.Contains(sub, "waiting for the answer") {
		t.Fatalf("after asking: %q", sub)
	}
	heldChoose(t, c, "held-ask")
	if qs, _ := ft.srv.Asks(id); len(qs) != 1 || c.sysTop() != "sys.held" {
		t.Fatal("asked twice for the same app")
	}

	if err := ft.srv.Answer(id, qs[0].ID, true, 20); err != nil {
		t.Fatal(err)
	}
	ft.tick()
	if c.held(calc) != "" || c.fg != calc || c.screenTime().More[calc.M.ID] < 20 || !notified(c, familyAskNotif, "20 more minutes") {
		t.Fatalf("granted: held %q, fg %v, more %v", c.held(calc), c.fg, c.screenTime().More)
	}
	answers := []family.AskState{{ID: qs[0].ID, State: family.AskGranted, Minutes: 20, Grant: qs[0].Signed}}
	more := c.screenTime().More[calc.M.ID]
	c.familyAnswers(f.file.Bound, answers)
	if c.screenTime().More[calc.M.ID] != more {
		t.Fatal("a grant taken twice")
	}

	now, today := c.now(), day(c.now())
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	for name, g := range map[string]struct {
		key     ed25519.PrivateKey
		device  string
		request string
		app     string
		date    string
	}{
		"another key":     {other, id, "", calc.M.ID, today},
		"another phone":   {ft.srv.Key, "fd0000", "", calc.M.ID, today},
		"another request": {ft.srv.Key, id, "a0a0a0a0a0a0", calc.M.ID, today},
		"another app":     {ft.srv.Key, id, "", "com.phone.snake", today},
		"yesterday":       {ft.srv.Key, id, "", calc.M.ID, day(now.AddDate(0, 0, -1))},
	} {
		ask := &familyAsk{Ask: family.Ask{ID: familyID(), App: calc.M.ID, Minutes: 15, Date: today}, At: now.Add(-2 * time.Hour).Unix(), State: family.AskPending}
		f.file.Bound.Asks = append(f.file.Bound.Asks, ask)
		req := g.request
		if req == "" {
			req = ask.ID
		}
		s, err := family.SignGrant(g.key, family.Grant{Device: g.device, Request: req, App: g.app, Date: g.date, Minutes: 200, Issued: now.Unix(), Expires: now.Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		f.err = ""
		c.familyAnswers(f.file.Bound, []family.AskState{{ID: ask.ID, State: family.AskGranted, Minutes: 200, Grant: &s}})
		if ask.State == family.AskGranted || c.screenTime().More[calc.M.ID] != more || f.err == "" {
			t.Errorf("%s: taken (%s, more %d)", name, ask.State, c.screenTime().More[calc.M.ID])
		}
	}

	c.ChargeScreenTime(calc.M.ID, 25*time.Minute)
	c.Launch(calc, "")
	if c.sysTop() != "sys.held" {
		t.Fatal("the granted minutes do not end")
	}
	heldChoose(t, c, "held-ask")
	heldChoose(t, c, "ask-15")
	ft.pump("the request", func() bool { return !f.asking })
	qs, _ = ft.srv.Asks(id)
	ft.srv.Answer(id, qs[1].ID, false, 0)
	ft.tick()
	if c.held(calc) != heldLimit || !notified(c, familyAskNotif, "did not give") {
		t.Fatalf("refused: held %q", c.held(calc))
	}

	ft.down.Store(true)
	heldChoose(t, c, "held-ask")
	heldChoose(t, c, "ask-15")
	ft.pump("the request", func() bool { return !f.asking })
	if last := f.file.Bound.Asks[len(f.file.Bound.Asks)-1]; !last.Failed || c.sysTop() != "sys.ask" || !strings.Contains(c.toast.body, "not sent") {
		t.Fatalf("offline: %+v, %q", last, c.sysTop())
	}
	ft.down.Store(false)
	c.back()
	for len(f.file.Bound.Asks) < 12 {
		f.file.Bound.Asks = append(f.file.Bound.Asks, &familyAsk{Ask: family.Ask{ID: familyID(), App: "com.phone.snake", Date: today}, At: now.Unix(), State: family.AskRefused})
	}
	if msg := c.familyAskRefusal(calc.M.ID); !strings.Contains(msg, "in the last hour") {
		t.Fatalf("past %d an hour: %q", family.AsksPerHour, msg)
	}

	ft.srv.Unbind(id)
	ft.tick()
	if sub := heldItem(c, "held-ask").Subtitle; c.sysTop() != "sys.held" || !strings.Contains(sub, "Remote management") {
		t.Fatalf("unlinked: %q, %q", c.sysTop(), sub)
	}
	heldChoose(t, c, "held-ask")
	if c.sysTop() != "sys.held" || c.toast == nil || !strings.Contains(c.toast.body, "No parent's account") {
		t.Fatalf("asked with no one to ask: %q", c.sysTop())
	}
}

// A grant lets the app past downtime for its minutes, counted on the
// screen from now.
func TestFamilyGrantPastDowntime(t *testing.T) {
	ft := newFamTest(t)
	c, f := ft.c, ft.c.family()
	calc := ft.apps["com.phone.calc"]
	id := ft.bind()
	st := c.screenTime()
	st.Downtime.Until = dayMinute(c.now(), st.Downtime.End).UnixMilli()
	c.Launch(calc, "")
	if c.held(calc) != heldDowntime || c.sysTop() != "sys.held" {
		t.Fatalf("not held in downtime: %q", c.held(calc))
	}
	heldChoose(t, c, "held-ask")
	heldChoose(t, c, "ask-15")
	ft.pump("the request", func() bool { return !f.asking })
	qs, _ := ft.srv.Asks(id)
	if len(qs) != 1 || qs[0].Why != family.WhyDowntime {
		t.Fatalf("asked: %+v", qs)
	}
	ft.srv.Answer(id, qs[0].ID, true, 15)
	ft.tick()
	if c.held(calc) != "" || c.fg != calc || !st.Downtime.active(c.now()) {
		t.Fatalf("granted in downtime: %q", c.held(calc))
	}
	c.ChargeScreenTime(calc.M.ID, 16*time.Minute)
	if c.held(calc) != heldDowntime {
		t.Fatal("the pass does not end")
	}
}

// A parent who forgot the PIN: only a linked phone can ask; the parent's
// signed reset, for this phone and the reset asked for, under the
// server's key and in time, lets a new PIN be set once on the system's
// screen; phone-keystored replaces the old one and the server hears. A
// reset that expires before the new PIN is set changes nothing.
// A phone whose clock is hours off still takes the reset the parent
// confirmed and keeps it for its ten minutes on its own clock.
func TestFamilyResetWithAClockOff(t *testing.T) {
	ft := newFamTest(t)
	c := ft.c
	id := ft.bind()
	c.clockSkew = 2 * time.Hour
	if err := ft.set(ui.SettingParentReset, 1, ""); err != nil {
		t.Fatal(err)
	}
	if err := ft.srv.ConfirmReset(id); err != nil {
		t.Fatal(err)
	}
	ft.tick()
	if c.familyView().Reset != resetReady {
		t.Fatalf("the reset with the clock 2 hours ahead: %+v", c.familyView())
	}
	c.setForeground(ft.apps[appSettings])
	ft.set(ui.SettingParentReset, 1, "")
	enterPIN(ft.locTest, "2468")
	enterPIN(ft.locTest, "2468")
	if r, _ := ft.ks.VerifyParentPIN("2468"); !r.OK {
		t.Fatal("the new PIN did not take")
	}
}

func TestFamilyPINReset(t *testing.T) {
	if !alone(t) {
		return
	}
	defer i18n.SetLang(i18n.Lang())
	i18n.SetLang(i18n.English)
	ft := newFamTest(t)
	c, f := ft.c, ft.c.family()
	if err := ft.set(ui.SettingParentReset, 1, ""); err == nil || !strings.Contains(err.Error(), "flashing") {
		t.Fatalf("unlinked: %v", err)
	}
	id := ft.bind()
	if err := ft.set(ui.SettingParentReset, 1, ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := ft.srv.Device(id); st.Reset != "asked" || c.familyView().Reset != resetAsked {
		t.Fatalf("asked: %+v %+v", st, c.familyView())
	}
	if err := ft.set(ui.SettingParentReset, 1, ""); err == nil {
		t.Fatal("asked twice")
	}
	rec := f.file.Bound
	now := c.now()
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	for name, r := range map[string]struct {
		key        ed25519.PrivateKey
		device, id string
		issued     time.Time
	}{
		"another key":   {other, id, rec.Reset.ID, now},
		"another phone": {ft.srv.Key, "fd0000", rec.Reset.ID, now},
		"another reset": {ft.srv.Key, id, "r0r0r0r0r0r0", now},
		"expired":       {ft.srv.Key, id, rec.Reset.ID, now.Add(-family.ResetTTL - time.Minute)},
	} {
		s, _ := family.SignReset(r.key, family.Reset{Device: r.device, ID: r.id, Issued: r.issued.Unix(), Expires: r.issued.Add(family.ResetTTL).Unix()})
		f.err = ""
		c.familyResetReady(rec, s)
		if rec.Reset.State != resetAsked || f.err == "" {
			t.Errorf("%s: taken", name)
		}
	}
	if err := ft.srv.ConfirmReset(id); err != nil {
		t.Fatal(err)
	}
	ft.tick()
	if c.familyView().Reset != resetReady || !notified(c, familyResetNotif, "mum@example.com confirmed") {
		t.Fatalf("confirmed: %+v", c.familyView())
	}
	c.setForeground(ft.apps[appSettings])
	ft.set(ui.SettingParentReset, 1, "")
	o, ok := overlayOf[*parentPadOverlay](c)
	if !ok || o.op != "reset" || o.step != 1 {
		t.Fatal("no screen for the new PIN")
	}
	enterPIN(ft.locTest, "2468")
	enterPIN(ft.locTest, "2468")
	noPINScreen(t, c)
	if r, _ := ft.ks.VerifyParentPIN("1357"); r.OK {
		t.Fatal("the old PIN still works")
	}
	if r, _ := ft.ks.VerifyParentPIN("2468"); !r.OK {
		t.Fatal("the new PIN does not work")
	}
	if rec.Reset != nil || rec.ResetDone == "" || !c.minorOn() || !notified(c, familyResetNotif, "the old PIN no longer works") {
		t.Fatalf("after the reset: %+v", rec)
	}
	ft.tick()
	if st, _ := ft.srv.Device(id); st.Reset != "done" || rec.ResetDone != "" {
		t.Fatalf("the server: %+v, %q", st, rec.ResetDone)
	}

	ft.set(ui.SettingParentReset, 1, "")
	ft.srv.ConfirmReset(id)
	ft.tick()
	ft.set(ui.SettingParentReset, 1, "")
	enterPIN(ft.locTest, "3579")
	c.clockSkew += family.ResetTTL
	enterPIN(ft.locTest, "3579")
	noPINScreen(t, c)
	if r, _ := ft.ks.VerifyParentPIN("2468"); !r.OK || c.familyView().Reset != "" {
		t.Fatalf("an expired reset changed the PIN (%+v)", c.familyView())
	}
}

// Without the product's server and key there is nothing to link.
func TestFamilyUnavailable(t *testing.T) {
	ft := newFamTest(t)
	ft.c.cfg.FamilyKey = ""
	if err := ft.set(ui.SettingParentLink, 1, ""); err == nil || ft.c.familyView().Available {
		t.Fatalf("linked without a key: %v", err)
	}
}
