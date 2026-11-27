package chargercmd

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/hal/power"
	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/shell"
)

type fakeLooks struct{}

func (fakeLooks) Clock(t time.Time) string { return t.Format("15:04") }
func (fakeLooks) Date(t time.Time) string  { return t.Format("Jan 2") }
func (fakeLooks) Theme() string            { return "light" }

// fakeScreen keeps what the charger showed, and the screen's changes in
// order ("on", "off").
type fakeScreen struct {
	frames  []shell.Frame
	changes []string
}

func (s *fakeScreen) Frame(f shell.Frame) { s.frames = append(s.frames, f) }
func (s *fakeScreen) Screen(on bool)      { s.changes = append(s.changes, onOff(on)) }

func (s *fakeScreen) last(t *testing.T) shell.Frame {
	t.Helper()
	if len(s.frames) == 0 {
		t.Fatal("no frame drawn")
	}
	return s.frames[len(s.frames)-1]
}

type rig struct {
	c    *charger
	scr  *fakeScreen
	acts []string
	fail error
	t0   time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	prev := i18n.Lang()
	i18n.SetLang(i18n.English)
	t.Cleanup(func() { i18n.SetLang(prev) })
	r := &rig{scr: &fakeScreen{}, t0: time.Date(2026, 11, 26, 7, 30, 0, 0, time.UTC)}
	r.c = &charger{looks: fakeLooks{}, screen: r.scr, log: func(string, ...any) {},
		act: func(what string) error {
			r.acts = append(r.acts, what)
			return r.fail
		}}
	return r
}

func (r *rig) at(d time.Duration) time.Time { return r.t0.Add(d) }

var plugged = power.Supply{Level: 47, Charging: true, Source: power.USB}

// plain is a gauge that says nothing besides the level.
var plain power.Gauge

func TestStartShowsTheBatteryAndGoesDark(t *testing.T) {
	r := newRig(t)
	r.c.begin(r.at(0), plugged, true, plain)
	f := r.scr.last(t)
	if f.T != "frame" || f.Idle == nil || f.Theme != "light" {
		t.Fatalf("frame %+v", f)
	}
	if f.Status.Battery != 47 || !f.Status.Charging || !f.Status.Percent {
		t.Errorf("status %+v, want 47%% charging with the percentage", f.Status)
	}
	if got := strings.Join(f.Idle.Lines, "|"); got != "Charging, 47%" || f.Idle.Hint != "Hold the power key to start" {
		t.Errorf("lines %q hint %q", got, f.Idle.Hint)
	}
	if f.Idle.Time != "07:30" || f.Idle.Date != "Nov 26" {
		t.Errorf("time %q date %q", f.Idle.Time, f.Idle.Date)
	}
	if !r.c.on || len(r.scr.changes) != 1 || r.scr.changes[0] != "on" {
		t.Fatalf("screen %v, changes %v", r.c.on, r.scr.changes)
	}
	r.c.tick(r.at(screenTime - time.Millisecond))
	if !r.c.on {
		t.Fatal("dark before screenTime")
	}
	r.c.tick(r.at(screenTime))
	if r.c.on || strings.Join(r.scr.changes, ",") != "on,off" {
		t.Fatalf("after %s: on %v, changes %v", screenTime, r.c.on, r.scr.changes)
	}
	if len(r.acts) != 0 {
		t.Errorf("acted: %v", r.acts)
	}
}

func TestShortPressTogglesTheScreen(t *testing.T) {
	r := newRig(t)
	r.c.begin(r.at(0), plugged, true, plain)
	r.c.tick(r.at(screenTime))
	r.c.key(r.at(10*time.Second), false)
	if !r.c.on {
		t.Fatal("a press did not light the screen")
	}
	// Lit again, it stays lit screenTime from the press.
	r.c.tick(r.at(10*time.Second + screenTime - time.Millisecond))
	if !r.c.on {
		t.Fatal("dark before screenTime after the press")
	}
	r.c.key(r.at(12*time.Second), false)
	if r.c.on || strings.Join(r.scr.changes, ",") != "on,off,on,off" {
		t.Fatalf("a press on a lit screen: on %v, changes %v", r.c.on, r.scr.changes)
	}
	if len(r.acts) != 0 {
		t.Errorf("short presses acted: %v", r.acts)
	}
}

