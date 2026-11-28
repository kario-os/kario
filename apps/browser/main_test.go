package main

import (
	"os"
	"testing"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/statefile/statefiletest"
)

// The bookmarks outlive a write that fails midway, and a file cut short
// (the power going) is read from its copy.
func TestBookmarksKeptWhole(t *testing.T) {
	b := &browser{app: &sdk.App{DataDir: t.TempDir()}}
	b.Bookmarks = []entry{{URL: "https://a.example/", Title: "A"}}
	b.save()
	b.History = []entry{{URL: "https://b.example/", Title: "B"}}
	statefiletest.FailPast(t, 16, b.save)
	got := &browser{app: b.app}
	if got.load(); len(got.Bookmarks) != 1 || len(got.History) != 0 {
		t.Fatalf("after a failed write: %+v %+v", got.Bookmarks, got.History)
	}
	b.save()
	if err := os.Truncate(b.path(), 20); err != nil {
		t.Fatal(err)
	}
	got = &browser{app: b.app}
	if got.load(); len(got.Bookmarks) != 1 || len(got.History) != 1 {
		t.Fatalf("cut short: %+v %+v, want them from the copy", got.Bookmarks, got.History)
	}
}
