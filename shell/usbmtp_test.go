package shell

import (
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

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
}
