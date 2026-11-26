package hifi

import (
	"bytes"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/kario-os/kario/audio/dsd"
	"github.com/kario-os/kario/audio/dsd/dsdtest"
	"github.com/kario-os/kario/audio/musicfile"
)

// The sample music in audio/testdata (scripts/stage.sh installs it for the
// selftest, named by titles.tsv): a hi-res FLAC, an album as one FLAC with
// a GBK cue sheet, and a DSD64 DSF kept as its header and one period of
// its tone (stage.sh repeats the period: 1-bit samples do not compress).
// KARIO_UPDATE_TESTDATA=1 makes them again with the host's ffmpeg.

const testdata = "../testdata"

// The cue sheet, its text in GBK as Chinese rippers write it.
const albumCue = "REM GENRE \"\u793a\u4f8b\"\r\nREM DATE 2026\r\nPERFORMER \"Kario\"\r\nTITLE \"\u793a\u4f8b\u4e13\u8f91\"\r\n" +
	"FILE \"\u793a\u4f8b\u4e13\u8f91.flac\" WAVE\r\n" +
	"  TRACK 01 AUDIO\r\n    TITLE \"C \u97f3\"\r\n    INDEX 01 00:00:00\r\n" +
	"  TRACK 02 AUDIO\r\n    TITLE \"E \u97f3\"\r\n    INDEX 01 00:01:45\r\n" +
	"  TRACK 03 AUDIO\r\n    TITLE \"G \u97f3\"\r\n    INDEX 01 00:03:15\r\n"

const (
	dsfPeriods = 2048 // stage.sh doubles the period 11 times
	dsfTone    = 441  // a whole number of DSD64 bytes a period (800)
)

func TestUpdateTestdata(t *testing.T) {
	if os.Getenv("KARIO_UPDATE_TESTDATA") == "" {
		t.Skip("KARIO_UPDATE_TESTDATA=1 makes the sample music again")
	}
	run := func(args ...string) {
		if out, err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v %s", err, out)
		}
	}
	// A beep, then digital silence (FLAC stores that in a few bytes).
	run("-f", "lavfi", "-i", "aevalsrc=0.45*sin(2*PI*1000*t)*max(0\\,min(1\\,min(t\\,0.6-t)*40)):s=96000:d=2.5", "-ac", "2",
		"-sample_fmt", "s32", "-bits_per_raw_sample", "24", "-compression_level", "12",
		"-metadata", "title=\u793a\u4f8b\u97f3 \u9ad8\u89e3\u6790", "-metadata", "artist=Kario", filepath.Join(testdata, "hires.flac"))
	tones := "0.4*sin(2*PI*if(lt(t\\,1.6)\\,523.25\\,if(lt(t\\,3.2)\\,659.25\\,783.99))*t)*min(1\\,min(mod(t\\,1.6)\\,1.6-mod(t\\,1.6))*50)"
	run("-f", "lavfi", "-i", "aevalsrc="+tones+":s=44100:d=4.8", "-ac", "2", "-sample_fmt", "s16", "-compression_level", "12",
		"-metadata", "album=\u793a\u4f8b\u4e13\u8f91", "-metadata", "artist=Kario", filepath.Join(testdata, "album.flac"))
	cue, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(albumCue))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(testdata, "album.cue"), cue, 0o644)
	per := dsd.Base / 8 / dsfTone
	warm := dsdtest.Tone(dsd.Base, 1, 5*float64(per*8)/dsd.Base, []float64{dsfTone}, 0.5)
	period := warm[4*per : 5*per]
	file := dsdtest.DSF(dsd.Base, 1, bytes.Repeat(period, dsfPeriods), nil)
	os.WriteFile(filepath.Join(testdata, "tone441.dsf.head"), file[:92], 0o644)
	os.WriteFile(filepath.Join(testdata, "tone441.dsf.period"), file[92:92+per], 0o644)
}

func TestSampleMusic(t *testing.T) {
	dir := t.TempDir()
	head, err1 := os.ReadFile(filepath.Join(testdata, "tone441.dsf.head"))
	period, err2 := os.ReadFile(filepath.Join(testdata, "tone441.dsf.period"))
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	dsf := filepath.Join(dir, "tone.dsf")
	os.WriteFile(dsf, append(head, bytes.Repeat(period, dsfPeriods)...), 0o644)
	i, err := musicfile.Probe(dsf)
	if err != nil || i.Label() != "DSF DSD64" || i.LenMS() != 4643 {
		t.Fatalf("the DSF stage.sh builds: %+v %v", i, err)
	}
	s, err := Open(Spec{Path: dsf}, Options{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pcm := unpackS32(drain(t, s, Negotiate(s.Native(), virtio, 1, DSDPCM), 1))
	s.Close()
	x := make([]float64, 0, 88200)
	for k := len(pcm) / 2; k < len(pcm) && len(x) < cap(x); k++ {
		x = append(x, float64(pcm[k])/(1<<31))
	}
	if f := dominant(x, 176400); math.Abs(f-dsfTone) > 3 {
		t.Fatalf("the DSD sample plays %.1f Hz", f)
	}
	h, err := musicfile.Probe(filepath.Join(testdata, "hires.flac"))
	if err != nil || h.Label() != "FLAC 24/96" || h.Tags.Title != "\u793a\u4f8b\u97f3 \u9ad8\u89e3\u6790" {
		t.Fatalf("hires.flac: %+v %v", h, err)
	}
	ts, err := musicfile.Tracks(filepath.Join(testdata, "album.cue"))
	if err != nil || len(ts) != 3 || ts[1].Title != "E \u97f3" || ts[0].Album != "\u793a\u4f8b\u4e13\u8f91" || ts[0].Frames != 70560 {
		t.Fatalf("album.cue: %+v %v", ts, err)
	}
}
