package shell

import "testing"

func TestPaletteMatchesUntruncated(t *testing.T) {
	m := &menuOverlay{palette: true, entries: []menuEntry{
		{group: "Go to", label: "Mini program “Morning Coffee Or…", find: "Mini program “Morning Coffee Order” · Camera"},
		{group: "Apps", label: "Settings"},
	}}
	for _, q := range []string{"Morning", "Morning Coffee Order", "camera", "Settings"} {
		m.query = q
		if len(m.visible()) != 1 {
			t.Errorf("query %q: %d matches, want 1", q, len(m.visible()))
		}
	}
	m.query = "no such entry"
	if len(m.visible()) != 0 {
		t.Errorf("query %q matched", m.query)
	}
}
