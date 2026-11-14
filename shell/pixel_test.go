package shell

import "testing"

func TestPixelScroll(t *testing.T) {
	// A 1000 px article in a 400 px area: 80% of the area per key.
	f, steps := 0.0, 0
	for {
		next, inside := pixelScroll(f, 400, 1000, 1)
		if !inside {
			break
		}
		f, steps = next, steps+1
	}
	if f != 1 || steps != 2 {
		t.Fatalf("down: frac %v after %d steps, want 1 after 2 (320 px of 600 a step)", f, steps)
	}
	if back, inside := pixelScroll(1, 400, 1000, -1); !inside || back >= 1 {
		t.Fatalf("up from the end: %v %v", back, inside)
	}
	if _, inside := pixelScroll(0, 400, 300, 1); inside {
		t.Fatal("a node that fits scrolled")
	}
}
