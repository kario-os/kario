package musicfile

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/kario-os/kario/audio/tags"
)

// probeAPE reads a Monkey's Audio header (both the 3.98+ layout with a
// descriptor and the older one) and the APEv2 tag at the end.
func probeAPE(f *os.File) (Info, error) {
	le := binary.LittleEndian
	start := int64(0)
	if _, n, ok := tags.ReadID3v2(f); ok || n > 0 {
		start = int64(n)
	}
	var b [128]byte
	if _, err := f.ReadAt(b[:], start); err != nil && err != io.EOF {
		return Info{}, err
	}
	if string(b[:4]) != "MAC " {
		return Info{}, errors.New("not a Monkey's Audio file")
	}
	version := int(le.Uint16(b[4:]))
	i := Info{Container: "APE", Codec: APE}
	var blocksPerFrame, finalBlocks, totalFrames int64
	if version >= 3980 {
		desc := int64(le.Uint32(b[8:]))
		var h [24]byte
		if _, err := f.ReadAt(h[:], start+desc); err != nil {
			return i, err
		}
		blocksPerFrame = int64(le.Uint32(h[4:]))
		finalBlocks = int64(le.Uint32(h[8:]))
		totalFrames = int64(le.Uint32(h[12:]))
		i.Bits = int(le.Uint16(h[16:]))
		i.Channels = int(le.Uint16(h[18:]))
		i.Rate = int(le.Uint32(h[20:]))
	} else {
		level := int(le.Uint16(b[6:]))
		flags := int(le.Uint16(b[8:]))
		i.Channels = int(le.Uint16(b[10:]))
		i.Rate = int(le.Uint32(b[12:]))
		totalFrames = int64(le.Uint32(b[24:]))
		finalBlocks = int64(le.Uint32(b[28:]))
		switch {
		case version >= 3950:
			blocksPerFrame = 73728 * 4
		case version >= 3900 || version >= 3800 && level == 4000:
			blocksPerFrame = 73728
		default:
			blocksPerFrame = 9216
		}
		i.Bits = 16
		if flags&1 != 0 {
			i.Bits = 8
		} else if flags&8 != 0 {
			i.Bits = 24
		}
	}
	if totalFrames > 0 {
		i.Frames = (totalFrames-1)*blocksPerFrame + finalBlocks
	}
	if i.Channels < 1 || i.Channels > 8 || i.Bits < 8 || i.Bits > 32 {
		return i, fmt.Errorf("APE: %d channels, %d bits", i.Channels, i.Bits)
	}
	if st, err := f.Stat(); err == nil {
		i.Tags, _ = tags.ReadTail(f, st.Size())
	}
	return i, nil
}

var wvRates = []int{6000, 8000, 9600, 11025, 12000, 16000, 22050, 24000, 32000, 44100, 48000, 64000, 88200, 96000, 192000}

// probeWV reads the first WavPack block header (and its metadata for the
// channel count and an unusual rate) and the APEv2 tag at the end.
func probeWV(f *os.File) (Info, error) {
	le := binary.LittleEndian
	var h [32]byte
	if _, err := f.ReadAt(h[:], 0); err != nil || string(h[:4]) != "wvpk" {
		return Info{}, errors.New("not a WavPack file")
	}
	size := int64(le.Uint32(h[4:])) + 8
	total := int64(le.Uint32(h[12:]))
	if h[11] != 0 || total != 0xffffffff {
		total |= int64(h[11]) << 32
	} else {
		total = 0
	}
	flags := le.Uint32(h[24:])
	if flags&(1<<31) != 0 {
		return Info{}, fmt.Errorf("WavPack DSD: %w", ErrUnsupported)
	}
	i := Info{Container: "WV", Codec: WavPack, Frames: total}
	bytesPer := int(flags&3) + 1
	shift := int(flags >> 13 & 31)
	i.Bits = 8*bytesPer - shift
	i.Float = flags&0x80 != 0
	if i.Float {
		i.Bits = 32
	}
	i.Channels = 2
	if flags&4 != 0 {
		i.Channels = 1
	}
	if k := int(flags >> 23 & 15); k < len(wvRates) {
		i.Rate = wvRates[k]
	}
	meta := make([]byte, min(size-32, 1<<16))
	if _, err := f.ReadAt(meta, 32); err == nil || err == io.EOF {
		for p := 0; p+2 <= len(meta); {
			id := meta[p]
			n := int(meta[p+1]) * 2
			p += 2
			if id&0x80 != 0 {
				if p+2 > len(meta) {
					break
				}
				n += (int(meta[p]) | int(meta[p+1])<<8) << 9
				p += 2
			}
			if p+n > len(meta) {
				break
			}
			body := meta[p : p+n]
			if id&0x40 != 0 && n > 0 {
				body = body[:n-1]
			}
			switch id & 0x3f {
			case 0x0d:
				if len(body) >= 1 {
					i.Channels = int(body[0])
				}
			case 0x27:
				if len(body) >= 3 {
					i.Rate = int(body[0]) | int(body[1])<<8 | int(body[2])<<16
				}
			}
			p += n
		}
	}
	if st, err := f.Stat(); err == nil {
		i.Tags, _ = tags.ReadTail(f, st.Size())
	}
	return i, nil
}
