package backlight

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/kario-os/kario/hal"
)

// LED is a backlight that is an LED class device: a keyboard's (the
// kernel names it <device>::kbd_backlight) and the panel backlight of
// vendor kernels that make it one (lcd-backlight). It has no bl_power:
// off is brightness 0, and Power(true) brings the level back.
type LED struct {
	name    string
	mu      sync.Mutex
	percent int
	// between is a level between the percent steps (a fade's), 0..1; -1
	// when the level is percent.
	between float64
	on      bool
}

// VendorPanel is the LED class name Qualcomm's vendor kernels give the
// panel backlight.
const VendorPanel = "lcd-backlight"

func ledDir(name string) string { return filepath.Join(hal.SysRoot, "class", "leds", name) }

// OpenLED is the LED called name; nil when there is none.
func OpenLED(name string) *LED {
	if name == "" || strings.Contains(name, "/") {
		return nil
	}
	if _, err := os.Stat(filepath.Join(ledDir(name), "max_brightness")); err != nil {
		return nil
	}
	l := &LED{name: name, between: -1}
	if top := l.int("max_brightness"); top > 0 {
		v := l.int("brightness")
		l.percent, l.on = (v*100+top/2)/top, v > 0
	}
	if !l.on {
		l.percent = 100
	}
	return l
}

// Keyboard is the keyboard backlight called name or, when the board names
// none, the first LED whose name ends in ::kbd_backlight; nil when there
// is none.
func Keyboard(name string) *LED {
	if name != "" {
		return OpenLED(name)
	}
	ents, _ := os.ReadDir(filepath.Join(hal.SysRoot, "class", "leds"))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), "::kbd_backlight") {
			if l := OpenLED(e.Name()); l != nil {
				return l
			}
		}
	}
	return nil
}

// Panel is the panel's backlight: the backlight class device called name
// (empty: the first), or an LED of that name, or the vendor kernels'
// lcd-backlight LED; nil when there is none.
func Panel(name string) Backlight {
	if b := Open(name); b != nil {
		return b
	}
	if name == "" {
		name = VendorPanel
	}
	if l := OpenLED(name); l != nil {
		return l
	}
	return nil
}

func (l *LED) Name() string { return l.name }

func (l *LED) int(f string) int {
	raw, _ := hal.ReadAttr(filepath.Join(ledDir(l.name), f))
	n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return n
}

func (l *LED) write() error {
	top := l.int("max_brightness")
	if top <= 0 {
		return fmt.Errorf("LED %s: no max_brightness", l.name)
	}
	v := 0
	switch {
	case l.on && l.between >= 0:
		v = max(1, int(math.Round(float64(top)*l.between)))
	case l.on:
		v = max(1, (top*l.percent+50)/100)
	}
	return hal.WriteAttr(filepath.Join(ledDir(l.name), "brightness"), []byte(strconv.Itoa(v)))
}

func (l *LED) Set(percent int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.percent, l.between = min(max(percent, 0), 100), -1
	if !l.on {
		return nil
	}
	return l.write()
}

func (l *LED) setFraction(f float64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.between = min(max(f, 0), 1)
	if !l.on {
		return nil
	}
	return l.write()
}

func (l *LED) Power(on bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.on = on
	return l.write()
}

func (l *LED) Level() (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.percent, l.on
}
