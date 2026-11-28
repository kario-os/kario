package shell

import (
	"fmt"
	"os/exec"
	"slices"
	"time"
)

// Key names follow the physical phone keys (docs/10 §1). The terminal
// frontend maps PC keys onto them: Enter=OK, Esc=Back, Tab=Menu/Options,
// F2=right softkey, F3=task switcher, F4=home, "/"=command palette.
const (
	KeyUp        = "up"
	KeyDown      = "down"
	KeyLeft      = "left"
	KeyRight     = "right"
	KeyEnter     = "enter"
	KeyEsc       = "esc"
	KeyTab       = "tab"
	KeyBackspace = "backspace"
	KeyF2        = "f2"
	KeyF3        = "f3"
	// KeyF4 is the end (red) key: it hangs up the call on screen, else goes
	// back to idle. Where the board has no power key it is the power key
	// too, the Nokia way: held, the power menu; pressed twice on standby,
	// the quick card view (cards.go).
	KeyF4 = "f4"
	// KeyCall is the call (green) key: answers, dials, opens recent calls.
	KeyCall = "call"
	// KeyPower is a power key apart from the end key, where the board has
	// one (KEY_POWER). It goes to PowerKey, not Key.
	KeyPower = "power"
	// KeyVolUp and KeyVolDown are the side volume keys.
	KeyVolUp   = "volup"
	KeyVolDown = "voldown"
	// Media keys: a headset's button, a keyboard's play / next / previous.
	KeyPlayPause = "playpause"
	KeyNextTrack = "next"
	KeyPrevTrack = "previous"
	// KeyIME switches input mode (Chinese / English): Ctrl+Space on a
	// keyboard (F6 in a terminal); the 9-key pad uses "#".
	KeyIME = "ime"
	// KeySym is Chr / Sym on a keyboard (F7 in a terminal): the symbol
	// table. The 9-key pad uses "*".
	KeySym   = "sym"
	KeyRunes = "runes"
	// KeyA11y is the accessibility shortcut: both volume keys pressed
	// together (Alt + F8 in a terminal); held, it reads the focused item
	// (a11y.go).
	KeyA11y = "a11y"
)

// A keyboard's editing keys (textedit.go), made by the renderer from
// combinations (Fn + arrows, Ctrl + letters, Shift + Chr; the keyboard
// configuration says how Ctrl is pressed) or a PC keyboard's own keys. The
// 9-key pad has none of them.
const (
	// KeyFirst and KeyLast: Fn + ← / → (Home, End): the start or end of
	// the text in a field, the first or last item elsewhere. "end" is
	// already the end (red) key's name.
	KeyFirst = "first"
	KeyLast  = "last"
	// KeyPageUp and KeyPageDown: Fn + ↑ / ↓ (Page Up, Page Down).
	KeyPageUp   = "pageup"
	KeyPageDown = "pagedown"
	// KeyDelete deletes after the caret: Shift or Fn + Backspace, Delete.
	KeyDelete = "delete"
	KeyCopy   = "copy"
	KeyCut    = "cut"
	KeyPaste  = "paste"
	// KeySelectAll is Ctrl+A, KeyUndo Ctrl+Z.
	KeySelectAll = "selectall"
	KeyUndo      = "undo"
	// KeyLang is Shift + Chr (E90) or Fn + Space (E71): the writing
	// language menu.
	KeyLang = "lang"
)

type Key struct {
	Name  string
	Runes []rune
	// Long marks a held key (Alt+key in a terminal). Retype: the renderer
	// typed the key's character at the press already, and the long press
	// replaces it (fnlayer.go).
	Long   bool
	Retype bool
	// Shift: an arrow, first or last with Shift held, which selects text.
	Shift bool
	// Chr: a letter pressed with Chr held, the nth time in a row: its
	// variants in turn, each replacing the one before (é, è, ê, ...).
	Chr int
	// At is when it was pressed: set by a source that queues keys, else by
	// Key. A key held and replayed keeps it (input.go).
	At time.Time
	// Injected: the computer sent it (kario device key, type), not the
	// person holding the phone; trust dialogs and the lock refuse it
	// (input.go). A key held and replayed keeps it.
	Injected bool
	// Held: how long the power key was down, where the renderer says.
	Held time.Duration
	// Dev: the renderer's number of the device it came from (0: none
	// said); one plugged in while locked types only into the lock screen
	// (inputlock.go).
	Dev int
}

