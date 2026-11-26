package canvas

import (
	"strings"
	"testing"
)

func TestWideGlyphs(t *testing.T) {
	c := New(10, 1, Style{})
	if n := c.Text(0, 0, "\u5f20\u4e09ab", Style{}, 10); n != 6 {
		t.Fatalf("width = %d, want 6", n)
	}
	if got := strings.TrimRight(c.Plain(), "\n"); got != "\u5f20\u4e09ab" {
		t.Fatalf("plain = %q", got)
	}
	c.Text(1, 0, "x", Style{}, 1)
	if got := strings.TrimRight(c.Plain(), "\n"); got != " x\u4e09ab" {
		t.Fatalf("overwriting half of a wide glyph: %q", got)
	}
}

func TestSecureCellsMasked(t *testing.T) {
	c := New(4, 1, Style{})
	c.Pixels(0, 0, 4, 1, func(i, j int) RGB { return RGB{255, 255, 255} }, true)
	if strings.ContainsRune(c.Plain(), '▀') || strings.Contains(c.ANSIMasked(), "▀") {
		t.Fatal("secure pixels leaked into a snapshot")
	}
	if !strings.Contains(c.ANSI(), "▀") {
		t.Fatal("secure pixels missing from the live screen")
	}
}

// Six wide characters fill a line of 12 cells, so the closing corner bracket
// (U+300D) would start the second line.
func TestWrapKinsoku(t *testing.T) {
	for _, l := range Wrap("\u5411\u300c"+strings.Repeat("\u4e2d", 4)+"\u300d\u4ed8\u6b3e", 12) {
		if r := []rune(l); len(r) > 0 && NoLineStart(r[0]) {
			t.Fatalf("line starts with closing punctuation: %q", l)
		}
	}
}

func TestWrapCJK(t *testing.T) {
	// Four wide characters, a full-width comma (U+FF0C), six more.
	lines := Wrap(strings.Repeat("\u4e2d", 4)+"\uff0c"+strings.Repeat("\u4e2d", 6), 10)
	for _, l := range lines {
		if StringWidth(l) > 10 {
			t.Fatalf("line %q is %d cells", l, StringWidth(l))
		}
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines: %q", len(lines), lines)
	}
}
