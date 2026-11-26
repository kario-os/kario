package shell

import (
	"testing"
	"time"

	"github.com/kario-os/kario/hal/vibrator"
)

// --stay-on: neither the timeout nor the power key turns the screen off or
// dims it, so the backlight stays lit and the renderer never powers the
// panel down. Without it the same Shell goes off.
func TestStayOn(t *testing.T) {
	idle := func(c *Core) {
		c.SetScreenAuto(true)
		c.screen.active = c.now().Add(-time.Hour)
		c.lastKey = c.screen.active
		c.checkScreen()
	}
	c := snapCore(t)
	idle(c)
	if !c.ScreenOff() {
		t.Fatal("an idle screen stayed on without the switch")
	}

	c = snapCore(t)
	c.StayOn()
	idle(c)
	if c.ScreenOff() || c.screen.dimmed || c.Backlight() <= 0 {
		t.Errorf("idle: off %v, dimmed %v, backlight %d", c.ScreenOff(), c.screen.dimmed, c.Backlight())
	}
	c.PowerKey(false)
	if c.ScreenOff() || c.Backlight() <= 0 {
		t.Errorf("the power key turned it off: backlight %d", c.Backlight())
	}
}

// --no-haptics: nothing reaches the motor; the requests are still counted.
func TestNoHaptics(t *testing.T) {
	c := snapCore(t)
	m := &vibrator.Sim{}
	c.screen.motor = m
	c.vibrateMS(300)
	c.NoHaptics()
	c.vibrateMS(300)
	if n, _ := m.Count(); n != 1 || c.Vibrations() != 2 {
		t.Errorf("the motor ran %d times for %d requests", n, c.Vibrations())
	}
}
