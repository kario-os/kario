// Package rtc is the real-time clock: the PMIC's or the SoC's counter that
// keeps running while the phone is off, which the kernel sets the system
// clock from at boot (CONFIG_RTC_HCTOSYS) and which wakes it from suspend
// (hal/pm). The board says how it keeps time (sku.json's rtc). Where the RTC
// can be set, setting the time writes it, as hwclock does. Where it cannot
// (the OnePlus 6T's PMIC RTC on mainline: its device tree has no
// allow-set-time and no offset cell, so RTC_SET_TIME fails with ENODEV),
// the system time is kept as an offset from it, as Android keeps it: the
// offset is measured whenever the time is set and saved in a file of its
// own (Offset), and at boot the clock is set from the RTC plus that offset
// (Restore, phone-clock.service) before the services start.
package rtc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/hal"
)

// Name is the RTC this package reads: the one the kernel sets the clock
// from (CONFIG_RTC_HCTOSYS_DEVICE) and the first, whose wake alarm hal/pm
// sets.
const Name = "rtc0"

// OffsetFile is the offset's file in the persistent area (/var/lib/slots,
// outside the data a factory reset erases: the clock is the phone's, not
// the user's).
const OffsetFile = "rtc-offset.json"

// OwnerEnv says whose the clock is when it is not this system's: "host"
// in a chroot on another system (tools/phone-chroot), which keeps its own
// time; nothing here then sets the clock, starts NTP or writes the RTC.
const OwnerEnv = "PHONE_CLOCK"

// Read is the RTC's time in whole seconds since 1970 (its since_epoch: the
// RTC runs in UTC).
var Read = func() (int64, error) {
	raw, err := os.ReadFile(filepath.Join(hal.SysRoot, "class", "rtc", Name, "since_epoch"))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
}

// Hctosys: the kernel set the system clock from the RTC at boot.
func Hctosys() bool {
	raw, err := os.ReadFile(filepath.Join(hal.SysRoot, "class", "rtc", Name, "hctosys"))
	return err == nil && strings.TrimSpace(string(raw)) == "1"
}

// Wait waits until the RTC can be read and the kernel has set the clock
// from it, which it does when the RTC registers (CONFIG_RTC_HCTOSYS): a
// driver built as a module (rtc-pm8xxx in Nura's 6T configuration)
// registers once udev has loaded it, and a clock set before that would be
// set back to the RTC's own count. A kernel without hctosys never says so:
// after hctosysWait the RTC is taken as it is.
func Wait(timeout time.Duration) error {
	return wait(timeout, Read, Hctosys, time.Now, func() { time.Sleep(10 * poll) })
}

const hctosysWait = time.Second

func wait(timeout time.Duration, read func() (int64, error), hctosys func() bool, now func() time.Time, pause func()) error {
	start := now()
	var since time.Time
	for {
		_, err := read()
		t := now()
		switch {
		case err == nil && since.IsZero():
			since = t
			fallthrough
		case err == nil:
			if hctosys() || t.Sub(since) >= hctosysWait {
				return nil
			}
		case t.Sub(start) >= timeout:
			return fmt.Errorf("no %s after %v: %w", Name, timeout, err)
		}
		pause()
	}
}

