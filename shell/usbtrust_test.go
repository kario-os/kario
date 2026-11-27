package shell

import (
	"slices"
	"testing"
)

// usbTrustCore is a bare Core with the simulated cable in, on a phone
// whose charger says whether it is plugged in.
func usbTrustCore(t *testing.T) (*Core, func(bool)) {
	c, _ := lockCore(t)
	c.usb.sim = true
	plug := func(on bool) {
		c.SetPower(Power{Level: 50, Charging: on, Plug: true})
		c.checkCharger()
	}
	plug(true)
	c.usb.simOn = true
	c.checkUSB()
	if c.usb.prompt == nil || c.topOverlay() != overlay(c.usb.prompt) {
		t.Fatal("a computer configured the phone: no question")
	}
	return c, plug
}

func (c *Core) shown(o overlay) bool { return slices.Contains(c.overlays, o) }

// Report 008 §1.7: the question shown, the screen turned off with the
// power key, the cable pulled and plugged in again while the controller
// is not read (every offTick with the screen off, never while the phone
// sleeps): the charger's uevent ends the computer, and the one plugged in
// is asked about again. Before, the controller still said configured and
// nothing asked again.
func TestUSBReplugBetweenReadsAsksAgain(t *testing.T) {
	c, plug := usbTrustCore(t)
	first := c.usb.prompt
	c.screenOff("power key")
	plug(false)
	if c.usb.prompt != nil || c.shown(first) {
		t.Fatalf("the cable out: question %v, still shown %v", c.usb.prompt != nil, c.shown(first))
	}
	plug(true)
	if c.usb.prompt == nil || c.usb.prompt == first || !c.shown(c.usb.prompt) {
		t.Fatalf("plugged in again: asked again %v", c.usb.prompt != nil && c.usb.prompt != first)
	}
	if c.usb.trusted {
		t.Fatal("trusted without an answer")
	}
}

// A computer trusted, its cable pulled and another's plugged in between
// two reads of the controller: the trust ends with the first and the
// second is asked about. Before, the second was trusted as the first.
func TestUSBTrustEndsWithTheCableBetweenReads(t *testing.T) {
	c, plug := usbTrustCore(t)
	c.popOverlay(c.usb.prompt)
	c.usb.trusted = true
	plug(false)
	plug(true)
	if c.usb.trusted || c.usb.prompt == nil {
		t.Fatalf("another computer: trusted %v, asked %v", c.usb.trusted, c.usb.prompt != nil)
	}
}

// The question goes with its overlay, whatever removes it: the next
// computer is asked about again.
func TestUSBQuestionGoesWithItsOverlay(t *testing.T) {
	c, _ := usbTrustCore(t)
	c.popOverlay(c.usb.prompt)
	if c.usb.prompt != nil {
		t.Fatal("the question outlived its overlay")
	}
	c.usb.simOn = false
	c.checkUSB()
	c.usb.simOn = true
	c.checkUSB()
	if c.usb.prompt == nil || !c.shown(c.usb.prompt) || c.usb.trusted {
		t.Fatalf("plugged in again: asked %v, trusted %v", c.usb.prompt != nil, c.usb.trusted)
	}
}

// The PIN lock closes the question unanswered, which is no refusal: it is
// asked again once the phone is unlocked, the computer still there, and
// not under the lock. Before, the lock answered "Don't trust" for the
// user.
func TestUSBQuestionClosedByTheLockAskedAfterUnlock(t *testing.T) {
	c, _ := usbTrustCore(t)
	c.lock.pin = true
	c.lockNow("test")
	if c.usb.prompt != nil || len(c.overlays) != 0 {
		t.Fatalf("locked: question %v, overlays %d", c.usb.prompt != nil, len(c.overlays))
	}
	c.checkUSB()
	if c.usb.prompt != nil {
		t.Fatal("asked under the lock")
	}
	c.unlockNow("test")
	c.checkUSB()
	if c.usb.prompt == nil || !c.shown(c.usb.prompt) || c.usb.trusted {
		t.Fatalf("unlocked: asked %v, trusted %v", c.usb.prompt != nil, c.usb.trusted)
	}
}

// Where no charger says whether it is plugged in (the battery's status
// alone, a simulated battery), the controller's word stands.
func TestUSBWithoutAChargersWord(t *testing.T) {
	c, _ := lockCore(t)
	c.usb.sim, c.usb.simOn = true, true
	c.SetPower(Power{Level: 100})
	c.checkCharger()
	c.checkUSB()
	if c.usb.prompt == nil {
		t.Fatal("no question with the controller configured")
	}
}
