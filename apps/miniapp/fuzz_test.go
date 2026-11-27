package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"
)

// FuzzProgram is a downloaded mini-program's app.json, its catalog and a
// page its runtime sends: its own text, read by the host, which writes the
// program's runtime module from it and checks the page before the Shell
// sees it.
func FuzzProgram(f *testing.F) {
	f.Add([]byte(`{"name":"Scan","permissions":["camera"],"purposes":{"camera":"Scan a code"}}`),
		[]byte(`{"Scan a code":"Escanear un c\u00f3digo","%d items, %s":"%d cosas, %s %%"}`),
		[]byte(`{"id":"mp:fz:index:1","title":"x","body":[{"kind":"text","text":"hi"}],"actions":[{"id":"a","label":"A"}]}`))
	f.Add([]byte(`{"name":"x"}`), []byte(`{"__proto__":"p","\u2028":"</script>"}`), []byte(`{"id":"mp:fz:i:2","body":"not nodes"}`))
	f.Fuzz(func(t *testing.T, appJSON, catalog, page []byte) {
		var m meta
		if json.Unmarshal(appJSON, &m) == nil {
			m.check()
		}
		msgs, err := readCatalog(fstest.MapFS{"locale/es.json": {Data: catalog}}, "es")
		if err != nil {
			return
		}
		m.ID = "fz"
		p := &program{meta: m, msgs: msgs, source: string(catalog)}
		for k := range msgs {
			lookup(msgs, k)
		}
		h := &host{}
		mod, err := h.module(p)
		if err != nil {
			t.Fatal(err)
		}
		head, _, _ := bytes.Cut(mod, []byte("\n"))
		lit := strings.TrimSuffix(strings.TrimPrefix(string(head), "const PROGRAM = JSON.parse("), ");")
		var text string
		if err := json.Unmarshal([]byte(lit), &text); err != nil {
			t.Fatalf("the program's data is not one string literal: %v", err)
		}
		var back struct {
			Source string            `json:"source"`
			Msgs   map[string]string `json:"msgs"`
		}
		if err := json.Unmarshal([]byte(text), &back); err != nil || len(back.Msgs) != len(msgs) {
			t.Fatalf("the program's data did not survive: %v", err)
		}
		// JSON writes what is not UTF-8 as U+FFFD, as the engine would read it.
		if utf8.ValidString(p.source) && back.Source != p.source {
			t.Fatalf("the program's code did not survive: %q", back.Source)
		}
		if s, ok := parsePage(p, page); ok {
			if !strings.HasPrefix(s.ID, "mp:fz:") || len(s.Actions) < 2 || s.Actions[len(s.Actions)-1].ID != "mp.exit" {
				t.Fatalf("page %+v", s)
			}
			if out, _ := json.Marshal(s); len(out) > maxPage+4096 {
				t.Fatalf("a page of %d bytes", len(out))
			}
		}
	})
}
