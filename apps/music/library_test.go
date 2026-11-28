package main

import (
	"reflect"
	"testing"

	"github.com/kario-os/kario/ui"
)

func tr2(id, title, album, artist, folder string, n int, q string) track {
	return track{MediaItem: ui.MediaItem{ID: id, Title: title, Artist: artist, Album: album, AlbumArtist: artist, Folder: folder,
		Number: n, Quality: q, Lossless: true, Rate: 44100, Bits: 16}}
}

func TestGrouping(t *testing.T) {
	lib := []track{
		tr2("music:x/2.flac", "Two", "X", "Band", "Band/X", 2, "16/44.1"),
		tr2("music:x/1.flac", "One", "X", "Band", "Band/X", 1, "16/44.1"),
		tr2("music:y.iso#1", "S", "Y", "Band", "Band", 1, "DSD64"),
		tr2("music:z.flac", "Z", "", "", "", 0, "24/96"),
		{MediaItem: ui.MediaItem{ID: "demo:dawn", Title: "Dawn"}},
	}
	lib[2].From = "sacd"
	lib[3].Rate, lib[3].Bits = 96000, 24
	as := albums(lib)
	if len(as) != 3 || as[0].title != "X" || !reflect.DeepEqual(as[0].tracks, []int{1, 0}) {
		t.Fatalf("albums %+v", as)
	}
	if q := quality(lib, as[1].tracks); q != "SACD" {
		t.Fatalf("SACD album shows %q", q)
	}
	rs := artists(lib, as)
	if len(rs) != 2 || rs[0].name != "Band" || rs[0].albums != 2 || len(rs[0].tracks) != 3 {
		t.Fatalf("artists %+v", rs)
	}
	subs, ts := folder(lib, "")
	if !reflect.DeepEqual(subs, []string{"Band"}) || !reflect.DeepEqual(ts, []int{3}) {
		t.Fatalf("top folder %v %v", subs, ts)
	}
	subs, ts = folder(lib, "Band")
	if !reflect.DeepEqual(subs, []string{"Band/X"}) || !reflect.DeepEqual(ts, []int{2}) {
		t.Fatalf("Band %v %v", subs, ts)
	}
}

func TestOutputLabel(t *testing.T) {
	flac := track{ui.MediaItem{Codec: "FLAC"}}
	dsf := track{ui.MediaItem{Codec: "DSD"}}
	for _, c := range []struct {
		out, want string
		t         track
	}{
		{"", "", flac},
		{"mixer 48 kHz", tr.T("Mixed with system sounds at 48 kHz"), flac},
		{"pcm S32_LE 96000 Hz 2ch bit-perfect", tr.T("Bit-perfect at %s", "96 kHz"), flac},
		{"pcm S32_LE 96000 Hz 2ch", tr.T("At %s, with digital volume", "96 kHz"), flac},
		{"dop S24_3LE 176400 Hz 2ch bit-perfect", tr.T("DoP (DSD over PCM) to the sound card"), dsf},
		{"dsd DSD_U32_BE 88200 Hz 2ch bit-perfect", tr.T("Native DSD to the sound card"), dsf},
		{"pcm S32_LE 176400 Hz 2ch", tr.T("DSD converted to PCM at %s", "176.4 kHz"), dsf},
	} {
		if got := outputLabel(c.out, c.t); got != c.want {
			t.Errorf("%q: %q, want %q", c.out, got, c.want)
		}
	}
}
