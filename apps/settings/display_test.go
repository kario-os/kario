package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// The refresh rate's row is on Display and brightness only where the panel
// has more than one rate; left and right step through automatic,
// standard and high, kept at once, and the system is told the index.
func TestRefreshRateRow(t *testing.T) {
	p, _, reqs := fakeShell(t)
	got := make(chan []ui.Node)
	page := func() []ui.Node {
		p.a.Post(func() {
			s, _ := p.groupScreen(groupPrefix + "display")
			got <- s.Body
		})
		return <-got
	}
	row := func(body []ui.Node) *ui.Node {
		for i := range body {
			if body[i].ID == ui.SettingRefreshRate {
				return &body[i]
			}
		}
		return nil
	}
	if r := row(page()); r != nil {
		t.Fatalf("a panel of one rate has the row: %+v", r)
	}
	p.a.Post(func() { p.s.Display.Rates, p.s.Display.Hz = []int{60, 120}, 60 })
	r := row(page())
	if r == nil || r.Trailing != "Automatic" || r.Title != "Refresh rate" {
		t.Fatalf("the row on a 60/120 Hz panel: %+v", r)
	}
	for _, c := range []struct {
		action   string
		index    int
		trailing string
	}{{"next", 1, "Standard 60 Hz"}, {"next", 2, "High 120 Hz"}, {"next", 0, "Automatic"}, {"prev", 2, "High 120 Hz"}} {
		p.a.Post(func() {
			p.action(sdk.ActionEvent{Screen: groupPrefix + "display", Node: ui.SettingRefreshRate, Action: c.action})
		})
		select {
		case m := <-reqs:
			var args ui.SettingArgs
			if err := json.Unmarshal(m.Args, &args); err != nil || m.Cap != ui.CapSettingsSet || args.Key != ui.SettingRefreshRate || int(args.Value) != c.index {
				t.Fatalf("%s: sent %s %s, want refresh_rate %d", c.action, m.Cap, m.Args, c.index)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: nothing sent", c.action)
		}
		if r := row(page()); r == nil || r.Trailing != c.trailing {
			t.Fatalf("%s: the row shows %+v, want %q", c.action, r, c.trailing)
		}
		// The system's answer, as the Shell gives it.
		p.a.Post(func() { p.sets = nil })
	}
	p.a.Post(func() { p.s.Display.Reason, p.s.Display.Hz = "cap:battery", 60 })
	if r := row(page()); r == nil || r.Subtitle != "Battery low: at most 60 Hz now" {
		t.Fatalf("capped by the battery: %+v", r)
	}
}
