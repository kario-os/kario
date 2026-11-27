package shell

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// The refresh rate through phone-displayd (08-display-adaptive-refresh.md).
// displayd decides; the Shell passes on what moves and what holds the rate
// down: every key, the renderer's animations (its "motion" messages), the
// hints of the app in front (display.hint, rate-limited, taken back when
// it leaves the front), a video playing on screen (its frame rate), the
// setting and the low battery's cap. displayd's choice goes to the
// renderer ("rate"), which switches the panel's mode and paces its frames
// by it, and the renderer's answer goes back.

// displayKeyEvery: keys closer together than this pass on as one (the
// hold starts again with each passed on).
const displayKeyEvery = 100 * time.Millisecond

type displayShell struct {
	up bool
	st svc.DisplayStatus
	// out runs the calls to displayd in order, off the loop.
	out chan func()
	// What displayd was told: the last key passed on, the renderer's
	// motion, the setting and the battery's cap (-1: not yet), the apps
	// with a hint in force and the videos' frame rates by player.
	keyAt    time.Time
	motion   bool
	setting  string
	battery  int
	apps     map[string]bool
	videos   map[string]int
	buckets  map[string]*hintBucket
	rendered int // the rate the renderer was told
	// seen: the rates the panel went to, in order, and how many a
	// selftest step has looked at (headless.go).
	seen   []int
	looked int
}

func (c *Core) displaySock() string { return filepath.Join(c.cfg.RunDir, svc.DisplaydSock) }

// HasDisplay: phone-displayd is there (selftest's require-display).
func (c *Core) HasDisplay() bool { return c.display.up }

func (c *Core) startDisplay() {
	d := &c.display
	d.out = make(chan func(), 64)
	d.apps, d.videos, d.buckets = map[string]bool{}, map[string]int{}, map[string]*hintBucket{}
	d.battery = -1
	go func() {
		for f := range d.out {
			f()
		}
	}()
	go func() {
		for !c.stopping.Load() {
			if _, err := os.Stat(c.displaySock()); err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			nc, err := net.Dial("unix", c.displaySock())
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			conn := wire.New(nc)
			_ = conn.Send(wire.RPC{ID: 1, Method: wire.Subscribe})
			c.Post(func() {
				d.up, d.setting, d.battery, d.motion, d.rendered, d.st = true, "", -1, false, 0, svc.DisplayStatus{}
				clear(d.apps)
				clear(d.videos)
				c.displayCall(svc.DisplayHint, svc.DisplayHintArgs{Source: svc.DisplaySourceApp + "*", Kind: svc.DisplayClear})
				c.displayCall(svc.DisplayHint, svc.DisplayHintArgs{Source: svc.DisplaySourceVideo + "*", Kind: svc.DisplayClear})
				c.displayRenderer()
				c.checkDisplay()
				c.displayCall(svc.DisplayState, nil)
			})
			for {
				var ev svc.DisplayEvent
				if err := conn.Recv(&ev); err != nil {
					break
				}
				if ev.Event == svc.DisplayEventRate {
					c.Post(func() { c.displayRate(ev.DisplayStatus) })
				}
			}
			nc.Close()
			c.Post(func() { d.up = false })
			time.Sleep(500 * time.Millisecond)
		}
	}()
}

// displayCall queues a call; its answer, a state, is taken as an event's.
func (c *Core) displayCall(method string, params any) {
	if !c.display.up {
		return
	}
	f := func() {
		var st svc.DisplayStatus
		if err := c.broker.rpc(svc.Displayd, method, params, &st); err != nil {
			c.Post(func() { c.log.Printf("displayd %s: %v", method, err) })
			return
		}
		if st.Want > 0 {
			c.Post(func() { c.displayRate(st) })
		}
	}
	select {
	case c.display.out <- f:
	default:
		c.log.Printf("displayd: %s dropped, %d calls waiting", method, len(c.display.out))
	}
}

// displayRate: what displayd chose; the renderer switches to it. A state
// older than the one held (a call's answer overtaken by an event) is not
// taken.
func (c *Core) displayRate(st svc.DisplayStatus) {
	d := &c.display
	if st.Seq < d.st.Seq {
		return
	}
	if n := len(d.seen); n == 0 || d.seen[n-1] != st.Hz {
		d.seen = append(d.seen, st.Hz)
		if len(d.seen) > 256 {
			d.looked -= len(d.seen) - 128
			d.seen = append([]int(nil), d.seen[len(d.seen)-128:]...)
		}
	}
	d.st = st
	if g := c.gui; g != nil && st.Want > 0 && st.Want != d.rendered && g.info.Has("rate") {
		d.rendered = st.Want
		g.Rate(st.Want)
	}
}

