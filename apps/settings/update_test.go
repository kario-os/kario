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
