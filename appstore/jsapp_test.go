package appstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/pkg"
)

// Review takes a TypeScript app with its module and no program of its own,
// and refuses one that brings native code or bytecode.
func TestReviewJSApp(t *testing.T) {
	_, ts := newServer(t)
	dev := developer(t, ts, "Alice", "91110108MA01ABCD2H")
	pack := func(id string, files map[string]os.FileMode) []byte {
		t.Helper()
		dir := t.TempDir()
		raw, _ := json.Marshal(map[string]any{"id": id, "name": "Notes", "version": "1.0", "runtime": "js", "main": "main.js"})
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644)
		for name, mode := range files {
			_ = os.WriteFile(filepath.Join(dir, name), []byte(`import * as kario from "kario";`), mode)
		}
		pk, err := pkg.Build(dir, dev)
		if err != nil {
			t.Fatal(err)
		}
		return pk
	}
	if code, st := submit(t, ts, pack("org.example.tsnotes", map[string]os.FileMode{"main.js": 0o644})); code != 200 || st != "pending_review" {
		t.Fatalf("a TypeScript app: %d %s", code, st)
	}
	for _, c := range []struct {
		files map[string]os.FileMode
		want  string
	}{
		{map[string]os.FileMode{"main.js": 0o644, "helper": 0o755}, "no executable file"},
		{map[string]os.FileMode{"main.js": 0o644, "main.jsc": 0o644}, "not bytecode"},
		{map[string]os.FileMode{"app.js": 0o644}, "not in the package"},
	} {
		var out struct{ Error string }
		code := call(t, "POST", ts.URL+"/v1/submissions", "", pack("org.example.tsbad", c.files), &out)
		if code == 200 || !strings.Contains(out.Error, c.want) {
			t.Errorf("%v: %d %q, want %q", c.files, code, out.Error, c.want)
		}
	}
}
