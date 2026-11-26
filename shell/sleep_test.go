package shell

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kario-os/kario/hal"
)

// wokenSleeper resumes at once, as the kernel does when the power key's
// interrupt wakes the phone.
type wokenSleeper struct{ slept chan struct{} }

func (s *wokenSleeper) Mode() string                  { return "freeze" }
func (s *wokenSleeper) Count() (string, error)        { return "1", nil }
func (s *wokenSleeper) Sleep(string, time.Time) error { s.slept <- struct{}{}; return nil }
func (s *wokenSleeper) WokenBy() string               { return "irq 90" }
func (s *wokenSleeper) WakeLocks() []string           { return nil }

// lockedSleeper is a kernel with wake locks held: reading its count would
// block until they go.
type lockedSleeper struct {
	locks   []string
	counted atomic.Bool
}

func (s *lockedSleeper) Mode() string { return "mem" }
func (s *lockedSleeper) Count() (string, error) {
	s.counted.Store(true)
	return "", errors.New("the count blocks while a wake lock is held")
}
func (s *lockedSleeper) Sleep(string, time.Time) error { return nil }
func (s *lockedSleeper) WokenBy() string               { return "" }
func (s *lockedSleeper) WakeLocks() []string           { return s.locks }

// The 6T's bring-up image holds the wake lock phone-bringup: the Shell went
// to sleep anyway, blocked reading the count, and the suspend case said
// only "slept 0 times (awake: )". The Shell names the lock as what keeps
// the phone up, and the case is skipped, saying how to let it sleep.
func TestForeignWakeLockKeepsThePhoneUp(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	log := screenLogTo(t)
	s := &lockedSleeper{locks: []string{"phone-bringup"}}
	old, allowed := c.sleep.sys, c.sleep.allowed
	t.Cleanup(func() { c.sleep.sys, c.sleep.allowed, c.sleep.going = old, allowed, false })
	c.sleep.sys, c.sleep.allowed = s, true
	c.screenOff("timeout")
	c.checkSleep()
	c.screen.offAt = c.now().Add(-time.Minute)
	c.checkSleep()
	c.checkSleep()
	if _, why := c.Sleeps(); c.sleep.going || why != "wake lock phone-bringup" {
		t.Fatalf("going to sleep: %v, awake for %q", c.sleep.going, why)
	}
	if s.counted.Load() {
		t.Error("the count was read with the wake lock held")
	}
	// Report 007: a night awake under the bring-up lock logged nothing of it.
	if got := strings.Count(log.String(), "phone-shell: sleep: awake"); got != 1 || !strings.Contains(log.String(), "phone-shell: sleep: awake (wake lock phone-bringup)\n") {
		t.Errorf("logged %d times why it stays awake, want once:\n%s", got, log.String())
	}
	c.sleep.allowed = false
	c.Wake("key")
	var out strings.Builder
	r := &Runner{C: c, Out: &out}
	r.Run("== suspend\nrequire-sleep\nsystem-sleep on\nslept 1 100\n")
	if got, m := caseLine(r.Cases), r.Cases[0].Message; got != "suspend SKIP" || !strings.Contains(m, "the wake lock phone-bringup is held") ||
		!strings.Contains(m, "systemctl stop phone-bringup-awake") {
		t.Fatalf("with the wake lock: %s %q\n%s", got, m, out.String())
	}
	s.locks = nil
	r.Run("== suspend\nrequire-sleep\n")
	if got := caseLine(r.Cases); got != "suspend PASS" {
		t.Fatalf("without it: %s\n%s", got, out.String())
	}
}

// The power key that woke the phone reaches the Shell after the resume
// (through the renderer, which reads it once it is thawed), before or after
// the Shell has heard that the phone woke: either way it lights the screen,
// as nothing else lights it at a wake (the key would then turn it off
// again), and the phone stays awake.
func TestPowerKeyAroundAWake(t *testing.T) {
	for _, first := range []string{"key", "wake"} {
		t.Run(first+" first", func(t *testing.T) {
			c := snapCore(t)
			s := &wokenSleeper{slept: make(chan struct{}, 1)}
			c.sleep.sys, c.sleep.allowed = s, true
			c.screenOff("timeout")
			c.screen.offAt = c.now().Add(-time.Minute)
			c.checkSleep()
			if !c.sleep.going {
				t.Fatalf("no sleep: %s", c.sleep.why)
			}
			pumpUntil(t, c, func() bool { return len(s.slept) == 1 })
			if first == "key" {
				c.Press(Key{Name: KeyPower})
			}
			pumpUntil(t, c, func() bool { return !c.sleep.going })
			if first == "wake" {
				c.Press(Key{Name: KeyPower})
			}
			if c.ScreenOff() {
				t.Fatal("the power key that woke the phone left the screen off")
			}
			if n, _ := c.Sleeps(); n != 1 || c.WokenBy() != "irq 90" {
				t.Errorf("slept %d times, woken by %q", n, c.WokenBy())
			}
			if _, why := c.sleepPlan(); why != "screen on" {
				t.Errorf("after the wake it would sleep again (%q)", why)
			}
		})
	}
}

// The board says whether it can be woken, the kernel whether it sleeps,
// the keys whether one wakes it (the emulator's do not): phone-charger
// takes the Shell's decision.
func TestOpenSleep(t *testing.T) {
	root := t.TempDir()
	saved := hal.SysRoot
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = saved })
	for f, v := range map[string]string{"power/state": "freeze mem\n", "class/rtc/rtc0/wakealarm": ""} {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sku := SKU{ID: "test", RTC: SKURTC{Alarm: "absolute"}}
	if _, err := OpenSleep(sku); err == nil || err.Error() != "the board cannot be woken from suspend" {
		t.Errorf("no suspend in the board file: %v", err)
	}
	sku.Suspend = true
	if _, err := OpenSleep(sku); err == nil || err.Error() != "no key can wake this board" {
		t.Errorf("no key that wakes: %v", err)
	}
	p := filepath.Join(root, "class", "input", "input0", "device", "power", "wakeup")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("enabled\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenSleep(sku); err != nil || s.Mode() != "mem" || !s.AbsoluteAlarm {
		t.Errorf("OpenSleep = %+v, %v", s, err)
	}
}
