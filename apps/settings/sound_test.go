package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// On a phone with a sound switch, Settings › Sound shows the mode it set
// instead of the silent mode switch: OK on the row asks nothing of the
// system, and the page follows the switch when the system says it moved.
// Without one, the switch as before.
func TestSoundModeBySwitch(t *testing.T) {
	others := make(chan ui.Msg, 64)
	p, _, reqs := fakeShellWith(t, others)
	if n := node(p.soundScreen(), ui.SettingSilent); n.Title != "Silent mode" || len(n.Actions) != 1 {
		t.Fatalf("without a switch: %+v", n)
	}
	p.s.Sound.Switch, p.s.Sound.Mode = true, ui.SoundSilent
	s := p.soundScreen()
	if n := node(s, ui.SettingSilent); n.Title != "Sound mode" || n.Trailing != "Silent" || len(n.Actions) != 0 ||
		!strings.Contains(n.Subtitle, "neither ring nor vibrate") || !strings.Contains(texts(s), "Set by the sound switch") {
		t.Fatalf("with one: %+v", s.Body)
	}
	raw, _ := json.Marshal(map[string]string{"key": "sound", "value": ui.SoundVibrate})
	p.a.Post(func() {
		p.a.Push(p.soundScreen())
		p.action(sdk.ActionEvent{Screen: "sound", Node: ui.SettingSilent, Action: "toggle"})
		p.soundChanged(string(raw))
	})
	timeout := time.After(10 * time.Second)
	for {
		select {
		case r := <-reqs:
			t.Fatalf("OK on the row asked the system for %s", r.Cap)
		case m := <-others:
			if m.T != ui.MsgUpdate || m.Screen == nil || m.Screen.ID != "sound" {
				continue
			}
			if n := node(*m.Screen, ui.SettingSilent); n.Trailing != "Vibrate" || !strings.Contains(n.Subtitle, "only vibrate") {
				t.Fatalf("moved to vibrate: %+v", n)
			}
			return
		case <-timeout:
			t.Fatal("the page did not follow the switch")
		}
	}
}
