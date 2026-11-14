package led

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/hal"
)

// fakeLED is an LED class directory; writing "timer" to trigger makes the
// delay files appear, as the kernel does.
func fakeLED(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	d := filepath.Join(root, "class", "leds", name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, v := range files {
		os.WriteFile(filepath.Join(d, f), []byte(v), 0o644)
	}
	return d
}

func read(t *testing.T, d, f string) string {
	b, err := os.ReadFile(filepath.Join(d, f))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestSlowBlinkUsesTheTimerTrigger(t *testing.T) {
	d := fakeLED(t, "rgb:status", map[string]string{"max_brightness": "511", "brightness": "0", "trigger": "[none] timer", "multi_intensity": "0 0 0"})
	l := Open(Config{Name: "rgb:status", Color: []int{0, 255, 0}})
	if err := l.Set(SlowBlink); err != nil {
		t.Fatal(err)
	}
	if got := read(t, d, "trigger"); got != "timer" {
		t.Errorf("trigger = %q", got)
	}
	if read(t, d, "delay_on") != "200" || read(t, d, "delay_off") != "4800" {
		t.Errorf("delays = %s/%s", read(t, d, "delay_on"), read(t, d, "delay_off"))
	}
	if read(t, d, "brightness") != "511" || read(t, d, "multi_intensity") != "0 255 0" {
		t.Errorf("brightness %s, colour %s", read(t, d, "brightness"), read(t, d, "multi_intensity"))
	}
	os.WriteFile(filepath.Join(d, "trigger"), []byte("none [timer]"), 0o644)
	if got := l.Pattern(); got != SlowBlink {
		t.Errorf("Pattern() = %v", got)
	}
	if err := l.Set(Pattern{}); err != nil {
		t.Fatal(err)
	}
	if read(t, d, "trigger") != "none" || read(t, d, "brightness") != "0" {
		t.Errorf("off: trigger %s, brightness %s", read(t, d, "trigger"), read(t, d, "brightness"))
	}
}

func TestAMissingLEDIsAnError(t *testing.T) {
	fakeLED(t, "other", nil)
	if err := Open(Config{Name: "white:status"}).Set(SlowBlink); err == nil {
		t.Fatal("no error for a missing LED")
	}
}

func TestPatternString(t *testing.T) {
	for p, want := range map[Pattern]string{{}: "off", {On: time.Second}: "on", SlowBlink: "blink 200/4800 ms"} {
		if got := p.String(); got != want {
			t.Errorf("%#v: %q, want %q", p, got, want)
		}
	}
}
