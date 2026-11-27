package shell

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/tools/prebuilt"
)

// styleAt is the style of the text screen's cell at (x, y).
func styleAt(can *cv.Canvas, x, y int) (s cv.Style) {
	can.Restyle(x, y, 1, func(st cv.Style) cv.Style { s = st; return st })
	return s
}

// gridOverlays open the photo picker and the share sheet with n tiles,
// tile i titled title(i); sel is the chosen tile.
var gridOverlays = []struct {
	name string
	open func(c *Core, n int, title func(int) string) (sel func() int)
}{
	{"photo picker", func(c *Core, n int, title func(int) string) func() int {
		var items []*mediaItem
		for i := range n {
			items = append(items, &mediaItem{ID: fmt.Sprintf("p%d", i), Name: title(i)})
		}
		o := &pickerOverlay{app: &App{M: &Manifest{ID: "com.example.chat", Name: "Chat"}}, items: items, done: func(*mediaItem) {}}
		c.pushOverlay(o)
		return func() int { return o.sel }
	}},
	{"share sheet", func(c *Core, n int, title func(int) string) func() int {
		var targets []*App
		for i := range n {
			targets = append(targets, &App{M: &Manifest{ID: fmt.Sprintf("com.example.app%d", i), Name: title(i), Icon: "A"}})
		}
		o := &shareOverlay{from: &App{M: &Manifest{ID: "com.example.chat", Name: "Chat"}}, what: "a piece of text", targets: targets, done: func(*App) {}}
		c.pushOverlay(o)
		return func() int { return o.sel }
	}},
}

// scrollSteps go to the end of a grid of n tiles, back up and to the
// start, then down a row at a time past the end.
func scrollSteps(n, cols int) []string {
	steps := []string{KeyLast, KeyUp, KeyLeft, KeyFirst}
	for range n/cols + 1 {
		steps = append(steps, KeyDown)
	}
	return append(steps, KeyRight, KeyFirst)
}

// In the text renderer the photo picker and the share sheet keep the
// chosen tile on screen on each form factor: with more tiles than their
// rows hold, after every key the chosen one is drawn on its highlight, in
// whole rows from the first that keeps its row on screen (as a menu keeps
// its entry), and the body's scroll bar says there is more above or below;
// with no more than fits, nothing moves and there is no bar.
func TestPickerGridScrollsText(t *testing.T) {
	const fit = 6 // rows of 3 lines from line 3 to the text on line 20 of 24
	for _, g := range gridOverlays {
		t.Run(g.name, func(t *testing.T) {
			onForms(t, func(t *testing.T, c *Core) {
				c.booted = time.Now().Add(-bootDuration)
				cols := c.menuColumns()
				title := func(i int) string { return fmt.Sprintf("T%02d", i) }
				for _, n := range []int{40, fit * cols} {
					sel := g.open(c, n, title)
					rows := (n + cols - 1) / cols
					for _, k := range scrollSteps(n, cols) {
						c.Key(Named(k))
						first, bar := max(0, sel()/cols-(fit-1)), 1
						switch {
						case rows <= fit:
							bar = -1
						case first == 0:
							bar = 0
						case first == rows-fit:
							bar = 2
						}
						gridShown(t, c, fmt.Sprintf("%d tiles, %s to tile %d", n, k, sel()), title(sel()), bar)
					}
					c.popOverlay(c.overlays[len(c.overlays)-1])
				}
			})
		})
	}
}

// gridShown checks the text screen of a grid overlay: the chosen tile's
// title drawn on its highlight, and the scroll bar's thumb at the top (0),
// between (1) or at the end (2) of the grid's lines; -1: no bar.
func gridShown(t *testing.T, c *Core, what, title string, bar int) {
	t.Helper()
	can := c.Render()
	lines := strings.Split(can.Plain(), "\n")
	chosen := false
	for y, l := range lines {
		if i := strings.Index(l, title); i >= 0 {
			chosen = styleAt(can, utf8.RuneCountInString(l[:i]), y).BG == colFocus
			break
		}
	}
	if !chosen {
		t.Errorf("%s: %q is not drawn chosen:\n%s", what, title, can.Plain())
	}
	thumb, track := -1, 0
	for y := 3; y < can.H-4; y++ {
		switch fg := styleAt(can, can.W-1, y).FG; {
		case fg == colDim && strings.HasSuffix(lines[y], "┃"):
			thumb = y
		case fg == colTitle && strings.HasSuffix(lines[y], "│"):
			track++
		}
	}
	ok := map[int]bool{-1: thumb < 0 && track == 0, 0: thumb == 3, 1: thumb > 3 && thumb < can.H-5, 2: thumb == can.H-5}[bar]
	if !ok || bar >= 0 && track != can.H-8 {
		t.Errorf("%s: the scroll bar's thumb on line %d (lines 3 to %d, %d of track), want %s", what, thumb, can.H-5, track,
			[]string{"no bar", "the top", "between", "the end"}[bar+1])
	}
}

