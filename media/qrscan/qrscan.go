// Package qrscan reads QR codes in camera pictures: phone-mediad's scanner
// (scan.qr) decodes the frames its camera captures, so the text an app gets
// is what the camera saw. The decoder is gozxing, a Go port of ZXing (MIT,
// ZXing's own code Apache-2.0).
//
// A frame shows whatever anyone holds up to the camera: its size is bounded
// before decoding, a panic in the decoder is an error, and the text is
// bounded and valid UTF-8.
package qrscan

import (
	"errors"
	"fmt"
	"strings"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode/decoder"
	"github.com/makiuchi-d/gozxing/qrcode/detector"

	"github.com/kario-os/kario/media"
)

const (
	// MaxPixels bounds a frame: the decoder's time and memory grow with
	// its area. mediad decodes 640×480.
	MaxPixels = 1280 * 960
	MaxSide   = 4096
	// MinSide is a version 1 code's width in modules.
	MinSide = 21
	// MaxText bounds the text: a code holds at most 7089 digits.
	MaxText = 8 << 10
)

var (
	ErrNotFound = errors.New("no readable QR code in the frame")
	ErrSize     = errors.New("frame size out of bounds")
	ErrDecoder  = errors.New("the QR decoder failed")
)

// Decode returns the text of a QR code in g.
func Decode(g *media.Gray) (text string, err error) {
	if g == nil || g.W < MinSide || g.H < MinSide || g.W > MaxSide || g.H > MaxSide || g.W*g.H > MaxPixels || len(g.Pix) != g.W*g.H {
		return "", ErrSize
	}
	defer func() {
		if p := recover(); p != nil {
			text, err = "", fmt.Errorf("%w: %v", ErrDecoder, p)
		}
	}()
	src, err := gozxing.NewPlanarYUVLuminanceSource(g.Pix, g.W, g.H, 0, 0, g.W, g.H, false)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDecoder, err)
	}
	// An even picture has no black and white to tell apart.
	m, err := gozxing.NewHybridBinarizer(src).GetBlackMatrix()
	if err != nil {
		return "", ErrNotFound
	}
	info := find(m)
	if info == nil {
		return "", ErrNotFound
	}
	det, err := detector.NewDetector(m).ProcessFinderPatternInfo(info)
	if err != nil {
		return "", ErrNotFound
	}
	// Byte segments without an ECI are UTF-8, what encoders write without
	// saying so; ZXing's guess takes short Chinese text for Shift_JIS.
	r, err := decoder.NewDecoder().Decode(det.GetBits(), map[gozxing.DecodeHintType]interface{}{gozxing.DecodeHintType_CHARACTER_SET: "UTF-8"})
	if err != nil {
		return "", ErrNotFound
	}
	text = r.GetText()
	if len(text) > MaxText {
		return "", fmt.Errorf("%w: %d bytes of text", ErrDecoder, len(text))
	}
	text = strings.ToValidUTF8(text, "\uFFFD")
	if text == "" {
		return "", ErrNotFound
	}
	return text, nil
}

// maxCenters bounds the finder pattern candidates of a frame: a code has
// three, a busy background adds a few.
const maxCenters = 64

// find is gozxing's FinderPatternFinder.Find with TRY_HARDER (every third
// row searched, so small codes are found too) and a bound: Find keeps every
// candidate and tries all their triples, so a picture full of finder-like
// marks takes seconds to minutes (7.6 s at 163×174). Past maxCenters
// candidates there is no code to read.
func find(m *gozxing.BitMatrix) *detector.FinderPatternInfo {
	f := detector.NewFinderPatternFinder(m, nil)
	maxI, maxJ := m.GetHeight(), m.GetWidth()
	iSkip := detector.FinderPatternFinder_MIN_SKIP
	done, skipped := false, false
	stateCount := make([]int, 5)
	// confirm is HandlePossibleCenter and what Find does after it: stop
	// once three candidates agree, or skip rows to where the third should
	// be (by rows, ending this one).
	confirm := func(i, j int) (ok bool, rows int) {
		if !f.HandlePossibleCenter(stateCount, i, j) {
			return false, -1
		}
		if skipped {
			done = f.HaveMultiplyConfirmedCenters()
			return true, -1
		}
		confirmed := 0
		for _, c := range f.GetPossibleCenters() {
			if c.GetCount() >= detector.FinderPatternFinder_CENTER_QUORUM {
				confirmed++
			}
		}
		// FindRowSkip marks the skip taken once it sees two confirmed.
		skipped = len(f.GetPossibleCenters()) > 1 && confirmed >= 2
		if rowSkip := f.FindRowSkip(); rowSkip > stateCount[2] {
			return true, rowSkip - stateCount[2] - 2
		}
		return true, -1
	}
	for i := iSkip - 1; i < maxI && !done; i += iSkip {
		clear(stateCount)
		state := 0
		for j := 0; j < maxJ && len(f.GetPossibleCenters()) <= maxCenters; j++ {
			if m.Get(j, i) {
				if state&1 == 1 {
					state++
				}
				stateCount[state]++
				continue
			}
			if state&1 == 1 {
				stateCount[state]++
				continue
			}
			if state != 4 {
				state++
				stateCount[state]++
				continue
			}
			if !detector.FinderPatternFinder_foundPatternCross(stateCount) {
				detector.FinderPatternFinder_ShiftCounts2(stateCount)
				state = 3
				continue
			}
			ok, rows := confirm(i, j)
			if !ok {
				detector.FinderPatternFinder_ShiftCounts2(stateCount)
				state = 3
				continue
			}
			iSkip = 2
			if rows >= 0 {
				i, j = i+rows, maxJ-1
			}
			state = 0
			clear(stateCount)
		}
		if len(f.GetPossibleCenters()) > maxCenters {
			return nil
		}
		if detector.FinderPatternFinder_foundPatternCross(stateCount) && f.HandlePossibleCenter(stateCount, i, maxJ) {
			iSkip = stateCount[0]
			if skipped {
				done = f.HaveMultiplyConfirmedCenters()
			}
		}
	}
	if len(f.GetPossibleCenters()) > maxCenters {
		return nil
	}
	fps, err := f.SelectBestPatterns()
	if err != nil {
		return nil
	}
	bl, tl, tr := gozxing.ResultPoint_OrderBestPatterns(fps[0], fps[1], fps[2])
	return detector.NewFinderPatternInfo(bl.(*detector.FinderPattern), tl.(*detector.FinderPattern), tr.(*detector.FinderPattern))
}
