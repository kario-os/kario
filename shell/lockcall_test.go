package shell

import (
	"strings"
	"testing"
)

// On the PIN lock screen with a call going on, the call key brings the
// call's screen back (without unlocking) and the lock screen says so; with
// no call it still opens the emergency keypad.
func TestLockReturnsToCall(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		c.lock.pin = true
		voip := &App{M: &Manifest{ID: "com.test.voip", Name: "VoIP"}, running: true}
		c.calls = map[string]*callInfo{"v1": {id: "v1", app: voip}}
		if !strings.Contains(c.lockHint(), tr.T("Call key: back to the call")) {
			t.Errorf("lock hint without the call: %q", c.lockHint())
		}
		c.secureLockedKey(Named(KeyCall))
		if c.fg != voip {
			t.Errorf("call key on the lock screen: foreground %v, want the call's app", c.fg)
		}
		c.calls = nil
		c.fg = nil
		c.secureLockedKey(Named(KeyCall))
		if _, ok := c.overlays[len(c.overlays)-1].(*lockPadOverlay); len(c.overlays) == 0 || !ok {
			t.Errorf("call key with no call: overlays %v, want the emergency keypad", c.overlays)
		}
		return nil
	})
}
