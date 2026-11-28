package rtc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/hal"
	"github.com/kario-os/kario/statefile"
)

// sysfs is a fake /sys with rtc0's since_epoch and hctosys.
func sysfs(t *testing.T, since, hctosys string) string {
	root := t.TempDir()
	dir := filepath.Join(root, "class", "rtc", Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, v := range map[string]string{"since_epoch": since, "hctosys": hctosys} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := hal.SysRoot
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = old })
	return dir
}

func TestRead(t *testing.T) {
	sysfs(t, "187506\n", "1\n")
	if s, err := Read(); err != nil || s != 187506 {
		t.Fatalf("Read = %d, %v", s, err)
	}
	if !Hctosys() {
		t.Error("hctosys 1 not read")
	}
	sysfs(t, "x\n", "0\n")
	if _, err := Read(); err == nil {
		t.Error("a since_epoch that is no number read")
	}
	if Hctosys() {
		t.Error("hctosys 0 read as set")
	}
}

// The 6T's RTC counts from 1970 while the system clock is 2026: the offset
// is found at the RTC's tick, to the polling interval, not to a second.
func TestMeasureAtTheTick(t *testing.T) {
	sys := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC).Add(137 * time.Millisecond)
	want := sys.Sub(time.Unix(187_506, 0).Add(420 * time.Millisecond))
	read := func() (int64, error) { return int64((sys.Add(-want).UnixNano()) / 1e9), nil }
	o, err := measure("ntp", read, func() time.Time { return sys }, func() { sys = sys.Add(poll) })
	if err != nil {
		t.Fatal(err)
	}
	if d := o.Duration() - want; d < 0 || d > poll {
		t.Errorf("offset %v, want %v (within %v)", o.Duration(), want, poll)
	}
	if o.By != "ntp" || !o.At.Equal(sys) {
		t.Errorf("measured %+v, at %v", o, sys)
	}
	stuck := func() (int64, error) { return 5, nil }
	if _, err := measure("ntp", stuck, func() time.Time { return sys }, func() { sys = sys.Add(poll) }); err == nil || !strings.Contains(err.Error(), "does not tick") {
		t.Errorf("an RTC that does not tick: %v", err)
	}
}

// rtc-pm8xxx as a module: the RTC appears when udev has loaded it, and the
// kernel sets the clock from it a moment later; the clock is set after.
func TestWait(t *testing.T) {
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	pause := func() { now = now.Add(50 * time.Millisecond) }
	appears, set := now.Add(3*time.Second), now.Add(3*time.Second+100*time.Millisecond)
	read := func() (int64, error) {
		if now.Before(appears) {
			return 0, os.ErrNotExist
		}
		return 100, nil
	}
	if err := wait(10*time.Second, read, func() bool { return !now.Before(set) }, clock, pause); err != nil || now.Before(set) {
		t.Fatalf("waited until %v: %v", now, err)
	}
	now, appears = time.Unix(0, 0), time.Unix(1, 0)
	if err := wait(10*time.Second, read, func() bool { return false }, clock, pause); err != nil || now.Sub(appears) < hctosysWait {
		t.Fatalf("a kernel without hctosys: waited until %v: %v", now, err)
	}
	now, appears = time.Unix(0, 0), time.Unix(60, 0)
	if err := wait(10*time.Second, read, func() bool { return true }, clock, pause); err == nil || !os.IsNotExist(errors.Unwrap(err)) {
		t.Fatalf("no RTC: %v", err)
	}
}

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slots", OffsetFile)
	if _, err := Load(path); !os.IsNotExist(err) {
		t.Fatalf("no file: %v", err)
	}
	o := Offset{MS: 1_795_175_400_250, At: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC), By: "user"}
	if err := Save(path, o); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got != o {
		t.Fatalf("Load = %+v, %v; want %+v", got, err, o)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("the temporary file is left")
	}
	if err := os.Remove(path + statefile.Bak); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"{", `{"offset_ms": 5}`} {
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("%q without a copy loaded", bad)
		}
	}
}

