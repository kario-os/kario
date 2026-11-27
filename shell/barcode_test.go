package shell

import (
	"encoding/base64"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/oned"
	zxqr "github.com/makiuchi-d/gozxing/qrcode"
	goqr "github.com/skip2/go-qrcode"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/tools/prebuilt"
	"github.com/kario-os/kario/ui"
)

// readBar reads a barcode image with ZXing's Code 128 reader, the
// decoder scanners are compared against.
func readBar(t *testing.T, img image.Image) (string, error) {
	t.Helper()
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatal(err)
	}
	res, err := oned.NewCode128Reader().Decode(bmp, map[gozxing.DecodeHintType]any{gozxing.DecodeHintType_PURE_BARCODE: true})
	if err != nil {
		return "", err
	}
	return res.GetText(), nil
}

// shownAt is the frame image as a screen shows it w pixels wide, each
// pixel taking the image's nearest (renderers draw codes pixelated).
func shownAt(f *FrameImage, w int) image.Image {
	pix, _ := base64.StdEncoding.DecodeString(f.RGB)
	h := f.H * w / f.W
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetGray(x, y, color.Gray{Y: pix[((y*f.H/h)*f.W+x*f.W/w)*3]})
		}
	}
	return img
}

func TestBarcodeReads(t *testing.T) {
	for _, text := range []string{"281234567890123456", "28", "PAYCODE-U8848", "a", "12345", strings.Repeat("9", ui.BarMaxLen), "~ !"} {
		k := codeOf("bar:" + text)
		if k == nil {
			t.Fatalf("%q: no barcode", text)
		}
		f := k.frame(false)
		for _, k := range []int{1, 2, 3} {
			if got, err := readBar(t, shownAt(f, f.W*k)); err != nil || got != text {
				t.Errorf("%q at %d pixels a module: read %q, %v", text, k, got, err)
			}
		}
	}
	for _, bad := range []string{"", "\u652f\u4ed8", "tab\there", strings.Repeat("1", ui.BarMaxLen+2)} {
		if _, err := barModules(bad); err == nil {
			t.Errorf("%q made a barcode", bad)
		}
	}
	pay := codeOf("bar:281234567890123456").frame(false)
	for _, w := range []int{240, 320, 480, 720} {
		if got, err := readBar(t, shownAt(pay, w)); err != nil {
			t.Errorf("a payment code drawn %d pixels wide: %q, %v", w, got, err)
		}
	}
	mods, _ := barModules("281234567890123456")
	if len(mods) != 2*barQuiet+11*11+13 {
		t.Errorf("18 digits take %d modules, not code set C's %d", len(mods), 2*barQuiet+11*11+13)
	}
	for i := 0; i < barQuiet; i++ {
		if mods[i] || mods[len(mods)-1-i] {
			t.Fatal("no quiet zone")
		}
	}
}

