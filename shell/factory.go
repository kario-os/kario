package shell

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kario-os/kario/audio"
	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/factory"
	"github.com/kario-os/kario/hal/usbgadget"
	"github.com/kario-os/kario/netfw"
	"github.com/kario-os/kario/svc"
)

// Factory mode (docs/31-lock-and-biometrics.md §13, docs/40-testing.md
// §10): the boot the line tests a phone in. The Shell shows the factory
// screen and nothing else: the runner's (phone-factory-test --serve)
// interactive checks, one screen each, answered with keys where the check
// cannot measure itself, each result handed back to the runner. No app
// starts (spawn), the status bar says factory mode, the screen stays on,
// and the USB link is the station's without the trust prompt (checkUSB).
// It ends with the station's provisioning (the phone powers off) or an
// erase; nothing of it outlives the boot.
type factoryMode struct {
	unit *factory.Unit
	// stop ends the runner this process started (the selftest's).
	stop func()
	plan *factory.PlanInfo
	err  string
	done map[string]factory.CheckResult
	auto *factory.Report
	// screen: "" (home), an interactive check's id, "auto", "erase",
	// "provisioned".
	screen string
	sel    int
	// The screen's state: when it opened, its progress (keys pressed,
	// squares touched, the pattern shown), whether it waits for the
	// runner, its note, the camera's stream.
	opened  time.Time
	moved   time.Time
	pressed map[string]bool
	last    string
	cells   []bool
	pattern int
	busy    bool
	note    string
	bad     bool
	camID   string
	camName string
	stream  string
	// usb: the station's link is up; offAt: when a provisioned phone
	// powers off, offAfter after the runner said so; poll: how often the
	// watcher asks the runner, fixed as the mode begins (tests change
	// factoryPoll).
	usb      bool
	offAt    time.Time
	offAfter time.Duration
	poll     time.Duration
	quit     chan struct{}
}

// How long a screen waits: the keys and touch checks fail after so long
// without progress, a full-panel pattern returns to the marked screen, a
// provisioned phone powers off.
var (
	factoryIdle       = 20 * time.Second
	factoryPatternMax = 30 * time.Second
	factoryOffAfter   = 10 * time.Second
	factoryPoll       = 2 * time.Second
)

// factoryScreenOff: the operator works at the line's pace; the screen
// stays on that long without a key.
const factoryScreenOff = 600

// The display check's patterns in turn: solid colours, then a grid on
// black for dead and stuck pixels.
var factoryPatterns = []FramePattern{
	{Color: "#ff0000"}, {Color: "#00ff00"}, {Color: "#0000ff"}, {Color: "#ffffff"}, {Color: "#000000"},
	{Color: "#000000", Grid: [2]int{9, 16}, Line: "#ffffff"},
}

// The touch check's squares, columns by rows on a portrait panel.
const factoryTouchCols, factoryTouchRows = 4, 6

// EnterFactory puts the Shell in factory mode with the runner behind u;
// stop, if any, ends a runner of this process when factory mode does.
func (c *Core) EnterFactory(u *factory.Unit, stop func()) {
	if c.factory != nil {
		c.LeaveFactory()
	}
	f := &factoryMode{unit: u, stop: stop, done: map[string]factory.CheckResult{}, offAfter: factoryOffAfter, poll: factoryPoll, quit: make(chan struct{})}
	c.factory = f
	c.log.Printf("factory mode: the factory screen in place of everything else")
	c.Wake("factory")
	go c.factoryWatch(f)
}

// LeaveFactory ends factory mode in this process (the selftest's; a phone
// leaves it only by powering off or restarting).
func (c *Core) LeaveFactory() {
	f := c.factory
	if f == nil {
		return
	}
	c.factoryLeaveScreen()
	close(f.quit)
	if f.usb {
		c.factoryLink(false)
	}
	c.factory = nil
	if f.stop != nil {
		f.stop()
	}
}

// InFactory: the Shell is in factory mode.
func (c *Core) InFactory() bool { return c.factory != nil }

// FactoryResult is the factory screen's result of a check ("" while not
// done), for the selftest.
func (c *Core) FactoryResult(id string) string {
	if c.factory == nil {
		return ""
	}
	return string(c.factory.done[id].Result)
}

