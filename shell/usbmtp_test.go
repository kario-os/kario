package shell

import (
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// The helper keeps the Shell's GODEBUG and its GOCOVERDIR, where a
// coverage build writes its counters (make coverage); neither when unset.
func TestUSBHelperGetsTheShellsGoVariables(t *testing.T) {
	t.Setenv("GODEBUG", "disablethp=1")
	t.Setenv("GOCOVERDIR", "/build/coverage/go/e2e")
	if got := strings.Join(usbHelperShellEnv(), " "); got != "GODEBUG=disablethp=1 GOCOVERDIR=/build/coverage/go/e2e" {
		t.Errorf("the helper gets %q", got)
	}
	t.Setenv("GOCOVERDIR", "")
	if got := strings.Join(usbHelperShellEnv(), " "); got != "GODEBUG=disablethp=1" {
		t.Errorf("without GOCOVERDIR the helper gets %q", got)
	}
}

// MTP has no password, so under a PIN lock the computer is asked about
// only once the phone is unlocked; network sharing asks as before (its
// password is on the phone's screen).
func TestFileTransferWaitsForTheUnlock(t *testing.T) {
	c, _ := lockCore(t)
	c.usb.sim = true
	if err := c.setUSBMode(1); err != nil || c.usbMode() != ui.USBModeMTP {
		t.Fatalf("usb_mode 1: %q, %v", c.usbMode(), err)
	}
	c.lock.pin = true
	c.lockNow("test")
	c.usb.simOn = true
	c.checkUSB()
	c.checkUSB()
	if c.usb.prompt != nil || !c.usb.waitUnlock {
		t.Fatalf("locked: prompt %v, waiting %v", c.usb.prompt != nil, c.usb.waitUnlock)
	}
	c.unlockNow("test")
	c.checkUSB()
	if c.usb.prompt == nil || c.usb.waitUnlock {
		t.Fatalf("unlocked: prompt %v, waiting %v", c.usb.prompt != nil, c.usb.waitUnlock)
	}
	c.usb.simOn = false
	c.checkUSB()
	if c.usb.prompt != nil {
		t.Fatal("the question outlived the cable")
	}

	if err := c.setUSBMode(0); err != nil || c.usbMode() != ui.USBModeNetwork {
		t.Fatalf("usb_mode 0: %q, %v", c.usbMode(), err)
	}
	c.lockNow("test")
	c.usb.simOn = true
	c.checkUSB()
	if c.usb.prompt == nil {
		t.Fatal("network sharing did not ask under the lock")
	}
	if err := c.setUSBMode(2); err == nil {
		t.Error("usb_mode 2 taken")
	}
}

// A bus reset (libmtp resets a phone it takes for Android when it lets
// go) passes through default and addressed: sharing stays while the
// computer enumerates the phone again, but not past the wait, and pulling
// the cable meanwhile still ends it.
func TestBusResetKeepsSharing(t *testing.T) {
	c, _ := lockCore(t)
	c.usb.trusted, c.usb.state = true, "configured"
	for _, st := range []string{"default", "addressed", "configured"} {
		if !c.usbReset(st) {
			t.Fatalf("%s during a reset was not held", st)
		}
	}
	if !c.usb.resetBy.IsZero() || c.usb.state != "configured" {
		t.Errorf("after the reset: %q, until %v", c.usb.state, c.usb.resetBy)
	}
	c.usbReset("default")
	if c.usbReset("not attached") {
		t.Error("the cable pulled during a reset was held")
	}
	c.usbReset("default")
	c.usb.resetBy = time.Now().Add(-time.Second)
	if c.usbReset("default") {
		t.Error("a reset held past its wait")
	}
	c.usb.trusted = false
	if c.usbReset("default") {
		t.Error("held without a trusted computer")
	}
	c.usb.resetBy = time.Time{}
	c.usb.prompt = &confirmOverlay{text: "Trust this computer?"}
	c.pushOverlay(c.usb.prompt)
	for _, st := range []string{"default", "addressed", "configured"} {
		if !c.usbReset(st) {
			t.Fatalf("%s while the user reads whether to trust the computer was not held", st)
		}
	}
	if len(c.overlays) == 0 || c.overlays[len(c.overlays)-1] != c.usb.prompt {
		t.Error("the question whether to trust the computer went during a bus reset")
	}
}
