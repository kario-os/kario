package qrscan

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/common/reedsolomon"
	"github.com/makiuchi-d/gozxing/qrcode/decoder"
	"github.com/makiuchi-d/gozxing/qrcode/encoder"

	"github.com/kario-os/kario/media"
)

// FuzzDecode gives the decoder what a camera could show it. b[0] even: a
// frame, its width and height in b[1:5] (big endian) and pixels after them,
// as many as there are (b[0]&2: tiled to fill a frame of at most 256×256,
// so the decoder runs on them). b[0] odd: a QR code of arbitrary data
// codewords with valid error correction (version, level and mask from
// b[1:3]), drawn on a card, so the decoder's bitstream parser reads
// anything a printed code can carry: modes, character sets, Kanji,
// structured append, lengths past the end.
func FuzzDecode(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0, 0})
	f.Add(append([]byte{0, 0, 21, 0, 21}, make([]byte, 21*21)...))
	f.Add([]byte{2, 0, 200, 0, 150, 0, 255, 0, 255, 30, 220})
	f.Add([]byte{0, 0xff, 0xff, 0xff, 0xff})
	// version 1-M "hello" in byte mode, then the terminator
	f.Add([]byte{1, 10, 2, 0x40, 0x56, 0x86, 0x56, 0xc6, 0xc6, 0xf0, 0xec, 0x11})
	// numeric mode claiming more digits than there are
	f.Add([]byte{1, 0, 0, 0x13, 0xff, 0x00})
	// ECI 26 (UTF-8), then a byte segment of an invalid sequence
	f.Add([]byte{1, 4, 5, 0x71, 0xa4, 0x03, 0xff, 0xfe, 0xc0, 0x00})
	// Kanji and GB 2312 (Hanzi) modes
	f.Add([]byte{1, 7, 1, 0x80, 0x31, 0x23, 0x45, 0x00})
	f.Add([]byte{1, 7, 3, 0xd1, 0x01, 0x23, 0x45, 0x67, 0x00})
	// structured append, FNC1
	f.Add([]byte{1, 2, 6, 0x30, 0x12, 0x34, 0x50, 0x41, 0x00})
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) == 0 {
			return
		}
		var g *media.Gray
		if b[0]&1 == 0 {
			g = frameOf(b[0], b[1:])
		} else if g = codeOf(t, b[1:]); g == nil {
			return
		}
		t0 := time.Now()
		text, err := Decode(g)
		if d := time.Since(t0); d > 2*time.Second {
			t.Fatalf("%s to decode a %dx%d frame", d, g.W, g.H)
		}
		inBounds := g.W >= MinSide && g.H >= MinSide && g.W <= MaxSide && g.H <= MaxSide && g.W*g.H <= MaxPixels && len(g.Pix) == g.W*g.H
		switch {
		case err == nil:
			if !inBounds || text == "" || len(text) > MaxText || !utf8.ValidString(text) {
				t.Fatalf("%dx%d frame read as %q", g.W, g.H, text)
			}
		case errors.Is(err, ErrSize):
			if inBounds {
				t.Fatalf("a %dx%d frame refused", g.W, g.H)
			}
		case errors.Is(err, ErrNotFound):
		default:
			t.Fatalf("%dx%d: %v", g.W, g.H, err)
		}
	})
}

// TestCodeOf: the fuzz target's codes are real codes.
func TestCodeOf(t *testing.T) {
	for b := byte(0); b < 40; b++ {
		// "hello" in byte mode: the length takes 8 bits up to version
		// 9, 16 from 10.
		data := []byte{0x40, 0x56, 0x86, 0x56, 0xc6, 0xc6, 0xf0}
		if b%10 == 9 {
			data = []byte{0x40, 0x00, 0x56, 0x86, 0x56, 0xc6, 0xc6, 0xf0}
		}
		if got, err := Decode(codeOf(t, append([]byte{b, 2}, data...))); err != nil || got != "hello" {
			t.Errorf("version %d level %d: %q, %v", 1+b%10, b/10, got, err)
		}
	}
}