// FactoryUSB is the station's link: up, and the ports the firewall opens
// on it.
func (c *Core) FactoryUSB() (up bool, tcp, udp []uint16) {
	if c.factory == nil {
		return false, nil, nil
	}
	return c.factory.usb, c.usb.link.TCP, c.usb.link.UDP
}

// factoryWatch fetches the plan and then asks the runner whether the
// station has provisioned the phone, until factory mode ends.
func (c *Core) factoryWatch(f *factoryMode) {
	for {
		if p, err := f.unit.Plan(); err == nil {
			c.Post(func() { f.plan, f.err = &p, "" })
			break
		} else {
			c.Post(func() { f.err = err.Error() })
		}
		select {
		case <-f.quit:
			return
		case <-time.After(f.poll):
		}
	}
	for {
		select {
		case <-f.quit:
			return
		case <-time.After(f.poll):
		}
		if info, err := f.unit.Info(); err == nil && info.Provisioned {
			c.Post(func() { c.factoryProvisioned(f) })
			return
		}
	}
}

func (c *Core) factoryProvisioned(f *factoryMode) {
	if c.factory != f {
		return
	}
	c.factoryLeaveScreen()
	f.screen, f.offAt = "provisioned", c.now().Add(f.offAfter)
	c.log.Printf("factory mode: provisioned; powering off in %s", f.offAfter)
	c.Wake("factory")
	c.factoryTick(f, "provisioned", func() { c.powerOff("poweroff") })
}

// factoryTick runs done once the screen's time is up, the screen still
// showing, redrawing the countdown each second until then.
func (c *Core) factoryTick(f *factoryMode, screen string, done func()) {
	time.AfterFunc(time.Second, func() {
		c.Post(func() {
			if c.factory != f || f.screen != screen {
				return
			}
			if !c.now().Before(f.offAt) {
				done()
				return
			}
			c.factoryTick(f, screen, done)
		})
	})
}

// factoryChecks are the interactive checks the plan runs on this phone.
func (f *factoryMode) checks() []factory.Check {
	if f.plan == nil {
		return nil
	}
	var out []factory.Check
	for _, ch := range factory.Only(f.plan.Checks, factory.Interactive) {
		if ch.Skip == "" {
			out = append(out, ch)
		}
	}
	return out
}

func (f *factoryMode) check(id string) (factory.Check, bool) {
	for _, ch := range f.checks() {
		if ch.ID == id {
			return ch, true
		}
	}
	return factory.Check{}, false
}

// buttons: the phone's keys are its buttons only (the 6T): power opens and
// passes, the volume keys move and answer.
func (f *factoryMode) buttons() bool {
	if f.plan == nil {
		return false
	}
	return f.plan.SKU.Factory.Keypad == nil && !slices.ContainsFunc(f.plan.SKU.Factory.Keys, func(k string) bool {
		return k != "power" && k != "vol-up" && k != "vol-down"
	})
}

func factoryTitle(id string) string {
	switch id {
	case "keys":
		return tr.T("Keys")
	case "display.patterns":
		return tr.T("Display")
	case "touch":
		return tr.T("Touchscreen")
	case "speaker":
		return tr.T("Speaker")
	case "microphone":
		return tr.T("Microphone")
	case "vibrator.felt":
		return tr.T("Vibration")
	case "camera.back":
		return tr.T("Back camera")
	case "camera.front":
		return tr.T("Front camera")
	case "auto":
		return tr.T("Automatic checks")
	case "erase":
		return tr.T("Erase and restart")
	}
	return id
}

func resultMark(r factory.Result) string {
	switch r {
	case factory.Pass:
		return "✓"
	case factory.Fail:
		return "✗"
	case factory.Skip:
		return "–"
	}
	return "·"
}

// homeItems are the home screen's rows: the checks, then the automatic
// checks and the erase.
func (f *factoryMode) homeItems() []string {
	var ids []string
	for _, ch := range f.checks() {
		ids = append(ids, ch.ID)
	}
	return append(ids, "auto", "erase")
}

