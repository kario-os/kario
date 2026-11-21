package shell

import (
	"os/exec"
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
}

func Named(n string) Key { return Key{Name: n} }

func Runes(s string) Key { return Key{Name: KeyRunes, Runes: []rune(s)} }

func LongRunes(s string) Key { return Key{Name: KeyRunes, Runes: []rune(s), Long: true} }

// Press is a key from a renderer, the terminal or a script: the power key
// goes to PowerKey, the others to Key, after the key press feedback (keys
// Key replays later are not pressed again).
func (c *Core) Press(k Key) {
	if k.Name == KeyPower {
		c.PowerKey(k.Long)
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
// lock follows as for any screen off) or lights it; a second press within
// doublePress lights it and opens the quick card view, as the side key's
// double press opens the wallet on Xiaomi's phones and the iPhone; a long
// press opens the power menu. While a call rings a press silences it, as
// the volume keys do.
func (c *Core) PowerKey(long bool) {
	c.lockTick()
	c.lastKey = c.now()
	now := time.Now()
	double := !c.nfc.powerAt.IsZero() && now.Sub(c.nfc.powerAt) <= doublePress
	c.nfc.powerAt = now
	switch {
	case long:
		c.nfc.powerAt = time.Time{}
		c.Wake("key")
		c.openPowerMenu()
	case c.ringing():
		c.Wake("key")
		c.silenceRing()
		c.showToast(tr.T("Silenced"))
	case double && c.cardViewOK():
		c.nfc.powerAt = time.Time{}
		c.Wake("key")
		c.openCardPick()
	case c.screen.off:
		c.Wake("key")
	default:
		c.screenOff("power key")
		c.lockTick()
	}
	c.syncNFC()
}

// openPowerMenu is the power key's long press (the end key's where there
// is no power key): power off or restart.
func (c *Core) openPowerMenu() {
	if n := len(c.overlays); n > 0 {
		if m, ok := c.overlays[n-1].(*menuOverlay); ok && m.title == tr.T("Power") {
			return
		}
	}
	c.pushOverlay(&menuOverlay{title: tr.T("Power"), entries: []menuEntry{
		{label: tr.T("Power off"), key: "1", run: func() { c.powerOff("poweroff") }},
		{label: tr.T("Restart"), key: "2", run: func() { c.powerOff("reboot") }},
		{label: map[bool]string{false: tr.T("Turn airplane mode on"), true: tr.T("Turn airplane mode off")}[c.settings.Airplane], key: "3", run: c.toggleAirplane},
	}})
}

// powerOff asks systemd to power off or reboot, on a phone only: a
// developer machine (no SKU) is not the shell's to switch off.
func (c *Core) powerOff(how string) {
	if c.cfg.SKU.ID == "" {
		c.log.Printf("power: %s skipped (not a phone)", how)
		c.showToast(tr.T("Not a phone: it stays on"))
		return
	}
	c.log.Printf("power: %s", how)
	go func() { _ = exec.Command("systemctl", how).Run() }()
}
