package musicfile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"

	"github.com/kario-os/kario/audio/tags"
)

// probeOgg reads an Ogg Vorbis or Ogg Opus file's first two packets (the
// identification and comment headers) and its last page's granule
// position, the exact length.
func probeOgg(f *os.File) (Info, error) {
	le := binary.LittleEndian
	var packets [][]byte
	var cur []byte
	pos := int64(0)
	for len(packets) < 2 && pos < 4<<20 {
		var h [27]byte
		if _, err := f.ReadAt(h[:], pos); err != nil || string(h[:4]) != "OggS" {
			break
		}
		segs := make([]byte, h[26])
		if _, err := f.ReadAt(segs, pos+27); err != nil {
			break
		}
		at := pos + 27 + int64(len(segs))
		for _, s := range segs {
			b := make([]byte, s)
			if _, err := f.ReadAt(b, at); err != nil {
				return Info{}, err
			}
			at += int64(s)
			cur = append(cur, b...)
			if s < 255 {
				packets = append(packets, cur)
				cur = nil
				if len(packets) == 2 {
					break
				}
			}
		}
		pos = at
	}
	if len(packets) < 2 {
		return Info{}, errors.New("not an Ogg file with Vorbis or Opus headers")
	}
	id, comment := packets[0], packets[1]
	i := Info{Container: "Ogg", Bits: 16}
	var preskip int64
	switch {
	case bytes.HasPrefix(id, []byte("\x01vorbis")) && len(id) >= 16:
		i.Codec = Vorbis
		i.Channels, i.Rate = int(id[11]), int(le.Uint32(id[12:]))
		if bytes.HasPrefix(comment, []byte("\x03vorbis")) {
			i.Tags = tags.VorbisComment(comment[7:])
		}
	case bytes.HasPrefix(id, []byte("OpusHead")) && len(id) >= 19:
		i.Codec, i.Container = Opus, "Opus"
		i.Channels, i.Rate = int(id[9]), 48000
		preskip = int64(le.Uint16(id[10:]))
		if bytes.HasPrefix(comment, []byte("OpusTags")) {
			i.Tags = tags.VorbisComment(comment[8:])
		}
	default:
		return Info{}, errors.New("Ogg: neither Vorbis nor Opus")
	}
	st, err := f.Stat()
	if err != nil {
		return i, err
	}
	tail := make([]byte, min(st.Size(), 64<<10))
	if _, err := f.ReadAt(tail, st.Size()-int64(len(tail))); err != nil && err != io.EOF {
		return i, err
	}
	if k := bytes.LastIndex(tail, []byte("OggS")); k >= 0 && k+14 <= len(tail) {
		if g := int64(le.Uint64(tail[k+6:])); g > 0 {
			i.Frames = max(0, g-preskip)
		}
	}
	return i, nil
}
