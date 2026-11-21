package shell

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/kario-os/kario/family"
	"github.com/kario-os/kario/ui"
)

// Asking the parent for more time, and resetting a forgotten parental PIN
// through the parent's account (19-core-apps.md §1.2, "A parent's remote
// management"; family.Ask, family.Grant, family.Reset).
//
// In minor mode the system's screen that holds an app back (sys.held)
// offers "Ask a parent": the child picks 15, 30 or 60 minutes and may add
// a note (sys.ask), and the phone sends the request with its device token.
// It keeps today's requests in family.json. The parent answers on their
// page; for a yes the server signs a grant for that request, app and day,
// and pushes. The phone takes a grant only under the product's key, for
// this phone, for a request it sent that has no answer yet (once), dated
// today on the phone and not expired, and gives the app that many minutes
// more today, past its limit and past downtime (giveGranted). Offline the
// request is not sent and nothing is queued: the screen says so. Unlinked
// there is no one to ask, and the button says that instead.
//
// A parent who forgot the parental PIN: with the phone linked, Settings ›
// Minor mode › Forgot the parental PIN asks the server, which shows it on
// the parent's page; the parent confirms after signing in afresh, and the
// server signs a reset for this phone and that request, good for
// family.ResetTTL. The phone checks it, and lets a new PIN be set once on
// the system's PIN screen before it expires; phone-keystored replaces the
// old one (parent.reset_pin). An unlinked phone has no one to confirm it.

const (
	// While a request or a reset waits, the phone looks every
	// familyWaitEvery rather than every familyEvery: the push may not
	// come.
	familyWaitEvery  = 5 * time.Minute
	familyAskNotif   = "family-ask"
	familyResetNotif = "family-reset"
	resetAsked       = "asked"
	resetReady       = "ready"
)

// familyAsk is a request for more time this phone sent, or tried to
// (Failed: it never reached the server); pending until the parent's
// answer, then granted (Granted minutes), refused or expired.
type familyAsk struct {
	family.Ask
	At      int64  `json:"at"`
	State   string `json:"state"`
	Failed  bool   `json:"failed,omitempty"`
	Granted int    `json:"granted,omitempty"`
}

// familyReset is a parental PIN reset asked for (Asked, Unix): asked
// until the parent confirms it, then ready, the server's signed reset
// kept, for a new PIN to be set once before Until.
type familyReset struct {
	ID     string              `json:"id"`
	Asked  int64               `json:"asked"`
	State  string              `json:"state"`
	Signed *family.SignedReset `json:"signed,omitempty"`
	Until  int64               `json:"until,omitempty"`
}

func familyID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// familyAsks are the binding's requests of today; those of other days go.
func (c *Core) familyAsks(rec *familyRecord) []*familyAsk {
	today := day(c.now())
	rec.Asks = slices.DeleteFunc(rec.Asks, func(a *familyAsk) bool { return a.Date != today })
	return rec.Asks
}

// familyAskOpen is the app's request the parent may still answer, nil for
// none.
func (c *Core) familyAskOpen(rec *familyRecord, app string) *familyAsk {
	for _, a := range c.familyAsks(rec) {
		if a.App == app && a.State == family.AskPending && !a.Failed && c.now().Before(time.Unix(a.At, 0).Add(family.AskTTL)) {
			return a
		}
	}
	return nil
}

// familyWaiting: a request or a reset waits for the parent.
func (c *Core) familyWaiting(rec *familyRecord) bool {
	for _, a := range c.familyAsks(rec) {
		if c.familyAskOpen(rec, a.App) != nil {
			return true
		}
	}
	return c.familyResetLive(rec) != ""
}

// familyAskRefusal says why the child cannot ask for more time with the
// app now, "" when they can.
func (c *Core) familyAskRefusal(app string) string {
	rec := c.family().file.Bound
	if rec == nil {
		return tr.T("No parent's account is linked to ask: Settings › Minor mode › Remote management")
	}
	if a := c.familyAskOpen(rec, app); a != nil {
		return tr.T("Asked at %s for %d minutes; waiting for the answer", c.clockText(time.Unix(a.At, 0)), a.Minutes)
	}
	var recent []int64
	for _, a := range c.familyAsks(rec) {
		if !a.Failed && c.now().Unix()-a.At < int64(time.Hour/time.Second) {
			recent = append(recent, a.At)
		}
	}
	if len(recent) >= family.AsksPerHour {
		next := time.Unix(recent[len(recent)-family.AsksPerHour], 0).Add(time.Hour)
		return tr.T("Asked %d times in the last hour; again at %s", len(recent), c.clockText(next))
	}
	return ""
}