// Write sets the RTC (RTC_SET_TIME); a read-only one refuses it.
var Write = func(t time.Time) error {
	fd, err := unix.Open("/dev/"+Name, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	u := t.UTC()
	return unix.IoctlSetRTCTime(fd, &unix.RTCTime{Sec: int32(u.Second()), Min: int32(u.Minute()), Hour: int32(u.Hour()),
		Mday: int32(u.Day()), Mon: int32(u.Month()) - 1, Year: int32(u.Year()) - 1900})
}

// SetSystem sets the system clock.
var SetSystem = func(t time.Time) error {
	tv := unix.NsecToTimeval(t.UnixNano())
	return unix.Settimeofday(&tv)
}

// Synced: NTP keeps the system clock, as the kernel says (STA_UNSYNC clear
// and an error bound under 16 s, what timedatectl reads); setting the clock
// by other means marks it unsynchronised again.
var Synced = func() bool {
	var tx unix.Timex
	if _, err := unix.Adjtimex(&tx); err != nil {
		return false
	}
	return tx.Status&unix.STA_UNSYNC == 0 && tx.Maxerror < 16_000_000
}

var (
	euid     = os.Geteuid
	chrooted = func() bool {
		var root, init unix.Stat_t
		if unix.Stat("/", &root) != nil || unix.Stat("/proc/1/root", &init) != nil {
			return false
		}
		return root.Dev != init.Dev || root.Ino != init.Ino
	}
)

// Ours: this system owns the clock and may set it: root, not told the
// clock is the host's (OwnerEnv), and not in a chroot (a root that is not
// init's), where the clock is the other system's.
var Ours = func() bool { return euid() == 0 && os.Getenv(OwnerEnv) != "host" && !chrooted() }

// Offset is what is kept of a read-only RTC: the system time less the
// RTC's, measured at At (the system time then) after By had set the time
// ("ntp", "network", "user", or "save": phone-clock save).
type Offset struct {
	MS int64     `json:"offset_ms"`
	At time.Time `json:"at"`
	By string    `json:"by"`
}

func (o Offset) Duration() time.Duration { return time.Duration(o.MS) * time.Millisecond }

// Moved: o is more than slack away from the offset kept (or none was).
func (o Offset) Moved(kept Offset, slack time.Duration) bool {
	d := o.Duration() - kept.Duration()
	return kept.At.IsZero() || d > slack || d < -slack
}

// poll is how often Measure reads the RTC while it waits for its tick.
const poll = 5 * time.Millisecond

// Measure is the system time less the RTC's now. The RTC counts whole
// seconds: it is read until its second turns over, when its time is that
// second exactly, so the offset is good to the polling interval rather than
// to a second.
func Measure(by string) (Offset, error) {
	return measure(by, Read, time.Now, func() { time.Sleep(poll) })
}

func measure(by string, read func() (int64, error), now func() time.Time, pause func()) (Offset, error) {
	first, err := read()
	if err != nil {
		return Offset{}, err
	}
	start := now()
	for {
		pause()
		s, err := read()
		if err != nil {
			return Offset{}, err
		}
		t := now()
		if s != first {
			return Offset{MS: t.Sub(time.Unix(s, 0)).Milliseconds(), At: t.Round(0).UTC(), By: by}, nil
		}
		if t.Sub(start) > 1500*time.Millisecond {
			return Offset{}, fmt.Errorf("%s does not tick (at %d s)", Name, s)
		}
	}
}

// Load reads the offset kept in path.
func Load(path string) (Offset, error) {
	var o Offset
	raw, err := os.ReadFile(path)
	if err != nil {
		return o, err
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return o, fmt.Errorf("%s: %w", path, err)
	}
	if o.At.IsZero() {
		return o, fmt.Errorf("%s: no time it was measured at", path)
	}
	return o, nil
}

// Save keeps o in path, whole or not at all.
func Save(path string, o Offset) error {
	raw, _ := json.MarshalIndent(o, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Restore sets the system clock from the RTC and the offset kept in path.
// It returns the time it set, or the zero time and why it left the clock
// alone.
func Restore(path string) (time.Time, string, error) {
	o, err := Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, "no offset kept yet: the time has not been set on this phone", nil
	}
	if err != nil {
		return time.Time{}, "", err
	}
	s, err := Read()
	if err != nil {
		return time.Time{}, "", err
	}
	t, why := restoreTime(o, s, time.Now())
	if why != "" {
		return time.Time{}, why, nil
	}
	return t, "", SetSystem(t)
}

// restoreTime is the RTC's second s, half a second in (when in it the RTC
// was read is not known), plus the offset; none when that is before the
// offset was measured (the RTC lost its power and started over: the offset
// no longer holds), or when the clock is that already.
func restoreTime(o Offset, s int64, now time.Time) (time.Time, string) {
	t := time.Unix(s, 0).Add(time.Second/2 + o.Duration()).UTC()
	if t.Before(o.At) {
		return time.Time{}, fmt.Sprintf("%s plus the offset is %s, before the offset was measured (%s): the RTC started over, the offset no longer holds",
			Name, t.Format(time.RFC3339), o.At.Format(time.RFC3339))
	}
	if d := t.Sub(now); d > -time.Second && d < time.Second {
		return time.Time{}, "the clock is right already"
	}
	return t, ""
}
