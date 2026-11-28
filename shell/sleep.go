package shell

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kario-os/kario/hal/pm"
	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/ui"
)

// System sleep, Android's autosleep with the shell as the only one holding
// wake locks: it sees everything that needs the CPU (one someone else
// holds in /sys/power/wake_lock keeps the kernel up, and the Shell from
// trying). Once the screen has been off a while with no call, no sound,
// no update installing, no computer on the USB link and no app woken for
// a push or a job, the whole phone suspends (to RAM where the board can,
// else to idle). The RTC wakes it a little before the next alarm or job
// deadline and at least every maintenance interval. Those wakes are the
// CPU's, not the radio's: the modem stays idle until something sends, so
// no heartbeat rides a wake by itself, only what it brings (a batch of
// jobs, a heartbeat due; 28-battery.md §8). The modem wakes it for calls
// and messages, the keys for the user, the NFC
// controller for a reader (phone-nfcd makes its interrupt a wake-up
// source; the phone stays up while the field lasts). What a wake
// brings gets awakeAfterWake before the next suspend. The kernel's
// wakeup_count handshake (hal/pm) keeps a key pressed while deciding from
// being lost. The notification light keeps blinking only where its blink
// is the LED controller's own (blink_set); the kernel's timer trigger
// stops with the CPU.
//
// Two parts of a flip change this (24-sku-framework.md §9.10.10). An
// outer display that shows the clock asleep (outer.go) has the RTC wake
// the phone at each minute; that wake redraws the time and suspends again
// at once, without awakeAfterWake and without the housekeeping, unless it
// finds something else to do, which makes it a wake like any other. With
// the lid shut (lid.go) the maintenance wake waits for the push
// connection's next heartbeat, a wake that comes anyway, so the phone has
// one periodic wake instead of two.

const (
	sleepAfter     = 30 * time.Second
	maintenance    = 15 * time.Minute
	awakeAfterWake = 10 * time.Second
	// wakeEarly: resuming takes a moment, and an alarm must ring on time.
	wakeEarly = 3 * time.Second
	// rtcEarly: the RTC counts whole seconds from a phase of its own, so
	// its alarm may ring up to a second before the time asked.
	rtcEarly = time.Second
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
	// logged: the why last logged in this stretch with the screen off.
	logged string
	// housekept: when the housekeeping last ran, which the maintenance
	// wake counts from; planned: the wake the last sleep asked for, and
	// minute: only the outer display's clock wanted it; short: this wake
	// is such a minute's, with no awakeAfterWake; shorts: how many came
	// since the last wake logged.
	housekept, planned time.Time
	minute, short      bool
	shorts             int
}

// setupSleep finds the kernel's sleep on a phone (a board's SKU: never on
// the developer machine, which this must not suspend).
func (c *Core) setupSleep(sku SKU) {
	if sku.ID == "" {
		return
	}
	sys, err := OpenSleep(sku)
	if err != nil {
		c.screenLog("sleep: off (%v)", err)
		return
	}
	c.sleep.sys, c.sleep.allowed = sys, true
	c.screenLog("sleep: %s after %s with the screen off", sys.Mode(), sleepAfter)
}

// OpenSleep is the kernel's sleep on the board, or why it has none the
// Shell would use (phone-charger takes the same).
func OpenSleep(sku SKU) (*pm.System, error) {
	if !sku.Suspend {
		return nil, errors.New("the board cannot be woken from suspend")
	}
	sys, err := pm.Open()
	if err != nil {
		return nil, err
	}
	sys.AbsoluteAlarm = sku.RTC.Alarm == "absolute"
	if !pm.KeysWake() {
		// The emulator: neither its virtual keyboard nor its RTC (pl031 arms
		// no wake-up interrupt for suspend-to-idle) can wake it.
		return nil, errors.New("no key can wake this board")
	}
	return sys, nil
}

// sleepPlan is when the phone must be awake again, or what keeps it awake.
func (c *Core) sleepPlan() (time.Time, string) {
	wake, why := c.nextSleep()
	if why != "" {
		c.fullWake()
	}
	return wake, why
}

// fullWake: whatever keeps the phone awake at a minute wake for the outer
// display's clock makes that a wake like any other, with its
// awakeAfterWake.
func (c *Core) fullWake() {
	if c.sleep.short {
		c.sleep.short, c.sleep.woke = false, c.now()
	}
}