func TestHoldStartsTheSystem(t *testing.T) {
	r := newRig(t)
	r.c.begin(r.at(0), plugged, true, plain)
	r.c.tick(r.at(screenTime))
	r.c.key(r.at(20*time.Second), true)
	if fmt.Sprint(r.acts) != "[reboot]" {
		t.Fatalf("acts %v, want a reboot", r.acts)
	}
	if !r.c.on || r.scr.last(t).Idle.Hint != "Starting…" {
		t.Errorf("screen %v, hint %q", r.c.on, r.scr.last(t).Idle.Hint)
	}
	// Leaving, nothing else happens: no press, no timeout, no power-off.
	r.c.key(r.at(21*time.Second), false)
	r.c.supply(r.at(21*time.Second), power.Supply{Level: 47}, true, plain)
	r.c.tick(r.at(40 * time.Second))
	if fmt.Sprint(r.acts) != "[reboot]" || !r.c.on {
		t.Errorf("after the reboot began: acts %v, screen %v", r.acts, r.c.on)
	}
}

func TestFailedRebootCanBeTriedAgain(t *testing.T) {
	r := newRig(t)
	r.fail = errors.New("no systemctl")
	r.c.begin(r.at(0), plugged, true, plain)
	r.c.key(r.at(time.Second), true)
	if r.c.leaving != "" || r.scr.last(t).Idle.Hint != "Hold the power key to start" {
		t.Fatalf("a failed reboot left leaving %q, hint %q", r.c.leaving, r.scr.last(t).Idle.Hint)
	}
	r.fail = nil
	r.c.key(r.at(2*time.Second), true)
	if fmt.Sprint(r.acts) != "[reboot reboot]" || r.c.leaving != actReboot {
		t.Errorf("acts %v, leaving %q", r.acts, r.c.leaving)
	}
}

func TestUnplugPowersOff(t *testing.T) {
	r := newRig(t)
	r.c.begin(r.at(0), plugged, true, plain)
	r.c.tick(r.at(screenTime))
	r.c.supply(r.at(10*time.Second), power.Supply{Level: 47}, true, plain)
	if !r.c.on || r.scr.last(t).Idle.Lines[0] != "Charger unplugged: powering off" || r.scr.last(t).Status.Charging {
		t.Fatalf("unplugged: screen %v, frame %+v", r.c.on, r.scr.last(t))
	}
	// The screen stays on while the phone is about to power off.
	r.c.tick(r.at(10*time.Second + unplugGrace - time.Millisecond))
	if len(r.acts) != 0 || !r.c.on {
		t.Fatalf("before the grace: acts %v, screen %v", r.acts, r.c.on)
	}
	r.c.tick(r.at(10*time.Second + unplugGrace))
	if fmt.Sprint(r.acts) != "[poweroff]" || r.scr.last(t).Idle.Hint != "Powering off…" {
		t.Errorf("acts %v, hint %q", r.acts, r.scr.last(t).Idle.Hint)
	}
}

func TestReplugKeepsCharging(t *testing.T) {
	r := newRig(t)
	r.c.begin(r.at(0), plugged, true, plain)
	r.c.supply(r.at(time.Second), power.Supply{Level: 47}, true, plain)
	r.c.supply(r.at(2*time.Second), plugged, true, plain)
	r.c.tick(r.at(time.Second + unplugGrace))
	r.c.tick(r.at(30 * time.Second))
	if len(r.acts) != 0 {
		t.Fatalf("a charger back in time still acted: %v", r.acts)
	}
	if r.c.on || r.scr.last(t).Idle.Lines[0] != "Charging, 47%" {
		t.Errorf("screen %v, line %q", r.c.on, r.scr.last(t).Idle.Lines[0])
	}
}

func TestStartedWithoutChargerPowersOff(t *testing.T) {
	r := newRig(t)
	r.c.begin(r.at(0), power.Supply{Level: 30}, true, plain)
	r.c.tick(r.at(unplugGrace))
	if fmt.Sprint(r.acts) != "[poweroff]" {
		t.Errorf("acts %v, want a power-off", r.acts)
	}
}

// A machine without a battery (the emulator without test_power) cannot
// tell a charger gone: it never powers off on its own.
func TestNoBatteryNeverPowersOff(t *testing.T) {
	r := newRig(t)
	r.c.begin(r.at(0), power.Supply{}, false, plain)
	r.c.supply(r.at(time.Second), power.Supply{}, false, plain)
	r.c.tick(r.at(time.Minute))
	if len(r.acts) != 0 {
		t.Fatalf("acts %v", r.acts)
	}
	f := r.scr.last(t)
	if f.Idle.Lines[0] != "No battery found" || f.Status.Charging {
		t.Errorf("frame %+v", f)
	}
}