// The codes phone-paint draws (as built here: make paint) read at each
// form factor's size and on a 240x320 panel at one pixel a logical one,
// where a barcode drawn at 1.48 pixels a module did not; each is drawn at
// a whole number of pixels a module with its whole quiet zone.
func TestCodesReadOnEveryScreen(t *testing.T) {
	paint := prebuilt.Paint(t)
	pay := "281234567890123456"
	long := "https://pay.example.cn/c/" + strings.Repeat("0123456789", 7) + "ABCDE"
	q, err := goqr.New(long, goqr.Low)
	if err != nil || len(long) != 100 {
		t.Fatal(len(long), err)
	}
	q.DisableBorder = true
	qrMods := len(q.Bitmap()) + 2*qrQuiet
	barMods, _ := barModules(pay)
	for _, id := range []string{"bar-t9", "bar-qwerty", "communicator", "bar-t9:240x320@1"} {
		t.Run(id, func(t *testing.T) {
			dev, _ := DeviceByID(id)
			c, g, snaps := paintCore(t, paint, dev)
			a := c.current()
			base := a.stack
			shown := func(name string, body ...ui.Node) (image.Image, *gozxing.BinaryBitmap) {
				t.Helper()
				a.stack = append(base[:len(base):len(base)], newView(ui.Screen{ID: name, Title: "Code", Body: body}))
				c.Render()
				if err := g.Snap(name); err != nil {
					t.Fatal(err)
				}
				img, err := readPNG(filepath.Join(snaps, name+".png"))
				if err != nil {
					t.Fatal(err)
				}
				if b := img.Bounds(); b.Dx() != dev.Width || b.Dy() != dev.Height {
					t.Fatalf("snapshot %v, not the panel's %dx%d", b, dev.Width, dev.Height)
				}
				bmp, err := gozxing.NewBinaryBitmapFromImage(img)
				if err != nil {
					t.Fatal(err)
				}
				return img, bmp
			}
			hard := map[gozxing.DecodeHintType]any{gozxing.DecodeHintType_TRY_HARDER: true}
			img, bmp := shown("bar", ui.Node{Kind: ui.KindImage, Src: "bar:" + pay, Bright: true}, ui.Node{Kind: ui.KindText, Text: "Refreshes every 30 seconds"})
			res, err := oned.NewCode128Reader().Decode(bmp, hard)
			if err != nil || res.GetText() != pay {
				t.Errorf("the payment barcode: %v, %v", res, err)
			} else {
				p := res.GetResultPoints()[0]
				wholeModules(t, "the barcode", img, int(p.GetX()), int(p.GetY()), 1, 0, len(barMods), barQuiet)
			}
			img, bmp = shown("qr", ui.Node{Kind: ui.KindImage, Src: "qr:" + long, Bright: true})
			res, err = zxqr.NewQRCodeReader().Decode(bmp, hard)
			if err != nil || res.GetText() != long {
				t.Errorf("a 100-character QR code: %v, %v", res, err)
			} else {
				p := res.GetResultPoints()[1]
				x, y := int(math.Round(p.GetX())), int(math.Round(p.GetY()))
				wholeModules(t, "the QR code's row", img, x, y, 1, 0, qrMods, qrQuiet)
				wholeModules(t, "the QR code's column", img, x, y, 0, 1, qrMods, qrQuiet)
			}
		})
	}
}

// The text renderer draws a QR code a module a cell across and a half cell
// down, with its quiet zone, which reads from its cells' colours; what does
// not fit in its columns says so, a barcode with its number (masked when
// secure, as every snapshot is).
func TestCodesInTheTextRenderer(t *testing.T) {
	c := loopCore(t, t.TempDir())
	paycode := "PAYCODE|U8848|1794677933|aEXEy1r0Wp/G"
	q, _ := goqr.New(paycode, goqr.Low)
	q.DisableBorder = true
	mods := len(q.Bitmap()) + 2*qrQuiet
	_ = c.onLoop(func() error {
		c.booted = time.Now().Add(-bootDuration)
		a := c.current()
		saved := a.stack
		defer func() { a.stack = saved }()
		show := func(nodes ...ui.Node) *cv.Canvas {
			a.stack = append(saved[:len(saved):len(saved)], newView(ui.Screen{ID: "s", Body: nodes}))
			return c.Render()
		}
		img := ansiImage(show(ui.Node{Kind: ui.KindImage, Src: "qr:" + paycode}).ANSI())
		bmp, _ := gozxing.NewBinaryBitmapFromImage(img)
		res, err := zxqr.NewQRCodeReader().Decode(bmp, map[gozxing.DecodeHintType]any{gozxing.DecodeHintType_TRY_HARDER: true})
		if err != nil || res.GetText() != paycode {
			t.Errorf("the payment QR code in the text renderer: %v, %v", res, err)
			return nil
		}
		p := res.GetResultPoints()[1]
		x, y := int(math.Round(p.GetX())), int(math.Round(p.GetY()))
		wholeModules(t, "the text renderer's QR code's row", img, x, y, 1, 0, mods, qrQuiet)
		wholeModules(t, "the text renderer's QR code's column", img, x, y, 0, 1, mods, qrQuiet)

		long := "https://pay.example.cn/c/" + strings.Repeat("0123456789", 7) + "ABCDE"
		qrNote, _ := codeOf("qr:" + long).tooSmall()
		barNote, _ := codeOf("bar:28").tooSmall()
		if txt := show(ui.Node{Kind: ui.KindImage, Src: "qr:" + long}).Plain(); !strings.Contains(txt, qrNote) || strings.Contains(txt, "▀") {
			t.Errorf("a QR code wider than the screen:\n%s", txt)
		}
		bar := ui.Node{Kind: ui.KindImage, Src: "bar:281234567890123456"}
		if txt := show(bar).Plain(); !strings.Contains(txt, barNote+" 2812 3456 7890 1234 56") {
			t.Errorf("a barcode wider than the screen, no number:\n%s", txt)
		}
		bar.Secure = true
		if txt := show(bar).Plain(); strings.Contains(txt, "2812") || !strings.Contains(txt, barNote+" ▒▒▒▒") {
			t.Errorf("a secure barcode wider than the screen:\n%s", txt)
		}
		return nil
	})
	f := codeOf("bar:281234567890123456").frame(true)
	if f.Code != codeBar || f.CodeNote == "" || f.CodeText != "2812 3456 7890 1234 56" || !f.Secure || !f.Pixelated {
		t.Errorf("the barcode's frame image: %+v", *f)
	}
	if f := codeOf("bar:PAY-1").frame(false); f.CodeText != "PAY-1" {
		t.Errorf("a barcode of letters shows %q", f.CodeText)
	}
	if f := codeOf("qr:" + paycode).frame(false); f.Code != codeQR || f.CodeNote == "" || f.CodeText != "" || f.W != mods || f.H != mods {
		t.Errorf("the QR code's frame image: %s %q %q %dx%d", f.Code, f.CodeNote, f.CodeText, f.W, f.H)
	}
	for _, bad := range []string{"qr", "bar:", "gen:x", "bar:\u652f"} {
		if codeOf(bad) != nil {
			t.Errorf("%q is a code", bad)
		}
	}
}

