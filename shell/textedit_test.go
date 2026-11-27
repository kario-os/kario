package shell

import (
	"testing"

	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/ui"
)

// fieldText is the field's value with the caret as | and the selection
// in [ ].
func fieldText(v *View, id string) string {
	e := v.edit(id)
	rs := []rune(e.text)
	from, to, sel := e.selection()
	out := ""
	for i := 0; i <= len(rs); i++ {
		if sel && i == from {
			out += "["
		}
		if i == e.caret {
			out += "|"
		}
		if sel && i == to {
			out += "]"
		}
		if i < len(rs) {
			out += string(rs[i])
		}
	}
	return out
}

func numberField(c *Core) *View {
	v := newView(ui.Screen{ID: "t", Body: []ui.Node{{Kind: ui.KindInput, ID: "f", Keyboard: "number"}}})
	c.sys.stack = []*View{v}
	return v
}

// A field's caret moves, Shift selects, typing replaces the selection,
// Backspace and Delete delete on either side, held Backspace a word,
// Select all, Cut, Paste and Undo; the app setting the value puts the
// caret back at the end.
func TestFieldEditing(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		v := numberField(c)
		step := func(want string, keys ...Key) {
			t.Helper()
			for _, k := range keys {
				c.Key(k)
			}
			if got := fieldText(v, "f"); got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		}
		shift := func(name string) Key { return Key{Name: name, Shift: true} }
		step("12345|", Runes("12345"))
		step("123|45", Named(KeyLeft), Named(KeyLeft))
		step("1239|45", Runes("9"))
		step("1[|239]45", shift(KeyLeft), shift(KeyLeft), shift(KeyLeft))
		step("1|45", Named(KeyBackspace))
		step("|145", Named(KeyFirst))
		step("|45", Named(KeyDelete))
		step("[45|]", shift(KeyLast))
		step("|45", Named(KeyLeft))
		step("[|45]", Named(KeyLast), shift(KeyUp))
		step("45|", Named(KeyRight))
		step("[45|]", Named(KeySelectAll))
		step("|", Named(KeyCut))
		if c.clipText() != "45" {
			t.Errorf("cut put %q on the clipboard", c.clipText())
		}
		step("45|", Named(KeyUndo))
		step("4545|", Named(KeyPaste))
		step("4|545", Named(KeyLeft), Named(KeyLeft), Named(KeyLeft))
		step("4[5|]45", shift(KeyRight))
		step("4|45", Named(KeyDelete))
		step("4545|", Named(KeyUndo), Named(KeyLast))
		v.inputs["f"] = "007"
		step("007|")
		return nil
	})
}

// A held Backspace deletes the word before the caret, spaces before it
// included; a Chinese character is a word of its own.
func TestDeleteWord(t *testing.T) {
	for text, want := range map[string]string{
		"hello big world": "hello big ",
		"hello big  ":     "hello ",
		"a, b.":           "a, b",
		"\u4f60\u597d":    "\u4f60",
		"x":               "",
		"":                "",
	} {
		rs := []rune(text)
		if got := string(rs[:wordStart(rs, len(rs))]); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		v := numberField(c)
		v.inputs["f"] = "12 345"
		c.Key(Key{Name: KeyBackspace, Long: true})
		if got := v.inputs["f"]; got != "12 " {
			t.Errorf("long backspace left %q", got)
		}
		return nil
	})
}

// Regression: ← in a field with text went back, out of the page, and the
// text typed was lost. It moves the caret and stops at the start; in an
// empty field ← still goes back, as outside fields.
func TestLeftStaysInTheField(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		base := newView(ui.Screen{ID: "base", Body: []ui.Node{{Kind: ui.KindItem, ID: "a", Title: "A"}}})
		v := newView(ui.Screen{ID: "t", Body: []ui.Node{{Kind: ui.KindInput, ID: "f", Value: "ab "}}})
		c.sys.stack = []*View{base, v}
		for range 5 {
			c.Key(Named(KeyLeft))
		}
		if top := c.sys.stack[len(c.sys.stack)-1]; top != v {
			t.Errorf("← left the page for %s", top.S.ID)
			return nil
		}
		if got := fieldText(v, "f"); got != "|ab " {
			t.Errorf("field %q, want |ab ", got)
		}
		v.inputs["f"] = ""
		c.Key(Named(KeyLeft))
		if top := c.sys.stack[len(c.sys.stack)-1]; top != base {
			t.Errorf("← in an empty field stayed on %s", top.S.ID)
		}
		return nil
	})
}

