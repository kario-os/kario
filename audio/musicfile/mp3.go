package musicfile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"

	"github.com/kario-os/kario/audio/tags"
)

var mp3Bitrates = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
var mp3Rates = [4]int{44100, 48000, 32000, 0}

// mp3Decoder is the extra delay of a Layer III decoder: what gapless
// players drop besides LAME's encoder delay.
const mp3Decoder = 529

// probeMP3 reads an MPEG-1 Layer III file: ID3v2 at the start, the first
// frame's header, the Xing/Info (with LAME's encoder delay and padding)
// or VBRI header for the frame count, else the length from the bitrate;
// APEv2 and ID3v1 at the end. MPEG-2 and 2.5 (lower rates) are refused:
// the decoder in use plays them wrongly.
func probeMP3(f *os.File) (Info, error) {
	st, err := f.Stat()
	if err != nil {
		return Info{}, err
	}
	i := Info{Container: "MP3", Codec: MP3, Bits: 16}
	t, n, _ := tags.ReadID3v2(f)
	i.Tags = t
	start := int64(n)
	buf := make([]byte, 64<<10)
	k, _ := f.ReadAt(buf, start)
	buf = buf[:k]
	at := -1
	for j := 0; j+4 <= len(buf); j++ {
		if buf[j] == 0xff && buf[j+1]&0xe0 == 0xe0 && frameLen(buf[j:]) > 0 {
			if l := frameLen(buf[j:]); j+l+2 > len(buf) || buf[j+l] == 0xff && buf[j+l+1]&0xe0 == 0xe0 {
				at = j
				break
			}
		}
	}
	if at < 0 {
		return i, errors.New("MP3 without frames")
	}
	h := buf[at:]
	if h[1]>>3&3 != 3 || h[1]>>1&3 != 1 {
		return i, fmt.Errorf("MPEG-2/2.5 or not Layer III: only MPEG-1 Layer III (32, 44.1, 48 kHz) is played: %w", ErrUnsupported)
	}
	i.Rate = mp3Rates[h[2]>>2&3]
	i.Channels = 2
	mono := h[3]>>6 == 3
	if mono {
		i.Channels = 1
	}
	side := 32
	if mono {
		side = 17
	}
	be := binary.BigEndian
	var frames int64
	if x := h[4+side:]; len(x) >= 8 && (bytes.HasPrefix(x, []byte("Xing")) || bytes.HasPrefix(x, []byte("Info"))) {
		flags := be.Uint32(x[4:])
		p := 8
		if flags&1 != 0 && len(x) >= p+4 {
			frames = int64(be.Uint32(x[p:]))
			p += 4
		}
		if flags&2 != 0 {
			p += 4
		}
		if flags&4 != 0 {
			p += 100
		}
		if flags&8 != 0 {
			p += 4
		}
		if len(x) >= p+24 && bytes.HasPrefix(x[p:], []byte("LAME")) || len(x) >= p+24 && bytes.HasPrefix(x[p:], []byte("Lavc")) || len(x) >= p+24 && bytes.HasPrefix(x[p:], []byte("Lavf")) {
			d := x[p+21:]
			i.Delay = int(d[0])<<4 | int(d[1])>>4
			i.Padding = int(d[1]&15)<<8 | int(d[2])
		}
		frames-- // the Xing frame itself is silent
	} else if x := h[36:]; len(x) >= 18 && bytes.HasPrefix(x, []byte("VBRI")) {
		frames = int64(be.Uint32(x[14:]))
	}
	_, end := tags.ReadTail(f, st.Size())
	if frames <= 0 {
		frames = (end - start - int64(at)) / int64(frameLen(h))
	}
	i.Frames = frames * 1152
	if i.Delay > 0 || i.Padding > 0 {
		i.Delay += mp3Decoder
		i.Padding = max(0, i.Padding-mp3Decoder)
		i.Frames = max(0, i.Frames-int64(i.Delay+i.Padding))
	}
	tail, _ := tags.ReadTail(f, st.Size())
	i.Tags.Merge(tail)
	return i, nil
}

// frameLen is the length of the MPEG-1 Layer III frame whose header is at
// b, 0 if it is not one.
func frameLen(b []byte) int {
	if len(b) < 4 || b[0] != 0xff || b[1]&0xe0 != 0xe0 {
		return 0
	}
	if b[1]>>3&3 == 1 || b[1]>>1&3 == 0 {
		return 0
	}
	br, sr := mp3Bitrates[b[2]>>4], b[2]>>2&3
	if br == 0 || sr == 3 {
		return 0
	}
	rate := mp3Rates[sr]
	switch b[1] >> 3 & 3 {
	case 2:
		rate /= 2
	case 0:
		rate /= 4
	}
	pad := int(b[2] >> 1 & 1)
	if b[1]>>3&3 == 3 {
		return 144*br*1000/rate + pad
	}
	return 72*br*1000/rate + pad
}
