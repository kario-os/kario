package shell

import (
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// callRigScreen is a call screen as the dialer's: Hang up focused, the
// page's hangup action (the end key's).
var callRigScreen = ui.Screen{ID: "call", Title: "Call", Focus: "hangup", Right: "new",
	Actions: []ui.Action{{ID: ui.ActionHangup, Label: "Hang up"}, {ID: "new", Label: "New"}},
	Body:    []ui.Node{{Kind: ui.KindItem, ID: "hangup", Title: "Hang up", Actions: []ui.Action{{ID: ui.ActionHangup, Label: "Hang up", Primary: true}}}}}

// inCall: a call connected on the earpiece.
func (r *touchRig) inCall() {
	_ = r.c.onLoop(func() error {
		r.c.cells = map[string]ui.TelCall{"1": {ID: "1", State: "active"}}
		return nil
	})
}

var softHit = TouchHit{Area: "soft", Soft: 2}

// In a call at the ear a touch presses nothing, but a volume key wakes
// touch instead of changing the volume, and says so; the next tap counts
// (report 008 §1.2: on the 6T, without a keyboard, three calls could only
// be ended by the far side).
func TestVolumeKeyWakesTouchInACall(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	r.inCall()
	r.tap(230, 310, softHit)
	r.noAction()
	var vol, after float64
	var toast string
	_ = r.c.onLoop(func() error {
		vol = r.c.settings.RingVolume
		r.c.Press(Named(KeyVolUp))
		after = r.c.settings.RingVolume
		if r.c.toast != nil {
			toast = r.c.toast.body
		}
		return nil
	})
	if toast != tr.T("Touch on for %d s", 10) || after != vol {
		t.Fatalf("the volume key: toast %q, ring volume %v then %v", toast, vol, after)
	}
	r.tap(230, 310, softHit)
	r.action("a new")
}

// On the 6T (no proximity sensor, no keypad of its own) the power key
// hangs up a call by default, as Android's "Power button ends call": the
// call screen's Hang up, the screen staying on. With the screen off a
// press only lights it.
func TestPowerKeyEndsACallOnThe6T(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	r.push(callRigScreen)
	r.inCall()
	var off bool
	_ = r.c.onLoop(func() error {
		r.c.cfg.SKU.Keypad, r.c.powerKey = "hid", true
		r.c.screenOff("test")
		r.c.PowerKey(false)
		off = r.c.ScreenOff()
		return nil
	})
	r.noAction()
	if off {
		t.Fatal("the power key did not light the screen")
	}
	_ = r.c.onLoop(func() error { r.c.PowerKey(false); off = r.c.ScreenOff(); return nil })
	r.action("hangup hangup")
	if off {
		t.Fatal("the screen went off as the call was hung up")
	}
}

// A touch at the ear keeps the screen no longer on; a finger held still
// for a second and lifted wakes touch, one held as long as a cheek rests,
// or moved, does not.
func TestHoldWakesTouchInACall(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	r.inCall()
	t0 := time.Now()
	held := func(d time.Duration, move float64) (woke, kept bool) {
		_ = r.c.onLoop(func() error {
			r.c.callTouch.until = time.Time{}
			before := r.c.lastKey
			r.c.Touch("down", 230, 310, &softHit, t0)
			kept = !r.c.lastKey.Equal(before)
			r.c.Touch("up", 230+move, 310, nil, t0.Add(d))
			woke = r.c.callTouchWoken()
			return nil
		})
		return woke, kept
	}
	hold := time.Duration(0)
	_ = r.c.onLoop(func() error { hold = r.c.callTouchHold(); return nil })
	if hold != callHoldMin {
		t.Fatalf("held %v to wake touch, want %v (the long press is shorter)", hold, callHoldMin)
	}
	for _, c := range []struct {
		d    time.Duration
		move float64
		want bool
	}{{300 * time.Millisecond, 0, false}, {hold + 200*time.Millisecond, 0, true}, {callHoldMax + time.Second, 0, false}, {hold + 200*time.Millisecond, 3 * touchSlop, false}} {
		woke, kept := held(c.d, c.move)
		if woke != c.want || kept {
			t.Errorf("held %v, moved %v px: woke %v (want %v), the screen kept on %v", c.d, c.move, woke, c.want, kept)
		}
	}
	r.noAction()
	held(hold+200*time.Millisecond, 0)
	r.tap(230, 310, softHit)
	r.action("a new")
}

// Woken touch stays on while touches come and goes off callTouchAwake
// after the last; then a tap presses nothing again.
func TestWokenTouchGoesOffAgain(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	r.inCall()
	_ = r.c.onLoop(func() error {
		r.c.Press(Named(KeyVolDown))
		r.c.callTouch.until = time.Now().Add(time.Second)
		return nil
	})
	r.tap(230, 310, softHit)
	r.action("a new")
	var left time.Duration
	_ = r.c.onLoop(func() error { left = time.Until(r.c.callTouch.until); return nil })
	if left < callTouchAwake-time.Second {
		t.Fatalf("a touch left woken touch %v, want about %v", left, callTouchAwake)
	}
	_ = r.c.onLoop(func() error {
		r.c.callTouch.until = time.Now().Add(-time.Millisecond)
		r.c.callTouchDue(r.c.callTouch.n)
		return nil
	})
	r.tap(230, 310, softHit)
	r.noAction()
}

// With the call's sound off the earpiece (the speaker) touches count; back
// on the earpiece they do not. A proximity sensor's word, where one is
// read, goes before the wake: away from the face they count, near it not
// even after a volume key. The next call starts on the earpiece again.
func TestTouchInACallOffTheEar(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	r.inCall()
	do := func(f func()) { _ = r.c.onLoop(func() error { f(); return nil }) }
	do(func() { r.c.callRouted(ui.RouteSpeaker) })
	r.tap(230, 310, softHit)
	r.action("a new")
	do(func() { r.c.callRouted(ui.RouteEarpiece) })
	r.tap(230, 310, softHit)
	r.noAction()
	do(func() { r.c.setProximity(false) })
	r.tap(230, 310, softHit)
	r.action("a new")
	do(func() { r.c.setProximity(true); r.c.Press(Named(KeyVolUp)) })
	r.tap(230, 310, softHit)
	r.noAction()
	do(func() {
		r.c.callTouch.prox = proxUnknown
		r.c.callRouted(ui.RouteSpeaker)
		r.c.cells = nil
		r.c.callTouchEnded()
		r.c.cells = map[string]ui.TelCall{"2": {ID: "2", State: "dialing"}}
	})
	r.tap(230, 310, softHit)
	r.noAction()
}

// The call screen's lines: the power key where it hangs up, and whether
// touch works (only where the renderer reads a touchscreen).
func TestCallHint(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	r.inCall()
	power, off := tr.T("Press the power key to hang up"), tr.T("Touch is off at the ear: press a volume key, or hold the screen for a second, to use it")
	on := tr.T("Touch is on until %d s after the last touch", 10)
	hint := func(f func()) string {
		var h string
		_ = r.c.onLoop(func() error { f(); h = r.c.callHint(); return nil })
		return h
	}
	if h := hint(func() { r.c.cfg.SKU.Keypad, r.c.powerKey = "hid", true }); h != power+"\n"+off {
		t.Errorf("the 6T at the ear: %q", h)
	}
	if h := hint(func() { r.c.Press(Named(KeyVolUp)) }); h != power+"\n"+on {
		t.Errorf("touch woken: %q", h)
	}
	if h := hint(func() { r.c.callRouted(ui.RouteSpeaker) }); h != power {
		t.Errorf("on the speaker: %q", h)
	}
	if h := hint(func() { _ = r.c.settingsSet(ui.SettingPowerEndsCall, 0); r.c.callRouted(ui.RouteEarpiece) }); strings.Contains(h, power) {
		t.Errorf("the power key set not to hang up: %q", h)
	}
	if h := hint(func() { _ = r.c.settingsSet(ui.SettingPowerEndsCall, 1); r.c.powerKey = false }); h == "" || strings.Contains(h, power) {
		t.Errorf("no power key of its own (the end key is it): %q", h)
	}
	deaf := newTouchRig(t)
	deaf.inCall()
	var h string
	_ = deaf.c.onLoop(func() error { h = deaf.c.callHint(); return nil })
	if h != "" {
		t.Errorf("no touchscreen, no power key: %q", h)
	}
}

// Power key ends call is on by default only on a phone with no proximity
// sensor and no keypad of its own; the setting overrides that, and
// Settings sees it where the board has a power key of its own. Set off,
// the power key turns the screen off in a call as anywhere.
func TestPowerEndsCallSetting(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	r.push(callRigScreen)
	r.inCall()
	for _, c := range []struct {
		sku  SKU
		want bool
	}{{SKU{}, false}, {SKU{Keypad: "hid"}, true}, {SKU{Keypad: "hid", Proximity: true}, false}, {SKU{Keypad: "t9-v1"}, false}} {
		var got bool
		_ = r.c.onLoop(func() error { r.c.cfg.SKU = c.sku; got = r.c.powerEndsCall(); return nil })
		if got != c.want {
			t.Errorf("keypad %q, proximity %v: on %v, want %v", c.sku.Keypad, c.sku.Proximity, got, c.want)
		}
	}
	var shown, hidden bool
	var on any
	_ = r.c.onLoop(func() error {
		r.c.cfg.SKU = SKU{Keypad: "t9-v1"}
		_, hidden = r.c.settingsGet()[ui.SettingPowerEndsCall]
		r.c.powerKey = true
		if err := r.c.settingsSet(ui.SettingPowerEndsCall, 1); err != nil {
			t.Error(err)
		}
		on, shown = r.c.settingsGet()[ui.SettingPowerEndsCall]
		return nil
	})
	if hidden || !shown || on != true {
		t.Fatalf("settings.get: without a power key %v, with one %v (%v)", hidden, shown, on)
	}
	var off bool
	_ = r.c.onLoop(func() error {
		_ = r.c.settingsSet(ui.SettingPowerEndsCall, 0)
		r.c.PowerKey(false)
		off = r.c.ScreenOff()
		return nil
	})
	r.noAction()
	if !off {
		t.Fatal("set off, the power key did not turn the screen off")
	}
}
