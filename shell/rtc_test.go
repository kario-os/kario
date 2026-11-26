package shell

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/hal/rtc"
	"github.com/kario-os/kario/ui"
)

// rtcCore owns the clock on a board with the RTC r: the system clock, the
// RTC's measurement and its writes are the test's; it returns the times
// set, the RTC writes and the offset the RTC is measured at.
func rtcCore(t *testing.T, r SKURTC) (c *Core, set, written *[]time.Time, offset *time.Duration) {
	c = localeCore(t)
	c.ev = make(chan func(), 16)
	c.cfg.PersistDir = t.TempDir()
	c.cfg.SKU.RTC = r
	set, written, offset = &[]time.Time{}, &[]time.Time{}, new(time.Duration)
	*offset = 56 * 365 * 24 * time.Hour
	can, synced, sys, measure, write := canSetClock, ntpSynced, setSystemClock, measureRTC, writeRTC
	t.Cleanup(func() {
		canSetClock, ntpSynced, setSystemClock, measureRTC, writeRTC = can, synced, sys, measure, write
	})
	canSetClock, ntpSynced = func() bool { return true }, func() bool { return false }
	setSystemClock = func(at time.Time, _ string) error { *set = append(*set, at); return nil }
	measureRTC = func(by string) (rtc.Offset, error) {
		return rtc.Offset{MS: offset.Milliseconds(), At: time.Now().Round(0).UTC(), By: by}, nil
	}
	// The Shell writes the RTC from a goroutine of its own; the write is
	// counted on the test's, which runs what the Shell posts.
	writeRTC = func(at time.Time) error {
		c.ev <- func() { *written = append(*written, at) }
		if r.ReadOnly {
			return syscall.ENODEV
		}
		return nil
	}
	return c, set, written, offset
}

// settle runs what the Shell's goroutines posted until none is left.
func settle(t *testing.T, c *Core) {
	t.Helper()
	for {
		select {
		case f := <-c.ev:
			f()
		case <-time.After(200 * time.Millisecond):
			return
		}
	}
}

func keptOffset(t *testing.T, c *Core) rtc.Offset {
	t.Helper()
	o, err := rtc.Load(c.rtcPath())
	if err != nil {
		t.Fatalf("the offset kept: %v", err)
	}
	return o
}

// On a read-only RTC (the 6T's) the time set by the user or the network is
// kept as an offset from it, never written to it; while NTP keeps the
// clock, the offset follows once the RTC drifted.
func TestReadOnlyRTCKeepsTheOffset(t *testing.T) {
	if !alone(t) {
		return
	}
	c, set, written, offset := rtcCore(t, SKURTC{ReadOnly: true, Offset: true})
	c.startRTC()
	if _, err := os.Stat(c.rtcPath()); !os.IsNotExist(err) {
		t.Fatalf("an offset before the time was set: %v", err)
	}
	if err := c.setLocale(ui.SettingAutoTime, 0); err != nil {
		t.Fatal(err)
	}
	wall := float64(time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC).Unix())
	if err := c.setLocale(ui.SettingTime, wall); err != nil {
		t.Fatalf("the time set by hand on a read-only RTC: %v", err)
	}
	settle(t, c)
	if o := keptOffset(t, c); len(*set) != 1 || o.By != "user" || o.MS != offset.Milliseconds() || len(*written) != 0 {
		t.Fatalf("set %v, kept %+v, RTC written %v", *set, o, *written)
	}

	if err := c.setLocale(ui.SettingAutoTime, 1); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	*offset += time.Hour
	c.syncFromNITZ(&nitz{Offset: 480, Time: now + 3600, Received: now})
	settle(t, c)
	if o := keptOffset(t, c); len(*set) != 2 || o.By != "network" || o.MS != offset.Milliseconds() {
		t.Fatalf("after NITZ: set %v, kept %+v", *set, o)
	}

	c.rtcTick()
	settle(t, c)
	if o := keptOffset(t, c); o.By != "network" {
		t.Fatalf("followed a clock NTP does not keep: %+v", o)
	}
	ntpSynced = func() bool { return true }
	*offset += 300 * time.Millisecond
	c.locale.rtc.checked = time.Time{}
	c.rtcTick()
	settle(t, c)
	if o := keptOffset(t, c); o.By != "network" {
		t.Fatalf("kept anew for a drift within %v: %+v", rtcSlack, o)
	}
	*offset += time.Second
	c.rtcTick()
	settle(t, c)
	if o := keptOffset(t, c); o.By != "network" {
		t.Fatalf("checked again before %v: %+v", rtcEvery, o)
	}
	c.locale.rtc.checked = time.Now().Add(-rtcEvery)
	c.rtcTick()
	settle(t, c)
	if o := keptOffset(t, c); o.By != "ntp" || o.MS != offset.Milliseconds() {
		t.Fatalf("NTP's clock not followed: %+v", o)
	}

	// The next Shell starts from the offset kept.
	again, _, _, _ := rtcCore(t, SKURTC{ReadOnly: true, Offset: true})
	again.cfg.PersistDir = c.cfg.PersistDir
	again.startRTC()
	if again.locale.rtc.kept != keptOffset(t, c) {
		t.Errorf("started with %+v", again.locale.rtc.kept)
	}
}

// A time set while a measurement is under way is measured again: the one
// under way may have read the clock before.
func TestOffsetMeasuredAgain(t *testing.T) {
	c, _, _, _ := rtcCore(t, SKURTC{ReadOnly: true, Offset: true})
	c.locale.rtc.busy = true
	c.measureOffset("user")
	c.measureOffset("ntp")
	if c.locale.rtc.again != "user" {
		t.Fatalf("again %q", c.locale.rtc.again)
	}
	c.locale.rtc.busy = false
	c.measureOffset(c.locale.rtc.again)
	settle(t, c)
	if o := keptOffset(t, c); o.By != "user" || c.locale.rtc.busy {
		t.Errorf("kept %+v, busy %v", o, c.locale.rtc.busy)
	}
}