func TestLinesFollowTheBattery(t *testing.T) {
	r := newRig(t)
	r.c.begin(r.at(0), plugged, true, plain)
	fast := power.Supply{Level: 60, Charging: true, Source: power.USBPD, Fast: true}
	for _, c := range []struct {
		sup   power.Supply
		gauge power.Gauge
		want  string
	}{
		{fast, plain, "Fast charging, 60%"},
		// At 100% the gauge may still be topping up: only its own Full is full.
		{power.Supply{Level: 100, Charging: true, Source: power.USBPD, Fast: true}, power.Gauge{Status: "Charging"}, "Fast charging, 100%"},
		{power.Supply{Level: 100, Charging: true, Source: power.USB}, power.Gauge{Status: "Full"}, "Full"},
		{power.Supply{Level: 61, Charging: true, Source: power.USB}, plain, "Charging, 61%"},
		{power.Supply{Charging: true, Source: power.USB}, power.Gauge{NoCapacity: true}, "Charging"},
		{fast, power.Gauge{NoCapacity: true}, "Fast charging"},
	} {
		r.c.supply(r.at(time.Second), c.sup, true, c.gauge)
		f := r.scr.last(t)
		if strings.Join(f.Idle.Lines, "|") != c.want || f.Status.Battery != c.sup.Level {
			t.Errorf("%+v %+v: lines %q, battery %d", c.sup, c.gauge, f.Idle.Lines, f.Status.Battery)
		}
	}
	// Dark, a change draws nothing until the screen lights.
	r.c.tick(r.at(time.Second + screenTime))
	n := len(r.scr.frames)
	r.c.supply(r.at(10*time.Second), power.Supply{Level: 62, Charging: true}, true, plain)
	if len(r.scr.frames) != n {
		t.Errorf("drew with the screen off")
	}
	r.c.key(r.at(11*time.Second), false)
	if r.scr.last(t).Status.Battery != 62 {
		t.Errorf("lit: battery %d, want 62", r.scr.last(t).Status.Battery)
	}
}

// The charge current, when the battery reports it, as a size (drivers
// differ in its sign); none once the charger is gone.
func TestChargeCurrentShown(t *testing.T) {
	r := newRig(t)
	r.c.begin(r.at(0), plugged, true, power.Gauge{Status: "Charging", Current: 1520400, HasCurrent: true})
	if got := strings.Join(r.scr.last(t).Idle.Lines, "|"); got != "Charging, 47%|1520 mA" {
		t.Errorf("lines %q, want the level and 1520 mA", got)
	}
	r.c.supply(r.at(time.Second), plugged, true, power.Gauge{Current: -799600, HasCurrent: true})
	if got := strings.Join(r.scr.last(t).Idle.Lines, "|"); got != "Charging, 47%|800 mA" {
		t.Errorf("negative current: lines %q", got)
	}
	r.c.supply(r.at(2*time.Second), power.Supply{Level: 47}, true, power.Gauge{Current: -300000, HasCurrent: true})
	if got := strings.Join(r.scr.last(t).Idle.Lines, "|"); got != "Charger unplugged: powering off" {
		t.Errorf("unplugged: lines %q", got)
	}
}

// A hold on a battery below bootMinLevel does not start the system: the
// screen says why, and the system starts once the level is there.
func TestLowBatteryStartsOnceCharged(t *testing.T) {
	r := newRig(t)
	low := power.Supply{Level: 2, Charging: true, Source: power.USB}
	r.c.begin(r.at(0), low, true, plain)
	if f := r.scr.last(t); f.Idle.Lines[0] != "Charging, 2% - too low to start" || f.Idle.Hint != "Hold the power key to start" {
		t.Fatalf("line %q, hint %q", f.Idle.Lines[0], f.Idle.Hint)
	}
	r.c.tick(r.at(screenTime))
	r.c.key(r.at(10*time.Second), true)
	if len(r.acts) != 0 {
		t.Fatalf("started at 2%%: %v", r.acts)
	}
	if f := r.scr.last(t); !r.c.on || f.Idle.Hint != "Starting at 3%" || f.Idle.Lines[0] != "Charging, 2% - too low to start" {
		t.Fatalf("after the hold: screen %v, line %q, hint %q", r.c.on, f.Idle.Lines[0], f.Idle.Hint)
	}
	r.c.tick(r.at(10*time.Second + screenTime))
	r.c.supply(r.at(time.Minute), low, true, plain)
	if len(r.acts) != 0 || r.c.on {
		t.Fatalf("still 2%%: acts %v, screen %v", r.acts, r.c.on)
	}
	r.c.supply(r.at(3*time.Minute), power.Supply{Level: 3, Charging: true, Source: power.USB}, true, plain)
	if fmt.Sprint(r.acts) != "[reboot]" || !r.c.on || r.scr.last(t).Idle.Hint != "Starting…" {
		t.Errorf("at 3%%: acts %v, screen %v, hint %q", r.acts, r.c.on, r.scr.last(t).Idle.Hint)
	}
}

