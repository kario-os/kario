package jsrt

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/kario-os/kario/jsrt/nativetest"
	"github.com/kario-os/kario/pkg"
)

func files(data map[string][]byte, exec ...string) []pkg.File {
	var out []pkg.File
	for p := range data {
		mode := uint32(0o644)
		for _, e := range exec {
			if e == p {
				mode = 0o755
			}
		}
		out = append(out, pkg.File{Path: p, Mode: mode})
	}
	return out
}

func TestCheck(t *testing.T) {
	js := map[string][]byte{"manifest.json": []byte("{}"), "main.js": []byte("import"), "locale/zh-Hans.json": []byte("{}")}
	with := func(path string, raw []byte) map[string][]byte {
		out := map[string][]byte{path: raw}
		for k, v := range js {
			out[k] = v
		}
		return out
	}
	for _, c := range []struct {
		name string
		app  App
		data map[string][]byte
		exec []string
		want string // "" passes, else a part of the error
	}{
		{"native", App{Exec: "app"}, map[string][]byte{"app": []byte("\x7fELF")}, []string{"app"}, ""},
		{"main without runtime", App{Main: "main.js", Exec: "app"}, nil, nil, `"main" goes with`},
		{"native modules without runtime", App{Exec: "app", Native: []string{"x"}}, nil, nil, `"native" goes with`},
		{"unknown runtime", App{Runtime: "lua", Main: "main.lua"}, nil, nil, "unknown runtime"},
		{"js", App{Runtime: "js", Main: "main.js"}, js, nil, ""},
		{"default main", App{Runtime: "js"}, js, nil, ""},
		{"js with exec", App{Runtime: "js", Main: "main.js", Exec: "app"}, js, nil, `no "exec"`},
		{"main in a directory", App{Runtime: "js", Main: "src/main.js"}, js, nil, "package root"},
		{"main not js", App{Runtime: "js", Main: "main.ts"}, js, nil, "package root"},
		{"main missing", App{Runtime: "js", Main: "app.js"}, js, nil, "not in the package"},
		{"native code", App{Runtime: "js"}, with("helper", []byte("#!")), []string{"helper"}, "no executable file"},
		{"executable module", App{Runtime: "js"}, js, []string{"main.js"}, "no executable file"},
		{"bytecode", App{Runtime: "js"}, with("main.jsc", []byte("KJSC")), nil, "not bytecode"},
		{"hidden machine code", App{Runtime: "js"}, with("assets/data.bin", []byte("\x7fELF\x02\x01")), nil, "machine code goes into lib/"},
		{"undeclared module", App{Runtime: "js"}, with("lib/arm64/x.so", []byte("\x7fELF")), nil, "does not declare"},
		{"other files in lib", App{Runtime: "js", Native: []string{"x"}}, with("lib/arm64/x.txt", nil), nil, "lib/ holds native modules only"},
		{"unknown architecture", App{Runtime: "js", Native: []string{"x"}}, with("lib/riscv64/x.so", nil), nil, "lib/ holds native modules only"},
		{"bad module name", App{Runtime: "js", Native: []string{"../x"}}, js, nil, "not a module name"},
		{"module twice", App{Runtime: "js", Native: []string{"x", "x"}}, js, nil, "twice"},
		{"declared, not there", App{Runtime: "js", Native: []string{"x"}}, js, nil, `native module "x": no lib/<arch>/x.so`},
		{"not a library", App{Runtime: "js", Native: []string{"x"}}, with("lib/arm64/x.so", []byte("text")), nil, "not an ELF library"},
	} {
		err := Check(c.app, files(c.data, c.exec...), FromData(c.data))
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// A package's modules are libraries of their architecture with the
// interface's entry points; the installer needs its own architecture, the
// store the phone's.
func TestCheckModules(t *testing.T) {
	host := HostArch()
	if host == "" {
		t.Skip("modules are built for arm64 and x86_64 only")
	}
	probe := nativetest.Build(t)
	other := nativetest.Other()
	js := map[string][]byte{"manifest.json": []byte("{}"), "main.js": []byte("import")}
	pack := func(mods map[string][]byte) map[string][]byte {
		out := map[string][]byte{}
		for k, v := range js {
			out[k] = v
		}
		for k, v := range mods {
			out[k] = v
		}
		return out
	}
	app := App{Runtime: "js", Native: []string{"probe"}}
	for _, c := range []struct {
		name string
		data map[string][]byte
		exec []string
		need []string
		want string
	}{
		{"host", pack(map[string][]byte{NativePath(host, "probe"): probe}), nil, []string{host}, ""},
		{"both", pack(map[string][]byte{NativePath(host, "probe"): probe, NativePath(other, "probe"): nativetest.Retarget(probe, other)}), nil, []string{ArchARM64, ArchX86_64}, ""},
		{"wrong architecture", pack(map[string][]byte{NativePath(other, "probe"): probe}), nil, nil, "a library for " + host + ", not " + other},
		{"missing for the device", pack(map[string][]byte{NativePath(host, "probe"): probe}), nil, []string{other}, "is not built for " + other},
		{"unknown device", pack(map[string][]byte{NativePath(host, "probe"): probe}), nil, []string{""}, "loads no native modules"},
		{"executable", pack(map[string][]byte{NativePath(host, "probe"): probe}), []string{NativePath(host, "probe")}, nil, "not an executable file"},
		{"no entry points", pack(map[string][]byte{NativePath(host, "probe"): nativetest.Plain(t)}), nil, nil, "not a native module"},
		{"another interface", pack(map[string][]byte{NativePath(host, "probe"): nativetest.Build(t, "PROBE_ABI=0x20000")}), nil, nil, "native interface 2.0"},
		{"a newer minor version", pack(map[string][]byte{NativePath(host, "probe"): nativetest.Build(t, "PROBE_ABI=0x10007")}), nil, nil, "native interface 1.7"},
		{"executable stack", pack(map[string][]byte{NativePath(host, "probe"): nativetest.Build(t, "-Wl,-z,execstack")}), nil, nil, "asks for an executable stack"},
	} {
		err := Check(app, files(c.data, c.exec...), FromData(c.data), c.need...)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// Modules written with kario_native.hpp and the Rust crate are modules
// like the C one.
func TestCheckWrappedModules(t *testing.T) {
	host := HostArch()
	if host == "" {
		t.Skip("modules are built for arm64 and x86_64 only")
	}
	for name, build := range map[string]func(testing.TB, ...string) []byte{"C++": nativetest.BuildCpp, "Rust": func(t testing.TB, _ ...string) []byte { return nativetest.BuildRust(t) }} {
		if err := CheckModule(build(t), host); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// NativeABI and NativeABIMinor follow the header's version.
func TestNativeABI(t *testing.T) {
	raw, err := os.ReadFile("../sdk/c/include/kario_native.h")
	if err != nil {
		t.Fatal(err)
	}
	for part, want := range map[string]int{"MAJOR": NativeABI, "MINOR": NativeABIMinor} {
		m := regexp.MustCompile(`#define KARIO_NATIVE_ABI_` + part + ` (\d+)`).FindSubmatch(raw)
		if m == nil {
			t.Fatalf("no KARIO_NATIVE_ABI_%s in kario_native.h", part)
		}
		if v, _ := strconv.Atoi(string(m[1])); v != want {
			t.Errorf("kario_native.h's %s is %d, the Go side's %d", part, v, want)
		}
	}
}