// displayRenderer: a renderer started (or started again); it gets the
// rate in force, and displayd reads the panel's modes once one drives it.
func (c *Core) displayRenderer() {
	g := c.gui
	if g == nil || !c.display.up {
		return
	}
	c.display.rendered = 0
	// A renderer that died mid-animation never said it stopped.
	c.displayMotion(false)
	if g.info.Has("kms") {
		c.displayCall(svc.DisplayPanel, svc.DisplayPanelArgs{KMS: true})
	}
	c.displayRate(c.display.st)
}

// displayApplied: the renderer's answer to "rate".
func (c *Core) displayApplied(hz int, err string) {
	c.displayCall(svc.DisplayPanel, svc.DisplayPanelArgs{Applied: hz, Err: err})
}

// displayInput: a key went down.
func (c *Core) displayInput() {
	d := &c.display
	if now := time.Now(); now.Sub(d.keyAt) >= displayKeyEvery {
		d.keyAt = now
		c.displayCall(svc.DisplayHint, svc.DisplayHintArgs{Source: svc.DisplaySourceInput, Kind: svc.DisplayInteraction})
	}
	c.displayApps()
}

// displayMotion: the renderer began or ended an animation.
func (c *Core) displayMotion(on bool) {
	if on == c.display.motion {
		return
	}
	c.display.motion = on
	kind := svc.DisplayEnd
	if on {
		kind = svc.DisplayAnimating
	}
	c.displayCall(svc.DisplayHint, svc.DisplayHintArgs{Source: svc.DisplaySourceRenderer, Kind: kind})
}

// lowBatteryDisplay: below the level at which motion is reduced, not
// charging, the rate is capped at the standard one. A test's stand-in
// battery counts; the developer machine's default one does not.
func (c *Core) lowBatteryDisplay() bool {
	p := c.power()
	return (!p.Simulated || c.powerOverride != nil) && !p.Charging && p.Level > 0 && p.Level < lowBatteryMotion
}

// checkDisplay runs with the housekeeping: the setting, the battery's cap,
// the apps' and the videos' hints.
func (c *Core) checkDisplay() {
	d := &c.display
	if !d.up {
		return
	}
	if s := firstNonEmpty(c.settings.RefreshRate, ui.RefreshAuto); s != d.setting {
		d.setting = s
		c.displayCall(svc.DisplaySetting, map[string]string{"setting": s})
	}
	limit := 0
	if c.lowBatteryDisplay() {
		limit = ui.StandardHz
	}
	if limit != d.battery {
		d.battery = limit
		c.displayCall(svc.DisplayCap, svc.DisplayCapArgs{Source: svc.DisplayCapBattery, Hz: limit})
	}
	c.displayApps()
	want := map[string]int{}
	for id, p := range c.players {
		if !p.away && !p.held && p.fps > 0 && p.app == c.fg && !c.screen.off {
			want[id] = p.fps
		}
	}
	for id, fps := range want {
		if d.videos[id] != fps {
			d.videos[id] = fps
			c.displayCall(svc.DisplayHint, svc.DisplayHintArgs{Source: svc.DisplaySourceVideo + id, Kind: svc.DisplayContent, FPS: fps})
		}
	}
	for id := range d.videos {
		if _, ok := want[id]; !ok {
			delete(d.videos, id)
			c.displayCall(svc.DisplayHint, svc.DisplayHintArgs{Source: svc.DisplaySourceVideo + id, Kind: svc.DisplayClear})
		}
	}
}

// displayFront: the app may hint the display, being in front of the user.
// A system overlay over it (a menu) leaves its hint: the app does not hear
// of the overlay, so it could not ask again once it closes.
func (c *Core) displayFront(a *App) bool {
	return a != nil && a == c.fg && a.running && !c.screen.off && !c.locked()
}

// displayApps takes back the hints of apps no longer in front.
func (c *Core) displayApps() {
	for id := range c.display.apps {
		if a := c.appByID(id); !c.displayFront(a) {
			delete(c.display.apps, id)
			c.displayCall(svc.DisplayHint, svc.DisplayHintArgs{Source: svc.DisplaySourceApp + id, Kind: svc.DisplayClear})
		}
	}
}

