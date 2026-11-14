package musicfile

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/audio/dsd"
	"github.com/kario-os/kario/audio/dsd/dsdtest"
)

func ffmpeg(t *testing.T, out string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	all := append([]string{"-v", "error", "-y"}, args...)
	if b, err := exec.Command("ffmpeg", append(all, out)...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot make %s here: %v %s", filepath.Base(out), err, b)
	}
}

const src = "aevalsrc=0.5*sin(440*2*PI*t)|0.5*sin(660*2*PI*t):d=1.5"

func TestProbeFormats(t *testing.T) {
	dir := t.TempDir()
	meta := []string{"-metadata", "title=\u7a7a\u5c71\u65b0\u96e8", "-metadata", "artist=Kario", "-metadata", "album=Tests", "-metadata", "track=3/9"}
	cases := []struct {
		file      string
		args      []string
		container string
		codec     string
		rate      int
		bits      int
		float     bool
		tags      bool
	}{
		{"a.flac", []string{"-ar", "96000", "-sample_fmt", "s32", "-bits_per_raw_sample", "24"}, "FLAC", FLAC, 96000, 24, false, true},
		{"b.wav", []string{"-ar", "44100", "-c:a", "pcm_s16le"}, "WAV", PCM, 44100, 16, false, false},
		{"c.wav", []string{"-ar", "192000", "-c:a", "pcm_s24le"}, "WAV", PCM, 192000, 24, false, false},
		{"d.wav", []string{"-ar", "48000", "-c:a", "pcm_s32le"}, "WAV", PCM, 48000, 32, false, false},
		{"e.wav", []string{"-ar", "88200", "-c:a", "pcm_f32le"}, "WAV", PCM, 88200, 32, true, false},
		{"f.aiff", []string{"-ar", "44100", "-c:a", "pcm_s24be", "-write_id3v2", "1"}, "AIFF", PCM, 44100, 24, false, true},
		{"g.m4a", []string{"-ar", "96000", "-c:a", "alac", "-sample_fmt", "s32p"}, "M4A", ALAC, 96000, 24, false, true},
		{"h.wv", []string{"-ar", "48000", "-c:a", "wavpack", "-sample_fmt", "s16p"}, "WV", WavPack, 48000, 16, false, true},
		{"i.mp3", []string{"-ar", "44100", "-b:a", "128k"}, "MP3", MP3, 44100, 16, false, true},
		{"j.ogg", []string{"-ar", "44100", "-c:a", "libvorbis"}, "Ogg", Vorbis, 44100, 16, false, true},
		{"k.opus", []string{"-c:a", "libopus"}, "Opus", Opus, 48000, 16, false, true},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			p := filepath.Join(dir, c.file)
			ffmpeg(t, p, append(append([]string{"-f", "lavfi", "-i", src}, c.args...), meta...)...)
			i, err := Probe(p)
			if err != nil {
				t.Fatal(err)
			}
			if i.Container != c.container || i.Codec != c.codec || i.Rate != c.rate || i.Bits != c.bits || i.Float != c.float || i.Channels != 2 {
				t.Fatalf("%+v", i)
			}
			// Lossy codecs pad their last frame; lossless ones are exact.
			want := int64(1.5 * float64(c.rate))
			slack := int64(0)
			if c.codec == MP3 || c.codec == Vorbis || c.codec == Opus {
				slack = int64(c.rate / 20)
			}
			if d := i.Frames - want; d < -slack || d > slack {
				t.Fatalf("%d frames, want %d", i.Frames, want)
			}
			if c.tags && (i.Tags.Title != "\u7a7a\u5c71\u65b0\u96e8" || i.Tags.Artist != "Kario" || i.Tags.Album != "Tests" || i.Tags.Track != 3) {
				t.Fatalf("tags %+v", i.Tags)
			}
		})
	}
}

