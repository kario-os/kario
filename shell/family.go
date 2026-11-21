package shell

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/family"
	"github.com/kario-os/kario/keyset"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// A parent's remote management of minor mode (Settings › Minor mode ›
// Remote management; kario-family/1, package family; 19-core-apps.md
// §1.2). With the parental PIN, the phone pairs with the product's Kario
// account server (Config.AccountURL) and shows a one-time code; a parent
// signed in on the server's web page enters it, and the phone asks again
// before it lets that account manage it. The server then signs each set
// of rules the parent saves; the phone takes a policy only with the
// server's signature under the key the product ships (Config.FamilyKey),
// for this phone, newer than the one in force and not expired, and puts
// it into minor mode as a change made with the PIN would. A push on
// family.PushApp (no app is called that) says something is waiting; the
// phone also looks every familyEvery, as a system job, and keeps the last
// good policy whatever the network does.
//
// Precedence: binding is the PIN holder's delegation, so a policy may
// loosen the rules as well as tighten them; it never turns minor mode off
// or on, changes the PIN, adds a parent, or touches what else the PIN
// guards. A change made on the phone with the PIN after a policy was
// issued stands over that policy (the later one wins) and is reported, so
// the parent's page starts from it. Turning minor mode off, which takes
// the PIN, ends the binding; so does Unlink here (the PIN) or the parent
// on the page, and the rules stay as they were.
//
// What goes back is what the page shows: the apps and today's time on the
// screen of each, the rules in force (reported after every change), and
// the location once each time the parent asks, which the phone says in a
// notification. The device token phone-keystored seals (for
// "family:<device>"); family.json keeps the binding and the policy as
// signed, never the token in the clear. Asking the parent for more time
// and resetting a forgotten parental PIN through them are in
// family_ask.go.

const (
	familyJob     = "family-check"
	familyEvery   = 30 * time.Minute
	familyPoll    = 3 * time.Second
	familyTimeout = 30 * time.Second
	// A report goes after a change (the rules, the policy) once
	// familyReportAfter passed, and otherwise when today's times changed
	// and familyReportEvery passed since the last one.
	familyReportAfter = 5 * time.Second
	familyReportEvery = 30 * time.Minute
	familyRetryMin    = time.Minute
	familyRetryMax    = 30 * time.Minute
	// A location request waits up to familyLocateWait for a fix; one
	// familyFixFresh old answers at once.
	familyLocateWait = 45 * time.Second
	familyFixFresh   = time.Minute
	familyNotif      = "family"
	familyLocNotif   = "family-locate"
)

// familyRecord is a binding: the server it is with, the phone's ID there,
// the token sealed, the parent, and the policy in force as signed. Local:
// the rules were changed on the phone with the PIN then (Unix), after the
// policy. Asks are today's requests for more time, Reset a PIN reset under
// way and ResetDone one set that the server has not heard of yet
// (family_ask.go).
type familyRecord struct {
	Server    string         `json:"server"`
	Device    string         `json:"device"`
	Sealed    string         `json:"token_sealed"`
	Parent    string         `json:"parent"`
	Since     int64          `json:"since"`
	Version   int            `json:"version"`
	Policy    *family.Signed `json:"policy,omitempty"`
	Applied   int64          `json:"applied,omitempty"`
	Local     int64          `json:"local,omitempty"`
	Asks      []*familyAsk   `json:"asks,omitempty"`
	Reset     *familyReset   `json:"reset,omitempty"`
	ResetDone string         `json:"reset_done,omitempty"`
}

// familyFile is state/shell/family.json; Leaving are bindings ended on the
// phone that the server has not heard of yet.
type familyFile struct {
	Bound   *familyRecord   `json:"bound,omitempty"`
	Leaving []*familyRecord `json:"leaving,omitempty"`
}

// familyPairing is a code on show (in memory: a restart ends it).
type familyPairing struct {
	server, device, token, code string
	until                       time.Time
	parent                      string
}

