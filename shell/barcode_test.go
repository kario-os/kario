package shell

import (
	"encoding/base64"
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/oned"

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
		f := barFrame(text, false)
		if f == nil {
			t.Fatalf("%q: no image", text)
		}
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
	pay := barFrame("281234567890123456", false)
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
