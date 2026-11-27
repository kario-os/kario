package shell

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// A call in the background (19-core-apps.md §5): the call screen is the
// dialer's page and stays its top page while the call goes on. Back, the
// right soft key (ui.ActionHide) and the system's other ways out put it in
// the background; the status bar shows the call on every other screen, and
// the call key, the command palette, the notification centre and the lock
// screen bring the call screen back. The end key ends the call wherever the
// phone is, as Nokia's red key does.

// liveCell is the call the call screen is about: the connected one, else
// one being set up, else a held one; nil when the user has none (a call
// still ringing in is the system's incoming screen).
func (c *Core) liveCell() *ui.TelCall {
	var best *ui.TelCall
	rank := map[string]int{"held": 1, "dialing": 2, "alerting": 2, "active": 3}
	for _, call := range c.cells {
		if r := rank[call.State]; r > 0 && (best == nil || r > rank[best.State] || r == rank[best.State] && call.ID > best.ID) {
			call := call
			best = &call
		}
	}
	return best
}

// cellBlocked says why no second call can be placed now, "" when one can
// (the connected call is then put on hold).
func (c *Core) cellBlocked() string {
	connected := 0
	for _, call := range c.cells {
		switch call.State {
		case "dialing", "alerting":
			return tr.T("The call is still connecting")
		case "incoming":
			return tr.T("A call is ringing")
		case "active", "held":
			if call.Emergency {
				return tr.T("An emergency call is in progress")
			}
			connected++
		}
	}
	if connected >= 2 {
		return tr.T("Two calls already")
	}
	return ""
}

// callBlockedHint is what digits typed away from the call say when no
// second call can go out: why, and the way back.
func (c *Core) callBlockedHint(why string) string {
	return tr.T("%s: press Call to go back to the call", why)
}

// returnToCall brings the screen of a call in progress to the front (from
// the secure lock screen too, without unlocking: only the call screen is
// reachable from there, lockedCallKey). False without a call.
func (c *Core) returnToCall() bool {
	for _, ci := range c.calls {
		if ci.app == nil {
			continue
		}
		if !ci.onCallScreen(c) {
			ci.app.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventCallResume, Value: ci.id})
		}
		c.Launch(ci.app, "")
		return true
	}
	call := c.liveCell()
	if call == nil {
		return false
	}
	if d := c.appByID(appDialer); d != nil && !c.cellOnScreen() {
		c.Launch(d, "call:"+call.ID)
	}
	return true
}

// callScreenInFront: a call's own screen is what shows.
func (c *Core) callScreenInFront() bool {
	if c.cellOnScreen() {
		return true
	}
	for _, ci := range c.calls {
		if c.fg == ci.app && ci.onCallScreen(c) {
			return true
		}
	}
	return false
}

// hideApp is ui.ActionHide: the app goes to the background with its page,
// and what was in front before it comes back. Locked, the call screen is all
// there is to show.
func (c *Core) hideApp(a *App) {
	if c.fg != a || c.secureLocked() {
		return
	}
	if p := a.cameFrom; p != nil && p != a && p.running && c.appByID(p.M.ID) == p {
		c.setForeground(p)
		return
	}
	c.setForeground(nil)
	if top := c.sys.top(); top != nil && systemRebuilt(top.S.ID) {
		top.replace(c.systemScreen(top.S.ID))
	}
}

// hangupCellCall is the end key (or the power key, by) away from the
// call screen: it ends the call in progress (the connected one, else the
// one being set up).
func (c *Core) hangupCellCall(by string) bool {
	call := c.liveCell()
	if call == nil {
		return false
	}
	c.log.Printf("%s: hanging up %s", by, call.ID)
	id := call.ID
	c.broker.call(svc.Telephonyd, svc.TelHangup, ui.CallIDArgs{CallID: id}, nil, func(err error) {
		if err != nil {
			c.showToast(tr.T("Hanging up failed: %v", err))
		}
	})
	return true
}

// cellStatus is the status bar's call indicator: the connected call's
// time, or that the call is being set up or on hold.
func (c *Core) cellStatus() string {
	call := c.liveCell()
	switch {
	case call == nil:
		return ""
	case call.State == "active":
		return tr.T("● Call %s", clock(time.Since(time.Unix(call.Connected, 0))))
	case call.State == "held":
		return tr.T("● On hold %s", clock(time.Since(time.Unix(call.Connected, 0))))
	}
	return tr.T("● Calling…")
}

// cellNotifNodes are the notification centre's rows for the call in
// progress, above the notifications: OK goes back to it, e hangs up.
func (c *Core) cellNotifNodes() []ui.Node {
	call := c.liveCell()
	d := c.appByID(appDialer)
	if call == nil || d == nil {
		return nil
	}
	return []ui.Node{{Kind: ui.KindItem, ID: "call:" + call.ID, Icon: d.M.Icon, Title: d.M.Name + " · " + strings.TrimPrefix(c.cellStatus(), "● "),
		Subtitle: displayName(*call), Actions: []ui.Action{
			{ID: ui.ActionOpen, Label: tr.T("Back to the call"), Primary: true},
			{ID: ui.ActionHangup, Label: tr.T("Hang up"), Key: "e"},
		}}}
}

// cellRowAction runs a notification centre call row's action.
func (c *Core) cellRowAction(id, action string) {
	call, ok := c.cells[id]
	if !ok {
		return
	}
	switch action {
	case ui.ActionOpen:
		c.sys.stack = c.sys.stack[:1]
		if d := c.appByID(appDialer); d != nil {
			c.Launch(d, "call:"+call.ID)
		}
	case ui.ActionHangup:
		c.broker.call(svc.Telephonyd, svc.TelHangup, ui.CallIDArgs{CallID: id}, nil, func(err error) {
			if err != nil {
				c.showToast(tr.T("Hanging up failed: %v", err))
			}
		})
	}
}

// cellChanged follows a call the user had (not one that only rang; one
// answered that failed is had) through its end: away from the call screen
// the system says it ended, since the dialer's own word only shows in
// front; with the last call over the audio goes back to the earpiece,
// whatever the call screen left it on.
func (c *Core) cellChanged(prev, call ui.TelCall, onScreen bool) {
	if call.State != "ended" || prev.State == "incoming" && call.Reason != "failed" {
		return
	}
	if call.Reason == "failed" {
		ims := ""
		if call.IMS != "" {
			ims = " (IMS " + call.IMS + ")"
		}
		c.log.Printf("call %s failed: %s%s", call.ID, firstNonEmpty(call.Error, "no reason given"), ims)
	}
	if !onScreen {
		who := displayName(call)
		switch {
		case call.Reason == "failed" && call.IMS != "":
			c.showToast(tr.T("Call failed: VoLTE is not registered"))
		case call.Reason == "failed" && call.Error != "":
			c.showToast(tr.T("Call failed: %s", call.Error))
		case call.Reason == "failed":
			c.showToast(tr.T("The call with %s failed", who))
		case call.Reason == "busy":
			c.showToast(tr.T("%s is busy", who))
		default:
			dur := int64(0)
			if call.Connected > 0 {
				dur = call.Ended - call.Connected
			}
			c.showToast(tr.T("Call with %s ended · %s", who, clock(time.Duration(dur)*time.Second)))
		}
	}
	if len(c.cells) == 0 && c.audioUp {
		if _, err := os.Stat(filepath.Join(c.cfg.RunDir, svc.AudiodSock)); err == nil {
			c.broker.call(svc.Audiod, svc.AudioRoute, ui.AudioRouteArgs{Route: ui.RouteEarpiece}, nil, func(error) {})
		}
	}
}
