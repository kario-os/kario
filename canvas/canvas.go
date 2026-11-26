// Package canvas is the shell's cell buffer: a grid of terminal cells with
// truecolor attributes, correct handling of double-width (CJK) glyphs, and
// half-block pixel blitting for images and video.
package canvas

import (
	_ "embed"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
)

type RGB struct{ R, G, B uint8 }

type Style struct {
	FG, BG    RGB
	Bold, Dim bool
	Under     bool
	Italic    bool
}

type cell struct {
	r    rune
	cont bool // right half of a wide glyph
	st   Style
	mask bool // secure content: never exported in snapshots
}

type Canvas struct {
	W, H  int
	cells []cell
	Base  Style
}

var cond = func() *runewidth.Condition {
	c := runewidth.NewCondition()
	c.EastAsianWidth = false
	return c
}()

func RuneWidth(r rune) int { return cond.RuneWidth(r) }

func StringWidth(s string) int { return cond.StringWidth(s) }

func New(w, h int, base Style) *Canvas {
	c := &Canvas{W: w, H: h, cells: make([]cell, w*h), Base: base}
	c.Fill(0, 0, w, h, base)
	return c
}

func (c *Canvas) at(x, y int) *cell { return &c.cells[y*c.W+x] }

func (c *Canvas) Fill(x, y, w, h int, st Style) {
	for j := y; j < y+h; j++ {
		for i := x; i < x+w; i++ {
			c.put(i, j, ' ', st, false)
		}
	}
}

// put writes one narrow cell, breaking any wide glyph it overlaps.
func (c *Canvas) put(x, y int, r rune, st Style, mask bool) {
	if x < 0 || y < 0 || x >= c.W || y >= c.H {
		return
	}
	cl := c.at(x, y)
	if cl.cont && x > 0 {
		c.at(x-1, y).r = ' '
	}
	if !cl.cont && x+1 < c.W && c.at(x+1, y).cont {
		n := c.at(x+1, y)
		n.cont, n.r = false, ' '
	}
	*cl = cell{r: r, st: st, mask: mask}
}

// Text draws s starting at (x,y), clipped to maxW cells; returns cells used.
func (c *Canvas) Text(x, y int, s string, st Style, maxW int) int {
	used := 0
	for _, r := range s {
		if r == '\n' || r == '\t' {
			r = ' '
		}
		w := RuneWidth(r)
		if w == 0 {
			continue
		}
		if used+w > maxW || x+used+w > c.W {
			break
		}
		c.put(x+used, y, r, st, false)
		if w == 2 {
			c.put(x+used+1, y, ' ', st, false)
			c.at(x+used+1, y).cont = true
		}
		used += w
	}
	return used
}

// Restyle changes colors of a region without touching glyphs.
func (c *Canvas) Restyle(x, y, w int, f func(Style) Style) {
	if y < 0 || y >= c.H {
		return
	}
	for i := x; i < x+w && i < c.W; i++ {
		if i >= 0 {
			cl := c.at(i, y)
			cl.st = f(cl.st)
		}
	}
}

// Pixels blits an image of w x 2h pixels into w x h cells using upper
// half-block glyphs (two vertical pixels per cell).
func (c *Canvas) Pixels(x, y, w, h int, px func(i, j int) RGB, mask bool) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			c.put(x+i, y+j, '▀', Style{FG: px(i, 2*j), BG: px(i, 2*j+1)}, mask)
		}
	}
}

func (c *Canvas) Box(x, y, w, h int, st Style, double bool) {
	tl, tr, bl, br, hz, vt := '┌', '┐', '└', '┘', '─', '│'
	if double {
		tl, tr, bl, br, hz, vt = '╔', '╗', '╚', '╝', '═', '║'
	}
	c.Fill(x, y, w, h, st)
	for i := x + 1; i < x+w-1; i++ {
		c.put(i, y, hz, st, false)
		c.put(i, y+h-1, hz, st, false)
	}
	for j := y + 1; j < y+h-1; j++ {
		c.put(x, j, vt, st, false)
		c.put(x+w-1, j, vt, st, false)
	}
	c.put(x, y, tl, st, false)
	c.put(x+w-1, y, tr, st, false)
	c.put(x, y+h-1, bl, st, false)
	c.put(x+w-1, y+h-1, br, st, false)
}

