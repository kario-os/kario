package shell

import (
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/push"
)

// nightSleeper is a suspend the test ends: by runs on the Shell's loop with
// the wake asked for (on the Shell's clock), moves the clock on to when the
// phone woke and says what woke it.
type nightSleeper struct {
	c     *Core
	by    func(wake time.Time) string
	woken string
	locks []string
}

func (s *nightSleeper) Mode() string           { return "mem" }
func (s *nightSleeper) Count() (string, error) { return "1", nil }
func (s *nightSleeper) Sleep(_ string, wake time.Time) error {
	done := make(chan struct{})
	s.c.Post(func() { s.woken = s.by(wake.Add(s.c.clockSkew)); close(done) })
	<-done
	return nil
}
func (s *nightSleeper) WokenBy() string     { return s.woken }
func (s *nightSleeper) WakeLocks() []string { return s.locks }

func moveTo(c *Core, t time.Time) { c.clockSkew += t.Sub(c.now()) }

// about: the Shell's clock runs on with the real one while the test runs.
func about(off time.Duration) bool { return off.Abs() < 100*time.Millisecond }

// nightCore is a Shell with nothing to do, its screen off for a minute, 30
// seconds into a minute; its RTC wakes it when asked.
func nightCore(t *testing.T, sku SKU) (*Core, *nightSleeper) {
	t.Helper()
	c := &Core{cfg: Config{StateDir: t.TempDir(), SKU: sku}, log: log.New(io.Discard, "", 0), ev: make(chan func(), 16),
		sys: &App{system: true, M: &Manifest{ID: "system"}}}
	c.alarms, c.powerOverride = &alarmStore{}, &Power{Level: 82}
	s := &nightSleeper{c: c}
	s.by = func(wake time.Time) string { moveTo(c, wake); return "alarmtimer" }
	c.sleep.sys, c.sleep.allowed = s, true
	moveTo(c, c.now().Truncate(time.Minute).Add(time.Minute+30*time.Second))
	c.screen.off, c.screen.offAt = true, c.now().Add(-time.Minute)
	return c, s
}

// flipSKU: an outer display of kind (none: "") and a lid switch (none: "").
func flipSKU(kind, lid string) SKU {
	s := SKU{ID: "test-flip", Lid: lid}
	if kind != "" {
		s.Outer = SKUOuter{Display: "x", Width: 144, Height: 168, Kind: kind, Color: "mono", DRMDriver: "sharp_memory_display"}
	}
	return s
}

// sleepNight has the housekeeping put the phone to sleep and runs it until
// a wake that is not only the outer display's clock's.
func sleepNight(t *testing.T, c *Core) {
	t.Helper()
	c.checkSleep()
	if !c.sleep.going {
		t.Fatalf("no sleep: %s", c.sleep.why)
	}
	for end := time.After(10 * time.Second); c.sleep.going; {
		select {
		case f := <-c.ev:
			f()
		case <-end:
			t.Fatal("the phone did not wake")
		}
	}
}

// A flip's outer display holds its picture asleep and shows the time
// (24-sku-framework.md §9.10.10): the RTC wakes the phone at each minute,
// and that wake redraws the time and suspends again at once, without the
// awakeAfterWake of other wakes; an RTC that rings up to a second early
// still rings in the new minute. The housekeeping still runs maintenance
// after it last ran: the minute wakes do not put it off.
func TestOuterClockMinuteWakes(t *testing.T) {
	for _, early := range []time.Duration{0, rtcEarly - 100*time.Millisecond} {
		t.Run("RTC "+early.String()+" early", func(t *testing.T) {
			c, s := nightCore(t, flipSKU("memory-lcd", ""))
			log := screenLogTo(t)
			var asked []time.Time
			var shown []string
			s.by = func(wake time.Time) string {
				asked, shown = append(asked, wake), append(shown, c.outer.clock)
				moveTo(c, wake.Add(-early))
				return "alarmtimer"
			}
			t0 := c.now()
			minute := t0.Truncate(time.Minute)
			sleepNight(t, c)
			if len(asked) != 16 {
				t.Fatalf("asked for %d wakes before one with a window, want 15 minute wakes and the housekeeping's: %v", len(asked), asked)
			}
			for i, w := range asked {
				if want := c.clockText(minute.Add(time.Duration(i) * time.Minute)); shown[i] != want {
					t.Errorf("sleep %d: the outer display showed %q, want %q", i+1, shown[i], want)
				}
				if i == 15 {
					break
				}
				if want := minute.Add(time.Duration(i+1)*time.Minute + rtcEarly); !w.Equal(want) {
					t.Errorf("wake %d at %s, want %s", i+1, w.Format("15:04:05.000"), want.Format("15:04:05.000"))
				}
			}
			if want := t0.Add(maintenance - wakeEarly); !about(asked[15].Sub(want)) {
				t.Errorf("the housekeeping's wake at %s, want %s (maintenance after it ran)", asked[15].Format("15:04:05.000"), want.Format("15:04:05.000"))
			}
			if n, _ := c.Sleeps(); n != 16 {
				t.Errorf("slept %d times, want 16", n)
			}
			if _, why := c.sleepPlan(); why != "just woke" {
				t.Errorf("after the housekeeping's wake: %q, want its window", why)
			}
			if got := log.String(); strings.Count(got, "sleep: woke after") != 1 || strings.Count(got, " until ") != 2 ||
				!strings.Contains(got, "(alarmtimer); 15 short wakes for the outer display's clock before it\n") {
				t.Errorf("the log:\n%s", got)
			}
		})
	}
}

