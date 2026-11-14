package shell

import (
	"io"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kario-os/kario/pkg"
)

// A TypeScript app installs with its module and no program; the Shell
// starts kario-jsrt with the module.
func TestJSApp(t *testing.T) {
	c := &Core{cfg: Config{AppBinDir: "/usr/lib/phone/bin"}, log: log.New(io.Discard, "", 0)}
	pack := func(manifest string, files ...pkg.File) *pkg.Package {
		return &pkg.Package{Manifest: []byte(manifest), Files: append([]pkg.File{{Path: "manifest.json", Mode: 0o644}}, files...)}
	}
	js := pkg.File{Path: "main.js", Mode: 0o644}
	m, err := c.checkPackage(pack(`{"id":"com.example.hellots","name":"N","runtime":"js","main":"main.js"}`, js))
	if err != nil {
		t.Fatal(err)
	}
	m.dir = "/data/packages/com.example.hellots"
	if exe, args := c.resolveExec(m), c.execArgs(m); exe != "/usr/lib/phone/bin/kario-jsrt" ||
		!slices.Equal(args, []string{filepath.Join(m.dir, "main.js")}) {
		t.Fatalf("starts %s %v", exe, args)
	}
	for _, c2 := range []struct {
		manifest string
		files    []pkg.File
		want     string
	}{
		{`{"id":"com.example.a","name":"N","runtime":"js"}`, []pkg.File{js}, ""},
		{`{"id":"com.example.a","name":"N","runtime":"js","exec":"main.js"}`, []pkg.File{js}, `no "exec"`},
		{`{"id":"com.example.a","name":"N","runtime":"js"}`, []pkg.File{js, {Path: "helper", Mode: 0o755}}, "no executable file"},
		{`{"id":"com.example.a","name":"N","runtime":"js"}`, []pkg.File{js, {Path: "main.jsc", Mode: 0o644}}, "not bytecode"},
		{`{"id":"com.example.a","name":"N","runtime":"wasm"}`, []pkg.File{js}, "unknown runtime"},
		{`{"id":"com.example.a","name":"N","runtime":"js"}`, nil, "not in the package"},
		{`{"id":"com.example.a","name":"N"}`, []pkg.File{js}, "needs a name and exec"},
		{`{"id":"com.example.a","name":"N","exec":"app","main":"main.js"}`, []pkg.File{{Path: "app", Mode: 0o755}}, `"main" goes with`},
	} {
		_, err := c.checkPackage(pack(c2.manifest, c2.files...))
		if c2.want == "" && err != nil || c2.want != "" && (err == nil || !strings.Contains(err.Error(), c2.want)) {
			t.Errorf("%s: %v, want %q", c2.manifest, err, c2.want)
		}
	}
	native := &Manifest{Exec: "phone-app-notes", Args: []string{"-x"}, dir: "/apps/notes"}
	if args := c.execArgs(native); !slices.Equal(args, []string{"-x"}) {
		t.Fatalf("native args %v", args)
	}
}
