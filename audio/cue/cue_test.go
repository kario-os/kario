package cue

import (
	"os"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const album = `REM GENRE "Chinese Pop"
REM DATE 1987
REM REPLAYGAIN_ALBUM_GAIN -7.25 dB
REM REPLAYGAIN_ALBUM_PEAK 0.977
PERFORMER "\u9093\u4e3d\u541b"
TITLE "\u6de1\u6de1\u5e7d\u60c5"
FILE "\u6de1\u6de1\u5e7d\u60c5.ape" WAVE
  TRACK 01 AUDIO
    TITLE "\u72ec\u4e0a\u897f\u697c"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "\u51e0\u591a\u6101"
    PERFORMER "\u9093\u4e3d\u541b & \u5408\u5531"
    REM REPLAYGAIN_TRACK_GAIN -6.10 dB
    INDEX 00 03:10:20
    INDEX 01 03:12:00
  TRACK 03 AUDIO
    TITLE "\u4eba\u751f\u81ea\u662f\u6709\u60c5\u75f4"
    INDEX 01 07:01:74
`

// unescape turns the \uXXXX in a raw string into characters (test code
// spells non-ASCII text that way).
func unescape(s string) string {
	return regexp.MustCompile(`\\u[0-9a-f]{4}`).ReplaceAllStringFunc(s, func(m string) string {
		r, _ := strconv.ParseUint(m[2:], 16, 32)
		return string(rune(r))
	})
}

func TestGBKAlbum(t *testing.T) {
	text := unescape(album)
	for name, raw := range map[string][]byte{
		"utf-8":     []byte(text),
		"utf-8 BOM": append([]byte{0xef, 0xbb, 0xbf}, text...),
		"gbk":       mustGBK(t, text),
		"utf-16":    utf16(text),
	} {
		s, err := Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.Title != "\u6de1\u6de1\u5e7d\u60c5" || s.Performer != "\u9093\u4e3d\u541b" || s.Genre != "Chinese Pop" || s.Date != "1987" ||
			!s.HasAlbumGain || s.AlbumGain != -7.25 || s.AlbumPeak != 0.977 {
			t.Fatalf("%s: album %+v", name, s)
		}
		if len(s.Tracks) != 3 || s.Tracks[1].Title != "\u51e0\u591a\u6101" || s.Tracks[1].Performer != "\u9093\u4e3d\u541b & \u5408\u5531" ||
			!s.Tracks[1].HasTrackGain || s.Tracks[1].TrackGain != -6.1 {
			t.Fatalf("%s: tracks %+v", name, s.Tracks)
		}
		f := "\u6de1\u6de1\u5e7d\u60c5.ape"
		want := [][]Segment{
			{{f, 0, (3*60 + 12) * 75}},
			{{f, (3*60 + 12) * 75, (7*60+1)*75 + 74}},
			{{f, (7*60+1)*75 + 74, -1}},
		}
		for i, w := range want {
			if !reflect.DeepEqual(s.Tracks[i].Segments, w) {
				t.Fatalf("%s: track %d plays %+v, want %+v", name, i+1, s.Tracks[i].Segments, w)
			}
		}
		if s.Tracks[1].Pregap != (3*60+10)*75+20 {
			t.Fatalf("pregap %d", s.Tracks[1].Pregap)
		}
	}
}

func TestMultiFile(t *testing.T) {
	// Track 2's pregap at the start of its own file (INDEX 00 at 0) plays
	// after track 1; track 3's pregap is written at the end of file 2
	// (the "noncompliant" layout EAC also makes).
	s, err := Parse([]byte(`FILE "01.flac" WAVE
  TRACK 01 AUDIO
    INDEX 01 00:00:00
FILE "02.flac" WAVE
  TRACK 02 AUDIO
    INDEX 00 00:00:00
    INDEX 01 00:02:10
  TRACK 03 AUDIO
    INDEX 00 04:00:00
FILE "03.flac" WAVE
    INDEX 01 00:00:00
`))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]Segment{
		{{"01.flac", 0, -1}, {"02.flac", 0, 160}},
		{{"02.flac", 160, -1}},
		{{"03.flac", 0, -1}},
	}
	for i, w := range want {
		if !reflect.DeepEqual(s.Tracks[i].Segments, w) {
			t.Fatalf("track %d plays %+v, want %+v", i+1, s.Tracks[i].Segments, w)
		}
	}
	if s.Tracks[2].File != "03.flac" || s.Tracks[2].PregapFile != "02.flac" {
		t.Fatalf("track 3: %+v", s.Tracks[2])
	}
}

func TestBadSheets(t *testing.T) {
	for _, raw := range []string{"", "FILE \"a.wav\" WAVE\n", "FILE \"a.wav\" WAVE\nTRACK 01 AUDIO\nINDEX 01 00:61:00\n", "TRACK x AUDIO\n"} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%q parsed", raw)
		}
	}
	s, err := Parse([]byte("FILE a.bin BINARY\nTRACK 01 MODE1/2352\nINDEX 01 00:00:00\nTRACK 02 AUDIO\nINDEX 01 01:00:00\n"))
	if err != nil || len(s.Tracks) != 1 || s.Tracks[0].Number != 2 {
		t.Fatalf("data track not skipped: %+v %v", s, err)
	}
}

func TestTestdataSheet(t *testing.T) {
	raw, err := os.ReadFile("../testdata/album.cue")
	if err != nil {
		t.Skip(err)
	}
	if _, err := Parse(raw); err != nil {
		t.Fatal(err)
	}
}

func mustGBK(t *testing.T, s string) []byte {
	b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func utf16(s string) []byte {
	out := []byte{0xff, 0xfe}
	for _, r := range s {
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}
