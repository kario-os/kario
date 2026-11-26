package shell

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// callRig is a Shell with a stand-in dialer (its messages kept in out, its
// pages set by the test) and a stand-in telephonyd that records requests.
type callRig struct {
	t      *testing.T
	c      *Core
	dialer *App
	mu     sync.Mutex
	asked  []string
}

func newCallRig(t *testing.T) *callRig {
	r := &callRig{t: t, c: loopCore(t, t.TempDir())}
	_, err := wire.ServeHub(filepath.Join(r.c.cfg.RunDir, svc.TelephonydSock), func(method string, params json.RawMessage) (any, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.asked = append(r.asked, method+" "+string(params))
		if method == svc.TelStatus {
			return telStatus{Registered: true, Operator: "Kario Mobile", Sim: "ready", Radio: true}, nil
		}
		return map[string]bool{"ok": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r.dialer = &App{M: &Manifest{ID: appDialer, Name: "Phone", Icon: "P", Permissions: []string{ui.PermPhone}}, running: true, ready: true,
		out: make(chan outbound, appOutQueue)}
	r.loop(func() {
		r.c.apps = append(r.c.apps, r.dialer)
		r.dialer.stack = []*View{newView(ui.Screen{ID: "main"}), newView(callScreen())}
	})
	return r
}

// callScreen is the dialer's call screen as far as the system reads it.
func callScreen() ui.Screen {
	return ui.Screen{ID: "call", Right: ui.ActionHide, Actions: []ui.Action{{ID: ui.ActionHangup, Label: "Hang up"}, {ID: ui.ActionHide, Label: "Minimize"}},
		Body: []ui.Node{{Kind: ui.KindButton, ID: "hangup", Text: "Hang up", Actions: []ui.Action{{ID: ui.ActionHangup, Primary: true}}}}}
}

func (r *callRig) loop(f func()) {
	r.t.Helper()
	_ = r.c.onLoop(func() error { f(); return nil })
}

// handled stands for the dialer's answer to what it was sent: the keys
// after it are no longer held (input.go).
func (r *callRig) handled() {
	r.dialer.input, r.dialer.ping.sent = inputBarrier{}, time.Time{}
}

// sent drains what the dialer was sent and returns its events' values.
func (r *callRig) sent() []string {
	defer r.handled()
	var out []string
	for {
		select {
		case o := <-r.dialer.out:
			if o.m.T == ui.MsgEvent {
				out = append(out, o.m.Event+" "+o.m.Value)
			}
		default:
			return out
		}
	}
}

// waitAsked waits until telephonyd was asked something starting so.
func (r *callRig) waitAsked(prefix string) {
	r.t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		r.mu.Lock()
		for _, a := range r.asked {
			if strings.HasPrefix(a, prefix) {
				r.mu.Unlock()
				return
			}
		}
		r.mu.Unlock()
	}
	r.t.Fatalf("telephonyd was not asked %q: %q", prefix, r.asked)
}

func call(id, state string, connectedAgo time.Duration) ui.TelCall {
	c := ui.TelCall{ID: id, Number: "152", Dir: "out", State: state}
	if connectedAgo > 0 {
		c.Connected = time.Now().Add(-connectedAgo).Unix()
	}
	return c
}

// Back on the call screen puts the call in the background: the page stays
// the dialer's top page, the status bar shows the call with its time, the
// standby screen and the command palette say how to get back, the
// notification centre lists it; the call key brings it back, and the
// right soft key hides it again.
func TestCallScreenToBackgroundAndBack(t *testing.T) {
	r := newCallRig(t)
	c := r.c
	r.loop(func() {
		c.cells = map[string]ui.TelCall{"c1": call("c1", "active", 65*time.Second)}
		c.setForeground(r.dialer)
		if mid, _ := c.statusMid(); mid != "" || !c.cellOnScreen() {
			t.Errorf("on the call screen: status %q, on screen %v", mid, c.cellOnScreen())
		}
		c.Key(Named(KeyEsc))
		if c.fg != nil || r.dialer.top().S.ID != "call" {
			t.Fatalf("after Back: foreground %v, the dialer's top %q", c.fg, r.dialer.top().S.ID)
		}
		if mid, kind := c.statusMid(); !strings.HasPrefix(mid, "● ") || !strings.Contains(mid, "01:0") || kind != "call" {
			t.Errorf("status bar away from the call: %q %q", mid, kind)
		}
		if h := c.idleHint(); !strings.Contains(h, tr.T("Call key: back to the call")) {
			t.Errorf("standby hint %q", h)
		}
		var back bool
		for _, e := range c.globalEntries() {
			back = back || e.label == tr.T("Back to the call")
		}
		if !back {
			t.Error("the command palette has no way back to the call")
		}
		s := c.systemScreen("sys.notifs")
		if len(s.Body) == 0 || s.Body[0].ID != "call:c1" || s.Body[0].PrimaryAction() == nil {
			t.Errorf("notification centre: %+v", s.Body)
		}
		r.sent()
		c.Key(Named(KeyCall))
		if c.fg != r.dialer {
			t.Fatalf("call key: foreground %v", c.fg)
		}
		if got := r.sent(); len(got) == 0 || got[len(got)-1] != ui.EventOpen+" call:c1" {
			t.Errorf("call key: the dialer was sent %q", got)
		}
		if mid, _ := c.statusMid(); mid != "" {
			t.Errorf("back on the call screen the status bar says %q", mid)
		}
		c.Key(Named(KeyF2))
		if c.fg != nil {
			t.Errorf("right soft key: foreground %v", c.fg)
		}
		// From the notification centre's row.
		c.openSystem("sys.notifs")
		top := c.sys.top()
		top.focus = "call:c1"
		c.Key(Named(KeyEnter))
		if c.fg != r.dialer {
			t.Errorf("notification centre row: foreground %v", c.fg)
		}
	})
}

// Hidden, the call screen gives way to what was in front before it.
func TestHideReturnsToTheAppBefore(t *testing.T) {
	r := newCallRig(t)
	c := r.c
	music := &App{M: &Manifest{ID: "com.test.music", Name: "Music"}, running: true}
	r.loop(func() {
		c.apps = append(c.apps, music)
		c.cells = map[string]ui.TelCall{"c1": call("c1", "active", time.Second)}
		c.setForeground(music)
		c.returnToCall()
		if c.fg != r.dialer {
			t.Fatalf("back to the call: foreground %v", c.fg)
		}
		r.handled()
		c.Key(Named(KeyEsc))
		if c.fg != music {
			t.Errorf("Back: foreground %v, want the music app", c.fg)
		}
	})
}

// The end key ends the call wherever the phone is, not only on its screen.
func TestEndKeyHangsUpAway(t *testing.T) {
	r := newCallRig(t)
	c := r.c
	r.loop(func() {
		c.cells = map[string]ui.TelCall{"c1": call("c1", "held", time.Minute), "c2": call("c2", "active", time.Second)}
		c.GoHome()
		c.Key(Named(KeyF4))
	})
	r.waitAsked(svc.TelHangup + ` {"call_id":"c2"}`)
}

// Digits on the standby screen during a call open a second call's number
// page in the dialer, unless no second call can go out: then the phone says
// why and how to get back.
func TestDigitsDuringACall(t *testing.T) {
	r := newCallRig(t)
	c := r.c
	r.loop(func() {
		for _, tc := range []struct {
			cells map[string]ui.TelCall
			want  string
		}{
			{map[string]ui.TelCall{"c1": call("c1", "alerting", 0)}, tr.T("The call is still connecting")},
			{map[string]ui.TelCall{"c1": call("c1", "held", time.Minute), "c2": call("c2", "active", time.Second)}, tr.T("Two calls already")},
			{map[string]ui.TelCall{"c1": {ID: "c1", Number: "112", State: "active", Emergency: true, Connected: time.Now().Unix()}}, tr.T("An emergency call is in progress")},
		} {
			c.cells = tc.cells
			c.GoHome()
			c.toast = nil
			c.Key(Runes("5"))
			if c.fg != nil || c.toast == nil || c.toast.body != c.callBlockedHint(tc.want) {
				t.Errorf("%v: foreground %v, toast %+v, want %q", tc.cells, c.fg, c.toast, c.callBlockedHint(tc.want))
			}
		}
		c.cells = map[string]ui.TelCall{"c1": call("c1", "active", time.Second)}
		c.GoHome()
		r.sent()
		c.Key(Runes("5"))
		if got := r.sent(); c.fg != r.dialer || len(got) == 0 || got[0] != ui.EventOpen+" dial:5" {
			t.Errorf("a second call: foreground %v, the dialer was sent %q", c.fg, got)
		}
	})
}

// An app's dial during a call the network cannot add to says why.
func TestDialRefusedBesideACall(t *testing.T) {
	r := newCallRig(t)
	c := r.c
	var a answer
	r.loop(func() {
		c.cells = map[string]ui.TelCall{"c1": call("c1", "dialing", 0)}
		c.broker.dispatchPhone(r.dialer, req(ui.CapTelDial, ui.DialArgs{Number: "10086"}), a.reply)
	})
	if a.err == nil || a.err.Error() != tr.T("%s: press Call to go back to the call", tr.T("The call is still connecting")) {
		t.Fatalf("dial while a call connects: %v", a.err)
	}
}

// The dialer is not frozen in the background while a cellular call goes
// on (its call screen follows the call; a frozen app's queue fills up).
func TestDialerRunsDuringACall(t *testing.T) {
	r := newCallRig(t)
	c := r.c
	r.loop(func() {
		if !c.inCall(r.dialer) {
			t.Error("the dialer with its call screen up counts as out of a call")
		}
		r.dialer.stack = r.dialer.stack[:1]
		if c.inCall(r.dialer) {
			t.Error("the dialer counts as in a call without one")
		}
		c.cells = map[string]ui.TelCall{"c1": call("c1", "held", time.Second)}
		if !c.inCall(r.dialer) {
			t.Error("the dialer counts as out of a call during a held one")
		}
	})
}

// A call ending away from its screen is said by the system (the dialer's
// toast only shows in front); on the call screen the dialer says it.
func TestCallEndedInTheBackground(t *testing.T) {
	r := newCallRig(t)
	c := r.c
	r.loop(func() {
		up := call("c1", "active", 12*time.Second)
		c.cellCall(up)
		c.GoHome()
		end := up
		end.State, end.Reason, end.Ended = "ended", "remote", up.Connected+12
		c.cellCall(end)
		if want := tr.T("Call with %s ended · %s", "152", "00:12"); c.toast == nil || c.toast.body != want {
			t.Errorf("toast %+v, want %q", c.toast, want)
		}
		if mid, _ := c.statusMid(); mid != "" {
			t.Errorf("status bar after the call: %q", mid)
		}
		c.toast = nil
		c.cellCall(call("c2", "active", time.Second))
		c.setForeground(r.dialer)
		end = call("c2", "ended", time.Second)
		c.cellCall(end)
		if c.toast != nil {
			t.Errorf("on the call screen the system said %q", c.toast.body)
		}
		ring := ui.TelCall{ID: "c3", Number: "13800000002", Dir: "in", State: "incoming"}
		c.cellCall(ring)
		c.GoHome()
		c.toast = nil
		ring.State, ring.Reason = "ended", "missed"
		c.cellCall(ring)
		if c.toast != nil {
			t.Errorf("a missed call: toast %q (the notification says it)", c.toast.body)
		}
	})
}

// A call that failed, answered (so still ringing in the Shell's eyes) or
// dialled, says why away from the call screen, where the dialer does not
// show; on it the dialer says it.
func TestCallFailedSaysWhy(t *testing.T) {
	r := newCallRig(t)
	c := r.c
	r.loop(func() {
		ring := ui.TelCall{ID: "c4", Number: "13800000004", Dir: "in", State: "incoming"}
		c.cellCall(ring)
		c.GoHome()
		c.toast = nil
		ring.State, ring.Reason, ring.Error = "ended", "failed", "answering failed: modem busy"
		c.cellCall(ring)
		if want := tr.T("Call failed: %s", ring.Error); c.toast == nil || c.toast.body != want {
			t.Errorf("an answer the modem failed: toast %+v, want %q", c.toast, want)
		}
		if len(c.overlays) != 0 {
			t.Errorf("overlays left: %v", c.overlays)
		}
		c.toast = nil
		out := call("c5", "dialing", 0)
		c.cellCall(out)
		out.State, out.Reason = "ended", "failed"
		c.cellCall(out)
		if want := tr.T("The call with %s failed", "152"); c.toast == nil || c.toast.body != want {
			t.Errorf("a dial that failed without a reason: toast %+v, want %q", c.toast, want)
		}
		c.toast = nil
		out = call("c6", "dialing", 0)
		c.cellCall(out)
		c.setForeground(r.dialer)
		out.State, out.Reason, out.Error = "ended", "failed", "no service"
		c.cellCall(out)
		if c.toast != nil {
			t.Errorf("on the call screen the system said %q", c.toast.body)
		}
	})
}

// The status bar follows the call: being set up, connected, all on hold.
func TestCallIndicator(t *testing.T) {
	r := newCallRig(t)
	c := r.c
	r.loop(func() {
		c.GoHome()
		for _, tc := range []struct {
			cells map[string]ui.TelCall
			want  string
		}{
			{map[string]ui.TelCall{"c1": call("c1", "dialing", 0)}, tr.T("● Calling…")},
			{map[string]ui.TelCall{"c1": call("c1", "held", time.Minute)}, tr.T("● On hold %s", "01:00")},
			{map[string]ui.TelCall{"c1": call("c1", "held", time.Minute), "c2": call("c2", "active", 5*time.Second)}, tr.T("● Call %s", "00:05")},
			{map[string]ui.TelCall{"c1": {ID: "c1", State: "incoming"}}, ""},
		} {
			c.cells = tc.cells
			if mid, _ := c.statusMid(); mid != tc.want {
				t.Errorf("%v: status %q, want %q", tc.cells, mid, tc.want)
			}
		}
	})
}

// A call coming in during another says that answering holds the first.
func TestWaitingCallNote(t *testing.T) {
	r := newCallRig(t)
	c := r.c
	r.loop(func() {
		c.cells = map[string]ui.TelCall{"c1": call("c1", "active", time.Second)}
		o := &cellIncomingOverlay{call: ui.TelCall{ID: "c2", Number: "13800000002", Dir: "in", State: "incoming"}}
		if n := o.ringNote(c); n != tr.T("Answering puts %s on hold", "152") {
			t.Errorf("note %q", n)
		}
	})
}

// A key pressed after a starting app's first page came, while the keys
// from before it wait for their replay, goes after them: "152" and OK
// typed at once on the standby screen dial 152, not 1 (the dialer starts
// with the first digit as its deep link).
func TestKeysAfterTheFirstPageKeepTheirPlace(t *testing.T) {
	r := newCallRig(t)
	c := r.c
	app := &App{M: &Manifest{ID: "com.test.pad", Name: "Pad"}, running: true, ready: true, out: make(chan outbound, appOutQueue)}
	page := ui.Screen{ID: "main", Actions: []ui.Action{{ID: "dial", Label: "Call"}},
		Body: []ui.Node{{Kind: ui.KindInput, ID: "number", Keyboard: "phone", Submit: "dial", Value: "1"}}}
	r.loop(func() {
		c.apps = append(c.apps, app)
		c.setForeground(app)
		c.Key(Runes("5"))
		c.Key(Runes("2"))
		c.appMsg(app, ui.Msg{T: ui.MsgReset, Screen: &page})
		c.Key(Named(KeyEnter))
	})
	var dialled []string
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end) && len(dialled) == 0; time.Sleep(20 * time.Millisecond) {
		r.loop(func() {
			for {
				select {
				case o := <-app.out:
					if o.m.T == ui.MsgAction && o.m.Action == "dial" {
						dialled = append(dialled, o.m.Fields["number"])
					}
					continue
				default:
				}
				break
			}
		})
	}
	if len(dialled) != 1 || dialled[0] != "152" {
		t.Fatalf("dialled %q, want 152", dialled)
	}
}

// Locked with the call screen in front (the screen went off during the
// call), the call screen stays and cannot be left; locked with the call in
// the background, the call key brings it back without unlocking.
func TestLockDuringACall(t *testing.T) {
	r := newCallRig(t)
	c := r.c
	other := &App{M: &Manifest{ID: "com.test.notes", Name: "Notes"}, running: true}
	r.loop(func() {
		c.apps = append(c.apps, other)
		c.lock.pin = true
		c.cells = map[string]ui.TelCall{"c1": call("c1", "active", time.Second)}
		c.setForeground(r.dialer)
		c.lockNow("screen off")
		if c.fg != r.dialer {
			t.Fatalf("locked on the call screen: foreground %v", c.fg)
		}
		c.Key(Named(KeyEsc))
		c.Key(Named(KeyF2))
		if c.fg != r.dialer {
			t.Errorf("locked: Back and the right soft key left the call screen (foreground %v)", c.fg)
		}
		c.unlockNow("test")
		c.setForeground(other)
		c.lockNow("screen off")
		if c.fg != nil || !strings.Contains(c.lockHint(), tr.T("Call key: back to the call")) {
			t.Fatalf("locked with the call away: foreground %v, hint %q", c.fg, c.lockHint())
		}
		r.sent()
		c.Key(Named(KeyCall))
		if c.fg != r.dialer || !c.secureLocked() {
			t.Errorf("call key on the lock screen: foreground %v, locked %v", c.fg, c.secureLocked())
		}
		if got := r.sent(); len(got) == 0 || got[0] != ui.EventOpen+" call:c1" {
			t.Errorf("the dialer was sent %q", got)
		}
	})
}
