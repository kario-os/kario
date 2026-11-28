package backlight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/hal"
)

func fakeLED(t *testing.T, root, name, max, now string) string {
	t.Helper()
	d := filepath.Join(root, "class", "leds", name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d, "max_brightness"), []byte(max+"\n"), 0o644)
	os.WriteFile(filepath.Join(d, "brightness"), []byte(now+"\n"), 0o644)
	return d
}

func brightness(t *testing.T, d string) string {
	t.Helper()
	raw, _ := os.ReadFile(filepath.Join(d, "brightness"))
	return strings.TrimSpace(string(raw))
}

// A keyboard backlight is found by the kernel's ::kbd_backlight name, off
// is brightness 0, and turning it on brings back the level set while off.
func TestKeyboardLED(t *testing.T) {
	root := t.TempDir()
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	if Keyboard("") != nil || OpenLED("../x") != nil {
		t.Fatal("a keyboard backlight where there is none")
	}
	fakeLED(t, root, "white:status", "255", "0")
	d := fakeLED(t, root, "pm8xxx::kbd_backlight", "255", "0")
	k := Keyboard("")
	if k == nil || k.Name() != "pm8xxx::kbd_backlight" || Keyboard("white:kbd") != nil {
		t.Fatal("Keyboard")
	}
	if p, on := k.Level(); on || p != 100 {
		t.Errorf("an unlit LED: Level() = %d, %v", p, on)
	}
	k.Set(50)
	if b := brightness(t, d); b != "0" {
		t.Errorf("set while off lit it: %s", b)
	}
	k.Power(true)
	if b := brightness(t, d); b != "128" {
		t.Errorf("on at 50%%: %s", b)
	}
	k.Set(0)
	if b := brightness(t, d); b != "1" {
		t.Errorf("0%% is the dimmest lit level: %s", b)
	}
	k.Power(false)
	if b := brightness(t, d); b != "0" {
		t.Errorf("off: %s", b)
	}
	if p, on := k.Level(); on || p != 0 {
		t.Errorf("Level() = %d, %v", p, on)
	}
}

// The panel: the backlight class first, then the vendor kernels'
// lcd-backlight LED.
func TestPanel(t *testing.T) {
	root := t.TempDir()
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	if Panel("") != nil {
		t.Fatal("a panel backlight where there is none")
	}
	d := fakeLED(t, root, VendorPanel, "4095", "2048")
	p := Panel("")
	l, ok := p.(*LED)
	if !ok || l.Name() != VendorPanel {
		t.Fatalf("Panel() = %#v", p)
	}
	if pc, on := l.Level(); !on || pc != 50 {
		t.Errorf("lit at half: Level() = %d, %v", pc, on)
	}
	p.Set(30)
	if b := brightness(t, d); b != "1229" {
		t.Errorf("30%% of 4095 = %s", b)
	}
	bl := filepath.Join(root, "class", "backlight", "wled")
	os.MkdirAll(bl, 0o755)
	os.WriteFile(filepath.Join(bl, "max_brightness"), []byte("4095\n"), 0o644)
	if _, ok := Panel("").(*Sysfs); !ok {
		t.Error("the backlight class comes first")
	}
}
