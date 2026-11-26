package media

import (
	"errors"
	"math"
	"sync"

	qrcode "github.com/skip2/go-qrcode"
)

// A Card is a QR code printed on a white card: what the synthetic scenes
// hold in front of the scanner's camera, the synthetic camera's table or the
// emulator's vivid picture behind it. The decoder has to read it from the
// pixels drawn, as from a real camera's.
type Card struct {
	n    int
	dark []bool
}

// cardQuiet is the card's white margin around the code, in modules.
const cardQuiet = 3

// NewCard prints text as a QR code with error correction level M, the
// usual one of payment codes.
func NewCard(text string) (*Card, error) {
	q, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		return nil, err
	}
	q.DisableBorder = true
	return CardOf(q.Bitmap())
}

// CardOf prints a code given as its modules (true: dark), rows of a square.
func CardOf(bm [][]bool) (*Card, error) {
	n := len(bm)
	if n < 21 || n > 177 {
		return nil, errors.New("not a QR code's size")
	}
	c := &Card{n: n, dark: make([]bool, n*n)}
	for y, row := range bm {
		if len(row) != n {
			return nil, errors.New("not a square")
		}
		copy(c.dark[y*n:], row)
	}
	return c, nil
}

// A View is how the camera sees a card: where its corners fall (top left,
// top right, bottom right, bottom left of the card, from the frame's centre
// in fractions of its shorter side), the defocus (a box blur's radius in
// pixels) and the sensor's noise (± grey levels; the same for the same
// seed). The blur and the noise apply to the decoder's grey picture.
type View struct {
	Corners [4][2]float64
	Blur    int
	Noise   int
	Seed    uint32
}

// Pose places a card half (a fraction of the frame's shorter side) wide on
// each side of its centre, turned by angle radians, its top edge tilted
// away from the camera by tilt (0 faces it; the far edge looks narrower),
// its centre moved by dx, dy.
func Pose(half, angle, tilt, dx, dy float64) [4][2]float64 {
	far := half * (1 - tilt)
	pts := [4][2]float64{{-far, -half * (1 - tilt/2)}, {far, -half * (1 - tilt/2)}, {half, half}, {-half, half}}
	sin, cos := math.Sincos(angle)
	for i, p := range pts {
		pts[i] = [2]float64{dx + p[0]*cos - p[1]*sin, dy + p[0]*sin + p[1]*cos}
	}
	return pts
}

// HeldView is a card held up to the camera by a hand that shakes: tilted,
// turning a little and drifting about the middle of the frame.
func HeldView(t float64) View {
	return View{
		Corners: Pose(0.36,
			0.07*math.Sin(t*1.7)+0.025*math.Sin(t*5.3),
			0.06+0.04*math.Sin(t*0.9),
			0.025*math.Sin(t*3.1)+0.01*math.Sin(t*7.7),
			0.02+0.025*math.Cos(t*2.3)+0.01*math.Sin(t*6.1)),
		Blur:  1,
		Noise: 10,
		Seed:  uint32(t * 1000),
	}
}

// mat3 is a homography, row by row: (x, y, w) = m · (u, v, 1).
type mat3 [9]float64

// squareToQuad maps the unit square's corners (0,0), (1,0), (1,1), (0,1)
// onto p.
func squareToQuad(p [4][2]float64) mat3 {
	x0, y0, x1, y1, x2, y2, x3, y3 := p[0][0], p[0][1], p[1][0], p[1][1], p[2][0], p[2][1], p[3][0], p[3][1]
	dx3, dy3 := x0-x1+x2-x3, y0-y1+y2-y3
	var g, h float64
	if dx3 != 0 || dy3 != 0 {
		dx1, dx2, dy1, dy2 := x1-x2, x3-x2, y1-y2, y3-y2
		den := dx1*dy2 - dx2*dy1
		g, h = (dx3*dy2-dx2*dy3)/den, (dx1*dy3-dx3*dy1)/den
	}
	return mat3{x1 - x0 + g*x1, x3 - x0 + h*x3, x0, y1 - y0 + g*y1, y3 - y0 + h*y3, y0, g, h, 1}
}

func (m mat3) inverse() mat3 {
	a, b, c, d, e, f, g, h, i := m[0], m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8]
	return mat3{e*i - f*h, c*h - b*i, b*f - c*e, f*g - d*i, a*i - c*g, c*d - a*f, d*h - e*g, b*g - a*h, a*e - b*d}
}

// place is where v puts the card in a w×h frame: the map from the frame's
// pixels to the card's unit square, and the pixels it covers.
func (v View) place(w, h int) (inv mat3, x0, y0, x1, y1 int) {
	m := float64(min(w, h))
	var px [4][2]float64
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i, c := range v.Corners {
		px[i] = [2]float64{float64(w)/2 + c[0]*m, float64(h)/2 + c[1]*m}
		minX, maxX = math.Min(minX, px[i][0]), math.Max(maxX, px[i][0])
		minY, maxY = math.Min(minY, px[i][1]), math.Max(maxY, px[i][1])
	}
	clampi := func(f float64, hi int) int { return int(math.Max(0, math.Min(f, float64(hi)))) }
	return squareToQuad(px).inverse(), clampi(math.Floor(minX), w), clampi(math.Floor(minY), h),
		clampi(math.Ceil(maxX)+1, w), clampi(math.Ceil(maxY)+1, h)
}

