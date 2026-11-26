package main

import (
	"strings"
	"testing"

	"github.com/kario-os/kario/ui"
)

func pageText(s ui.Screen) string {
	var b strings.Builder
	for _, n := range s.Body {
		b.WriteString(n.Text + "|" + n.Title + "\n")
	}
	return b.String()
}

// Until the installed apps and the catalogue first come in, the page says
// it is loading them, not that there are none.
func TestPageBeforeItsData(t *testing.T) {
	loading, noApps, noCatalog := tr.T("Loading…"), tr.T("No apps installed yet"), tr.T("The catalogue is empty")
	p := &page{server: "https://store.example", downloads: t.TempDir()}
	before := pageText(p.screen())
	if strings.Count(before, loading) != 2 || strings.Contains(before, noApps) || strings.Contains(before, noCatalog) {
		t.Errorf("before the data:\n%s", before)
	}
	p.loaded = true
	after := pageText(p.screen())
	if strings.Contains(after, loading) || !strings.Contains(after, noApps) || !strings.Contains(after, noCatalog) {
		t.Errorf("once it came in, empty:\n%s", after)
	}
	p.installed = []ui.PkgInfo{{ID: "org.example.todo", Name: "Todo", Version: "1.0"}}
	p.catalog = []entry{{ID: "org.example.todo", Name: "Todo", Version: "1.1"}}
	if got := pageText(p.screen()); strings.Contains(got, noApps) || strings.Contains(got, noCatalog) || !strings.Contains(got, "|Todo\n") {
		t.Errorf("with data:\n%s", got)
	}
	none := pageText((&page{downloads: t.TempDir()}).screen())
	if !strings.Contains(none, tr.T("No store backend configured")) || strings.Count(none, loading) != 1 {
		t.Errorf("no backend, before the installed apps:\n%s", none)
	}
}