type familyShell struct {
	loaded bool
	// skew is the account server's clock less the phone's, as its last
	// answer showed (familyNow).
	skew time.Duration
	file familyFile
	pair *familyPairing
	// busy: a request is under way; the next is due then.
	busy      bool
	due       time.Time
	leaveDue  time.Time
	failures  int
	err       string
	reportDue time.Time
	reportSum string
	reportAt  time.Time
	// locate is the parent's request being answered, until locateUntil.
	locate      string
	locateUntil time.Time
	located     string
	locatedAt   time.Time
	// asking: a request for more time or a PIN reset is being sent;
	// askApp and askWhy are what the screen asking for time (sys.ask) is
	// about.
	asking         bool
	askApp, askWhy string
	// sim is the selftest's server (family_headless.go).
	sim *familySim
}

func (c *Core) familyPath() string { return filepath.Join(c.cfg.StateDir, "shell", "family.json") }

func (c *Core) family() *familyShell {
	f := &c.parental.remote
	if !f.loaded {
		f.loaded = true
		if raw, err := os.ReadFile(c.familyPath()); err == nil {
			if err := json.Unmarshal(raw, &f.file); err != nil {
				c.log.Printf("family: %s: %v", c.familyPath(), err)
			}
		}
		if f.file.Bound != nil {
			f.due, f.reportDue = c.now(), c.now()
		}
	}
	return f
}

func (c *Core) saveFamily() {
	f := c.family()
	if f.file.Bound == nil && len(f.file.Leaving) == 0 {
		_ = os.Remove(c.familyPath())
	} else {
		raw, _ := json.MarshalIndent(f.file, "", "  ")
		if err := contacts.WriteFile(c.familyPath(), raw); err != nil {
			c.log.Printf("family: %v", err)
		}
	}
	every := time.Duration(0)
	if f.file.Bound != nil || len(f.file.Leaving) > 0 {
		every = familyEvery
	}
	c.setSystemJob(familyJob, every)
}

// familyKey is the account server's policy keys the product ships (base64
// Ed25519 public keys in a file, one a line: the one it signs with and,
// while it is rotated, the next or the replaced one).
func familyKey(path string) (keyset.Set, error) {
	k, err := keyset.Load(path)
	if path == "" || err != nil {
		return nil, fmt.Errorf("the account server's policy key (%q) is unreadable", path)
	}
	return k, nil
}

// familyAvailable: the product names an account server and its key.
func (c *Core) familyAvailable() bool {
	_, err := familyKey(c.cfg.FamilyKey)
	return c.cfg.AccountURL != "" && err == nil
}

func familyHTTP() *http.Client { return &http.Client{Timeout: familyTimeout} }

func (c *Core) familyToken(r *familyRecord) (string, error) {
	var out struct {
		Data string `json:"data"`
	}
	if err := c.broker.rpc(svc.Keystored, keystore.MethodSecretOpen, map[string]string{"purpose": "family:" + r.Device, "sealed": r.Sealed}, &out); err != nil {
		return "", fmt.Errorf("%w: %v", errKeystore, err)
	}
	raw, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		return "", errKeystore
	}
	return string(raw), nil
}

func (c *Core) familySeal(device, token string) (string, error) {
	var out struct {
		Sealed string `json:"sealed"`
	}
	args := map[string]string{"purpose": "family:" + device, "data": base64.StdEncoding.EncodeToString([]byte(token))}
	if err := c.broker.rpc(svc.Keystored, keystore.MethodSecretSeal, args, &out); err != nil || out.Sealed == "" {
		return "", fmt.Errorf("%w: %v", errKeystore, err)
	}
	return out.Sealed, nil
}

// familyPushToken is the phone's token on the family channel, "" without
// the push service (the phone then only looks by itself).
func (c *Core) familyPushToken() string {
	var tok struct {
		Token string `json:"token"`
	}
	if err := c.pushRPC(svc.PushRegister, map[string]string{"app": family.PushApp}, &tok); err != nil {
		return ""
	}
	return tok.Token
}

