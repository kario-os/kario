package tags

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unicode/utf8"
)

func id3(ver byte, frames ...[]byte) []byte {
	body := bytes.Join(frames, nil)
	n := len(body)
	return append([]byte{'I', 'D', '3', ver, 0, 0, byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}, body...)
}

func frame(id string, data string) []byte {
	return append(binary.BigEndian.AppendUint32([]byte(id), uint32(len(data))), append([]byte{0, 0}, data...)...)
}

func vorbis(comments ...string) []byte {
	b := binary.LittleEndian.AppendUint32(nil, 5)
	b = append(b, "Kario"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(comments)))
	for _, c := range comments {
		b = append(binary.LittleEndian.AppendUint32(b, uint32(len(c))), c...)
	}
	return b
}

func valid(t *testing.T, g Tags) {
	for _, s := range []string{g.Title, g.Artist, g.Album, g.AlbumArtist, g.Genre, g.Date, g.Composer} {
		if !utf8.ValidString(s) {
			t.Fatalf("text that is not UTF-8: %q", s)
		}
	}
}

// FuzzID3v2 is an MP3's (or WAV's, AIFF's, DSF's) ID3v2 tag.
func FuzzID3v2(f *testing.F) {
	f.Add(id3(3, frame("TIT2", "\x03Title"), frame("TRCK", "\x003/9"), frame("TXXX", "\x00REPLAYGAIN_TRACK_GAIN\x00-6.5 dB")))
	f.Add(id3(4, frame("APIC", "\x00image/png\x00\x03\x00\x89PNG"), frame("TCON", "\x01\xff\xfe(\x001\x007\x00)\x00")))
	f.Add(id3(2, []byte("TT2\x00\x00\x06\x00Title"), []byte("PIC\x00\x00\x07\x00PNG\x03\x00xx")))
	f.Fuzz(func(t *testing.T, data []byte) {
		g, _, _ := ReadID3v2(bytes.NewReader(data))
		valid(t, g)
		g, _ = ID3v2(data)
		valid(t, g)
		if g.Cover != nil && g.Cover.Size != int64(len(g.Cover.Data)) {
			t.Fatalf("a cover of %d bytes says %d", len(g.Cover.Data), g.Cover.Size)
		}
	})
}

// FuzzVorbisComment is a FLAC or Ogg comment block, and the pictures in it.
func FuzzVorbisComment(f *testing.F) {
	f.Add(vorbis("TITLE=\u7a7a\u5c71", "ARTIST=Kario", "TRACKNUMBER=3/9", "REPLAYGAIN_ALBUM_GAIN=-7.25 dB", "R128_TRACK_GAIN=-512"))
	f.Add(vorbis("METADATA_BLOCK_PICTURE=AAAAAwAAAAlpbWFnZS9wbmcAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAEiVBORw=="))
	f.Fuzz(func(t *testing.T, data []byte) {
		valid(t, VorbisComment(data))
		if p := flacPicture(data); p != nil && len(p.Data) > len(data) {
			t.Fatalf("a %d-byte picture from %d bytes", len(p.Data), len(data))
		}
	})
}

// FuzzTail is the end of a file: APEv2 before ID3v1.
func FuzzTail(f *testing.F) {
	item := func(k, v string, flags uint32) []byte {
		b := binary.LittleEndian.AppendUint32(nil, uint32(len(v)))
		b = binary.LittleEndian.AppendUint32(b, flags)
		return append(append(append(b, k...), 0), v...)
	}
	items := append(item("Title", "Song", 0), item("Cover Art (Front)", "front.jpg\x00\xff\xd8", 2)...)
	footer := append([]byte("APETAGEX"), make([]byte, 24)...)
	binary.LittleEndian.PutUint32(footer[8:], 2000)
	binary.LittleEndian.PutUint32(footer[12:], uint32(len(items)+32))
	binary.LittleEndian.PutUint32(footer[16:], 2)
	v1 := make([]byte, 128)
	copy(v1, "TAGTitle")
	v1[126] = 4
	f.Add(append(append([]byte("audio"), items...), footer...))
	f.Add(append(append(append([]byte("audio"), items...), footer...), v1...))
	f.Fuzz(func(t *testing.T, data []byte) {
		g, start := ReadTail(bytes.NewReader(data), int64(len(data)))
		valid(t, g)
		if start < 0 || start > int64(len(data)) {
			t.Fatalf("the tags start at %d of %d bytes", start, len(data))
		}
	})
}
