package tags

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"

	"github.com/kario-os/kario/audio/internal/textenc"
)

// MaxID3 bounds the tag read into memory (covers included).
const MaxID3 = 16 << 20

// ID3v2Size is the length of the ID3v2 tag whose 10-byte header is h,
// header and footer included; 0 if h is not one.
func ID3v2Size(h []byte) int {
	if len(h) < 10 || string(h[:3]) != "ID3" || h[3] < 2 || h[3] > 4 {
		return 0
	}
	n := syncsafe(h[6:10]) + 10
	if h[5]&0x10 != 0 {
		n += 10
	}
	return n
}

// ReadID3v2 reads the ID3v2 tag at the reader's position, if there is one.
func ReadID3v2(r io.Reader) (Tags, int, bool) {
	var h [10]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Tags{}, 0, false
	}
	n := ID3v2Size(h[:])
	if n == 0 || n > MaxID3 {
		return Tags{}, n, false
	}
	b := make([]byte, n)
	copy(b, h[:])
	if _, err := io.ReadFull(r, b[10:]); err != nil {
		return Tags{}, n, false
	}
	t, ok := ID3v2(b)
	return t, n, ok
}

func syncsafe(b []byte) int {
	return int(b[0]&0x7f)<<21 | int(b[1]&0x7f)<<14 | int(b[2]&0x7f)<<7 | int(b[3]&0x7f)
}

func unsync(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte{0xff, 0x00}, []byte{0xff})
}

// ID3v2 reads a whole tag (versions 2.2, 2.3 and 2.4).
func ID3v2(b []byte) (Tags, bool) {
	var t Tags
	if len(b) < 10 || string(b[:3]) != "ID3" {
		return t, false
	}
	ver, flags := b[3], b[5]
	body := b[10:min(len(b), 10+syncsafe(b[6:10]))]
	if flags&0x80 != 0 && ver < 4 {
		body = unsync(body)
	}
	if flags&0x40 != 0 && ver >= 3 && len(body) >= 4 {
		ext := int(binary.BigEndian.Uint32(body)) + 4
		if ver == 4 {
			ext = syncsafe(body)
		}
		if ext > len(body) {
			return t, false
		}
		body = body[ext:]
	}
	idLen, hdrLen := 4, 10
	if ver == 2 {
		idLen, hdrLen = 3, 6
	}
	for len(body) >= hdrLen && body[0] != 0 {
		id := string(body[:idLen])
		var size int
		var fflags uint16
		switch ver {
		case 2:
			size = int(body[3])<<16 | int(body[4])<<8 | int(body[5])
		case 3:
			size = int(binary.BigEndian.Uint32(body[4:]))
			fflags = binary.BigEndian.Uint16(body[8:])
		default:
			size = syncsafe(body[4:8])
			fflags = binary.BigEndian.Uint16(body[8:])
		}
		if size < 0 || hdrLen+size > len(body) {
			break
		}
		data := body[hdrLen : hdrLen+size]
		body = body[hdrLen+size:]
		switch ver {
		case 3:
			if fflags&0x00c0 != 0 { // compressed or encrypted
				continue
			}
			if fflags&0x0020 != 0 && len(data) > 0 {
				data = data[1:]
			}
		case 4:
			if fflags&0x000c != 0 {
				continue
			}
			if fflags&0x0040 != 0 && len(data) > 0 {
				data = data[1:]
			}
			if fflags&0x0001 != 0 && len(data) >= 4 {
				data = data[4:]
			}
			if fflags&0x0002 != 0 || flags&0x80 != 0 {
				data = unsync(data)
			}
		}
		t.frame(id, data)
	}
	return t, true
}

var id3Fields = map[string]string{
	"TIT2": "TITLE", "TT2": "TITLE",
	"TPE1": "ARTIST", "TP1": "ARTIST",
	"TALB": "ALBUM", "TAL": "ALBUM",
	"TPE2": "ALBUMARTIST", "TP2": "ALBUMARTIST",
	"TRCK": "TRACKNUMBER", "TRK": "TRACKNUMBER",
	"TPOS": "DISCNUMBER", "TPA": "DISCNUMBER",
	"TCON": "GENRE", "TCO": "GENRE",
	"TDRC": "DATE", "TYER": "DATE", "TYE": "DATE",
	"TCOM": "COMPOSER", "TCM": "COMPOSER",
}

