package hifi

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Monkey's Audio samples in testdata, made by the reference encoder
// from source() (testdata/README.md says how). The tests compare what
// ffmpeg decodes with source(), sample for sample.
type apeSample struct {
	file       string
	bits, rate int
	frames     int
	quiet      [2]int // frames left silent: they only carry the file into its second APE frame
	level      string // mac's compression level
	tags       string // mac's -t
	seeks      []int64
}

var apeSamples = []apeSample{
	// A CD image's format at "extra high" (one APE frame).
	{file: "tone16.ape", bits: 16, rate: 44100, frames: 13230, level: "-c4000",
		tags:  "Title=\u7a7a\u5c71\u65b0\u96e8|Artist=Kario|Album=APE samples|Track=2/9|Year=2026",
		seeks: []int64{150}},
	// At "normal" an APE frame holds 73728 frames: this file has two, and
	// the seeks land just before the boundary and in the second frame.
	{file: "tone24.ape", bits: 24, rate: 192000, frames: 76800, quiet: [2]int{4800, 71040}, level: "-c2000",
		tags:  "Title=APE 24/192|Artist=Kario|Album=APE samples|Track=3/9|Year=2026",
		seeks: []int64{383, 390}},
}

// source is the sample's signal, interleaved, in integers only (the file
// is compared with it on any machine, where floating point may round
// otherwise): on the left a 1 kHz parabola wave at -12 dBFS with a
// full-scale click (the largest and the smallest value), on the right a
// sawtooth at -12 dBFS rising by an odd step (all the low bits change and
// no value comes twice, so a dropped or repeated frame shows).
func (a apeSample) source() []int32 {
	const turn = 1 << 16
	full := int64(1) << (a.bits - 1)
	s := make([]int32, 2*a.frames)
	for i := 0; i < a.frames; i++ {
		if i >= a.quiet[0] && i < a.quiet[1] {
			continue
		}
		ph := int64(i) * 1000 * turn / int64(a.rate) % turn
		p := ph % (turn / 2)
		l := full * p * (turn/2 - p) / (turn / 2 * turn / 2)
		if ph >= turn/2 {
			l = -l
		}
		switch i {
		case 100:
			l = full - 1
		case 101:
			l = -full
		}
		r := int64(7*i)%(full/2) - full/4
		s[2*i], s[2*i+1] = int32(l), int32(r)
	}
	return s
}

// ref is source() as the decoders give it, left-justified in 32 bits.
func (a apeSample) ref() []int32 {
	s := a.source()
	for i := range s {
		s[i] <<= 32 - a.bits
	}
	return s
}

// wav is source() as the WAV file the encoder reads.
func (a apeSample) wav() []byte {
	var b bytes.Buffer
	le := binary.LittleEndian
	bps := a.bits / 8
	data := a.frames * 2 * bps
	b.WriteString("RIFF")
	binary.Write(&b, le, uint32(36+data))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, le, uint32(16))
	binary.Write(&b, le, uint16(1))
	binary.Write(&b, le, uint16(2))
	binary.Write(&b, le, uint32(a.rate))
	binary.Write(&b, le, uint32(a.rate*2*bps))
	binary.Write(&b, le, uint16(2*bps))
	binary.Write(&b, le, uint16(a.bits))
	b.WriteString("data")
	binary.Write(&b, le, uint32(data))
	for _, v := range a.source() {
		for k := 0; k < bps; k++ {
			b.WriteByte(byte(v >> (8 * k)))
		}
	}
	return b.Bytes()
}

func TestUpdateAPESamples(t *testing.T) {
	mac := os.Getenv("MAC")
	if os.Getenv("KARIO_UPDATE_TESTDATA") == "" || mac == "" {
		t.Skip("KARIO_UPDATE_TESTDATA=1 MAC=<Monkey's Audio's mac> makes the APE samples again (testdata/README.md)")
	}
	for _, a := range apeSamples {
		src := filepath.Join(t.TempDir(), "src.wav")
		if err := os.WriteFile(src, a.wav(), 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join("testdata", a.file)
		os.Remove(out)
		if b, err := exec.Command(mac, src, out, a.level, "-t", a.tags).CombinedOutput(); err != nil {
			t.Fatalf("mac: %v %s", err, b)
		}
	}
}

// needAPE skips unless ffmpeg has Monkey's Audio's demuxer and decoder.
func needAPE(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(FFmpeg); err != nil {
		t.Skip("no ffmpeg")
	}
	for _, kind := range []string{"-demuxers", "-decoders"} {
		out, err := exec.Command(FFmpeg, "-hide_banner", kind).Output()
		if err != nil {
			t.Skipf("ffmpeg %s: %v", kind, err)
		}
		found := false
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[1] == "ape" {
				found = true
			}
		}
		if !found {
			t.Skipf("this ffmpeg has no APE in %s", kind)
		}
	}
}

