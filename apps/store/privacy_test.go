package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ui"
)

func texts(nodes []ui.Node) string {
	var out []string
	for _, n := range nodes {
		out = append(out, n.Text)
	}
	return strings.Join(out, "\n")
}

// An app's page lists its permissions with their purposes, and its privacy
// label: the system's word when it has no network, else the developer's.
func TestPrivacyNodes(t *testing.T) {
	i18n.SetLang("en")
	var e entry
	raw := `{"id": "org.example.maps", "permissions": ["net", "location", "contacts"], "lang": "en",
		"purposes": {"location": "Show where you are", "contacts": "Share a place"},
		"locales": {"zh-Hans": {"purposes": {"location": "zh"}}},
		"privacy": {"collects": [{"data": "usage", "use": ["analytics", "ads"], "shared": true},
			{"data": "location", "use": ["app"], "linked": true}], "local": ["contacts"]}}`
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatal(err)
	}
	e.localize()
	want := "Permissions\nNetwork\nLocation: Show where you are\nContacts: Share a place"
	if got := texts(permissionNodes(e)); got != want {
		t.Errorf("permissions:\n%s\nwant:\n%s", got, want)
	}
	want = "Privacy\nSent: Location (app features, linked to you)\nSent: Usage data (analytics, advertising, shared with third parties)\n" +
		"Used on this phone only: Contacts\nDeclared by the developer and checked in the store's review"
	if got := texts(privacyNodes(e)); got != want {
		t.Errorf("label:\n%s\nwant:\n%s", got, want)
	}

	e.Privacy = &ui.Privacy{}
	if got := texts(privacyNodes(e)); !strings.Contains(got, "Collects no data") {
		t.Errorf("an empty label: %s", got)
	}
	e.Privacy = nil
	if got := texts(privacyNodes(e)); !strings.Contains(got, "has not provided") {
		t.Errorf("no label: %s", got)
	}
	e.Permissions = []string{ui.PermLocation}
	if got := privacyNodes(e); len(got) != 2 || got[1].Style != ui.StyleOK || !strings.Contains(got[1].Text, "no network access") {
		t.Errorf("no network: %+v", got)
	}
	e.Permissions = nil
	if got := texts(permissionNodes(e)); got != "Permissions\nNone" {
		t.Errorf("no permissions: %s", got)
	}
}
