package vorbisfile

import (
	"math"
	"testing"

	"github.com/kario-os/kario/audio"
)

// A 44.1 kHz Vorbis file plays at 48 kHz, as long as it is, at its pitch.
func TestStreamsVorbis(t *testing.T) {
	v, err := Open("../testdata/tone660.ogg")
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if ms := v.Len() * 1000 / audio.Rate; ms < 950 || ms > 1050 {
		t.Fatalf("length %d ms, want 1 s", ms)
	}
	if n, err := audio.VorbisLen("../testdata/tone660.ogg"); err != nil || n != v.Len() {
		t.Fatalf("VorbisLen %d (%v), the decoder says %d", n, err, v.Len())
	}
	var all []float64
	buf := make([]float64, 960)
	for v.Fill(buf) {
		all = append(all, buf...)
		if len(all) > 3*audio.Rate {
			t.Fatal("does not end")
		}
	}
	if d := len(all) - v.Len(); d < 0 || d > len(buf) {
		t.Fatalf("played %d samples, the file has %d", len(all), v.Len())
	}
	mid := all[len(all)/4 : len(all)*3/4]
	cross, peak := 0, 0.0
	for i := 1; i < len(mid); i++ {
		if (mid[i-1] < 0) != (mid[i] < 0) {
			cross++
		}
		peak = math.Max(peak, math.Abs(mid[i]))
	}
	hz := float64(cross) / 2 / (float64(len(mid)) / audio.Rate)
	if math.Abs(hz-660) > 10 || peak < 0.05 {
		t.Fatalf("decoded %.0f Hz, peak %.2f", hz, peak)
	}
}

// .ogg is either codec: the first packet decides.
func TestTellsOggCodecs(t *testing.T) {
	for path, want := range map[string]string{"../testdata/tone660.ogg": audio.CodecVorbis, "../testdata/tone520.opus": audio.CodecOpus} {
		if got, err := audio.OggCodec(path); err != nil || got != want {
			t.Errorf("%s: %q (%v), want %q", path, got, err, want)
		}
	}
	if _, err := audio.OggCodec("../testdata/tone440.mp3"); err == nil {
		t.Error("an MP3 passed for Ogg")
	}
	if _, err := Open("../testdata/tone520.opus"); err == nil {
		t.Error("an Opus file opened as Vorbis")
	}
}
