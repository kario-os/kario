package pm

import (
	"os"
	"path/filepath"
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
	wake := time.Unix(1_800_000_000, 0)
	if err := s.Sleep(count, wake); err != nil {
		t.Fatal(err)
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
