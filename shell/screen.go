package shell

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/kario-os/kario/hal/backlight"
	"github.com/kario-os/kario/hal/led"
	"github.com/kario-os/kario/hal/sensor"
	"github.com/kario-os/kario/hal/vibrator"
	"github.com/kario-os/kario/ui"
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

// ScreenOffEnv: seconds without a key before the screen goes off where
// Settings › Display names none, instead of defaultScreenOff; for an
// emulator whose renderer takes tens of seconds a frame (a software GPU,
// yocto/qemu-gui.sh), which would otherwise go dark between two keys.
const ScreenOffEnv = "PHONE_SCREEN_OFF"

const (
	defaultScreenOff = 30
	screenDimFor     = 5 * time.Second
	screenDimLevel   = 20 // percent of the backlight
	// The brightness the user sets (settings: brightness) by default.
	defaultBrightness = 80
	onTick            = 500 * time.Millisecond
	offTick           = 5 * time.Second
)

// Backlight fades (docs/37-motion.md §3): lighting up 150 ms, dimming and
// going dark 300 ms, a new level (the setting, automatic brightness)
// 250 ms; at once with Settings › Animations off. Reduced motion keeps
// them: they move nothing on the screen and cost a few writes.
const (
	fadeLight = 150 * time.Millisecond
	fadeDark  = 300 * time.Millisecond
	fadeLevel = 250 * time.Millisecond
)

func (c *Core) fadeFor(d time.Duration) time.Duration {
	if c.motion() == motionOff {
		return 0
	}
	return d
}

type screenState struct {
	off, dimmed bool
	// auto: the timeout applies (scripts turn it on for the case that
	// tests it: their keys would otherwise be spent waking the screen).
	auto bool
	// stayOn: nothing turns the screen off or dims it (StayOn).
	stayOn bool
	// active is the last time something kept the screen on; offAt when it
	// went off.
	active time.Time
	offAt  time.Time
	wakes  int
	why    string
	// offAfter: ScreenOffEnv's seconds, 0 without it.
	offAfter int
	// The panel's power, one change at a time (panelSync): panel is the
	// renderer's last word on it, panelErr why it could not do what it was
	// asked, panelAt when it said so; want is what the screen needs.
	// asking: a change is on its way to the renderer, asked what for, at
	// askedAt; late once it has not answered in panelAnswer. retried: the
	// change failed once; stuck: twice, and it is not asked for again until
	// the screen wants the other. recycle: the panel came on with a
	// warning (no frame shown) and goes off and on again, once per change
	// (recycled). lightLater: the backlight waits for the panel to be on.
	// dueAt: when a panelSync is due.
	panel, want, asking, asked bool
	late, retried, stuck       bool
	recycle, recycled          bool
	lightLater                 bool
	panelErr                   string
	panelAt, askedAt, dueAt    time.Time

	// chargeToast: the toast of the charger plugged in at pluggedAt waits
	// for its driver to tell what it is.
	chargeToast bool
	pluggedAt   time.Time

	charging bool
	tick     atomic.Int64 // the housekeeping interval, in nanoseconds
	bl       *backlight.Fader
	// blSim: no backlight on this machine; bl is one in memory, so the
	// setting still works (and says so).
	blSim bool
	// kbd: the keyboard's backlight, lit while the screen is on and not
	// dimmed, at the screen's brightness; in memory where there is none.
	kbd *backlight.Fader
	// als: the ambient light sensor (hal/sensor), simulated (alsSim)
	// where there is none; lux its smoothed reading (-1 before the
	// first), daylight above daylightOn; autoLevel the automatic
	// brightness's level, 0 until known, set for the light autoLux and
	// adjusting while the light moves away from it (autobright.go).
	als       sensor.Light
	alsSim    bool
	alsFailed bool
	lux       float64
	daylight  bool
	// codeLit: a code to be scanned (Node.Bright) is in front, and the
	// screen is at full brightness for it.
	codeLit   bool
	autoLevel int
	autoLux   float64
	adjusting bool
	// motor: the vibration motor (hal/vibrator), nil where there is none;
	// motorFailed once it has refused (logged once).
	motor       vibrator.Motor
	motorFailed bool
	light       led.Light
	noLight     led.Light // the light a selftest took away
	uled        *led.User
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
		s = c.screen.offAfter
	}
	if s <= 0 {
		s = defaultScreenOff
	}
	return time.Duration(s) * time.Second
}