// factoryPress takes every key in factory mode: the screen lights first,
// then the factory screen has it.
func (c *Core) factoryPress(k Key) {
	if k.At.IsZero() {
		k.At = time.Now()
	}
	c.lastKey = c.now()
	if c.Wake("key") {
		return
	}
	f := c.factory
	switch f.screen {
	case "":
		c.factoryHomeKey(f, k)
	case "keys":
		c.factoryKeysKey(f, k)
	case "display.patterns":
		c.factoryPatternKey(f, k)
	case "touch":
		if factoryFail(k) {
			c.factoryRecord(f, "touch", factory.Fail, "", fmt.Sprintf("%d of %d squares not touched", f.untouched(), len(f.cells)))
		}
	case "microphone":
		c.factoryMicKey(f, k)
	case "auto":
		if !f.busy {
			c.factoryHome(f)
		}
	case "erase":
		switch {
		case factoryPass(k, f.buttons()):
			c.factoryReset("factory test", false)
		case factoryFail(k):
			c.factoryHome(f)
		}
	case "provisioned":
		if factoryPass(k, f.buttons()) {
			c.powerOff("poweroff")
		}
	default:
		c.factoryAnswerKey(f, k)
	}
}

func factoryPass(k Key, buttons bool) bool {
	return k.Name == KeyEnter || k.Name == KeyTab || k.Name == KeyVolUp || buttons && k.Name == KeyPower
}

func factoryFail(k Key) bool {
	return k.Name == KeyEsc || k.Name == KeyF2 || k.Name == KeyBackspace || k.Name == KeyVolDown
}

func (c *Core) factoryHomeKey(f *factoryMode, k Key) {
	items := f.homeItems()
	switch k.Name {
	case KeyDown, KeyVolDown:
		f.sel = (f.sel + 1) % len(items)
	case KeyUp, KeyVolUp:
		f.sel = (f.sel + len(items) - 1) % len(items)
	case KeyEnter, KeyRight, KeyPower:
		if f.plan != nil {
			c.factoryOpen(f, items[min(f.sel, len(items)-1)])
		}
	}
}

func (c *Core) factoryHome(f *factoryMode) {
	c.factoryLeaveScreen()
	f.screen = ""
}

// factoryOpen starts a check's screen.
func (c *Core) factoryOpen(f *factoryMode, id string) {
	c.factoryLeaveScreen()
	f.screen, f.opened, f.moved, f.note, f.bad, f.busy = id, c.now(), c.now(), "", false, false
	switch id {
	case "keys":
		f.pressed, f.last = map[string]bool{}, ""
		c.factoryIdleTimer(f, id, func() {
			ch, _ := f.check("keys")
			c.factoryRecord(f, "keys", factory.Fail, "", "not pressed: "+strings.Join(f.keysLeft(ch), " "))
		})
	case "display.patterns":
		f.pattern = -1
		if c.gui != nil && !c.gui.info.Has("pattern") {
			c.factoryRecord(f, id, factory.Skip, "", "the pixel renderer draws no test patterns (no feature pattern)")
		}
	case "touch":
		f.cells = make([]bool, factoryTouchCols*factoryTouchRows)
		c.factoryIdleTimer(f, id, func() {
			c.factoryRecord(f, "touch", factory.Fail, "", fmt.Sprintf("%d of %d squares not touched", f.untouched(), len(f.cells)))
		})
	case "speaker":
		c.sound("factory", "alarm", audio.Alarm)
	case "vibrator.felt":
		c.vibrateMS(600)
	case "camera.back", "camera.front":
		c.factoryCamera(f, id)
	case "auto":
		f.busy = true
		go func() {
			rep, err := f.unit.Test(factory.ModeAuto, 0)
			c.Post(func() {
				f.busy = false
				if err != nil {
					f.note, f.bad = err.Error(), true
				} else {
					f.auto = rep
				}
			})
		}()
	}
}