// familyAskItem is the held-back screen's "Ask a parent" in minor mode;
// false when the phone has no account server to ask through.
func (c *Core) familyAskItem(a *App) (ui.Node, bool) {
	rec := c.family().file.Bound
	if rec == nil && !c.familyAvailable() {
		return ui.Node{}, false
	}
	sub := c.familyAskRefusal(a.M.ID)
	if sub == "" {
		sub = tr.T("%s answers on their phone", rec.Parent)
	}
	return ui.Node{Kind: ui.KindItem, ID: "held-ask", Title: tr.T("Ask a parent"), Subtitle: sub,
		Actions: []ui.Action{{ID: ui.ActionOpen, Label: tr.T("Ask a parent"), Primary: true}}}, true
}

// familyAskStart opens the screen asking for time, or says why not.
func (c *Core) familyAskStart(a *App, why string) {
	if msg := c.familyAskRefusal(a.M.ID); msg != "" {
		c.showToast(msg)
		return
	}
	f := c.family()
	f.askApp, f.askWhy = a.M.ID, why
	c.sys.stack = append(c.sys.stack, newView(c.familyAskScreen(a)))
}

func (c *Core) familyAskScreen(a *App) ui.Screen {
	s := ui.Screen{ID: "sys.ask", Title: tr.T("Ask a parent"), Focus: "note", Right: "ask_cancel",
		Actions: []ui.Action{{ID: "ask_cancel", Label: tr.T("Cancel")}},
		Body: []ui.Node{
			{Kind: ui.KindText, Style: ui.StyleBig, Text: a.M.Name},
			{Kind: ui.KindText, Text: tr.T("Ask %s for more time with “%s” today; they answer on their phone.", c.familyManagedBy(), a.M.Name)},
			{Kind: ui.KindInput, ID: "note", Placeholder: tr.T("A note for them (optional)")},
		}}
	for _, m := range family.AskMinutes {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: fmt.Sprint("ask-", m), Title: tr.T("%d more minutes", m),
			Actions: []ui.Action{{ID: ui.ActionOpen, Label: tr.T("Ask"), Primary: true}}})
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleDim,
		Text: tr.T("At most %d requests an hour. The answer comes as a notification.", family.AsksPerHour)})
	return s
}

// familyAskAction runs a choice of the screen asking for time.
func (c *Core) familyAskAction(v *View, node *ui.Node, act ui.Action) {
	if act.ID == "ask_cancel" {
		c.back()
		return
	}
	var m int
	if node == nil || act.ID != ui.ActionOpen {
		return
	}
	if _, err := fmt.Sscanf(node.ID, "ask-%d", &m); err != nil || !slices.Contains(family.AskMinutes, m) {
		return
	}
	f := c.family()
	a := c.appByID(f.askApp)
	if a == nil {
		c.back()
		return
	}
	c.familyAskSend(a, f.askWhy, m, v.inputs["note"])
}

