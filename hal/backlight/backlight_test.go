package backlight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/hal"
)

func TestSysfsBacklight(t *testing.T) {
	root := t.TempDir()
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	if Open("") != nil {
		t.Fatal("a backlight where there is none")
	}
	d := filepath.Join(root, "class", "backlight", "wled")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "max_brightness"), []byte("4095\n"), 0o644)
	os.WriteFile(filepath.Join(d, "brightness"), []byte("4095\n"), 0o644)
	os.WriteFile(filepath.Join(d, "bl_power"), []byte("0\n"), 0o644)
	b := Open("")
	if b == nil || Open("panel") != nil {
		t.Fatal("Open")
	}
	if err := b.Set(30); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(d, "brightness"))
	if strings.TrimSpace(string(raw)) != "1229" {
		t.Errorf("30%% of 4095 = %s", raw)
	}
	b.Set(0)
	if raw, _ := os.ReadFile(filepath.Join(d, "brightness")); string(raw) != "1" {
		t.Errorf("0%% is the dimmest lit level, got %s", raw)
	}
	b.Power(false)
	if p, on := b.Level(); on || p != 0 {
		t.Errorf("Level() = %d, %v", p, on)
	}
}
