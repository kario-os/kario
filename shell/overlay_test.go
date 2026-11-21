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

func TestPaletteExactNameFirst(t *testing.T) {
	// The idle screen lists SMS notifications before the apps.
	m := &menuOverlay{palette: true, entries: []menuEntry{
		{group: "Go to", label: "Messages: Wang", find: "Messages: Wang"},
		{group: "Go to", label: "Messages: Carrier", find: "Messages: Carrier"},
		{group: "Apps", label: "Messages"},
	}}
	m.query = "messages"
	got := m.visible()
	if len(got) != 3 || got[0].label != "Messages" || got[1].label != "Messages: Wang" || got[2].label != "Messages: Carrier" {
		t.Errorf("query %q: %v, want the app first and the rest in order", m.query, got)
	}
}

func TestPaletteCommandsOnSearch(t *testing.T) {
	m := &menuOverlay{palette: true, entries: []menuEntry{
		{group: "Apps", label: "Settings"},
		{group: "Commands", label: "Settings · Bluetooth", search: true},
	}}
	if got := m.visible(); len(got) != 1 || got[0].label != "Settings" {
		t.Errorf("empty query: %v, want the app only", got)
	}
	m.query = "bluetooth"
	if got := m.visible(); len(got) != 1 || got[0].label != "Settings · Bluetooth" {
		t.Errorf("query %q: %v, want the command", m.query, got)
	}
}