// familyTick runs from the shell's tick: the request due, one at a time.
func (c *Core) familyTick() {
	f := c.family()
	now := c.now()
	if f.locate != "" && !now.Before(f.locateUntil) {
		c.familyAnswer(nil)
	}
	if f.busy {
		return
	}
	switch {
	case len(f.file.Leaving) > 0 && !now.Before(f.leaveDue):
		c.familyLeaveRun()
	case f.pair != nil && !now.Before(f.pair.until):
		c.log.Printf("family: the pairing code expired")
		f.pair, f.err = nil, tr.T("The code expired; link again for a new one")
		c.refreshSystemScreens()
	case f.pair != nil && !now.Before(f.due):
		c.familyPollPairing()
	case f.file.Bound != nil && !now.Before(f.due):
		c.familyCheck()
	case f.file.Bound != nil && !f.reportDue.IsZero() && !now.Before(f.reportDue):
		c.familyReport()
	}
}

// familyDue: the job's time, or a push said something waits.
func (c *Core) familyDue() {
	f := c.family()
	f.due = c.now()
	if len(f.file.Leaving) > 0 {
		f.leaveDue = c.now()
	}
}

func (c *Core) familyPush(m push.Message) {
	var d family.PushData
	_ = json.Unmarshal([]byte(m.Data), &d)
	c.log.Printf("family: push %q", d.T)
	c.familyDue()
}

// familyFailed counts a failed request: the next after a backoff.
func (c *Core) familyFailed(err error) {
	f := c.family()
	f.failures++
	wait := min(familyRetryMax, familyRetryMin<<min(f.failures-1, 6))
	var se *family.StatusError
	if errors.As(err, &se) && se.RetryAfter > wait {
		wait = se.RetryAfter
	}
	f.due = c.now().Add(wait)
	f.err = tr.T("Could not reach the account server; trying again at %s", c.clockText(f.due))
	c.log.Printf("family: %v; again in %s", err, wait)
}

// familyPair asks the server for a pairing code to show.
func (c *Core) familyPair(done func(error)) {
	f := c.family()
	switch {
	case !c.familyAvailable():
		done(errors.New(tr.T("This phone has no account server to be managed through")))
		return
	case f.file.Bound != nil:
		done(errors.New(tr.T("Already managed by %s", f.file.Bound.Parent)))
		return
	case f.busy:
		done(errors.New(tr.T("Busy; try again in a moment")))
		return
	}
	f.busy, f.err = true, ""
	server := c.cfg.AccountURL
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), familyTimeout)
		defer cancel()
		cl := &family.Client{Server: server, HTTP: familyHTTP()}
		p, err := cl.Pair(ctx, family.Pair{PushToken: c.familyPushToken()})
		c.Post(func() {
			f.busy = false
			if err != nil {
				c.log.Printf("family: pairing: %v", err)
				done(errors.New(tr.T("Could not reach the account server")))
				return
			}
			c.log.Printf("family: pairing as %s", p.Device)
			c.familyDropPairing()
			f.pair = &familyPairing{server: server, device: p.Device, token: p.Token, code: p.Code, until: c.now().Add(time.Duration(p.ExpiresIn) * time.Second)}
			f.due = c.now().Add(familyPoll)
			c.refreshSystemScreens()
			done(nil)
		})
	}()
}

// familyPollPairing looks whether a parent entered the code.
func (c *Core) familyPollPairing() {
	f := c.family()
	p := f.pair
	f.busy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), familyTimeout)
		defer cancel()
		st, err := (&family.Client{Server: p.server, Token: p.token, HTTP: familyHTTP()}).State(ctx)
		c.Post(func() {
			f.busy = false
			if f.pair != p {
				return
			}
			f.due = c.now().Add(familyPoll)
			switch {
			case errors.Is(err, family.ErrGone):
				f.pair, f.err = nil, tr.T("The code expired; link again for a new one")
			case err != nil:
				c.log.Printf("family: pairing: %v", err)
			case st.State == family.StateClaimed && p.parent != st.Parent:
				p.parent = st.Parent
				c.log.Printf("family: a parent entered the code")
			default:
				return
			}
			c.refreshSystemScreens()
		})
	}()
}