// familyAskSend sends the request, kept first so that a grant for it is
// known however the sending went.
func (c *Core) familyAskSend(a *App, why string, minutes int, note string) {
	f := c.family()
	if msg := c.familyAskRefusal(a.M.ID); msg != "" {
		c.showToast(msg)
		return
	}
	if f.asking {
		c.showToast(tr.T("Busy; try again in a moment"))
		return
	}
	rec, now := f.file.Bound, c.now()
	y, mo, d := now.Date()
	note = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, note)
	ask := &familyAsk{Ask: family.Ask{ID: familyID(), App: a.M.ID, Name: cut(a.M.Name, 64), Minutes: minutes, Note: cut(strings.TrimSpace(note), family.MaxNote),
		Why: map[string]string{heldLimit: family.WhyLimit, heldDowntime: family.WhyDowntime}[why], Date: day(now),
		Until: time.Date(y, mo, d+1, 0, 0, 0, 0, now.Location()).Unix()}, At: now.Unix(), State: family.AskPending}
	rec.Asks = append(c.familyAsks(rec), ask)
	c.saveFamily()
	f.asking = true
	r, sent := *rec, ask.Ask
	c.log.Printf("family: asking the parent for %d minutes with %s", minutes, a.M.ID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), familyTimeout)
		defer cancel()
		token, err := c.familyToken(&r)
		if err == nil {
			err = (&family.Client{Server: r.Server, Token: token, HTTP: familyHTTP()}).Ask(ctx, sent)
		}
		c.Post(func() { c.familyAsked(r.Device, ask, err) })
	}()
}

func (c *Core) familyAsked(device string, ask *familyAsk, err error) {
	f := c.family()
	f.asking = false
	rec := f.file.Bound
	if rec == nil || rec.Device != device {
		return
	}
	var se *family.StatusError
	switch {
	case errors.Is(err, family.ErrGone):
		c.familyEnded(rec)
		return
	case errors.As(err, &se) && se.Code == http.StatusTooManyRequests:
		ask.Failed = true
		c.showToast(tr.T("Asked too often; try again at %s", c.clockText(c.now().Add(se.RetryAfter))))
	case err != nil:
		ask.Failed = true
		c.log.Printf("family: the request was not sent: %v", err)
		c.showToast(tr.T("Could not reach the account server; the request was not sent"))
	default:
		c.log.Printf("family: the request went to the parent")
		c.showToast(tr.T("Asked %s; the answer comes as a notification", rec.Parent))
		if wait := c.now().Add(familyWaitEvery); f.due.After(wait) {
			f.due = wait
		}
		if top := c.sys.top(); top != nil && top.S.ID == "sys.ask" {
			c.sys.stack = c.sys.stack[:len(c.sys.stack)-1]
		}
	}
	c.saveFamily()
	c.familyRefreshHeld()
}

// familyNow is the account server's clock as the phone estimates it: the
// server signs a grant or a reset good for an hour or minutes by its clock,
// and a phone's clock may be off by more. Its Date is not signed, but a
// grant or reset counts only for the request this phone sent and once.
func (c *Core) familyNow() time.Time { return c.now().Add(c.family().skew) }

// familyAnswers takes the parent's answers to requests still waiting:
// a grant that checks out gives its minutes, once; a refusal is told.
func (c *Core) familyAnswers(rec *familyRecord, answers []family.AskState) {
	key, kerr := familyKey(c.cfg.FamilyKey)
	changed := false
	for _, x := range answers {
		i := slices.IndexFunc(c.familyAsks(rec), func(a *familyAsk) bool { return a.ID == x.ID })
		if i < 0 || rec.Asks[i].State != family.AskPending {
			continue
		}
		ask := rec.Asks[i]
		switch x.State {
		case family.AskRefused:
			ask.State, changed = family.AskRefused, true
			c.log.Printf("family: the parent gave %s no more time", ask.App)
			c.familyNotifyAs(familyAskNotif, tr.T("No more time from your parent"), tr.T("%s did not give “%s” more time today.", rec.Parent, ask.Name))
		case family.AskExpired:
			ask.State, changed = family.AskExpired, true
		case family.AskGranted:
			err := kerr
			var g family.Grant
			switch {
			case err != nil:
			case x.Grant == nil:
				err = errors.New("family: a yes without a grant")
			default:
				g, err = family.VerifyGrant(key, *x.Grant, rec.Device, day(c.now()), c.familyNow())
			}
			if err == nil && (g.Request != ask.ID || g.App != ask.App) {
				err = errors.New("family: the grant answers another request")
			}
			// A grant that does not check out now never will.
			ask.State, changed = family.AskExpired, true
			if err != nil {
				c.log.Printf("family: grant refused: %v", err)
				c.family().err = tr.T("More time from the account server was refused: %v", err)
				continue
			}
			ask.State, ask.Granted = family.AskGranted, g.Minutes
			c.giveGranted(ask.App, g.Minutes)
			c.saveScreenTime()
			c.log.Printf("family: the parent gave %s %d more minutes", ask.App, g.Minutes)
			c.familyNotifyAs(familyAskNotif, tr.T("More time from your parent"), tr.T("%s gave “%s” %d more minutes today.", rec.Parent, ask.Name, g.Minutes))
			if top := c.sys.top(); top != nil && (top.S.ID == "sys.held" || top.S.ID == "sys.ask") && c.stime.held == ask.App && c.fg == nil {
				if a := c.appByID(ask.App); a != nil && c.held(a) == "" {
					c.sys.stack = c.sys.stack[:1]
					c.Launch(a, "")
				}
			}
		}
	}
	if changed {
		c.saveFamily()
		c.familyRefreshHeld()
	}
}

