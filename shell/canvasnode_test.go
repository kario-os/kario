package shell

import (
	"testing"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ui"
)

// The shapes land on the pixels they name, inside the canvas only; sizes and
// shape counts are capped.
func TestPaintCanvas(t *testing.T) {
	red, white := cv.RGB{R: 255}, cv.RGB{R: 255, G: 255, B: 255}
	r := paintCanvas(&ui.Canvas{W: 10, H: 8, Bg: "#000000", Ops: []ui.DrawOp{
		{Op: "rect", X: 1, Y: 1, W: 3, H: 2, Color: "#ff0000", Fill: true},
		{Op: "line", X: 0, Y: 7, X2: 9, Y2: 7, Color: "#ffffff"},
		{Op: "circle", X: 7, Y: 3, R: 2, Color: "#ffffff"},
		{Op: "poly", Pts: []int{5, 0, 9, 0, 9, 1}, Color: "#ff0000", Fill: true},
		{Op: "dot", X: 40, Y: -3, Color: "#ffffff"},
	}})
	if r.at(1, 1) != red || r.at(3, 2) != red || r.at(4, 2) == red || r.at(0, 0) != (cv.RGB{}) {
		t.Fatal("filled rect")
	}
	for x := 0; x < 10; x++ {
		if r.at(x, 7) != white {
			t.Fatalf("line misses x=%d", x)
		}
	}
	if r.at(7, 1) != white || r.at(9, 3) != white || r.at(7, 3) == white {
		t.Fatal("circle outline")
	}
	if r.at(8, 0) != red {
		t.Fatal("polygon")
	}
	big := paintCanvas(&ui.Canvas{W: 100000, H: -4})
	if big.w != ui.MaxCanvasSide || big.h != 1 {
		t.Fatalf("canvas %dx%d", big.w, big.h)
	}
	if canvasKey(Key{Name: KeyRunes, Runes: []rune{'/'}}) != "" || canvasKey(Named(KeyEsc)) != "" || canvasKey(Key{Name: KeyRunes, Runes: []rune{'5'}}) != "5" {
		t.Fatal("canvas keys")
	}
}