// familyConfirm is the answer to the parent who entered the code: allowed,
// the binding starts.
func (c *Core) familyConfirm(allow bool, done func(error)) {
	f := c.family()
	p := f.pair
	if p == nil || p.parent == "" {
		done(errors.New(tr.T("No parent is waiting to be allowed")))
		return
	}
	if f.busy {
		done(errors.New(tr.T("Busy; try again in a moment")))
		return
	}
	f.busy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), familyTimeout)
		defer cancel()
		err := (&family.Client{Server: p.server, Token: p.token, HTTP: familyHTTP()}).Confirm(ctx, allow)
		sealed := ""
		if err == nil && allow {
			sealed, err = c.familySeal(p.device, p.token)
		}
		c.Post(func() {
			f.busy = false
			if f.pair == p {
				f.pair = nil
			}
			if err != nil {
				c.log.Printf("family: confirm: %v", err)
				done(errors.New(tr.T("Could not reach the account server")))
				return
			}
			if !allow {
				c.log.Printf("family: the parent was refused")
				done(nil)
				return
			}
			f.file.Bound = &familyRecord{Server: p.server, Device: p.device, Sealed: sealed, Parent: p.parent, Since: c.now().Unix()}
			f.due, f.reportDue, f.failures, f.err, f.reportSum = c.now(), c.now(), 0, "", ""
			c.saveFamily()
			c.log.Printf("family: managed now (%s)", p.device)
			c.familyNotify(tr.T("Minor mode is managed remotely"), tr.T("%s manages minor mode from their phone; Settings › Minor mode shows what they see.", p.parent))
			c.refreshSystemScreens()
			done(nil)
		})
	}()
}

// familyNews is what a check brought: the binding's state, a newer policy,
// the answers to the requests for more time, the reset the parent
// confirmed, and whether the server heard of a reset done.
type familyNews struct {
	// date is the server's clock in its last answer.
	date      time.Time
	st        family.State
	signed    *family.Signed
	answers   []family.AskState
	reset     *family.SignedReset
	resetDone string
}

// familyCheck asks the server for the binding's state, the policy when a
// newer one waits, the answers to requests still waiting, the reset when
// the parent confirmed the one asked for, and tells it of a reset done.
func (c *Core) familyCheck() {
	f := c.family()
	rec := *f.file.Bound
	waiting := map[string]bool{}
	for _, a := range c.familyAsks(f.file.Bound) {
		waiting[a.ID] = a.State == family.AskPending
	}
	resetID := ""
	if r := rec.Reset; r != nil && r.State == resetAsked {
		resetID = r.ID
	}
	f.busy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), familyTimeout)
		defer cancel()
		var n familyNews
		token, err := c.familyToken(&rec)
		if err == nil {
			cl := &family.Client{Server: rec.Server, Token: token, HTTP: familyHTTP()}
			if n.st, err = cl.State(ctx); err == nil && n.st.Version > rec.Version {
				n.signed, err = cl.Policy(ctx)
			}
			if err == nil && slices.ContainsFunc(n.st.Answered, func(id string) bool { return waiting[id] }) {
				n.answers, err = cl.Asks(ctx)
			}
			if err == nil && resetID != "" && n.st.Reset == resetID {
				n.reset, err = cl.Reset(ctx)
			}
			n.date = cl.Date
			var se *family.StatusError
			if err == nil && rec.ResetDone != "" {
				if err = cl.ResetDone(ctx, rec.ResetDone); err == nil || errors.As(err, &se) && se.Code == http.StatusConflict {
					n.resetDone, err = rec.ResetDone, nil
				}
			}
		}
		c.Post(func() { c.familyChecked(rec.Device, n, err) })
	}()
}

