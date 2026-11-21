package main

import "testing"

// The ID and version name the downloaded file and the request: an entry
// that does not have their shapes is left out, signed catalogue or not.
func TestCatalogEntriesKeepTheirShapes(t *testing.T) {
	for _, c := range []struct {
		id, version string
		ok          bool
	}{
		{"org.example.todo", "1.0", true},
		{"com.phone.chat", "2.10.3", true},
		{"org.example.todo", "../org.example.notes/1.0", false},
		{"org.example.todo", "1/../../../outside", false},
		{"org.example.todo", "1.0-beta", false},
		{"org.example.todo", "", false},
		{"../org.example", "1.0", false},
		{"org.example/../x", "1.0", false},
		{"Org.Example", "1.0", false},
		{"todo", "1.0", false},
	} {
		if got := listable(entry{ID: c.id, Version: c.version}); got != c.ok {
			t.Errorf("listable(%q, %q) = %v, want %v", c.id, c.version, got, c.ok)
		}
	}
}

// The store is asked for what this phone's platform level runs, and an
// entry needing a newer one is not listed.
func TestCatalogForPlatform(t *testing.T) {
	if got := catalogURL("https://store.example", 3); got != "https://store.example/v1/catalog?platform=3" {
		t.Errorf("catalogue URL %s", got)
	}
	for _, c := range []struct {
		need, have int
		ok         bool
	}{{0, 1, true}, {1, 1, true}, {2, 1, false}, {2, 3, true}} {
		if got := runs(entry{ID: "org.example.todo", Version: "1.0", MinPlatform: c.need}, c.have); got != c.ok {
			t.Errorf("min_platform %d on level %d: %v, want %v", c.need, c.have, got, c.ok)
		}
	}
}