// factoryLeaveScreen ends what the screen started: the tone, the camera.
func (c *Core) factoryLeaveScreen() {
	f := c.factory
	if f == nil {
		return
	}
	switch f.screen {
	case "speaker":
		c.quiet("factory")
	case "camera.back", "camera.front":
		if id := f.camID; id != "" {
			go func() { _ = c.broker.rpc(svc.Mediad, svc.MediaCameraClose, map[string]string{"cam_id": id}, nil) }()
		}
		f.camID, f.camName, f.stream = "", "", ""
	}
}

// factoryIdleTimer fails the screen after factoryIdle without progress.
func (c *Core) factoryIdleTimer(f *factoryMode, screen string, fail func()) {
	var arm func()
	arm = func() {
		time.AfterFunc(factoryIdle, func() {
			c.Post(func() {
				if c.factory != f || f.screen != screen {
					return
				}
				if c.now().Sub(f.moved) >= factoryIdle {
					fail()
					return
				}
				arm()
			})
		})
	}
	arm()
}

// factoryRecord keeps a check's result and hands it to the runner; the
// home screen shows again.
func (c *Core) factoryRecord(f *factoryMode, id string, r factory.Result, value, reason string) {
	res := factory.CheckResult{ID: id, Kind: factory.Interactive, Result: r, Value: value, Reason: reason,
		DurationMS: c.now().Sub(f.opened).Milliseconds()}
	f.done[id] = res
	c.log.Printf("factory: %s %s %s %s", id, r, value, reason)
	c.factoryHome(f)
	if i := slices.Index(f.homeItems(), id); i >= 0 {
		f.sel = i + 1
	}
	go func() {
		if err := f.unit.Record(res); err != nil {
			c.Post(func() {
				f.err = err.Error()
			})
		}
	}()
}

// factoryKeyName is keys.toml's name of a key the Shell got ("" for one
// that is no key of a keypad).
func factoryKeyName(k Key) string {
	switch k.Name {
	case KeyTab:
		return "soft-left"
	case KeyF2:
		return "soft-right"
	case KeyEnter:
		return "ok"
	case KeyEsc:
		return "back"
	case KeyBackspace:
		return "clear"
	case KeyF4:
		return "end"
	case KeyVolUp:
		return "vol-up"
	case KeyVolDown:
		return "vol-down"
	case KeyCall, KeyUp, KeyDown, KeyLeft, KeyRight, KeyIME, KeySym, KeyPower:
		return k.Name
	case KeyShiftOnce, KeyShiftLock, KeyShiftOff:
		return "shift"
	case KeyFnOnce, KeyFnLock, KeyFnOff:
		return "fn"
	case KeyRunes:
		if len(k.Runes) != 1 {
			return ""
		}
		switch r := k.Runes[0]; {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z':
			return string(r)
		case r >= 'A' && r <= 'Z':
			return string(r - 'A' + 'a')
		case r == '*':
			return "star"
		case r == '#':
			return "pound"
		case r == ' ':
			return "space"
		case r == ',':
			return "comma"
		case r == '.':
			return "dot"
		}
	}
	return ""
}

func (f *factoryMode) keysLeft(ch factory.Check) []string {
	var left []string
	for _, k := range ch.Keys {
		if !f.pressed[k] {
			left = append(left, k)
		}
	}
	return left
}

func (c *Core) factoryKeysKey(f *factoryMode, k Key) {
	ch, _ := f.check("keys")
	name := factoryKeyName(k)
	if name == "" || !slices.Contains(ch.Keys, name) {
		return
	}
	f.last = name
	if !f.pressed[name] {
		f.pressed[name], f.moved = true, c.now()
	}
	if len(f.keysLeft(ch)) == 0 {
		c.factoryRecord(f, "keys", factory.Pass, fmt.Sprintf("%d keys", len(ch.Keys)), "")
	}
}

func (c *Core) factoryPatternKey(f *factoryMode, k Key) {
	if f.pattern < len(factoryPatterns) {
		if f.pattern < 0 && !factoryPass(k, f.buttons()) {
			if factoryFail(k) {
				c.factoryHome(f)
			}
			return
		}
		c.factoryNextPattern(f)
		return
	}
	c.factoryAnswerKey(f, k)
}