func (c *Core) familyChecked(device string, n familyNews, err error) {
	f := c.family()
	f.busy = false
	rec := f.file.Bound
	if rec == nil || rec.Device != device {
		return
	}
	switch {
	case errors.Is(err, family.ErrGone):
		c.familyEnded(rec)
		return
	case err != nil:
		c.familyFailed(err)
		c.refreshSystemScreens()
		return
	}
	st := n.st
	if !n.date.IsZero() {
		f.skew = n.date.Sub(c.now())
	}
	f.failures, f.err = 0, ""
	f.due = c.now().Add(familyEvery)
	if n.signed != nil {
		c.familyApply(rec, *n.signed)
	}
	if len(n.answers) > 0 {
		c.familyAnswers(rec, n.answers)
	}
	if n.reset != nil {
		c.familyResetReady(rec, *n.reset)
	}
	if n.resetDone != "" && rec.ResetDone == n.resetDone {
		rec.ResetDone = ""
		c.saveFamily()
	}
	if c.familyWaiting(rec) {
		f.due = c.now().Add(familyWaitEvery)
	}
	if st.Locate != "" && st.Locate != f.locate && st.Locate != f.located {
		c.familyLocate(st.Locate)
	}
	if st.Report {
		f.reportSum, f.reportDue = "", c.now()
	} else if f.reportDue.IsZero() || f.reportDue.After(c.now().Add(familyReportEvery)) {
		f.reportDue = c.now().Add(familyReportEvery)
	}
	c.refreshSystemScreens()
}

// familyApply puts a policy that checks out into minor mode.
func (c *Core) familyApply(rec *familyRecord, s family.Signed) {
	f := c.family()
	key, err := familyKey(c.cfg.FamilyKey)
	var p family.Policy
	if err == nil {
		p, err = family.Verify(key, s, rec.Device, rec.Version, c.now())
	}
	if err != nil {
		c.log.Printf("family: policy refused: %v", err)
		f.err = tr.T("A policy from the account server was refused: %v", err)
		return
	}
	rec.Version, rec.Policy, rec.Parent = p.Version, &s, p.Parent
	switch {
	case !c.minorOn():
		c.log.Printf("family: policy %d not applied: minor mode is off", p.Version)
	case rec.Local > p.Issued:
		c.log.Printf("family: policy %d not applied: changed on the phone with the PIN after it was issued", p.Version)
	default:
		c.familySetRules(p.Rules)
		rec.Applied, rec.Local = c.now().Unix(), 0
		c.log.Printf("family: policy %d applied", p.Version)
		c.familyNotify(tr.T("Minor mode's rules changed"), tr.T("%s changed the allowed contacts, apps, limits or downtime.", p.Parent))
	}
	c.saveFamily()
	f.reportSum, f.reportDue = "", c.now().Add(familyReportAfter)
}

// familySetRules makes the rules minor mode's and screen time's.
func (c *Core) familySetRules(r family.Rules) {
	m, st := c.minor(), c.screenTime()
	m.Contacts = []minorContact{}
	for _, x := range r.Contacts {
		m.Contacts = append(m.Contacts, minorContact{Name: x.Name, Number: x.Number})
	}
	m.Apps = slices.Clone(r.Apps)
	st.Limits = map[string]int{}
	for id, lim := range r.Limits {
		if a := c.appByID(id); a == nil || !neverHeld(a) {
			st.Limits[id] = lim
		}
	}
	if st.Downtime.On && !r.Downtime.On {
		st.Downtime.Skip = 0
	}
	st.Downtime.On, st.Downtime.Start, st.Downtime.End = r.Downtime.On, r.Downtime.Start, r.Downtime.End
	st.Downtime.Apps = slices.Clone(r.Downtime.Apps)
	c.saveMinor()
	c.saveScreenTime()
	c.minorApply()
}