func (t *Tags) frame(id string, data []byte) {
	if len(data) == 0 {
		return
	}
	switch id {
	case "TXXX", "TXX":
		enc := data[0]
		desc, rest := cutText(enc, data[1:])
		t.Set(decodeText(enc, desc), firstValue(decodeText(enc, rest)))
		return
	case "APIC", "PIC":
		enc := data[0]
		var mime string
		rest := data[1:]
		if id == "PIC" {
			if len(rest) < 3 {
				return
			}
			mime = map[string]string{"JPG": "image/jpeg", "PNG": "image/png"}[strings.ToUpper(string(rest[:3]))]
			rest = rest[3:]
		} else {
			i := bytes.IndexByte(rest, 0)
			if i < 0 {
				return
			}
			mime, rest = string(rest[:i]), rest[i+1:]
		}
		if len(rest) < 1 {
			return
		}
		typ := rest[0]
		_, img := cutText(enc, rest[1:])
		if len(img) == 0 {
			return
		}
		if mime == "" || !strings.Contains(mime, "/") {
			mime = "image/" + strings.ToLower(strings.TrimPrefix(mime, "image/"))
		}
		if t.Cover == nil || typ == 3 {
			t.Cover = &Cover{MIME: mime, Data: img, Size: int64(len(img))}
		}
		return
	}
	key, ok := id3Fields[id]
	if !ok {
		return
	}
	v := firstValue(decodeText(data[0], data[1:]))
	if key == "GENRE" {
		v = genre(v)
	}
	t.Set(key, v)
}

// cutText splits at the encoding's terminator.
func cutText(enc byte, b []byte) ([]byte, []byte) {
	if enc == 1 || enc == 2 {
		for i := 0; i+1 < len(b); i += 2 {
			if b[i] == 0 && b[i+1] == 0 {
				return b[:i], b[i+2:]
			}
		}
		return b, nil
	}
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return b[:i], b[i+1:]
	}
	return b, nil
}

func decodeText(enc byte, b []byte) string {
	switch enc {
	case 1:
		if len(b) >= 2 && b[0] == 0xfe && b[1] == 0xff {
			return textenc.UTF16(b[2:], true)
		}
		if len(b) >= 2 && b[0] == 0xff && b[1] == 0xfe {
			b = b[2:]
		}
		return textenc.UTF16(b, false)
	case 2:
		return textenc.UTF16(b, true)
	case 3:
		return string(b)
	}
	return textenc.Legacy(bytes.TrimRight(b, "\x00"))
}

// firstValue: ID3v2.4 separates several values with NUL.
func firstValue(s string) string {
	parts := strings.Split(strings.TrimRight(s, "\x00"), "\x00")
	return strings.Join(parts, ", ")
}

// genre turns ID3's "(17)" or "(17)Rock" into its text when there is one.
func genre(v string) string {
	if strings.HasPrefix(v, "(") {
		if i := strings.IndexByte(v, ')'); i > 0 && i+1 < len(v) {
			return v[i+1:]
		}
	}
	return v
}

// ID3v1 reads the 128-byte tag at the end of an MP3.
func ID3v1(b []byte) (Tags, bool) {
	var t Tags
	if len(b) != 128 || string(b[:3]) != "TAG" {
		return t, false
	}
	field := func(p []byte) string { return strings.TrimSpace(textenc.Legacy(bytes.TrimRight(p, "\x00 "))) }
	t.Title, t.Artist, t.Album = field(b[3:33]), field(b[33:63]), field(b[63:93])
	if y := field(b[93:97]); y != "" {
		t.Date = y
	}
	if b[125] == 0 && b[126] != 0 {
		t.Track = int(b[126])
	}
	return t, true
}
