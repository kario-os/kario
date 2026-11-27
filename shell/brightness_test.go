package shell

import (
	"bufio"
	"fmt"
	"io"
	"strings"
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

// PHONE_SCREEN_OFF stands in for the default timeout (a software GPU on
// an emulator takes tens of seconds a frame); the user's choice in
// Settings › Display still comes first.
func TestScreenOffFromTheEnvironment(t *testing.T) {
	t.Setenv(ScreenOffEnv, "600")
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		if d := c.screenTimeout(); d != 600*time.Second {
			t.Errorf("timeout %v with %s=600", d, ScreenOffEnv)
		}
		c.lastKey, c.screen.active = c.now().Add(-5*time.Minute), time.Time{}
		c.checkScreen()
		if c.screen.off || c.screen.dimmed {
			t.Errorf("five minutes without a key: off %v, dimmed %v", c.screen.off, c.screen.dimmed)
		}
		_ = c.settingsSet(ui.SettingScreenOff, 60)
		if d := c.screenTimeout(); d != time.Minute {
			t.Errorf("timeout %v with Settings at a minute", d)
		}
		return nil
	})
	t.Setenv(ScreenOffEnv, "soon")
	c = loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		if d := c.screenTimeout(); d != defaultScreenOff*time.Second {
			t.Errorf("timeout %v with %s=soon", d, ScreenOffEnv)
		}
		return nil
	})
}

// The backlight fades to a new level, to dark before the screen goes off
// and up again on a key; with Settings › Animations off it jumps, as it
// did before the fades.
func TestBacklightFades(t *testing.T) {
	if !alone(t) {
		return
	}
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

// A Shell that ended with the screen off left the backlight off (bl_power
// 4; the selftest starts the phone over after a failed case, as the 6T's
// pixel run did after the charging case). The next starts with the screen
// on: it lights the backlight, and the screen going off has the renderer
// turn the panel off. Before, the backlight stayed dark under a screen the
// Shell had on, and going off never asked for the panel (off true, panel
// on true).
func TestScreenAfterAShellLeftTheBacklightOff(t *testing.T) {
	if !alone(t) {
		return
	}
	c := loopCore(t, t.TempDir())
	sim := backlight.NewSim()
	_ = sim.Power(false)
	r, w := io.Pipe()
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	t.Cleanup(func() { _ = w.Close() })
	_ = c.onLoop(func() error {
		c.screen.bl = backlight.NewFader(sim)
		c.gui = &GUI{c: c, in: w, info: RendererInfo{Features: []string{"power"}}}
		c.applyBrightness()
		return nil
	})
	lit := false
	for end := time.Now().Add(2 * time.Second); !lit && time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		p, on := sim.Level()
		lit = on && p == defaultBrightness
	}
	if !lit {
		t.Fatalf("the backlight a Shell before left off: %v with the screen on", fmt.Sprint(sim.Level()))
	}
	_ = c.onLoop(func() error { c.screenOff("timeout"); return nil })
	select {
	case l := <-lines:
		if l != `{"on":false,"t":"power"}` {
			t.Errorf("the renderer got %s", l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the renderer was never asked to turn the panel off")
	}
	_ = c.onLoop(func() error {
		c.gui = nil
		return nil
	})
}

// The step waiting on the panel says what it waits for.
func TestPanelWhy(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		c.gui = &GUI{c: c, in: nopWriter{}, info: RendererInfo{Features: []string{"power"}}}
		s := &c.screen
		for _, x := range []struct {
			off, want, asking, panel bool
			err, why                 string
		}{
			{off: true, want: true, panel: true, why: "the backlight has not faded out"},
			{off: true, asking: true, panel: true, why: "asked to turn it off"},
			{off: true, panel: true, err: "DPMS off: EIO", why: "could not: DPMS off: EIO"},
			{off: true, panel: true, why: "waiting for the panel to settle"},
			{off: true, why: ""},
		} {
			s.off, s.want, s.asking, s.panel, s.panelErr = x.off, x.want, x.asking, x.panel, x.err
			s.asked, s.askedAt = false, time.Now()
			if got := c.panelWhy(); x.why == "" && got != "" || !strings.Contains(got, x.why) {
				t.Errorf("%+v: %q, want %q", x, got, x.why)
			}
		}
		s.off, s.want, s.asking, s.panel, s.panelErr = false, true, false, true, ""
		c.gui = nil
		return nil
	})
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriter) Close() error                { return nil }

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