// A minute wake that finds something else to do is a wake like any other,
// with its window: woken before the minute by something else (a key, the
// modem), a job due a moment after it, a wake lock someone holds.
func TestOuterClockWakeKeepsItsWindow(t *testing.T) {
	type night struct {
		c      *Core
		s      *nightSleeper
		minute time.Time
		asked  []time.Time
	}
	for _, tc := range []struct {
		name  string
		setup func(n *night)
		// wakes: the wakes asked for, the last the one with a window;
		// why: what keeps the phone awake after it until pending clears it.
		wakes   int
		why     string
		pending func(n *night)
	}{
		{"woken early", func(n *night) {
			n.s.by = func(wake time.Time) string {
				if n.asked = append(n.asked, wake); len(n.asked) == 3 {
					moveTo(n.c, wake.Add(-20*time.Second))
					return "irq 90"
				}
				moveTo(n.c, wake)
				return "alarmtimer"
			}
		}, 3, "", nil},
		{"a job due", func(n *night) {
			j := &job{App: systemJob, ID: "test", Every: 15}
			j.Next = n.minute.Add(3*time.Minute + 5*time.Second).Add(-j.deadline().Sub(j.Next))
			n.c.jobs = []*job{j}
		}, 3, "due at ", func(n *night) { n.c.jobs = nil }},
		{"a wake lock", func(n *night) {
			n.s.by = func(wake time.Time) string {
				if n.asked = append(n.asked, wake); len(n.asked) == 2 {
					n.s.locks = []string{"pushd"}
				}
				moveTo(n.c, wake)
				return "alarmtimer"
			}
		}, 2, "wake lock pushd", func(n *night) { n.s.locks = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, s := nightCore(t, flipSKU("memory-lcd", ""))
			n := &night{c: c, s: s, minute: c.now().Truncate(time.Minute)}
			s.by = func(wake time.Time) string {
				n.asked = append(n.asked, wake)
				moveTo(c, wake)
				return "alarmtimer"
			}
			tc.setup(n)
			sleepNight(t, c)
			if len(n.asked) != tc.wakes {
				t.Fatalf("asked for %d wakes, want %d: %v", len(n.asked), tc.wakes, n.asked)
			}
			for i, w := range n.asked {
				if want := n.minute.Add(time.Duration(i+1)*time.Minute + rtcEarly); !w.Equal(want) {
					t.Errorf("wake %d at %s, want the minute's %s", i+1, w.Format("15:04:05.000"), want.Format("15:04:05.000"))
				}
			}
			if _, why := c.Sleeps(); !strings.HasPrefix(why, tc.why) {
				t.Fatalf("after the wake awake for %q, want %q", why, tc.why)
			}
			if tc.pending != nil {
				tc.pending(n)
			}
			if _, why := c.sleepPlan(); why != "just woke" {
				t.Fatalf("then: %q, want the wake's window", why)
			}
			c.clockSkew += awakeAfterWake
			if _, why := c.sleepPlan(); why != "" {
				t.Errorf("after the window: %q", why)
			}
		})
	}
}

