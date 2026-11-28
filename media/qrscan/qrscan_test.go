package qrscan

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/kario-os/kario/media"
)

// demoCode is the code on the synthetic camera's table
// (programs/mediad/demo-merchant.txt).
const demoCode = "PAY|M10086|\u6668\u5149\u5496\u5561|1800"

func card(t testing.TB, text string) *media.Card {
	t.Helper()
	c, err := media.NewCard(text)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// seen is c on the synthetic table in a w×h frame, as v shows it.
func seen(c *media.Card, w, h int, v media.View) *media.Gray {
	g := media.NewGray(w, h)
	media.TableLook(0, g)
	c.DrawGray(g, v)
	return g
}

// longest is the longest prefix of filler that fits version v at level H.
func longest(t *testing.T, v int, filler string) string {
	lo, hi := 1, len(filler)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if _, err := qrcode.NewWithForcedVersion(filler[:mid], v, qrcode.Highest); err == nil {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return filler[:lo]
}

func TestVersionsAndLevels(t *testing.T) {
	filler := strings.Repeat("kario-scan:0123456789abcdefghijklmnopqrstuvwxyz/", 40)
	levels := map[string]qrcode.RecoveryLevel{"L": qrcode.Low, "M": qrcode.Medium, "Q": qrcode.High, "H": qrcode.Highest}
	for _, v := range []int{1, 2, 4, 7, 10, 15, 25, 40} {
		text := longest(t, v, filler)
		for name, lv := range levels {
			q, err := qrcode.NewWithForcedVersion(text, v, lv)
			if err != nil {
				t.Fatalf("version %d %s: %v", v, name, err)
			}
			q.DisableBorder = true
			bm := q.Bitmap()
			if len(bm) != 17+4*v {
				t.Fatalf("version %d is %d modules wide", v, len(bm))
			}
			c, err := media.CardOf(bm)
			if err != nil {
				t.Fatal(err)
			}
			// The code faces the camera, about 5.5 pixels a module
			// (5 for version 40, within MaxPixels): ZXing maps the
			// grid by one alignment pattern, so over 100 modules a
			// little perspective puts the far corner a module off
			// (TestPoses has the angles).
			side := int(math.Ceil(float64(len(bm)+6) * 5.5 / 0.9))
			if v == 40 {
				side = int(math.Ceil(float64(len(bm)+6) * 5 / 0.9))
			}
			got, err := Decode(seen(c, side, side, media.View{Corners: media.Pose(0.45, 0, 0, 0, 0), Noise: 6, Seed: uint32(v)}))
			if err != nil || got != text {
				t.Errorf("version %d level %s (%d bytes): %q, %v", v, name, len(text), got, err)
			}
		}
	}
}

// TestPoses: a code filling most of the frame, as one holds it to scan it,
// at any rotation, tilted away up to 0.2 (the far edge 80% as wide),
// defocused and noisy.
func TestPoses(t *testing.T) {
	c := card(t, demoCode)
	n, ok := 0, 0
	for ai := 0; ai < 12; ai++ {
		for _, tilt := range []float64{0, 0.1, 0.2} {
			for _, blur := range []int{0, 1, 2} {
				for _, noise := range []int{0, 10, 20} {
					n++
					angle := float64(ai) * math.Pi / 6
					got, err := Decode(seen(c, 640, 480, media.View{Corners: media.Pose(0.36, angle, tilt, 0.02, -0.01), Blur: blur, Noise: noise, Seed: uint32(n)}))
					if err == nil && got == demoCode {
						ok++
					} else {
						t.Logf("%d° tilt %.1f blur %d noise %d: %q, %v", ai*30, tilt, blur, noise, got, err)
					}
				}
			}
		}
	}
	t.Logf("%d of %d poses read", ok, n)
	if ok < n*99/100 {
		t.Errorf("%d of %d poses read, want 99%%", ok, n)
	}
}

// TestVersion1: a version 1 code (up to 14 bytes at level M) has no
// alignment pattern; ZXing takes it for a parallelogram, so it is read
// facing the camera or tilted a little (5%: the far edge 95% as wide), not
// more.
func TestVersion1(t *testing.T) {
	c := card(t, "hello")
	for _, tilt := range []float64{0, 0.05} {
		n, ok := 0, 0
		for ai := 0; ai < 12; ai++ {
			for _, blur := range []int{0, 1} {
				n++
				if got, err := Decode(seen(c, 640, 480, media.View{Corners: media.Pose(0.36, float64(ai)*math.Pi/6, tilt, 0, 0), Blur: blur, Noise: 8, Seed: uint32(n)})); err == nil && got == "hello" {
					ok++
				}
			}
		}
		t.Logf("tilt %.2f: %d of %d read", tilt, ok, n)
		if ok < n*8/10 {
			t.Errorf("tilt %.2f: %d of %d read", tilt, ok, n)
		}
	}
}

// TestSmallCodes: a code a third of the frame high (about 4.6 pixels a
// module at 640×480) is read when sharp; defocus loses frames, which the
// scanner makes up by trying four a second.
func TestSmallCodes(t *testing.T) {
	c := card(t, demoCode)
	for _, blur := range []int{0, 1} {
		n, ok := 0, 0
		for ai := 0; ai < 12; ai++ {
			for _, tilt := range []float64{0, 0.1} {
				n++
				angle := float64(ai)*math.Pi/6 + 0.05
				if got, err := Decode(seen(c, 640, 480, media.View{Corners: media.Pose(0.15, angle, tilt, 0, 0), Blur: blur, Noise: 8, Seed: uint32(n)})); err == nil && got == demoCode {
					ok++
				}
			}
		}
		t.Logf("blur %d: %d of %d read", blur, ok, n)
		if want := map[int]int{0: n * 9 / 10, 1: n / 2}[blur]; ok < want {
			t.Errorf("blur %d: %d of %d read, want %d", blur, ok, n, want)
		}
	}
}

// TestHeldCard: the frames phone-mediad decodes on the developer machine (a
// card held over the synthetic table) and on the emulator (over vivid's
// colour bars), at its frame size, four a second for a minute.
func TestHeldCard(t *testing.T) {
	c := card(t, demoCode)
	for name, bg := range map[string]media.GraySource{"table": media.TableLook, "bars": colourBars} {
		_, look := media.ScanScene(media.Table, bg, func() *media.Card { return c })
		g := media.NewGray(640, 480)
		n, ok := 240, 0
		for i := 0; i < n; i++ {
			look(float64(i)/4, g)
			if got, err := Decode(g); err == nil && got == demoCode {
				ok++
			} else {
				t.Logf("%s, %.2f s: %q, %v", name, float64(i)/4, got, err)
			}
		}
		t.Logf("%s: %d of %d frames read", name, ok, n)
		if ok < n*98/100 {
			t.Errorf("%s: %d of %d frames read", name, ok, n)
		}
	}
}

// colourBars is vivid's default test pattern in grey: eight 75% bars.
func colourBars(_ float64, g *media.Gray) {
	bars := [8]byte{180, 162, 131, 112, 84, 65, 35, 16}
	for y := 0; y < g.H; y++ {
		for x := 0; x < g.W; x++ {
			g.Pix[y*g.W+x] = bars[x*8/g.W]
		}
	}
}

func TestMirrored(t *testing.T) {
	q, err := qrcode.New(demoCode, qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	q.DisableBorder = true
	bm := q.Bitmap()
	for y := range bm {
		for x := y + 1; x < len(bm); x++ {
			bm[y][x], bm[x][y] = bm[x][y], bm[y][x]
		}
	}
	c, _ := media.CardOf(bm)
	if got, err := Decode(seen(c, 640, 480, media.View{Corners: media.Pose(0.36, 0.3, 0.1, 0, 0), Blur: 1, Noise: 8})); err != nil || got != demoCode {
		t.Fatalf("mirrored: %q, %v", got, err)
	}
}

// TestNotUTF8: a code's bytes that are not UTF-8 (GBK, say) come out as
// valid UTF-8, replacement characters where they do not decode.
func TestNotUTF8(t *testing.T) {
	c := card(t, "PAY|M1|\xd6\xd0\xce\xc4|100")
	got, err := Decode(seen(c, 640, 480, media.View{Corners: media.Pose(0.36, 0, 0, 0, 0)}))
	if err != nil || !utf8.ValidString(got) || !strings.HasPrefix(got, "PAY|M1|") || !strings.Contains(got, "\uFFFD") {
		t.Fatalf("%q, %v", got, err)
	}
}

func TestNoCode(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	frames := map[string]func(g *media.Gray){
		"table": func(g *media.Gray) { media.TableLook(0, g) },
		"bars":  func(g *media.Gray) { colourBars(0, g) },
		"grey": func(g *media.Gray) {
			for i := range g.Pix {
				g.Pix[i] = 128
			}
		},
		"noise": func(g *media.Gray) { rng.Read(g.Pix) },
		"checkerboard": func(g *media.Gray) {
			for i := range g.Pix {
				if (i%g.W/8+i/g.W/8)%2 == 0 {
					g.Pix[i] = 230
				} else {
					g.Pix[i] = 20
				}
			}
		},
		// a card with the code torn off: the finder patterns' square
		// rings, nothing between them
		"blank card": func(g *media.Gray) {
			c, _ := media.CardOf(finderOnly(25))
			media.TableLook(0, g)
			c.DrawGray(g, media.View{Corners: media.Pose(0.36, 0.1, 0.1, 0, 0), Blur: 1, Noise: 8})
		},
	}
	for name, draw := range frames {
		g := media.NewGray(640, 480)
		draw(g)
		if got, err := Decode(g); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
}

// finderOnly is an n-module code's three finder patterns and nothing else.
func finderOnly(n int) [][]bool {
	bm := make([][]bool, n)
	for y := range bm {
		bm[y] = make([]bool, n)
	}
	for _, o := range [][2]int{{0, 0}, {n - 7, 0}, {0, n - 7}} {
		for y := 0; y < 7; y++ {
			for x := 0; x < 7; x++ {
				ring := max(abs(x-3), abs(y-3))
				bm[o[1]+y][o[0]+x] = ring != 2
			}
		}
	}
	return bm
}

func abs(v int) int { return max(v, -v) }

func TestBadFrames(t *testing.T) {
	bad := map[string]*media.Gray{
		"nil":          nil,
		"empty":        {},
		"too small":    media.NewGray(20, 400),
		"truncated":    {W: 640, H: 480, Pix: make([]byte, 640*480-1)},
		"too long":     {W: 640, H: 480, Pix: make([]byte, 640*480+1)},
		"negative":     {W: -640, H: -480, Pix: make([]byte, 640*480)},
		"too wide":     {W: MaxSide + 1, H: 100, Pix: make([]byte, (MaxSide+1)*100)},
		"too many":     media.NewGray(1600, 1200),
		"overflowing":  {W: 1 << 31, H: 1 << 31},
		"4K camera":    media.NewGray(3840, 2160),
		"pixels short": {W: 1280, H: 960, Pix: nil},
	}
	for name, g := range bad {
		t0 := time.Now()
		if got, err := Decode(g); !errors.Is(err, ErrSize) {
			t.Errorf("%s: %q, %v", name, got, err)
		}
		if d := time.Since(t0); d > 10*time.Millisecond {
			t.Errorf("%s: refused in %s", name, d)
		}
	}
	// Garbage of every size the bounds allow is no code, not a panic.
	rng := rand.New(rand.NewSource(2))
	for _, sz := range [][2]int{{MinSide, MinSide}, {21, 900}, {900, 21}, {640, 480}, {1280, 960}, {MaxPixels / 1000, 1000}} {
		g := media.NewGray(sz[0], sz[1])
		rng.Read(g.Pix)
		if _, err := Decode(g); !errors.Is(err, ErrNotFound) {
			t.Errorf("%v of noise: %v", sz, err)
		}
	}
}

// TestDecodeTime: one decoding of the scanner's frame stays well inside its
// share of a phone's CPU. Here (a Xeon E5-2670 v3) about 3 ms; a Cortex-A53
// is about five to six times slower. The bound only catches a runaway.
func TestDecodeTime(t *testing.T) {
	c := card(t, demoCode)
	rng := rand.New(rand.NewSource(3))
	worst := map[string]time.Duration{}
	for i := 0; i < 20; i++ {
		frames := map[string]*media.Gray{
			"code":  seen(c, 640, 480, media.HeldView(float64(i)/4)),
			"table": seen(c, 640, 480, media.View{Corners: media.Pose(0.36, 0, 0, 2, 2)}),
			"noise": media.NewGray(640, 480),
		}
		rng.Read(frames["noise"].Pix)
		for name, g := range frames {
			t0 := time.Now()
			_, _ = Decode(g)
			worst[name] = max(worst[name], time.Since(t0))
		}
	}
	t.Logf("slowest of 20 at 640x480: %v", worst)
	for name, d := range worst {
		if d > 100*time.Millisecond {
			t.Errorf("%s: %s", name, d)
		}
	}
}

func BenchmarkDecode(b *testing.B) {
	c := card(b, demoCode)
	noise := media.NewGray(640, 480)
	rand.New(rand.NewSource(4)).Read(noise.Pix)
	for _, f := range []struct {
		name string
		g    *media.Gray
	}{
		{"code-640x480", seen(c, 640, 480, media.HeldView(1))},
		{"table-640x480", seen(c, 640, 480, media.View{Corners: media.Pose(0.36, 0, 0, 2, 2)})},
		{"noise-640x480", noise},
		{"code-1280x960", seen(c, 1280, 960, media.HeldView(1))},
	} {
		b.Run(f.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = Decode(f.g)
			}
		})
	}
}

func Example() {
	c, _ := media.NewCard("hello")
	g := media.NewGray(640, 480)
	media.TableLook(0, g)
	c.DrawGray(g, media.View{Corners: media.Pose(0.3, 0.2, 0, 0, 0), Blur: 1, Noise: 8})
	text, err := Decode(g)
	fmt.Println(text, err)
	// Output: hello <nil>
}

// marks is a frame tiled with finder patterns of 2- and 3-pixel modules, a
// sheet of stickers: hundreds of candidates of different sizes, which
// gozxing's own search tries in all their triples.
func marks() *media.Gray {
	g := media.NewGray(640, 480)
	for i := range g.Pix {
		g.Pix[i] = 230
	}
	ring := finderOnly(21)
	for oy, row := 0, 0; oy+24 <= g.H; oy, row = oy+24, row+1 {
		for ox, col := 0, 0; ox+24 <= g.W; ox, col = ox+24, col+1 {
			px := 2 + (row+col)%2
			for y := 0; y < 7*px; y++ {
				for x := 0; x < 7*px; x++ {
					if ring[y/px][x/px] {
						g.Pix[(oy+y)*g.W+ox+x] = 20
					}
				}
			}
		}
	}
	return g
}

// TestManyFinderMarks: frames full of finder-like marks are refused fast;
// gozxing's own search takes 7.6 s on the stripes at 163 wide (find in
// qrscan.go).
func TestManyFinderMarks(t *testing.T) {
	for _, w := range []int{163, 640} {
		stripes := media.NewGray(w, w*3/4)
		for i := range stripes.Pix {
			stripes.Pix[i] = "\x00\xffkkk\x00\xff0x"[i%9]
		}
		for name, g := range map[string]*media.Gray{fmt.Sprintf("stripes %d", w): stripes, "marks": marks()} {
			t0 := time.Now()
			if got, err := Decode(g); !errors.Is(err, ErrNotFound) {
				t.Errorf("%s: %q, %v", name, got, err)
			}
			if d := time.Since(t0); d > 100*time.Millisecond {
				t.Errorf("%s: %s", name, d)
			}
		}
	}
}
