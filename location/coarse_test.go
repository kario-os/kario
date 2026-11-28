package location

import (
	"math"
	"math/rand/v2"
	"testing"
)

var places = [][2]float64{{39.9087, 116.3975}, {-33.8688, 151.2093}, {64.1466, -21.9426}, {0.0001, 179.9999}, {1.29, 103.85}, {-54.8, -68.3}}

// Fixes of one place, with a receiver's noise, all get the same cell: their
// average is the cell's centre, no nearer the place than one fix.
func TestCoarseStable(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, p := range places {
		g := &Coarse{Offset: [2]float64{0.37, 0.81}}
		la0, lo0 := g.Snap(p[0], p[1])
		for i := 0; i < 2000; i++ {
			dLat, dLon := r.NormFloat64()*20/metresPerDegree, r.NormFloat64()*20/metresPerDegree/math.Cos(p[0]*math.Pi/180)
			if la, lo := g.Snap(p[0]+dLat, p[1]+dLon); la != la0 || lo != lo0 {
				t.Fatalf("%v: fix %d moved the cell from %v,%v to %v,%v", p, i, la0, lo0, la, lo)
			}
		}
		if d := Distance(la0, lo0, p[0], p[1]); d > CoarseCellM*0.75 {
			t.Errorf("%v: %.0f m from the fix", p, d)
		}
		again := &Coarse{Offset: g.Offset}
		if la, lo := again.Snap(p[0], p[1]); la != la0 || lo != lo0 {
			t.Errorf("%v: another day the place has another cell", p)
		}
	}
}

// At a cell's edge the noise does not flip the cell; leaving it for good
// does, to the next one, a cell away.
func TestCoarseEdge(t *testing.T) {
	g := &Coarse{Offset: [2]float64{0.5, 0.5}}
	h := CoarseCellM / metresPerDegree
	edge := (10 - 0.5) * h
	la0, lo0 := g.Snap(edge-1e-6, 116.4)
	for _, d := range []float64{30, -30, 100, -100, 250, -250} {
		if la, lo := g.Snap(edge+d/metresPerDegree, 116.4); la != la0 || lo != lo0 {
			t.Fatalf("%+.0f m across the edge: a new cell", d)
		}
	}
	la1, lo1 := g.Snap(edge+600/metresPerDegree, 116.4)
	if la1 == la0 {
		t.Fatal("600 m into the next row and still in the old one")
	}
	if d := Distance(la0, lo0, la1, lo1); d < CoarseCellM*0.9 || d > CoarseCellM*1.1 {
		t.Fatalf("the next row is %.0f m away", d)
	}
	if la, _ := g.Snap(edge-30/metresPerDegree, 116.4); la != la1 {
		t.Fatal("30 m back across the edge flipped back")
	}
}

// The cells are about CellM wide both ways at any latitude, and every
// cell's centre is on Earth.
func TestCoarseCells(t *testing.T) {
	for _, p := range append(places, [2]float64{89.9999, 10}, [2]float64{-89.9999, -179.9999}) {
		g := &Coarse{Offset: [2]float64{0.37, 0.81}}
		la, lo := g.Snap(p[0], p[1])
		if math.Abs(la) > 90 || math.Abs(lo) > 180 || math.IsNaN(la+lo) {
			t.Fatalf("%v: off Earth %v %v", p, la, lo)
		}
		if math.Abs(p[0]) > 80 {
			continue
		}
		north := &Coarse{Offset: g.Offset}
		nla, nlo := north.Snap(p[0]+2.5*CoarseCellM/metresPerDegree, p[1])
		if d := Distance(nla, nlo, la, lo); d < CoarseCellM*1.9 || d > CoarseCellM*3.1 {
			t.Errorf("%v: 5 km north lands %.0f m away", p, d)
		}
		east := &Coarse{Offset: g.Offset}
		ela, elo := east.Snap(p[0], p[1]+2.5*CoarseCellM/metresPerDegree/math.Cos(p[0]*math.Pi/180))
		if d := Distance(ela, elo, la, lo); d < CoarseCellM*1.9 || d > CoarseCellM*3.1 {
			t.Errorf("%v: 5 km east lands %.0f m away", p, d)
		}
	}
}

// Another device's offset puts the lines elsewhere.
func TestCoarseOffset(t *testing.T) {
	a, b := &Coarse{Offset: [2]float64{0.1, 0.1}}, &Coarse{Offset: [2]float64{0.6, 0.6}}
	la, lo := a.Snap(39.9087, 116.3975)
	lb, ob := b.Snap(39.9087, 116.3975)
	if Distance(la, lo, lb, ob) < 300 {
		t.Fatal("two offsets, one grid")
	}
}
