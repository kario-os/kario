package shell

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"math/rand"
	"testing"

	"github.com/kario-os/kario/ui"
)

func TestPickedPhotoShrunk(t *testing.T) {
	read := func(t *testing.T, raw []byte, side int) (*ui.MediaItem, image.Image, string) {
		t.Helper()
		item := &ui.MediaItem{Size: int64(len(raw)), Mime: "keep"}
		f, err := shrunkPhoto(bytes.NewReader(raw), side, item)
		if err != nil {
			t.Fatal(err)
		}
		if f == nil {
			return item, nil, ""
		}
		defer f.Close()
		out, err := io.ReadAll(f)
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(out)) != item.Size {
			t.Errorf("size %d, the file has %d bytes", item.Size, len(out))
		}
		img, format, err := image.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		return item, img, format
	}

	big := testJPEG(t, 2000, 1500)
	item, img, format := read(t, big, ui.PhotoSendSide)
	if format != "jpeg" || img.Bounds().Dx() != 1280 || img.Bounds().Dy() != 960 {
		t.Fatalf("a 2000x1500 photo at 1280: %s %v", format, img.Bounds())
	}
	if item.W != 1280 || item.H != 960 || item.Mime != "image/jpeg" || item.Original != int64(len(big)) || item.Size >= item.Original {
		t.Errorf("the entry does not describe the copy: %+v", item)
	}

	tall := testJPEG(t, 300, 1200)
	if _, img, _ := read(t, tall, 600); img.Bounds().Dx() != 150 || img.Bounds().Dy() != 600 {
		t.Errorf("a 300x1200 photo at 600: %v", img.Bounds())
	}

	small := testJPEG(t, 640, 480)
	if item, img, _ := read(t, small, ui.PhotoSendSide); img != nil || item.Original != 0 || item.Mime != "keep" {
		t.Errorf("a JPEG within the side was copied: %+v", item)
	}

	var p bytes.Buffer
	if err := png.Encode(&p, samplePhoto("gen:sample1", 480, 360)); err != nil {
		t.Fatal(err)
	}
	item, img, format = read(t, p.Bytes(), ui.PhotoSendSide)
	if format != "jpeg" || img.Bounds().Dx() != 480 || item.Original != int64(p.Len()) {
		t.Errorf("a PNG within the side is not handed over as a JPEG: %s %+v", format, item)
	}

	if _, err := shrunkPhoto(bytes.NewReader(hugePNGHeader(t, 10000, 6000)), ui.PhotoSendSide, &ui.MediaItem{}); err == nil {
		t.Error("a 60 megapixel photo was decoded")
	}
	if _, err := shrunkPhoto(bytes.NewReader([]byte("not a picture")), ui.PhotoSendSide, &ui.MediaItem{}); err == nil {
		t.Error("not a picture, no error")
	}
}

// Sending a 2400x1800 photo: scaleCrop read it a pixel at a time through
// At, which allocates a colour for each (4.3 million), and the send ran
// past half a minute on a loaded emulator. A few allocations, whatever the
// size. The count is the whole process's, so the test runs in one of its
// own: other tests' goroutines allocate meanwhile.
func TestScaleCropAllocations(t *testing.T) {
	if !alone(t) {
		return
	}
	img, _, err := image.Decode(bytes.NewReader(testJPEG(t, 2400, 1800)))
	if err != nil {
		t.Fatal(err)
	}
	if n := testing.AllocsPerRun(3, func() { scaleCrop(img, ui.PhotoSendSide, ui.PhotoSendSide*3/4) }); n > 20 {
		t.Fatalf("scaling a 2400x1800 photo to 1280x960 took %.0f allocations", n)
	}
}

// scaleCrop gives what reading the picture pixel by pixel through At gave,
// for the kinds of picture the decoders return, bounds off the origin, and
// sizes smaller and larger than the picture.
func TestScaleCropAsThroughAt(t *testing.T) {
	byAt := func(img image.Image, w, h int) *image.RGBA {
		b := img.Bounds()
		sw, sh := float64(b.Dx()), float64(b.Dy())
		s := math.Max(float64(w)/sw, float64(h)/sh)
		ox, oy := (sw-float64(w)/s)/2, (sh-float64(h)/s)/2
		out := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			y0, y1 := int(oy+float64(y)/s), int(oy+float64(y+1)/s)
			for x := 0; x < w; x++ {
				x0, x1 := int(ox+float64(x)/s), int(ox+float64(x+1)/s)
				var r, g, bl, n uint32
				for yy := y0; yy <= max(y0, y1-1); yy++ {
					for xx := x0; xx <= max(x0, x1-1); xx++ {
						cr, cg, cb, _ := img.At(b.Min.X+min(xx, b.Dx()-1), b.Min.Y+min(yy, b.Dy()-1)).RGBA()
						r, g, bl, n = r+cr>>8, g+cg>>8, bl+cb>>8, n+1
					}
				}
				i := out.PixOffset(x, y)
				out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = uint8(r/n), uint8(g/n), uint8(bl/n), 255
			}
		}
		return out
	}
	rnd := rand.New(rand.NewSource(1))
	jpg, _, err := image.Decode(bytes.NewReader(testJPEG(t, 403, 301)))
	if err != nil {
		t.Fatal(err)
	}
	ycc := func(r image.Rectangle, ratio image.YCbCrSubsampleRatio) image.Image {
		m := image.NewYCbCr(r, ratio)
		rnd.Read(m.Y)
		rnd.Read(m.Cb)
		rnd.Read(m.Cr)
		return m
	}
	nrgba := image.NewNRGBA(image.Rect(3, 5, 323, 245))
	rnd.Read(nrgba.Pix)
	gray := image.NewGray(image.Rect(0, 0, 200, 150))
	rnd.Read(gray.Pix)
	cmyk := image.NewCMYK(image.Rect(-7, 2, 233, 182))
	rnd.Read(cmyk.Pix)
	pal := image.NewPaletted(image.Rect(0, 0, 160, 120), color.Palette{color.Black, color.White, color.RGBA{200, 10, 10, 255}, color.NRGBA{10, 200, 10, 90}})
	for i := range pal.Pix {
		pal.Pix[i] = uint8(rnd.Intn(len(pal.Palette)))
	}
	for name, img := range map[string]image.Image{
		"jpeg":  jpg,
		"422":   ycc(image.Rect(1, 1, 301, 221), image.YCbCrSubsampleRatio422),
		"411":   ycc(image.Rect(0, 0, 300, 200), image.YCbCrSubsampleRatio411),
		"rgba":  samplePhoto("gen:scale", 320, 240),
		"nrgba": nrgba,
		"gray":  gray,
		"cmyk":  cmyk,
		"pal":   pal,
	} {
		for _, sz := range []image.Point{{240, 240}, {128, 96}, {90, 160}, {100, 37}, {500, 350}} {
			if got, want := scaleCrop(img, sz.X, sz.Y), byAt(img, sz.X, sz.Y); !bytes.Equal(got.Pix, want.Pix) {
				t.Errorf("%s %v at %v: not as through At", name, img.Bounds(), sz)
			}
		}
	}
}

// hugePNGHeader is a PNG whose header claims w x h: enough for DecodeConfig.
func hugePNGHeader(t *testing.T, w, h int) []byte {
	var p bytes.Buffer
	if err := png.Encode(&p, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	raw := p.Bytes()
	ihdr := raw[12 : 12+4+13]
	binary.BigEndian.PutUint32(ihdr[4:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[8:], uint32(h))
	binary.BigEndian.PutUint32(raw[12+4+13:], crc32.ChecksumIEEE(ihdr))
	return raw
}
