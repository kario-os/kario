package opusfile

import (
	"math"
	"testing"

	"github.com/kario-os/kario/audio"
)

func TestStreamsStereoOpus(t *testing.T) {
	o, err := Open("../testdata/tone520.opus")
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if ms := o.Len() * 1000 / audio.Rate; ms < 1450 || ms > 1550 {
		t.Fatalf("length %d ms, want 1.5 s", ms)
	}
	var all []float64
	buf := make([]float64, 960)
	for o.Fill(buf) {
		all = append(all, buf...)
		if len(all) > 3*audio.Rate {
			t.Fatal("does not end")
		}
	}
	if d := len(all) - o.Len(); d < 0 || d > len(buf) {
		t.Fatalf("played %d samples, the file has %d", len(all), o.Len())
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
	if math.Abs(hz-520) > 10 || peak < 0.05 {
		t.Fatalf("decoded %.0f Hz, peak %.2f", hz, peak)
	}
}

func TestRejectsOtherOgg(t *testing.T) {
	if _, err := Open("../testdata/tone440.mp3"); err == nil {
		t.Fatal("an MP3 opened as Opus")
	}
}