// s32Card takes S32_LE only: 16-bit samples reach it whole, in the high
// bits, so all 32 bits of each decoded sample are compared.
var s32Card = Caps{Alts: []Alt{{Formats: []string{"S32_LE"}, MinCh: 2, MaxCh: 2, MinRate: 8000, MaxRate: 384000}}}

func sameSamples(t *testing.T, what string, got, want []int32, frame0 int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d samples, want %d", what, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: frame %d channel %d is %#x, want %#x", what, frame0+i/2, i%2, got[i], want[i])
		}
	}
}

func TestAPEDecode(t *testing.T) {
	needAPE(t)
	for _, a := range apeSamples {
		t.Run(a.file, func(t *testing.T) {
			ref := a.ref()
			s, err := Open(Spec{Path: filepath.Join("testdata", a.file)}, Options{}, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if f := s.Native(); f.Rate != a.rate || f.Bits != a.bits || f.Channels != 2 || f.Float {
				t.Fatalf("format %v", f)
			}
			plan := Negotiate(s.Native(), s32Card, 1, "")
			if plan.Format != "S32_LE" || !plan.BitPerfect {
				t.Fatalf("plan %v", plan)
			}
			sameSamples(t, "from the start", unpackS32(drain(t, s, plan, 1)), ref, 0)
			for _, ms := range a.seeks {
				if err := s.SeekMS(ms); err != nil {
					t.Fatal(err)
				}
				at := int(ms) * a.rate / 1000
				n := min(2000, a.frames-at)
				buf := make([]byte, n*plan.BytesPerFrame())
				k, _, err := s.Out(plan, 1, n, buf)
				if err != nil {
					t.Fatal(err)
				}
				sameSamples(t, fmt.Sprintf("after a seek to %d ms", ms), unpackS32(buf[:k]), ref[2*at:2*(at+n)], at)
			}
		})
	}
}

// countFFmpeg points FFmpeg at a script that counts its runs.
func countFFmpeg(t *testing.T) func() int {
	t.Helper()
	real, err := exec.LookPath(FFmpeg)
	if err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	script := fmt.Sprintf("#!/bin/sh\necho >> '%s'\nexec '%s' \"$@\"\n", runs, real)
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := FFmpeg
	FFmpeg = filepath.Join(dir, "ffmpeg")
	t.Cleanup(func() { FFmpeg = old })
	return func() int {
		b, _ := os.ReadFile(runs)
		return bytes.Count(b, []byte("\n"))
	}
}

// The 16-bit sample as a CD image with a cue sheet: three tracks in a row
// are the image, and one ffmpeg decodes them all.
func TestAPECueIsGapless(t *testing.T) {
	needAPE(t)
	a := apeSamples[0]
	ref := a.ref()
	dir := t.TempDir()
	img, err := os.ReadFile(filepath.Join("testdata", a.file))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "image.ape"), img, 0o644)
	// Track 2's pregap (INDEX 00) belongs to track 1; the starts are CD
	// frames (588 frames of 44.1 kHz), not whole microseconds.
	sheet := "FILE \"image.ape\" WAVE\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n  TRACK 02 AUDIO\n    INDEX 00 00:00:06\n    INDEX 01 00:00:08\n  TRACK 03 AUDIO\n    INDEX 01 00:00:15\n"
	cue := filepath.Join(dir, "image.cue")
	os.WriteFile(cue, []byte(sheet), 0o644)
	specs := []Spec{{Path: cue, Track: 1, Media: "1"}, {Path: cue, Track: 2, Media: "2"}, {Path: cue, Track: 3, Media: "3"}}
	runs := countFFmpeg(t)
	s, err := Open(specs[0], Options{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Queue(&specs[1])
	time.Sleep(50 * time.Millisecond)
	s.mu.Lock()
	if s.next == nil && s.pending != nil {
		s.next = <-s.pending
		s.pending = nil
	}
	lazy := s.next != nil && s.next.pcm == nil
	s.mu.Unlock()
	if !lazy {
		t.Fatal("the next track of the image should wait for the current decoder")
	}
	outs, plans := play(t, s, s32Card, specs)
	if len(plans) != 1 || !plans[0].BitPerfect {
		t.Fatalf("plans %v", plans)
	}
	sameSamples(t, "the three tracks", unpackS32(outs[0]), ref, 0)
	if media, _, _ := s.Now(); media != "3" {
		t.Fatalf("ends on %q", media)
	}
	if n := runs(); n != 1 {
		t.Fatalf("ffmpeg ran %d times for the album, want once", n)
	}

	// Track 2 on its own: ffmpeg starts at frame 4704 (0.106667 s).
	s2, err := Open(specs[1], Options{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	plan := Negotiate(s2.Native(), s32Card, 1, "")
	sameSamples(t, "track 2", unpackS32(drain(t, s2, plan, 1)), ref[2*8*588:2*15*588], 8*588)
}
