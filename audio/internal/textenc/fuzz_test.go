package textenc

import (
	"testing"
	"unicode/utf8"
)

// FuzzDecode is the text of a tag or a cue sheet in whatever encoding its
// writer used: it comes out as UTF-8.
func FuzzDecode(f *testing.F) {
	f.Add([]byte("\xc4\xe3\xba\xc3"))
	f.Add([]byte("\xff\xfe\x60\x4f\x7d\x59"))
	f.Add([]byte("\xef\xbb\xbfhello"))
	f.Add([]byte("caf\xe9"))
	f.Fuzz(func(t *testing.T, data []byte) {
		for i, s := range []string{Decode(data), Legacy(data), Charset(data, ShiftJIS), Charset(data, KSC5601), Charset(data, GB2312), Charset(data, 7)} {
			if !utf8.ValidString(s) {
				t.Fatalf("decoder %d: %q from %x is not UTF-8", i, s, data)
			}
		}
	})
}
