package media

// Gray is a picture's luminance, a byte a pixel: what the scanner's decoder
// reads (a camera's Y plane).
type Gray struct {
	W, H int
	Pix  []byte
}

func NewGray(w, h int) *Gray { return &Gray{W: w, H: h, Pix: make([]byte, w*h)} }

// GraySource draws what a source shows at time t, in grey.
type GraySource func(t float64, g *Gray)

func luma(r, g, b uint8) uint8 { return uint8((77*int(r) + 150*int(g) + 29*int(b)) >> 8) }

// FromFrame fills g with f's luminance, scaled to g's size.
func (g *Gray) FromFrame(f *Frame) {
	for y := 0; y < g.H; y++ {
		sy := y * f.H / g.H
		for x := 0; x < g.W; x++ {
			i := (sy*f.W + x*f.W/g.W) * 3
			g.Pix[y*g.W+x] = luma(f.Pix[i], f.Pix[i+1], f.Pix[i+2])
		}
	}
}

// blur box-blurs the rectangle [x0, x1) × [y0, y1) with radius r, rows then
// columns.
func (g *Gray) blur(x0, y0, x1, y1, r int) {
	x0, y0, x1, y1 = max(x0, 0), max(y0, 0), min(x1, g.W), min(y1, g.H)
	if r <= 0 || x1-x0 <= 2*r || y1-y0 <= 2*r {
		return
	}
	n := 2*r + 1
	line := make([]int, max(x1-x0, y1-y0))
	for y := y0; y < y1; y++ {
		row := g.Pix[y*g.W:]
		for x := x0; x < x1; x++ {
			line[x-x0] = int(row[x])
		}
		sum := 0
		for x := x0; x < x0+n-1; x++ {
			sum += line[x-x0]
		}
		for x := x0 + r; x < x1-r; x++ {
			sum += line[x+r-x0]
			row[x] = byte(sum / n)
			sum -= line[x-r-x0]
		}
	}
	for x := x0; x < x1; x++ {
		for y := y0; y < y1; y++ {
			line[y-y0] = int(g.Pix[y*g.W+x])
		}
		sum := 0
		for y := y0; y < y0+n-1; y++ {
			sum += line[y-y0]
		}
		for y := y0 + r; y < y1-r; y++ {
			sum += line[y+r-y0]
			g.Pix[y*g.W+x] = byte(sum / n)
			sum -= line[y-r-y0]
		}
	}
}

// noise adds sensor noise of up to ±amount grey levels to the rectangle,
// the same for the same seed.
func (g *Gray) noise(x0, y0, x1, y1, amount int, seed uint32) {
	x0, y0, x1, y1 = max(x0, 0), max(y0, 0), min(x1, g.W), min(y1, g.H)
	if amount <= 0 {
		return
	}
	s, span := seed*2654435761|1, uint32(2*amount+1)
	for y := y0; y < y1; y++ {
		row := g.Pix[y*g.W:]
		for x := x0; x < x1; x++ {
			s ^= s << 13
			s ^= s >> 17
			s ^= s << 5
			v := int(row[x]) + int((s>>16)*span>>16) - amount
			row[x] = byte(min(max(v, 0), 255))
		}
	}
}
