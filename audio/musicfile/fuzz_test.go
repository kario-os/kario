package musicfile

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/audio/dsd"
	"github.com/kario-os/kario/audio/dsd/dsdtest"
)

// probeExts are the kinds of file a fuzzed one is named as (its second
// argument); an SACD image's bytes start at its master TOC.
var probeExts = []string{".flac", ".wav", ".aiff", ".m4a", ".ape", ".wv", ".mp3", ".ogg", ".dsf", ".dff", ".cue", ".iso"}

const sacdAt = 510 * dsd.SectorSize

func kind(ext string) uint8 {
	for i, e := range probeExts {
		if e == ext {
			return uint8(i)
		}
	}
	panic(ext)
}

func head(path string, n int) []byte {
	b, _ := os.ReadFile(path)
	return b[:min(n, len(b))]
}

func wavSeed() []byte {
	b := []byte("RIFF\x00\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x02\x00\x44\xac\x00\x00\x10\xb1\x02\x00\x04\x00\x10\x00")
	b = append(b, "LIST\x12\x00\x00\x00INFOINAM\x05\x00\x00\x00Title\x00"...)
	b = append(b, "data\x08\x00\x00\x00\x01\x00\x02\x00\x03\x00\x04\x00"...)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	return b
}

// FuzzProbe is a music file a computer, a Bluetooth peer or a download put
// in the library: the Shell reads its headers and tags when it lists the
// library, so nothing in one may stop it.
func FuzzProbe(f *testing.F) {
	for _, s := range []struct {
		file, ext string
		n         int
	}{{"album.flac", ".flac", 8192}, {"hires.flac", ".flac", 4096}, {"tone440.mp3", ".mp3", 4096}, {"mpeg2-22khz.mp3", ".mp3", 2048},
		{"tone660.ogg", ".ogg", 6000}, {"tone520.opus", ".opus", 4096}, {"album.cue", ".cue", 1024}} {
		ext := s.ext
		if ext == ".opus" {
			ext = ".ogg"
		}
		f.Add(head(filepath.Join("..", "testdata", s.file), s.n), kind(ext))
	}
	tone := dsdtest.Tone(dsd.Base, 2, 0.02, []float64{1000}, 0.5)
	f.Add(dsdtest.DSF(dsd.Base, 2, tone, []byte("ID3\x03\x00\x00\x00\x00\x00\x0bTIT2\x00\x00\x00\x01\x00\x00\x00")), kind(".dsf"))
	f.Add(dsdtest.DFF(dsd.Base, 2, tone, false, false, "Title", "Artist"), kind(".dff"))
	f.Add(dsdtest.DFF(dsd.Base, 2, tone, true, true, "Title", "Artist"), kind(".dff"))
	f.Add(dsdtest.SACD("Album", "Artist", dsdtest.Area{Channels: 2, Tracks: []dsdtest.Track{{Title: "One", Frames: tone}}})[sacdAt:], kind(".iso"))
	f.Add(wavSeed(), kind(".wav"))
	f.Fuzz(func(t *testing.T, data []byte, k uint8) {
		if len(data) > 1<<17 {
			return
		}
		ext := probeExts[int(k)%len(probeExts)]
		p := filepath.Join(t.TempDir(), "x"+ext)
		at := int64(0)
		if ext == ".iso" {
			at = sacdAt
		}
		fh, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fh.WriteAt(data, at)
		fh.Close()
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			Tracks(p)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("reading the file did not end")
		}
	})
}
