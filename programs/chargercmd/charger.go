package chargercmd

import (
	"fmt"
	"time"

	"github.com/kario-os/kario/hal/power"
	"github.com/kario-os/kario/shell"
)

// screenTime: the screen goes off this long after it lit. unplugGrace: a
// charger gone this long powers the phone off (a cable that slipped and
// went back in keeps it charging). holdToStart: the power key held this
// long starts the system (Android's charger takes about as long).
const (
	screenTime  = 5 * time.Second
	unplugGrace = 3 * time.Second
	holdToStart = 2 * time.Second
)

// bootMinLevel: below this level, or while the gauge says Critical, a
// hold does not start the system but waits for the level. The gauge's 0%
// is already its empty voltage under load; the system's start (the
// modem's boot, the panel, every service) draws more than a computer's
// port gives, so the battery makes up the rest, and a phone at 0-2% would
// die on the way and be started by its charger again, over and over.
const bootMinLevel = 3

// Sleep with the screen off: not within sleepAfter of a key or of the
// screen going off; awake awakeAfterWake after a wake (the key that woke
// it and the supplies' uevents come after the thaw, and the bq27xxx gauge
// answers from a reading up to 5 s old by the kernel's tick, which stops
// in suspend); the RTC wakes it every refresh to read the battery (full,
// a charger gone that woke nothing), every startRefresh while a start
// waits for the level.
const (
	sleepAfter     = 5 * time.Second
	awakeAfterWake = 6 * time.Second
	refresh        = 10 * time.Minute
	startRefresh   = time.Minute
)

// The ways out of charger mode.
const (
	actReboot   = "reboot"
	actPoweroff = "poweroff"
)

// looks are the standby screen's words (shell.Standby).
type looks interface {
	Clock(t time.Time) string
	Date(t time.Time) string
	Theme() string
}

// screen is what the charger shows on: the renderer's frame and the
// panel's power.
type screen interface {
	Frame(f shell.Frame)
	Screen(on bool)
}

// charger is charger mode's state: the battery, the screen and how it
// ends. Its methods are the events; the caller brings the time.
type charger struct {
	looks  looks
	screen screen
	// act starts a way out (actReboot, actPoweroff); an error leaves the
	// charger as it was, so that the next press tries again.
	act func(what string) error
	log func(format string, a ...any)

	sup     power.Supply
	battery bool
	gauge   power.Gauge
	on      bool
	offAt   time.Time
	// unplugged: when the charger went (zero while one is plugged in).
	unplugged time.Time
	leaving   string
	// pending: held while the battery was too low; the system starts once
	// it is not.
	pending bool
	// keyAt: the last power key; darkAt: when the screen last went off;
	// woke: the last wake from a suspend.
	keyAt, darkAt, woke time.Time
}

// begin: the renderer is up with the panel on.
func (c *charger) begin(now time.Time, sup power.Supply, battery bool, g power.Gauge) {
	c.sup, c.battery, c.gauge = sup, battery, g
	if battery && !sup.Charging {
		c.unplugged = now
		c.log("no charger at the start: powering off in %s unless one is plugged in", unplugGrace)
	}
	c.light(now)
}

// supply: the power supplies changed, or were read again.
func (c *charger) supply(now time.Time, sup power.Supply, battery bool, g power.Gauge) {
	was := c.sup
	c.sup, c.battery, c.gauge = sup, battery, g
	switch {
	case battery && was.Charging && !sup.Charging && c.unplugged.IsZero():
		c.unplugged = now
		c.log("charger unplugged: powering off in %s unless it comes back", unplugGrace)
		c.light(now)
	case battery && sup.Charging && !c.unplugged.IsZero():
		c.unplugged = time.Time{}
		c.log("charger back: charging")
		c.light(now)
	case c.pending && c.leaving == "" && c.unplugged.IsZero() && !c.tooLow():
		c.log("battery at %d%%: starting", c.sup.Level)
		c.leave(now, actReboot)
	case c.on:
		c.draw(now)
	}
}

// key: the power key, a short press or held for holdToStart.
func (c *charger) key(now time.Time, long bool) {
	c.keyAt = now
	switch {
	case c.leaving != "":
	case long && c.tooLow():
		if !c.pending {
			c.log("battery at %d%%: too low to start; starting at %d%%", c.sup.Level, bootMinLevel)
		}
		c.pending = true
		c.light(now)
	case long:
		c.leave(now, actReboot)
	case c.on:
		c.dark(now)
	default:
		c.light(now)
	}
}

