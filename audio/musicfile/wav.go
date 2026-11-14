package musicfile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/kario-os/kario/audio/internal/textenc"
	"github.com/kario-os/kario/audio/tags"
)

const maxTextChunk = 1 << 20

// probeWAV reads RIFF/WAVE and RF64 (WAV past 4 GB): integer PCM of 8 to
// 32 bits, 32- and 64-bit float, WAVE_FORMAT_EXTENSIBLE; LIST/INFO and
// ID3 tags.
func probeWAV(f *os.File) (Info, error) {
	le := binary.LittleEndian
	var h [12]byte
	if _, err := io.ReadFull(f, h[:]); err != nil || (string(h[:4]) != "RIFF" && string(h[:4]) != "RF64") || string(h[8:12]) != "WAVE" {
		return Info{}, errors.New("not a WAV file")
	}
	i := Info{Container: "WAV", Codec: PCM}
	var format, align int
	var ds64Data int64 = -1
	var fmtOK bool
	pos := int64(12)
	for {
		var ch [8]byte
		if _, err := f.ReadAt(ch[:], pos); err != nil {
			break
		}
		id, size := string(ch[:4]), int64(le.Uint32(ch[4:]))
		body := pos + 8
		switch id {
		case "ds64":
			var b [24]byte
			if _, err := f.ReadAt(b[:], body); err == nil {
				ds64Data = int64(le.Uint64(b[8:]))
			}
		case "fmt ":
			b := make([]byte, min(size, 64))
			if _, err := f.ReadAt(b, body); err != nil || len(b) < 16 {
				return i, errors.New("WAV: short fmt chunk")
			}
			format = int(le.Uint16(b))
			i.Channels = int(le.Uint16(b[2:]))
			i.Rate = int(le.Uint32(b[4:]))
			align = int(le.Uint16(b[12:]))
			i.Bits = int(le.Uint16(b[14:]))
			if format == 0xfffe && len(b) >= 40 {
				if v := int(le.Uint16(b[18:])); v > 0 {
					i.Bits = v
				}
				format = int(le.Uint16(b[24:]))
			}
			fmtOK = true
		case "data":
			if !fmtOK {
				return i, errors.New("WAV: data before fmt")
			}
			if size == 0xffffffff && ds64Data >= 0 {
				size = ds64Data
			}
			if st, err := f.Stat(); err == nil {
				size = min(size, st.Size()-body)
			}
			i.PCM = &Layout{Offset: body, Size: size}
		case "LIST":
			if size <= maxTextChunk {
				b := make([]byte, size)
				if _, err := f.ReadAt(b, body); err == nil && bytes.HasPrefix(b, []byte("INFO")) {
					riffInfo(b[4:], &i.Tags)
				}
			}
		case "id3 ", "ID3 ":
			if _, err := f.Seek(body, 0); err == nil {
				t, _, _ := tags.ReadID3v2(f)
				i.Tags.Merge(t)
			}
		}
		if size == 0xffffffff && id == "data" && ds64Data < 0 {
			break
		}
		pos = body + size + size&1
	}
	if i.PCM == nil {
		return i, errors.New("WAV without data")
	}
	if i.Channels < 1 || i.Channels > 8 || align <= 0 || align%i.Channels != 0 {
		return i, fmt.Errorf("WAV: %d channels, %d bytes a frame", i.Channels, align)
	}
	i.PCM.BytesPerSample = align / i.Channels
	switch format {
	case 1:
		if i.PCM.BytesPerSample < 1 || i.PCM.BytesPerSample > 4 || i.Bits > 8*i.PCM.BytesPerSample || i.Bits < 1 {
			return i, fmt.Errorf("WAV: %d-bit samples in %d bytes", i.Bits, i.PCM.BytesPerSample)
		}
	case 3:
		if i.PCM.BytesPerSample != 4 && i.PCM.BytesPerSample != 8 {
			return i, fmt.Errorf("WAV: %d-byte floats", i.PCM.BytesPerSample)
		}
		i.Float, i.Bits = true, 8*i.PCM.BytesPerSample
	default:
		return i, fmt.Errorf("WAV format %#x: %w", format, ErrUnsupported)
	}
	i.Frames = i.PCM.Size / int64(align)
	return i, nil
}

// riffInfo reads the sub-chunks of LIST/INFO.
func riffInfo(b []byte, t *tags.Tags) {
	le := binary.LittleEndian
	keys := map[string]string{"INAM": "TITLE", "IART": "ARTIST", "IPRD": "ALBUM", "ITRK": "TRACKNUMBER", "IPRT": "TRACKNUMBER",
		"ICRD": "DATE", "IGNR": "GENRE"}
	for len(b) >= 8 {
		id, n := string(b[:4]), int(le.Uint32(b[4:]))
		if n < 0 || 8+n > len(b) {
			return
		}
		if k, ok := keys[id]; ok {
			t.Set(k, textenc.Decode(bytes.TrimRight(b[8:8+n], "\x00")))
		}
		b = b[8+n+n&1:]
	}
}

