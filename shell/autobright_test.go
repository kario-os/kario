package shell

import (
	"testing"

	"github.com/kario-os/kario/ui"
)

func TestAutoLevel(t *testing.T) {
	for _, c := range []struct {
		set  int
		lux  float64
		want int
	}{
		{80, 300, 80}, {80, 0, 24}, {80, 10, 40}, {80, 100, 64}, {80, 50000, 100},
		{10, 0, ui.MinBrightness}, {50, 1000, 65}, {50, 10000, 100},
	} {
		if got := autoLevel(c.set, c.lux); got != c.want {
			t.Errorf("autoLevel(%d, %v) = %d, want %d", c.set, c.lux, got, c.want)
		}
	}
	for lux, prev := 0.0, 0; lux <= 20000; lux += 37 {
		l := autoLevel(80, lux)
		if l < prev {
			t.Fatalf("darker at %v lux (%d) than below it (%d)", lux, l, prev)
		}
		prev = l
	}
}

// Off: the brightness set, whatever the light. On: the light sets the
// level, smoothed, not moved by small changes; daylight turns the keyboard
// light off either way.
func TestAutoBrightness(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		settle := func(lux float64) {
			c.SetLux(lux)
			for range 40 {
				c.checkScreen()
			}
		}
		if !c.screen.alsSim {
			t.Skip("a real light sensor")
		}
		settle(5)
		if b := c.Backlight(); b != defaultBrightness {
			t.Errorf("automatic brightness off: backlight %d%% in the dark", b)
		}
		_ = c.settingsSet(ui.SettingAutoBrightness, 1)
		if b, want := c.Backlight(), autoLevel(defaultBrightness, c.Lux()); b != want {
			t.Errorf("switched on in the dark: %d%%, want %d%% at once", b, want)
		}
		settle(300)
		if b := c.Backlight(); b != defaultBrightness {
			t.Errorf("an ordinary room: %d%%, want the brightness set", b)
		}
		settle(330)
		if b := c.Backlight(); b != defaultBrightness {
			t.Errorf("a little more light moved it to %d%%", b)
		}
		settle(20000)
		if b, k := c.Backlight(), c.KeyLight(); b != 100 || k != -1 {
			t.Errorf("daylight: backlight %d%%, keyboard %d%%", b, k)
		}
		c.SetLux(0)
		c.checkScreen()
		if b := c.Backlight(); b != 100 {
			t.Errorf("one dark reading after daylight dimmed it to %d%%", b)
		}
		_ = c.settingsSet(ui.SettingBrightness, 40)
		settle(300)
		if b, k := c.Backlight(), c.KeyLight(); b != 40 || k != 40 {
			t.Errorf("back indoors at 40%%: backlight %d%%, keyboard %d%%", b, k)
		}
		_ = c.settingsSet(ui.SettingAutoBrightness, 0)
		settle(5)
		if b := c.Backlight(); b != 40 {
			t.Errorf("switched off: %d%%", b)
		}
		return nil
	})
}
