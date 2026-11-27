package shell

import (
	"fmt"

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

// inGrid: the focus is on a grid's tile.
func inGrid(v *View) bool {
	cur := v.indexOf(v.focus)
	return v.S.Columns > 1 && cur >= 0 && isTile(&v.S.Body[cur])
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

// drawTileRow draws one row of a grid for the text renderer. Tiles
// without an icon (keys, a calendar's days) show their subtitle under the
// title.
func (c *Core) drawTileRow(row []*ui.Node, focusID string, W, cols int) *cv.Canvas {
	icons, subs := false, false
	for _, n := range row {
		icons = icons || n.Icon != ""
		subs = subs || n.Subtitle != ""
	}
	subs = subs && !icons
	h := 2
	if icons || subs {
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
		switch {
		case icons:
			center(0, n.Icon, bold(st(colAccent, bg)))
			center(1, label, st(colFG, bg))
		case subs:
			center(0, label, bold(st(colFG, bg)))
			center(1, n.Subtitle, st(colDim, bg))
		default:
			center(0, label, bold(st(colFG, bg)))
		}
	}
	return b
}

// drawPickerGrid draws a full-screen picker's grid (the photo picker, the
// share sheet) in rows top to bottom, tile sel focused: whole rows, from
// the first that keeps sel's row on screen, as a menu keeps its entry,
// with the scroll bar while some rows do not show. A row's last line is
// blank, so it may lie on line bottom.
func (c *Core) drawPickerGrid(can *cv.Canvas, tiles []*ui.Node, sel, top, bottom int) {
	W, cols := c.cfg.W, c.menuColumns()
	var rows []*cv.Canvas
	for i := 0; i < len(tiles); i += cols {
		rows = append(rows, c.drawTileRow(tiles[i:min(len(tiles), i+cols)], fmt.Sprint(sel), W, cols))
	}
	if len(rows) == 0 {
		return
	}
	// firstFor is the first row shown with row r the last that has to.
	firstFor := func(r int) int {
		f, h := r, rows[r].H
		for f > 0 && h+rows[f-1].H <= bottom-top+1 {
			f--
			h += rows[f].H
		}
		return f
	}
	height := func(n int) int {
		h := 0
		for _, b := range rows[:n] {
			h += b.H
		}
		return h
	}
	first := firstFor(min(max(sel, 0)/cols, len(rows)-1))
	y, last := top, first-1
	for r := first; r < len(rows) && y+rows[r].H-1 <= bottom; r++ {
		can.Blit(0, y, rows[r])
		y += rows[r].H
		last = r
	}
	if first > 0 || last < len(rows)-1 {
		scrollBar(can, W-1, top, bottom-top, height(first), height(firstFor(len(rows)-1)))
	}
}
