package shell

import (
	"math"

	"github.com/kario-os/kario/ui"
)

// Automatic brightness follows the ambient light sensor (hal/sensor). The
// brightness set is then the level in an ordinary room (300 lux): a dark
// room scales it down to a third, daylight up to two and a half times,
// within MinBrightness and 100. The reading is smoothed, faster when it
// gets brighter (stepping outside must not leave the screen unreadable).
// The level stays put while the light stays within a quarter of what it
// was set for, so a hand's shadow does not make it flicker; beyond that it
// follows the smoothed reading and, once the light has settled, lands on
// the level for it. In daylight the keyboard's backlight goes off,
// automatic brightness or not: it cannot be seen there.

var luxGain = [...]struct{ lux, gain float64 }{
	{0, 0.3}, {10, 0.5}, {100, 0.8}, {300, 1}, {1000, 1.3}, {3000, 1.7}, {10000, 2.5},
}

// The smoothing and the bands work on the logarithm of the light (plus one
// lux), as the eye sees it: from daylight back indoors takes as long as
// from indoors into the dark.
var (
	// The light moves the level once it is a quarter away from the light
	// the level was set for, and has settled within a tenth of the
	// smoothed light.
	luxBand    = math.Log10(1.25)
	luxSettled = math.Log10(1.1)
)

const (
	brightenAlpha = 0.5
	darkenAlpha   = 0.25
	// The keyboard light goes off above daylightOn lux and comes back
	// below daylightOff.
	daylightOn  = 1000
	daylightOff = 600
)

func logLux(lux float64) float64 { return math.Log10(lux + 1) }
func unlog(l float64) float64    { return math.Pow(10, l) - 1 }

// autoLevel is the backlight for the brightness set at lux, interpolated
// on the logarithm of the light.
func autoLevel(set int, lux float64) int {
	x := math.Log10(lux + 1)
	gain := luxGain[len(luxGain)-1].gain
	for i := 1; i < len(luxGain); i++ {
		if lux <= luxGain[i].lux {
			x0, x1 := math.Log10(luxGain[i-1].lux+1), math.Log10(luxGain[i].lux+1)
			gain = luxGain[i-1].gain + (luxGain[i].gain-luxGain[i-1].gain)*(x-x0)/(x1-x0)
			break
		}
	}
	return min(max(int(math.Round(float64(set)*gain)), ui.MinBrightness), 100)
}

// level is the backlight the screen gets when lit and not dimmed.
func (c *Core) level() int {
	if c.settings.AutoBrightness && c.screen.autoLevel > 0 {
		return c.screen.autoLevel
	}
	return c.brightness()
}

// senseLight reads the light sensor while the screen is on (housekeeping).
func (c *Core) senseLight() {
	s := &c.screen
	if s.als == nil || s.off {
		return
	}
	lux, err := s.als.Lux()
	if err != nil {
		if !s.alsFailed {
			s.alsFailed = true
			c.log.Printf("light sensor %s: %v", s.als.Name(), err)
		}
		return
	}
	if s.lux < 0 {
		s.lux = lux
	} else {
		a := darkenAlpha
		if lux > s.lux {
			a = brightenAlpha
		}
		s.lux = unlog(logLux(s.lux) + a*(logLux(lux)-logLux(s.lux)))
	}
	changed := false
	if day := s.lux >= daylightOn || (s.daylight && s.lux >= daylightOff); day != s.daylight {
		s.daylight, changed = day, true
	}
	if c.settings.AutoBrightness {
		if s.autoLevel == 0 {
			c.resetAutoLevel()
			changed = true
		}
		if !s.adjusting && math.Abs(logLux(s.lux)-logLux(s.autoLux)) > luxBand {
			s.adjusting = true
		}
		if s.adjusting {
			at := s.lux
			if math.Abs(logLux(lux)-logLux(s.lux)) <= luxSettled {
				s.lux, s.autoLux, s.adjusting, at = lux, lux, false, lux
			}
			if l := autoLevel(c.brightness(), at); l != s.autoLevel {
				s.autoLevel, changed = l, true
			}
		}
	}
	if changed {
		c.applyBrightness()
	}
}

// resetAutoLevel sets the automatic level for the light now, at once (the
// switch or the brightness set changed).
func (c *Core) resetAutoLevel() {
	s := &c.screen
	s.autoLevel, s.adjusting = 0, false
	if c.settings.AutoBrightness && s.lux >= 0 {
		s.autoLevel, s.autoLux = autoLevel(c.brightness(), s.lux), s.lux
	}
}

// Lux is the smoothed light sensor reading, -1 before the first (selftest).
func (c *Core) Lux() float64 { return c.screen.lux }

// SetLux makes the simulated light sensor read lux (selftest); false when
// the sensor is a real one.
func (c *Core) SetLux(lux float64) bool {
	if sim, ok := c.screen.als.(interface{ Set(float64) }); ok && c.screen.alsSim {
		sim.Set(lux)
		return true
	}
	return false
}
