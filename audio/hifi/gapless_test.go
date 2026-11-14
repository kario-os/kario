package hifi

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/audio/dsd"
	"github.com/kario-os/kario/audio/dsd/dsdtest"
)

// play runs a queue through Out the way phone-audiod does: queueing the
// following spec each time the stream moves on, renegotiating when the
// format changes. It returns the bytes of each plan in turn.
func play(t *testing.T, s *Stream, caps Caps, specs []Spec) ([][]byte, []Plan) {
	t.Helper()
	next := 1
	if next < len(specs) {
		s.Queue(&specs[next])
	}
	var outs [][]byte
	var plans []Plan
	plan := Negotiate(s.Native(), caps, 1, DSDAuto)
	plans = append(plans, plan)
	outs = append(outs, nil)
	buf := make([]byte, 1<<16)
	for i := 0; i < 1000000; i++ {
		n, more, err := s.Out(plan, 1, plan.Rate/50, buf)
		if errors.Is(err, ErrFormatChanged) {
			plan = Negotiate(s.Native(), caps, 1, DSDAuto)
			plans = append(plans, plan)
			outs = append(outs, nil)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		outs[len(outs)-1] = append(outs[len(outs)-1], buf[:n]...)
		if s.Changed() {
			next++
			if next < len(specs) {
				s.Queue(&specs[next])
			}
		}
		if !more {
			return outs, plans
		}
	}
	t.Fatal("no end")
	return nil, nil
}

func TestCueAlbumIsGapless(t *testing.T) {
	dir := t.TempDir()
	ref := wav24(t, filepath.Join(dir, "image.wav"), 44100, 3*44100+123)
	sheet := "FILE \"image.wav\" WAVE\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n  TRACK 02 AUDIO\n    INDEX 00 00:00:70\n    INDEX 01 00:01:00\n  TRACK 03 AUDIO\n    INDEX 01 00:02:10\n"
	cue := filepath.Join(dir, "image.cue")
	os.WriteFile(cue, []byte(sheet), 0o644)
	specs := []Spec{{Path: cue, Track: 1, Media: "1"}, {Path: cue, Track: 2, Media: "2"}, {Path: cue, Track: 3, Media: "3"}}
	s, err := Open(specs[0], Options{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := s.cur.pcm
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
	outs, plans := play(t, s, virtio, specs)
	if len(plans) != 1 || !plans[0].BitPerfect {
		t.Fatalf("plans %v", plans)
	}
	got := unpackS32(outs[0])
	if len(got) != len(ref) {
		t.Fatalf("%d samples for the album, the image has %d", len(got), len(ref))
	}
	for i := range ref {
		if got[i] != ref[i] {
			t.Fatalf("sample %d (%.3f s) is %#x, want %#x: a gap or an overlap between tracks", i, float64(i/2)/44100, got[i], ref[i])
		}
	}
	if media, _, _ := s.Now(); media != "3" {
		t.Fatalf("ends on %q", media)
	}
	if s.cur.pcm != first && s.cur.pcm != nil {
		t.Fatal("the decoder was not handed on from track to track")
	}
}

func TestCueTrackSeekAndLength(t *testing.T) {
	dir := t.TempDir()
	ref := wav24(t, filepath.Join(dir, "image.wav"), 44100, 3*44100)
	os.WriteFile(filepath.Join(dir, "image.cue"), []byte("FILE \"image.wav\" WAVE\nTRACK 01 AUDIO\nINDEX 01 00:00:00\nTRACK 02 AUDIO\nINDEX 01 00:01:00\n"), 0o644)
	s, err := Open(Spec{Path: filepath.Join(dir, "image.cue"), Track: 2}, Options{}, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Len() != 2*48000 || s.Pos() != 24000 {
		t.Fatalf("track 2: len %d pos %d", s.Len(), s.Pos())
	}
	plan := Negotiate(s.Native(), virtio, 1, "")
	got := unpackS32(drain(t, s, plan, 1))
	start := (44100 + 22050) * 2
	if len(got) != len(ref)-start {
		t.Fatalf("%d samples from 0:00.5 of track 2, want %d", len(got), len(ref)-start)
	}
	for i := range got {
		if got[i] != ref[start+i] {
			t.Fatalf("sample %d", i)
		}
	}
}

func TestFormatChangeBetweenTracks(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.wav"), filepath.Join(dir, "b.wav")
	ra := wav24(t, a, 44100, 10000)
	rb := wav24(t, b, 96000, 20000)
	s, err := Open(Spec{Path: a, Media: "a"}, Options{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	outs, plans := play(t, s, virtio, []Spec{{Path: a}, {Path: b, Media: "b"}})
	if len(plans) != 2 || plans[0].Rate != 44100 || plans[1].Rate != 96000 {
		t.Fatalf("plans %v", plans)
	}
	if len(unpackS32(outs[0])) != len(ra) || len(unpackS32(outs[1])) != len(rb) {
		t.Fatalf("%d and %d samples, want %d and %d", len(outs[0])/4, len(outs[1])/4, len(ra), len(rb))
	}
}

func TestSACDQueue(t *testing.T) {
	t1 := dsdtest.Tone(dsd.Base, 2, 0.3, []float64{500}, 0.5)
	t2 := dsdtest.Tone(dsd.Base, 2, 0.2, []float64{700}, 0.5)
	p := filepath.Join(t.TempDir(), "disc.iso")
	os.WriteFile(p, dsdtest.SACD("Disc", "Band", dsdtest.Area{Channels: 2, Tracks: []dsdtest.Track{{Title: "1", Frames: t1}, {Title: "2", Frames: t2}}}), 0o644)
	specs := []Spec{{Path: p, Track: 1, Media: "1"}, {Path: p, Track: 2, Media: "2"}}
	s, err := Open(specs[0], Options{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	usb := ParseUSBStream(usbStream)
	outs, plans := play(t, s, usb, specs)
	if len(plans) != 1 || plans[0].Mode != ModeDSD {
		t.Fatalf("plans %v", plans)
	}
	per := dsd.FrameBytes * 2
	n1, n2 := (len(t1)+per-1)/per*per, (len(t2)+per-1)/per*per
	if len(outs[0]) != (n1+n2)/8*8 {
		t.Fatalf("%d bytes, want %d", len(outs[0]), n1+n2)
	}
	// DSD_U32_BE: 4 bytes of the left channel, then 4 of the right.
	if !bytes.Equal(outs[0][:4], []byte{t1[0], t1[2], t1[4], t1[6]}) {
		t.Fatal("first bytes")
	}
}

func TestDSTAreaPlaysUncodedFramesNatively(t *testing.T) {
	tone := dsdtest.Tone(dsd.Base, 2, 0.2, []float64{500}, 0.5)
	p := filepath.Join(t.TempDir(), "dst.iso")
	os.WriteFile(p, dsdtest.SACD("Disc", "", dsdtest.Area{Channels: 2, DST: true, Tracks: []dsdtest.Track{{Frames: tone}}}), 0o644)
	s, err := Open(Spec{Path: p, Track: 1}, Options{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if f := s.Native(); !f.DSD || f.Rate != dsd.Base {
		t.Fatalf("format %v", f)
	}
	out := drain(t, s, Negotiate(s.Native(), ParseUSBStream(usbStream), 1, DSDAuto), 1)
	if len(out) != (len(tone)+dsd.FrameBytes*2-1)/(dsd.FrameBytes*2)*dsd.FrameBytes*2 {
		t.Fatalf("%d bytes", len(out))
	}
}
