package flac

import (
	"bytes"
	"os"
	"testing"
)

// FuzzDecoder is a FLAC file played (audiod) or listed (the Shell reads
// its metadata): decoding and seeking end in samples or an error.
func FuzzDecoder(f *testing.F) {
	for _, name := range []string{"album.flac", "hires.flac"} {
		b, _ := os.ReadFile("../testdata/" + name)
		f.Add(b[:min(len(b), 12<<10)], int64(100))
	}
	f.Fuzz(func(t *testing.T, data []byte, seek int64) {
		if len(data) > 1<<17 {
			return
		}
		d, err := Open(bytes.NewReader(data))
		if err != nil {
			return
		}
		buf := make([]int32, 4096*d.Channels())
		for range 64 {
			if _, err := d.Read(buf); err != nil {
				break
			}
		}
		if d.SeekSample(seek) == nil {
			d.Read(buf)
		}
	})
}
