package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/jsrt/nativetest"
	"github.com/kario-os/kario/pkg"
)

// nativeProject is the template declaring native:probe, with the probe
// built for arches in its native/ (the other architecture's a retargeted
// copy).
func nativeProject(t *testing.T, arches ...string) string {
	t.Helper()
	probe := nativetest.Build(t)
	dir := newTestProject(t)
	replace(t, dir, "manifest.json", `"main": "main.js",`, `"main": "main.js", "native": ["probe"],`)
	for _, a := range arches {
		p := filepath.Join(dir, "native", a, "probe.so")
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, nativetest.Retarget(probe, a), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// kario build copies the declared modules into lib/<arch>/ as libraries
// and says which architecture is missing; kario package needs the phone's.
func TestBuildNative(t *testing.T) {
	host := jsrt.HostArch()
	if host == "" {
		t.Skip("modules are built for arm64 and x86_64 only")
	}
	if _, err := build(nativeProject(t), buildOptions{}); err == nil || !strings.Contains(err.Error(), `native module "probe": no `) {
		t.Fatalf("no library at all: %v", err)
	}

	dir := nativeProject(t, jsrt.ArchX86_64)
	r, err := build(dir, buildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(dir, "build", "lib", jsrt.ArchX86_64, "probe.so")
	if st, err := os.Stat(lib); err != nil || st.Mode().Perm() != 0o644 || !strings.Contains(r.summary(), "native probe (x86_64)") ||
		!strings.Contains(r.summary(), "no arm64 build") {
		t.Fatalf("build %v:\n%s", err, r.summary())
	}
	store, file := t.TempDir(), filepath.Join(t.TempDir(), "app.phoneapp")
	if _, err := packageApp(dir, packageOptions{out: file, devStore: store, name: "Dev"}); err == nil || !strings.Contains(err.Error(), "is not built for arm64") {
		t.Fatalf("a package without the phone's library: %v", err)
	}

	dir = nativeProject(t, jsrt.ArchARM64, jsrt.ArchX86_64)
	if _, err := packageApp(dir, packageOptions{out: file, devStore: store, name: "Dev"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(file)
	p, err := pkg.Open(raw, pkg.LoadRoots(filepath.Join(store, "trust")))
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]uint32{}
	for _, f := range p.Files {
		modes[f.Path] = f.Mode
	}
	if modes["lib/arm64/probe.so"] != 0o644 || modes["lib/x86_64/probe.so"] != 0o644 {
		t.Fatalf("package files %v", modes)
	}
	if err := jsrt.Check(jsrt.App{Runtime: jsrt.Runtime, Native: []string{"probe"}}, p.Files, jsrt.FromData(p.Data), jsrt.DeviceArch); err != nil {
		t.Fatal(err)
	}

	// -arch picks the architectures that go in, each required; the
	// phone's always is. Both: the store thins it for each phone.
	for _, c := range []struct {
		arch, want string
		files      []string
	}{
		{"arm64", "", []string{"lib/arm64/probe.so"}},
		{"arm64,x86_64", "", []string{"lib/arm64/probe.so", "lib/x86_64/probe.so"}},
		{"x86_64", "needs arm64", nil},
		{"arm64,riscv64", `"riscv64" is not an architecture`, nil},
	} {
		_, err := packageApp(dir, packageOptions{out: file, devStore: store, name: "Dev", arches: c.arch})
		if c.want != "" {
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("-arch %s: %v, want %q", c.arch, err, c.want)
			}
			continue
		}
		if err != nil {
			t.Fatalf("-arch %s: %v", c.arch, err)
		}
		raw, _ := os.ReadFile(file)
		p, err := pkg.Open(raw, pkg.LoadRoots(filepath.Join(store, "trust")))
		if err != nil {
			t.Fatal(err)
		}
		var libs []string
		for _, f := range p.Files {
			if strings.HasPrefix(f.Path, "lib/") {
				libs = append(libs, f.Path)
			}
		}
		if strings.Join(libs, " ") != strings.Join(c.files, " ") {
			t.Errorf("-arch %s: %v", c.arch, libs)
		}
	}
	if _, err := packageApp(nativeProject(t, jsrt.ArchARM64), packageOptions{out: file, devStore: store, name: "Dev", arches: "arm64,x86_64"}); err == nil ||
		!strings.Contains(err.Error(), "is not built for x86_64") {
		t.Fatalf("-arch with an architecture not built: %v", err)
	}

	// -native takes the libraries from elsewhere; the wrong architecture's
	// in a directory, or a name the manifest cannot have, is refused.
	other := t.TempDir()
	_ = os.MkdirAll(filepath.Join(other, jsrt.ArchARM64), 0o755)
	_ = os.WriteFile(filepath.Join(other, jsrt.ArchARM64, "probe.so"), nativetest.Retarget(nativetest.Build(t), jsrt.ArchX86_64), 0o644)
	if _, err := build(nativeProject(t), buildOptions{native: other}); err == nil || !strings.Contains(err.Error(), "a library for x86_64, not arm64") {
		t.Fatalf("the wrong architecture: %v", err)
	}
	dir = newTestProject(t)
	replace(t, dir, "manifest.json", `"main": "main.js",`, `"main": "main.js", "native": ["Probe"],`)
	if _, err := build(dir, buildOptions{}); err == nil || !strings.Contains(err.Error(), "not a module name") {
		t.Fatalf("a bad name: %v", err)
	}
}

// kario test runs an app with native modules under Node.js through
// kario-native.node; kario build leaves "native:" imports to the runtime.
func TestKarioTestNative(t *testing.T) {
	n := node(t)
	host := jsrt.HostArch()
	if host == "" {
		t.Skip("modules are built for arm64 and x86_64 only")
	}
	dir := nativeProject(t, host)
	_ = os.WriteFile(filepath.Join(dir, "src", "main.ts"), []byte(`import * as kario from "kario";
import { add, upper } from "native:probe";
kario.on("start", () => kario.reset({ id: "main", title: upper("sum") + " " + add(2, 3), body: [] }));
`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "test", "main.test.ts"), []byte(`import { launch, test } from "@kario/test";
test("native", async () => {
  const app = await launch();
  app.expect("SUM 5");
});
`), 0o644)
	if err := testApp(dir, testOptions{node: n, lang: "en"}); err != nil {
		t.Fatal(err)
	}
	main, _ := os.ReadFile(filepath.Join(dir, "build", "main.js"))
	if _, err := build(dir, buildOptions{minify: false}); err != nil {
		t.Fatal(err)
	}
	if main, _ = os.ReadFile(filepath.Join(dir, "build", "main.js")); !strings.Contains(string(main), `from "native:probe"`) {
		t.Fatalf("main.js:\n%s", main)
	}
}

// buildExampleModule compiles examples/imgproc's module for this machine
// into dir/<arch>/imgproc.so, as make native-example does.
func buildExampleModule(t *testing.T, dir string) {
	t.Helper()
	cc := nativetest.Compiler(t)
	out := filepath.Join(dir, jsrt.HostArch(), "imgproc.so")
	_ = os.MkdirAll(filepath.Dir(out), 0o755)
	if msg, err := exec.Command(cc, "-shared", "-fPIC", "-O2", "-I../../sdk/c/include", "-o", out, "../../examples/imgproc/native/imgproc.c").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, msg)
	}
}