// An offset file cut short (the power going while phone-clock or the Shell
// kept it) is read from its copy, and the clock is set from it; with
// neither whole the clock is left alone, and the error says the offset is
// damaged rather than that none was ever kept.
func TestOffsetFromItsCopy(t *testing.T) {
	sysfs(t, "400000\n", "1\n")
	var set []time.Time
	old := SetSystem
	SetSystem = func(t time.Time) error { set = append(set, t); return nil }
	t.Cleanup(func() { SetSystem = old })
	path := filepath.Join(t.TempDir(), OffsetFile)
	at := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	o := Offset{MS: at.Sub(time.Unix(300_000, 0)).Milliseconds(), At: at, By: "ntp"}
	if err := Save(path, o); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 20); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(path); err != nil || got != o {
		t.Fatalf("cut short: %+v, %v; want %+v from the copy", got, err, o)
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, why, err := Restore(path); err != nil || why != "" || len(set) != 1 {
		t.Fatalf("restored from the copy: %q %v, set %v", why, err, set)
	}
	for _, p := range []string{path, path + statefile.Bak} {
		if err := os.WriteFile(p, []byte(`{"offset_ms":`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, why, err := Restore(path); !errors.Is(err, statefile.ErrDamaged) || why != "" || len(set) != 1 {
		t.Fatalf("neither whole: %q %v, set %v", why, err, set)
	}
}

func TestMoved(t *testing.T) {
	kept := Offset{MS: 10_000, At: time.Unix(1, 0)}
	for _, c := range []struct {
		ms   int64
		kept Offset
		want bool
	}{{10_400, kept, false}, {9_600, kept, false}, {10_600, kept, true}, {9_400, kept, true}, {10_000, Offset{}, true}} {
		if got := (Offset{MS: c.ms}).Moved(c.kept, 500*time.Millisecond); got != c.want {
			t.Errorf("%d ms against %+v: moved %v", c.ms, c.kept, got)
		}
	}
}

func TestRestoreTime(t *testing.T) {
	at := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	o := Offset{MS: at.Sub(time.Unix(100_000, 0)).Milliseconds(), At: at}
	booted := time.Unix(100_000+3*86_400+12, 0)
	got, why := restoreTime(o, 100_000+3*86_400, booted)
	if want := at.Add(3*24*time.Hour + 500*time.Millisecond); why != "" || !got.Equal(want) {
		t.Errorf("three days later: %v (%s), want %v", got, why, want)
	}
	if got, why := restoreTime(o, 50, booted); !got.IsZero() || !strings.Contains(why, "started over") {
		t.Errorf("an RTC that started over: %v %q", got, why)
	}
	if got, why := restoreTime(o, 100_000+60, at.Add(time.Minute)); !got.IsZero() || why != "the clock is right already" {
		t.Errorf("the clock right already: %v %q", got, why)
	}
}

func TestRestore(t *testing.T) {
	sysfs(t, "400000\n", "1\n")
	var set []time.Time
	old := SetSystem
	SetSystem = func(t time.Time) error { set = append(set, t); return nil }
	t.Cleanup(func() { SetSystem = old })
	path := filepath.Join(t.TempDir(), OffsetFile)
	if got, why, err := Restore(path); err != nil || !got.IsZero() || !strings.Contains(why, "no offset kept yet") || len(set) != 0 {
		t.Fatalf("without an offset: %v %q %v %v", got, why, err, set)
	}
	at := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	if err := Save(path, Offset{MS: at.Sub(time.Unix(300_000, 0)).Milliseconds(), At: at, By: "ntp"}); err != nil {
		t.Fatal(err)
	}
	got, why, err := Restore(path)
	want := at.Add(100_000*time.Second + 500*time.Millisecond)
	if err != nil || why != "" || !got.Equal(want) || len(set) != 1 || !set[0].Equal(want) {
		t.Fatalf("Restore = %v %q %v, set %v; want %v", got, why, err, set, want)
	}
}

// Only the system that owns the clock sets it: root, not in a chroot on
// another system, not told by the chroot kit that the clock is the host's.
func TestOurs(t *testing.T) {
	oldEuid, oldChroot := euid, chrooted
	t.Cleanup(func() { euid, chrooted = oldEuid, oldChroot })
	for _, c := range []struct {
		uid    int
		env    string
		chroot bool
		want   bool
	}{
		{0, "", false, true},
		{0, "host", false, false},
		{0, "", true, false},
		{1000, "", false, false},
	} {
		euid, chrooted = func() int { return c.uid }, func() bool { return c.chroot }
		t.Setenv(OwnerEnv, c.env)
		if got := Ours(); got != c.want {
			t.Errorf("uid %d, %s=%q, chroot %v: ours %v", c.uid, OwnerEnv, c.env, c.chroot, got)
		}
	}
}