// tick: time passed; the screen times out, an unplugged charger stays
// gone, the clock moves on.
func (c *charger) tick(now time.Time) {
	switch {
	case c.leaving != "":
	case !c.unplugged.IsZero() && !now.Before(c.unplugged.Add(unplugGrace)):
		c.leave(now, actPoweroff)
	case c.on && c.unplugged.IsZero() && !now.Before(c.offAt):
		c.dark(now)
	case c.on:
		c.draw(now)
	}
}

// busy: something is due soon (the screen is on, or the phone powers off
// shortly), so ticks come every second rather than for the slow poll.
func (c *charger) busy() bool { return c.on || !c.unplugged.IsZero() }

// tooLow: the battery is too low to start the system (bootMinLevel). A
// board without a battery (on a bench supply) never is.
func (c *charger) tooLow() bool {
	return c.battery && (c.gauge.Critical || !c.gauge.NoCapacity && c.sup.Level < bootMinLevel)
}

// sleepPlan is when the phone must be awake again (its RTC wake) if it
// suspends now, or what keeps it awake.
func (c *charger) sleepPlan(now time.Time) (time.Time, string) {
	switch {
	case c.leaving != "":
		return time.Time{}, c.leaving
	case c.on:
		return time.Time{}, "screen on"
	case !c.unplugged.IsZero():
		return time.Time{}, "charger unplugged"
	case c.sup.Detecting:
		return time.Time{}, "charger being identified"
	case now.Sub(c.keyAt) < sleepAfter:
		return time.Time{}, "key"
	case now.Sub(c.darkAt) < sleepAfter:
		return time.Time{}, "screen just off"
	case now.Sub(c.woke) < awakeAfterWake:
		return time.Time{}, "just woke"
	case c.pending:
		return now.Add(startRefresh), ""
	}
	return now.Add(refresh), ""
}

// woken: the phone runs again after a suspend (or one called off).
func (c *charger) woken(now time.Time) { c.woke = now }

func (c *charger) light(now time.Time) {
	c.offAt = now.Add(screenTime)
	c.draw(now)
	if !c.on {
		c.on = true
		c.screen.Screen(true)
	}
}

func (c *charger) dark(now time.Time) {
	if c.on {
		c.on, c.darkAt = false, now
		c.screen.Screen(false)
	}
}

func (c *charger) leave(now time.Time, what string) {
	c.leaving = what
	c.light(now)
	c.log("%s", what)
	if err := c.act(what); err != nil {
		c.log("%s: %v", what, err)
		c.leaving = ""
		c.draw(now)
	}
}

func (c *charger) draw(now time.Time) {
	hint := tr.T("Hold the power key to start")
	switch {
	case c.leaving == actReboot:
		hint = tr.T("Starting…")
	case c.leaving == actPoweroff:
		hint = tr.T("Powering off…")
	case c.pending:
		hint = tr.T("Starting at %d%%", bootMinLevel)
	}
	lines := []string{c.line()}
	if l := c.current(); l != "" {
		lines = append(lines, l)
	}
	f := shell.Frame{T: "frame", Screen: "charger", Body: []shell.FrameNode{}, Theme: c.looks.Theme(),
		Status: shell.FrameStatus{Time: c.looks.Clock(now), Battery: c.sup.Level, Charging: c.sup.Charging && c.battery, Percent: true},
		Idle:   &shell.FrameIdle{Time: c.looks.Clock(now), Date: c.looks.Date(now), Lines: lines, Hint: hint}}
	c.screen.Frame(f)
}

// line says what the battery is doing.
func (c *charger) line() string {
	level := c.sup.Level
	switch {
	case !c.battery:
		return tr.T("No battery found")
	case !c.unplugged.IsZero():
		return tr.T("Charger unplugged: powering off")
	case c.tooLow() && c.gauge.NoCapacity:
		return tr.T("Charging - too low to start")
	case c.tooLow():
		return tr.T("Charging, %d%% - too low to start", level)
	case c.gauge.Status == "Full":
		return tr.T("Full")
	case c.gauge.NoCapacity && c.sup.Fast:
		return tr.T("Fast charging")
	case c.gauge.NoCapacity:
		return tr.T("Charging")
	case c.sup.Fast:
		return tr.T("Fast charging, %d%%", level)
	}
	return tr.T("Charging, %d%%", level)
}

// current is the charge current when the battery reports one, as a size:
// drivers differ in its sign.
func (c *charger) current() string {
	if !c.battery || !c.sup.Charging || !c.unplugged.IsZero() || !c.gauge.HasCurrent {
		return ""
	}
	ua := c.gauge.Current
	if ua < 0 {
		ua = -ua
	}
	return fmt.Sprintf("%d mA", (ua+500)/1000)
}
