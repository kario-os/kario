package shell

import (
	"strings"
	"time"

	"github.com/kario-os/kario/ui"
)

// Touch and the power key in a call (19-core-apps.md §5.6). At the ear a
// cheek presses the screen, and a phone without a proximity sensor to
// blank it there (the 6T's is behind its DSP) cannot tell it from a
// finger. So a touch in a call, but on a call ringing in, counts only
// where it cannot be a cheek's (touchAtEar), as Android's do once the
// screen is away from the face:
//
//   - the call's sound is not on the earpiece (the speaker, or a headset's
//     route once audiod has one): the phone is not at the ear;
//   - touch was woken: a volume key, or a finger held still on the screen
//     for callTouchHold and lifted within callHoldMax (a cheek rests
//     longer), turns it on until callTouchAwake after the last touch;
//   - a proximity sensor, where one is read, says the phone is away from
//     the face; near it, no touch counts.
//
// A touch that does not count presses nothing and keeps the screen no
// longer on. The power key hangs up where Settings › Accessibility ›
// Power key ends call is on (powerEndsCall), and the call screen says
// how the call is hung up on this phone and whether touch works
// (callHint, ui.EventCallHint).

const (
	// callTouchAwake: touch woken in a call stays on this long after the
	// last touch.
	callTouchAwake = 10 * time.Second
	// callHoldMin: a finger held still at least this long (the long press
	// set in Settings › Accessibility, if longer) wakes touch as it lifts;
	// callHoldMax: one held longer is a cheek resting on the screen.
	callHoldMin = time.Second
	callHoldMax = 4 * time.Second
)

// proximity is what a proximity sensor says: nothing (none, or not
// read), near the face, or away from it.
type proximity int

const (
	proxUnknown proximity = iota
	proxNear
	proxFar
)

// callTouchState is what a touch may do in a call.
type callTouchState struct {
	// route: the call's sound as audiod last took audio.route (ui.Route*;
	// "": the earpiece, where every call starts).
	route string
	// until: touch was woken and stays on until then; n numbers the
	// wakes, a timer is for the last one.
	until time.Time
	n     int
	prox  proximity
}

// callOffEar: the call's sound is not on the earpiece.
func (c *Core) callOffEar() bool {
	r := c.callTouch.route
	return r != "" && r != ui.RouteEarpiece
}

// callTouchWoken: touch was woken in the call and has not gone off again.
func (c *Core) callTouchWoken() bool { return time.Now().Before(c.callTouch.until) }

// touchAtEar: a call is up, and a touch may be a cheek's.
func (c *Core) touchAtEar() bool {
	switch {
	case !c.anyCall(), c.callOffEar():
		return false
	case c.callTouch.prox != proxUnknown:
		return c.callTouch.prox == proxNear
	}
	return !c.callTouchWoken()
}

// touchable: the renderer reads a touchscreen (its feature "touch").
func (c *Core) touchable() bool { return c.gui != nil && c.gui.info.Has("touch") }

// callTouchHold is how long a finger is held still to wake touch in a
// call.
func (c *Core) callTouchHold() time.Duration { return max(c.touchLong(), callHoldMin) }

// longPress is how long a finger stays down for a long press now.
func (c *Core) longPress() time.Duration {
	if c.touchable() && c.touchAtEar() {
		return c.callTouchHold()
	}
	return c.touchLong()
}

// wakeCallTouch turns touch on in the call until callTouchAwake after the
// last touch.
func (c *Core) wakeCallTouch(by string) {
	t := &c.callTouch
	t.n++
	t.until = time.Now().Add(callTouchAwake)
	c.log.Printf("touch: on in the call (%s), off again %s after the last touch", by, callTouchAwake)
	c.showToast(tr.T("Touch on for %d s", int(callTouchAwake/time.Second)))
	c.callTouchTimer()
	c.sendCallHint()
}

// callTouchTimer looks again when woken touch would go off.
func (c *Core) callTouchTimer() {
	n := c.callTouch.n
	time.AfterFunc(time.Until(c.callTouch.until), func() { c.Post(func() { c.callTouchDue(n) }) })
}

func (c *Core) callTouchDue(n int) {
	t := &c.callTouch
	switch {
	case t.n != n || t.until.IsZero():
	case time.Now().Before(t.until):
		c.callTouchTimer()
	default:
		t.until = time.Time{}
		c.log.Printf("touch: off again in the call (%s without a touch)", callTouchAwake)
		c.sendCallHint()
	}
}

