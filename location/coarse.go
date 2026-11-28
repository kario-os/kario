package location

import "math"

// CoarseCellM is the side of the cells an approximate location is snapped
// to (Android's LocationFudger: 2000 m).
const CoarseCellM = 2000.0

// coarseMargin is how far, in cells, a fix may stray past the cell it was
// last snapped to and still be given that cell: the noise of fixes taken
// at a cell's edge would otherwise flip between two cells and show where
// the edge, and so the phone, is.
const coarseMargin = 0.15

const metresPerDegree = 111320.0

// Coarse snaps fixes to the centre of their cell on a grid of CellM
// (CoarseCellM if 0), shifted by Offset (fractions of a cell, random per
// device) so its lines fall nowhere known. A place gets the same cell
// every time, so fixes averaged tell no more than the cell; only just past
// the edge of the cell it was last given does it keep that one. Cells are
// as wide east to west as north to south at any latitude.
type Coarse struct {
	CellM  float64
	Offset [2]float64

	last     bool
	row, col float64
}

func (g *Coarse) cellDeg() float64 {
	if g.CellM > 0 {
		return g.CellM / metresPerDegree
	}
	return CoarseCellM / metresPerDegree
}

// Snap is the centre of the cell of a position; latitude first, as the
// width of a row's cells depends on it.
func (g *Coarse) Snap(lat, lon float64) (float64, float64) {
	h := g.cellDeg()
	y := lat/h + g.Offset[0]
	row := math.Floor(y)
	if g.last && y > g.row-coarseMargin && y < g.row+1+coarseMargin {
		row = g.row
	}
	la := math.Max(-90, math.Min(90, (row+0.5-g.Offset[0])*h))
	w := h / math.Max(math.Cos(la*math.Pi/180), 0.01)
	x := lon/w + g.Offset[1]
	if g.last && row == g.row {
		// Across the antimeridian the last column is a turn of the
		// Earth away.
		turn := 360 / w
		x -= math.Round((x-g.col-0.5)/turn) * turn
	}
	col := math.Floor(x)
	if g.last && row == g.row && x > g.col-coarseMargin && x < g.col+1+coarseMargin {
		col = g.col
	}
	g.last, g.row, g.col = true, row, col
	lo := math.Mod((col+0.5-g.Offset[1])*w+540, 360) - 180
	return la, lo
}

// Distance is the great-circle distance between two positions, in metres.
func Distance(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371008.8
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	s := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(s)))
}