// factoryNextPattern shows the next pattern, which goes back to the marked
// screen by itself after factoryPatternMax.
func (c *Core) factoryNextPattern(f *factoryMode) {
	f.pattern++
	if f.pattern >= len(factoryPatterns) {
		return
	}
	n := f.pattern
	time.AfterFunc(factoryPatternMax, func() {
		c.Post(func() {
			if c.factory == f && f.screen == "display.patterns" && f.pattern == n {
				f.pattern = len(factoryPatterns)
			}
		})
	})
}

// factoryAnswerKey is the operator's answer where the check cannot
// measure itself.
func (c *Core) factoryAnswerKey(f *factoryMode, k Key) {
	if f.busy {
		return
	}
	id := f.screen
	value := ""
	if strings.HasPrefix(id, "camera.") {
		if f.stream == "" {
			if f.note != "" && (factoryPass(k, f.buttons()) || factoryFail(k)) {
				c.factoryHome(f)
			}
			return
		}
		value = f.camName
	}
	switch {
	case factoryPass(k, f.buttons()):
		c.factoryRecord(f, id, factory.Pass, value, "")
	case factoryFail(k):
		c.factoryRecord(f, id, factory.Fail, value, "the operator saw it fail")
	case id == "vibrator.felt" && k.Name == KeyRunes:
		c.vibrateMS(600)
	}
}

func (c *Core) factoryMicKey(f *factoryMode, k Key) {
	switch {
	case f.busy:
	case f.note != "":
		c.factoryHome(f)
	case factoryPass(k, f.buttons()):
		f.busy = true
		go func() {
			res, err := f.unit.Microphone(true)
			c.Post(func() {
				f.busy = false
				if err != nil {
					f.note, f.bad = err.Error(), true
				} else {
					f.done["microphone"] = res
					f.note, f.bad = res.Value, res.Result != factory.Pass
					if res.Reason != "" {
						f.note = res.Reason
					}
					res.DurationMS = c.now().Sub(f.opened).Milliseconds()
					go func() { _ = f.unit.Record(res) }()
				}
			})
		}()
	case factoryFail(k):
		c.factoryHome(f)
	}
}

// factoryCamera opens the check's camera through mediad, or skips the
// check where mediad cannot reach it: it drives one camera, the first of
// the board's, and draws a picture of its own without one.
func (c *Core) factoryCamera(f *factoryMode, id string) {
	f.busy = true
	go func() {
		var open struct {
			CamID  string `json:"cam_id"`
			Stream string `json:"stream"`
		}
		var name, reason string
		if id == "camera.front" {
			reason = "mediad drives one camera, the back one; the front one cannot be opened yet"
		} else {
			name, _ = c.MediaInfo()["camera"].(string)
		}
		switch {
		case reason != "":
		case name == "" || name == "synthetic":
			reason = "mediad has no camera driver: it shows a picture of its own"
		default:
			if err := c.broker.rpc(svc.Mediad, svc.MediaCameraOpen, map[string]string{"app": "system.factory"}, &open); err != nil {
				reason = "the camera did not open: " + err.Error()
			}
		}
		c.Post(func() {
			if c.factory != f || f.screen != id {
				if open.CamID != "" {
					go func() {
						_ = c.broker.rpc(svc.Mediad, svc.MediaCameraClose, map[string]string{"cam_id": open.CamID}, nil)
					}()
				}
				return
			}
			f.busy = false
			if reason != "" {
				res := factory.CheckResult{ID: id, Kind: factory.Interactive, Result: factory.Skip, Reason: reason}
				f.done[id] = res
				f.note = reason
				go func() { _ = f.unit.Record(res) }()
			} else {
				f.camID, f.camName, f.stream = open.CamID, name, open.Stream
			}
		})
	}()
}

func (f *factoryMode) untouched() int {
	n := 0
	for _, d := range f.cells {
		if !d {
			n++
		}
	}
	return n
}

