package shell

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/kario-os/kario/hal/pm"
	"github.com/kario-os/kario/ui"
)

// System sleep, Android's autosleep with the shell as the only one holding
// wake locks: it sees everything that needs the CPU. Once the screen has
// been off a while with no call, no sound, no update installing, no
// computer on the USB link and no app woken for a push or a job, the whole
// phone suspends (to RAM where the board can, else to idle). The RTC wakes
// it a little before the next alarm or job deadline and at least every
// maintenance interval (the push connection catches up, due jobs run); the
// modem wakes it for calls and messages, the keys for the user, the NFC
// controller for a reader (phone-nfcd makes its interrupt a wake-up
// source; the phone stays up while the field lasts). What a wake
// brings gets awakeAfterWake before the next suspend. The kernel's
// wakeup_count handshake (hal/pm) keeps a key pressed while deciding from
// being lost. The notification light keeps blinking only where its blink
// is the LED controller's own (blink_set); the kernel's timer trigger
// stops with the CPU.

const (
	sleepAfter     = 30 * time.Second
	maintenance    = 15 * time.Minute
	awakeAfterWake = 10 * time.Second
	// wakeEarly: resuming takes a moment, and an alarm must ring on time.
	wakeEarly = 3 * time.Second
)

var errStayAwake = errors.New("something needs the CPU again")

type sleepState struct {
	sys pm.Sleeper
	// allowed: the board sleeps; scripts turn it on for the cases that
	// test it (a suspended emulator would stall the others).
	allowed bool
	going   bool
	woke    time.Time
	sleeps  int
	slept   time.Duration
	why     string // what keeps the phone awake now
	wokenBy string // what woke it last
}

// setupSleep finds the kernel's sleep on a phone (a board's SKU: never on
// the developer machine, which this must not suspend).
func (c *Core) setupSleep(sku SKU) {
	if sku.ID == "" {
		return
	}
	if !sku.Suspend {
		c.log.Printf("sleep: off (the board cannot be woken from suspend)")
		return
	}
	sys, err := pm.Open()
	if err != nil {
		c.log.Printf("sleep: off (%v)", err)
		return
	}
	if !pm.KeysWake() {
		// The emulator: neither its virtual keyboard nor its RTC (pl031 arms
		// no wake-up interrupt for suspend-to-idle) can wake it.
		c.log.Printf("sleep: off (no key can wake this board)")
		return
	}
	c.sleep.sys, c.sleep.allowed = sys, true
	c.log.Printf("sleep: %s after %s with the screen off", sys.Mode(), sleepAfter)
}

// sleepPlan is when the phone must be awake again, or what keeps it awake.
func (c *Core) sleepPlan() (time.Time, string) {
	now := c.now()
	s := &c.screen
	// The clock set back (by the user or the network) starts these over.
	if s.offAt.After(now) {
		s.offAt = now
	}
	if c.sleep.woke.After(now) {
		c.sleep.woke = now
	}
	switch {
	case !s.off:
		return time.Time{}, "screen on"
	case now.Sub(s.offAt) < sleepAfter:
		return time.Time{}, "screen just off"
	case now.Sub(c.sleep.woke) < awakeAfterWake:
		return time.Time{}, "just woke"
	case c.anyCall() || c.ringing():
		return time.Time{}, "call"
	case c.soundPlaying():
		return time.Time{}, "sound"
	case c.update.state == "installing":
		return time.Time{}, "update installing"
	case c.usb.trusted && c.usb.state == "configured":
		return time.Time{}, "computer on USB"
	case c.nfc.field:
		return time.Time{}, "NFC reader"
	}
	if a := c.awakeApp(); a != nil {
		return time.Time{}, "app " + a.M.ID + " awake"
	}
	wake := c.nextWake(now)
	if wake.Sub(now) < wakeEarly+awakeAfterWake {
		return time.Time{}, "due at " + wake.Format("15:04:05")
	}
	return wake.Add(-wakeEarly), ""
}

func (c *Core) soundPlaying() bool {
	for _, st := range c.streams {
		if st.State == ui.PlayPlaying {
			return true
		}
	}
	return false
}

// awakeApp is a background app let run for now (a push or a job's wake
// budget, or its grace before freezing); apps that run in the background
// for good (playing audio, VoIP) sleep with the phone.
func (c *Core) awakeApp() *App {
	after := c.freezeAfter()
	for _, a := range c.apps {
		if !a.running || a.frozen || a == c.fg || a.proc == nil || a.bgSince.IsZero() || after < 0 {
			continue
		}
		if time.Since(a.bgSince) < after && !c.runsInBackground(a) && !c.inCall(a) {
			return a
		}
	}
	return nil
}

