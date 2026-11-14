package media

import (
	"math"

	qrcode "github.com/skip2/go-qrcode"
)

// Synthetic sources stand in for the camera and the remote call peer so the
// whole pipeline can be exercised without hardware.

type Source func(t float64, f *Frame)

func clamp(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func inEllipse(x, y, cx, cy, rx, ry float64) bool {
	dx, dy := (x-cx)/rx, (y-cy)/ry
	return dx*dx+dy*dy <= 1
}

type person struct {
	skin, hair, shirt [3]float64
}

func drawScene(t float64, f *Frame, bg func(u, v float64) [3]float64, p person, sway float64, blink bool) {
	cx := 0.5 + sway
	for y := 0; y < f.H; y++ {
		for x := 0; x < f.W; x++ {
			u, v := (float64(x)+0.5)/float64(f.W), (float64(y)+0.5)/float64(f.H)
			c := bg(u, v)
			switch {
			case inEllipse(u, v, cx, 1.05, 0.42, 0.28):
				c = p.shirt
			case inEllipse(u, v, cx, 0.36, 0.22, 0.2) && v < 0.36:
				c = p.hair
			case inEllipse(u, v, cx, 0.5, 0.19, 0.26):
				c = p.skin
				shade := 1 - 0.25*math.Max(0, (u-cx)/0.19)
				c = [3]float64{c[0] * shade, c[1] * shade, c[2] * shade}
				if !blink && (inEllipse(u, v, cx-0.07, 0.47, 0.025, 0.03) || inEllipse(u, v, cx+0.07, 0.47, 0.025, 0.03)) {
					c = [3]float64{30, 25, 25}
				}
				if inEllipse(u, v, cx, 0.62, 0.06, 0.02+0.015*math.Abs(math.Sin(t*6))) {
					c = [3]float64{150, 60, 60}
				}
			}
			f.Set(x, y, clamp(c[0]), clamp(c[1]), clamp(c[2]))
		}
	}
}

// Camera is the local "selfie" camera.
func Camera(t float64, f *Frame) {
	light := 0.5 + 0.5*math.Sin(t*0.7)
	drawScene(t, f, func(u, v float64) [3]float64 {
		return [3]float64{70 + 40*u + 30*light, 90 + 30*v, 120 + 50*(1-u)}
	}, person{skin: [3]float64{230, 190, 160}, hair: [3]float64{40, 30, 25}, shirt: [3]float64{40, 70, 140}},
		0.06*math.Sin(t*1.3), math.Mod(t, 4) < 0.15)
}

// Peer is the simulated remote participant, with a bouncing ball so motion is
// obvious even at very low resolution.
func Peer(t float64, f *Frame) {
	drawScene(t, f, func(u, v float64) [3]float64 {
		return [3]float64{200 - 60*v, 150 + 40*u, 90}
	}, person{skin: [3]float64{220, 170, 130}, hair: [3]float64{120, 60, 20}, shirt: [3]float64{180, 40, 50}},
		0.12*math.Sin(t*0.9), math.Mod(t+1.7, 3.5) < 0.15)
	bx := 0.15 + 0.7*math.Abs(math.Mod(t*0.35, 2)-1)
	by := 0.2 + 0.25*math.Abs(math.Sin(t*2.2))
	for y := 0; y < f.H; y++ {
		for x := 0; x < f.W; x++ {
			u, v := (float64(x)+0.5)/float64(f.W), (float64(y)+0.5)/float64(f.H)
			if inEllipse(u, v, bx, by, 0.06, 0.06*float64(f.W)/float64(f.H)) {
				f.Set(x, y, 250, 230, 40)
			}
		}
	}
}

func Off(_ float64, f *Frame) {
	for i := range f.Pix {
		f.Pix[i] = 25
	}
}

// Scanner renders a QR code lying on a table, as seen by a shaking hand-held
// camera. The prototype "decodes" by returning the payload it drew.
func Scanner(payload string) Source {
	q, err := qrcode.New(payload, qrcode.Low)
	var bm [][]bool
	if err == nil {
		q.DisableBorder = true
		bm = q.Bitmap()
	}
	return func(t float64, f *Frame) {
		n := len(bm)
		ox := f.W/2 - n/2 + int(math.Round(1.5*math.Sin(t*3.1)))
		oy := f.H/2 - n/2 + int(math.Round(1.5*math.Cos(t*2.3)))
		for y := 0; y < f.H; y++ {
			for x := 0; x < f.W; x++ {
				r, g, b := 120.0+10*math.Sin(float64(x)*0.9), 80.0, 50.0
				mx, my := x-ox, y-oy
				if mx >= -2 && my >= -2 && mx < n+2 && my < n+2 {
					r, g, b = 235, 235, 230
					if mx >= 0 && my >= 0 && mx < n && my < n && bm[my][mx] {
						r, g, b = 15, 15, 20
					}
				}
				f.Set(x, y, clamp(r), clamp(g), clamp(b))
			}
		}
	}
}