// familyRules are the rules in force on the phone.
func (c *Core) familyRules() family.Rules {
	m, st := c.minor(), c.screenTime()
	r := family.Rules{Contacts: []family.Contact{}, Apps: []string{}, Limits: map[string]int{},
		Downtime: family.Downtime{On: st.Downtime.On, Start: st.Downtime.Start, End: st.Downtime.End, Apps: []string{}}}
	for _, x := range m.Contacts {
		r.Contacts = append(r.Contacts, family.Contact{Name: cut(x.Name, 64), Number: x.Number})
	}
	for _, id := range m.Apps {
		if family.ValidApp(id) {
			r.Apps = append(r.Apps, id)
		}
	}
	for id, lim := range st.Limits {
		if family.ValidApp(id) && lim >= 1 && lim <= family.MaxLimit {
			r.Limits[id] = lim
		}
	}
	for _, id := range st.Downtime.Apps {
		if family.ValidApp(id) {
			r.Downtime.Apps = append(r.Downtime.Apps, id)
		}
	}
	return r.Normalize()
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// familyLocalChange: the rules were changed on the phone with the PIN.
func (c *Core) familyLocalChange() {
	f := c.family()
	if rec := f.file.Bound; rec != nil {
		rec.Local = c.now().Unix()
		c.saveFamily()
		f.reportDue = c.now().Add(familyReportAfter)
	}
}

// familyReportNow is what the parent's page shows of the phone now.
func (c *Core) familyReportNow(rec *familyRecord) family.Report {
	c.stCount()
	now := c.now()
	today := c.screenTime().Days[day(now)]
	y, mo, d := now.Date()
	r := family.Report{At: now.Unix(), Until: time.Date(y, mo, d+1, 0, 0, 0, 0, now.Location()).Unix(), Version: rec.Version,
		Local: rec.Local, Apps: []family.AppUse{}, Rules: c.familyRules()}
	for _, a := range c.apps {
		if family.ValidApp(a.M.ID) {
			r.Apps = append(r.Apps, family.AppUse{App: a.M.ID, Name: cut(a.M.Name, 64), MS: today[a.M.ID], Fixed: neverHeld(a)})
		}
	}
	return r
}

// familyReport sends the report when it changed, in minutes (or was asked
// for: reportSum cleared).
func (c *Core) familyReport() {
	f := c.family()
	rec := *f.file.Bound
	r := c.familyReportNow(&rec)
	sumOf := r
	sumOf.At, sumOf.Until, sumOf.Apps = 0, 0, slices.Clone(r.Apps)
	for i := range sumOf.Apps {
		sumOf.Apps[i].MS /= 60000
	}
	raw, _ := json.Marshal(sumOf)
	h := sha256.Sum256(raw)
	sum := hex.EncodeToString(h[:])
	if sum == f.reportSum {
		f.reportDue = c.now().Add(familyReportEvery)
		return
	}
	f.busy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), familyTimeout)
		defer cancel()
		token, err := c.familyToken(&rec)
		if err == nil {
			r.PushToken = c.familyPushToken()
			err = (&family.Client{Server: rec.Server, Token: token, HTTP: familyHTTP()}).Report(ctx, r)
		}
		c.Post(func() {
			f.busy = false
			cur := f.file.Bound
			if cur == nil || cur.Device != rec.Device {
				return
			}
			switch {
			case errors.Is(err, family.ErrGone):
				c.familyEnded(cur)
			case err != nil:
				c.familyFailed(err)
				f.reportDue = f.due
			default:
				f.reportSum, f.reportAt, f.reportDue = sum, c.now(), c.now().Add(familyReportEvery)
			}
		})
	}()
}

// familyLocate starts answering the parent's location request: the phone
// says so, then gives the last fix if it is fresh, else waits for one.
func (c *Core) familyLocate(id string) {
	f := c.family()
	rec := f.file.Bound
	f.locate, f.locateUntil = id, c.now().Add(familyLocateWait)
	c.log.Printf("family: the parent asks where the phone is")
	if s := c.appByID(healthApp); s != nil {
		c.addNotifArg(s, ui.Notification{ID: familyLocNotif, Title: tr.T("Your location was sent"),
			Body: tr.T("%s asked where this phone is, at %s; the phone answered once.", rec.Parent, c.clockText(c.now()))}, "minor")
	}
	c.showToast(tr.T("%s asked where this phone is", rec.Parent))
	c.locInit()
	switch {
	case c.settings.LocationOff:
		c.familyAnswer(&family.Location{Error: family.LocOff})
	case !c.locAvailable():
		c.familyAnswer(&family.Location{Error: family.LocUnavailable})
	case !c.loc.lastAt.IsZero() && time.Since(c.loc.lastAt) <= familyFixFresh:
		c.familyAnswer(c.familyFix())
	default:
		c.locSync()
	}
}

