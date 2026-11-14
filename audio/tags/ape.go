package tags

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
)

// ReadTail reads the tags at the end of a file of the given size: APEv2
// (before an ID3v1 tag, if any) and ID3v1. start is where they begin, the
// end of the audio.
func ReadTail(r io.ReadSeeker, size int64) (t Tags, start int64) {
	start = size
	var v1 Tags
	var has1 bool
	if size >= 128 {
		b := make([]byte, 128)
		if _, err := r.Seek(size-128, io.SeekStart); err == nil {
			if _, err := io.ReadFull(r, b); err == nil {
				if v1, has1 = ID3v1(b); has1 {
					start = size - 128
				}
			}
		}
	}
	if ape, at, ok := readAPE(r, start); ok {
		t, start = ape, at
	}
	if has1 {
		t.Merge(v1)
	}
	return t, start
}

const maxAPE = 16 << 20

// readAPE reads an APEv2 (or v1) tag whose footer ends at end.
func readAPE(r io.ReadSeeker, end int64) (Tags, int64, bool) {
	var t Tags
	if end < 32 {
		return t, end, false
	}
	var f [32]byte
	if _, err := r.Seek(end-32, io.SeekStart); err != nil {
		return t, end, false
	}
	if _, err := io.ReadFull(r, f[:]); err != nil || string(f[:8]) != "APETAGEX" {
		return t, end, false
	}
	le := binary.LittleEndian
	size := int64(le.Uint32(f[12:]))
	count := int(le.Uint32(f[16:]))
	flags := le.Uint32(f[20:])
	if size < 32 || size > maxAPE || size > end {
		return t, end, false
	}
	items := make([]byte, size-32)
	if _, err := r.Seek(end-size, io.SeekStart); err != nil {
		return t, end, false
	}
	if _, err := io.ReadFull(r, items); err != nil {
		return t, end, false
	}
	start := end - size
	if flags&(1<<31) != 0 {
		start -= 32
	}
	APE(items, count, &t)
	return t, start, true
}

// APE reads count APEv2 items.
func APE(b []byte, count int, t *Tags) {
	le := binary.LittleEndian
	for i := 0; i < count && len(b) >= 9; i++ {
		n := int(le.Uint32(b))
		flags := le.Uint32(b[4:])
		b = b[8:]
		k := bytes.IndexByte(b, 0)
		if k < 0 || n < 0 || k+1+n > len(b) {
			return
		}
		key, val := string(b[:k]), b[k+1:k+1+n]
		b = b[k+1+n:]
		if flags>>1&3 == 1 {
			if strings.EqualFold(key, "Cover Art (Front)") || strings.HasPrefix(strings.ToLower(key), "cover art") && t.Cover == nil {
				if j := bytes.IndexByte(val, 0); j >= 0 {
					img := val[j+1:]
					t.Cover = &Cover{MIME: sniffImage(img), Data: img, Size: int64(len(img))}
				}
			}
			continue
		}
		t.Set(key, strings.ReplaceAll(string(val), "\x00", ", "))
	}
}

// sniffImage tells JPEG from PNG by their signatures.
func sniffImage(b []byte) string {
	if bytes.HasPrefix(b, []byte("\x89PNG")) {
		return "image/png"
	}
	return "image/jpeg"
}