func frameOf(mode byte, b []byte) *media.Gray {
	if len(b) < 4 {
		return &media.Gray{}
	}
	w, h, pix := int(binary.BigEndian.Uint16(b)), int(binary.BigEndian.Uint16(b[2:])), b[4:]
	if mode&2 == 0 {
		return &media.Gray{W: w, H: h, Pix: pix}
	}
	g := media.NewGray(MinSide+w%(256-MinSide), MinSide+h%(256-MinSide))
	for i := 0; len(pix) > 0 && i < len(g.Pix); i += len(pix) {
		copy(g.Pix[i:], pix)
	}
	return g
}

// codeOf draws a code of versions 1-10 holding b[2:] as its data codewords
// (cut or padded to the version's), with their error correction.
func codeOf(t *testing.T, b []byte) *media.Gray {
	if len(b) < 2 {
		return nil
	}
	ver, err := decoder.Version_GetVersionForNumber(1 + int(b[0])%10)
	if err != nil {
		t.Fatal(err)
	}
	level := []decoder.ErrorCorrectionLevel{decoder.ErrorCorrectionLevel_L, decoder.ErrorCorrectionLevel_M,
		decoder.ErrorCorrectionLevel_Q, decoder.ErrorCorrectionLevel_H}[int(b[0])/10%4]
	ecb := ver.GetECBlocksForLevel(level)
	var blocks [][]int
	data, pad := b[2:], 0
	for _, group := range ecb.GetECBlocks() {
		for i := 0; i < group.GetCount(); i++ {
			blk := make([]int, group.GetDataCodewords()+ecb.GetECCodewordsPerBlock())
			for j := 0; j < group.GetDataCodewords(); j++ {
				if len(data) > 0 {
					blk[j], data = int(data[0]), data[1:]
				} else {
					blk[j], pad = []int{0xec, 0x11}[pad%2], pad+1
				}
			}
			if err := reedsolomon.NewReedSolomonEncoder(reedsolomon.GenericGF_QR_CODE_FIELD_256).Encode(blk, ecb.GetECCodewordsPerBlock()); err != nil {
				t.Fatal(err)
			}
			blocks = append(blocks, blk)
		}
	}
	bits := gozxing.NewEmptyBitArray()
	put := func(dataPart bool) {
		for i := 0; ; i++ {
			more := false
			for k, blk := range blocks {
				nd := ecb.GetECBlocks()[0].GetDataCodewords()
				if len(ecb.GetECBlocks()) > 1 && k >= ecb.GetECBlocks()[0].GetCount() {
					nd = ecb.GetECBlocks()[1].GetDataCodewords()
				}
				j := i
				if !dataPart {
					j, nd = nd+i, len(blk)
				}
				if j < nd {
					_ = bits.AppendBits(blk[j], 8)
					more = true
				}
			}
			if !more {
				return
			}
		}
	}
	put(true)
	put(false)
	if bits.GetSizeInBytes() != ver.GetTotalCodewords() {
		t.Fatalf("%d codewords, version %d has %d", bits.GetSizeInBytes(), ver.GetVersionNumber(), ver.GetTotalCodewords())
	}
	n := ver.GetDimensionForVersion()
	m := encoder.NewByteMatrix(n, n)
	if err := encoder.MatrixUtil_buildMatrix(bits, level, ver, int(b[1])%8, m); err != nil {
		t.Fatal(err)
	}
	bm := make([][]bool, n)
	for y := range bm {
		bm[y] = make([]bool, n)
		for x := range bm[y] {
			bm[y][x] = m.Get(x, y) == 1
		}
	}
	c, err := media.CardOf(bm)
	if err != nil {
		t.Fatal(err)
	}
	side := (n + 6) * 5 * 10 / 9
	g := media.NewGray(side, side)
	media.TableLook(0, g)
	c.DrawGray(g, media.View{Corners: media.Pose(0.45, 0, 0, 0, 0)})
	return g
}
