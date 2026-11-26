package main

import (
	"fmt"

	"github.com/kario-os/kario/ui"
)

// The board is a monochrome LCD, one unit per LCD pixel. The text screen
// (40 columns) samples a canvas 76 units wide and a multiple of 4 high at
// every other unit both ways, so whatever must show there sits on even
// units: a cell's 3×3 block is seen at its corners, and the font and icons
// have 2×2 pixels.
const (
	lcdW, lcdH = 76, 60
	pitch      = 4 // a cell: a 3×3 block and a one-pixel gap
	fieldX     = 4
	fieldY     = 16
	frameX     = fieldX - 2
	frameY     = fieldY - 2
	frameX1    = fieldX + cols*pitch
	frameY1    = fieldY + rows*pitch
	midX       = (frameX + frameX1) / 2
	big        = 2
	// The wrap-around border's dashes: both sides of the frame are whole
	// multiples of it, so every corner gets one.
	dashEvery = 14
	lcdBg     = "#b4cf68"
	lcdInk    = "#1e2c10"
)

type sprite []string

var (
	block = sprite{"###", "###", "###"}
	// The head looks right, its mouth open. Its back corners stay unlit so
	// that on the text screen it stands apart from the body.
	headRight = sprite{".##", "##.", ".##"}
	heads     = map[pt]sprite{
		right: headRight,
		left:  headRight.mirror(),
		down:  headRight.transpose(),
		up:    headRight.mirror().transpose(),
	}
	food = sprite{"#.#", ".#.", "#.#"}
	// The bug spans two cells and the gap between them.
	bug   = sprite{".#####.", "##.####", "#..#..#"}
	note  = sprite{"..##", "..#.", "..#.", "###.", "###."}
	pause = sprite{"##.##", "##.##", "##.##", "##.##", "##.##"}
)

var glyphs = map[rune]sprite{
	'0': {"###", "#.#", "#.#", "#.#", "###"},
	'1': {".#.", "##.", ".#.", ".#.", "###"},
	'2': {"###", "..#", "###", "#..", "###"},
	'3': {"###", "..#", ".##", "..#", "###"},
	'4': {"#.#", "#.#", "###", "..#", "..#"},
	'5': {"###", "#..", "###", "..#", "###"},
	'6': {"###", "#..", "###", "#.#", "###"},
	'7': {"###", "..#", "..#", "..#", "..#"},
	'8': {"###", "#.#", "###", "#.#", "###"},
	'9': {"###", "#.#", "###", "..#", "###"},
	'A': {"###", "#.#", "###", "#.#", "#.#"},
	'E': {"###", "#..", "##.", "#..", "###"},
	'G': {"###", "#..", "#.#", "#.#", "###"},
	'L': {"#..", "#..", "#..", "#..", "###"},
	'M': {"#.#", "###", "#.#", "#.#", "#.#"},
	'O': {"###", "#.#", "#.#", "#.#", "###"},
	'P': {"###", "#.#", "###", "#..", "#.."},
	'R': {"###", "#.#", "##.", "#.#", "#.#"},
	'T': {"###", ".#.", ".#.", ".#.", ".#."},
	'V': {"#.#", "#.#", "#.#", "#.#", ".#."},
}

func (s sprite) mirror() sprite {
	out := make(sprite, len(s))
	for j, row := range s {
		b := []byte(row)
		for i := range len(b) / 2 {
			b[i], b[len(b)-1-i] = b[len(b)-1-i], b[i]
		}
		out[j] = string(b)
	}
	return out
}

func (s sprite) transpose() sprite {
	out := make(sprite, len(s[0]))
	for i := range out {
		b := make([]byte, len(s))
		for j, row := range s {
			b[j] = row[i]
		}
		out[i] = string(b)
	}
	return out
}

type lcd []ui.DrawOp

func (l *lcd) fill(x, y, w, h int, color string) {
	*l = append(*l, ui.DrawOp{Op: "rect", X: x, Y: y, W: w, H: h, Color: color, Fill: true})
}