// familyRefreshHeld rebuilds the held-back screen, which shows the
// request's state.
func (c *Core) familyRefreshHeld() {
	for _, v := range c.sys.stack {
		if a := c.appByID(c.stime.held); v.S.ID == "sys.held" && a != nil {
			v.replace(c.heldScreen(a, c.stime.heldWhy))
		}
	}
}

// familyResetLive is the binding's PIN reset's state, "" for none; one
// the parent did not confirm in time, or not used in time, goes.
func (c *Core) familyResetLive(rec *familyRecord) string {
	r := rec.Reset
	if r == nil {
		return ""
	}
	now := c.now()
	if r.State == resetAsked && !now.Before(time.Unix(r.Asked, 0).Add(family.ResetAskTTL)) || r.State == resetReady && now.Unix() >= r.Until {
		rec.Reset = nil
		c.saveFamily()
		return ""
	}
	return r.State
}

// familyResetStep is Settings' "Forgot the parental PIN": ask the parent,
// or, the parent having confirmed, set the new PIN.
func (c *Core) familyResetStep(done func(error)) {
	f := c.family()
	rec := f.file.Bound
	switch {
	case rec == nil:
		done(errors.New(tr.T("Without a parent's account linked, a forgotten parental PIN means flashing the phone, which wipes it")))
	case c.familyResetLive(rec) == resetReady:
		c.familyResetPIN(done)
	case rec.Reset != nil:
		done(errors.New(tr.T("Already asked; waiting for %s to confirm", rec.Parent)))
	case f.asking:
		done(errors.New(tr.T("Busy; try again in a moment")))
	default:
		c.familyResetAsk(done)
	}
}

func (c *Core) familyResetAsk(done func(error)) {
	f := c.family()
	r, id := *f.file.Bound, familyID()
	f.asking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), familyTimeout)
		defer cancel()
		token, err := c.familyToken(&r)
		if err == nil {
			err = (&family.Client{Server: r.Server, Token: token, HTTP: familyHTTP()}).AskReset(ctx, id)
		}
		c.Post(func() {
			f.asking = false
			rec := f.file.Bound
			var se *family.StatusError
			switch {
			case rec == nil || rec.Device != r.Device:
				done(errors.New(tr.T("Minor mode is no longer managed remotely")))
			case errors.Is(err, family.ErrGone):
				c.familyEnded(rec)
				done(errors.New(tr.T("Minor mode is no longer managed remotely")))
			case errors.As(err, &se) && se.Code == http.StatusTooManyRequests:
				done(errors.New(tr.T("Asked too often; try again at %s", c.clockText(c.now().Add(se.RetryAfter)))))
			case err != nil:
				c.log.Printf("family: reset not asked: %v", err)
				done(errors.New(tr.T("Could not reach the account server")))
			default:
				rec.Reset = &familyReset{ID: id, Asked: c.now().Unix(), State: resetAsked}
				f.due = c.now().Add(familyWaitEvery)
				c.saveFamily()
				c.log.Printf("family: a parental PIN reset asked of the parent")
				c.showToast(tr.T("Asked %s to confirm on their page", rec.Parent))
				c.refreshSystemScreens()
				done(nil)
			}
		})
	}()
}

