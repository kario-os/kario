package shell

import (
	"fmt"
	"testing"
	"time"
)

// A USB keyboard plugged in while the phone is securely locked enters the
// PIN and nothing else: not the call key (emergency calls), not # (the
// erase offered after wrong PINs), not the power key, nothing over the
// lock; the phone's own keys and a keyboard plugged in before the lock
// keep working. After the unlock it types everywhere.
func TestKeyboardPluggedInLockedTypesOnlyThePIN(t *testing.T) {
	c := menuCore(t)
	c.lock.pin = true
	c.lock.st.Failures = forgotAfter
	c.cfg.PersistDir = t.TempDir()
	reported(t, c, `{"t":"input","name":"Desk keyboard","kind":"keyboard","added":true,"ok":true,"id":1,"bus":"usb"}`)
	c.lockNow("test")
	reported(t, c,
		`{"t":"input","name":"pm8941_pwrkey","kind":"keyboard","added":true,"ok":false,"id":2}`,
		`{"t":"input","name":"Stick","kind":"keyboard","added":true,"ok":true,"id":3,"bus":"usb"}`)
	if c.inputs.byID[1].locked || c.inputs.byID[2].locked || !c.inputs.byID[3].locked {
		t.Fatalf("locked: desk %v, power key %v, stick %v", c.inputs.byID[1].locked, c.inputs.byID[2].locked, c.inputs.byID[3].locked)
	}
	reported(t, c, `{"t":"key","key":"call","text":"","dev":3}`)
	if len(c.overlays) != 0 {
		t.Fatalf("the stick's call key opened %T", c.overlays[0])
	}
	if c.toast == nil || c.toast.body != tr.T("This keyboard was plugged in while the phone was locked: it only enters the PIN until you unlock") {
		t.Errorf("toast %+v", c.toast)
	}
	reported(t, c, `{"t":"key","key":"","text":"4","dev":3}`)
	pad, ok := overlayOf[*lockPadOverlay](c)
	if !ok || pad.digits != "4" {
		t.Fatalf("the stick's digit: pad %+v", pad)
	}
	for _, k := range []Key{Runes("#"), Named(KeyCall), Named(KeyTab), Runes("a")} {
		k.Dev = 3
		c.Press(k)
		if p, ok := overlayOf[*lockPadOverlay](c); !ok || p != pad || pad.digits != "4" || pad.note != "" {
			t.Fatalf("the stick's %s reached the pad: %+v", keyLabel(k.Name), pad)
		}
	}
	c.Press(Key{Name: KeyBackspace, Dev: 3})
	if pad.digits != "" {
		t.Errorf("the stick's backspace: %q", pad.digits)
	}
	c.nfc.powerAt = time.Time{}
	c.Press(Key{Name: KeyPower, Dev: 3})
	if !c.nfc.powerAt.IsZero() || c.ScreenOff() {
		t.Error("the stick's power key was taken")
	}
	// The desk keyboard, plugged in before the lock, is the owner's.
	c.Press(Key{Name: KeyCall, Dev: 1})
	if pad.note != tr.T("Only emergency numbers can be called while locked") {
		t.Errorf("the desk keyboard's call key: note %q", pad.note)
	}
	c.Press(Key{Name: KeyEsc, Dev: 3})
	if _, ok := overlayOf[*lockPadOverlay](c); ok {
		t.Error("the stick's Back left the pad up")
	}
	c.unlockNow("test")
	if c.inputs.byID[3].locked {
		t.Error("still held back after the unlock")
	}
	c.lockNow("test")
	if c.lockedOut(Key{Name: KeyCall, Dev: 3}) {
		t.Error("held back again at the next lock")
	}
	reported(t, c, `{"t":"input","name":"Stick","kind":"keyboard","added":false,"ok":true,"id":3,"bus":"usb"}`)
	if c.inputs.byID[3] != nil {
		t.Error("an unplugged device kept")
	}
}

// Under the keypad lock (no PIN) such a keyboard has only the unlock
// sequence; a Bluetooth one counts as USB does; a renderer that numbers no
// devices holds nothing back.
func TestKeyboardPluggedInUnderTheKeypadLock(t *testing.T) {
	c, _ := lockCore(t)
	c.lockNow("test")
	c.inputAdded(5, "keyboard", "BT Keyboard", "bluetooth", true)
	c.inputAdded(6, "mouse", "Wheel", "usb", true)
	c.inputAdded(0, "keyboard", "Old renderer", "usb", true)
	for _, k := range []Key{Runes("1"), Named(KeyCall), Named(KeyF2), Named(KeyDown)} {
		k.Dev = 5
		if !c.lockedOut(k) {
			t.Errorf("%s from the Bluetooth keyboard was let through", keyLabel(k.Name))
		}
	}
	if c.lockedOut(Key{Name: KeyTab, Dev: 5}) || c.lockedOut(Key{Name: KeyRunes, Runes: []rune("*"), Dev: 6}) {
		t.Error("the unlock sequence was held back")
	}
	if !c.lockedOut(Key{Name: KeyDown, Dev: 6}) {
		t.Error("the mouse's wheel was let through")
	}
	if c.lockedOut(Key{Name: KeyCall}) {
		t.Error("a key of no device was held back")
	}
}

