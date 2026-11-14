package main

import (
	"testing"

	"github.com/kario-os/kario/audio"
)

func TestBuzz(t *testing.T) {
	s := buzz(1000, 10)
	if len(s) != audio.Rate/100 {
		t.Fatalf("%d samples", len(s))
	}
	flips, loud := 0, 0
	for i := 1; i < len(s); i++ {
		if (s[i] < 0) != (s[i-1] < 0) {
			flips++
		}
		loud = max(loud, int(s[i]), -int(s[i]))
	}
	if flips < 18 || flips > 21 || loud > 8000 || loud < 4000 {
		t.Fatalf("1 kHz for 10 ms: %d sign changes, peak %d", flips, loud)
	}
	if last := s[len(s)-1]; last > 200 || last < -200 {
		t.Fatalf("no fade at the end: %d", last)
	}
	if len(bugSound) <= len(eatSound) {
		t.Fatal("the bug's sound is not the longer one")
	}
}