// probeAIFF reads AIFF and AIFF-C (uncompressed: big-endian "twos"/NONE,
// little-endian "sowt", 32/64-bit float).
func probeAIFF(f *os.File) (Info, error) {
	be := binary.BigEndian
	var h [12]byte
	if _, err := io.ReadFull(f, h[:]); err != nil || string(h[:4]) != "FORM" || (string(h[8:12]) != "AIFF" && string(h[8:12]) != "AIFC") {
		return Info{}, errors.New("not an AIFF file")
	}
	aifc := string(h[8:12]) == "AIFC"
	i := Info{Container: "AIFF", Codec: PCM}
	end := 8 + int64(be.Uint32(h[4:]))
	little, float := false, false
	var ssnd int64 = -1
	var ssndSize int64
	pos := int64(12)
	for pos+8 <= end {
		var ch [8]byte
		if _, err := f.ReadAt(ch[:], pos); err != nil {
			break
		}
		id, size := string(ch[:4]), int64(be.Uint32(ch[4:]))
		body := pos + 8
		switch id {
		case "COMM":
			b := make([]byte, min(size, 64))
			if _, err := f.ReadAt(b, body); err != nil || len(b) < 18 {
				return i, errors.New("AIFF: short COMM")
			}
			i.Channels = int(be.Uint16(b))
			i.Frames = int64(be.Uint32(b[2:]))
			i.Bits = int(be.Uint16(b[6:]))
			i.Rate = int(math.Round(extended(b[8:18])))
			if aifc && len(b) >= 22 {
				switch strings.ToLower(string(b[18:22])) {
				case "none", "twos":
				case "sowt":
					little = true
				case "fl32", "fl64":
					float = true
				case "in24", "in32":
				default:
					return i, fmt.Errorf("AIFF-C compression %q: %w", b[18:22], ErrUnsupported)
				}
			}
		case "SSND":
			var b [8]byte
			if _, err := f.ReadAt(b[:], body); err != nil {
				return i, err
			}
			ssnd, ssndSize = body+8+int64(be.Uint32(b[:])), size-8-int64(be.Uint32(b[:]))
		case "ID3 ", "id3 ":
			if _, err := f.Seek(body, 0); err == nil {
				t, _, _ := tags.ReadID3v2(f)
				i.Tags.Merge(t)
			}
		case "NAME", "AUTH":
			if size <= 4096 {
				b := make([]byte, size)
				if _, err := f.ReadAt(b, body); err == nil {
					k := "TITLE"
					if id == "AUTH" {
						k = "ARTIST"
					}
					i.Tags.Set(k, textenc.Decode(bytes.TrimRight(b, "\x00")))
				}
			}
		}
		pos = body + size + size&1
	}
	if ssnd < 0 || i.Channels < 1 || i.Channels > 8 || i.Bits < 1 || i.Bits > 64 {
		return i, errors.New("AIFF without sound")
	}
	bps := (i.Bits + 7) / 8
	if float {
		i.Float = true
		bps = map[bool]int{true: 8, false: 4}[i.Bits > 32]
		i.Bits = 8 * bps
	} else if bps > 4 {
		return i, fmt.Errorf("AIFF: %d-bit samples", i.Bits)
	}
	i.PCM = &Layout{Offset: ssnd, Size: min(ssndSize, i.Frames*int64(bps*i.Channels)), BytesPerSample: bps, BigEndian: !little}
	return i, nil
}

// extended reads an 80-bit IEEE 754 extended number (AIFF's rate).
func extended(b []byte) float64 {
	exp := int(binary.BigEndian.Uint16(b)) & 0x7fff
	mant := binary.BigEndian.Uint64(b[2:])
	if exp == 0 && mant == 0 {
		return 0
	}
	v := math.Ldexp(float64(mant), exp-16383-63)
	if b[0]&0x80 != 0 {
		v = -v
	}
	return v
}

func probeFLAC(f *os.File) (Info, error) {
	m, err := readFLAC(f)
	if err != nil {
		return Info{}, err
	}
	i := Info{Container: "FLAC", Codec: FLAC, Rate: m.Info.Rate, Bits: m.Info.Bits, Channels: m.Info.Channels, Frames: m.Info.Samples}
	i.Tags = tags.VorbisComment(m.Comments)
	for _, p := range m.Pictures {
		if i.Tags.Cover == nil || p.Type == 3 {
			i.Tags.Cover = &tags.Cover{MIME: p.MIME, Offset: p.Offset, Size: p.Size}
		}
	}
	if _, err := f.Seek(0, 0); err == nil {
		if t, _, ok := tags.ReadID3v2(f); ok {
			i.Tags.Merge(t)
		}
	}
	return i, nil
}
