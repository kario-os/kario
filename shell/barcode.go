package shell

import (
	"errors"
	"fmt"
	"strings"

	qrcode "github.com/skip2/go-qrcode"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ui"
)

// The codes the system draws: "qr:<text>" images are QR codes (ISO/IEC
// 18004), "bar:<text>" ones Code 128 barcodes (ISO/IEC 15417), the kind a
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
	// barQuiet and qrQuiet: the light modules each standard puts on every
	// side of its code.
	barQuiet = 10
	qrQuiet  = 4
	// codeQR and codeBar: the src prefixes, and FrameImage.Code.
	codeQR  = "qr"
	codeBar = "bar"
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

// A code is a system-drawn code ("qr:<text>", "bar:<text>") as its
// modules, quiet zones included (true: dark): a QR code's rows, or a
// barcode's one row.
type code struct {
	kind, text string
	rows       [][]bool
}

// codeOf reads src as a system-drawn code; nil when it is none or its text
// does not make one.
func codeOf(src string) *code {
	kind, text, ok := strings.Cut(src, ":")
	if !ok {
		return nil
	}
	switch kind {
	case codeQR:
		q, err := qrcode.New(text, qrcode.Low)
		if err != nil {
			return nil
		}
		q.DisableBorder = true
		bm := q.Bitmap()
		n := len(bm) + 2*qrQuiet
		rows := make([][]bool, n)
		for y := range rows {
			rows[y] = make([]bool, n)
			if y >= qrQuiet && y < n-qrQuiet {
				copy(rows[y][qrQuiet:], bm[y-qrQuiet])
			}
		}
		return &code{kind, text, rows}
	case codeBar:
		mods, err := barModules(text)
		if err != nil {
			return nil
		}
		return &code{kind, text, [][]bool{mods}}
	}
	return nil
}

// isCode: src is a system-drawn code's.
func isCode(src string) bool {
	return strings.HasPrefix(src, codeQR+":") || strings.HasPrefix(src, codeBar+":")
}

// dark: the module at column i of pixel row j, a barcode's every row the
// same.
func (k *code) dark(i, j int) bool {
	return k.rows[min(j, len(k.rows)-1)][i]
}

// tooSmall is what shows instead of a code that does not fit at one pixel a
// module; number is a barcode's text, which a till can take typed in.
func (k *code) tooSmall() (note, number string) {
	if k.kind == codeQR {
		return tr.T("The QR code does not fit the screen"), ""
	}
	number = k.text
	if strings.Trim(number, "0123456789") == "" {
		var b strings.Builder
		for i, r := range number {
			if i > 0 && i%4 == 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
		}
		number = b.String()
	}
	return tr.T("The barcode does not fit:"), number
}

// frame is the code for the pixel renderer, one pixel a module (a barcode a
// quarter of its width high), which it draws at a whole number of panel
// pixels a module or, when that does not fit, as the note and number.
func (k *code) frame(secure bool) *FrameImage {
	w, h := len(k.rows[0]), len(k.rows)
	if k.kind == codeBar {
		h = max(16, w/4)
	}
	pix := make([]byte, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !k.dark(x, y) {
				i := (y*w + x) * 3
				pix[i], pix[i+1], pix[i+2] = 255, 255, 255
			}
		}
	}
	f := encodeRGB(w, h, pix, secure, true)
	f.Code = k.kind
	f.CodeNote, f.CodeText = k.tooSmall()
	return f
}

// canvas is the code for the text renderer at a whole number of cells a
// module across and half cells down (about square), in at most maxW
// columns; when one does not fit, the note and the number (secure:
// masked).
func (k *code) canvas(maxW int, secure bool) *cv.Canvas {
	n := len(k.rows[0])
	if s := maxW / n; s > 0 {
		h := len(k.rows) * s
		if k.kind == codeBar {
			h = 6
		}
		b := cv.New(n*s, (h+1)/2, stBase)
		b.Pixels(0, 0, n*s, (h+1)/2, func(i, j int) cv.RGB {
			switch {
			case j >= h:
				return colBG
			case k.dark(i/s, j/s):
				return cv.RGB{0, 0, 0}
			}
			return cv.RGB{255, 255, 255}
		}, secure)
		return b
	}
	note, number := k.tooSmall()
	if w := cv.StringWidth(note) + 1 + cv.StringWidth(number); number != "" && w <= maxW {
		b := cv.New(maxW, 1, stBase)
		x := (maxW - w) / 2
		x += b.Text(x, 0, note, st(colDim, colBG), maxW) + 1
		if used := b.Text(x, 0, number, bold(stBase), maxW-x); secure {
			b.Mask(x, 0, used, 1)
		}
		return b
	}
	lines := cv.Wrap(note, maxW)
	var digits []string
	if number != "" {
		digits = cv.Wrap(number, maxW)
	}
	b := cv.New(maxW, len(lines)+len(digits), stBase)
	for i, l := range lines {
		b.Text((maxW-cv.StringWidth(l))/2, i, l, st(colDim, colBG), maxW)
	}
	for i, l := range digits {
		x, y := (maxW-cv.StringWidth(l))/2, len(lines)+i
		if used := b.Text(x, y, l, bold(stBase), maxW); secure {
			b.Mask(x, y, used, 1)
		}
	}
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
		if n.Bright && n.Kind == ui.KindImage && isCode(n.Src) {
			return true
		}
	}
	return false
}