func TestLabels(t *testing.T) {
	for want, i := range map[string]Info{
		"FLAC 24/96":    {Codec: FLAC, Rate: 96000, Bits: 24},
		"FLAC 16/44.1":  {Codec: FLAC, Rate: 44100, Bits: 16},
		"WAV 32f/88.2":  {Container: "WAV", Codec: PCM, Rate: 88200, Bits: 32, Float: true},
		"DSF DSD64":     {Container: "DSF", Codec: DSD, Rate: dsd.Base, Bits: 1},
		"DFF DSD128":    {Container: "DFF", Codec: DSD, Rate: 2 * dsd.Base, Bits: 1},
		"SACD DSD64":    {Container: "SACD", Codec: DST, Rate: dsd.Base, Bits: 1},
		"MP3 44.1 kHz":  {Codec: MP3, Rate: 44100, Bits: 16},
		"ALAC 24/192":   {Codec: ALAC, Rate: 192000, Bits: 24},
		"DFF DST DSD64": {Container: "DFF", Codec: DST, Rate: dsd.Base, Bits: 1},
	} {
		if got := i.Label(); got != want {
			t.Errorf("%+v: %q, want %q", i, got, want)
		}
	}
}

func TestAPEHeader(t *testing.T) {
	// A 3.99 descriptor and header (no encoder to make real APE files
	// here: decoding is ffmpeg's, the header is ours).
	le := binary.LittleEndian
	b := make([]byte, 52+24)
	copy(b, "MAC ")
	le.PutUint16(b[4:], 3990)
	le.PutUint32(b[8:], 52)
	le.PutUint32(b[12:], 24)
	h := b[52:]
	le.PutUint16(h[0:], 2000)
	le.PutUint32(h[4:], 73728)
	le.PutUint32(h[8:], 1000)
	le.PutUint32(h[12:], 11)
	le.PutUint16(h[16:], 24)
	le.PutUint16(h[18:], 2)
	le.PutUint32(h[20:], 96000)
	p := filepath.Join(t.TempDir(), "x.ape")
	os.WriteFile(p, append(b, make([]byte, 100)...), 0o644)
	i, err := Probe(p)
	if err != nil {
		t.Fatal(err)
	}
	if i.Codec != APE || i.Rate != 96000 || i.Bits != 24 || i.Channels != 2 || i.Frames != 10*73728+1000 {
		t.Fatalf("%+v", i)
	}
}

func TestDSDFiles(t *testing.T) {
	dir := t.TempDir()
	frames := dsdtest.Tone(dsd.Base, 2, 0.1, []float64{1000}, 0.5)
	id3 := []byte("ID3\x03\x00\x00\x00\x00\x00\x12TIT2\x00\x00\x00\x08\x00\x00\x00DSD one")
	os.WriteFile(filepath.Join(dir, "a.dsf"), dsdtest.DSF(dsd.Base, 2, frames, id3), 0o644)
	os.WriteFile(filepath.Join(dir, "b.dff"), dsdtest.DFF(2*dsd.Base, 2, frames, false, false, "DFF title", "Who"), 0o644)
	os.WriteFile(filepath.Join(dir, "c.dff"), dsdtest.DFF(dsd.Base, 2, frames, true, true, "", ""), 0o644)
	i, err := Probe(filepath.Join(dir, "a.dsf"))
	if err != nil || i.Codec != DSD || i.Frames != int64(len(frames)/2*8) || i.Tags.Title != "DSD one" || i.Label() != "DSF DSD64" {
		t.Fatalf("DSF %+v %v", i, err)
	}
	i, err = Probe(filepath.Join(dir, "b.dff"))
	if err != nil || i.Rate != 2*dsd.Base || i.Tags.Title != "DFF title" || i.Tags.Artist != "Who" {
		t.Fatalf("DFF %+v %v", i, err)
	}
	i, err = Probe(filepath.Join(dir, "c.dff"))
	if err != nil || i.Codec != DST || i.LenMS() < 100 || i.LenMS() > 120 {
		t.Fatalf("DST DFF %+v %v", i, err)
	}
}