// A settable RTC is written with the time the Shell sets, unless timedated
// set it (which writes the RTC itself); a read-only one without an offset
// is left alone, and setting the time does not fail on it.
func TestRTCWrittenWhereItCanBe(t *testing.T) {
	if !alone(t) {
		return
	}
	for _, x := range []struct {
		rtc   SKURTC
		sync  string
		write bool
	}{
		{SKURTC{}, "", true},
		{SKURTC{}, "timedated", false},
		{SKURTC{ReadOnly: true}, "", false},
	} {
		c, set, written, _ := rtcCore(t, x.rtc)
		c.locale.timeSync = x.sync
		c.settings.ManualTime = true
		if err := c.setManualTime(float64(time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC).Unix())); err != nil {
			t.Fatalf("%+v %q: %v", x.rtc, x.sync, err)
		}
		deadline := time.Now().Add(time.Second)
		for x.write && len(*written) == 0 && time.Now().Before(deadline) {
			select {
			case f := <-c.ev:
				f()
			case <-time.After(5 * time.Millisecond):
			}
		}
		settle(t, c)
		if len(*set) != 1 || (len(*written) == 1) != x.write {
			t.Errorf("%+v %q: set %v, RTC written %v", x.rtc, x.sync, *set, *written)
		}
		if _, err := os.Stat(c.rtcPath()); !os.IsNotExist(err) {
			t.Errorf("%+v: an offset kept without the board's asking", x.rtc)
		}
	}
}

// NITZ sets the clock while automatic time is on, when timedated refuses
// SetTime (NTP is on): it sets it directly and writes a settable RTC
// itself; the time set by hand goes through timedated, which writes it.
func TestNITZSetsTheClockItself(t *testing.T) {
	c, set, written, _ := rtcCore(t, SKURTC{})
	var hows []string
	setSystemClock = func(at time.Time, how string) error {
		*set, hows = append(*set, at), append(hows, how)
		return nil
	}
	c.locale.timeSync = "timedated"
	now := time.Now().Unix()
	c.syncFromNITZ(&nitz{Offset: 480, Time: now + 3600, Received: now})
	for deadline := time.Now().Add(time.Second); len(*written) == 0 && time.Now().Before(deadline); {
		select {
		case f := <-c.ev:
			f()
		case <-time.After(5 * time.Millisecond):
		}
	}
	if len(hows) != 1 || hows[0] != "" || len(*written) != 1 {
		t.Fatalf("NITZ: set through %q, RTC written %v", hows, *written)
	}
	c.settings.ManualTime = true
	if err := c.setManualTime(float64(time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC).Unix())); err != nil {
		t.Fatal(err)
	}
	settle(t, c)
	if len(hows) != 2 || hows[1] != "timedated" || len(*written) != 1 {
		t.Errorf("by hand: set through %q, RTC written %v", hows, *written)
	}
}

// The selftest's time steps move the Shell's clock (clockSkew), not the
// system's: they set no clock and write no RTC, and the offset measured
// while NTP keeps the clock is the system clock's.
func TestTimeStepsLeaveTheRTC(t *testing.T) {
	c, set, written, _ := rtcCore(t, SKURTC{ReadOnly: true, Offset: true})
	ntpSynced = func() bool { return true }
	c.SetClockSkew(26 * time.Hour)
	c.rtcTick()
	settle(t, c)
	o := keptOffset(t, c)
	if d := time.Since(o.At); d < 0 || d > time.Minute || len(*set) != 0 || len(*written) != 0 {
		t.Errorf("after a timeshift: kept %+v (at the system's time, not the Shell's), set %v, written %v", o, *set, *written)
	}
}

// Where the clock is not the Shell's (the chroot kit, the developer
// machine) nothing is set, measured or started.
func TestClockNotOurs(t *testing.T) {
	c, set, _, _ := rtcCore(t, SKURTC{ReadOnly: true, Offset: true})
	canSetClock = func() bool { return false }
	ntpSynced = func() bool { return true }
	if got := detectTimeSync(); got != "" {
		t.Errorf("network time through %q on a clock that is not ours", got)
	}
	c.rtcTick()
	c.syncFromNITZ(&nitz{Time: time.Now().Unix() + 3600, Received: time.Now().Unix()})
	settle(t, c)
	if _, err := os.Stat(c.rtcPath()); !os.IsNotExist(err) || len(*set) != 0 {
		t.Errorf("set %v, offset %v", *set, err)
	}
}

func TestSKURTC(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sku.json")
	for raw, ok := range map[string]bool{
		`{"rtc": {"read_only": true, "offset": true, "alarm": "relative"}}`: true,
		`{"rtc": {"alarm": "absolute"}}`:                                    true,
		`{}`:                                                                true,
		`{"rtc": {"offset": true}}`:                                         false,
		`{"rtc": {"read_only": true, "alarm": "absolute"}}`:                 false,
		`{"rtc": {"alarm": "+N"}}`:                                          false,
	} {
		if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := LoadSKUFile(path)
		if (err == nil) != ok {
			t.Errorf("%s: %+v, %v", raw, s.RTC, err)
		}
	}
	if err := os.WriteFile(path, []byte(`{"id": "x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if s, _ := LoadSKUFile(path); s.RTC != (SKURTC{}) {
		t.Errorf("the zero value is not a settable RTC with relative alarms: %+v", s.RTC)
	}
}
