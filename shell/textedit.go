package shell

import (
	"strings"
	"unicode"

	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/ui"
)

// A text field has a caret and a selection, as on Nokia's QWERTY phones:
// ← → move the caret, with Shift held they select (the E71's "press and
// hold shift and scroll"); Fn + ← → (first, last) go to the start and the
// end; Ctrl + C X V A Z copy, cut, paste, select all and undo (where the
// keyboard has no Ctrl key its configuration says what is: Fn + Chr, as
// on the E71); a held Backspace deletes a word at a time. The options menu
// offers the same (editEntries). Positions count runes. The caret starts
// at the end and goes back there when the text changes under it (the app
// set the value, a script filled it in).

// undoDepth is how many edits of a field Undo can take back.
const undoDepth = 20

type fieldEdit struct {
	// text is the value caret and anchor are positions in.
	text          string
	caret, anchor int
	undo          []editSnap
	// typing: the last edit typed text; more typing joins its undo step
	// until a space starts a new one.
	typing bool
}

type editSnap struct {
	text  string
	caret int
}

// edit is the caret and selection of field id.
func (v *View) edit(id string) *fieldEdit {
	if v.edits == nil {
		v.edits = map[string]*fieldEdit{}
	}
	text := v.inputs[id]
	e := v.edits[id]
	if e == nil || e.text != text {
		n := len([]rune(text))
		e = &fieldEdit{text: text, caret: n, anchor: n}
		v.edits[id] = e
	}
	return e
}

// selection is the selected runes [from, to); ok is false when none are.
func (e *fieldEdit) selection() (from, to int, ok bool) {
	from, to = min(e.caret, e.anchor), max(e.caret, e.anchor)
	return from, to, from < to
}

func (e *fieldEdit) remember(typing bool, s string) {
	if typing && e.typing && !strings.HasPrefix(s, " ") {
		return
	}
	e.undo = append(e.undo, editSnap{e.text, e.caret})
	if len(e.undo) > undoDepth {
		e.undo = e.undo[len(e.undo)-undoDepth:]
	}
	e.typing = typing
}

func (v *View) setText(id string, e *fieldEdit, rs []rune, caret int) {
	e.text = string(rs)
	e.caret, e.anchor = caret, caret
	v.inputs[id] = e.text
}

func (v *View) node(id string) *ui.Node {
	if i := v.indexOf(id); i >= 0 {
		return &v.S.Body[i]
	}
	return nil
}

// insertText types s into field id at the caret, over the selection. A
// web page's field (its own editor) takes it at the end.
func (v *View) insertText(id, s string) {
	if s == "" {
		return
	}
	if n := v.node(id); n == nil || n.Kind != ui.KindInput {
		v.inputs[id] += s
		return
	}
	e := v.edit(id)
	e.remember(true, s)
	rs := []rune(e.text)
	from, to, _ := e.selection()
	in := []rune(s)
	out := append(append(append(make([]rune, 0, len(rs)+len(in)), rs[:from]...), in...), rs[to:]...)
	v.setText(id, e, out, from+len(in))
}

// textBefore is the text before the caret of field n: the input method's
// context (half-width marks after digits, capitals after a full stop).
func (v *View) textBefore(n *ui.Node) string {
	if n.Kind != ui.KindInput {
		return v.inputs[n.ID]
	}
	e := v.edit(n.ID)
	return string([]rune(e.text)[:e.caret])
}

// deleteText deletes the selection, else the rune (word: the word) before
// the caret (dir < 0) or after it.
func (v *View) deleteText(id string, dir int, word bool) {
	e := v.edit(id)
	rs := []rune(e.text)
	from, to, sel := e.selection()
	if !sel {
		switch {
		case dir < 0 && word:
			from, to = wordStart(rs, e.caret), e.caret
		case dir < 0:
			from, to = max(0, e.caret-1), e.caret
		default:
			from, to = e.caret, min(len(rs), e.caret+1)
		}
	}
	if from == to {
		return
	}
	e.remember(false, "")
	v.setText(id, e, append(append(make([]rune, 0, len(rs)), rs[:from]...), rs[to:]...), from)
}