// Chr held with a key types its variants in turn, each replacing the one
// before; pinyin mode puts the Chinese marks first; a key without
// variants types itself. Typing goes in at the caret.
func TestChrVariants(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		v := newView(ui.Screen{ID: "t", Body: []ui.Node{{Kind: ui.KindInput, ID: "f"}}})
		c.sys.stack = []*View{v}
		chr := func(s string, n int) {
			for i := 1; i <= n; i++ {
				c.Key(Key{Name: KeyRunes, Runes: []rune(s), Chr: i})
			}
		}
		c.ime.SetMode(ime.En)
		chr("e", 2)
		chr("E", 1)
		chr("q", 1)
		chr(".", 1)
		if got := v.inputs["f"]; got != "\u00e8\u00c9q\u2026" {
			t.Errorf("English: %q", got)
		}
		v.inputs["f"] = ""
		c.ime.SetMode(ime.Zh)
		chr(".", 2)
		chr(",", 3)
		if got := v.inputs["f"]; got != "\u2026\u2026\uff0c" {
			t.Errorf("pinyin: %q, want the ellipsis then the full-width comma", got)
		}
		c.Key(Named(KeyFirst))
		chr("n", 1)
		if got := fieldText(v, "f"); got != "\u00f1|\u2026\u2026\uff0c" {
			t.Errorf("at the caret: %q", got)
		}
		return nil
	})
}

// Regression: a character the input method does not take (an accented
// letter) dropped the word being composed; it is committed first.
func TestAccentKeepsTheWord(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		v := newView(ui.Screen{ID: "t", Body: []ui.Node{{Kind: ui.KindInput, ID: "f", Keyboard: "email"}}})
		c.sys.stack = []*View{v}
		c.ime.SetMode(ime.En)
		c.Key(Runes("caf"))
		c.Key(Runes("\u00e9"))
		if got := v.inputs["f"] + c.ime.Preedit(); got != "caf\u00e9" {
			t.Errorf("got %q, want caf\u00e9", got)
		}
		return nil
	})
}

// On a keyboard in English the first letter of a field or sentence is a
// capital (the status bar says Abc); not in an email field, in pinyin, or
// on the 9-key pad; Shift tapped there makes that letter small (abc).
func TestAutoCapitals(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		show := func(keyboard string) *View {
			v := newView(ui.Screen{ID: "t", Body: []ui.Node{{Kind: ui.KindInput, ID: "f", Keyboard: keyboard}}})
			c.sys.stack = []*View{v}
			return v
		}
		typed := func(v *View, s string) string {
			for _, r := range s {
				c.Key(Runes(string(r)))
			}
			return v.inputs["f"] + c.ime.Preedit()
		}
		v := show("")
		c.ime.SetMode(ime.En)
		if m := c.inputMode(); m != "Abc" {
			t.Errorf("an empty field in English: %q, want Abc", m)
		}
		if got := typed(v, "hi. yes"); got != "Hi. Yes" {
			t.Errorf("got %q", got)
		}
		v = show("")
		c.Press(Named(KeyShiftOnce))
		if m := c.inputMode(); m != "abc" {
			t.Errorf("Shift where a capital is due: %q, want abc", m)
		}
		c.Press(Named(KeyShiftOff))
		if got := typed(v, "Iphone"); got != "iphone" {
			t.Errorf("after Shift: %q", got)
		}
		v = show("email")
		if got := typed(v, "bob"); got != "bob" {
			t.Errorf("email: %q", got)
		}
		v = show("")
		c.ime.SetMode(ime.Zh)
		if m := c.inputMode(); m == "Abc" {
			t.Errorf("pinyin: %q", m)
		}
		c.SetKeypad("t9")
		c.ime.SetMode(ime.En)
		if m := c.inputMode(); m == "Abc" {
			t.Errorf("9-key smart English: %q", m)
		}
		return nil
	})
}

// A letter in a list goes to the next item starting with it, a Chinese
// name by its pinyin; a shortcut on the letter comes first; not on the
// 9-key pad. Menus do the same.
func TestLetterJump(t *testing.T) {
	c := loopCore(t, t.TempDir())
	open := []ui.Action{{ID: ui.ActionOpen, Label: "Open"}}
	_ = c.onLoop(func() error {
		v := newView(ui.Screen{ID: "l", Body: []ui.Node{
			{Kind: ui.KindItem, ID: "a", Title: "Apple", Actions: open},
			{Kind: ui.KindItem, ID: "s1", Title: "\u8bbe\u7f6e", Actions: open},
			{Kind: ui.KindItem, ID: "b", Title: "Banana", Actions: open},
			{Kind: ui.KindItem, ID: "s2", Title: "Sound", Actions: open},
		}})
		c.sys.stack = []*View{v}
		for _, want := range []string{"s1", "s2", "s1"} {
			c.Key(Runes("s"))
			if v.focus != want {
				t.Errorf("s: focus on %s, want %s", v.focus, want)
			}
		}
		c.Key(Runes("B"))
		if v.focus != "b" {
			t.Errorf("B: focus on %s", v.focus)
		}
		m := &menuOverlay{entries: []menuEntry{{label: "Open"}, {label: "Share"}, {label: "Save", key: "x"}, {label: "Stop"}}}
		c.pushOverlay(m)
		c.Key(Runes("s"))
		c.Key(Runes("s"))
		if m.sel != 2 {
			t.Errorf("menu: s s selected %d, want 2", m.sel)
		}
		c.popOverlay(m)
		c.SetKeypad("t9")
		c.Key(Runes("a"))
		if v.focus != "b" {
			t.Errorf("9-key pad: focus moved to %s", v.focus)
		}
		return nil
	})
}

