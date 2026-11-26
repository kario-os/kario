package shell

import (
	"errors"
	"fmt"
	"strings"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ui"
)

// "bar:<text>" images are Code 128 barcodes (ISO/IEC 15417), the kind a
// shop's laser scanner reads; payment codes show one beside the QR code.

// code128 is the symbol table: bar and space widths in modules, value by
// value; the last is the stop pattern.
var code128 = strings.Fields(`
212222 222122 222221 121223 121322 131222 122213 122312 132212 221213
221312 231212 112232 122132 122231 113222 123122 123221 223211 221132
221231 213212 223112 312131 311222 321122 321221 312212 322112 322211
212123 212321 232121 111323 131123 131321 112313 132113 132311 211313
231113 231311 112133 112331 132131 113123 113321 133121 313121 211331
231131 213113 213311 213131 311123 311321 331121 312113 312311 332111
314111 221411 431111 111224 111422 121124 121421 141122 141221 112214
112412 122114 122411 142112 142211 241211 221114 413111 241112 134111
111242 121142 121241 114212 124112 124211 411212 421112 421211 212141
214121 412121 111143 111341 131141 114113 114311 411113 411311 113141
114131 311141 411131 211412 211214 211232 2331112`)

const (
	code128StartB = 104
	code128StartC = 105
	code128Stop   = 106
	// barQuiet: the blank modules a scanner needs on each side.
	barQuiet = 10
)

// barModules is text as Code 128 modules (true: a bar), quiet zones
// included: code set C for an even number of digits (payment codes are
// 18), B for printable ASCII otherwise.
func barModules(text string) ([]bool, error) {
	if text == "" || len(text) > ui.BarMaxLen {
		return nil, fmt.Errorf("a barcode holds 1 to %d characters", ui.BarMaxLen)
	}
	digits := len(text)%2 == 0
	for _, r := range text {
		if r < ' ' || r > '~' {
			return nil, errors.New("a barcode holds printable ASCII only")
		}
		digits = digits && r >= '0' && r <= '9'
	}
	vals := []int{code128StartB}
	if digits {
		vals[0] = code128StartC
		for i := 0; i < len(text); i += 2 {
			vals = append(vals, int(text[i]-'0')*10+int(text[i+1]-'0'))
		}
	} else {
		for i := 0; i < len(text); i++ {
			vals = append(vals, int(text[i]-' '))
		}
	}
	sum := vals[0]
	for i, v := range vals[1:] {
		sum += (i + 1) * v
	}
	vals = append(vals, sum%103, code128Stop)
	mods := make([]bool, barQuiet)
	for _, v := range vals {
		for i, w := range code128[v] {
			for range int(w - '0') {
				mods = append(mods, i%2 == 0)
			}
		}
	}
	return append(mods, make([]bool, barQuiet)...), nil
}

// barFrame is a barcode for the pixel renderer, one pixel per module, a
// quarter of its width high.
func barFrame(text string, secure bool) *FrameImage {
	mods, err := barModules(text)
	if err != nil {
		return nil
	}
	w, h := len(mods), max(16, len(mods)/4)
	pix := make([]byte, w*h*3)
	for y := 0; y < h; y++ {
		for x, bar := range mods {
			if !bar {
				i := (y*w + x) * 3
				pix[i], pix[i+1], pix[i+2] = 255, 255, 255
			}
		}
	}
	return encodeRGB(w, h, pix, secure, true)
}

// barCanvas is the text renderer's barcode, squeezed to maxW columns: a
// picture of the code, not one a scanner reads.
func barCanvas(text string, maxW int, secure bool) *cv.Canvas {
	mods, err := barModules(text)
	if err != nil {
		return cv.New(1, 1, stBase)
	}
	w, h := max(1, min(maxW, len(mods))), 3
	b := cv.New(w, h, stBase)
	b.Pixels(0, 0, w, h, func(i, j int) cv.RGB {
		if mods[i*len(mods)/w] {
			return cv.RGB{0, 0, 0}
		}
		return cv.RGB{255, 255, 255}
	}, secure)
	return b
}

// codeShown: a code to be scanned (Node.Bright) is on the screen in front,
// nothing over it.
func (c *Core) codeShown() bool {
	if c.screen.off || c.Locked() || len(c.overlays) > 0 {
		return false
	}
	v := c.current().top()
	if v == nil {
		return false
	}
	for _, n := range v.S.Body {
		if n.Bright && n.Kind == ui.KindImage && (strings.HasPrefix(n.Src, "qr:") || strings.HasPrefix(n.Src, "bar:")) {
			return true
		}
	}
	return false
}
