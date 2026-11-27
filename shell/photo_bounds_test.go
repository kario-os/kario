package shell

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"testing"
)

// pngHeader is the start of a PNG that declares w×h pixels: enough for a
// decoder to allocate them.
func pngHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6
	chunk := append([]byte("IHDR"), ihdr...)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

// What mediad hands over for a video's still is decoded only if it is a
// photo of a sane size: a header declaring 10^10 pixels or a stream past the
// photo limit is refused before any decoding.
func TestReadPhotoBounds(t *testing.T) {
	if _, err := readPhoto(bytes.NewReader(pngHeader(100000, 100000))); err == nil {
		t.Error("a PNG declaring 100000×100000 was decoded")
	}
	big := append(pngHeader(64, 64), make([]byte, maxPhotoBytes)...)
	if _, err := readPhoto(bytes.NewReader(big)); err == nil {
		t.Error("a stream past maxPhotoBytes was decoded")
	}
	var ok bytes.Buffer
	if err := png.Encode(&ok, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	img, err := readPhoto(&ok)
	if err != nil || img.Bounds().Dx() != 4 || img.Bounds().Dy() != 3 {
		t.Errorf("a 4×3 PNG: %v, %v", img, err)
	}
}
