package shell

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/kario-os/kario/hal/backlight"
	"github.com/kario-os/kario/hal/led"
)

// Screen power, for battery life. The panel is the phone's biggest draw,
// so it goes off after the user's timeout (Settings › Display), dimmed for
// its last seconds; while it is off the renderer gets no frames (it powers
// the panel down: DRM) and the shell's housekeeping slows from twice a
// second to every offTick. A key wakes it, and that first press only
// wakes (a key pressed in a pocket does nothing else); so do what needs
// the user now: an incoming call, an alarm, a charger plugged in, a
// computer asking to be trusted. New messages and missed calls do not
// light the screen: the notification light slow-blinks while the screen
// is off and something is unread (hal/led), until the user has seen it in
// the notification centre or in its app; a board without a light lights
// the screen for them instead, and its timeout applies. A video, the
// camera or a video call on the screen keeps it on.

var screenOffChoices = []int{15, 30, 60, 120, 300}

const (
	defaultScreenOff = 30
	screenDimFor     = 5 * time.Second
	screenDimLevel   = 20 // percent of the backlight
	onTick           = 500 * time.Millisecond
	offTick          = 5 * time.Second
)

type screenState struct {
	off, dimmed bool
	// auto: the timeout applies (scripts turn it on for the case that
	// tests it: their keys would otherwise be spent waking the screen).
	auto bool
	// active is the last time something kept the screen on; offAt when it
	// went off.
	active time.Time
	offAt  time.Time
	wakes  int
	why    string
	// panel: the renderer's last word on the panel's power.
	panel    bool
	charging bool
	tick     atomic.Int64 // the housekeeping interval, in nanoseconds
	bl       backlight.Backlight
	light    led.Light
	noLight  led.Light // the light a selftest took away
	uled     *led.User
	// blinks counts the kernel lighting a userspace LED a blink period
	// after it last did (the timer trigger at work, not the setup).
	blinks atomic.Int64
	ledErr string
}

