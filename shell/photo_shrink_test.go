package shell

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"io"
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
