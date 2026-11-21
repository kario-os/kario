package shell

import (
	"strings"
	"time"

	cv "github.com/kario-os/kario/canvas"
)

// The boot animation: an original design, not Nokia's handshake. The twelve
// keys of the pad light up in turn, then the name and a progress bar. The
// shell owns the timing; each renderer draws it its own way.

const bootDuration = 1500 * time.Millisecond

// RestartBoot plays the animation from the start (once the renderer is up).
func (c *Core) RestartBoot() { c.booted = time.Now() }

// BootProgress is 0…1 while the boot animation plays, -1 afterwards.
func (c *Core) BootProgress() float64 {
	t := time.Since(c.booted)
	if t >= bootDuration {
		return -1
	}
	return float64(t) / float64(bootDuration)
}

var padKeys = []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "0", "#"}

func (c *Core) drawBoot(can *cv.Canvas, p float64) {
	W, H := c.cfg.W, c.cfg.H
	bg := cv.RGB{R: 6, G: 8, B: 14}
	can.Fill(0, 0, W, H, st(colFG, bg))
	lit := int(p * 1.6 * float64(len(padKeys)))
	x0, y0 := (W-15)/2, H/2-6
	for i, k := range padKeys {
		s := st(colDim, bg)
		if i < lit {
			s = bold(st(colBG, colAccent))
		}
		can.Text(x0+(i%3)*5, y0+(i/3)*2, " "+k+" ", s, 3)
	}
	if p > 0.55 {
		name := strings.Join(strings.Split(Brand, ""), " ")
		can.Text((W-cv.StringWidth(name))/2, y0+9, name, bold(st(colFG, bg)), W)
		tag := tr.T("The keyboard-first phone system")
		can.Text((W-cv.StringWidth(tag))/2, y0+11, tag, st(colDim, bg), W)
	}
	bar := 20
	done := int(p * float64(bar))
	can.Text((W-bar)/2, H-3, strings.Repeat("━", done)+strings.Repeat("─", bar-done), st(colAccent, bg), bar)
}
