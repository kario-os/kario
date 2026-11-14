package shell

import (
	"strconv"

	halpower "github.com/kario-os/kario/hal/power"
)

// Power is the battery as the scheduler and the status bar see it.
type Power struct {
	Level    int  // percent
	Charging bool // on external power (charging or full)
	// Fast: the charger negotiated more than USB's default (USB PD, a
	// dedicated charger: hal/power).
	Fast bool
	// Simulated: no battery in this machine (a developer machine, QEMU); the
	// values are a stand-in a test may change.
	Simulated bool
}

var simulatedPower = Power{Level: 82, Simulated: true}

func (c *Core) power() Power {
	if c.powerOverride != nil {
		return *c.powerOverride
	}
	if s, ok := halpower.Read(); ok {
		return Power{Level: s.Level, Charging: s.Charging, Fast: s.Fast}
	}
	return simulatedPower
}

// SetPower stands in for the battery (tests): level and charging.
func (c *Core) SetPower(p Power) {
	p.Simulated = true
	charging := c.power().Charging
	c.powerOverride = &p
	if p.Charging && !charging {
		c.runJobs()
	}
}

func (c *Core) batteryText() string {
	p := c.power()
	if p.Charging {
		return "⚡" + strconv.Itoa(p.Level) + "%"
	}
	return strconv.Itoa(p.Level) + "%"
}