// callTouched: a touch counted in the call; woken touch stays on.
func (c *Core) callTouched() {
	if c.callTouchWoken() {
		c.callTouch.until = time.Now().Add(callTouchAwake)
	}
}

// callHeldUp is the end of a touch in a call that did not count: held
// still long enough, and not as long as a cheek rests, it wakes touch.
func (c *Core) callHeldUp(t touchState, at time.Time, gesture string) {
	held := at.Sub(t.at)
	if t.moved || held < c.callTouchHold() || held > callHoldMax || c.callTouch.prox == proxNear {
		c.log.Printf("touch: %s: nothing (in a call touch is off at the ear)", gesture)
		return
	}
	c.Wake("touch")
	c.lastKey = c.now()
	c.wakeCallTouch("a finger held on the screen")
}

// volumeWakesTouch: a volume key in a call whose touches count for
// nothing wakes touch instead of changing the volume.
func (c *Core) volumeWakesTouch() bool {
	if !c.touchable() || !c.touchAtEar() || c.callTouch.prox == proxNear || c.ringing() {
		return false
	}
	c.wakeCallTouch("a volume key")
	return true
}

// callRouted: audiod took the call's sound to route (audio.route).
func (c *Core) callRouted(route string) {
	if !c.anyCall() || route == c.callTouch.route {
		return
	}
	c.callTouch.route = route
	c.log.Printf("call audio: %s", route)
	c.sendCallHint()
}

// callTouchEnded: the last call is over; the next starts on the earpiece
// with touch off at the ear.
func (c *Core) callTouchEnded() {
	if c.anyCall() {
		return
	}
	t := &c.callTouch
	*t = callTouchState{n: t.n + 1, prox: t.prox}
}

// setProximity is a proximity sensor's reading, near the face or not
// (nothing reads one yet: hal/sensor has the light sensor only).
func (c *Core) setProximity(near bool) {
	p := map[bool]proximity{true: proxNear, false: proxFar}[near]
	if c.callTouch.prox == p {
		return
	}
	c.callTouch.prox = p
	c.sendCallHint()
}

// phoneKeypad is a whole phone's keypad in sku.json: its buttons and
// whatever keyboard is connected, no keys of its own to type with.
const phoneKeypad = "hid"

// powerEndsCall: a press of the power key hangs up (Settings ›
// Accessibility › Power key ends call). Unset, it does on a phone with
// no other way at the ear: no proximity sensor and no keypad of its own
// (the 6T); Android leaves it off, where the screen goes dark at the ear.
func (c *Core) powerEndsCall() bool {
	if on := c.settings.A11y.PowerEndsCall; on != nil {
		return *on
	}
	return !c.cfg.SKU.Proximity && c.cfg.SKU.Keypad == phoneKeypad
}

// powerHangsUp: the power key hung up the call, being set to and the
// screen being on (a press in a pocket lights it first, as Android's);
// false with no call to hang up.
func (c *Core) powerHangsUp() bool {
	if c.screen.off || !c.anyCall() || !c.powerEndsCall() {
		return false
	}
	return c.pageKey(ui.ActionHangup) || c.hangupCellCall("power key")
}

// callHint is what the call screen says besides its Hang up (which OK
// presses, and the end key wherever the phone has one): that the power
// key hangs up, and whether touch works now.
func (c *Core) callHint() string {
	var lines []string
	if c.powerKey && c.powerEndsCall() {
		lines = append(lines, tr.T("Press the power key to hang up"))
	}
	switch {
	case !c.touchable():
	case c.touchAtEar():
		lines = append(lines, tr.T("Touch is off at the ear: press a volume key, or hold the screen for a second, to use it"))
	case c.callTouchWoken() && !c.callOffEar() && c.callTouch.prox == proxUnknown:
		lines = append(lines, tr.T("Touch is on until %d s after the last touch", int(callTouchAwake/time.Second)))
	}
	return strings.Join(lines, "\n")
}

// sendCallHint tells the dialer what its call screen says (callHint).
func (c *Core) sendCallHint() {
	if d := c.appByID(appDialer); d != nil && d.running && c.anyCall() {
		d.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventCallHint, Value: c.callHint()})
	}
}
