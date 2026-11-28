package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kario-os/kario/i18n"
)

// With an incremental bundle chosen (system.update's available.delta), the
// size shown is the delta's, never the full bundle's alone.
func TestUpdateSize(t *testing.T) {
	i18n.SetLang("en")
	n := func(v int64) *int64 { return &v }
	for _, c := range []struct {
		full  int64
		delta *int64
		want  string
	}{
		{130 << 20, nil, "130.0 MB"},
		{130 << 20, n(7 << 20), "7.0 MB incremental (the full system is 130.0 MB)"},
		{0, n(7 << 20), "7.0 MB incremental"},
		{130 << 20, n(0), "Incremental"},
		{0, nil, ""},
	} {
		if got := offerSize(c.full, c.delta); got != c.want {
			t.Errorf("offerSize(%d, %v) = %q, want %q", c.full, c.delta, got, c.want)
		}
	}
	var p app
	raw := `{"supported": true, "version": "0.7.0", "slot": "a", "state": "idle",
		"available": {"version": "0.8.0", "size": 136314880, "notes": "Kario 0.8.0", "delta": {"from": "0.7.0", "size": 1572864, "url": "d.raucb"}}}`
	if err := json.Unmarshal([]byte(raw), &p.upd); err != nil {
		t.Fatal(err)
	}
	for _, node := range p.updateScreen().Body {
		if node.ID == "available" {
			if !strings.Contains(node.Subtitle, "1.5 MB incremental") {
				t.Fatalf("the offer reads %q", node.Subtitle)
			}
			return
		}
	}
	t.Fatal("no offer on the update page")
}

// While installing, the page says how far: a download tried again, an
// incremental bundle's operations (where it resumed), or RAUC's percent.
func TestInstallProgress(t *testing.T) {
	i18n.SetLang("en")
	for raw, want := range map[string]string{
		`{"state": "installing", "progress": {"percent": 40}}`:                                     "40% done",
		`{"state": "installing", "progress": {"percent": 40, "done": 52, "total": 83}}`:            "Operation 52 of 83",
		`{"state": "installing", "progress": {"done": 52, "total": 83, "resumed": 40}}`:            "Operation 52 of 83 (resumed at 40)",
		`{"state": "installing", "progress": {}, "retry": {"attempt": 1, "of": 3, "at": "14:05"}}`: "The download stopped; trying again at 14:05 (1 of 3)",
		`{"state": "installing", "progress": {"percent": 0}}`:                                      "",
	} {
		var u update
		if err := json.Unmarshal([]byte(raw), &u); err != nil {
			t.Fatal(err)
		}
		if got := installProgress(u); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}

// An automatic install that stopped part way: the page says why it waits
// (the shell's words), and still offers the update to install now.
func TestUpdatePaused(t *testing.T) {
	i18n.SetLang("en")
	var p app
	raw := `{"supported": true, "version": "0.7.0", "slot": "a", "state": "idle", "paused": "The automatic update is paused during the call; it goes on afterwards",
		"available": {"version": "0.8.0", "size": 136314880}}`
	if err := json.Unmarshal([]byte(raw), &p.upd); err != nil {
		t.Fatal(err)
	}
	paused, offer := false, false
	for _, node := range p.updateScreen().Body {
		paused = paused || node.Text == p.upd.Paused || node.Title == p.upd.Paused
		offer = offer || node.ID == "available"
	}
	if !paused || !offer {
		t.Fatalf("paused shown %v, the offer %v", paused, offer)
	}
}