func Named(n string) Key { return Key{Name: n} }

func Runes(s string) Key { return Key{Name: KeyRunes, Runes: []rune(s)} }

func LongRunes(s string) Key { return Key{Name: KeyRunes, Runes: []rune(s), Long: true} }

// Press is a key from a renderer, the terminal or a script: the power key
// goes to PowerKey, the others to Key, after the key press feedback (keys
// Key replays later are not pressed again).
func (c *Core) Press(k Key) {
	if c.factory != nil {
		c.factoryPress(k)
		return
	}
	if c.lockedOut(k) {
		return
	}
	if k.Name == KeyPower {
		c.powerPressed(k)
		return
	}
	if c.lidDrops(k) {
		return
	}
	if mode, ok := fnModes[k.Name]; ok {
		c.fnMode = mode
		return
	}
	if mode, ok := shiftModes[k.Name]; ok {
		c.shiftKey(mode)
		return
	}
	c.keyFeedback()
	c.Key(k)
}

// PowerKey is a press of the power key. Unlike the other keys it does not
// light the screen first (Key does): a press turns the screen off (the
// lock follows as for any screen off) or lights it; a double press opens
// the quick card view, as the side key's double press opens the wallet on
// Xiaomi's phones and the iPhone; a long press opens the power menu.
// While a call rings a press silences it, as the volume keys do; while an
// alarm rings a press snoozes it, as on Android (held, the key still opens
// the power menu, over the alarm); in a call, with the screen on, a press
// hangs up where Power key ends call is on (powerEndsCall, Android's
// "Power button ends call"). Under a flip's shut lid it does only
// the ring's and the alarm's (powerShut). The renderer tells the short press
// from the long one (powerLong); each press goes into the log with what
// it did.
//
// A double press is a press after a pause and a second one within
// doublePress, with no third within doublePress after it: the quick view
// opens then. Pressed again and again, the key only turns the screen off
// and on, ending as the last press leaves it: the second press lights a
// dark screen at once (the quick view needs it lit), and leaves a lit one
// on until it is known whether a third press follows, which then undoes
// the off it would have been.
func (c *Core) PowerKey(long bool) { c.powerPressed(Key{Name: KeyPower, Long: long}) }

