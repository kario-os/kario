package chargercmd

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/hal/pm"
)

// fakeSleep is the kernel's sleep: each Sleep is kept with its count and
// wake, and returns err.
type fakeSleep struct {
	locks  []string
	err    error
	sleeps []string
	wakes  []time.Time
}

func (s *fakeSleep) Mode() string           { return "mem" }
func (s *fakeSleep) Count() (string, error) { return "7", nil }
func (s *fakeSleep) WokenBy() string        { return "irq 90" }
func (s *fakeSleep) WakeLocks() []string    { return s.locks }
func (s *fakeSleep) Sleep(n string, w time.Time) error {
	s.sleeps, s.wakes = append(s.sleeps, n), append(s.wakes, w)
	return s.err
}

// sleepRig is a charger dark for a while, by real time (dozer reads the
// clock), with the panel off and answered.
func sleepRig(t *testing.T) (*rig, *dozer, *fakeSleep, *output, *[]string) {
	r := newRig(t)
	r.t0 = time.Now().Add(-time.Hour)
	r.c.begin(r.at(0), plugged, true, plain)
	r.c.tick(r.at(screenTime))
	sys := &fakeSleep{}
	var said []string
	d := &dozer{sys: sys, say: func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) }}
	return r, d, sys, &output{}, &said
}

func TestDozerSleepsUntilTheRefresh(t *testing.T) {
	r, d, sys, o, said := sleepRig(t)
	if !d.due(r.c, o, time.Now()) {
		t.Fatalf("not due: %q", d.why)
	}
	before := time.Now()
	if !d.sleep(r.c, o, func() bool { return true }) {
		t.Fatal("the renderer gone")
	}
	if len(sys.sleeps) != 1 || sys.sleeps[0] != "7" {
		t.Fatalf("sleeps %v, want one with the count read before", sys.sleeps)
	}
	if w := sys.wakes[0].Sub(before); w < refresh || w > refresh+time.Second {
		t.Errorf("RTC wake in %s, want %s", w, refresh)
	}
	if d.sleeps != 1 || !strings.Contains(strings.Join(*said, "\n"), "sleep: woke after 0s (irq 90)") {
		t.Errorf("sleeps %d, log %q", d.sleeps, *said)
	}
	// Just woken, it stays up for what woke it.
	if d.due(r.c, o, time.Now()) || d.why != "just woke" {
		t.Errorf("due right after a wake (%q)", d.why)
	}
}

// What comes while the count is read (a key here) is handled before the
// plan is asked again, and wins.
func TestDozerKeyWhileCountingStaysAwake(t *testing.T) {
	r, d, sys, o, _ := sleepRig(t)
	if !d.due(r.c, o, time.Now()) {
		t.Fatalf("not due: %q", d.why)
	}
	d.sleep(r.c, o, func() bool {
		r.c.key(time.Now(), false)
		return true
	})
	if len(sys.sleeps) != 0 || !r.c.on {
		t.Errorf("slept %v with a key in between; screen %v", sys.sleeps, r.c.on)
	}
	if d.sleep(r.c, o, func() bool { return false }) {
		t.Error("the renderer gone while counting, and sleep went on")
	}
}

func TestDozerWaitsForThePanelAndWakeLocks(t *testing.T) {
	r, d, sys, o, said := sleepRig(t)
	o.panel = true
	if d.due(r.c, o, time.Now()) || d.why != "panel not off" {
		t.Errorf("due with the panel on (%q)", d.why)
	}
	o.panel = false
	sys.locks = []string{"phone-bringup"}
	if d.due(r.c, o, time.Now()) || d.why != "wake lock phone-bringup" {
		t.Errorf("due with a wake lock (%q)", d.why)
	}
	d.due(r.c, o, time.Now())
	if got := strings.Join(*said, "|"); got != "sleep: awake (panel not off)|sleep: awake (wake lock phone-bringup)" {
		t.Errorf("logged %q, each reason once", got)
	}
	r.c.key(time.Now(), false)
	if d.due(r.c, o, time.Now()) || d.why != "screen on" {
		t.Errorf("due with the screen on (%q)", d.why)
	}
}

// Called off by a wake-up event it tries again later; a kernel that
// cannot suspend at all turns sleep off.
func TestDozerAbortedAndBroken(t *testing.T) {
	r, d, sys, o, _ := sleepRig(t)
	sys.err = pm.ErrAborted
	d.sleep(r.c, o, func() bool { return true })
	if d.awake() || d.sleeps != 0 || r.c.woke.IsZero() {
		t.Errorf("aborted: off %q, sleeps %d, woke %v", d.off, d.sleeps, r.c.woke)
	}
	r.c.woke = time.Time{}
	sys.err = errors.New("write /sys/power/state: device or resource busy")
	d.sleep(r.c, o, func() bool { return true })
	if !d.awake() || d.due(r.c, o, time.Now()) || d.state() != "off: write /sys/power/state: device or resource busy" {
		t.Errorf("broken: state %q", d.state())
	}
}