// wordStart is where the word before i starts, spaces before i included: a
// run of letters and digits or of other marks; a Chinese character is a
// word of its own (the text has no word breaks).
func wordStart(rs []rune, i int) int {
	for i > 0 && unicode.IsSpace(rs[i-1]) {
		i--
	}
	if i > 0 && unicode.Is(unicode.Han, rs[i-1]) {
		return i - 1
	}
	if i == 0 {
		return 0
	}
	word := wordRune(rs[i-1])
	for i > 0 && !unicode.IsSpace(rs[i-1]) && !unicode.Is(unicode.Han, rs[i-1]) && wordRune(rs[i-1]) == word {
		i--
	}
	return i
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func (v *View) moveCaret(id string, to int, extend bool) {
	e := v.edit(id)
	e.caret = max(0, min(len([]rune(e.text)), to))
	if !extend {
		e.anchor = e.caret
	}
	e.typing = false
}

func (v *View) undoEdit(id string) {
	e := v.edit(id)
	if len(e.undo) == 0 {
		return
	}
	s := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	v.setText(id, e, []rune(s.text), s.caret)
	e.typing = false
}

// selectedText is what Copy takes from field n: the selection, else the
// whole text; nothing from a password.
func (v *View) selectedText(n *ui.Node) string {
	if n.Secret {
		return ""
	}
	e := v.edit(n.ID)
	if from, to, ok := e.selection(); ok {
		return string([]rune(e.text)[from:to])
	}
	return e.text
}

// editKey is a key for the focused text field n that moves its caret or
// edits it; false hands it on. In an empty field ← → go on as they do
// elsewhere (← goes back); in one with text they stop at its ends.
func (c *Core) editKey(v *View, n *ui.Node, k Key) bool {
	if c.imeComposing(v, n) {
		// Not while a word is being composed at the caret.
		switch k.Name {
		case KeyFirst, KeyLast, KeyDelete, KeySelectAll, KeyCopy, KeyCut, KeyPaste, KeyUndo:
			return true
		}
	}
	e := v.edit(n.ID)
	length := len([]rune(e.text))
	from, to, sel := e.selection()
	switch k.Name {
	case KeyLeft, KeyRight:
		if length == 0 {
			return false
		}
		step := map[string]int{KeyLeft: -1, KeyRight: 1}[k.Name]
		switch {
		case k.Shift:
			v.moveCaret(n.ID, e.caret+step, true)
		case sel && step < 0:
			v.moveCaret(n.ID, from, false)
		case sel:
			v.moveCaret(n.ID, to, false)
		default:
			v.moveCaret(n.ID, e.caret+step, false)
		}
	case KeyUp, KeyDown:
		// A field is one line: Shift + ↑ ↓ select to its start or end.
		if !k.Shift {
			return false
		}
		v.moveCaret(n.ID, map[string]int{KeyUp: 0, KeyDown: length}[k.Name], true)
	case KeyFirst:
		v.moveCaret(n.ID, 0, k.Shift)
	case KeyLast:
		v.moveCaret(n.ID, length, k.Shift)
	case KeyBackspace:
		v.deleteText(n.ID, -1, k.Long)
	case KeyDelete:
		v.deleteText(n.ID, 1, false)
	case KeySelectAll:
		e.anchor, e.caret, e.typing = 0, length, false
	case KeyCopy:
		if t := v.selectedText(n); t != "" {
			c.copyText(t)
		}
	case KeyCut:
		if sel && !n.Secret {
			c.copyText(string([]rune(e.text)[from:to]))
			v.deleteText(n.ID, -1, false)
		}
	case KeyPaste:
		v.insertText(n.ID, c.clipText())
	case KeyUndo:
		v.undoEdit(n.ID)
	default:
		return false
	}
	return true
}

// typeChr types a key pressed with Chr held into field n: the key's
// variants in turn (in pinyin mode Chinese punctuation first), the nth
// replacing the one before it, as the E71 did. False for a key without
// variants, which types as usual.
func (c *Core) typeChr(v *View, n *ui.Node, k Key) bool {
	if k.Chr < 1 || len(k.Runes) != 1 {
		return false
	}
	vs := ime.Variants(k.Runes[0], c.ime.Mode() == ime.Zh)
	if len(vs) == 0 {
		return false
	}
	if k.Chr > 1 && n.Kind == ui.KindInput {
		if prev := vs[(k.Chr-2)%len(vs)]; strings.HasSuffix(v.textBefore(n), prev) {
			e := v.edit(n.ID)
			e.anchor = e.caret - len([]rune(prev))
		}
	}
	v.insertText(n.ID, vs[(k.Chr-1)%len(vs)])
	return true
}