func (c *Core) powerPressed(k Key) {
	c.lockTick()
	c.lastKey = c.now()
	now := time.Now()
	burst := !c.nfc.powerAt.IsZero() && now.Sub(c.nfc.powerAt) <= doublePress
	start := c.nfc.powerStart
	c.nfc.powerAt, c.nfc.powerStart = now, !burst
	due, held := c.nfc.cardsDue, c.nfc.cardsHeld
	c.nfc.cardsDue = 0
	what := "short press"
	if k.Long {
		what = "long press"
	}
	if k.Held > 0 {
		what += fmt.Sprintf(" (held %d ms)", k.Held.Milliseconds())
	}
	did := ""
	menu := c.powerKeyMenu()
	switch {
	case c.lidShut() && c.screen.off:
		c.nfc.powerAt = time.Time{}
		did = c.powerShut()
	case k.Long:
		c.nfc.powerAt = time.Time{}
		c.Wake("key")
		c.openPowerMenu()
		did = "the power menu"
	case c.ringing():
		c.Wake("key")
		c.silenceRing("the power key")
		c.showToast(tr.T("Silenced"))
		did = "the ring silenced"
	case menu != nil:
		// The press closing the menu starts no double press: the next
		// one turns the screen off.
		c.nfc.powerAt = time.Time{}
		c.Wake("key")
		did = c.powerMenuPress(menu)
	case c.alarmOnTop() != nil:
		c.nfc.powerAt = time.Time{}
		c.Wake("key")
		c.alarmOnTop().snooze(c)
		did = "the alarm snoozed"
	case c.powerHangsUp():
		c.nfc.powerAt = time.Time{}
		did = "the call hung up"
	case due != 0 && burst && held:
		did = "screen stays on (a third press: with the one before, off and on again)"
	case burst && start && c.cardViewOK():
		c.nfc.cardsN++
		c.nfc.cardsDue, c.nfc.cardsHeld = c.nfc.cardsN, !c.screen.off
		n := c.nfc.cardsN
		time.AfterFunc(doublePress, func() {
			c.Post(func() {
				if c.nfc.cardsDue == n {
					c.nfc.cardsDue = 0
					c.Wake("key")
					c.openCardPick()
					c.screenLog("power key: a double press: the quick card view")
				}
			})
		})
		c.Wake("key")
		did = "screen on; the quick card view unless another press follows"
	case c.screen.off:
		c.Wake("key")
		did = "screen on"
	default:
		c.screenOff("power key")
		c.lockTick()
		did = "screen off"
	}
	c.screenLog("power key: %s: %s (the panel %s%s)", what, did, onOff(c.screen.panel), c.panelWhy())
	c.syncNFC()
}

// powerLongMin: however short the long press is set (Settings ›
// Accessibility), the power key's opens the power menu no sooner: a slow
// tap of the side key, the thing most often done with it, must stay a
// tap. Android's global actions wait 500 ms as well.
const powerLongMin = 500 * time.Millisecond

// powerLong: how long the power key is held for its long press, the long
// press set in Settings › Accessibility (600 ms by default) but no less
// than powerLongMin.
func (c *Core) powerLong() time.Duration {
	return max(time.Duration(c.settings.A11y.LongPress)*time.Millisecond, powerLongMin)
}

// openPowerMenu is the power key's long press (the end key's where there
// is no power key): power off or restart. Cancel comes first and is
// chosen when it opens, so that OK, or the power key on a phone of
// buttons only, closes it until another entry is chosen.
func (c *Core) openPowerMenu() {
	if n := len(c.overlays); n > 0 {
		if m, ok := c.overlays[n-1].(*menuOverlay); ok && m.power {
			return
		}
	}
	m := &menuOverlay{title: tr.T("Power"), power: true}
	m.entries = []menuEntry{
		{label: tr.T("Cancel"), run: func() {}},
		{label: tr.T("Power off"), key: "1", run: func() { c.powerChoice(m, "poweroff") }},
		{label: tr.T("Restart"), key: "2", run: func() { c.powerChoice(m, "reboot") }},
		{label: map[bool]string{false: tr.T("Turn airplane mode on"), true: tr.T("Turn airplane mode off")}[c.settings.Airplane], key: "3", run: c.toggleAirplane},
	}
	if c.buttonsOnly() {
		for i := range m.entries {
			m.entries[i].group = tr.T("Volume keys move · power key chooses")
		}
	}
	c.pushOverlay(m)
}

// powerChoice is Power off or Restart (how) chosen from the power menu m.
// Chosen by a tap it is asked again, in a dialog whose answers are large:
// the menu's rows are about 5 mm high on the OnePlus 6T, Restart right
// under Power off, and a finger a little off chooses the other. The keys
// choose the entry they highlighted.
func (c *Core) powerChoice(m *menuOverlay, how string) {
	if !m.tapped {
		c.powerOff(how)
		return
	}
	text, yes := tr.T("Power off the phone?"), tr.T("Power off")
	if how == "reboot" {
		text, yes = tr.T("Restart the phone?"), tr.T("Restart")
	}
	c.screenLog("power menu: %s tapped; asking again", how)
	c.pushOverlay(&confirmOverlay{text: text, yes: yes, no: tr.T("Cancel"), power: true, onYes: func() { c.powerOff(how) }})
}

