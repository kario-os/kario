package audio

import (
	"math"
	"testing"
)

func TestMP3Streams(t *testing.T) {
	m, err := OpenMP3("testdata/tone440.mp3")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if ms := m.Len() * 1000 / Rate; ms < 950 || ms > 1150 {
		t.Fatalf("length %d ms, want about 1 s", ms)
	}
	var all []float64
	buf := make([]float64, 960)
	for m.Fill(buf) {
		all = append(all, buf...)
		if len(all) > 3*Rate {
			t.Fatal("does not end")
		}
	}
	// The middle of the file: a 440 Hz sine at 48 kHz crosses zero 880
	// times a second.
	mid := all[len(all)/4 : len(all)*3/4]
	cross, peak := 0, 0.0
	for i := 1; i < len(mid); i++ {
		if (mid[i-1] < 0) != (mid[i] < 0) {
			cross++
		}
		peak = math.Max(peak, math.Abs(mid[i]))
	}
	hz := float64(cross) / 2 / (float64(len(mid)) / Rate)
	if math.Abs(hz-440) > 10 || peak < 0.05 {
		t.Fatalf("decoded %.0f Hz, peak %.2f", hz, peak)
	}
}

func TestMP3RejectsLowRates(t *testing.T) {
	if _, err := OpenMP3("testdata/mpeg2-22khz.mp3"); err == nil {
		t.Fatal("a 22.05 kHz (MPEG-2) file was accepted")
	}
}
