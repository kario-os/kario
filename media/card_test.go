package media

import (
	"bytes"
	"testing"
)

func TestCardOf(t *testing.T) {
	square := func(n int) [][]bool {
		bm := make([][]bool, n)
		for i := range bm {
			bm[i] = make([]bool, n)
		}
		return bm
	}
	for _, n := range []int{0, 20, 178} {
		if _, err := CardOf(square(n)); err == nil {
			t.Errorf("%d modules accepted", n)
		}
	}
	ragged := square(21)
	ragged[3] = ragged[3][:20]
	if _, err := CardOf(ragged); err == nil {
		t.Error("ragged rows accepted")
	}
	if _, err := NewCard(string(make([]byte, 3000))); err == nil {
		t.Error("3000 bytes fit a card")
	}
}

// TestScanScene: the viewfinder and the decoder see the card in the same
// place, the background around it; without a card only the background.
func TestScanScene(t *testing.T) {
	c, err := NewCard("hello")
	if err != nil {
		t.Fatal(err)
	}
	var held *Card
	show, look := ScanScene(Table, TableLook, func() *Card { return held })
	f, g := NewFrame(160, 120), NewGray(160, 120)
	show(0, f)
	look(0, g)
	plain := NewGray(160, 120)
	TableLook(0, plain)
	if !bytes.Equal(g.Pix, plain.Pix) {
		t.Fatal("no card, yet the decoder sees something on the table")
	}
	held = c
	show(0, f)
	look(0, g)
	// the corner is table, the card's white margin is near the middle's left
	if r, gg, b := f.At(2, 2); r < gg || gg < b {
		t.Errorf("the corner is not wood: %d %d %d", r, gg, b)
	}
	dark, light := 0, 0
	for y := 40; y < 80; y++ {
		for x := 60; x < 100; x++ {
			r, _, _ := f.At(x, y)
			switch v := g.Pix[y*g.W+x]; {
			case v < 90 && r < 90:
				dark++
			case v > 160 && r > 160:
				light++
			}
		}
	}
	if dark < 200 || light < 200 {
		t.Errorf("the middle is not a code in both: %d dark, %d light", dark, light)
	}
}

func TestViewOffFrame(t *testing.T) {
	c, _ := NewCard("hello")
	g := NewGray(64, 48)
	TableLook(0, g)
	before := append([]byte(nil), g.Pix...)
	for _, v := range []View{
		{Corners: Pose(0.3, 0, 0, 5, 5), Blur: 2, Noise: 10},
		{Corners: Pose(0.3, 0, 0, -5, 0), Blur: 2, Noise: 10},
		{Corners: [4][2]float64{}, Blur: 3},
	} {
		c.DrawGray(g, v)
		c.Draw(NewFrame(64, 48), v)
	}
	if !bytes.Equal(g.Pix, before) {
		t.Fatal("a card off the frame changed it")
	}
	// half on the frame: drawn up to the edge, no further
	c.DrawGray(g, View{Corners: Pose(0.4, 0.3, 0.2, 0.45, 0), Blur: 1, Noise: 5})
	c.Draw(NewFrame(64, 48), View{Corners: Pose(0.4, 0.3, 0.2, 0.45, 0)})
}

func TestNoiseDeterministic(t *testing.T) {
	a, b := NewGray(32, 32), NewGray(32, 32)
	a.noise(0, 0, 32, 32, 10, 7)
	b.noise(0, 0, 32, 32, 10, 7)
	if !bytes.Equal(a.Pix, b.Pix) {
		t.Fatal("same seed, different noise")
	}
	for _, v := range a.Pix {
		if v > 10 {
			t.Fatalf("noise of %d over ±10", v)
		}
	}
}

func TestV4L2Look(t *testing.T) {
	// 4×2 YUYV with a padded stride: Y0 U Y1 V per two pixels
	c := &V4L2{w: 4, h: 2, stride: 10, latest: []byte{
		10, 128, 20, 128, 30, 128, 40, 128, 0, 0,
		50, 128, 60, 128, 70, 128, 80, 128, 0, 0,
	}}
	g := NewGray(4, 2)
	c.Look(0, g)
	if want := []byte{10, 20, 30, 40, 50, 60, 70, 80}; !bytes.Equal(g.Pix, want) {
		t.Fatalf("%v, want %v", g.Pix, want)
	}
	half := NewGray(2, 1)
	c.Look(0, half)
	if !bytes.Equal(half.Pix, []byte{10, 30}) {
		t.Fatalf("scaled: %v", half.Pix)
	}
	c.latest = c.latest[:5]
	c.Look(0, g)
	if g.Pix[0] != 25 {
		t.Fatal("a short frame is not dark")
	}
}
