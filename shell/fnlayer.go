package shell

import (
	"strings"
	"unicode"

	"github.com/kario-os/kario/ui"
)

// A full keyboard shares its keys between letters and digits (the SKU's Fn
// layer: qwerty-v1 has the digits on W E R / S D F / Z X C and 0 on the full
// stop). As on Nokia's QWERTY phones, what a key types follows the field:
// a number or phone field, or a PIN, takes the key's Fn character without
// Fn (S60's numeric input mode); the standby screen searches by the letters
// and the call key dials the digits behind them; text fields keep the
// letters, and Fn gives the digits: held, tapped for the next key, or
// tapped twice to lock (the renderer keeps that state and says so, below).

const dialChars = "0123456789*#+"

// The renderer's Fn state, sent as key names: tapped once (the next key
// types its Fn character), locked, or back to letters.
const (
	KeyFnOnce = "fn-once"
	KeyFnLock = "fn-lock"
	KeyFnOff  = "fn-off"
)

const (
	fnOnce = "once"
	fnLock = "lock"
)

var fnModes = map[string]string{KeyFnOnce: fnOnce, KeyFnLock: fnLock, KeyFnOff: ""}

// Shift as Nokia's QWERTY phones have it, kept by the renderer the same
// way: tapped once the next letter is a capital, twice caps lock.
const (
	KeyShiftOnce = "shift-once"
	KeyShiftLock = "shift-lock"
	KeyShiftOff  = "shift-off"
)

var shiftModes = map[string]string{KeyShiftOnce: fnOnce, KeyShiftLock: fnLock, KeyShiftOff: ""}

// longFn: a full keyboard's letter held in a text field types its Fn
// character instead (the E71's other way to its digits). The renderer has
// typed the letter at the press (Retype), so it goes first; a terminal's
// Alt+key or a script's long press has typed nothing. Elsewhere the press
// already did what the key does, and the long press is dropped.
func (c *Core) longFn(k Key) bool {
	if len(c.cfg.SKU.Fn) == 0 || len(k.Runes) != 1 {
		return false
	}
	fn := c.fnRunes(k.Runes)
	if fn[0] == k.Runes[0] {
		return false
	}
	_, n := c.imeTarget()
	if n == nil {
		if v := c.current().top(); v != nil && len(c.overlays) == 0 {
			if f := v.focused(); f != nil && f.Kind == ui.KindInput && f.Secret && !numericField(f.Keyboard) {
				n = f
			}
		}
	}
	if n == nil {
		return k.Retype
	}
	if k.Retype {
		c.Key(Named(KeyBackspace))
	}
	c.Key(Runes(string(fn)))
	return true
}

// fnRunes is what rs type in a field that takes numbers: each key's Fn
// character where the keyboard has one (the layer is keyed by what the keys
// type unshifted), the others as they are.
func (c *Core) fnRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		if fn := []rune(c.cfg.SKU.Fn[string(unicode.ToLower(r))]); len(fn) == 1 {
			r = fn[0]
		}
		out[i] = r
	}
	return out
}

// SetFnLayer sets the keyboard's Fn layer (a script's; the SKU's otherwise).
func (c *Core) SetFnLayer(layer map[string]string) { c.cfg.SKU.Fn = layer }

// numericField: the input takes numbers (its keys type their Fn
// characters), date and time fields too.
func numericField(keyboard string) bool {
	return keyboard == ui.KeyboardNumber || keyboard == ui.KeyboardPhone || dateField(keyboard)
}

// dialFor is the number the letters typed on the standby screen stand for
// on a keyboard with an Fn layer, "" when some key has no dial character
// behind it or there is no layer.
func (c *Core) dialFor(query string) string {
	if len(c.cfg.SKU.Fn) == 0 || query == "" {
		return ""
	}
	d := string(c.fnRunes([]rune(query)))
	if strings.Trim(d, dialChars) != "" {
		return ""
	}
	return d
}

// inputMode is the status bar's input mode for the focused field: the
// IME's, or Fn or Shift tapped or locked in a text field, Abc where the
// next letter is a capital of itself (abc after Shift took it back); "123"
// in a number field of a keyboard with an Fn layer, as Nokia showed it.
func (c *Core) inputMode() string {
	if v, n := c.imeTarget(); n != nil {
		switch c.fnMode {
		case fnOnce:
			return "Fn"
		case fnLock:
			return "123"
		}
		switch {
		case c.shiftMode == fnOnce && c.capsOff:
			return "abc"
		case c.shiftMode == fnOnce:
			return "Abc"
		case c.shiftMode == fnLock:
			return "ABC"
		case c.autoCap(v, n):
			return "Abc"
		}
		return c.ime.Indicator()
	}
	if len(c.cfg.SKU.Fn) == 0 || len(c.overlays) > 0 {
		return ""
	}
	if v := c.current().top(); v != nil {
		if n := v.focused(); n != nil && n.Kind == ui.KindInput && numericField(n.Keyboard) {
			return "123"
		}
	}
	return ""
}
