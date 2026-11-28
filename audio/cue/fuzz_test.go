package cue

import (
	"os"
	"testing"
	"unicode/utf8"
)

// FuzzParse is a cue sheet next to an album, or embedded in its tags.
func FuzzParse(f *testing.F) {
	f.Add([]byte(unescape(album)))
	if b, err := os.ReadFile("../testdata/album.cue"); err == nil {
		f.Add(b)
	}
	f.Add([]byte("FILE \"a.wav\" WAVE\nTRACK 01 AUDIO\nINDEX 01 99:59:74\nTRACK 02 AUDIO\nINDEX 00 00:00:00\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := Parse(data)
		if err != nil {
			return
		}
		for _, tr := range s.Tracks {
			if !utf8.ValidString(tr.Title) || !utf8.ValidString(tr.Performer) {
				t.Fatalf("text that is not UTF-8: %+v", tr)
			}
			if len(tr.Segments) == 0 {
				t.Fatalf("track %d plays nothing", tr.Number)
			}
			tr.Frames()
		}
	})
}