// byPowerKey: the power key opened the menu: the power menu, or the
// quick card view (cards.go).
func (m *menuOverlay) byPowerKey() bool { return m.power || m.title == tr.T("Cards") }

// powerKeyMenu is the menu on top if the power key opened it.
func (c *Core) powerKeyMenu() *menuOverlay {
	n := len(c.overlays)
	if n == 0 {
		return nil
	}
	if m, ok := c.overlays[n-1].(*menuOverlay); ok && m.byPowerKey() {
		return m
	}
	return nil
}

// powerMenuPress is a short press of the power key over a menu it opened.
// On a phone of buttons only it chooses the power menu's highlighted
// entry, Cancel unless the volume keys moved it; anywhere else it closes
// the menu (as the end key's press does where it is the power key,
// cardPickKey): with keys to choose with, the power key never powers the
// phone off from the menu.
func (c *Core) powerMenuPress(m *menuOverlay) string {
	if vis := m.visible(); m.power && c.buttonsOnly() && m.sel < len(vis) {
		did := fmt.Sprintf("the power menu's entry %d of %d chosen (buttons only)", m.sel+1, len(vis))
		m.key(c, Named(KeyEnter))
		return did
	}
	m.key(c, Named(KeyEsc))
	if m.power {
		return "the power menu closed"
	}
	return "the quick card view closed"
}

// powerMenuVolume: on a phone of buttons only the volume keys move the
// power menu's highlight instead of the volume.
func (c *Core) powerMenuVolume(up bool) bool {
	m := c.powerKeyMenu()
	if m == nil || !m.power || !c.buttonsOnly() {
		return false
	}
	m.key(c, Named(map[bool]string{true: KeyUp, false: KeyDown}[up]))
	return true
}

// closePowerMenus closes the menus the power key opened, and the dialog
// asking again for the power menu's choice, wherever they are: the screen
// went off, and they are not left for the next person to pick up the
// phone.
func (c *Core) closePowerMenus() {
	for _, o := range slices.Clone(c.overlays) {
		if m, ok := o.(*menuOverlay); ok && m.byPowerKey() {
			m.key(c, Named(KeyEsc))
		}
		if d, ok := o.(*confirmOverlay); ok && d.power {
			d.key(c, Named(KeyEsc))
		}
	}
}

// inputDevices is what the renderer reported of the keyboards and mice
// connected (inputChanged): how many, and how many of them can press OK
// (a key read as Enter, a mouse's left button); kbd: the keyboards of
// those, which type (typeless); byID, each by the renderer's number
// (inputlock.go). sim: a selftest stands in for a phone of buttons only
// (SetButtonsOnly).
type inputDevices struct {
	n, ok, kbd int
	byID       map[int]*inputDevice
	sim        bool
}

// buttonsOnly: nothing connected can press OK, as on the OnePlus 6T with
// no keyboard: its own keys are the power and volume keys. Only a
// renderer that reads the keys says so; a terminal is a keyboard.
func (c *Core) buttonsOnly() bool {
	in := c.inputs
	return in.sim || in.n > 0 && in.ok == 0
}

// SetButtonsOnly stands in for a phone of buttons only (selftest); false
// goes back to what the renderer reported.
func (c *Core) SetButtonsOnly(on bool) { c.inputs.sim = on }

// SetPowerKey stands in for a board with a power key of its own (on) or
// one whose end key is the power key (selftest).
func (c *Core) SetPowerKey(on bool) { c.powerKey = on }

// powerOff asks systemd to power off or reboot, on a phone only: a
// developer machine (no SKU) is not the shell's to switch off. The line
// goes to the journal too: shell.log in /run goes with the power-off.
func (c *Core) powerOff(how string) {
	if c.cfg.SKU.ID == "" {
		c.screenLog("power: %s skipped (not a phone)", how)
		c.showToast(tr.T("Not a phone: it stays on"))
		return
	}
	c.screenLog("power: %s", how)
	go func() { _ = exec.Command("systemctl", how).Run() }()
}
