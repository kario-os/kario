package shell

import (
	"fmt"
	"testing"
	"time"

	"github.com/kario-os/kario/hal/backlight"
	"github.com/kario-os/kario/ui"
)

// The backlight is at the brightness set, dims before the screen goes off
// (never brighter than set), and comes back to it on a key.
func TestBrightness(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		if b := c.Backlight(); b != defaultBrightness {
			t.Errorf("default backlight %d%%", b)
		}
		for v, want := range map[float64]int{50: 50, 0: ui.MinBrightness, 300: 100} {
			_ = c.settingsSet(ui.SettingBrightness, v)
			if b := c.Backlight(); b != want {
				t.Errorf("brightness %v: backlight %d%%, want %d%%", v, b, want)
			}
		}
		for _, set := range []int{50, ui.MinBrightness} {
			_ = c.settingsSet(ui.SettingBrightness, float64(set))
			c.lastKey, c.screen.active = c.now().Add(-c.screenTimeout()+screenDimFor/2), time.Time{}
			c.checkScreen()
			if b := c.Backlight(); !c.screen.dimmed || b != min(screenDimLevel, set) {
				t.Errorf("set %d%%: dimmed %v at %d%%", set, c.screen.dimmed, b)
			}
			if k := c.KeyLight(); k != -1 {
				t.Errorf("set %d%%: the keyboard lit at %d%% on a dimmed screen", set, k)
			}
			c.Wake("key")
			if b := c.Backlight(); b != set {
				t.Errorf("set %d%%: %d%% after a key", set, b)
			}
			if k := c.KeyLight(); k != set {
				t.Errorf("set %d%%: the keyboard at %d%% after a key", set, k)
			}
		}
		return nil
	})
}

// The backlight fades to a new level, to dark before the screen goes off
// and up again on a key; with Settings › Animations off it jumps, as it
// did before the fades.
func TestBacklightFades(t *testing.T) {
	c := loopCore(t, t.TempDir())
	sim := backlight.NewSim()
	reached := func(want int, on bool) bool {
		for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
			if p, lit := sim.Level(); p == want && lit == on {
				return true
			}
		}
		return false
	}
	_ = c.onLoop(func() error {
		c.screen.bl = backlight.NewFader(sim)
		c.settings.Motion = motionOff
		_ = c.settingsSet(ui.SettingBrightness, 50)
		if p, _ := sim.Level(); p != 50 {
			t.Errorf("animations off: %d%% after setting 50%%", p)
		}
		c.screenOff("key")
		if _, on := sim.Level(); on {
			t.Error("animations off: still lit after going off")
		}
		c.Wake("key")
		c.settings.Motion = ""
		_ = c.settingsSet(ui.SettingBrightness, 30)
		if b := c.Backlight(); b != 30 {
			t.Errorf("Backlight() = %d while fading to 30%%", b)
		}
		return nil
	})
	if !reached(30, true) {
		t.Fatalf("never faded to 30%%: %v", fmt.Sprint(sim.Level()))
	}
	_ = c.onLoop(func() error {
		c.screenOff("key")
		if _, on := sim.Level(); !on {
			t.Error("switched off before the light faded")
		}
		return nil
	})
	if !reached(0, false) {
		t.Fatalf("never went dark: %v", fmt.Sprint(sim.Level()))
	}
	_ = c.onLoop(func() error {
		c.Wake("key")
		if p, on := sim.Level(); !on || p >= 30 {
			t.Errorf("waking: %d%%, %v at once, want lit below 30%%", p, on)
		}
		return nil
	})
	if !reached(30, true) {
		t.Fatalf("never lit up again: %v", fmt.Sprint(sim.Level()))
	}
}

// The keyboard backlight goes off with the screen and comes back with it.
func TestKeyboardBacklight(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		if k := c.KeyLight(); k != defaultBrightness {
			t.Errorf("the keyboard at %d%% with the screen on", k)
		}
		c.screenOff("key")
		if k := c.KeyLight(); k != -1 {
			t.Errorf("the keyboard at %d%% with the screen off", k)
		}
		_ = c.settingsSet(ui.SettingBrightness, float64(40))
		if k := c.KeyLight(); k != -1 {
			t.Errorf("a brightness change lit the keyboard of an off screen: %d%%", k)
		}
		c.Wake("key")
		if k := c.KeyLight(); k != 40 {
			t.Errorf("the keyboard at %d%% after waking, want 40%%", k)
		}
		return nil
	})
}
