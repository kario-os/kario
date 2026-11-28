package shell

import (
	"fmt"
	"strings"

	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/ui"
)

// The on-screen keypad (10-keyboard-interaction-model.md §2.1): on a phone
// with a touchscreen and no keys that type, the 9-key pad the Shell draws
// under a field with the focus. Its keys are pressed as the keypad's own
// would be: the input method, multi-tap and the fields take them as they
// are.

// The keypad's layouts: the 9-key pad for text; the digits for a number
// field and a password (which the 9-key pad types digits into too); a
// phone number's with * and # (0 held is +); a date's or a time's with
// ↓ ↑, which step the part being edited.
const (
	keypadText   = "text"
	keypadDigits = "digits"
	keypadPhone  = "phone"
	keypadDate   = "date"
)

// softKeypad is the keypad's state.
type softKeypad struct {
	// hidden is the field Back put the keypad away for (keypadField's
	// owner): it stays away until the field is tapped or the focus leaves.
	hidden string
	// shown is the layout last drawn, to log when it changes.
	shown string
}

// keypadField is the field the keypad would type into: the focused input
// or web page field of the page on top with nothing over it, its layout
// and a name for it; "" when there is none or it would not take the keys
// (a text field while the input method is a keyboard's).
func (c *Core) keypadField() (layout, owner string) {
	if len(c.overlays) > 0 || c.keylock || c.anyCall() {
		return "", ""
	}
	a := c.current()
	if a == nil {
		return "", ""
	}
	v := a.top()
	if v == nil {
		return "", ""
	}
	n := v.focused()
	if n == nil {
		return "", ""
	}
	field := 0
	switch {
	case n.Kind == ui.KindWeb:
		wv := c.webviews[webKey(a, v, n)]
		if wv == nil || wv.edit == "" {
			return "", ""
		}
		field = wv.field
		layout = map[string]string{"password": keypadDigits, "number": keypadDigits, "tel": keypadPhone}[wv.edit]
	case n.Kind != ui.KindInput:
		return "", ""
	case dateField(n.Keyboard):
		layout = keypadDate
	case n.Keyboard == ui.KeyboardPhone:
		layout = keypadPhone
	case n.Keyboard == ui.KeyboardNumber || n.Secret:
		layout = keypadDigits
	}
	if layout == "" {
		if c.ime.Keypad() != ime.T9Pad {
			return "", ""
		}
		layout = keypadText
	}
	return layout, fmt.Sprintf("%p/%s/%d", v, n.ID, field)
}

// typeless: the phone has no keys that type, only a touchscreen and
// buttons; a keyboard with an OK key that is connected (a Bluetooth one)
// types, and puts the keypad away.
func (c *Core) typeless() bool {
	in := c.inputs
	return in.sim || in.n > 0 && in.kbd == 0
}

// keypadLayout is the layout the keypad shows, "" when it is not shown:
// on a renderer that takes touches, a typeless phone, for the field with
// the focus unless Back put it away there.
func (c *Core) keypadLayout() string {
	if c.gui == nil || !c.gui.info.Has("touch") || !c.typeless() {
		return ""
	}
	layout, owner := c.keypadField()
	if owner != c.keypad.hidden {
		c.keypad.hidden = ""
	}
	if layout == "" || owner == c.keypad.hidden {
		return ""
	}
	return layout
}

// keypadBack: Back with the keypad on screen puts it away, the field
// keeping the focus; the next Back goes back.
func (c *Core) keypadBack() bool {
	if c.keypadLayout() == "" {
		return false
	}
	_, c.keypad.hidden = c.keypadField()
	return true
}

// keypadRows are a layout's keys, top first: the 9-key pad with the keys
// beside it that the fields need (Backspace, ← →, OK); a gap's id is "".
func (c *Core) keypadRows(layout string) [][]FrameKey {
	sub := map[string]string{}
	if layout == keypadText {
		sub = t9Letters(c.ime.Mode())
		sub["#"] = c.ime.Indicator()
	}
	if layout == keypadPhone {
		sub["0"] = "+"
	}
	digit := func(d string) FrameKey { return FrameKey{ID: d, Label: d, Sub: sub[d]} }
	fn := func(id, label string) FrameKey { return FrameKey{ID: id, Label: label, Style: "fn"} }
	rows := [][]FrameKey{
		{digit("1"), digit("2"), digit("3"), fn(KeyBackspace, "⌫")},
		{digit("4"), digit("5"), digit("6"), fn(KeyLeft, "←")},
		{digit("7"), digit("8"), digit("9"), fn(KeyRight, "→")},
		{digit("*"), digit("0"), digit("#"), {ID: KeyEnter, Label: tr.T("OK"), Style: "ok"}},
	}
	switch layout {
	case keypadDigits:
		rows[3][0], rows[3][2] = FrameKey{}, FrameKey{}
	case keypadDate:
		rows[3][0], rows[3][2] = fn(KeyDown, "↓"), fn(KeyUp, "↑")
	}
	return rows
}

// t9Letters is what each key of the 9-key pad types in an input method's
// mode, shown under its digit.
func t9Letters(m ime.Mode) map[string]string {
	switch m {
	case ime.Num:
		return map[string]string{}
	case ime.Stroke:
		s := map[string]string{"6": "?"}
		for _, d := range "12345" {
			s[string(d)] = ime.StrokeGlyphs(string(d))
		}
		return s
	}
	s := map[string]string{"1": ".,?!", "2": "abc", "3": "def", "4": "ghi", "5": "jkl", "6": "mno", "7": "pqrs", "8": "tuv", "9": "wxyz", "0": "␣"}
	if m == ime.Abc || m == ime.Upper {
		for k, v := range s {
			s[k] = strings.ToUpper(v)
		}
	}
	return s
}

// frameKeypad is the keypad for the frame, nil when it is not shown.
func (c *Core) frameKeypad() *FrameKeypad {
	layout := c.keypadLayout()
	if layout != c.keypad.shown {
		c.keypad.shown = layout
		c.log.Printf("keypad: %q", layout)
	}
	if layout == "" {
		return nil
	}
	return &FrameKeypad{Rows: c.keypadRows(layout)}
}

// keypadPress is a touch on the keypad's key id, held or not: the key it
// is pressed. A key the keypad on screen no longer has (the frame touched
// is gone) presses nothing.
func (c *Core) keypadPress(id string, long bool, t touchState) {
	layout := c.keypadLayout()
	found := false
	for _, row := range c.keypadRows(layout) {
		for _, k := range row {
			found = found || id != "" && k.ID == id
		}
	}
	if layout == "" || !found {
		return
	}
	k := Key{Name: id, Long: long, At: t.at}
	if len([]rune(id)) == 1 {
		k.Name, k.Runes = KeyRunes, []rune(id)
		if long && id == "0" && layout == keypadPhone {
			k.Runes, k.Long = []rune("+"), false
		}
	}
	c.Press(k)
}
