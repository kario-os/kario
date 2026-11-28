package dsd_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/audio/dsd"
	"github.com/kario-os/kario/audio/dsd/dsdtest"
)

type frames interface {
	Next() ([]byte, error)
	SeekFrame(int64) error
	Count() int64
}

type stream interface {
	io.Reader
	SeekFrame(int64) error
	Len() int64
}

// play reads a little of what s holds, seeks to its middle and reads again,
// as a player does.
func play(s any) {
	switch s := s.(type) {
	case stream:
		buf := make([]byte, 4096)
		for range 32 {
			if _, err := s.Read(buf); err != nil {
				break
			}
		}
		if s.SeekFrame(s.Len()/2) == nil {
			s.Read(buf)
		}
	case frames:
		for range 32 {
			if _, err := s.Next(); err != nil {
				break
			}
		}
		if s.SeekFrame(s.Count()/2) == nil {
			s.Next()
		}
	}
}

func open(path string) {
	switch filepath.Ext(path) {
	case ".dsf":
		if d, err := dsd.OpenDSF(path); err == nil {
			play(d)
			d.Close()
		}
	case ".dff":
		if d, err := dsd.OpenDFF(path); err == nil {
			play(d)
			d.Close()
		}
		if d, err := dsd.OpenDFFDST(path); err == nil {
			play(d)
			d.Close()
		}
	case ".iso":
		s, err := dsd.OpenSACD(path)
		if err != nil {
			return
		}
		defer s.Close()
		for _, a := range s.Areas {
			for i := range a.Tracks {
				if t, err := s.OpenTrack(a, i+1); err == nil {
					play(t)
				}
			}
		}
	}
}

const sacdAt = 510 * dsd.SectorSize

var kinds = []string{".dsf", ".dff", ".iso"}

// FuzzFile is a DSD file or an SACD image played by audiod.
func FuzzFile(f *testing.F) {
	tone := dsdtest.Tone(dsd.Base, 2, 0.05, []float64{1000}, 0.5)
	f.Add(dsdtest.DSF(dsd.Base, 2, tone, nil), uint8(0))
	f.Add(dsdtest.DFF(dsd.Base, 2, tone, false, false, "Title", "Artist"), uint8(1))
	f.Add(dsdtest.DFF(dsd.Base, 2, tone, true, false, "", ""), uint8(1))
	f.Add(dsdtest.DFF(dsd.Base, 2, tone, true, true, "", ""), uint8(1))
	f.Add(dsdtest.SACD("Album", "Artist", dsdtest.Area{Channels: 2, Tracks: []dsdtest.Track{{Title: "One", Frames: tone}, {Frames: tone}}})[sacdAt:], uint8(2))
	f.Add(dsdtest.SACD("Album", "Artist", dsdtest.Area{Channels: 2, DST: true, Tracks: []dsdtest.Track{{Frames: tone}}})[sacdAt:], uint8(2))
	f.Fuzz(func(t *testing.T, data []byte, k uint8) {
		if len(data) > 1<<17 {
			return
		}
		ext := kinds[int(k)%len(kinds)]
		p := filepath.Join(t.TempDir(), "x"+ext)
		fh, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		at := int64(0)
		if ext == ".iso" {
			at = sacdAt
		}
		_, err = fh.WriteAt(data, at)
		fh.Close()
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			open(p)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("playing the file did not end")
		}
	})
}

func writeTemp(t *testing.T, name string, b []byte) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not end", what)
	}
}

// A chunk inside "DST " whose size is negative ends the walk instead of
// leading it back to where it was.
func TestDSTChunkWalkEnds(t *testing.T) {
	tone := dsdtest.Tone(dsd.Base, 2, 0.05, []float64{1000}, 0.5)
	img := dsdtest.DFF(dsd.Base, 2, tone, true, false, "", "")
	at := bytes.Index(img, []byte("DSTF"))
	copy(img[at:], "XXXX\xff\xff\xff\xff\xff\xff\xff\xf4")
	p := writeTemp(t, "x.dff", img)
	within(t, "reading the frames", func() {
		d, err := dsd.OpenDFFDST(p)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		d.Next()
		d.SeekFrame(d.Count())
	})
}

// Seeking far into a track that claims every sector of the disc ends.
func TestSACDSeekFarEnds(t *testing.T) {
	tone := dsdtest.Tone(dsd.Base, 2, 0.5, []float64{1000}, 0.5)
	img := dsdtest.SACD("Album", "Artist", dsdtest.Area{Channels: 2, Tracks: []dsdtest.Track{{Frames: tone}}})
	trl := bytes.Index(img, []byte("SACDTRL1"))
	copy(img[trl+8+4*255:], "\xff\xff\xff\xff")
	p := writeTemp(t, "x.iso", img)
	within(t, "seeking", func() {
		s, err := dsd.OpenSACD(p)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		tr, err := s.OpenTrack(s.Stereo(), 1)
		if err != nil {
			t.Fatal(err)
		}
		d := tr.(*dsd.SACDDSD)
		d.SeekFrame(d.Len() * 3 / 4)
	})
}
