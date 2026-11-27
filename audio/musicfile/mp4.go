package musicfile

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kario-os/kario/audio/flac"
	"github.com/kario-os/kario/audio/tags"
)

func readFLAC(f *os.File) (flac.Metadata, error) { return flac.ReadMetadata(f) }

const maxAtom = 32 << 20

type atom struct {
	typ        string
	body, size int64 // body offset and size
}

// atoms lists the boxes in [from, to).
func atoms(f *os.File, from, to int64) []atom {
	be := binary.BigEndian
	var out []atom
	for from+8 <= to && len(out) < 4096 {
		var h [16]byte
		if _, err := f.ReadAt(h[:8], from); err != nil {
			break
		}
		size, hdr := int64(be.Uint32(h[:])), int64(8)
		switch size {
		case 1:
			if _, err := f.ReadAt(h[8:16], from+8); err != nil {
				return out
			}
			size, hdr = int64(be.Uint64(h[8:])), 16
		case 0:
			size = to - from
		}
		if size < hdr || from+size > to {
			break
		}
		out = append(out, atom{typ: string(h[4:8]), body: from + hdr, size: size - hdr})
		from += size
	}
	return out
}

func find(f *os.File, in atom, path ...string) (atom, bool) {
	cur := in
	for _, p := range path {
		from := cur.body
		if cur.typ == "meta" {
			from += 4 // a full box
		}
		found := false
		for _, a := range atoms(f, from, cur.body+cur.size) {
			if a.typ == p {
				cur, found = a, true
				break
			}
		}
		if !found {
			return atom{}, false
		}
	}
	return cur, true
}

// probeMP4 reads an MP4 / M4A: the sound track's codec (ALAC; AAC is not
// played here), rate, depth and length, and the iTunes-style tags.
func probeMP4(f *os.File) (Info, error) {
	be := binary.BigEndian
	st, err := f.Stat()
	if err != nil {
		return Info{}, err
	}
	root := atom{body: 0, size: st.Size()}
	top := atoms(f, 0, st.Size())
	if len(top) == 0 || top[0].typ != "ftyp" {
		return Info{}, errors.New("not an MP4 file")
	}
	moov, ok := find(f, root, "moov")
	if !ok {
		return Info{}, errors.New("MP4 without moov")
	}
	i := Info{Container: "M4A"}
	for _, trak := range atoms(f, moov.body, moov.body+moov.size) {
		if trak.typ != "trak" {
			continue
		}
		hdlr, ok := find(f, trak, "mdia", "hdlr")
		var ht [12]byte
		if !ok || hdlr.size < 12 {
			continue
		}
		if _, err := f.ReadAt(ht[:], hdlr.body); err != nil || string(ht[8:12]) != "soun" {
			continue
		}
		mdhd, ok := find(f, trak, "mdia", "mdhd")
		if !ok {
			continue
		}
		var m [32]byte
		f.ReadAt(m[:], mdhd.body)
		var scale, dur int64
		if m[0] == 1 {
			scale, dur = int64(be.Uint32(m[20:])), int64(be.Uint64(m[24:]))
		} else {
			scale, dur = int64(be.Uint32(m[12:])), int64(be.Uint32(m[16:]))
		}
		stsd, ok := find(f, trak, "mdia", "minf", "stbl", "stsd")
		if !ok {
			continue
		}
		entries := atoms(f, stsd.body+8, stsd.body+stsd.size)
		if len(entries) == 0 {
			continue
		}
		e := entries[0]
		switch e.typ {
		case "alac":
			i.Codec = ALAC
			var b [28 + 12 + 24]byte
			n, _ := f.ReadAt(b[:], e.body)
			i.Channels, i.Bits, i.Rate = int(be.Uint16(b[16:])), int(be.Uint16(b[18:])), int(be.Uint32(b[24:])>>16)
			for _, c := range atoms(f, e.body+28, e.body+e.size) {
				if c.typ == "alac" && c.size >= 28 && n >= 0 {
					var cfg [28]byte
					f.ReadAt(cfg[:], c.body)
					i.Bits, i.Channels, i.Rate = int(cfg[9]), int(cfg[13]), int(be.Uint32(cfg[24:]))
				}
			}
		case "mp4a":
			return i, fmt.Errorf("AAC: %w", ErrUnsupported)
		default:
			return i, fmt.Errorf("MP4 audio %q: %w", e.typ, ErrUnsupported)
		}
		if scale > 0 && i.Rate > 0 {
			i.Frames = dur * int64(i.Rate) / scale
		}
		break
	}
	if i.Codec == "" {
		return i, errors.New("MP4 without a sound track")
	}
	if ilst, ok := find(f, moov, "udta", "meta", "ilst"); ok {
		mp4Tags(f, ilst, &i.Tags)
	}
	return i, nil
}

var mp4Keys = map[string]string{"\xa9nam": "TITLE", "\xa9ART": "ARTIST", "\xa9alb": "ALBUM", "aART": "ALBUMARTIST",
	"\xa9gen": "GENRE", "\xa9day": "DATE", "\xa9wrt": "COMPOSER"}

func mp4Tags(f *os.File, ilst atom, t *tags.Tags) {
	be := binary.BigEndian
	read := func(a atom) []byte {
		if a.size > maxAtom || a.size < 0 {
			return nil
		}
		b := make([]byte, a.size)
		if _, err := f.ReadAt(b, a.body); err != nil && err != io.EOF {
			return nil
		}
		return b
	}
	for _, it := range atoms(f, ilst.body, ilst.body+ilst.size) {
		var name string
		var data []byte
		var typ uint32
		for _, c := range atoms(f, it.body, it.body+it.size) {
			switch c.typ {
			case "name":
				if b := read(c); len(b) > 4 {
					name = string(b[4:])
				}
			case "data":
				if c.typ == "data" && it.typ == "covr" && t.Cover == nil && c.size > 8 {
					var h [4]byte
					f.ReadAt(h[:], c.body)
					mime := "image/jpeg"
					if be.Uint32(h[:]) == 14 {
						mime = "image/png"
					}
					t.Cover = &tags.Cover{MIME: mime, Offset: c.body + 8, Size: c.size - 8}
					continue
				}
				if b := read(c); len(b) >= 8 {
					typ, data = be.Uint32(b), b[8:]
				}
			}
		}
		switch {
		case it.typ == "trkn" || it.typ == "disk":
			if len(data) >= 6 {
				n, total := int(be.Uint16(data[2:])), int(be.Uint16(data[4:]))
				if it.typ == "trkn" {
					t.Track, t.TrackTotal = n, total
				} else {
					t.Disc, t.DiscTotal = n, total
				}
			}
		case it.typ == "----":
			t.Set(strings.ToUpper(name), string(data))
		case typ == 1:
			if k, ok := mp4Keys[it.typ]; ok {
				t.Set(k, string(data))
			}
		}
	}
}
