package audio_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/audio/opusfile"
	"github.com/kario-os/kario/audio/vorbisfile"
)

type source interface {
	Fill(buf []float64) bool
}

// open is audiod's choice of decoder for a "file:" source.
func open(path string) (source, error) {
	switch {
	case filepath.Ext(path) == ".mp3":
		return audio.OpenMP3(path)
	case audio.IsOgg(path):
		if codec, err := audio.OggCodec(path); err != nil {
			return nil, err
		} else if codec == audio.CodecVorbis {
			return vorbisfile.Open(path)
		}
		return opusfile.Open(path)
	}
	return audio.OpenWav(path)
}

var playExts = []string{".wav", ".mp3", ".ogg"}

// FuzzPlay is a music file audiod plays (the library's, a ringtone).
func FuzzPlay(f *testing.F) {
	for _, s := range []struct {
		file string
		k    uint8
	}{{"tone440.mp3", 1}, {"tone660.ogg", 2}, {"tone520.opus", 2}} {
		b, _ := os.ReadFile(filepath.Join("testdata", s.file))
		f.Add(b[:min(len(b), 6000)], s.k)
	}
	f.Add(append(audio.WavHeaderFor(8, 2), 1, 0, 2, 0, 3, 0, 4, 0), uint8(0))
	f.Fuzz(func(t *testing.T, data []byte, k uint8) {
		if len(data) > 1<<16 {
			return
		}
		p := filepath.Join(t.TempDir(), "x"+playExts[int(k)%len(playExts)])
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			s, err := open(p)
			if err != nil {
				return
			}
			buf := make([]float64, 4096)
			for i := 0; i < 64 && s.Fill(buf); i++ {
			}
			if c, ok := s.(interface{ Close() error }); ok {
				c.Close()
			}
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("playing the file did not end")
		}
	})
}
