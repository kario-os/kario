package shell

import (
	"strconv"
	"sync/atomic"

	halpower "github.com/kario-os/kario/hal/power"
)

// Power is the battery as the scheduler and the status bar see it.
type Power struct {
	Level    int  // percent
	Charging bool // on external power (charging or full)
	// Fast: the charger negotiated more than USB's default (USB PD, a
	// dedicated charger: hal/power).
	Fast bool
	// Detecting: the charger just plugged in is not known yet (Fast is not).
	Detecting bool
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
		return Power{Level: s.Level, Charging: s.Charging, Fast: s.Fast, Detecting: s.Detecting}
	}
	return simulatedPower
}

// watchSupplies is the kernel's power supply uevents (tests replace it).
var watchSupplies = halpower.Watch

// watchPower: on a phone with a battery the kernel's word that a supply
// changed (the charger plugged in or out) has the charger checked at once,
// not at the next housekeeping (every offTick with the screen off); a
// plug that wakes the phone from suspend (the PMI8998's cable interrupt
// is a wake-up source) lights the screen as soon as the Shell runs again.
func (c *Core) watchPower(sku SKU) {
	if sku.ID == "" {
		return
	}
	if _, ok := halpower.Read(); !ok {
		return
	}
	var due atomic.Bool
	err := watchSupplies(func() {
		if due.CompareAndSwap(false, true) {
			c.Post(func() {
				due.Store(false)
				c.checkCharger()
			})
		}
	})
	if err != nil {
		c.log.Printf("power: no uevents (%v): the charger is checked with the housekeeping", err)
	}
}

// SetPower stands in for the battery (tests): level and charging.
func (c *Core) SetPower(p Power) {
	p.Simulated = true
	charging := c.power().Charging
	c.powerOverride = &p
	if p.Charging != charging {
		c.runJobs()
	}
}

// batteryText is the text renderer's battery: its percentage, or with the
// percentage turned off (battery_percent) a battery sign.
func (c *Core) batteryText() string {
	p := c.shownPower()
	level := strconv.Itoa(p.Level) + "%"
	if c.settings.HidePercent {
		level = "🔋"
	}
	if p.Charging {
		return "⚡" + level
	}
	return level
}
