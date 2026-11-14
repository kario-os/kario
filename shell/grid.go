package shell

import (
	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ui"
)

// Grids: consecutive "tile" items on a screen with Columns > 1 (the home
// screen, a calculator's keys). Arrows move in two dimensions inside the
// grid and hand over to normal focus movement at its edges.

func isTile(n *ui.Node) bool { return n.Kind == ui.KindItem && n.Style == ui.StyleTile }

// tileRun returns the [start, end) indices of the tile run containing i.
func tileRun(body []ui.Node, i int) (int, int) {
	start, end := i, i
	for start > 0 && isTile(&body[start-1]) {
		start--
	}
	for end < len(body) && isTile(&body[end]) {
		end++
	}
	return start, end
}

func (c *Core) moveInGrid(v *View, key string) bool {
	cols := v.S.Columns
	cur := v.indexOf(v.focus)
	if cols < 2 || cur < 0 || !isTile(&v.S.Body[cur]) {
		return false
	}
	start, end := tileRun(v.S.Body, cur)
	pos, count := cur-start, end-start
	next := -1
	switch key {
	case KeyLeft:
		if pos > 0 {
			next = pos - 1
		}
	case KeyRight:
		if pos+1 < count {
			next = pos + 1
		}
	case KeyUp:
		if pos-cols >= 0 {
			next = pos - cols
		}
	case KeyDown:
		switch {
		case pos+cols < count:
			next = pos + cols
		case pos/cols < (count-1)/cols:
			next = count - 1
		}
	}
	if next < 0 {
		return false
	}
	v.focus, v.focusIdx, v.ensure, v.stick = v.S.Body[start+next].ID, start+next, true, false
	return true
}

// drawTileRow draws one row of a grid for the text renderer.
func (c *Core) drawTileRow(row []*ui.Node, focusID string, W, cols int) *cv.Canvas {
	icons := false
	for _, n := range row {
		icons = icons || n.Icon != ""
	}
	h := 2
	if icons {
		h = 3
	}
	b := cv.New(W, h, stBase)
	cw := (W - 2) / cols
	for k, n := range row {
		x := 1 + k*cw
		bg := colBG
		if n.ID == focusID {
			bg = colFocus
			b.Fill(x, 0, cw-1, h-1, st(colFG, bg))
		}
		center := func(y int, s string, sty cv.Style) {
			s = cv.Truncate(s, cw-1)
			b.Text(x+(cw-1-cv.StringWidth(s))/2, y, s, sty, cw-1)
		}
		label := n.Title
		if n.Badge != "" {
			label += "·" + n.Badge
		}
		if icons {
			center(0, n.Icon, bold(st(colAccent, bg)))
			center(1, label, st(colFG, bg))
		} else {
			center(0, label, bold(st(colFG, bg)))
		}
	}
	return b
}