// hintBucket rate-limits an app's hints: DisplayHintBurst at once, then
// DisplayHintPerSecond.
type hintBucket struct {
	tokens float64
	at     time.Time
}

func (b *hintBucket) take(now time.Time) bool {
	if b.at.IsZero() {
		b.tokens = ui.DisplayHintBurst
	} else {
		b.tokens = min(ui.DisplayHintBurst, b.tokens+now.Sub(b.at).Seconds()*ui.DisplayHintPerSecond)
	}
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

var errNoDisplay = errors.New("no display service")

// displayHint is display.hint: the app in front only, rate-limited.
func (c *Core) displayHint(a *App, args ui.DisplayHintArgs, reply func(any, error)) {
	d := &c.display
	set := 0
	for _, on := range []bool{args.AnimatingMS > 0, args.FPS > 0, args.Clear} {
		if on {
			set++
		}
	}
	switch {
	case set != 1 || args.AnimatingMS < 0 || args.FPS < 0:
		reply(nil, errors.New("display.hint takes one of animating_ms, fps, clear"))
		return
	case args.AnimatingMS > ui.DisplayHintMaxMS:
		reply(nil, errors.New("display.hint: animating_ms is at most 3000; ask again to go on"))
		return
	case args.FPS > ui.DisplayHintMaxFPS:
		reply(nil, errors.New("display.hint: fps is at most 240"))
		return
	case !c.displayFront(a):
		reply(nil, errors.New(ui.ErrDisplayNotFront))
		return
	case !d.up:
		reply(nil, errNoDisplay)
		return
	}
	b := d.buckets[a.M.ID]
	if b == nil {
		b = &hintBucket{}
		d.buckets[a.M.ID] = b
	}
	if !b.take(time.Now()) {
		reply(nil, errors.New(ui.ErrDisplayHintRate))
		return
	}
	h := svc.DisplayHintArgs{Source: svc.DisplaySourceApp + a.M.ID, Kind: svc.DisplayClear}
	switch {
	case args.AnimatingMS > 0:
		h.Kind, h.MS = svc.DisplayAnimating, args.AnimatingMS
	case args.FPS > 0:
		h.Kind, h.FPS = svc.DisplayContent, args.FPS
	}
	if h.Kind == svc.DisplayClear {
		delete(d.apps, a.M.ID)
	} else {
		d.apps[a.M.ID] = true
	}
	var st svc.DisplayStatus
	c.broker.call(svc.Displayd, svc.DisplayHint, h, &st, func(err error) {
		if err == nil {
			c.displayRate(st)
		}
		reply(ui.DisplayHintRes{Hz: st.Hz, Rates: st.Rates}, err)
	})
}

// displayView is settings.get's "display": the panel's rates, the rate
// now and why, the setting.
func (c *Core) displayView() map[string]any {
	st := c.display.st
	return map[string]any{"up": c.display.up, "panel": st.Panel, "rates": st.Rates, "hz": st.Hz, "reason": st.Reason,
		ui.SettingRefreshRate: max(0, slices.Index(ui.RefreshRates, firstNonEmpty(c.settings.RefreshRate, ui.RefreshAuto)))}
}

func (c *Core) setRefreshRate(v float64) error {
	i := int(v)
	if float64(i) != v || i < 0 || i >= len(ui.RefreshRates) {
		return errors.New("refresh_rate: 0 automatic, 1 standard, 2 high")
	}
	c.settings.RefreshRate = ""
	if i > 0 {
		c.settings.RefreshRate = ui.RefreshRates[i]
	}
	c.checkDisplay()
	return nil
}

// DisplayRate is the panel's rate as displayd last said, and why.
func (c *Core) DisplayRate() (int, string) { return c.display.st.Hz, c.display.st.Reason }

// DisplaySeen: whether the panel went to hz since the last look, which
// this one is; the look starts again from the rate now.
func (c *Core) DisplaySeen(hz int) bool {
	d := &c.display
	seen := slices.Contains(d.seen[max(0, d.looked):], hz)
	if seen {
		d.looked = len(d.seen) - 1
	}
	return seen
}

// DisplayThermal makes the simulated thermal zone hot or not (selftest).
func (c *Core) DisplayThermal(hot bool) error {
	if !c.display.up {
		return errNoDisplay
	}
	return c.broker.rpc(svc.Displayd, svc.DisplaySimThermal, map[string]bool{"hot": hot}, nil)
}
