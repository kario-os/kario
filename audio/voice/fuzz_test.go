package voice

import (
	"math"
	"testing"
)

// FuzzDecode is a voice message a chat peer sent.
func FuzzDecode(f *testing.F) {
	pcm := make([]int16, rate/5)
	for i := range pcm {
		pcm[i] = int16(8000 * math.Sin(float64(i)*2*math.Pi*300/rate))
	}
	if raw, err := Encode(pcm); err == nil {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		if out, err := Decode(data); err == nil && len(out) > MaxSamples {
			t.Fatalf("%d samples", len(out))
		}
	})
}