// factoryTouch marks the square under a finger; every square touched
// passes the check.
func (c *Core) factoryTouch(phase string, x, y float64) {
	f := c.factory
	if f.screen != "touch" || phase == "cancel" {
		return
	}
	d := c.screenDev
	s := d.Scale
	if s <= 0 {
		s = 1
	}
	w, h := float64(d.Width)/s, float64(d.Height)/s
	cols, rows := f.touchGrid(w, h)
	col, row := int(x/w*float64(cols)), int(y/h*float64(rows))
	if col < 0 || row < 0 || col >= cols || row >= rows {
		return
	}
	if i := row*cols + col; !f.cells[i] {
		f.cells[i], f.moved = true, c.now()
	}
	if f.untouched() == 0 {
		c.factoryRecord(f, "touch", factory.Pass, fmt.Sprintf("%d squares", len(f.cells)), "")
	}
}

// touchGrid lays the squares across a landscape panel the other way.
func (f *factoryMode) touchGrid(w, h float64) (cols, rows int) {
	if w > h {
		return factoryTouchRows, factoryTouchCols
	}
	return factoryTouchCols, factoryTouchRows
}

// factoryLink opens the USB link to the station without the trust prompt,
// factory mode only: DHCP and the runner's port, nothing else.
func (c *Core) factoryLink(on bool) {
	f := c.factory
	if on == f.usb {
		return
	}
	f.usb = on
	l := netfw.Link{Name: usbgadget.Interface(c.usb.cfg)}
	if on {
		l.UDP, l.TCP = []uint16{67}, []uint16{factory.Port}
	}
	c.applyLink(l)
	if !c.usb.sim {
		if err := c.usbNetwork(on); err != nil {
			c.log.Printf("factory mode: usb: %v", err)
		}
	}
	c.log.Printf("factory mode: the station's USB link %s", map[bool]string{true: "up (udp 67, tcp 7171)", false: "down"}[on])
}

// factoryPattern is the full-panel pattern showing, if any.
func (c *Core) factoryPattern() *FramePattern {
	f := c.factory
	switch {
	case f == nil:
	case f.screen == "display.patterns" && f.pattern >= 0 && f.pattern < len(factoryPatterns):
		p := factoryPatterns[f.pattern]
		return &p
	case f.screen == "touch":
		d := c.screenDev
		cols, rows := f.touchGrid(float64(d.Width), float64(d.Height))
		p := FramePattern{Color: "#000000", Grid: [2]int{cols, rows}, Line: "#808080", Mark: "#2e7d32"}
		for i, done := range f.cells {
			if done {
				p.Done = append(p.Done, i)
			}
		}
		return &p
	}
	return nil
}