// watchBlinks reads a userspace LED's brightness changes: a light coming
// on SlowBlink's period (give or take a fifth) after it last came on is a
// blink.
func (s *screenState) watchBlinks(changes <-chan led.Change) {
	period := led.SlowBlink.On + led.SlowBlink.Off
	var lastOn time.Time
	lit := false
	for ch := range changes {
		on := ch.Brightness > 0
		if on && !lit {
			if d := ch.At.Sub(lastOn); !lastOn.IsZero() && d > period*4/5 && d < period*6/5 {
				s.blinks.Add(1)
			}
			lastOn = ch.At
		}
		lit = on
	}
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (c *Core) screenTimeout() time.Duration {
	s := c.settings.ScreenOff
	if s <= 0 {
		s = defaultScreenOff
	}
	return time.Duration(s) * time.Second
}

// setupScreen finds the backlight and the notification light the SKU
// names (sku.json); on QEMU the light is a userspace LED this process makes.
func (c *Core) setupScreen(sku SKU) {
	c.screen.tick.Store(int64(onTick))
	c.screen.active = c.now()
	c.screen.auto = true
	c.screen.panel = true
	c.screen.charging = c.power().Charging
	if b := backlight.Open(sku.Backlight); b != nil {
		c.screen.bl = b
	}
	switch {
	case sku.LEDs.Notify.Name != "":
		l := led.Open(sku.LEDs.Notify)
		if _, err := os.Stat(filepath.Join("/sys/class/leds", sku.LEDs.Notify.Name)); err != nil {
			u, err := led.NewUser(sku.LEDs.Notify.Name, 255)
			switch {
			case errors.Is(err, led.ErrNoUleds):
				c.log.Printf("notification light %s: not in this kernel", sku.LEDs.Notify.Name)
			case err != nil:
				c.log.Printf("notification light %s: %v", sku.LEDs.Notify.Name, err)
			default:
				c.screen.uled = u
				go c.screen.watchBlinks(u.Changes)
				c.log.Printf("notification light %s: a userspace LED (no LED on this machine)", sku.LEDs.Notify.Name)
			}
		}
		c.screen.light = l
	case sku.ID == "":
		c.screen.light = led.NewSim("sim")
	}
	_ = c.setLight(led.Pattern{})
	c.setupSleep(sku)
	c.powerKey = sku.PowerKey
}

// keepScreen: what holds the screen on.
func (c *Core) keepScreen() bool {
	return len(c.want) > 0 || len(c.wantGUI) > 0 || c.ringing()
}

// checkScreen runs with the housekeeping.
func (c *Core) checkScreen() {
	s := &c.screen
	if p := c.power(); p.Charging != s.charging {
		s.charging = p.Charging
		if p.Charging {
			c.Wake("charger")
			if p.Fast {
				c.showToast(tr.T("Fast charging"))
			} else {
				c.showToast(tr.T("Charging"))
			}
		}
	}
	if s.off || !s.auto {
		return
	}
	now := c.now()
	if c.keepScreen() || s.active.After(now) {
		s.active = now
	}
	if c.lastKey.After(now) {
		c.lastKey = now
	}
	idle := now.Sub(later(s.active, c.lastKey))
	t := c.screenTimeout()
	switch {
	case idle >= t:
		c.screenOff("timeout")
	case idle >= t-screenDimFor && !s.dimmed:
		s.dimmed = true
		if s.bl != nil {
			_ = s.bl.Set(screenDimLevel)
		}
	}
}

func (c *Core) screenOff(why string) {
	s := &c.screen
	if s.off {
		return
	}
	s.off, s.dimmed, s.why, s.offAt = true, false, why, c.now()
	c.updateFocus()
	if s.bl != nil {
		_ = s.bl.Power(false)
	}
	if c.gui != nil {
		c.gui.Power(false)
	}
	s.tick.Store(int64(offTick))
	c.log.Printf("screen off (%s)", why)
	c.updateLight()
}

// Wake lights the screen; it reports whether it was off.
func (c *Core) Wake(why string) bool {
	s := &c.screen
	s.active = c.now()
	if s.dimmed {
		s.dimmed = false
		if s.bl != nil {
			_ = s.bl.Set(100)
		}
	}
	if !s.off {
		return false
	}
	s.off, s.why = false, why
	s.wakes++
	c.updateFocus()
	if s.bl != nil {
		_ = s.bl.Set(100)
		_ = s.bl.Power(true)
	}
	if c.gui != nil {
		c.gui.Power(true)
	}
	s.tick.Store(int64(onTick))
	c.log.Printf("screen on (%s)", why)
	c.updateLight()
	return true
}

// ScreenOff is whether the screen is off (selftest).
func (c *Core) ScreenOff() bool { return c.screen.off }

// SetScreenAuto turns the timeout on or off (scripts start with it off).
func (c *Core) SetScreenAuto(on bool) {
	c.screen.auto = on
	c.screen.active = c.now()
	if !on {
		c.Wake("script")
	}
}

// unseen is how many notifications the user has not seen yet.
func (c *Core) unseen() int {
	n := 0
	for _, x := range c.notifs {
		if !x.seen {
			n++
		}
	}
	return n
}

// markSeen: the user has seen app's notifications (nil: all of them, the
// notification centre).
func (c *Core) markSeen(a *App) {
	changed := false
	for _, x := range c.notifs {
		if !x.seen && (a == nil || x.app == a) {
			x.seen, changed = true, true
		}
	}
	if changed {
		c.updateLight()
	}
}

// notified: a notification has come in. Without a notification light an
// unseen one lights the screen instead, and the timeout takes it from
// there.
func (c *Core) notified() {
	if c.screen.light == nil && c.screen.off && c.unseen() > 0 {
		c.Wake("notification")
	}
	c.updateLight()
}

// SetLightPresent stands in for a board without a notification light
// (selftest); true gives the light back.
func (c *Core) SetLightPresent(on bool) {
	s := &c.screen
	switch {
	case !on && s.light != nil:
		s.noLight, s.light = s.light, nil
	case on && s.light == nil:
		s.light, s.noLight = s.noLight, nil
	}
}

// updateLight: slow blink while the screen is off and something is unread.
func (c *Core) updateLight() {
	want := led.Pattern{}
	if c.screen.off && c.unseen() > 0 {
		want = led.SlowBlink
	}
	if c.screen.light == nil || c.screen.light.Pattern() == want {
		return
	}
	if err := c.setLight(want); err != nil {
		if e := err.Error(); e != c.screen.ledErr {
			c.screen.ledErr = e
			c.log.Printf("notification light: %v", err)
		}
		return
	}
	c.log.Printf("notification light: %s", want)
}

func (c *Core) setLight(p led.Pattern) error {
	if c.screen.light == nil {
		return nil
	}
	return c.screen.light.Set(p)
}

// Light is what the notification light shows ("" without one).
func (c *Core) Light() string {
	if c.screen.light == nil {
		return ""
	}
	return c.screen.light.Pattern().String()
}

// LightBlinks counts a userspace LED's blinks with the slow blink's period.
func (c *Core) LightBlinks() int64 { return c.screen.blinks.Load() }

// inputChanged: the renderer picked up a keyboard or mouse, or lost one
// (one on the USB-C port, plugged in or out).
func (c *Core) inputChanged(kind, name string, added bool) {
	state := "removed"
	if added {
		state = "added"
	}
	c.log.Printf("input: %s %s (%s)", kind, state, name)
	if c.OnInput != nil {
		c.OnInput(kind, name, added)
	}
}
