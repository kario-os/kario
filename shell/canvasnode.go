package shell

import (
	"encoding/base64"
	"strconv"
	"strings"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ui"
)

// Canvas nodes (ui.KindCanvas): the system paints the app's shapes into
// pixels itself, one pixel per unit, so every renderer shows them as it
// shows any picture the system made (pixel renderers stretch the image
// without smoothing, the text renderer draws half-block cells) and none
// needs to know about canvases. An app's drawing only ever fills its node:
// it cannot reach the status bar, the soft keys or a system dialog.

type raster struct {
	w, h int
	pix  []byte // RGB, row by row
}

func paintCanvas(cn *ui.Canvas) *raster {
	w, h := min(max(cn.W, 1), ui.MaxCanvasSide), min(max(cn.H, 1), ui.MaxCanvasSide)
	r := &raster{w: w, h: h, pix: make([]byte, w*h*3)}
	bg := parseColor(cn.Bg, cv.RGB{})
	for i := 0; i < w*h; i++ {
		r.pix[i*3], r.pix[i*3+1], r.pix[i*3+2] = bg.R, bg.G, bg.B
	}
	for i, op := range cn.Ops {
		if i == ui.MaxCanvasOps {
			break
		}
		r.draw(op, parseColor(op.Color, cv.RGB{R: 255, G: 255, B: 255}))
	}
	return r
}

func parseColor(s string, def cv.RGB) cv.RGB {
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "#"), 16, 32)
	if len(s) != 7 || s[0] != '#' || err != nil {
		return def
	}
	return cv.RGB{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v)}
}

func (r *raster) set(x, y int, c cv.RGB) {
	if x < 0 || y < 0 || x >= r.w || y >= r.h {
		return
	}
	i := (y*r.w + x) * 3
	r.pix[i], r.pix[i+1], r.pix[i+2] = c.R, c.G, c.B
}

func (r *raster) at(x, y int) cv.RGB {
	i := (min(max(y, 0), r.h-1)*r.w + min(max(x, 0), r.w-1)) * 3
	return cv.RGB{R: r.pix[i], G: r.pix[i+1], B: r.pix[i+2]}
}

func (r *raster) fillRect(x, y, w, h int, c cv.RGB) {
	for j := max(y, 0); j < min(y+h, r.h); j++ {
		for i := max(x, 0); i < min(x+w, r.w); i++ {
			r.set(i, j, c)
		}
	}
}

// line is Bresenham's, with a square brush for widths over one.
func (r *raster) line(x0, y0, x1, y1, width int, c cv.RGB) {
	width = max(width, 1)
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := sign(x1-x0), sign(y1-y0)
	e := dx + dy
	for n := 0; n <= 2*ui.MaxCanvasSide; n++ {
		r.fillRect(x0-(width-1)/2, y0-(width-1)/2, width, width, c)
		if x0 == x1 && y0 == y1 {
			return
		}
		if e2 := 2 * e; e2 >= dy {
			e, x0 = e+dy, x0+sx
		}
		if e2 := 2 * e; e2 <= dx {
			e, y0 = e+dx, y0+sy
		}
	}
}

func (r *raster) draw(op ui.DrawOp, c cv.RGB) {
	switch op.Op {
	case "dot":
		r.set(op.X, op.Y, c)
	case "rect":
		if op.Fill {
			r.fillRect(op.X, op.Y, op.W, op.H, c)
			return
		}
		x1, y1 := op.X+op.W-1, op.Y+op.H-1
		r.line(op.X, op.Y, x1, op.Y, 1, c)
		r.line(op.X, y1, x1, y1, 1, c)
		r.line(op.X, op.Y, op.X, y1, 1, c)
		r.line(x1, op.Y, x1, y1, 1, c)
	case "line":
		r.line(op.X, op.Y, op.X2, op.Y2, op.Width, c)
	case "circle":
		rr := min(max(op.R, 0), ui.MaxCanvasSide)
		for j := -rr; j <= rr; j++ {
			for i := -rr; i <= rr; i++ {
				d := i*i + j*j
				// The ring is the pixels whose distance rounds to the radius.
				if d <= rr*rr+rr && (op.Fill || d >= rr*rr-rr) {
					r.set(op.X+i, op.Y+j, c)
				}
			}
		}
	case "poly":
		pts := op.Pts[:len(op.Pts)/2*2]
		if len(pts) < 4 {
			return
		}
		if op.Fill {
			r.fillPoly(pts, c)
			return
		}
		for k := 2; k < len(pts); k += 2 {
			r.line(pts[k-2], pts[k-1], pts[k], pts[k+1], op.Width, c)
		}
	}
}

// fillPoly fills by the even-odd rule, testing each row at pixel centres.
func (r *raster) fillPoly(pts []int, c cv.RGB) {
	n := len(pts) / 2
	for y := 0; y < r.h; y++ {
		fy := float64(y) + 0.5
		var xs []float64
		for k := 0; k < n; k++ {
			x0, y0 := float64(pts[2*k]), float64(pts[2*k+1])
			x1, y1 := float64(pts[2*((k+1)%n)]), float64(pts[2*((k+1)%n)+1])
			if (y0 <= fy) != (y1 <= fy) {
				xs = append(xs, x0+(fy-y0)*(x1-x0)/(y1-y0))
			}
		}
		for i := 1; i < len(xs); i++ {
			for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
				xs[j], xs[j-1] = xs[j-1], xs[j]
			}
		}
		for k := 0; k+1 < len(xs); k += 2 {
			for x := int(xs[k] + 0.5); x < int(xs[k+1]+0.5); x++ {
				r.set(x, y, c)
			}
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sign(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// canvasKey names a key a canvas with Keys gets, "" for the system's own:
// OK and the arrows by name, a digit, a letter, * or # as itself ("/" stays
// the command palette).
func canvasKey(k Key) string {
	switch k.Name {
	case KeyUp, KeyDown, KeyLeft, KeyRight, KeyEnter:
		return k.Name
	case KeyRunes:
		if r := k.Runes; len(r) == 1 && (r[0] >= '0' && r[0] <= '9' || r[0] >= 'a' && r[0] <= 'z' || r[0] >= 'A' && r[0] <= 'Z' || r[0] == '*' || r[0] == '#') {
			return string(r)
		}
	}
	return ""
}

// frameCanvas is the canvas as the pixel renderers get it: a picture.
func frameCanvas(cn *ui.Canvas) *FrameImage {
	r := paintCanvas(cn)
	return &FrameImage{W: r.w, H: r.h, RGB: base64.StdEncoding.EncodeToString(r.pix), Pixelated: true}
}

// drawCanvasNode is the canvas in the text renderer: as many cells across as
// fit (two pixels a cell down), each sampled from the painted pixels.
func (c *Core) drawCanvasNode(n *ui.Node, W int, focused bool) *cv.Canvas {
	r := paintCanvas(n.Canvas)
	cols := min(W-2, max(r.w, 1))
	rows := max(1, (r.h*cols/r.w+1)/2)
	b := cv.New(W, rows, stBase)
	x0 := (W - cols) / 2
	b.Pixels(x0, 0, cols, rows, func(i, j int) cv.RGB { return r.at(i*r.w/cols, j*r.h/(rows*2)) }, false)
	if focused {
		for row := 0; row < rows; row++ {
			b.Text(0, row, "▌", st(colAccent, colBG), 1)
		}
	}
	return b
}
