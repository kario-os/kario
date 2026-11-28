package vibrator

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
	"unsafe"

	"github.com/kario-os/kario/hal"
)

func sysRoot(t *testing.T) string {
	root := t.TempDir()
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	return root
}

// The LED-class motor: the duration, then activate.
func TestLED(t *testing.T) {
	root := sysRoot(t)
	if Open() != nil {
		t.Fatal("a motor where there is none")
	}
	d := filepath.Join(root, "class", "leds", "vibrator")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "activate"), []byte("0"), 0o644)
	m := Open()
	if m == nil || m.Name() != "led:vibrator" {
		t.Fatalf("Open() = %v", m)
	}
	if err := m.Vibrate(800 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(d, "duration")); string(raw) != "800" {
		t.Errorf("duration %q", raw)
	}
	if raw, _ := os.ReadFile(filepath.Join(d, "activate")); string(raw) != "1" {
		t.Errorf("activate %q", raw)
	}
}

// Without the LED, the first input device whose force feedback has
// FF_RUMBLE (bit 80 of capabilities/ff).
func TestRumbleFound(t *testing.T) {
	root := sysRoot(t)
	DevRoot = "/nonexistent"
	t.Cleanup(func() { DevRoot = "/dev" })
	for name, ff := range map[string]string{"event0": "0", "event3": rumbleMask(), "event5": rumbleMask()} {
		d := filepath.Join(root, "class", "input", name, "device", "capabilities")
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "ff"), []byte(ff+"\n"), 0o644)
	}
	m := Open()
	r, ok := m.(*Rumble)
	if !ok || r.node != "/nonexistent/input/event3" {
		t.Fatalf("Open() = %#v", m)
	}
	if err := r.Vibrate(time.Second); err == nil {
		t.Error("vibrated with no device node")
	}
}

func rumbleMask() string {
	if strconv.IntSize == 64 {
		return "10000 0"
	}
	return "10000 0 0"
}

func TestHasRumble(t *testing.T) {
	for _, c := range []struct {
		mask string
		bits int
		want bool
	}{
		{"10000 0", 64, true},
		{"10000 0 0", 32, true},
		{"107030000 0", 64, true},
		{"0", 64, false},
		{"0 ffffffffffffffff", 64, false},
		{"zz 0", 64, false},
		{"", 64, false},
	} {
		if got := hasRumble(c.mask, c.bits); got != c.want {
			t.Errorf("hasRumble(%q, %d) = %v", c.mask, c.bits, got)
		}
	}
}

// The structs the kernel reads have its layout: ff_effect is 48 bytes on a
// 64-bit kernel (its union 8-aligned at 16), 44 on a 32-bit one.
func TestLayout(t *testing.T) {
	size, union, event := unsafe.Sizeof(ffEffect{}), unsafe.Offsetof(ffEffect{}.U), unsafe.Sizeof(inputEvent{})
	want := [3]uintptr{48, 16, 24}
	if strconv.IntSize == 32 {
		want = [3]uintptr{44, 16, 16}
	}
	if [3]uintptr{size, union, event} != want {
		t.Errorf("ff_effect %d (union at %d), input_event %d; want %v", size, union, event, want)
	}
	if strconv.IntSize == 64 && eviocsff != 0x40304580 {
		t.Errorf("EVIOCSFF %#x", eviocsff)
	}
}

func TestSim(t *testing.T) {
	var s Sim
	s.Vibrate(300 * time.Millisecond)
	s.Vibrate(800 * time.Millisecond)
	if n, total := s.Count(); n != 2 || total != 1100*time.Millisecond {
		t.Errorf("Count() = %d, %v", n, total)
	}
}