func TestCueAlbum(t *testing.T) {
	dir := t.TempDir()
	album := filepath.Join(dir, "Album.flac")
	ffmpeg(t, album, "-f", "lavfi", "-i", "sine=f=500:d=9", "-ar", "44100", "-metadata", "album=From tags")
	sheet := "\ufeffPERFORMER \"\u97e9\u7ea2\"\r\nTITLE \"\u5929\u8def\"\r\nREM DATE 2005\r\nFILE \"CDImage.wav\" WAVE\r\n" +
		"  TRACK 01 AUDIO\r\n    TITLE \"\u4e00\"\r\n    INDEX 01 00:00:00\r\n" +
		"  TRACK 02 AUDIO\r\n    TITLE \"\u4e8c\"\r\n    INDEX 00 00:02:50\r\n    INDEX 01 00:03:00\r\n" +
		"  TRACK 03 AUDIO\r\n    INDEX 01 00:07:37\r\n"
	cuePath := filepath.Join(dir, "Album.cue")
	os.WriteFile(cuePath, []byte(sheet), 0o644)
	ts, err := Tracks(cuePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 3 || ts[0].Title != "\u4e00" || ts[0].Artist != "\u97e9\u7ea2" || ts[0].Album != "\u5929\u8def" || ts[1].Number != 2 || ts[2].Title != "\u7b2c 03 \u9996" && ts[2].Title != "Track 03" {
		t.Fatalf("%+v", ts)
	}
	if ts[0].Frames != 3*44100 || ts[1].Frames != 4*44100+37*588 || ts[0].Frames+ts[1].Frames+ts[2].Frames != 9*44100 {
		t.Fatalf("lengths %d %d %d", ts[0].Frames, ts[1].Frames, ts[2].Frames)
	}
	if ts[1].Segments[0].Start != 3*44100 || ts[1].Segments[0].Path != album || len(ts[0].Uses) != 1 || ts[0].Uses[0] != album {
		t.Fatalf("segments %+v uses %v", ts[1].Segments, ts[0].Uses)
	}
	if ts[0].Info.Label() != "FLAC 16/44.1" {
		t.Fatal(ts[0].Info.Label())
	}
	os.WriteFile(filepath.Join(dir, "evil.cue"), []byte("FILE \"../../etc/passwd\" WAVE\nTRACK 01 AUDIO\nINDEX 01 00:00:00\n"), 0o644)
	if _, err := Tracks(filepath.Join(dir, "evil.cue")); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("a sheet naming a file outside its folder: %v", err)
	}
}

func TestSACDTracks(t *testing.T) {
	p := filepath.Join(t.TempDir(), "disc.iso")
	tone := dsdtest.Tone(dsd.Base, 2, 0.2, []float64{500}, 0.5)
	os.WriteFile(p, dsdtest.SACD("Disc", "Band", dsdtest.Area{Channels: 2, Tracks: []dsdtest.Track{{Title: "One", Frames: tone}, {Frames: tone}}}), 0o644)
	ts, err := Tracks(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 || ts[0].Title != "One" || ts[0].Album != "Disc" || ts[0].Artist != "Band" || ts[1].Index != 2 || ts[0].Area != AreaStereo ||
		ts[0].Info.Label() != "SACD DSD64" || ts[0].LenMS() != 200 {
		t.Fatalf("%+v", ts)
	}
	mc := filepath.Join(t.TempDir(), "mc.iso")
	os.WriteFile(mc, dsdtest.SACD("MC", "", dsdtest.Area{Channels: 6, Speakers: 4, DST: true, Tracks: []dsdtest.Track{{Title: "Surround", Frames: dsdtest.Tone(dsd.Base, 6, 0.1, []float64{500}, 0.4)}}}), 0o644)
	ts, err = Tracks(mc)
	if err != nil || len(ts) != 1 || ts[0].Area != AreaMulti || ts[0].Info.Codec != DST || ts[0].Info.Channels != 6 {
		t.Fatalf("multichannel only: %+v %v", ts, err)
	}
}

func TestTestdata(t *testing.T) {
	for _, name := range []string{"tone440.mp3", "tone520.opus", "tone660.ogg"} {
		i, err := Probe(filepath.Join("..", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if i.Frames == 0 || i.Channels == 0 {
			t.Fatalf("%s: %+v", name, i)
		}
	}
	if _, err := Probe(filepath.Join("..", "testdata", "mpeg2-22khz.mp3")); err == nil {
		t.Fatal("MPEG-2 MP3 must be refused")
	}
}