// factoryView is the factory screen as an overlay's parts: its title,
// lines, the home screen's rows, note and footer.
func (c *Core) factoryView() (title string, lines []string, items []FrameMenuItem, note string, bad bool, footer []string) {
	f := c.factory
	title = tr.T("Factory test")
	pass := tr.T("OK passed · Back failed")
	if f.buttons() {
		pass = tr.T("Vol+ passed · Vol− failed")
	}
	if f.err != "" {
		note, bad = tr.T("The test runner does not answer: %s", f.err), true
	}
	if f.plan == nil {
		return title, []string{tr.T("Waiting for the test runner…")}, nil, note, bad, nil
	}
	if f.screen != "" && f.screen != "provisioned" {
		title = tr.T("Factory test · %s", factoryTitle(f.screen))
	}
	switch f.screen {
	case "":
		lines = []string{tr.T("SKU %s", f.plan.SKU.ID)}
		if f.usb {
			lines = append(lines, tr.T("USB: the station's link is up"))
		}
		for i, id := range f.homeItems() {
			text := factoryTitle(id)
			if r, ok := f.done[id]; ok {
				text = resultMark(r.Result) + " " + text
			} else if id != "auto" && id != "erase" {
				text = resultMark("") + " " + text
			}
			items = append(items, FrameMenuItem{Text: text, Sel: i == f.sel})
		}
		footer = []string{tr.T("OK open · ↑↓ move")}
		if f.buttons() {
			footer = []string{tr.T("Power open · Vol+ Vol− move")}
		}
	case "keys":
		ch, _ := f.check("keys")
		left := f.keysLeft(ch)
		lines = []string{tr.T("Press each key once."), tr.T("%d of %d keys pressed", len(ch.Keys)-len(left), len(ch.Keys)),
			tr.T("Left: %s", strings.Join(left, " "))}
		if f.last != "" {
			note = tr.T("Last: %s", f.last)
		}
		footer = []string{tr.T("Fails after %d s without a new key", int(factoryIdle/time.Second))}
	case "display.patterns":
		if f.pattern < len(factoryPatterns) {
			lines = []string{tr.T("Red, green, blue, white, black and a grid fill the screen in turn; any key shows the next.")}
			footer = []string{tr.T("OK start · Back home")}
			if f.buttons() {
				footer = []string{tr.T("Power start · Vol− home")}
			}
		} else {
			lines, footer = []string{tr.T("Did every pattern fill the screen evenly, with no dead or stuck pixels?")}, []string{pass}
		}
	case "touch":
		lines = []string{tr.T("Touch every square until it turns green."), tr.T("%d of %d squares", len(f.cells)-f.untouched(), len(f.cells))}
	case "speaker":
		lines, footer = []string{tr.T("A tone is playing: does the speaker sound clear?")}, []string{pass}
	case "microphone":
		lines = []string{tr.T("A tone plays through the speaker while the microphone listens.")}
		switch {
		case f.busy:
			lines = append(lines, tr.T("Listening…"))
		case f.note == "":
			footer = []string{tr.T("OK measure · Back home")}
			if f.buttons() {
				footer = []string{tr.T("Power measure · Vol− home")}
			}
		default:
			note, bad = f.note, f.bad
			footer = []string{tr.T("Any key: home")}
		}
	case "vibrator.felt":
		lines, footer = []string{tr.T("Did the phone vibrate?")}, []string{pass}
	case "camera.back", "camera.front":
		switch {
		case f.busy:
			lines = []string{tr.T("Opening the camera…")}
		case f.stream == "":
			lines, note, bad = []string{tr.T("Skipped")}, f.note, true
			footer = []string{tr.T("Any key: home")}
		default:
			lines, footer = []string{tr.T("Does the picture show what the camera sees?")}, []string{pass}
		}
	case "auto":
		if f.busy || f.auto == nil {
			lines = []string{tr.T("Running…")}
			break
		}
		var p, fl, sk int
		for _, r := range f.auto.Checks {
			switch r.Result {
			case factory.Pass:
				p++
			case factory.Fail:
				fl++
			default:
				sk++
			}
		}
		lines = []string{tr.T("%d passed, %d failed, %d skipped", p, fl, sk)}
		for _, r := range f.auto.Checks {
			if r.Result == factory.Fail || r.Result == factory.Pass {
				lines = append(lines, resultMark(r.Result)+" "+r.ID+": "+cmp.Or(r.Reason, r.Value))
			}
		}
		footer = []string{tr.T("Any key: home")}
	case "erase":
		lines = []string{tr.T("Erase this phone and restart? Everything on it goes, but for its identity.")}
		footer = []string{tr.T("OK erase · Back cancel")}
		if f.buttons() {
			footer = []string{tr.T("Power erase · Vol− cancel")}
		}
	case "provisioned":
		left := max(0, int(f.offAt.Sub(c.now()).Round(time.Second)/time.Second))
		lines = []string{tr.T("This phone has its certificate. It powers off in %d s.", left)}
		footer = []string{tr.T("OK power off now")}
	}
	return title, lines, items, note, bad, footer
}

// factoryFrame is the factory screen for the pixel renderer: an empty page
// under it, the screen as an overlay (a capture's on the camera check),
// or a full-panel pattern alone.
func (c *Core) factoryFrame(fr *Frame) {
	fr.Screen, fr.Title, fr.Body, fr.Focus, fr.Soft = "sys/factory", tr.T("Factory test"), []FrameNode{}, -1, [3]string{}
	fr.Loading, fr.Skeleton, fr.Idle, fr.Keypad, fr.IME, fr.Toast = "", false, nil, nil, nil, ""
	if p := c.factoryPattern(); p != nil {
		fr.Pattern = p
	}
	title, lines, items, note, bad, footer := c.factoryView()
	kind := "confirm"
	switch {
	case len(items) > 0:
		kind = "menu"
		head := make([]FrameMenuItem, 0, len(lines)+len(items))
		for _, l := range lines {
			head = append(head, FrameMenuItem{Header: true, Text: l})
		}
		items = append(head, items...)
		lines = nil
	case c.factory.stream != "":
		kind = "capture"
	}
	o := &FrameOverlay{Kind: kind, Title: title, Lines: lines, Items: items, Note: note, NoteBad: bad, Footer: footer, Phase: -1, Choice: -1}
	if kind == "capture" {
		o.Image = c.frameStream(c.factory.stream, mediaW, mediaW, false)
	}
	fr.Overlay = o
}

