package shell

import (
	"slices"
	"strings"
	"testing"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/ui"
)

// A sensor in the OK key reads a finger only in a system prompt: it never
// unlocks, the lock screen does not offer it, prompts ask for a press of OK,
// and a prompt says when no TEE keeps the key.
func TestFingerprintInTheOKKey(t *testing.T) {
	c, _ := lockCore(t)
	c.lock.pin, c.lock.prefs.FingerUnlock = true, true
	c.lock.st = keystore.LockStatus{PIN: true, Sensor: "simulated", Fingers: []keystore.Finger{{ID: 1, Name: "Thumb"}},
		Security: keystore.Security}
	if !c.fingerUnlockReady() || !strings.Contains(c.lockHint(), tr.T("or touch the fingerprint sensor")) {
		t.Fatalf("a sensor of its own: ready %v, hint %q", c.fingerUnlockReady(), c.lockHint())
	}
	if c.fingerTouchText() != tr.T("Touch the fingerprint sensor to confirm, or enter the payment PIN") {
		t.Fatalf("a sensor of its own asks %q", c.fingerTouchText())
	}

	c.lock.st.Place = ui.FingerprintOKKey
	c.lockNow("test")
	if c.fingerUnlockReady() || c.lock.armed {
		t.Fatal("the OK key's sensor unlocks")
	}
	if h := c.lockHint(); h != tr.T("OK: enter the PIN") {
		t.Fatalf("hint %q", h)
	}
	c.Key(Named(KeyEnter))
	pad, ok := overlayOf[*lockPadOverlay](c)
	if !ok {
		t.Fatal("OK did not open the PIN pad")
	}
	if lines, _, _ := pad.lines(c); slices.Contains(lines, tr.T("A fingerprint unlocks it too.")) {
		t.Fatalf("the PIN pad offers the finger: %q", lines)
	}

	o := &authOverlay{bio: true, bioAsk: c.fingerTouchText(), bioStore: c.keyStoreText()}
	want := []string{tr.T("Press OK with an enrolled finger to confirm, or enter the payment PIN"),
		tr.T("This phone has no secure hardware (TEE): the system's key service keeps the key and matches the finger.")}
	if got := o.bioLines(); !slices.Equal(got, want) {
		t.Fatalf("the payment prompt says %q", got)
	}
	c.lock.st.Security = ui.SecurityTEE
	if s := c.keyStoreText(); s != "" {
		t.Fatalf("with a TEE: %q", s)
	}
	if lines := (&enrolOverlay{okKey: true, steps: 5}).lines(); lines[0] != tr.T("Press OK with the same finger, lifting it between presses, so all of it is read.") {
		t.Fatalf("enrolling: %q", lines)
	}

	var got error
	if !c.securitySetting(ui.SettingArgs{Key: ui.SettingFingerprintUnlock, Value: 1}, func(err error) { got = err }) || got == nil {
		t.Fatalf("turning unlocking on for the OK key's sensor: %v", got)
	}
}
