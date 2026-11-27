package shell

import "errors"

// A flip's lid (the SKU's lid: a Hall switch reporting SW_LID), as the
// pixel renderer reports it (feature and message "lid",
// 23-renderer-protocol.md); under a renderer that does not, it is open
// (lidReader). Shutting it turns the inner display off and locks as the
// screen going off does, and silences a ringing call. While it is shut
// the inner display stays off (Wake), the keypad under it counts for
// nothing (lidDrops), the power key lights nothing (powerShut), and the
// maintenance wake waits for the push heartbeat's (sleep.go). Opening it
// lights the display as a key would; without a PIN it also releases the
// keypad lock, with one it asks for the PIN (24-sku-framework.md
// §9.10.8). The outer display shows nothing yet (outer.go).
type lidState struct {
	shut bool
	// dropped: the keys dropped under the lid since it shut.
	dropped int
}

// lidSwitched takes the lid switch's position; only a change acts.
func (c *Core) lidSwitched(shut bool) {
	if c.lid.shut == shut {
		return
	}
	c.lid.shut = shut
	c.screenLog("lid: %s", map[bool]string{true: "shut", false: "open"}[shut])
	if c.cfg.SKU.Lid == "" {
		return
	}
	if shut {
		c.lidClosed()
	} else {
		c.lidOpened()
	}
}

func (c *Core) lidClosed() {
	c.lid.dropped = 0
	if c.ringing() {
		c.silenceRing("the lid")
	}
	c.screenOff("lid shut")
	c.lockTick()
}

func (c *Core) lidOpened() {
	if n := c.lid.dropped; n > 0 {
		c.screenLog("lid: %d keys pressed under it dropped", n)
		c.lid.dropped = 0
	}
	// A lock due while the lid was shut comes before the screen lights,
	// and the keypad's auto-lock counts from the opening.
	c.lockTick()
	c.lastKey = c.now()
	c.Wake("lid open")
	switch {
	case c.keylock && !c.lock.pin:
		c.unlockNow("lid open")
	case c.secureLocked() && len(c.overlays) == 0 && !c.anyCall():
		c.openLockPad("", false)
	}
}

// lidReader: a renderer has started; one that does not report the lid
// (feature "lid") leaves it open, as nothing would tell when it opens.
func (c *Core) lidReader(reports bool) {
	if reports || !c.lid.shut {
		return
	}
	c.lid.shut = false
	c.screenLog("lid: the renderer does not report it; open")
}

// SetLid shuts or opens the lid for the selftest, as the renderer reports
// it; only on a SKU with a lid.
func (c *Core) SetLid(shut bool) error {
	if c.cfg.SKU.Lid == "" {
		return errors.New("lid: the SKU has no lid (sku.json's lid)")
	}
	c.lidSwitched(shut)
	return nil
}

// lidShut: the phone has a lid, and it is shut.
func (c *Core) lidShut() bool { return c.cfg.SKU.Lid != "" && c.lid.shut }

// lidDrops drops a key pressed under the shut lid: the keypad's, which the
// lid can press. The power key, the side keys (volume), a headset's media
// keys and keys the computer sends still count. So would a keyboard's
// plugged in, but keys do not say which device they came from: its keys
// are dropped too.
func (c *Core) lidDrops(k Key) bool {
	if !c.lidShut() || k.Injected {
		return false
	}
	switch k.Name {
	case KeyPower, KeyVolUp, KeyVolDown, KeyA11y, KeyPlayPause, KeyNextTrack, KeyPrevTrack:
		return false
	}
	if c.lid.dropped == 0 {
		c.screenLog("lid: keys pressed under the shut lid are dropped")
	}
	c.lid.dropped++
	return true
}

// powerShut is the power key under the shut lid: it silences a ringing
// call or snoozes a ringing alarm, as with the lid open, and otherwise
// does nothing: the inner display stays off, and the power menu, which
// nothing could answer there, stays closed. It is to refresh the outer
// display (§9.10.9), which is not drawn yet.
func (c *Core) powerShut() string {
	switch {
	case c.ringing():
		c.silenceRing("the power key")
		return "the ring silenced (the lid is shut)"
	case c.alarmOnTop() != nil:
		c.alarmOnTop().snooze(c)
		return "the alarm snoozed (the lid is shut)"
	}
	return "nothing: the lid is shut"
}
