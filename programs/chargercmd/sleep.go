package chargercmd

import (
	"errors"
	"strings"
	"time"

	"github.com/kario-os/kario/hal/pm"
)

// dozer suspends the phone through the kernel (hal/pm) while the
// charger's plan lets it: the power key, a charger's plug or unplug and
// the RTC wake it.
type dozer struct {
	sys pm.Sleeper
	say func(string, ...any)
	// why keeps the phone awake now; off: why it no longer suspends.
	why    string
	off    string
	sleeps int
}

// awake: the board has no sleep, or it stopped suspending.
func (d *dozer) awake() bool { return d.off != "" }

// due: the phone may suspend now, by the charger's plan, the panel (off
// and answered) and the wake locks; what keeps it awake is logged once
// each time it changes, but for the moments every stretch has.
func (d *dozer) due(c *charger, o *output, now time.Time) bool {
	if d.awake() {
		return false
	}
	_, why := c.sleepPlan(now)
	switch {
	case why != "":
	case !o.settled():
		why = "panel not off"
	default:
		if locks := d.sys.WakeLocks(); len(locks) > 0 {
			why = "wake lock " + strings.Join(locks, " ")
		}
	}
	switch why {
	case "", d.why, "screen on", "key", "screen just off", "just woke", actReboot, actPoweroff:
	default:
		d.say("sleep: awake (%s)", why)
	}
	d.why = why
	return why == ""
}

// sleep suspends until a wake-up event or the plan's wake. The count is
// read first, then drain handles what came meanwhile and the plan is
// asked again, so a key or a plug that arrived wins (the kernel calls the
// suspend off for an event after the count). False: drain found the
// renderer gone.
func (d *dozer) sleep(c *charger, o *output, drain func() bool) bool {
	count, err := d.sys.Count()
	if err != nil {
		d.off = err.Error()
		d.say("sleep: %v; sleep off", err)
		return true
	}
	if !drain() {
		return false
	}
	wake, why := c.sleepPlan(time.Now())
	if why != "" || !o.settled() {
		return true
	}
	d.say("sleep: %s until %s", d.sys.Mode(), wake.Format("15:04:05"))
	start := time.Now().Round(0)
	err = d.sys.Sleep(count, wake)
	slept := time.Now().Round(0).Sub(start)
	switch {
	case errors.Is(err, pm.ErrAborted):
		d.say("sleep: %v", err)
	case err != nil:
		// A board that cannot suspend after all: stay awake rather than
		// retry every few seconds.
		d.off = err.Error()
		d.say("sleep: %v; sleep off", err)
	default:
		d.sleeps++
		by := d.sys.WokenBy()
		if by == "" {
			by = "no wake reason"
		}
		d.say("sleep: woke after %s (%s)", slept.Round(time.Second), by)
	}
	c.woken(time.Now())
	return true
}

// state is the sleep for the status file: the mode, or why there is none.
func (d *dozer) state() string {
	if d.off != "" {
		return "off: " + d.off
	}
	return d.sys.Mode()
}
