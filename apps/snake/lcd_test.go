package main

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/kario-os/kario/ui"
)

// raster paints filled and outlined rectangles the way the shell does:
// true where the ink is.
func raster(t *testing.T, ops []ui.DrawOp) [][]bool {
	t.Helper()
	if len(ops) > ui.MaxCanvasOps {
		t.Fatalf("%d ops", len(ops))
	}
	px := make([][]bool, lcdH)
	for y := range px {
		px[y] = make([]bool, lcdW)
	}
	for _, op := range ops {
		if op.Op != "rect" || op.W < 1 || op.H < 1 || op.X < 0 || op.Y < 0 || op.X+op.W > lcdW || op.Y+op.H > lcdH {
			t.Fatalf("op off the board: %+v", op)
		}
		for y := op.Y; y < op.Y+op.H; y++ {
			for x := op.X; x < op.X+op.W; x++ {
				if op.Fill || y == op.Y || y == op.Y+op.H-1 || x == op.X || x == op.X+op.W-1 {
					px[y][x] = op.Color == lcdInk
				}
			}
		}
	}
	return px
}

// textView is what the 40-column text screen samples: every other unit.
func textView(px [][]bool, x, y, w, h int) string {
	var b strings.Builder
	for j := y; j < y+h; j += 2 {
		for i := x; i < x+w; i += 2 {
			b.WriteByte(map[bool]byte{false: '.', true: '#'}[px[j][i]])
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func playingGame() *game {
	g := newGame(prefs{Level: 3}, rand.New(rand.NewSource(1)))
	g.start()
	g.food = pt{12, 2}
	g.bug, g.bugLeft = pt{2, 8}, 7
	g.score = 42
	return g
}

func TestTextScreenSeesTheBoard(t *testing.T) {
	g := playingGame()
	px := raster(t, draw(g))
	cell := func(p pt) string {
		x, y := cellXY(p)
		return textView(px, x, y, 4, 4)
	}
	if got := cell(g.snake[1]); got != "##\n##\n" {
		t.Fatalf("body on the text screen:\n%s", got)
	}
	if got := cell(g.snake[0]); got != ".#\n.#\n" {
		t.Fatalf("head looking right on the text screen:\n%s", got)
	}
	if got := cell(g.food); got != "##\n##\n" {
		t.Fatalf("food on the text screen:\n%s", got)
	}
	x, y := cellXY(g.bug)
	if got := textView(px, x, y, 8, 4); got != ".##.\n#..#\n" {
		t.Fatalf("bug on the text screen:\n%s", got)
	}
	if got := textView(px, frameX, 2, 30, 10); got != strings.Join([]string{
		"###.###.#.#.###",
		"#.#.#.#.#.#...#",
		"#.#.#.#.###.###",
		"#.#.#.#...#.#..",
		"###.###...#.###", ""}, "\n") {
		t.Fatalf("score 0042 on the text screen:\n%s", got)
	}
	for _, p := range []pt{{frameX, frameY}, {frameX1, frameY}, {frameX, frameY1}, {frameX1, frameY1}} {
		if p.x%2 != 0 || p.y%2 != 0 || !px[p.y][p.x] {
			t.Fatalf("border corner %v is not on the text screen", p)
		}
	}
}

func TestHeadsAndGaps(t *testing.T) {
	g := playingGame()
	px := raster(t, draw(g))
	h := g.snake[0]
	x, y := cellXY(h)
	if px[y+1][x+2] || !px[y+1][x] {
		t.Fatal("the head's mouth does not face right")
	}
	for _, s := range g.snake[1:] {
		x, y := cellXY(s)
		for i := range 3 {
			if px[y+3][x+i] || px[y+i][x+3] {
				t.Fatalf("no gap after the segment at %v", s)
			}
		}
	}
	if heads[up][0] != "#.#" || heads[up][2] != ".#." || heads[down][2] != "#.#" || heads[down][0] != ".#." || heads[left][1] != ".##" {
		t.Fatalf("heads: %v", heads)
	}
}

func TestScreensFit(t *testing.T) {
	for _, g := range []*game{
		newGame(prefs{Level: 9, Top: 12345}, rand.New(rand.NewSource(2))),
		newGame(prefs{Level: 1, Wrap: true, Mute: true}, rand.New(rand.NewSource(3))),
	} {
		raster(t, draw(g))
		g.start()
		raster(t, draw(g))
		g.phase = paused
		raster(t, draw(g))
		g.end()
		raster(t, draw(g))
	}
	for _, s := range []string{"GAME", "OVER", "LEVEL 9", "TOP 9999"} {
		if x := centred(s); x%2 != 0 || x < frameX+1 || x+textWidth(s) > frameX1 {
			t.Fatalf("%q at %d does not fit", s, x)
		}
		for _, r := range s {
			if r != ' ' && glyphs[r] == nil {
				t.Fatalf("no glyph for %q", r)
			}
		}
	}
}

func TestWrapBorderIsDashed(t *testing.T) {
	g := newGame(prefs{Wrap: true}, rand.New(rand.NewSource(1)))
	px := raster(t, draw(g))
	row := textView(px, frameX, frameY, frameX1-frameX+1, 1)
	if !strings.HasPrefix(row, "##..") || !strings.HasSuffix(row, "..##\n") || !strings.Contains(row, ".###.") {
		t.Fatalf("dashed top edge on the text screen: %q", row)
	}
	walls := textView(raster(t, draw(newGame(prefs{}, rand.New(rand.NewSource(1))))), frameX, frameY, frameX1-frameX+1, 1)
	if strings.Contains(walls, ".") {
		t.Fatalf("solid top edge: %q", walls)
	}
}
