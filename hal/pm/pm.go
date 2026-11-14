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
	"os"
	"path/filepath"
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
}

// System is the kernel's.
type System struct {
	mode  string
	alarm string
}

// Open finds how this system can sleep and the RTC that wakes it. Without a
// wake alarm alarms could not ring, so there is no sleep.
func Open() (*System, error) {
	raw, err := os.ReadFile(filepath.Join(hal.SysRoot, "power", "state"))
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
	raw, err := os.ReadFile(filepath.Join(hal.SysRoot, "power", "wakeup_count"))
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
		if err := write(s.alarm, fmt.Sprint(wake.Unix())); err != nil {
			return err
		}
	}
	err := write(filepath.Join(hal.SysRoot, "power", "state"), s.mode)
	_ = write(s.alarm, "0")
	if errors.Is(err, syscall.EBUSY) {
		return ErrAborted
	}
	return err
}

func (s *System) WokenBy() string {
	raw, err := os.ReadFile(filepath.Join(hal.SysRoot, "power", "pm_wakeup_irq"))
	if err != nil {
		return ""
	}
	return "irq " + strings.TrimSpace(string(raw))
}

// KeysWake is whether some key can wake the system: an input device whose
// device is a wake-up source (gpio-keys, the PMIC's power key). Without one
// a suspended phone could not be woken by its user.
func KeysWake() bool {
	paths, _ := filepath.Glob(filepath.Join(hal.SysRoot, "class", "input", "input*", "device", "power", "wakeup"))
	for _, p := range paths {
		if raw, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(raw)) == "enabled" {
			return true
		}
	}
	return false
}

// write writes to an attribute that exists (sysfs: never create).
func write(path, v string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(v)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
