package shell

import (
	"fmt"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/ui"
)

// The input method is system UI like the trusted dialogs: keys reach it before
// any app, and apps only receive committed text. Secret fields bypass it.

func (c *Core) SetKeypad(k string) { c.ime.SetKeypad(ime.Keypad(k)) }

func (c *Core) SetIMEMode(m string) { c.ime.SetMode(ime.Mode(m)) }

// imeTarget returns the focused text input that the IME may compose into.
func (c *Core) imeTarget() (*View, *ui.Node) {
	if len(c.overlays) > 0 {
		return nil, nil
	}
	v := c.current().top()
	if v == nil {
		return nil, nil
	}
	n := v.focused()
	if n == nil {
		return nil, nil
	}
	keyboard, field := n.Keyboard, 0
	switch {
	case n.Kind == ui.KindWeb:
		wv, ok := c.webField(v, n)
		if !ok {
			return nil, nil
		}
		keyboard, field = map[string]string{"email": "email"}[wv.edit], wv.field
	case n.Kind != ui.KindInput || n.Secret || n.Keyboard == "number" || n.Keyboard == "phone":
		return nil, nil
	}
	owner := fmt.Sprintf("%p/%s/%d", v, n.ID, field)
	if c.imeOwner != owner {
		c.ime.Preload()
		c.ime.Cancel()
		c.imeOwner = owner
		// A field-forced mode (email -> English) lasts only while that field
		// has focus.
		if c.imeSaved != "" {
			c.ime.SetMode(c.imeSaved)
			c.imeSaved = ""
		}
		if keyboard == "email" && c.ime.Mode() != ime.En {
			c.imeSaved = c.ime.Mode()
			c.ime.SetMode(ime.En)
		}
	}
	return v, n
}

var imeKeyNames = map[string]string{
	KeyBackspace: "backspace", KeyEnter: "enter", KeyEsc: "esc", KeyLeft: "left",
	KeyRight: "right", KeyUp: "up", KeyDown: "down", KeyIME: "ime", KeySym: "sym",
}

// imeKey routes a key through the IME; false means normal handling applies.
func (c *Core) imeKey(k Key) bool {
	v, n := c.imeTarget()
	if n == nil {
		return false
	}
	insert := func(s string) {
		if s != "" {
			v.inputs[n.ID] += s
		}
	}
	defer c.openSymbolsIfWanted(v, n)
	if k.Name == KeyRunes {
		for _, r := range k.Runes {
			if r > 127 {
				c.ime.Cancel()
				insert(string(r))
				continue
			}
			handled, text := c.ime.Handle(ime.Key{Name: "rune", Rune: r, Long: k.Long, Before: v.inputs[n.ID]})
			insert(text)
			if !handled {
				insert(string(r))
			}
		}
		return true
	}
	name, ok := imeKeyNames[k.Name]
	if !ok {
		return false
	}
	handled, text := c.ime.Handle(ime.Key{Name: name, Before: v.inputs[n.ID]})
	insert(text)
	return handled
}

// imeTick commits a pending multi-tap letter after its timeout.
func (c *Core) imeTick() {
	if v, n := c.imeTarget(); n != nil {
		if t := c.ime.Tick(); t != "" {
			v.inputs[n.ID] += t
			c.webPushValue(v, n)
		}
	}
}

func (c *Core) imeComposing(v *View, n *ui.Node) bool {
	if !c.ime.Composing() {
		return false
	}
	field := 0
	if wv, ok := c.webField(v, n); ok {
		field = wv.field
	}
	return c.imeOwner == fmt.Sprintf("%p/%s/%d", v, n.ID, field)
}

func (c *Core) drawIME(can *cv.Canvas) {
	if _, n := c.imeTarget(); n == nil || !c.ime.Active() {
		return
	}
	rows := c.ime.Rows()
	W, H := c.cfg.W, c.cfg.H
	y0 := H - 1 - len(rows)
	for i, r := range rows {
		y := y0 + i
		base := st(colFG, colMenu)
		can.Fill(0, y, W, 1, base)
		x := 1
		for j, item := range r.Items {
			label := item
			if r.Numbered {
				label = fmt.Sprintf("%d%s", j+1, item)
			}
			s := base
			switch {
			case j == r.Sel && r.Active:
				s = bold(st(colFG, colMenuSel))
			case j == r.Sel:
				s = st(colAccent, colMenu)
			}
			w := cv.StringWidth(label)
			if x+w > W-2 {
				break
			}
			can.Text(x, y, label, s, w)
			x += w + 1
		}
		if r.More {
			can.Text(W-2, y, "›", st(colDim, colMenu), 1)
		}
	}
}