// With a flip's lid shut the maintenance wake waits for the push
// connection's heartbeat, which wakes the phone anyway: the phone wakes
// once each heartbeat (24-sku-framework.md §9.10.10). With the lid open,
// without a lid, or without a push connection to count on, the
// maintenance wake comes as before.
func TestLidShutOneWake(t *testing.T) {
	const beat = 28 * time.Minute
	for _, tc := range []struct {
		name string
		sku  SKU
		shut bool
		push bool
		// merged: every wake with a window is the heartbeat's.
		merged bool
	}{
		{"lid shut", flipSKU("", "Lid"), true, true, true},
		{"flip, lid shut", flipSKU("memory-lcd", "Lid"), true, true, true},
		{"lid open", flipSKU("", "Lid"), false, true, false},
		{"lid shut, no push connection", flipSKU("", "Lid"), true, false, false},
		{"no lid", flipSKU("", ""), true, true, false},
		{"flip, lid open", flipSKU("memory-lcd", "Lid"), false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, s := nightCore(t, tc.sku)
			c.lidSwitched(tc.shut)
			c.pushUp = tc.push
			c.pushWindow(beat)
			calls, pings, rtc := 0, 0, 0
			s.by = func(wake time.Time) string {
				calls++
				if next := c.keep.lastPush.Add(beat); tc.push && next.Before(wake) {
					moveTo(c, next)
					c.pushWindow(beat)
					pings++
					return "modem"
				}
				moveTo(c, wake)
				return "alarmtimer"
			}
			const wakes = 4
			start := c.now()
			last := start
			for i := 0; i < wakes; i++ {
				c.clockSkew += awakeAfterWake
				sleepNight(t, c)
				gap := c.now().Sub(last)
				last = c.now()
				if c.WokenBy() == "alarmtimer" {
					rtc++
				}
				switch {
				case tc.merged && !about(gap-beat):
					t.Errorf("wake %d %s after the one before, want the heartbeat's %s", i+1, gap, beat)
				case !tc.merged && gap > maintenance+awakeAfterWake:
					t.Errorf("wake %d %s after the one before: the housekeeping waited longer than %s", i+1, gap, maintenance)
				}
			}
			switch shorts, minutes := calls-wakes, int(last.Sub(start)/time.Minute); {
			case tc.merged && (rtc != 0 || pings != wakes):
				t.Errorf("of %d wakes %d the RTC's, %d the heartbeat's: want only the heartbeat's", wakes, rtc, pings)
			case !tc.merged && rtc == 0:
				t.Errorf("no maintenance wake: %d heartbeats", pings)
			case tc.sku.Outer.Kind != "" && shorts < minutes-2*wakes:
				t.Errorf("%d minute wakes for the outer display's clock in %d minutes", shorts, minutes)
			case tc.sku.Outer.Kind == "" && shorts != 0:
				t.Errorf("%d wakes without a window and no outer display", shorts)
			}
		})
	}
}

// A heartbeat that does not come leaves the housekeeping waiting no longer
// than KeepaliveSlack past it: the RTC wakes the phone then, and the
// maintenance wake comes as without the lid until the push connection is
// heard from again.
func TestLidShutHeartbeatMissing(t *testing.T) {
	for name, kind := range map[string]string{"lid only": "", "flip": "memory-lcd"} {
		t.Run(name, func(t *testing.T) {
			c, s := nightCore(t, flipSKU(kind, "Lid"))
			c.lidSwitched(true)
			c.pushUp = true
			c.pushWindow(28 * time.Minute)
			beat := c.now().Add(28 * time.Minute)
			var full []time.Time
			s.by = func(wake time.Time) string {
				if wake.Sub(wake.Truncate(time.Minute)) != rtcEarly {
					full = append(full, wake)
				}
				moveTo(c, wake)
				return "alarmtimer"
			}
			c.clockSkew += awakeAfterWake
			sleepNight(t, c)
			if want := beat.Add(push.KeepaliveSlack - wakeEarly); len(full) != 1 || !about(full[0].Sub(want)) {
				t.Fatalf("woke for the housekeeping at %v, want %s", full, want.Format("15:04:05.000"))
			}
			c.clockSkew += awakeAfterWake
			at := c.now()
			sleepNight(t, c)
			if want := at.Add(maintenance - wakeEarly); len(full) != 2 || !about(full[1].Sub(want)) {
				t.Fatalf("then at %v, want %s", full, want.Format("15:04:05.000"))
			}
		})
	}
}

// A phone without a picture-holding outer display or a lid sleeps as it
// did: one wake, maintenance on, with its window.
func TestSleepWithoutFlipParts(t *testing.T) {
	for name, sku := range map[string]SKU{
		"bar":                  {ID: "test"},
		"OLED outer display":   flipSKU("oled", ""),
		"LCD outer display":    flipSKU("lcd", ""),
		"lid open":             flipSKU("", "Lid"),
		"OLED outer, lid open": flipSKU("oled", "Lid"),
	} {
		t.Run(name, func(t *testing.T) {
			c, s := nightCore(t, sku)
			var asked []time.Time
			s.by = func(wake time.Time) string {
				asked = append(asked, wake)
				moveTo(c, wake)
				return "alarmtimer"
			}
			c.pushUp = true
			c.pushWindow(28 * time.Minute)
			t0 := c.now()
			sleepNight(t, c)
			if len(asked) != 1 || !about(asked[0].Sub(t0.Add(maintenance-wakeEarly))) {
				t.Fatalf("asked for %v, want one wake at %s", asked, t0.Add(maintenance-wakeEarly))
			}
			if _, why := c.sleepPlan(); why != "just woke" {
				t.Errorf("after the wake: %q", why)
			}
			if c.outer.clock != "" {
				t.Errorf("the outer display shows %q", c.outer.clock)
			}
		})
	}
}