// setupScreen finds the backlight and the notification light the SKU
// names (sku.json); on QEMU the light is a userspace LED this process makes.
func (c *Core) setupScreen(sku SKU) {
	c.screen.tick.Store(int64(onTick))
	c.screen.offAfter, _ = strconv.Atoi(os.Getenv(ScreenOffEnv))
	c.screen.active = c.now()
	c.screen.auto = true
	c.screen.panel, c.screen.want = true, true
	c.screen.charging = c.power().Charging
	c.watchPower(sku)
	// Not root (a developer's machine, as for the radios): a laptop's own
	// backlight is not the phone's.
	if b := backlight.Panel(sku.Backlight); b != nil && os.Geteuid() == 0 {
		c.screen.bl = backlight.NewFader(b)
	} else {
		c.screen.bl, c.screen.blSim = backlight.NewFader(backlight.NewSim()), true
	}
	if k := backlight.Keyboard(sku.KeyboardBacklight); k != nil && os.Geteuid() == 0 {
		c.screen.kbd = backlight.NewFader(k)
		c.log.Printf("keyboard backlight %s", k.Name())
	} else {
		c.screen.kbd = backlight.NewFader(backlight.NewSim())
	}
	c.screen.lux = -1
	if l := sensor.OpenLight(sku.LightSensor); l != nil && os.Geteuid() == 0 {
		c.screen.als = l
		c.log.Printf("light sensor %s", l.Name())
	} else {
		c.screen.als, c.screen.alsSim = sensor.NewSim(), true
	}
	if m := vibrator.Open(); m != nil && os.Geteuid() == 0 {
		c.screen.motor = m
		c.log.Printf("vibration motor %s", m.Name())
	}
	c.applyBrightness()
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

// chargeToastWait: how long a charger's toast waits for its driver to
// tell what it is (Power.Detecting), at most.
const chargeToastWait = 3 * time.Second

// checkCharger lights the screen for a charger plugged in and says
// "Charging" or "Fast charging" once its driver knows which. It runs with
// the housekeeping and at once when the kernel says a supply changed
// (watchPower).
func (c *Core) checkCharger() {
	s := &c.screen
	p := c.power()
	if p.Charging != s.charging {
		s.charging, s.chargeToast, s.pluggedAt = p.Charging, p.Charging, time.Now()
		if p.Charging {
			c.Wake("charger")
		}
		c.checkUSB()
	}
	if s.chargeToast && (!p.Detecting || time.Since(s.pluggedAt) >= chargeToastWait) {
		s.chargeToast = false
		if p.Fast {
			c.showToast(tr.T("Fast charging"))
		} else {
			c.showToast(tr.T("Charging"))
		}
	}
}

// checkScreen runs with the housekeeping.
func (c *Core) checkScreen() {
	s := &c.screen
	c.checkCharger()
	c.senseLight()
	if lit := c.codeShown(); lit != s.codeLit {
		s.codeLit = lit
		c.log.Printf("code to be scanned: full brightness %v", lit)
		c.applyBrightness()
	}
	if s.off || !s.auto || s.stayOn {
		return
	}
	now := c.now()
	if c.lastKey.After(now) {
		c.lastKey = now
	}
	if c.keepScreen() || s.active.After(now) || s.codeLit && now.Sub(c.lastKey) < ui.CodeAwakeSecs*time.Second {
		s.active = now
	}
	idle := now.Sub(later(s.active, c.lastKey))
	t := c.screenTimeout()
	switch {
	case idle >= t:
		c.screenOff("timeout")
	case idle >= t-screenDimFor && !s.dimmed:
		s.dimmed = true
		if s.bl != nil {
			s.bl.Fade(min(screenDimLevel, c.level()), c.fadeFor(fadeDark))
		}
		c.keyLight()
	}
}

// keyLight lights the keyboard with the screen, at its brightness; dimming
// turns it off, the first sign the screen is about to go, and so does
// daylight (autobright.go).
func (c *Core) keyLight() {
	s := &c.screen
	if s.kbd == nil {
		return
	}
	on := !s.off && !s.dimmed && !s.daylight
	_, lit := s.kbd.Level()
	switch {
	case on && lit:
		s.kbd.Fade(c.level(), c.fadeFor(fadeLevel))
	case on:
		s.kbd.FadeOn(c.level(), c.fadeFor(fadeLight))
	case lit:
		s.kbd.FadeOff(c.fadeFor(fadeDark), nil)
	}
}

// KeyLight is the keyboard backlight's level in percent, -1 while it is off
// (selftest).
func (c *Core) KeyLight() int {
	if c.screen.kbd == nil {
		return -1
	}
	p, on := c.screen.kbd.Level()
	if !on {
		return -1
	}
	return p
}

func (c *Core) brightness() int {
	if b := c.settings.Brightness; b > 0 {
		return b
	}
	return defaultBrightness
}

// applyBrightness puts the brightness set in force, unless the screen is
// dimmed or off (waking it does) or the light waits for the panel. The
// Shell starts with the screen on, and lights a backlight found off: the
// Shell before it ended with the screen off, and the hardware stays as it
// left it.
func (c *Core) applyBrightness() {
	if s := &c.screen; s.bl != nil && !s.dimmed && !s.off && !s.lightLater {
		if _, on := s.bl.Level(); on {
			s.bl.Fade(c.level(), c.fadeFor(fadeLevel))
		} else {
			s.bl.FadeOn(c.level(), c.fadeFor(fadeLight))
		}
	}
	c.keyLight()
}

// Backlight is the backlight's level in percent, the one it is at or
// fading to (selftest).
func (c *Core) Backlight() int {
	if c.screen.bl == nil {
		return -1
	}
	p, _ := c.screen.bl.Level()
	return p
}

func (c *Core) screenOff(why string) {
	s := &c.screen
	if s.stayOn {
		c.screenLog("screen stays on (%s): bring-up switch", why)
		return
	}
	if s.off {
		return
	}
	c.closePowerMenus()
	s.off, s.dimmed, s.why, s.offAt = true, false, why, c.now()
	s.lightLater = false
	c.updateFocus()
	c.btSearchEnd(c.radios.search, "the screen went off")
	// The renderer turns the panel off once the light has faded out: its
	// driver takes the backlight down with the panel, cutting a fade short.
	if d := c.fadeFor(fadeDark); s.bl != nil && d > 0 {
		s.bl.FadeOff(d, func() { c.Post(c.panelOff) })
	} else {
		if s.bl != nil {
			_ = s.bl.Power(false)
		}
		c.panelOff()
	}
	c.keyLight()
	s.tick.Store(int64(offTick))
	c.screenLog("screen off (%s)", why)
	c.updateLight()
}

// The panel's power changes one at a time: the next is asked for
// panelSettle after the renderer answered the one before (a DSI panel
// switched faster than its power sequence settles can stay dark); a
// renderer that has not answered in panelAnswer is logged.
const (
	panelSettle = 250 * time.Millisecond
	panelAnswer = 2 * time.Second
)

// panelOff tells the renderer to turn the panel off, if the screen is
// still off.
func (c *Core) panelOff() {
	if c.screen.off {
		c.panelPower(false)
	}
}

// panelControl: the renderer turns the panel on and off (feature "power").
func (c *Core) panelControl() bool { return c.gui != nil && c.gui.info.Has("power") }

// panelPower: the screen needs the panel on or off; the renderer gets the
// change when the one before it is done (panelSync).
func (c *Core) panelPower(on bool) {
	s := &c.screen
	if s.want != on {
		s.want, s.retried, s.stuck = on, false, false
		s.recycle, s.recycled = false, false
	}
	c.panelSync()
}

// panelSync asks the renderer for the panel the screen needs: only a
// change, one at a time, panelSettle after its last answer; what the
// screen came to need meanwhile replaces what it needed before, so the
// panel ends as the last press left the screen whatever came between.
func (c *Core) panelSync() {
	s := &c.screen
	if !c.panelControl() {
		return
	}
	now := time.Now()
	if s.asking {
		if wait := panelAnswer - now.Sub(s.askedAt); wait > 0 {
			c.panelAfter(wait)
		} else if !s.late {
			s.late = true
			c.screenLog("panel: the renderer has not answered in %s (asked to turn it %s)", panelAnswer, onOff(s.asked))
		}
		return
	}
	want := s.want && !s.recycle
	if want == s.panel || s.stuck {
		return
	}
	if wait := panelSettle - now.Sub(s.panelAt); wait > 0 {
		c.panelAfter(wait)
		return
	}
	s.asking, s.asked, s.askedAt, s.late = true, want, now, false
	c.screenLog("panel: asking the renderer to turn it %s", onOff(want))
	c.gui.Power(want)
	c.panelAfter(panelAnswer)
}

// panelAfter has panelSync run again after d.
func (c *Core) panelAfter(d time.Duration) {
	s := &c.screen
	at := time.Now().Add(d)
	if !s.dueAt.IsZero() && !at.Before(s.dueAt) {
		return
	}
	s.dueAt = at
	time.AfterFunc(d, func() {
		c.Post(func() {
			if s.dueAt.Equal(at) {
				s.dueAt = time.Time{}
			}
			c.panelSync()
		})
	})
}

// panelAnswered is the renderer's answer to "power": the panel's state now,
// why it could not do what it was asked (err), how long it took and what
// it noticed about a panel it did turn (warn). A change that failed is
// asked for once more; failing again, it is left: a panel that would not
// come on leaves the screen off, so that the Shell's screen is what the
// panel shows and the next press tries again. A panel that came on with a
// warning goes off and on once more: phone-paint warns when no page flip
// completes after it came on, as a command-mode DSI panel that did not
// come up after a suspend does (the 6T on 2026-11-22: black until a second
// mode set, by hand).
func (c *Core) panelAnswered(on bool, err string, ms int, warn string) {
	s := &c.screen
	asked, asking := s.asked, s.asking
	s.panel, s.panelErr, s.panelAt, s.asking = on, err, time.Now(), false
	took := ""
	if ms > 0 {
		took = fmt.Sprintf(" in %d ms", ms)
	}
	if warn != "" {
		warn = "; the renderer says: " + warn
	}
	switch {
	case on && asking && asked && warn != "" && !s.recycled && !s.off:
		s.retried, s.recycle, s.recycled = false, true, true
		c.screenLog("panel: on%s%s; turning it off and on again", took, warn)
	case !asking || on == asked:
		s.retried = false
		c.screenLog("panel: %s%s%s", onOff(on), took, warn)
	case !s.retried:
		s.retried = true
		c.screenLog("panel: the renderer could not turn it %s%s (%s); trying once more", onOff(asked), took, err)
	default:
		s.retried, s.stuck = false, true
		c.screenLog("panel: the renderer could not turn it %s again%s (%s)", onOff(asked), took, err)
		if asked && !s.off {
			c.screenOff("the panel did not come on")
		}
	}
	if !s.panel || s.stuck {
		s.recycle = false
	}
	if on && s.lightLater && !s.off && !s.recycle {
		c.lightUp()
	}
	c.panelSync()
}

// panelReset: a renderer has started, the panel on (it sets the mode as it
// opens the display), nothing asked of it yet.
func (c *Core) panelReset() {
	s := &c.screen
	s.panel, s.panelErr, s.panelAt = true, "", time.Time{}
	s.asking, s.late, s.retried, s.stuck = false, false, false, false
	s.recycle, s.recycled = false, false
	if s.lightLater && !s.off {
		c.lightUp()
	}
	c.panelSync()
}

// lightUp lights the backlight of a screen that is on: at once over a panel
// that is on, else when the renderer says it is, however late (a new
// renderer starts with it on). On a phone the panel driver owns the
// backlight: lit while the panel is off or powering up, the writes race
// its power sequence (a renderer stuck there would hold the Shell's loop
// in the write), and the light would show the panel's memory before the
// frame.
func (c *Core) lightUp() {
	s := &c.screen
	if c.panelControl() && (!s.panel || s.asking || s.recycle) {
		s.lightLater = true
		return
	}
	s.lightLater = false
	if s.bl != nil && !s.dimmed {
		s.bl.FadeOn(c.level(), c.fadeFor(fadeLight))
	}
}

// panelWhy says, after the Shell's screen and the renderer's panel, what
// the panel waits for: the backlight's fade out before the Shell asks, the
// renderer's answer, the panel settling, or what the renderer could not do.
func (c *Core) panelWhy() string {
	s := &c.screen
	switch {
	case !c.panelControl():
		return ""
	case s.asking:
		return fmt.Sprintf("; asked to turn it %s %s ago, the renderer has not answered", onOff(s.asked), time.Since(s.askedAt).Round(time.Millisecond))
	case s.panelErr != "":
		return "; the renderer could not: " + s.panelErr
	case s.recycle:
		return "; turning it off and on again: it showed no frame after it came on"
	case s.off && s.want:
		return "; not asked to turn it off yet: the backlight has not faded out"
	case s.want != s.panel:
		return "; waiting for the panel to settle"
	}
	return ""
}

// panelSettled: nothing is on its way to the renderer and the panel is as
// the screen is (selftest).
func (c *Core) panelSettled() bool {
	s := &c.screen
	return !c.panelControl() || !s.asking && !s.recycle && s.panel == !s.off
}

// Wake lights the screen; it reports whether it was off. Under a shut lid
// the screen stays off (lid.go), and Wake reports false.
func (c *Core) Wake(why string) bool {
	s := &c.screen
	s.active = c.now()
	if s.dimmed {
		s.dimmed = false
		if s.bl != nil && !s.lightLater {
			s.bl.Fade(c.level(), c.fadeFor(fadeLight))
		}
		c.keyLight()
	}
	if !s.off {
		return false
	}
	if c.lidShut() {
		c.screenLog("screen stays off (%s): the lid is shut", why)
		return false
	}
	s.off, s.why = false, why
	s.wakes++
	c.updateFocus()
	c.panelPower(true)
	c.lightUp()
	c.keyLight()
	s.tick.Store(int64(onTick))
	c.screenLog("screen on (%s)", why)
	c.updateLight()
	return true
}

// ScreenLog, when set, also gets the Shell's lines about the screen, the
// panel, the power key, the input devices and sleep (device mode: the
// journal keeps them over a forced restart, shell.log in /run does not).
var ScreenLog io.Writer

func (c *Core) screenLog(format string, args ...any) {
	c.log.Printf(format, args...)
	if w := ScreenLog; w != nil {
		fmt.Fprintf(w, "phone-shell: "+format+"\n", args...)
	}
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// ScreenOff is whether the screen is off (selftest).
func (c *Core) ScreenOff() bool { return c.screen.off }

// StayOn is a bring-up switch (phone-shell --stay-on): neither the timeout
// nor the power key turns the screen off or dims it, so the backlight and
// the panel stay powered, and the phone never sleeps (it sleeps with the
// screen off). Not a setting: it lasts as long as this Shell.
func (c *Core) StayOn() {
	c.Wake("bring-up switch")
	c.screen.stayOn = true
	c.log.Printf("screen: stays on (bring-up switch)")
}

// NoHaptics is a bring-up switch (phone-shell --no-haptics): the vibration
// motor is left alone; its requests are still counted.
func (c *Core) NoHaptics() {
	c.screen.motor = nil
	c.log.Printf("vibration: off (bring-up switch)")
}

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
// (one on the USB-C port, plugged in or out); ok: it can press OK.
func (c *Core) inputChanged(kind, name string, added, ok bool) {
	state, step := "removed", -1
	if added {
		state, step = "added", 1
	}
	in := &c.inputs
	in.n = max(0, in.n+step)
	if ok {
		in.ok = max(0, in.ok+step)
	}
	if ok && kind == "keyboard" {
		in.kbd = max(0, in.kbd+step)
	}
	c.screenLog("input: %s %s (%s%s)", kind, state, name, map[bool]string{false: ", no OK key"}[ok])
	if c.OnInput != nil {
		c.OnInput(kind, name, added)
	}
}
