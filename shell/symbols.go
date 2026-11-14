package shell

import (
	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ui"
)

// The symbol table (Nokia's "special characters"): * on the 9-key pad,
// Chr / Sym on a keyboard. It is system UI, so the app only ever receives the
// character that was picked.

var symbolPages = [][]string{
	// Chinese (full-width) punctuation.
	{"\uff0c", "\u3002", "\uff1f", "\uff01", "\u3001", "\uff1a", "\uff1b", "…", "“", "”", "‘", "’", "\uff08", "\uff09", "\u300a", "\u300b", "\u3010", "\u3011", "—", "\uff5e", "·", "\uffe5", "\uff05", "\uff20"},
	{",", ".", "?", "!", "'", "\"", "-", "_", "(", ")", "@", "/", ":", ";", "&", "#", "*", "+", "=", "%", "$", "<", ">", "~"},
	{"☺", "☹", "♥", "★", "☆", "✓", "✗", "→", "←", "↑", "↓", "♪", "☀", "☁", "☂", "☎", "✉", "✈", "©", "°", "±", "×", "÷", "∞"},
}

const symCols = 6

type symOverlay struct {
	v         *View
	id        string
	page, sel int
}

func (c *Core) openSymbolsIfWanted(v *View, n *ui.Node) {
	if !c.ime.TakeSymbols() {
		return
	}
	c.pushOverlay(&symOverlay{v: v, id: n.ID})
}

func (o *symOverlay) title() string {
	return tr.T("Symbols %d/%d · * next page", o.page+1, len(symbolPages))
}

func (o *symOverlay) turn(d int) {
	o.page = (o.page + d + len(symbolPages)) % len(symbolPages)
	o.sel = min(o.sel, len(symbolPages[o.page])-1)
}

func (o *symOverlay) key(c *Core, k Key) {
	items := symbolPages[o.page]
	switch k.Name {
	case KeyEsc:
		c.popOverlay(o)
	case KeyLeft:
		o.sel = (o.sel + len(items) - 1) % len(items)
	case KeyRight:
		o.sel = (o.sel + 1) % len(items)
	case KeyUp:
		if o.sel >= symCols {
			o.sel -= symCols
		}
	case KeyDown:
		if o.sel+symCols < len(items) {
			o.sel += symCols
		}
	case KeySym, KeyTab:
		o.turn(1)
	case KeyEnter:
		c.popOverlay(o)
		o.v.inputs[o.id] += items[o.sel]
	case KeyRunes:
		for _, r := range k.Runes {
			switch r {
			case '*':
				o.turn(1)
			case '#':
				o.turn(-1)
			}
		}
	}
}

func (o *symOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	items := symbolPages[o.page]
	rows := (len(items) + symCols - 1) / symCols
	h := rows + 2
	y0 := H - 1 - h
	s := st(colFG, colMenu)
	can.Box(1, y0, W-2, h, st(colDim, colMenu), false)
	can.Text(3, y0, " "+o.title()+" ", bold(s), W-6)
	cell := (W - 6) / symCols
	for i, sym := range items {
		x, y := 3+(i%symCols)*cell, y0+1+i/symCols
		cs := s
		if i == o.sel {
			cs = bold(st(colFG, colMenuSel))
			can.Fill(x, y, cell-1, 1, cs)
		}
		can.Text(x+(cell-1-cv.StringWidth(sym))/2, y, sym, cs, cell-1)
	}
}

func (o *symOverlay) frame(c *Core) *FrameOverlay {
	f := &FrameOverlay{Kind: "symbols", Title: o.title(), Choice: symCols}
	for i, sym := range symbolPages[o.page] {
		f.Items = append(f.Items, FrameMenuItem{Text: sym, Sel: i == o.sel})
	}
	return f
}
