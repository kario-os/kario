package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// The page groups its rows under headings (screen reader, seeing, keys,
// hearing, shortcut), says what is not there yet, and its switches tell
// the screen reader they are switches.
func TestA11yPage(t *testing.T) {
	defer i18n.SetLang("en")
	i18n.SetLang("en")
	var p app
	raw := `{"settings": {"accessibility": {"magnification": 2, "key_repeat_delay": 250, "key_repeat_rate": 30, "long_press": 600,
		"bold_text": true, "audio_balance": -0.3, "color_correction": "tritanomaly"}}, "power_ends_call": true}`
	if err := json.Unmarshal([]byte(raw), &p.s); err != nil {
		t.Fatal(err)
	}
	s, ok := p.groupScreen(a11yGroup)
	if !ok || s.Title != tr.T("Accessibility") {
		t.Fatalf("no page: %+v", s)
	}
	var heads []string
	rows := map[string]ui.Node{}
	notes := 0
	for _, n := range s.Body {
		switch {
		case n.Kind == ui.KindSection:
			heads = append(heads, n.Text)
		case n.Kind == ui.KindItem:
			rows[n.ID] = n
		case n.Style == ui.StyleDim:
			notes++
		}
	}
	if want := []string{"Screen reader", "Seeing", "Keys", "Hearing", "Shortcut"}; len(heads) != len(want) || heads[0] != want[0] || heads[4] != want[4] {
		t.Errorf("headings %q", heads)
	}
	if len(rows) != len(a11yRows()) || notes != 3 {
		t.Errorf("%d rows, %d notes", len(rows), notes)
	}
	if n := rows[ui.SettingBoldText]; n.Role != ui.RoleSwitch || !n.Checked || n.Trailing != onOff(true) {
		t.Errorf("bold text: %+v", n)
	}
	if n := rows[ui.SettingLargeText]; n.Role != ui.RoleSwitch || n.Checked {
		t.Errorf("large text, a switch of the display group's too: %+v", n)
	}
	if n := rows[ui.SettingPowerEndsCall]; n.Role != ui.RoleSwitch || !n.Checked || n.Title != "Power key ends call" {
		t.Errorf("power key ends call: %+v", n)
	}
	// Where the end key is the power key the system names no such switch.
	p.s.PowerEndsCall = nil
	if _, ok := p.row(ui.SettingPowerEndsCall); ok {
		t.Error("a power key switch on a phone without a power key of its own")
	}
	for id, want := range map[string]string{ui.SettingAudioBalance: "Left 30%", ui.SettingColorCorrection: "Tritanomaly (blue-yellow)",
		ui.SettingKeyRepeatDelay: "0.25 s", ui.SettingKeyRepeatRate: "30 a second", ui.SettingLongPress: "0.6 s", ui.SettingSlowKeys: "Off",
		ui.SettingMagnification: "2×", ui.SettingA11yShortcut: "Magnification", ui.SettingReaderOnDemand: "When the focus moves", ui.SettingKeyFeedback: "None"} {
		if got := rows[id].Trailing; got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
	if got := p.a11ySummary(); got != "Bold text · Colour correction" {
		t.Errorf("summary %q", got)
	}
	if groupOf(ui.SettingStickyKeys) != a11yGroup || groupOf(ui.SettingLargeText) != groupPrefix+"display" {
		t.Error("deep links: a row of the page goes to it; large text stays on Display")
	}
}

// Each row sends its setting as the system takes it: a switch 1 or 0, a
// list's index, a time or rate itself, the balance in tenths; choices go
// round at their ends.
func TestA11yActions(t *testing.T) {
	p, _, reqs := fakeShell(t)
	ready := make(chan struct{})
	p.a.Post(func() {
		p.s.Settings.A11y = a11yInfo{Magnification: 2, RepeatDelay: 250, RepeatRate: 30, LongPress: 600}
		on := true
		p.s.PowerEndsCall = &on
		close(ready)
	})
	<-ready
	for _, c := range []struct {
		node, action string
		want         float64
	}{
		{ui.SettingBoldText, "toggle", 1},
		{ui.SettingBoldText, "toggle", 0},
		{ui.SettingColorCorrection, "prev", 3},
		{ui.SettingColorCorrection, "next", 0},
		{ui.SettingMagnification, "next", 3},
		{ui.SettingMagnification, "next", 2},
		{ui.SettingKeyRepeatRate, "prev", 0},
		{ui.SettingKeyRepeatDelay, "next", 500},
		{ui.SettingLongPress, "next", 1000},
		{ui.SettingSlowKeys, "next", 300},
		{ui.SettingAudioBalance, "left", -0.1},
		{ui.SettingAudioBalance, "right", 0},
		{ui.SettingA11yShortcut, "next", 1},
		{ui.SettingReaderOnDemand, "next", 1},
		{ui.SettingKeyFeedback, "prev", 3},
		{ui.SettingPowerEndsCall, "toggle", 0},
		{ui.SettingPowerEndsCall, "toggle", 1},
	} {
		p.a.Post(func() { p.action(sdk.ActionEvent{Screen: a11yGroup, Node: c.node, Action: c.action}) })
	wait:
		for {
			select {
			case m := <-reqs:
				if m.Cap != ui.CapSettingsSet {
					// The top's rows load what they show (hotspot, NFC).
					continue
				}
				var args ui.SettingArgs
				if err := json.Unmarshal(m.Args, &args); err != nil || args.Key != c.node || args.Value != c.want {
					t.Fatalf("%s %s: sent %s, want %v", c.node, c.action, m.Args, c.want)
				}
				// As if answered: the next change goes out.
				p.a.Post(func() { p.sets = nil })
				break wait
			case <-time.After(3 * time.Second):
				t.Fatalf("%s %s: nothing sent", c.node, c.action)
			}
		}
	}
}