// sprite draws one rectangle per run of lit pixels in a row.
func (l *lcd) sprite(s sprite, x, y, px int) {
	for j, row := range s {
		for i := 0; i < len(row); {
			if row[i] != '#' {
				i++
				continue
			}
			k := i
			for k < len(row) && row[k] == '#' {
				k++
			}
			l.fill(x+i*px, y+j*px, (k-i)*px, px, lcdInk)
			i = k
		}
	}
}

func (l *lcd) text(s string, x, y int) {
	for _, r := range s {
		if r != ' ' {
			l.sprite(glyphs[r], x, y, big)
		}
		x += advance(r) * big
	}
}

func advance(r rune) int {
	if r == ' ' {
		return 2
	}
	return 4
}

func textWidth(s string) int {
	w := 0
	for _, r := range s {
		w += advance(r)
	}
	return (w - 1) * big
}

// centred is where text starts to sit in the middle of the field, on an
// even unit.
func centred(s string) int { return (2*midX + 1 - textWidth(s)) / 2 &^ 1 }

func (l *lcd) frame(dashed bool) {
	if !dashed {
		*l = append(*l, ui.DrawOp{Op: "rect", X: frameX, Y: frameY, W: frameX1 - frameX + 1, H: frameY1 - frameY + 1, Color: lcdInk})
		return
	}
	const half = 3
	for x := frameX; x <= frameX1; x += dashEvery {
		a, b := max(x-half, frameX), min(x+half, frameX1)
		l.fill(a, frameY, b-a+1, 1, lcdInk)
		l.fill(a, frameY1, b-a+1, 1, lcdInk)
	}
	for y := frameY; y <= frameY1; y += dashEvery {
		a, b := max(y-half, frameY), min(y+half, frameY1)
		l.fill(frameX, a, 1, b-a+1, lcdInk)
		l.fill(frameX1, a, 1, b-a+1, lcdInk)
	}
}

func cellXY(p pt) (int, int) { return fieldX + p.x*pitch, fieldY + p.y*pitch }

func (l *lcd) cell(s sprite, p pt) {
	x, y := cellXY(p)
	l.sprite(s, x, y, 1)
}

func number(n, digits int) string {
	return fmt.Sprintf("%0*d", digits, min(n, 9999))
}

func draw(g *game) []ui.DrawOp {
	var l lcd
	l.frame(g.Wrap)
	switch g.phase {
	case inMenu:
		l.text("TOP "+number(g.Top, 4), frameX, 2)
		if !g.Mute {
			l.sprite(note, frameX1-8, 2, big)
		}
		s := fmt.Sprintf("LEVEL %d", g.Level)
		l.text(s, centred(s), 20)
		// A bar per level, growing up to the chosen one; the rest are stubs.
		for i := 1; i <= levels; i++ {
			h := 2
			if i <= g.Level {
				h = 2 * i
			}
			l.fill(midX-25+(i-1)*6, frameY1-4-h, 4, h, lcdInk)
		}
	case over:
		l.text(number(g.score, 4), frameX, 2)
		l.text("GAME", centred("GAME"), 18)
		l.text("OVER", centred("OVER"), 30)
		s := "TOP " + number(g.Top, 4)
		l.text(s, centred(s), 44)
	default:
		l.text(number(g.score, 4), frameX, 2)
		switch {
		case g.phase == paused:
			l.sprite(pause, frameX1-10, 2, big)
		case g.bugLeft > 0:
			l.sprite(bug, frameX1-30, 4, big)
			l.text(number(g.bugLeft, 2), frameX1-14, 2)
		}
		if g.bugLeft > 0 {
			l.cell(bug, g.bug)
		}
		l.cell(food, g.food)
		if g.phase != dying || g.blink%2 == 0 {
			for i, s := range g.snake {
				if i == 0 {
					l.cell(heads[g.dir], s)
				} else {
					l.cell(block, s)
				}
			}
		}
	}
	return l
}
