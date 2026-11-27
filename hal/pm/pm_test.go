package pm

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/hal"
)

func tree(t *testing.T, state string) string {
	root := t.TempDir()
	for name, v := range map[string]string{
		"power/state":              state,
		"power/wakeup_count":       "42\n",
		"power/pm_wakeup_irq":      "37\n",
		"class/rtc/rtc0/wakealarm": "",
	} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := hal.SysRoot
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = old })
	return root
}

func read(t *testing.T, root, name string) string {
	raw, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSleepHandshakeAndAlarm(t *testing.T) {
	root := tree(t, "freeze mem disk\n")
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode() != "mem" {
		t.Errorf("mode %q, want mem (suspend-to-RAM when the board has it)", s.Mode())
	}
	count, err := s.Count()
	if err != nil || count != "42" {
		t.Fatalf("count %q, %v", count, err)
	}
	var alarms []string
	write = func(path, v string) error {
		if filepath.Base(path) == "wakealarm" {
			alarms = append(alarms, v)
		}
		return writeFile(path, v)
	}
	t.Cleanup(func() { write = writeFile })
	wake := time.Now().Add(90 * time.Second)
	if err := s.Sleep(count, wake); err != nil {
		t.Fatal(err)
	}
	// Cleared, set from the RTC's own now (not wake.Unix(), the system
	// clock's), cleared again after waking.
	if len(alarms) != 3 || alarms[0] != "0" || (alarms[1] != "+90" && alarms[1] != "+89") || alarms[2] != "0" {
		t.Errorf("wake alarm written %q, want 0, +90, 0", alarms)
	}
	if got := read(t, root, "power/wakeup_count"); got != "42" {
		t.Errorf("wakeup_count written back as %q", got)
	}
	if got := read(t, root, "power/state"); got != "mem" {
		t.Errorf("state %q", got)
	}
	// Cleared after waking (a file keeps only the last write).
	if got := read(t, root, "class/rtc/rtc0/wakealarm"); got != "0" {
		t.Errorf("wake alarm left at %q", got)
	}
	if s.WokenBy() != "irq 37" {
		t.Errorf("woken by %q", s.WokenBy())
	}
}

// The alarm is in seconds from now, rounded up, at least one: one in the
// past still wakes the phone at once rather than never.
func TestAlarmIn(t *testing.T) {
	now := time.Unix(1_795_362_904, 0)
	for _, c := range []struct {
		wake time.Time
		want string
	}{
		{now.Add(30 * time.Second), "+30"},
		{now.Add(30*time.Second + time.Millisecond), "+31"},
		{now.Add(10 * time.Hour), "+36000"},
		{now, "+1"},
		{now.Add(-time.Minute), "+1"},
	} {
		if got := alarmIn(c.wake, now); got != c.want {
			t.Errorf("alarmIn(now%+v) = %q, want %q", c.wake.Sub(now), got, c.want)
		}
	}
}

// A board whose RTC runs on the system's clock may take the wake time
// itself (sku.json rtc.alarm "absolute"), rounded up to the second.
func TestAbsoluteAlarm(t *testing.T) {
	tree(t, "mem\n")
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	s.AbsoluteAlarm = true
	var alarms []string
	write = func(path, v string) error {
		if filepath.Base(path) == "wakealarm" {
			alarms = append(alarms, v)
		}
		return writeFile(path, v)
	}
	t.Cleanup(func() { write = writeFile })
	wake := time.Unix(1_795_362_904, 300_000_000)
	if err := s.Sleep("42", wake); err != nil {
		t.Fatal(err)
	}
	if len(alarms) != 3 || alarms[1] != "1795362905" {
		t.Errorf("wake alarm written %q, want 0, 1795362905, 0", alarms)
	}
	if got := alarmAt(time.Unix(1_795_362_904, 0)); got != "1795362904" {
		t.Errorf("a whole second: %s", got)
	}
}

// The bring-up image's phone-bringup-awake holds the wake lock
// phone-bringup (39-oneplus-6t.md §16, report 006); a kernel without user
// space wake locks has no file.
func TestWakeLocks(t *testing.T) {
	root := tree(t, "mem\n")
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if got := s.WakeLocks(); got != nil {
		t.Errorf("without the file: %q", got)
	}
	p := filepath.Join(root, "power", "wake_lock")
	for v, want := range map[string]string{"\n": "", "phone-bringup\n": "phone-bringup", "phone-bringup other\n": "phone-bringup,other"} {
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(s.WakeLocks(), ","); got != want {
			t.Errorf("%q: %q, want %q", v, got, want)
		}
	}
}

// The modem's call or message wakes the 6T through its GLINK channel's
// wake-up source, with no wake-up interrupt: pm_wakeup_irq has no data and
// the Shell logged "no wake reason". Now the sources whose wakeup_count
// rose while it slept are named, after the interrupt where there is one;
// a suspend called off names them too.
func TestWokenByNamesTheWakeUpSources(t *testing.T) {
	root := tree(t, "mem\n")
	if err := os.Remove(filepath.Join(root, "power", "pm_wakeup_irq")); err != nil {
		t.Fatal(err)
	}
	src := func(n, name, count string) {
		d := filepath.Join(root, "class", "wakeup", "wakeup"+n)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for f, v := range map[string]string{"name": name + "\n", "wakeup_count": count + "\n"} {
			if err := os.WriteFile(filepath.Join(d, f), []byte(v), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	const modem = "4080000.remoteproc:glink-edge.IPCRTR.-1.-1"
	src("3", "alarmtimer.0.auto", "2")
	src("9", modem, "5")
	src("12", "c440000.spmi:pmic@2:charger@1000", "1")
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	var busy error
	n := 5
	write = func(path, v string) error {
		if path == filepath.Join(root, "power", "state") {
			n++
			src("9", modem, strconv.Itoa(n))
			if busy != nil {
				return busy
			}
		}
		return writeFile(path, v)
	}
	t.Cleanup(func() { write = writeFile })
	if err := s.Sleep("42", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, want := s.WokenBy(), "wake-up sources "+modem; got != want {
		t.Errorf("woken by %q, want %q", got, want)
	}
	if err := os.WriteFile(filepath.Join(root, "power", "pm_wakeup_irq"), []byte("90\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Sleep("42", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, want := s.WokenBy(), "wake-up sources "+modem; !strings.HasPrefix(got, "irq 90; ") || !strings.HasSuffix(got, want) {
		t.Errorf("woken by %q, want irq 90; %s", got, want)
	}
	busy = syscall.EBUSY
	err = s.Sleep("42", time.Now().Add(time.Minute))
	if !errors.Is(err, ErrAborted) || !strings.Contains(err.Error(), modem) {
		t.Errorf("called off: %v", err)
	}
}

func TestKeysWake(t *testing.T) {
	root := tree(t, "mem\n")
	if KeysWake() {
		t.Error("no input devices, yet keys wake")
	}
	for i, v := range []string{"disabled\n", "enabled\n"} {
		p := filepath.Join(root, "class", "input", "input"+string(rune('0'+i)), "device", "power", "wakeup")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := KeysWake(); got != (v == "enabled\n") {
			t.Errorf("with %q: %v", v, got)
		}
	}
}

func TestChargersWake(t *testing.T) {
	root := tree(t, "mem\n")
	for name, files := range map[string]map[string]string{
		"bq27411-0":     {"type": "Battery", "device/power/wakeup": "enabled\n"},
		"pmi8998":       {"type": "USB", "device/power/wakeup": "disabled\n"},
		"ac":            {"type": "Mains", "device/power/wakeup": "enabled\n"},
		"test_usb":      {"type": "USB"},
		"wireless-coil": {"type": "Wireless", "device/power/wakeup": "enabled\n"},
	} {
		for f, v := range files {
			p := filepath.Join(root, "class", "power_supply", name, f)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := strings.Join(ChargersWake(), " "); got != "ac pmi8998 wireless-coil" {
		t.Errorf("chargers that wake: %q, want ac pmi8998 wireless-coil", got)
	}
	if got := read(t, root, "class/power_supply/pmi8998/device/power/wakeup"); got != "enabled" {
		t.Errorf("the charger's wakeup left %q", got)
	}
}

func TestIdleOnlyAndNoRTC(t *testing.T) {
	root := tree(t, "freeze\n")
	if s, err := Open(); err != nil || s.Mode() != "freeze" {
		t.Fatalf("%v, %v", s, err)
	}
	if err := os.RemoveAll(filepath.Join(root, "class", "rtc")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(); err == nil {
		t.Error("sleeps without a wake alarm: alarms would not ring")
	}
	tree(t, "disk\n")
	if _, err := Open(); err == nil {
		t.Error("sleeps with only hibernation")
	}
}
