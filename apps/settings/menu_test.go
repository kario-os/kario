package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ui"
)

// Every row and group of the top is a palette command labelled as the row
// reads in each language, and every command finds its row.
func TestMenuCommands(t *testing.T) {
	var m struct {
		Name     string `json:"name"`
		Commands []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
			Arg   string `json:"arg"`
		} `json:"commands"`
		Locales map[string]struct {
			Commands map[string]string `json:"commands"`
		} `json:"locales"`
	}
	raw, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var p app
	nfcState.tried, locState.tried, hsState.tried = true, true, true
	p.radio.Cellular = "4G"
	if err := json.Unmarshal([]byte(`{"sim": {"state": "ready"}, "display": {"rates": [60, 120]}}`), &p.s); err != nil {
		t.Fatal(err)
	}
	titles := func() map[string]string {
		out := map[string]string{}
		for _, g := range groups() {
			if g.id != "" {
				out[g.id] = g.title
			}
			for _, id := range g.rows {
				n, ok := p.row(id)
				if !ok {
					t.Fatalf("row %q is missing", id)
				}
				out[id] = n.Title
			}
		}
		for _, n := range p.soundScreen().Body {
			if n.ID == ui.SettingSilent {
				out[n.ID] = n.Title
			}
		}
		return out
	}
	defer i18n.SetLang("en")
	i18n.SetLang("zh-Hans")
	want := titles()
	seen := map[string]bool{}
	for _, c := range m.Commands {
		if c.Arg != c.ID || want[c.ID] == "" || seen[c.ID] {
			t.Errorf("command %q (arg %q): not a row of the top, or twice", c.ID, c.Arg)
		}
		seen[c.ID] = true
		if c.Label != want[c.ID] {
			t.Errorf("command %q is labelled %q, the row reads %q", c.ID, c.Label, want[c.ID])
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("row %q has no command", id)
		}
	}
	for _, lang := range []string{"zh-Hant", "en", "es"} {
		i18n.SetLang(lang)
		want := titles()
		for id, label := range m.Locales[lang].Commands {
			if label != want[id] {
				t.Errorf("%s: command %q is labelled %q, the row reads %q", lang, id, label, want[id])
			}
		}
		if len(m.Locales[lang].Commands) != len(m.Commands) {
			t.Errorf("%s: %d command labels for %d commands", lang, len(m.Locales[lang].Commands), len(m.Commands))
		}
	}
}

// A deep link to a row of a group shows the group's page with the row
// focused; one to a row of the top focuses it there.
func TestMenuGroupOf(t *testing.T) {
	for row, want := range map[string]string{"update": "g.device", "net_speed": "g.status", "wifi": "", "g.mobile": "", "perms": "g.privacy", "location": "g.privacy"} {
		if got := groupOf(row); got != want {
			t.Errorf("groupOf(%q) = %q, want %q", row, got, want)
		}
	}
}