// familyLocating: a location request waits for a fix (location.go keeps
// the receiver on for it).
func (c *Core) familyLocating() bool { return c.parental.remote.locate != "" }

// familyFixed: a fix came (location.go) while a request waits.
func (c *Core) familyFixed() {
	if c.familyLocating() {
		c.familyAnswer(c.familyFix())
	}
}

func (c *Core) familyFix() *family.Location {
	fx := c.loc.last
	l := &family.Location{Lat: fx.Lat, Lon: fx.Lon, AccuracyM: fx.AccuracyM, At: c.now().Add(-time.Since(c.loc.lastAt)).Unix(), Source: ui.LocationSourceGNSS}
	if fx.Network {
		l.Source = ui.LocationSourceNetwork
	}
	return l
}

// familyAnswer sends the answer to the request waiting: l, or (nil, its
// time is up) the last fix known, or none.
func (c *Core) familyAnswer(l *family.Location) {
	f := c.family()
	rec := f.file.Bound
	id := f.locate
	f.locate = ""
	c.locSync()
	if rec == nil || id == "" {
		return
	}
	if l == nil {
		l = &family.Location{Error: family.LocNoFix}
		if !c.loc.lastAt.IsZero() {
			l = c.familyFix()
		}
	}
	l.Request = id
	f.located, f.locatedAt = id, c.now()
	if l.Error == "" {
		c.loc.handed = time.Now()
	}
	c.log.Printf("family: location answered (%s)", map[bool]string{true: "a position", false: l.Error}[l.Error == ""])
	r := *rec
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), familyTimeout)
		defer cancel()
		token, err := c.familyToken(&r)
		if err == nil {
			err = (&family.Client{Server: r.Server, Token: token, HTTP: familyHTTP()}).Location(ctx, *l)
		}
		if err != nil {
			c.Post(func() { c.log.Printf("family: location not sent: %v", err) })
		}
	}()
}

// familyLeave ends the binding (or the pairing) from the phone: at once
// here, and at the server as soon as it can be reached.
func (c *Core) familyLeave() {
	f := c.family()
	c.familyDropPairing()
	rec := f.file.Bound
	if rec == nil {
		return
	}
	f.file.Bound, f.locate, f.err = nil, "", ""
	f.file.Leaving = append(f.file.Leaving, rec)
	f.leaveDue = c.now()
	c.saveFamily()
	c.log.Printf("family: unlinked on the phone (%s)", rec.Device)
	c.showToast(tr.T("No longer managed by %s", rec.Parent))
	c.refreshSystemScreens()
	c.familyRefreshHeld()
}

// familyDropPairing ends the code on show, at the server too (else it
// expires there by itself).
func (c *Core) familyDropPairing() {
	f := c.family()
	p := f.pair
	if p == nil {
		return
	}
	f.pair = nil
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), familyTimeout)
		defer cancel()
		_ = (&family.Client{Server: p.server, Token: p.token, HTTP: familyHTTP()}).Unbind(ctx)
	}()
}