func (c *Core) nextSleep() (time.Time, string) {
	now := c.now()
	s := &c.screen
	c.sleep.minute = false
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
	case c.accounts.run != nil:
		return time.Time{}, "contacts syncing"
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
	wake = wake.Add(-wakeEarly)
	if m := c.outerMinute(now); !m.IsZero() && m.Before(wake) {
		c.sleep.minute = true
		return m, ""
	}
	return wake, ""
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

// nextWake: the next alarm, scheduled notification, job deadline, own
// connection's heartbeat no other wake carries (keepalive.go) or
// maintenance wake.
func (c *Core) nextWake(now time.Time) time.Time {
	wake := c.maintenanceWake(now)
	for _, e := range c.alarms.list {
		if e.On && !e.Next.IsZero() && e.Next.Before(wake) {
			wake = e.Next
		}
	}
	if c.sched != nil {
		if t := c.sched.next(); !t.IsZero() && t.Before(wake) {
			wake = t
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
	if t := c.keepaliveNextWake(); !t.IsZero() && t.Before(wake) {
		wake = t
	}
	return wake
}

// maintenanceWake is the latest the housekeeping waits while the phone
// sleeps: maintenance after it last ran or, with the lid shut, the push
// connection's next heartbeat, the radio's wake that comes anyway
// (28-battery.md §8), KeepaliveSlack past it should that come late.
func (c *Core) maintenanceWake(now time.Time) time.Time {
	from := now
	if s := &c.sleep; s.short && !s.housekept.IsZero() && !s.housekept.After(now) {
		from = s.housekept
	}
	if beat := c.nextPushWindow(); c.lidShut() && beat.After(now) && beat.Sub(from) <= push.HeartbeatMax {
		return beat.Add(push.KeepaliveSlack)
	}
	return from.Add(maintenance)
}

// checkSleep runs with the housekeeping.
func (c *Core) checkSleep() {
	c.sleep.housekept = c.now()
	c.trySleep()
}

// trySleep suspends unless something needs the CPU. The count is read
// first and the plan checked again after it, so whatever arrived meanwhile
// wins.
func (c *Core) trySleep() {
	s := &c.sleep
	if s.sys == nil || !s.allowed || s.going {
		return
	}
	if _, why := c.sleepPlan(); why != "" {
		c.awakeFor(why)
		return
	}
	if locks := s.sys.WakeLocks(); len(locks) > 0 {
		c.fullWake()
		c.awakeFor("wake lock " + strings.Join(locks, " "))
		return
	}
	s.going, s.why, s.logged = true, "", ""
	sys, skew := s.sys, c.clockSkew
	go func() {
		start := time.Now().Round(0)
		count, err := sys.Count()
		if err == nil {
			plan := make(chan time.Time, 1)
			c.Post(func() {
				c.outerTick()
				wake, why := c.sleepPlan()
				switch {
				case why != "":
					wake = time.Time{}
				case !c.sleep.minute || !c.sleep.short:
					c.screenLog("sleep: %s until %s", sys.Mode(), wake.Format("15:04:05"))
				}
				c.sleep.planned = wake
				plan <- wake
			})
			if wake := <-plan; wake.IsZero() {
				err = errStayAwake
			} else {
				err = sys.Sleep(count, wake.Add(-skew))
			}
		}
		slept := time.Now().Round(0).Sub(start)
		c.Post(func() { c.wokeUp(slept, err) })
	}()
}

// awakeFor logs what keeps the phone awake with the screen off once each
// time it changes, so a night that drained the battery says why; the
// moments every stretch has (the screen on or just off, just woken,
// something due) are left out.
func (c *Core) awakeFor(why string) {
	s := &c.sleep
	s.why = why
	switch {
	case why == "screen on" || why == "screen just off" || why == "just woke" || strings.HasPrefix(why, "due at "):
		s.logged = ""
	case why != s.logged:
		s.logged = why
		c.screenLog("sleep: awake (%s)", why)
	}
}

func (c *Core) wokeUp(slept time.Duration, err error) {
	s := &c.sleep
	s.going, s.short = false, false
	switch {
	case errors.Is(err, errStayAwake):
		return
	case errors.Is(err, pm.ErrAborted):
		c.screenLog("sleep: %v", err)
	case err != nil:
		// A board that cannot suspend after all: stay awake rather than retry
		// twice a second.
		c.screenLog("sleep: %v; sleep off", err)
		s.allowed = false
		return
	}
	now := c.now()
	// Woken before the RTC could have rung, it was something else: a key,
	// the modem, the lid.
	s.short = err == nil && s.minute && !now.Before(s.planned.Add(-rtcEarly))
	if !s.short {
		s.woke = now
	}
	if err == nil {
		s.sleeps++
		s.slept += slept
		s.wokenBy = firstNonEmpty(s.sys.WokenBy(), "no wake reason")
		if s.short {
			s.shorts++
		} else {
			c.screenLog("sleep: woke after %s (%s)%s", slept.Round(time.Second), s.wokenBy, shortWakes(s.shorts))
			s.shorts = 0
		}
	}
	if s.short {
		c.outerTick()
		c.trySleep()
	}
}

// shortWakes says how many minute wakes for the outer display's clock came
// before a wake logged.
func shortWakes(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("; %d short wakes for the outer display's clock before it", n)
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

// sleepOff says why the phone cannot suspend for the selftest, "" when it
// can: a wake lock someone else holds (the bring-up image's
// phone-bringup-awake holds phone-bringup) keeps the kernel up.
func (c *Core) sleepOff() string {
	if c.sleep.sys == nil {
		return ""
	}
	locks := c.sleep.sys.WakeLocks()
	if len(locks) == 0 {
		return ""
	}
	why := "the wake lock " + strings.Join(locks, " ") + " is held (/sys/power/wake_lock): the kernel does not suspend"
	if slices.Contains(locks, "phone-bringup") {
		why += "; systemctl stop phone-bringup-awake lets the phone sleep"
	}
	return why
}

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
		s.c.SetClockSkew(d)
		close(done)
	})
	<-done
	return nil
}

func (s *simSleep) WokenBy() string { return s.by }

func (s *simSleep) WakeLocks() []string { return nil }