// renderFactory draws the factory screen on the text renderer: the whole
// screen below the status bar, or a pattern on all of it.
func (c *Core) renderFactory(can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	if p := c.factoryPattern(); p != nil {
		bg := hexRGB(p.Color)
		can.Fill(0, 0, W, H, st(bg, bg))
		if p.Grid[0] > 0 {
			ls := st(hexRGB(p.Line), bg)
			mark := st(hexRGB(p.Mark), hexRGB(p.Mark))
			// Evenly spaced cells, two cells high at least: a grid finer
			// than the canvas would draw lines on every row.
			cols, rows := p.Grid[0], p.Grid[1]
			sx, sy := max(W/cols, 2), max(H/rows, 2)
			for i := 0; i < cols*rows; i++ {
				x0, y0 := i%cols*sx, i/cols*sy
				x1, y1 := min(x0+sx, W), min(y0+sy, H)
				if slices.Contains(p.Done, i) {
					can.Fill(x0, y0, x1-x0, y1-y0, mark)
				}
				for x := x0; x < x1; x++ {
					can.Text(x, y0, "─", ls, 1)
				}
				for y := y0; y < y1; y++ {
					can.Text(x0, y, "│", ls, 1)
				}
			}
		}
		return
	}
	title, lines, items, note, bad, footer := c.factoryView()
	bs := st(colFG, colTrustBG)
	can.Box(0, 1, W, H-1, st(colTrusted, colTrustBG), true)
	can.Text(2, 2, "■ "+title, bold(st(colTrusted, colTrustBG)), W-4)
	y := 4
	for _, l := range lines {
		for _, w := range factoryWrap(l, W-6) {
			if y < H-3 {
				can.Text(3, y, w, bs, W-6)
			}
			y++
		}
	}
	if c.factory.stream != "" {
		c.blitStream(can, 3, y, W-6, H-4-y-len(footer), c.factory.stream, false, tr.T("Opening the camera…"))
	}
	if len(items) > 0 {
		y++
	}
	for _, it := range items {
		sty := bs
		if it.Sel {
			sty = st(colTrustBG, colFG)
		}
		if y < H-3 {
			can.Fill(3, y, W-6, 1, sty)
			can.Text(4, y, cv.Truncate(it.Text, W-8), sty, W-8)
		}
		y++
	}
	if note != "" {
		y++
		sty := st(colAccent, colTrustBG)
		if bad {
			sty = bold(st(colDanger, colTrustBG))
		}
		for _, w := range factoryWrap(note, W-6) {
			if y < H-2-len(footer) {
				can.Text(3, y, w, sty, W-6)
			}
			y++
		}
	}
	for i, f := range footer {
		can.Text(3, H-1-len(footer)+i, cv.Truncate(f, W-6), st(colDim, colTrustBG), W-6)
	}
}

// factoryWrap wraps at spaces where it can, so key names stay whole.
func factoryWrap(s string, w int) []string {
	var out []string
	cur := ""
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case cv.StringWidth(cur+" "+word) <= w:
			cur += " " + word
		default:
			out, cur = append(out, cur), word
		}
		if cv.StringWidth(cur) > w {
			ls := cv.Wrap(cur, w)
			out, cur = append(out, ls[:len(ls)-1]...), ls[len(ls)-1]
		}
	}
	return append(out, cur)
}

func hexRGB(s string) cv.RGB {
	var r, g, b uint8
	if _, err := fmt.Sscanf(strings.TrimPrefix(s, "#"), "%02x%02x%02x", &r, &g, &b); err != nil {
		return cv.RGB{}
	}
	return cv.RGB{r, g, b}
}