// ansiImage is the picture a terminal shows of ANSI cells: two pixels down
// a cell, an upper half block's colours, any other glyph its background.
func ansiImage(ans string) image.Image {
	lines := strings.Split(ans, "\n")
	w := 0
	for _, l := range lines {
		w = max(w, cv.StringWidth(regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(l, "")))
	}
	img := image.NewRGBA(image.Rect(0, 0, w, 2*len(lines)))
	num := func(p []string, i int) uint8 { n, _ := strconv.Atoi(p[i]); return uint8(n) }
	for y, l := range lines {
		var fg, bg color.RGBA
		x := 0
		for len(l) > 0 {
			if strings.HasPrefix(l, "\x1b[") {
				end := strings.IndexByte(l, 'm')
				p := strings.Split(l[2:end], ";")
				for i := 0; i+4 < len(p); i++ {
					if p[i+1] == "2" && (p[i] == "38" || p[i] == "48") {
						c := color.RGBA{num(p, i+2), num(p, i+3), num(p, i+4), 255}
						if p[i] == "38" {
							fg = c
						} else {
							bg = c
						}
						i += 4
					}
				}
				l = l[end+1:]
				continue
			}
			r, n := utf8.DecodeRuneInString(l)
			l = l[n:]
			top := bg
			if r == '▀' {
				top = fg
			}
			for i := 0; i < cv.RuneWidth(r); i++ {
				img.Set(x, 2*y, top)
				img.Set(x, 2*y+1, bg)
				x++
			}
		}
	}
	return img
}