// familyResetReady takes the reset the parent confirmed: this phone's, the
// one asked for and not taken before, under the product's key, not
// expired.
func (c *Core) familyResetReady(rec *familyRecord, s family.SignedReset) {
	r, err := c.familyResetCheck(rec, s)
	if err == nil && (rec.Reset == nil || rec.Reset.State != resetAsked) {
		err = errors.New("family: no reset was asked for")
	}
	if err != nil {
		c.log.Printf("family: reset refused: %v", err)
		c.family().err = tr.T("A PIN reset from the account server was refused: %v", err)
		return
	}
	// Until is on the phone's clock, which may be off from the server's.
	rec.Reset.State, rec.Reset.Signed, rec.Reset.Until = resetReady, &s, time.Unix(r.Expires, 0).Add(-c.family().skew).Unix()
	c.saveFamily()
	c.log.Printf("family: the parent confirmed the parental PIN reset")
	until := c.clockText(time.Unix(rec.Reset.Until, 0))
	c.familyNotifyAs(familyResetNotif, tr.T("Set a new parental PIN"),
		tr.T("%s confirmed the reset: set the new parental PIN in Settings › Minor mode before %s.", rec.Parent, until))
	c.showToast(tr.T("%s confirmed the PIN reset", rec.Parent))
	c.refreshSystemScreens()
}

// familyResetCheck verifies a signed reset as the one rec asked for.
func (c *Core) familyResetCheck(rec *familyRecord, s family.SignedReset) (family.Reset, error) {
	key, err := familyKey(c.cfg.FamilyKey)
	var r family.Reset
	if err == nil {
		r, err = family.VerifyReset(key, s, rec.Device, c.familyNow())
	}
	if err == nil && (rec.Reset == nil || r.ID != rec.Reset.ID) {
		err = errors.New("family: the reset is not the one asked for")
	}
	return r, err
}

// familyResetValid: a ready reset still checks out (the PIN screen asks
// before the new PIN goes to phone-keystored).
func (c *Core) familyResetValid() error {
	rec := c.family().file.Bound
	if rec == nil || c.familyResetLive(rec) != resetReady || rec.Reset.Signed == nil {
		return errors.New(tr.T("The PIN reset expired; ask again"))
	}
	if _, err := c.familyResetCheck(rec, *rec.Reset.Signed); err != nil {
		return errors.New(tr.T("The PIN reset expired; ask again"))
	}
	return nil
}

// familyResetPIN sets the new parental PIN on the system's screen; the
// reset is then used up, and the server hears.
func (c *Core) familyResetPIN(done func(error)) {
	if err := c.familyResetValid(); err != nil {
		done(err)
		return
	}
	rec := c.family().file.Bound
	c.askParentPIN("reset", tr.T("%s confirmed the reset. Choose a new parental PIN; the old one stops working.", rec.Parent), func(err error) {
		if err == nil {
			c.familyResetDone()
		}
		done(err)
	})
}

func (c *Core) familyResetDone() {
	f := c.family()
	rec := f.file.Bound
	if rec == nil || rec.Reset == nil {
		return
	}
	rec.ResetDone, rec.Reset = rec.Reset.ID, nil
	f.due = c.now()
	c.saveFamily()
	c.log.Printf("family: the parental PIN was reset")
	c.familyNotifyAs(familyResetNotif, tr.T("Parental PIN reset"),
		tr.T("Set at %s after %s confirmed it from their page; the old PIN no longer works.", c.clockText(c.now()), rec.Parent))
	c.refreshSystemScreens()
}

// familyResetCancel withdraws a reset asked for; the server drops one it
// does not hear of in time anyway.
func (c *Core) familyResetCancel() {
	f := c.family()
	rec := f.file.Bound
	if rec == nil || rec.Reset == nil {
		return
	}
	rec.Reset = nil
	c.saveFamily()
	c.refreshSystemScreens()
	r := *rec
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), familyTimeout)
		defer cancel()
		token, err := c.familyToken(&r)
		if err == nil {
			err = (&family.Client{Server: r.Server, Token: token, HTTP: familyHTTP()}).CancelReset(ctx)
		}
		if err != nil {
			c.Post(func() { c.log.Printf("family: the server has not heard the reset was withdrawn: %v", err) })
		}
	}()
}
