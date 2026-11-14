package voice

import (
	"math"
	"testing"
)

func TestVoiceCodec(t *testing.T) {
	// Two seconds of a voiced, syllabic signal (as the simulated
	// microphone): a gliding pitch with harmonics, on and off three times
	// a second.
	pcm := make([]int16, 2*rate)
	for i := range pcm {
		x := float64(i) / rate
		f0 := 140 + 30*math.Sin(2*math.Pi*0.7*x)
		v := 0.0
		for h := 1; h <= 12; h++ {
			v += math.Sin(2*math.Pi*f0*float64(h)*x) / float64(h)
		}
		pcm[i] = int16(math.Max(0, math.Sin(2*math.Pi*3*x)) * v * 6000)
	}
	raw, err := Encode(pcm)
	if err != nil {
		t.Fatal(err)
	}
	if ratio := float64(2*len(pcm)) / float64(len(raw)); ratio < 40 {
		t.Fatalf("compressed only %.1f times (%d bytes)", ratio, len(raw))
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(pcm) {
		t.Fatalf("%d samples back for %d", len(got), len(pcm))
	}
	// Opus at this rate keeps the waveform well enough to correlate
	// sample by sample, which also shows the pre-skip is cut exactly (a
	// few samples off and the correlation drops).
	var c, ea, eb float64
	for i := range pcm {
		a, b := float64(pcm[i]), float64(got[i])
		c, ea, eb = c+a*b, ea+a*a, eb+b*b
	}
	if r := c / math.Sqrt(ea*eb); r < 0.7 {
		t.Fatalf("waveform correlation %.3f", r)
	}
	if _, err := Decode([]byte("RIFF....WAVE")); err == nil {
		t.Fatal("a WAV taken for a voice message")
	}
}
