package shell

import (
	"io"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/jsrt/nativetest"
	"github.com/kario-os/kario/pkg"
)

// A TypeScript app installs with its module and no program; the Shell
// starts kario-jsrt with the module.
func TestJSApp(t *testing.T) {
	c := &Core{cfg: Config{AppBinDir: "/usr/lib/phone/bin"}, log: log.New(io.Discard, "", 0)}
	pack := func(manifest string, files ...pkg.File) *pkg.Package {
		p := &pkg.Package{Manifest: []byte(manifest), Files: append([]pkg.File{{Path: "manifest.json", Mode: 0o644}}, files...), Data: map[string][]byte{}}
		for _, f := range p.Files {
			p.Data[f.Path] = []byte("x")
		}
		return p
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

// A TypeScript app's native modules install when they are built for this
// device, and not otherwise.
func TestJSAppNative(t *testing.T) {
	if !alone(t) {
		return
	}
	arch := jsrt.HostArch()
	if arch == "" {
		t.Skip("modules are built for arm64 and x86_64 only")
	}
	probe := nativetest.Build(t)
	c := &Core{cfg: Config{AppBinDir: "/usr/lib/phone/bin"}, log: log.New(io.Discard, "", 0)}
	pack := func(manifest, arch string, so []byte) *pkg.Package {
		mod := jsrt.NativePath(arch, "probe")
		return &pkg.Package{Manifest: []byte(manifest),
			Files: []pkg.File{{Path: "manifest.json", Mode: 0o644}, {Path: "main.js", Mode: 0o644}, {Path: mod, Mode: 0o644}},
			Data:  map[string][]byte{"manifest.json": []byte(manifest), "main.js": []byte("import"), mod: so}}
	}
	const manifest = `{"id":"com.example.n","name":"N","runtime":"js","native":["probe"]}`
	m, err := c.checkPackage(pack(manifest, arch, probe))
	if err != nil || !slices.Equal(m.Native, []string{"probe"}) {
		t.Fatalf("%+v %v", m, err)
	}
	other := nativetest.Other()
	if _, err := c.checkPackage(pack(manifest, other, nativetest.Retarget(probe, other))); err == nil || !strings.Contains(err.Error(), "is not built for "+arch) {
		t.Fatalf("a package for the other architecture: %v", err)
	}
	if _, err := c.checkPackage(pack(`{"id":"com.example.n","name":"N","runtime":"js"}`, arch, probe)); err == nil || !strings.Contains(err.Error(), "does not declare") {
		t.Fatalf("an undeclared module: %v", err)
	}
}