// nextWake: the next alarm, job deadline or maintenance wake.
func (c *Core) nextWake(now time.Time) time.Time {
	wake := now.Add(maintenance)
	for _, e := range c.alarms.list {
		if e.On && !e.Next.IsZero() && e.Next.Before(wake) {
			wake = e.Next
		}
	}
	charging := c.power().Charging
	for _, j := range c.jobs {
		if j.Charging && !charging {
			continue
		}
		if d := j.deadline(); d.Before(wake) {
			wake = d
		}
	}
	return wake
}

// checkSleep runs with the housekeeping. The count is read first and the
// plan checked again after it, so whatever arrived meanwhile wins.
func (c *Core) checkSleep() {
	s := &c.sleep
	if s.sys == nil || !s.allowed || s.going {
		return
	}
	if _, why := c.sleepPlan(); why != "" {
		s.why = why
		return
	}
	s.going, s.why = true, ""
	sys, skew := s.sys, c.clockSkew
	go func() {
		start := time.Now().Round(0)
		count, err := sys.Count()
		if err == nil {
			plan := make(chan time.Time, 1)
			c.Post(func() {
				wake, why := c.sleepPlan()
				if why != "" {
					wake = time.Time{}
				}
				plan <- wake
			})
			if wake := <-plan; wake.IsZero() {
				err = errStayAwake
			} else {
				c.log.Printf("sleep: %s until %s", sys.Mode(), wake.Format("15:04:05"))
				err = sys.Sleep(count, wake.Add(-skew))
			}
		}
		slept := time.Now().Round(0).Sub(start)
		c.Post(func() { c.wokeUp(slept, err) })
	}()
}

func (c *Core) wokeUp(slept time.Duration, err error) {
	s := &c.sleep
	s.going = false
	switch {
	case errors.Is(err, errStayAwake):
		return
	case errors.Is(err, pm.ErrAborted):
		c.log.Printf("sleep: %v", err)
	case err != nil:
		// A board that cannot suspend after all: stay awake rather than retry
		// twice a second.
		c.log.Printf("sleep: %v; sleep off", err)
		s.allowed = false
		return
	}
	s.woke = c.now()
	if err == nil {
		s.sleeps++
		s.slept += slept
		s.wokenBy = firstNonEmpty(s.sys.WokenBy(), "no wake reason")
		c.log.Printf("sleep: woke after %s (%s)", slept.Round(time.Second), s.wokenBy)
	}
	c.radioActive()
}

// SetSleep lets the phone suspend (selftest). Without a sleep it can wake
// from (the developer machine, the emulator) it gets a simulated one: it
// dozes for simDoze, then its RTC moves the shell's clock to the wake time
// at once.
func (c *Core) SetSleep(on bool) {
	if on && c.sleep.sys == nil {
		c.sleep.sys = &simSleep{c: c, events: make(chan string, 1)}
	}
	c.sleep.allowed = on && c.sleep.sys != nil
}

// sleepWake: a wake-up event the shell hears about (an NFC reader's
// field). On a phone the kernel has resumed for it already; it ends the
// simulated sleep.
func (c *Core) sleepWake(why string) {
	if s, ok := c.sleep.sys.(*simSleep); ok {
		select {
		case s.events <- why:
		default:
		}
	}
}

// Sleeps is how often the phone slept and what keeps it awake now (selftest).
func (c *Core) Sleeps() (int, string) { return c.sleep.sleeps, c.sleep.why }

// Asleep: the simulated sleep is on now; WokenBy: what ended the last
// sleep (selftest).
func (c *Core) Asleep() bool {
	s, ok := c.sleep.sys.(*simSleep)
	return ok && s.asleep.Load()
}

func (c *Core) WokenBy() string { return c.sleep.wokenBy }

// simDoze: how long the simulated sleep waits for a wake-up event before
// its RTC fires.
const simDoze = 2 * time.Second

type simSleep struct {
	c      *Core
	events chan string
	asleep atomic.Bool
	by     string
}

func (s *simSleep) Mode() string { return "simulated" }

// Count drops wake-up events from before the decision to sleep.
func (s *simSleep) Count() (string, error) {
	for {
		select {
		case <-s.events:
		default:
			return "0", nil
		}
	}
}

func (s *simSleep) Sleep(_ string, wake time.Time) error {
	s.asleep.Store(true)
	defer s.asleep.Store(false)
	select {
	case why := <-s.events:
		s.by = "simulated " + why
		return nil
	case <-time.After(min(simDoze, time.Until(wake))):
	}
	s.by = "simulated RTC"
	d := time.Until(wake)
	done := make(chan struct{})
	s.c.Post(func() {
		s.c.clockSkew += d
		close(done)
	})
	<-done
	return nil
}

func (s *simSleep) WokenBy() string { return s.by }
