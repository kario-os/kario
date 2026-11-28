package shell

import (
	"testing"
	"time"

	"github.com/kario-os/kario/hal/vibrator"
)

// The motor pulses while an alarm rings, silent mode or not; a ringing
// call only in silent mode; and the pulses go to the motor.
func TestVibrateRinging(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		sim := &vibrator.Sim{}
		c.screen.motor = sim
		pulse := func() int {
			c.lastPulse = time.Time{}
			before := c.Vibrations()
			c.vibrateRinging()
			return c.Vibrations() - before
		}
		c.sysWant = map[string]bool{"ring:1": true}
		if n := pulse(); n != 0 {
			t.Errorf("a ringing call vibrated outside silent mode")
		}
		c.settings.Silent = true
		if n := pulse(); n != 1 {
			t.Errorf("a ringing call in silent mode: %d pulses", n)
		}
		c.settings.Silent = false
		c.sysWant = map[string]bool{"alarm:a1": true}
		if n := pulse(); n != 1 {
			t.Errorf("a ringing alarm: %d pulses", n)
		}
		c.vibrateRinging()
		if n, total := sim.Count(); n != 2 || total != 1600*time.Millisecond {
			t.Errorf("the motor: %d pulses, %v (pulses come 1.5 s apart)", n, total)
		}
		return nil
	})
}