// Blit copies another canvas into this one at (x,y).
func (c *Canvas) Blit(x, y int, o *Canvas) {
	for j := 0; j < o.H; j++ {
		for i := 0; i < o.W; i++ {
			if x+i < 0 || y+j < 0 || x+i >= c.W || y+j >= c.H {
				continue
			}
			*c.at(x+i, y+j) = *o.at(i, j)
		}
	}
}

func sgr(b *strings.Builder, st Style) {
	b.WriteString("\x1b[0")
	if st.Bold {
		b.WriteString(";1")
	}
	if st.Dim {
		b.WriteString(";2")
	}
	if st.Italic {
		b.WriteString(";3")
	}
	if st.Under {
		b.WriteString(";4")
	}
	b.WriteString(";38;2;")
	b.WriteString(strconv.Itoa(int(st.FG.R)) + ";" + strconv.Itoa(int(st.FG.G)) + ";" + strconv.Itoa(int(st.FG.B)))
	b.WriteString(";48;2;")
	b.WriteString(strconv.Itoa(int(st.BG.R)) + ";" + strconv.Itoa(int(st.BG.G)) + ";" + strconv.Itoa(int(st.BG.B)))
	b.WriteByte('m')
}

// ANSI renders the canvas as truecolor escape sequences, one line per row.
func (c *Canvas) ANSI() string { return c.ansi(false) }

// ANSIMasked is ANSI with secure cells blanked, for screenshots.
func (c *Canvas) ANSIMasked() string { return c.ansi(true) }

var maskStyle = Style{FG: RGB{90, 90, 90}, BG: RGB{40, 40, 40}}

func (c *Canvas) ansi(mask bool) string {
	var b strings.Builder
	b.Grow(c.W * c.H * 8)
	for y := 0; y < c.H; y++ {
		var last *Style
		for x := 0; x < c.W; x++ {
			cl := c.at(x, y)
			if cl.cont {
				continue
			}
			st, r := cl.st, cl.r
			if mask && cl.mask {
				st, r = maskStyle, '▒'
			}
			if last == nil || *last != st {
				sgr(&b, st)
				last = &st
			}
			b.WriteRune(r)
		}
		b.WriteString("\x1b[0m")
		if y < c.H-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Plain renders glyphs only, with secure cells masked; used for snapshots and
// automated tests, which therefore can never capture a payment code.
func (c *Canvas) Plain() string {
	var b strings.Builder
	for y := 0; y < c.H; y++ {
		var line strings.Builder
		for x := 0; x < c.W; x++ {
			cl := c.at(x, y)
			if cl.cont {
				continue
			}
			if cl.mask {
				line.WriteRune('▒')
			} else {
				line.WriteRune(cl.r)
			}
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

//go:embed no-line-start.txt
var noLineStartFile string

// noLineStart: no-line-start.txt lists the closing punctuation, full-width
// and ASCII, that Chinese typesetting (kinsoku) does not allow at the start
// of a line.
var noLineStart = strings.TrimSpace(noLineStartFile)

// NoLineStart reports closing punctuation that Chinese typesetting (kinsoku)
// does not allow at the start of a line.
func NoLineStart(r rune) bool {
	return strings.ContainsRune(noLineStart, r)
}

// Wrap breaks s into lines of at most w cells (CJK may break anywhere, but a
// closing punctuation mark takes the preceding character to the next line).
func Wrap(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		var cur []rune
		cw := 0
		for _, r := range para {
			rw := RuneWidth(r)
			if cw+rw > w {
				var carry []rune
				if NoLineStart(r) && len(cur) > 1 {
					carry = cur[len(cur)-1:]
					cur = cur[:len(cur)-1]
				}
				lines = append(lines, string(cur))
				cur, cw = append([]rune{}, carry...), StringWidth(string(carry))
				if r == ' ' {
					continue
				}
			}
			cur = append(cur, r)
			cw += rw
		}
		lines = append(lines, string(cur))
	}
	return lines
}

// Truncate clips s to w cells, adding an ellipsis when clipped.
func Truncate(s string, w int) string {
	if StringWidth(s) <= w {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := RuneWidth(r)
		if used+rw > w-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + "…"
}