// The toast says what is held back: a mouse is no keyboard. Devices held
// back together (a stick that is a keyboard and a mouse) get one toast,
// which names no kind when they are of several.
func TestHeldBackToastNamesTheDevice(t *testing.T) {
	c, _ := lockCore(t)
	c.lockNow("test")
	c.inputAdded(1, "mouse", "USB Optical Mouse", "usb", true)
	if !c.lockedOut(Key{Name: KeyDown, Dev: 1}) {
		t.Fatal("the mouse's wheel was let through")
	}
	if c.toast == nil || c.toast.body != tr.T("This mouse was plugged in while the phone was locked: it works once you unlock") {
		t.Errorf("toast for the mouse %+v", c.toast)
	}
	c.unlockNow("test")
	c.lockNow("test")
	c.toast = nil
	c.inputAdded(2, "keyboard", "Stick", "usb", true)
	c.inputAdded(3, "mouse", "Stick", "usb", true)
	if !c.lockedOut(Key{Name: KeyCall, Dev: 2}) {
		t.Fatal("the stick's call key was let through")
	}
	if c.toast == nil || c.toast.body != tr.T("This input device was plugged in while the phone was locked: it is limited until you unlock") {
		t.Errorf("toast for a keyboard and a mouse %+v", c.toast)
	}
	c.toast = nil
	if !c.lockedOut(Key{Name: KeyDown, Dev: 3}) || c.toast != nil {
		t.Errorf("the stick's mouse: a second toast %+v", c.toast)
	}
}

// A touchscreen plugged in while the phone is locked touches nothing until
// the unlock: not the PIN pad's Back, not between the touches of the
// phone's own touchscreen, which works throughout, as touches no device is
// named for do (an older renderer's). After the unlock, with two
// touchscreens, the finger of the one that came down is followed.
func TestTouchscreenPluggedInLockedTouchesNothing(t *testing.T) {
	c := menuCore(t)
	c.lock.pin = true
	c.gui = &GUI{c: c, info: RendererInfo{Features: []string{"touch"}}}
	reported(t, c, `{"t":"input","name":"synaptics_dsx","kind":"touchscreen","added":true,"ok":true,"id":1}`)
	c.lockNow("test")
	reported(t, c, `{"t":"input","name":"USB Touch Panel","kind":"touchscreen","added":true,"ok":true,"id":2,"bus":"usb"}`)
	if c.inputs.byID[1].locked || !c.inputs.byID[2].locked {
		t.Fatalf("locked: own %v, plugged in %v", c.inputs.byID[1].locked, c.inputs.byID[2].locked)
	}
	back := func(dev int) []string {
		return []string{
			fmt.Sprintf(`{"t":"touch","phase":"down","x":120,"y":20,"hit":{"area":"outside","kind":"auth"},"dev":%d}`, dev),
			fmt.Sprintf(`{"t":"touch","phase":"up","x":120,"y":20,"dev":%d}`, dev),
		}
	}
	c.Press(Runes("1"))
	reported(t, c, back(2)...)
	pad, ok := overlayOf[*lockPadOverlay](c)
	if !ok || pad.digits != "1" {
		t.Fatalf("the plugged-in touchscreen's Back reached the pad: %+v", pad)
	}
	if c.toast == nil || c.toast.body != tr.T("This input device was plugged in while the phone was locked: it is limited until you unlock") {
		t.Errorf("toast %+v", c.toast)
	}
	own := back(1)
	reported(t, c, own[0],
		`{"t":"touch","phase":"down","x":200,"y":300,"hit":{"area":"soft","soft":1},"dev":2}`,
		`{"t":"touch","phase":"up","x":200,"y":300,"dev":2}`,
		own[1])
	if _, ok := overlayOf[*lockPadOverlay](c); ok || pad.note != "" {
		t.Fatalf("the phone's own Back, with the other's tap between: pad up %v, note %q", ok, pad.note)
	}
	c.Press(Runes("1"))
	reported(t, c, `{"t":"touch","phase":"down","x":120,"y":20,"hit":{"area":"outside","kind":"auth"}}`, `{"t":"touch","phase":"up","x":120,"y":20}`)
	if _, ok := overlayOf[*lockPadOverlay](c); ok {
		t.Fatal("a touch of no device did not reach the pad")
	}
	c.unlockNow("test")
	reported(t, c, `{"t":"touch","phase":"down","x":100,"y":100,"hit":{"area":"idle"},"dev":2}`)
	if c.inputs.byID[2].locked || !c.touch.down || c.touch.dev != 2 {
		t.Fatalf("after the unlock: held back %v, touch down %v on %d", c.inputs.byID[2].locked, c.touch.down, c.touch.dev)
	}
	reported(t, c, `{"t":"touch","phase":"move","x":160,"y":100,"dev":1}`, `{"t":"touch","phase":"up","x":160,"y":100,"dev":1}`)
	if !c.touch.down || c.touch.dev != 2 || c.touch.moved {
		t.Errorf("the other touchscreen's finger moved this one's: down %v on %d, moved %v", c.touch.down, c.touch.dev, c.touch.moved)
	}
}
