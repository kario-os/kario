// Package dsd reads Direct Stream Digital audio: DSF and DSDIFF (.dff,
// plain or DST-compressed) files and the tracks of SACD disc images
// (.iso), and turns DSD into what a sound card takes: PCM through a
// low-pass decimation filter, DoP (DSD over PCM) or ALSA's native DSD
// sample formats.
//
// Inside the package DSD travels as byte frames: one byte per channel in
// turn, each byte eight consecutive 1-bit samples with the earliest in the
// most significant bit (DSDIFF's layout). Rates are DSD samples per second
// per channel (2822400 for DSD64).
package dsd

import "fmt"

// Base is DSD64's rate, 64 × 44.1 kHz.
const Base = 2822400

// Silence is the idle pattern of DSD: as many ones as zeros, no DC.
const Silence = 0x69

// Stream is plain DSD to read.
type Stream interface {
	Rate() int
	Channels() int
	// Len is the length in byte frames (8 samples per channel each).
	Len() int64
	// Read fills p (a multiple of the channel count) with byte frames.
	Read(p []byte) (int, error)
	// SeekFrame moves to a byte frame.
	SeekFrame(n int64) error
	Close() error
}

// Frames is DST-compressed DSD: one coded frame per 1/75 s (SACD's and
// DSDIFF's DST), which ffmpeg decodes (audio/hifi).
type Frames interface {
	Rate() int
	Channels() int
	// Count is the number of frames.
	Count() int64
	// Next returns the next coded frame; io.EOF after the last.
	Next() ([]byte, error)
	SeekFrame(n int64) error
	Close() error
}

// FramesPerSecond: DST frames (and SACD's timecodes) are 1/75 s.
const FramesPerSecond = 75

// Name is the usual name of a DSD rate: DSD64, DSD128, DSD256.
func Name(rate int) string {
	if rate%Base == 0 {
		return fmt.Sprintf("DSD%d", 64*rate/Base)
	}
	if rate%(Base/441*480) == 0 {
		return fmt.Sprintf("DSD%d", 64*rate/(Base/441*480))
	}
	return fmt.Sprintf("DSD %.1f MHz", float64(rate)/1e6)
}

// reverse is a byte with its bits in the opposite order (DSF stores the
// earliest sample in the least significant bit).
var reverse [256]byte

func init() {
	for i := range reverse {
		var r byte
		for b := 0; b < 8; b++ {
			if i&(1<<b) != 0 {
				r |= 0x80 >> b
			}
		}
		reverse[i] = r
	}
}