// ink is what the frame's point (px, py) shows of the card: 0 paper, 1 a
// dark module, -1 not the card.
func (c *Card) ink(inv mat3, px, py float64) int {
	w := inv[6]*px + inv[7]*py + inv[8]
	return c.module((inv[0]*px+inv[1]*py+inv[2])/w, (inv[3]*px+inv[4]*py+inv[5])/w)
}

// module is ink at (s, u) of the card's unit square.
func (c *Card) module(s, u float64) int {
	if !(s >= 0 && s < 1 && u >= 0 && u < 1) {
		return -1
	}
	t := float64(c.n + 2*cardQuiet)
	mx, my := int(s*t)-cardQuiet, int(u*t)-cardQuiet
	if mx >= 0 && my >= 0 && mx < c.n && my < c.n && c.dark[my*c.n+mx] {
		return 1
	}
	return 0
}

// paper is the card's shading: lit from the top left.
func paper(x, y, w, h int) float64 {
	return 1 - 0.12*float64(x)/float64(w) - 0.08*float64(y)/float64(h)
}

// DrawGray draws the card into g where v puts it, over what g holds, then
// blurs and adds noise to the part it covers.
func (c *Card) DrawGray(g *Gray, v View) {
	inv, x0, y0, x1, y1 := v.place(g.W, g.H)
	drawn := false
	for y := y0; y < y1; y++ {
		py, px := float64(y)+0.5, float64(x0)+0.5
		sn, un, wn := inv[0]*px+inv[1]*py+inv[2], inv[3]*px+inv[4]*py+inv[5], inv[6]*px+inv[7]*py+inv[8]
		lit, row := paper(x0, y, g.W, g.H), g.Pix[y*g.W:]
		for x := x0; x < x1; x++ {
			switch c.module(sn/wn, un/wn) {
			case 1:
				row[x], drawn = 36, true
			case 0:
				row[x], drawn = clamp(228*lit), true
			}
			sn, un, wn, lit = sn+inv[0], un+inv[3], wn+inv[6], lit-0.12/float64(g.W)
		}
	}
	if !drawn {
		return
	}
	g.blur(x0-v.Blur, y0-v.Blur, x1+v.Blur, y1+v.Blur, v.Blur)
	g.noise(x0, y0, x1, y1, v.Noise, v.Seed)
}

// Draw draws the card into f where v puts it, four samples a pixel, for
// the viewfinder.
func (c *Card) Draw(f *Frame, v View) {
	inv, x0, y0, x1, y1 := v.place(f.W, f.H)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			on, dark := 0, 0
			for i := 0; i < 4; i++ {
				switch c.ink(inv, float64(x)+0.25+0.5*float64(i&1), float64(y)+0.25+0.5*float64(i>>1)) {
				case 1:
					on, dark = on+1, dark+1
				case 0:
					on++
				}
			}
			if on == 0 {
				continue
			}
			r, g, b := f.At(x, y)
			lit := paper(x, y, f.W, f.H)
			mix := func(bg uint8, p, ink float64) uint8 {
				return clamp((float64(bg)*float64(4-on) + p*lit*float64(on-dark) + ink*float64(dark)) / 4)
			}
			f.Set(x, y, mix(r, 240, 24), mix(g, 236, 24), mix(b, 224, 30))
		}
	}
}

// Table is the synthetic camera's view for scanning: a wooden table.
func Table(_ float64, f *Frame) {
	for y := 0; y < f.H; y++ {
		for x := 0; x < f.W; x++ {
			r, g, b := wood(x, y, f.W, f.H)
			f.Set(x, y, r, g, b)
		}
	}
}

// TableLook is the table in grey. It does not move: drawn once a size.
func TableLook(_ float64, g *Gray) {
	tableGray.Lock()
	defer tableGray.Unlock()
	if tableGray.w != g.W || tableGray.h != g.H {
		tableGray.w, tableGray.h, tableGray.pix = g.W, g.H, make([]byte, g.W*g.H)
		for y := 0; y < g.H; y++ {
			for x := 0; x < g.W; x++ {
				tableGray.pix[y*g.W+x] = luma(wood(x, y, g.W, g.H))
			}
		}
	}
	copy(g.Pix, tableGray.pix)
}

var tableGray struct {
	sync.Mutex
	w, h int
	pix  []byte
}

// sines is a period of sin in 1024 steps: math.Sin for every pixel of the
// table would cost more than decoding the frame.
var sines = func() (s [1024]float32) {
	for i := range s {
		s[i] = float32(math.Sin(float64(i) * 2 * math.Pi / 1024))
	}
	return
}()

func fastSin(a float64) float64 { return float64(sines[int(a*(1024/(2*math.Pi)))&1023]) }

func wood(x, y, w, h int) (uint8, uint8, uint8) {
	u, v := float64(x)/float64(w), float64(y)/float64(h)
	grain := fastSin(u*55 + 3*fastSin(v*7+u*2))
	return clamp(126 + 16*grain), clamp(84 + 9*grain), clamp(52 + 6*grain)
}

// ScanScene is what the scanner's camera sees: bg (look in grey) with the
// card held() returns, if any, held up in front of it. show is for the
// viewfinder, look for the decoder.
func ScanScene(bg Source, look GraySource, held func() *Card) (Source, GraySource) {
	return func(t float64, f *Frame) {
			bg(t, f)
			if c := held(); c != nil {
				c.Draw(f, HeldView(t))
			}
		}, func(t float64, g *Gray) {
			look(t, g)
			if c := held(); c != nil {
				c.DrawGray(g, HeldView(t))
			}
		}
}
