package main

import (
	"fmt"
	"testing"

	"github.com/kario-os/kario/ui"
)

// The player binds the digits, each to its tenth of the video, as page
// actions that are not shown (a tap on the bar presses them); 9 is short
// of the end, so it does not just finish.
func TestPlayerDigitsSeek(t *testing.T) {
	g := &gallery{videos: []ui.MediaItem{{ID: "v1", Kind: ui.MediaVideo, Title: "Clip"}}}
	s := g.play("v1")
	keys := map[string]string{}
	for _, a := range s.Actions {
		if a.Key != "" {
			keys[a.Key] = a.ID
		}
		if a.ID == "jump3" && !a.Hidden {
			t.Error("the digits are in the menu")
		}
	}
	for d := range 10 {
		if got, want := keys[fmt.Sprint(d)], fmt.Sprintf("jump%d", d); got != want {
			t.Errorf("key %d: %q, want %q", d, got, want)
		}
	}
	for _, c := range []struct {
		d       int
		dur, at int64
	}{{0, 60000, 0}, {4, 60000, 24000}, {9, 60000, 54000}, {9, 2000, 1500}, {12, 60000, 54000}, {-1, 60000, 0}, {5, 0, 0}} {
		if got := jumpTo(c.d, c.dur); got != c.at {
			t.Errorf("jumpTo(%d, %d) = %d, want %d", c.d, c.dur, got, c.at)
		}
	}
}