// familyLeaveRun tells the server of the first binding ended here.
func (c *Core) familyLeaveRun() {
	f := c.family()
	rec := f.file.Leaving[0]
	f.busy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), familyTimeout)
		defer cancel()
		token, err := c.familyToken(rec)
		if err == nil {
			err = (&family.Client{Server: rec.Server, Token: token, HTTP: familyHTTP()}).Unbind(ctx)
		}
		c.Post(func() {
			f.busy = false
			if err != nil && !errors.Is(err, errKeystore) {
				f.failures++
				f.leaveDue = c.now().Add(min(familyRetryMax, familyRetryMin<<min(f.failures-1, 6)))
				c.log.Printf("family: the server has not heard of the unlinking yet: %v", err)
				return
			}
			f.failures = 0
			f.file.Leaving = slices.DeleteFunc(f.file.Leaving, func(r *familyRecord) bool { return r == rec })
			c.saveFamily()
			c.log.Printf("family: the server forgot %s", rec.Device)
		})
	}()
}

// familyEnded: the server no longer has the binding (the parent unlinked
// the phone, or deleted their account).
func (c *Core) familyEnded(rec *familyRecord) {
	f := c.family()
	f.file.Bound, f.locate, f.err = nil, "", ""
	c.saveFamily()
	c.log.Printf("family: the binding ended at the server (%s)", rec.Device)
	c.familyNotify(tr.T("Minor mode is no longer managed remotely"), tr.T("%s no longer manages this phone; minor mode keeps its rules.", rec.Parent))
	c.refreshSystemScreens()
	c.familyRefreshHeld()
}

func (c *Core) familyNotify(title, body string) { c.familyNotifyAs(familyNotif, title, body) }

func (c *Core) familyNotifyAs(id, title, body string) {
	if s := c.appByID(healthApp); s != nil {
		c.addNotifArg(s, ui.Notification{ID: id, Title: title, Body: body}, "minor")
	}
}

// familyStatus is settings.get's minor.remote.
type familyStatus struct {
	Available bool   `json:"available"`
	State     string `json:"state"` // none, pairing, claimed, bound
	Code      string `json:"code,omitempty"`
	URL       string `json:"url,omitempty"`
	Until     string `json:"until,omitempty"`
	Parent    string `json:"parent,omitempty"`
	Since     string `json:"since,omitempty"`
	Version   int    `json:"version,omitempty"`
	Applied   string `json:"applied,omitempty"`
	Local     string `json:"local,omitempty"`
	Located   string `json:"located,omitempty"`
	Error     string `json:"error,omitempty"`
	// Reset: a parental PIN reset asked for (at ResetAt) or ready (a new
	// PIN to be set before ResetUntil).
	Reset      string `json:"reset,omitempty"`
	ResetAt    string `json:"reset_at,omitempty"`
	ResetUntil string `json:"reset_until,omitempty"`
}

func (c *Core) familyView() familyStatus {
	f := c.family()
	v := familyStatus{Available: c.familyAvailable(), State: "none", Error: f.err}
	at := func(unix int64) string {
		t := time.Unix(unix, 0)
		if day(t) == day(c.now()) {
			return c.clockText(t)
		}
		return dateText(t) + " " + c.clockText(t)
	}
	switch {
	case f.pair != nil:
		p := f.pair
		v.State, v.Code, v.Until = family.StatePairing, p.code, c.clockText(p.until)
		v.URL = strings.TrimSuffix(p.server, "/") + "/family/#code=" + p.code
		if p.parent != "" {
			v.State, v.Parent = family.StateClaimed, p.parent
		}
	case f.file.Bound != nil:
		r := f.file.Bound
		v.State, v.Parent, v.Since, v.Version = family.StateBound, r.Parent, at(r.Since), r.Version
		if r.Applied > 0 {
			v.Applied = at(r.Applied)
		}
		if r.Local > 0 {
			v.Local = at(r.Local)
		}
		if !f.locatedAt.IsZero() {
			v.Located = at(f.locatedAt.Unix())
		}
		if v.Reset = c.familyResetLive(r); v.Reset != "" {
			v.ResetAt, v.ResetUntil = at(r.Reset.Asked), c.clockText(time.Unix(r.Reset.Until, 0))
		}
	}
	return v
}

// familyManagedBy is the parent managing the phone, "" for none.
func (c *Core) familyManagedBy() string {
	if r := c.family().file.Bound; r != nil {
		return r.Parent
	}
	return ""
}
