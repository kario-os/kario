package shell

import (
	"encoding/json"
	"io"
	"log"
	"testing"
)

func TestDump(t *testing.T) {
	c := &Core{cfg: Config{RunDir: t.TempDir(), StateDir: t.TempDir()}, log: log.New(io.Discard, "", 0)}
	c.apps = []*App{{M: &Manifest{ID: "com.example.a", Name: "A", Commands: []AppCommand{{ID: "x", Label: "New", Arg: "new"}}}}}
	c.overlays = []overlay{&confirmOverlay{text: "?"}}
	if err := c.WriteDump(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(c.Dump())
	var d struct {
		Apps     []struct{ ID string }
		Overlays []string
	}
	_ = json.Unmarshal(raw, &d)
	if len(d.Apps) != 1 || d.Apps[0].ID != "com.example.a" || len(d.Overlays) != 1 || d.Overlays[0] != "confirmOverlay" {
		t.Fatalf("dump %s", raw)
	}
}
