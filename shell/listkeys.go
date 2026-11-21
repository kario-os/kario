package shell

import (
	"unicode"

	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/ui"
)

// A keyboard's keys outside text fields (none of them come from the 9-key
// pad): Fn + ↑ ↓ page through a list and Fn + ← → go to its first and last
// item; a letter that is no shortcut, on a page without a field to type it
// into, goes to the next item whose name starts with it (a Chinese name by
// its pinyin), as BlackBerry's lists did; Ctrl+C runs the focused item's
// Copy (its "copy" action), Ctrl+V pastes into the page's field; Shift +
// Chr is the writing language menu where a field takes text.

func (c *Core) listKey(a *App, v *View, node *ui.Node, k Key) bool {
	switch k.Name {
	case KeyFirst, KeyLast, KeyPageUp, KeyPageDown, KeyCopy, KeyCut, KeyPaste, KeySelectAll, KeyUndo, KeyDelete, KeyLang:
	default:
		return false
	}
	if a.system && v.S.ID == "sys.idle" {
		return true
	}
	switch k.Name {
	case KeyFirst, KeyLast:
		c.focusEnd(v, k.Name == KeyLast)
	case KeyPageUp, KeyPageDown:
		dir := map[string]int{KeyPageUp: -1, KeyPageDown: 1}[k.Name]
		for range pageStep(v) {
			c.moveFocus(v, dir)
		}
	case KeyCopy:
		if node != nil {
			for _, act := range node.Actions {
				if act.ID == "copy" {
					c.invoke(a, v, node, act)
					break
				}
			}
		}
	case KeyPaste:
		for i := range v.S.Body {
			if n := &v.S.Body[i]; n.Kind == ui.KindInput {
				v.focus, v.focusIdx, v.ensure = n.ID, i, true
				v.insertText(n.ID, c.clipText())
				break
			}
		}
	case KeyLang:
		if _, n := c.imeTarget(); n != nil {
			c.openLangMenu()
		}
	}
	return true
}

func (c *Core) focusEnd(v *View, last bool) {
	fs := v.focusables()
	if len(fs) == 0 {
		return
	}
	i := fs[0]
	if last {
		i = fs[len(fs)-1]
	} else {
		v.scroll = 0
	}
	v.focus, v.focusIdx, v.ensure, v.stick, v.pixel = v.S.Body[i].ID, i, true, false, false
}

// pageStep is how many items a page key moves past: those the screen
// shows (by the text layout), less one, which stays in view.
func pageStep(v *View) int {
	n := 0
	for _, i := range v.focusables() {
		if g, ok := v.geom[i]; ok && g[0] >= v.scroll && g[0]+g[1] <= v.scroll+v.bodyH {
			n++
		}
	}
	return max(1, n-1)
}

// letterJump moves the focus to the next item whose name starts with the
// letter typed.
func (c *Core) letterJump(v *View, rs []rune) {
	if c.ime.Keypad() != ime.QWERTY || len(rs) != 1 {
		return
	}
	fs := v.focusables()
	cur := -1
	for j, i := range fs {
		if v.S.Body[i].ID == v.focus {
			cur = j
		}
	}
	if j := nextInitial(len(fs), cur, func(j int) string { return nodeLabel(&v.S.Body[fs[j]]) }, rs[0]); j >= 0 {
		i := fs[j]
		v.focus, v.focusIdx, v.ensure, v.stick, v.pixel = v.S.Body[i].ID, i, true, false, false
	}
}

// nextInitial is the first of n names after cur, round the end, that
// starts with the letter r (a Chinese name by its pinyin); -1 for none or
// when r is no letter.
func nextInitial(n, cur int, name func(int) string, r rune) int {
	if r > unicode.MaxASCII || !unicode.IsLetter(r) {
		return -1
	}
	r = unicode.ToLower(r)
	for d := 1; d <= n; d++ {
		j := (cur + d + n) % n
		if p := ime.Pinyin(name(j)); p != "" && rune(p[0]) == r {
			return j
		}
	}
	return -1
}

// openLangMenu: the writing languages, Shift + Chr in a text field (E90);
// on a keyboard the input method writes pinyin and English.
func (c *Core) openLangMenu() {
	cur := c.ime.Mode()
	entry := func(label string, m ime.Mode, key string) menuEntry {
		if m == cur {
			label = "✓ " + label
		}
		return menuEntry{label: label, key: key, run: func() { c.ime.SetMode(m) }}
	}
	c.pushOverlay(&menuOverlay{title: tr.T("Writing language"), entries: []menuEntry{
		entry(tr.T("Chinese (pinyin)"), ime.Zh, "1"),
		entry(tr.T("English"), ime.En, "2"),
	}})
}
