package shell

import (
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// The renderer's input lines on the OnePlus 6T, as phone-paint reports
// each input device (kms.rs, read_inputs): the side key and the volume
// keys, neither able to press OK; a Bluetooth keyboard can.
const (
	sixTPower    = `{"t":"input","name":"pm8941_pwrkey","kind":"keyboard","added":true,"ok":false}`
	sixTVolume   = `{"t":"input","name":"Volume keys","kind":"keyboard","added":true,"ok":false}`
	btKeyboard   = `{"t":"input","name":"BT Keyboard","kind":"keyboard","added":true,"ok":true}`
	btKeyboardRm = `{"t":"input","name":"BT Keyboard","kind":"keyboard","added":false,"ok":true}`
)

// menuCore is powerCore with somewhere to keep settings (the volume keys
// set the volume).
func menuCore(t *testing.T) *Core {
	c := powerCore(t)
	c.cfg.StateDir, c.cfg.RunDir = t.TempDir(), t.TempDir()
	c.settings = defaultSettings()
	return c
}

// reported has the renderer report input devices as it does on the phone
// and the Shell take the reports in.
func reported(t *testing.T, c *Core, lines ...string) {
	t.Helper()
	if c.gui == nil {
		c.gui = &GUI{c: c, done: make(chan struct{})}
	}
	g := c.gui
	g.done = make(chan struct{})
	g.read(strings.NewReader(strings.Join(lines, "\n")+"\n"), c.frameVersions())
	for len(c.ev) > 0 {
		(<-c.ev)()
	}
}

// powerMenu is the power menu if it is the top overlay.
func powerMenu(c *Core) (*menuOverlay, bool) {
	m, ok := overlayOf[*menuOverlay](c)
	if !ok || m.title != tr.T("Power") {
		return nil, false
	}
	return m, true
}

// selected is the label of the power menu's highlighted entry.
func selected(m *menuOverlay) string {
	vis := m.visible()
	if m.sel < 0 || m.sel >= len(vis) {
		return ""
	}
	return vis[m.sel].label
}

// A short press of the power key over the power menu closes it and leaves
// the screen on; the next press turns the screen off as always, however
// soon. Before, the press only turned the screen off and the menu was
// still there when it came back: on a phone with no other keys nothing
// closed it.
func TestPowerMenuShortPressCloses(t *testing.T) {
	c := menuCore(t)
	c.PowerKey(true)
	if _, ok := powerMenu(c); !ok {
		t.Fatal("a long press opened no power menu")
	}
	c.PowerKey(false)
	if _, ok := powerMenu(c); ok {
		t.Error("a short press left the power menu open")
	}
	if c.ScreenOff() {
		t.Error("the press closing the power menu turned the screen off")
	}
	if c.toast != nil && c.toast.body == tr.T("Not a phone: it stays on") {
		t.Error("the press closing the power menu powered off")
	}
	c.PowerKey(false)
	if !c.ScreenOff() {
		t.Error("the press after the menu closed left the screen on")
	}
}

// The screen going off, for any reason, closes the power menu, also from
// under another overlay; the quick card view, which the power key opens
// too, closes with it and with a short press.
func TestPowerMenuClosesWithTheScreen(t *testing.T) {
	if !alone(t) {
		return
	}
	c := menuCore(t)
	c.PowerKey(true)
	c.screenOff("timeout")
	if _, ok := powerMenu(c); ok {
		t.Error("the screen went off over the power menu, which stayed")
	}

	c = menuCore(t)
	c.PowerKey(true)
	confirm := &confirmOverlay{text: "?", onYes: func() {}}
	c.pushOverlay(confirm)
	c.powerOverride = &Power{Level: 80, Simulated: true}
	c.screen.auto, c.screen.active, c.lastKey = true, c.now().Add(-time.Hour), c.now().Add(-time.Hour)
	c.checkScreen()
	if !c.ScreenOff() {
		t.Fatal("the timeout left the screen on")
	}
	for _, o := range c.overlays {
		if m, ok := o.(*menuOverlay); ok && m.title == tr.T("Power") {
			t.Error("the timeout left the power menu under a dialog")
		}
	}
	if top, _ := overlayOf[*confirmOverlay](c); top != confirm {
		t.Error("the dialog over the power menu went with it")
	}

	quick := func(c *Core) {
		t.Helper()
		c.PowerKey(false)
		c.PowerKey(false)
		time.Sleep(doublePress + 50*time.Millisecond)
		if got := quickView(t, c); len(got) != 2 {
			t.Fatalf("a double press: quick view %q", got)
		}
	}
	c = menuCore(t)
	quick(c)
	c.PowerKey(false)
	if got := quickView(t, c); got != nil || c.ScreenOff() {
		t.Errorf("a press over the quick view: still open %v, screen off %v", got != nil, c.ScreenOff())
	}
	c = menuCore(t)
	quick(c)
	c.screenOff("timeout")
	if got := quickView(t, c); got != nil {
		t.Error("the screen went off over the quick view, which stayed")
	}
}

// On a phone of buttons only (the 6T without a keyboard) the power menu
// is answered with them: the volume keys move its highlight (not the
// volume), a short press of the power key chooses the entry highlighted,
// Cancel when it opens.
func TestPowerMenuButtonsOnly(t *testing.T) {
	c := menuCore(t)
	reported(t, c, sixTPower, sixTVolume)
	vol := c.settings.RingVolume

	c.PowerKey(true)
	m, ok := powerMenu(c)
	if !ok {
		t.Fatal("a long press opened no power menu")
	}
	if m.sel != 0 || selected(m) != tr.T("Cancel") {
		t.Errorf("the power menu opens on %q, not Cancel", selected(m))
	}
	if vis := m.visible(); len(vis) == 0 || vis[0].group != tr.T("Volume keys move · power key chooses") {
		t.Error("the power menu does not say how the buttons answer it")
	}
	c.Press(Named(KeyVolDown))
	if m.sel != 1 || selected(m) != tr.T("Power off") {
		t.Errorf("volume down: on %q", selected(m))
	}
	c.Press(Named(KeyVolUp))
	if m.sel != 0 {
		t.Errorf("volume up: on %q", selected(m))
	}
	if c.settings.RingVolume != vol {
		t.Errorf("the volume keys in the power menu set the volume: %v, was %v", c.settings.RingVolume, vol)
	}
	c.PowerKey(false)
	if _, ok := powerMenu(c); ok || c.ScreenOff() {
		t.Errorf("Cancel: the menu still open %v, screen off %v", ok, c.ScreenOff())
	}
	if c.toast != nil && c.toast.body == tr.T("Not a phone: it stays on") {
		t.Error("Cancel powered off")
	}

	c.Wake("test")
	c.Key(Named(KeyEsc))
	c.toast = nil
	c.nfc.powerAt = time.Time{}
	c.PowerKey(true)
	if _, ok := powerMenu(c); !ok {
		t.Fatal("a long press opened no power menu")
	}
	c.Press(Named(KeyVolDown))
	c.PowerKey(false)
	if c.toast == nil || c.toast.body != tr.T("Not a phone: it stays on") {
		t.Errorf("volume down, then the power key: no power off (%+v)", c.toast)
	}
	if _, ok := powerMenu(c); ok {
		t.Error("choosing Power off left the menu open")
	}

	// Without the menu the volume keys set the volume again.
	c.Wake("test")
	c.Key(Named(KeyEsc))
	vol = c.settings.RingVolume
	c.Press(Named(KeyVolDown))
	if c.settings.RingVolume == vol {
		t.Error("volume down outside the power menu left the volume")
	}
}

// With a keyboard that has OK (or a phone with a keypad) the power key
// never chooses from the power menu: a short press closes it whatever is
// highlighted, and the volume keys keep setting the volume. Unplugged,
// the phone is one of buttons only again.
func TestPowerMenuWithAKeyboard(t *testing.T) {
	c := menuCore(t)
	reported(t, c, sixTPower, sixTVolume, btKeyboard)

	c.PowerKey(true)
	m, ok := powerMenu(c)
	if !ok {
		t.Fatal("a long press opened no power menu")
	}
	c.Key(Named(KeyDown))
	on := m.sel
	c.Press(Named(KeyVolDown))
	if m.sel != on {
		t.Errorf("with a keyboard volume down moved the highlight to %q", selected(m))
	}
	c.PowerKey(false)
	if c.toast != nil && c.toast.body == tr.T("Not a phone: it stays on") {
		t.Error("with a keyboard the power key powered off from the menu")
	}
	if _, ok := powerMenu(c); ok || c.ScreenOff() {
		t.Errorf("with a keyboard a short press: the menu still open %v, screen off %v", ok, c.ScreenOff())
	}

	// A renderer that does not say whether a device has OK: taken as yes.
	c2 := menuCore(t)
	reported(t, c2, `{"t":"input","name":"pm8941_pwrkey","kind":"keyboard","added":true}`)
	c2.PowerKey(true)
	c2.Press(Named(KeyVolDown))
	c2.PowerKey(false)
	if c2.toast != nil && c2.toast.body == tr.T("Not a phone: it stays on") {
		t.Error("a device of unknown keys made the power key choose")
	}

	c.Wake("test")
	c.Key(Named(KeyEsc))
	reported(t, c, btKeyboardRm)
	c.nfc.powerAt = time.Time{}
	c.PowerKey(true)
	m, _ = powerMenu(c)
	c.Press(Named(KeyVolDown))
	if m == nil || m.sel != 1 {
		t.Error("the keyboard unplugged: volume down left the highlight")
	}
	c.Key(Named(KeyEsc))
}

// A case choosing Power off runs on the developer machine only: on a
// phone or the emulator (an SKU's id) it would power off.
func TestRequireDevMachine(t *testing.T) {
	if !alone(t) {
		return
	}
	script := "== power off\nrequire-dev-machine\nsleep 10\n"
	r := &Runner{C: snapCore(t), Out: &strings.Builder{}}
	if !r.Run(script) || len(r.Cases) != 1 || r.Cases[0].Status != "PASS" {
		t.Fatalf("on the developer machine: %+v", r.Cases)
	}
	c := snapCore(t)
	c.cfg.SKU.ID = "qemu-t9"
	r = &Runner{C: c, Out: &strings.Builder{}}
	if !r.Run(script) || len(r.Cases) != 1 || r.Cases[0].Status != "SKIP" {
		t.Fatalf("on a phone: %+v", r.Cases)
	}
}

// Where the end key is the power key (no power key of the board's own),
// its short press over the power menu only closes the menu: the page
// under it stays, and the next press is a press of its own.
func TestPowerMenuEndKey(t *testing.T) {
	c := menuCore(t)
	c.sys.stack = append(c.sys.stack, newView(ui.Screen{ID: "sys.notifs"}))
	c.Key(Key{Name: KeyF4, Long: true})
	if _, ok := powerMenu(c); !ok {
		t.Fatal("holding the end key opened no power menu")
	}
	c.Key(Named(KeyF4))
	if _, ok := powerMenu(c); ok {
		t.Error("the end key left the power menu open")
	}
	if n := len(c.sys.stack); n != 2 || c.ScreenOff() {
		t.Errorf("the end key closing the power menu: %d pages left, screen off %v", n, c.ScreenOff())
	}
}