// On phone-paint (as built here: make paint) the photo picker and the
// share sheet show the chosen tile whole on each form factor's panel,
// after every key: each photo and each app's icon is a colour of its own,
// so a snapshot says how much of the chosen one is on screen.
func TestPickerGridScrollsOnPhonePaint(t *testing.T) {
	paint := prebuilt.Paint(t)
	const n = 40
	tint := func(i int) color.RGBA { return color.RGBA{R: uint8(40 + 5*i), G: 230, B: uint8(200 - 4*i), A: 255} }
	solid := func(i int) []byte {
		img := image.NewRGBA(image.Rect(0, 0, 64, 64))
		for p := 0; p < len(img.Pix); p += 4 {
			c := tint(i)
			copy(img.Pix[p:], []byte{c.R, c.G, c.B, c.A})
		}
		b, err := encodePNG(img)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, id := range []string{"bar-t9", "bar-qwerty", "communicator"} {
		for _, kind := range []string{"photos", "apps"} {
			t.Run(id+"/"+kind, func(t *testing.T) {
				dev, _ := DeviceByID(id)
				c, g, snaps := paintCore(t, paint, dev)
				from := &App{M: &Manifest{ID: "com.example.chat", Name: "Chat"}}
				var sel func() int
				if kind == "photos" {
					var items []*mediaItem
					for i := range n {
						it, err := c.media.add(solid(i), fmt.Sprintf("P%02d", i), time.Now())
						if err != nil {
							t.Fatal(err)
						}
						items = append(items, it)
					}
					o := &pickerOverlay{app: from, items: items, done: func(*mediaItem) {}}
					c.pushOverlay(o)
					sel = func() int { return o.sel }
				} else {
					dir := t.TempDir()
					var targets []*App
					for i := range n {
						icon := fmt.Sprintf("icon%d.png", i)
						if err := os.WriteFile(filepath.Join(dir, icon), solid(i), 0o644); err != nil {
							t.Fatal(err)
						}
						targets = append(targets, &App{M: &Manifest{ID: fmt.Sprintf("com.example.app%d", i), Name: fmt.Sprintf("A%02d", i), IconImage: icon, dir: dir}})
					}
					o := &shareOverlay{from: from, what: "a piece of text", targets: targets, done: func(*App) {}}
					c.pushOverlay(o)
					sel = func() int { return o.sel }
				}
				drawn := func(step string, i int) int {
					c.Render()
					if err := g.Snap(step); err != nil {
						t.Fatal(err)
					}
					img, err := readPNG(filepath.Join(snaps, step+".png"))
					if err != nil {
						t.Fatal(err)
					}
					want, px := tint(i), 0
					near := func(v uint32, w uint8) bool { return int(v>>8)-int(w) <= 3 && int(w)-int(v>>8) <= 3 }
					box := img.Bounds()
					for y := box.Min.Y; y < box.Max.Y; y++ {
						for x := box.Min.X; x < box.Max.X; x++ {
							r, gr, b, _ := img.At(x, y).RGBA()
							if near(r, want.R) && near(gr, want.G) && near(b, want.B) {
								px++
							}
						}
					}
					return px
				}
				whole := drawn("start", 0)
				if whole < 500 {
					t.Fatalf("the first tile, chosen, is %d pixels of its colour", whole)
				}
				for j, k := range scrollSteps(n, c.menuColumns()) {
					c.Key(Named(k))
					if got := drawn(fmt.Sprintf("%02d-%s", j, k), sel()); got < whole*9/10 {
						t.Errorf("%s to tile %d: %d pixels of its colour on the panel, %d when it shows whole", k, sel(), got, whole)
					}
				}
			})
		}
	}
}