// paintCore is a Shell past its boot with phone-paint drawing it headless
// on dev, and where its snapshots go.
func paintCore(t *testing.T, paint string, dev Device) (*Core, *GUI, string) {
	t.Helper()
	apps := t.TempDir()
	_ = os.MkdirAll(filepath.Join(apps, "a"), 0o755)
	_ = os.WriteFile(filepath.Join(apps, "a", "manifest.json"), []byte(`{"id": "com.test.a", "name": "A", "exec": "true"}`), 0o644)
	run, err := os.MkdirTemp("", "codes-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	c, err := New(Config{W: 40, H: 24, AppsDir: apps, RunDir: run, StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Shutdown)
	snaps := t.TempDir()
	g, err := StartGUI(c, paint, dev, "--headless", "--snap-dir", snaps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	c.booted = time.Now().Add(-bootDuration)
	return c, g, snaps
}

// wholeModules checks the code through (x, y) along (dx, dy): its pixels
// (pure black and white, which no theme draws around it) are mods modules
// of a whole number of pixels each, the first and last quiet of them light.
func wholeModules(t *testing.T, what string, img image.Image, x, y, dx, dy, mods, quiet int) {
	t.Helper()
	at := func(i int) (bool, bool) {
		r, g, b, _ := img.At(x+i*dx, y+i*dy).RGBA()
		switch {
		case r == 0 && g == 0 && b == 0:
			return true, true
		case r == 0xffff && g == 0xffff && b == 0xffff:
			return false, true
		}
		return false, false
	}
	lo, hi := 0, 0
	for _, ok := at(lo - 1); ok; _, ok = at(lo - 1) {
		lo--
	}
	for _, ok := at(hi + 1); ok; _, ok = at(hi + 1) {
		hi++
	}
	var runs []int
	for i, prev := lo, false; i <= hi; i++ {
		dark, _ := at(i)
		if i == lo || dark != prev {
			runs = append(runs, 0)
		}
		runs[len(runs)-1]++
		prev = dark
	}
	n := hi - lo + 1
	k := n / mods
	if k < 1 || n != k*mods {
		t.Errorf("%s: %d pixels for %d modules", what, n, mods)
		return
	}
	for _, r := range runs {
		if r%k != 0 {
			t.Errorf("%s at %d pixels a module: a run of %d pixels (%v)", what, k, r, runs)
			return
		}
	}
	if dark, _ := at(lo); dark || runs[0] < quiet*k || runs[len(runs)-1] < quiet*k {
		t.Errorf("%s: quiet zones of %d and %d pixels, not %d modules of %d", what, runs[0], runs[len(runs)-1], quiet, k)
	}
}

// A payment code in front lights the screen fully and keeps it on for a
// while; anything over it, or leaving it, gives the brightness set back.
func TestBrightCode(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		_ = c.settingsSet(ui.SettingBrightness, 40)
		a := c.current()
		saved := a.stack
		defer func() { a.stack = saved; c.checkScreen() }()
		show := func(nodes ...ui.Node) {
			a.stack = append(saved[:len(saved):len(saved)], newView(ui.Screen{ID: "s", Body: nodes}))
			c.Wake("key")
			c.checkScreen()
		}
		show(ui.Node{Kind: ui.KindImage, Src: "qr:PAY", Bright: true, Secure: true})
		if b := c.Backlight(); !c.screen.codeLit || b != 100 {
			t.Fatalf("a payment code in front: lit %v, backlight %d%%", c.screen.codeLit, b)
		}
		c.lastKey, c.screen.active = c.now().Add(-c.screenTimeout()+screenDimFor/2), time.Time{}
		c.checkScreen()
		if c.screen.dimmed || c.screen.off {
			t.Error("the screen dimmed under a payment code")
		}
		c.lastKey, c.screen.active = c.now().Add(-ui.CodeAwakeSecs*time.Second-c.screenTimeout()), time.Time{}
		c.checkScreen()
		c.checkScreen()
		if !c.screen.off {
			t.Error("a payment code kept the screen on with no key for longer than CodeAwakeSecs")
		}
		show(ui.Node{Kind: ui.KindImage, Src: "qr:PAY", Bright: true})
		dlg := &confirmOverlay{text: "x"}
		c.pushOverlay(dlg)
		c.checkScreen()
		if b := c.Backlight(); c.screen.codeLit || b != 40 {
			t.Errorf("a dialog over the code: backlight %d%%", b)
		}
		c.popOverlay(dlg)
		c.checkScreen()
		if !c.screen.codeLit {
			t.Error("the dialog gone, not full brightness again")
		}
		show(ui.Node{Kind: ui.KindImage, Src: "gen:photo", Bright: true})
		if b := c.Backlight(); c.screen.codeLit || b != 40 {
			t.Errorf("an app's own picture asked to be bright and got %d%%", b)
		}
		show(ui.Node{Kind: ui.KindImage, Src: "bar:281234567890123456", Bright: true})
		if !c.screen.codeLit {
			t.Error("a barcode in front, not full brightness")
		}
		show(ui.Node{Kind: ui.KindImage, Src: "qr:WIFI"})
		if b := c.Backlight(); c.screen.codeLit || b != 40 {
			t.Errorf("a code not marked bright: backlight %d%%", b)
		}
		return nil
	})
}