// The gauge's own Critical is too low whatever its percentage says; a
// battery whose level cannot be read is not held back by its 0.
func TestCriticalGaugeIsTooLowAnUnknownLevelIsNot(t *testing.T) {
	r := newRig(t)
	r.c.begin(r.at(0), plugged, true, power.Gauge{Critical: true})
	r.c.key(r.at(time.Second), true)
	if len(r.acts) != 0 || r.scr.last(t).Idle.Lines[0] != "Charging, 47% - too low to start" {
		t.Fatalf("critical: acts %v, line %q", r.acts, r.scr.last(t).Idle.Lines[0])
	}
	r.c.supply(r.at(2*time.Second), plugged, true, plain)
	if fmt.Sprint(r.acts) != "[reboot]" {
		t.Errorf("no longer critical: acts %v", r.acts)
	}

	r = newRig(t)
	r.c.begin(r.at(0), power.Supply{Charging: true}, true, power.Gauge{NoCapacity: true, Critical: true})
	if got := r.scr.last(t).Idle.Lines[0]; got != "Charging - too low to start" {
		t.Errorf("critical without a level: line %q", got)
	}
	r = newRig(t)
	r.c.begin(r.at(0), power.Supply{Charging: true}, true, power.Gauge{NoCapacity: true})
	r.c.key(r.at(time.Second), true)
	if fmt.Sprint(r.acts) != "[reboot]" {
		t.Errorf("no level to read: acts %v, want a reboot", r.acts)
	}
}

// A board on a bench supply (no battery) always starts.
func TestNoBatteryIsNeverTooLow(t *testing.T) {
	r := newRig(t)
	r.c.begin(r.at(0), power.Supply{}, false, plain)
	r.c.key(r.at(time.Second), true)
	if fmt.Sprint(r.acts) != "[reboot]" {
		t.Errorf("acts %v, want a reboot", r.acts)
	}
}

// Unplugged while a start waits for the level, the phone powers off.
func TestUnplugWhileWaitingToStartPowersOff(t *testing.T) {
	r := newRig(t)
	r.c.begin(r.at(0), power.Supply{Level: 1, Charging: true}, true, plain)
	r.c.key(r.at(time.Second), true)
	r.c.supply(r.at(2*time.Second), power.Supply{Level: 1}, true, plain)
	r.c.tick(r.at(2*time.Second + unplugGrace))
	if fmt.Sprint(r.acts) != "[poweroff]" {
		t.Errorf("acts %v, want a power-off", r.acts)
	}
}

// The phone suspends only with the screen off, sleepAfter after the
// screen went off or a key, not while a charger is unplugged or being
// identified, nor just after a wake; the RTC wakes it every refresh, or
// every startRefresh while a start waits.
func TestSleepPlan(t *testing.T) {
	r := newRig(t)
	r.c.begin(r.at(0), plugged, true, plain)
	plan := func(d time.Duration, why string, wake time.Duration) {
		t.Helper()
		w, got := r.c.sleepPlan(r.at(d))
		var want time.Time
		if why == "" {
			want = r.at(d + wake)
		}
		if got != why || !w.Equal(want) {
			t.Errorf("at %s: %q until %v, want %q until %v", d, got, w.Sub(r.t0), why, want.Sub(r.t0))
		}
	}
	plan(time.Second, "screen on", 0)
	r.c.tick(r.at(screenTime))
	plan(screenTime+sleepAfter-time.Millisecond, "screen just off", 0)
	plan(screenTime+sleepAfter, "", refresh)

	// The power key lights it; darkened by the key, the key counts.
	r.c.key(r.at(time.Minute), false)
	plan(time.Minute, "screen on", 0)
	r.c.key(r.at(time.Minute+time.Second), false)
	plan(time.Minute+time.Second+sleepAfter-time.Millisecond, "key", 0)
	plan(time.Minute+time.Second+sleepAfter, "", refresh)

	r.c.supply(r.at(2*time.Minute), power.Supply{Level: 47, Charging: true, Detecting: true}, true, plain)
	plan(2*time.Minute, "charger being identified", 0)
	r.c.supply(r.at(2*time.Minute), plugged, true, plain)
	r.c.woken(r.at(3 * time.Minute))
	plan(3*time.Minute+awakeAfterWake-time.Millisecond, "just woke", 0)
	plan(3*time.Minute+awakeAfterWake, "", refresh)

	// Unplugged, it stays awake to power off.
	r.c.supply(r.at(4*time.Minute), power.Supply{Level: 47}, true, plain)
	r.c.dark(r.at(4 * time.Minute))
	plan(5*time.Minute, "charger unplugged", 0)

	r = newRig(t)
	r.c.begin(r.at(0), power.Supply{Level: 2, Charging: true}, true, plain)
	r.c.key(r.at(time.Second), true)
	r.c.tick(r.at(time.Second + screenTime))
	plan(time.Minute, "", startRefresh)
	r.c.supply(r.at(2*time.Minute), plugged, true, plain)
	plan(2*time.Minute, actReboot, 0)
}