// Fn + arrows on a list: the first and last item, a page up and down.
func TestListEnds(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		var body []ui.Node
		for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
			body = append(body, ui.Node{Kind: ui.KindItem, ID: id, Title: id, Actions: []ui.Action{{ID: ui.ActionOpen, Label: "Open"}}})
		}
		v := newView(ui.Screen{ID: "l", Body: body})
		c.sys.stack = []*View{v}
		c.Key(Named(KeyLast))
		if v.focus != "h" {
			t.Errorf("last: %s", v.focus)
		}
		c.Key(Named(KeyFirst))
		if v.focus != "a" {
			t.Errorf("first: %s", v.focus)
		}
		c.Key(Named(KeyPageDown))
		if v.focus == "a" || v.focus == "b" && pageStep(v) > 1 {
			t.Errorf("page down: %s (a page is %d)", v.focus, pageStep(v))
		}
		return nil
	})
}

// The options menu of a field with text offers Copy and Select all, and
// Cut with a selection; Ctrl+C on an item runs its Copy; Ctrl+V pastes
// into the page's field; Shift + Chr opens the writing languages.
func TestEditMenuAndShortcuts(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		a := &App{M: &Manifest{ID: "com.example.notes", Name: "Notes"}, running: true, out: make(chan outbound, 8)}
		v := newView(ui.Screen{ID: "n", Body: []ui.Node{
			{Kind: ui.KindItem, ID: "m", Title: "Note", Actions: []ui.Action{{ID: "copy", Label: "Copy"}}},
			{Kind: ui.KindInput, ID: "f"},
		}})
		a.stack = []*View{v}
		c.fg = a
		labels := func() []string {
			c.openMenu(a, v, false)
			m := c.overlays[len(c.overlays)-1].(*menuOverlay)
			c.popOverlay(m)
			var out []string
			for _, e := range m.entries {
				switch e.label {
				case tr.T("Copy"), tr.T("Cut"), tr.T("Select all"):
					out = append(out, e.label)
				}
			}
			return out
		}
		c.Key(Named(KeyCopy))
		select {
		case o := <-a.out:
			if o.m.Action != "copy" {
				t.Errorf("Ctrl+C on the item sent %q", o.m.Action)
			}
		default:
			t.Error("Ctrl+C on the item ran nothing")
		}
		c.copyText("hi")
		c.Key(Named(KeyPaste))
		if v.focus != "f" || v.inputs["f"] != "hi" {
			t.Errorf("Ctrl+V: focus %s, field %q", v.focus, v.inputs["f"])
		}
		if got := labels(); len(got) != 2 {
			t.Errorf("menu of a field with text: %q", got)
		}
		c.Key(Key{Name: KeyLeft, Shift: true})
		if got := labels(); len(got) != 3 {
			t.Errorf("menu with a selection: %q", got)
		}
		c.ime.SetMode(ime.Zh)
		c.Key(Named(KeyLang))
		m, ok := c.overlays[len(c.overlays)-1].(*menuOverlay)
		if !ok || m.title != tr.T("Writing language") {
			t.Errorf("Shift + Chr opened %T", c.overlays[len(c.overlays)-1])
			return nil
		}
		c.Key(Named(KeyDown))
		c.Key(Named(KeyEnter))
		if c.ime.Mode() != ime.En {
			t.Errorf("picked English, the mode is %s", c.ime.Mode())
		}
		return nil
	})
}

// A key typed in the symbol table shows its variants; the key again
// moves to the next; OK types it at the caret.
func TestSymbolVariants(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		v := newView(ui.Screen{ID: "t", Body: []ui.Node{{Kind: ui.KindInput, ID: "f", Value: "ab"}}})
		c.sys.stack = []*View{v}
		c.ime.SetMode(ime.En)
		c.Key(Named(KeyLeft))
		c.Key(Named(KeySym))
		o, ok := c.overlays[len(c.overlays)-1].(*symOverlay)
		if !ok {
			t.Error("Chr opened no symbol table")
			return nil
		}
		c.Key(Runes("n"))
		c.Key(Runes("n"))
		if f := o.frame(c); f.Items[o.sel].Text != "\u0144" || f.Title != tr.T("Variants of \u201c%s\u201d \u00b7 press it again for the next", "n") {
			t.Errorf("variants: %q, title %q", f.Items[o.sel].Text, f.Title)
		}
		c.Key(Named(KeyEnter))
		if got := fieldText(v, "f"); got != "a\u0144|b" {
			t.Errorf("field %q", got)
		}
		return nil
	})
}
