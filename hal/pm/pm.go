// Package pm puts the whole system to sleep through the kernel's
// /sys/power: suspend-to-RAM where the board has it (state "mem"), else
// suspend-to-idle ("freeze"), with an RTC wake alarm for the next thing due.
// The wakeup_count handshake (Documentation/ABI/testing/sysfs-power) keeps a
// wake-up event that arrives while the caller decides from being lost: the
// count read before deciding is written back before suspending, and the
// kernel refuses it when an event came in between.
package pm

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kario-os/kario/hal"
)

// ErrAborted is a suspend called off by a wake-up event.
var ErrAborted = errors.New("suspend aborted by a wake-up event")

// Sleeper suspends the system.
type Sleeper interface {
	// Mode is how it sleeps: "mem" or "freeze".
	Mode() string
	// Count is the wake-up event count, read before deciding to sleep; it
	// blocks while the kernel is still handling wake-up events.
	Count() (string, error)
	// Sleep suspends until a wake-up event, or the RTC at wake when it is
	// not zero, and returns when the system runs again (ErrAborted when an
	// event came after count was read).
	Sleep(count string, wake time.Time) error
	// WokenBy is what the kernel says woke it last ("" when it does not).
	WokenBy() string
	// WakeLocks are the wake locks user space holds: while one is, the
	// kernel does not suspend and Count blocks.
	WakeLocks() []string
}

// System is the kernel's.
type System struct {
	// AbsoluteAlarm writes the wake alarm as the wake time's seconds since
	// 1970 instead of "+N": only right where the RTC runs on the system's
	// clock (a settable RTC set with it; sku.json rtc.alarm).
	AbsoluteAlarm bool
	mode          string
	alarm         string
	// sources: the wake-up sources that woke the last sleep or called it
	// off (wakeups).
	sources []string
}

// Open finds how this system can sleep and the RTC that wakes it. Without a
// wake alarm alarms could not ring, so there is no sleep.
func Open() (*System, error) {
	raw, err := hal.ReadAttr(filepath.Join(hal.SysRoot, "power", "state"))
	if err != nil {
		return nil, err
	}
	s := &System{}
	for _, m := range strings.Fields(string(raw)) {
		switch {
		case m == "mem":
			s.mode = m
		case m == "freeze" && s.mode == "":
			s.mode = m
		}
	}
	if s.mode == "" {
		return nil, fmt.Errorf("the kernel offers no sleep state (%q)", strings.TrimSpace(string(raw)))
	}
	alarms, _ := filepath.Glob(filepath.Join(hal.SysRoot, "class", "rtc", "rtc*", "wakealarm"))
	if len(alarms) == 0 {
		return nil, errors.New("no RTC wake alarm")
	}
	s.alarm = alarms[0]
	return s, nil
}

func (s *System) Mode() string { return s.mode }

func (s *System) Count() (string, error) {
	raw, err := hal.ReadAttr(filepath.Join(hal.SysRoot, "power", "wakeup_count"))
	return strings.TrimSpace(string(raw)), err
}

func (s *System) Sleep(count string, wake time.Time) error {
	if err := write(filepath.Join(hal.SysRoot, "power", "wakeup_count"), count); err != nil {
		return ErrAborted
	}
	// The alarm is cleared first: the kernel refuses a new one over a pending one.
	if err := write(s.alarm, "0"); err != nil {
		return err
	}
	if !wake.IsZero() {
		v := alarmIn(wake, time.Now())
		if s.AbsoluteAlarm {
			v = alarmAt(wake)
		}
		if err := write(s.alarm, v); err != nil {
			return err
		}
	}
	before := wakeups()
	err := write(filepath.Join(hal.SysRoot, "power", "state"), s.mode)
	s.sources = rose(before, wakeups())
	_ = write(s.alarm, "0")
	if errors.Is(err, syscall.EBUSY) {
		if len(s.sources) > 0 {
			return fmt.Errorf("%w (%s)", ErrAborted, strings.Join(s.sources, ", "))
		}
		return ErrAborted
	}
	return err
}

