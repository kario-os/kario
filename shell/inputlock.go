package shell

import "unicode"

// A keyboard or mouse plugged in while the phone is locked (on USB or
// Bluetooth, the buses anyone can plug into; the phone's own keys are on
// others) types only into the lock screen until the phone is next
// unlocked: digits, Backspace, OK and Back on the PIN pad, and the keypad
// lock's unlock sequence. Not the call key (emergency calls), not # (erase
// the phone after wrong PINs), not the lock screen's shortcuts, nothing
// over the lock, so a device that types by itself (a BadUSB stick) can do
// no more than guess the PIN, which phone-keystored makes wait. A
// touchscreen plugged in while locked touches nothing, not even the PIN
// pad's bar or a call ringing in. The renderer numbers each device and
// names its bus, and every key and touch carries its device's number
// (Key.Dev, the touch's dev); one with none (a terminal, a selftest, the
// computer's, which input.go refuses, an older renderer's) is not a
// device's.

// inputDevice is one device the renderer reported.
type inputDevice struct {
	name, kind, bus string
	// locked: plugged in while the phone was locked; told: the user was
	// told why its keys go nowhere.
	locked, told bool
}

// externalBus: a bus a stranger's device can come on.
func externalBus(bus string) bool { return bus == "usb" || bus == "bluetooth" }

// inputAdded notes a device the renderer reported (id 0: a renderer that
// numbers none).
func (c *Core) inputAdded(id int, kind, name, bus string, added bool) {
	if id == 0 {
		return
	}
	in := &c.inputs
	if !added {
		delete(in.byID, id)
		return
	}
	if in.byID == nil {
		in.byID = map[int]*inputDevice{}
	}
	// A lock due while the screen was off counts.
	c.lockTick()
	d := &inputDevice{name: name, kind: kind, bus: bus, locked: c.locked() && externalBus(bus)}
	in.byID[id] = d
	if d.locked {
		c.log.Printf("input: %s %q plugged in while locked: it types only into the lock screen until the phone is unlocked", kind, name)
	}
}

// inputsUnlocked: the phone was unlocked; every device types everywhere.
func (c *Core) inputsUnlocked() {
	for _, d := range c.inputs.byID {
		if d.locked {
			d.locked = false
			c.log.Printf("input: %s %q may type everywhere (unlocked)", d.kind, d.name)
		}
	}
}

// heldBack is device dev if it was plugged in while locked and the phone
// still is.
func (c *Core) heldBack(dev int) *inputDevice {
	d := c.inputs.byID[dev]
	if dev == 0 || d == nil || !d.locked {
		return nil
	}
	if !c.locked() {
		c.inputsUnlocked()
		return nil
	}
	return d
}

// lockedOut: k comes from a device plugged in while locked and is not
// for the lock screen's PIN; it is dropped.
func (c *Core) lockedOut(k Key) bool {
	d := c.heldBack(k.Dev)
	if d == nil || c.lockScreenKey(k) {
		return false
	}
	// What was typed stays out of the log: a key's name, not its text.
	c.log.Printf("input: %s from %q dropped (plugged in while locked)", k.Name, d.name)
	c.tellHeldBack(d)
	return true
}

// touchLockedOut: a touch (each phase of it) from touchscreen dev, plugged
// in while locked; it is dropped.
func (c *Core) touchLockedOut(dev int, phase string) bool {
	d := c.heldBack(dev)
	if d == nil {
		return false
	}
	// Where it touched stays out of the log, as a key's text does.
	if phase == "down" {
		c.log.Printf("input: a touch from %q dropped (plugged in while locked)", d.name)
		c.tellHeldBack(d)
	}
	return true
}

// tellHeldBack says once why d's input goes nowhere, and of the other
// devices held back with it (one keyboard is often several devices, a
// stick a keyboard and a mouse) in the same toast: what they are when
// they are all one kind.
func (c *Core) tellHeldBack(d *inputDevice) {
	if d.told {
		return
	}
	kind := d.kind
	for _, o := range c.inputs.byID {
		if o.locked && !o.told {
			o.told = true
			if o.kind != kind {
				kind = ""
			}
		}
	}
	switch kind {
	case "keyboard":
		c.showToast(tr.T("This keyboard was plugged in while the phone was locked: it only enters the PIN until you unlock"))
	case "mouse":
		c.showToast(tr.T("This mouse was plugged in while the phone was locked: it works once you unlock"))
	default:
		c.showToast(tr.T("This input device was plugged in while the phone was locked: it is limited until you unlock"))
	}
}

// lockScreenKey: k only enters or clears the PIN, or unlocks the keypad
// lock.
func (c *Core) lockScreenKey(k Key) bool {
	digits := func() bool {
		rs := c.fnRunes(k.Runes)
		if k.Name != KeyRunes || len(rs) == 0 {
			return false
		}
		for _, r := range rs {
			if !unicode.IsDigit(r) {
				return false
			}
		}
		return true
	}
	if n := len(c.overlays); n > 0 {
		if _, ok := c.overlays[n-1].(*lockPadOverlay); !ok {
			return false
		}
		return digits() || k.Name == KeyBackspace || k.Name == KeyEnter || k.Name == KeyEsc
	}
	if c.lock.pin {
		return digits() || k.Name == KeyEnter
	}
	return k.Name == KeyTab || k.Name == KeyRunes && string(k.Runes) == "*"
}