// WokenBy names the interrupt that woke the system (pm_wakeup_irq, which
// the kernel clears at each suspend) and the wake-up sources that
// reported an event meanwhile: a modem's message over GLINK wakes the
// system through its channel's source, with no wake-up interrupt.
func (s *System) WokenBy() string {
	var by []string
	if raw, err := hal.ReadAttr(filepath.Join(hal.SysRoot, "power", "pm_wakeup_irq")); err == nil {
		by = append(by, "irq "+strings.TrimSpace(string(raw)))
	}
	if len(s.sources) > 0 {
		by = append(by, "wake-up sources "+strings.Join(s.sources, ", "))
	}
	return strings.Join(by, "; ")
}

// wakeups is each wake-up source's wakeup_count (by its name): how often
// it reported an event while the kernel checked for them, from the write
// of /sys/power/wakeup_count to the next event that called a suspend off
// or ended one.
func wakeups() map[string]int64 {
	dirs, _ := filepath.Glob(filepath.Join(hal.SysRoot, "class", "wakeup", "wakeup*"))
	out := make(map[string]int64, len(dirs))
	for _, d := range dirs {
		name, err := hal.ReadAttr(filepath.Join(d, "name"))
		if err != nil {
			continue
		}
		raw, err := hal.ReadAttr(filepath.Join(d, "wakeup_count"))
		if err != nil {
			continue
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil {
			out[strings.TrimSpace(string(name))] = n
		}
	}
	return out
}

// rose: the sources whose count grew from before to after, by name.
func rose(before, after map[string]int64) []string {
	var out []string
	for name, n := range after {
		if n > before[name] {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// WakeLocks reads /sys/power/wake_lock, which lists the ones held (a
// kernel without CONFIG_PM_WAKELOCKS has none).
func (s *System) WakeLocks() []string {
	raw, err := os.ReadFile(filepath.Join(hal.SysRoot, "power", "wake_lock"))
	if err != nil {
		return nil
	}
	return strings.Fields(string(raw))
}

// KeysWake is whether some key can wake the system: an input device whose
// device is a wake-up source (gpio-keys, the PMIC's power key). Without one
// a suspended phone could not be woken by its user.
func KeysWake() bool {
	paths, _ := filepath.Glob(filepath.Join(hal.SysRoot, "class", "input", "input*", "device", "power", "wakeup"))
	for _, p := range paths {
		if raw, err := hal.ReadAttr(p); err == nil && strings.TrimSpace(string(raw)) == "enabled" {
			return true
		}
	}
	return false
}

// ChargersWake lets each charger that can wake the system do so (its
// device's power/wakeup "disabled" made "enabled": the PMI8998's charger,
// whose plug interrupt is a wake-up interrupt) and names the chargers that
// wake it, so that plugging one in or out ends a suspend.
func ChargersWake() []string {
	supplies, _ := filepath.Glob(filepath.Join(hal.SysRoot, "class", "power_supply", "*"))
	var out []string
	for _, d := range supplies {
		if raw, err := hal.ReadAttr(filepath.Join(d, "type")); err != nil || strings.TrimSpace(string(raw)) == "Battery" {
			continue
		}
		p := filepath.Join(d, "device", "power", "wakeup")
		raw, err := hal.ReadAttr(p)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(raw)) == "disabled" && write(p, "enabled") != nil {
			continue
		}
		out = append(out, filepath.Base(d))
	}
	return out
}

// write writes to an attribute that exists (sysfs: never create).
// alarmIn is what wakealarm takes for wake: "+N", seconds from the RTC's
// own time (sysfs-class-rtc), rounded up so that it rings at wake or just
// after. An absolute time would be the RTC's clock, which need not be the
// system's: the 6T's RTC is read-only on mainline and counts from 1970
// while NTP sets the system clock, and its alarm fell some 57 years ahead.
func alarmIn(wake, now time.Time) string {
	return "+" + strconv.FormatInt(max(int64(math.Ceil(wake.Sub(now).Seconds())), 1), 10)
}

// alarmAt is wake as an absolute alarm, rounded up as alarmIn rounds.
func alarmAt(wake time.Time) string {
	s := wake.Unix()
	if wake.Nanosecond() > 0 {
		s++
	}
	return strconv.FormatInt(s, 10)
}

// write is a variable for the tests, which see each value it writes.
var write = writeFile

func writeFile(path, v string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return hal.WriteAttr(path, []byte(v))
}
