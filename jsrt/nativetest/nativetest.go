// Package nativetest builds the native module the tests load
// (jsrt/testdata/native/probe.c) with this machine's C compiler ($CC, else
// cc), for the packages whose tests check native modules: jsrt, the kario
// command, the store's review, the installer; and the library that slows a
// runtime's fsync (jsrt/testdata/slowfs) for the tests of its storage.
package nativetest

import (
	"debug/elf"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var (
	mu    sync.Mutex
	built = map[string][]byte{}
)

// Dir is jsrt/testdata/native, the probe's sources.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "testdata", "native")
}

// Compiler is the C compiler, or skips the test without one (fails it when
// KARIO_JSRT_REQUIRED says the runtime's tests must run).
func Compiler(t testing.TB) string {
	cc := os.Getenv("CC")
	if cc == "" {
		cc = "cc"
	}
	p, err := exec.LookPath(cc)
	if err != nil {
		if os.Getenv("KARIO_JSRT_REQUIRED") != "" {
			t.Fatalf("no C compiler (%s) to build native modules", cc)
		}
		t.Skipf("no C compiler (%s) to build native modules", cc)
	}
	return p
}

// Build compiles probe.c into a shared library for this machine, with
// debug information (kario symbolize reads it); defs are -D options
// (PROBE_ABI=0x20000, PROBE_INIT_FAILS), or compiler options when they
// start with "-" (-Wl,-z,execstack). Each variant is built once a test
// binary.
func Build(t testing.TB, defs ...string) []byte {
	t.Helper()
	return compile(t, Compiler(t), "probe.c", nil, defs)
}

// BuildCpp compiles probe.cpp, the same module written with
// kario_native.hpp, with this machine's C++ compiler ($CXX, else c++).
func BuildCpp(t testing.TB, defs ...string) []byte {
	t.Helper()
	return compile(t, tool(t, "CXX", "c++"), "probe.cpp", []string{"-std=c++17"}, defs)
}

// Include is the SDK's headers (kario_native.h, kario_native.hpp).
func Include() string { return filepath.Join(Dir(), "..", "..", "..", "sdk", "c", "include") }

func compile(t testing.TB, cc, src string, extra, defs []string) []byte {
	t.Helper()
	key := src + " " + strings.Join(defs, " ")
	mu.Lock()
	defer mu.Unlock()
	if raw, ok := built[key]; ok {
		return raw
	}
	out := filepath.Join(t.TempDir(), "probe.so")
	args := append([]string{"-shared", "-fPIC", "-O2", "-g", "-pthread", "-Wl,--build-id", "-I" + Include()}, extra...)
	for _, d := range defs {
		if strings.HasPrefix(d, "-") {
			args = append(args, d)
		} else {
			args = append(args, "-D"+d)
		}
	}
	args = append(args, "-o", out, filepath.Join(Dir(), src))
	if msg, err := exec.Command(cc, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", cc, strings.Join(args, " "), err, msg)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	built[key] = raw
	return raw
}

// Crate is sdk/rust/kario-native, the Rust crate of native modules.
func Crate() string { return filepath.Join(Dir(), "..", "..", "..", "sdk", "rust", "kario-native") }

// BuildRust builds the crate's example module (examples/probe.rs, the
// probe in Rust) with cargo, offline and from its lockfile.
func BuildRust(t testing.TB) []byte {
	t.Helper()
	cargo := tool(t, "CARGO", "cargo")
	mu.Lock()
	defer mu.Unlock()
	if raw, ok := built["probe.rs"]; ok {
		return raw
	}
	target := t.TempDir()
	cmd := exec.Command(cargo, "build", "--release", "--locked", "--offline", "--example", "probe", "--manifest-path", filepath.Join(Crate(), "Cargo.toml"))
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target, "RUSTFLAGS=-C link-arg=-Wl,--build-id -g")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo build: %v\n%s", err, msg)
	}
	raw, err := os.ReadFile(filepath.Join(target, "release", "examples", "libprobe.so"))
	if err != nil {
		t.Fatal(err)
	}
	built["probe.rs"] = raw
	return raw
}

// tool is $env or name on PATH, or skips the test (fails it under
// KARIO_JSRT_REQUIRED) without it.
func tool(t testing.TB, env, name string) string {
	if v := os.Getenv(env); v != "" {
		name = v
	}
	p, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv("KARIO_JSRT_REQUIRED") != "" {
			t.Fatalf("no %s to build native modules", name)
		}
		t.Skipf("no %s to build native modules", name)
	}
	return p
}

// SlowFS is jsrt/testdata/slowfs built for this machine, written into a
// directory of the test's: the library to preload (LD_PRELOAD) into a
// runtime whose fsync should take SLOWFS_DELAY_MS longer, each call
// appending the thread that made it ("main" or "worker") to SLOWFS_LOG.
func SlowFS(t testing.TB) string {
	t.Helper()
	cc := Compiler(t)
	mu.Lock()
	defer mu.Unlock()
	raw, ok := built["slowfs.c"]
	if !ok {
		out := filepath.Join(t.TempDir(), "slowfs.so")
		src := filepath.Join(Dir(), "..", "slowfs", "slowfs.c")
		if msg, err := exec.Command(cc, "-shared", "-fPIC", "-O2", "-o", out, src).CombinedOutput(); err != nil {
			t.Fatalf("%s %s: %v\n%s", cc, src, err, msg)
		}
		var err error
		if raw, err = os.ReadFile(out); err != nil {
			t.Fatal(err)
		}
		built["slowfs.c"] = raw
	}
	lib := filepath.Join(t.TempDir(), "slowfs.so")
	if err := os.WriteFile(lib, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return lib
}

// Plain is a shared library without the module's entry points.
func Plain(t testing.TB) []byte {
	t.Helper()
	cc := Compiler(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "plain.c")
	if err := os.WriteFile(src, []byte("int plain(void) { return 1; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "plain.so")
	if msg, err := exec.Command(cc, "-shared", "-fPIC", "-o", out, src).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", cc, err, msg)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Retarget is raw with the ELF header claiming another machine (arm64 or
// x86_64): what a library built for the other architecture looks like to
// the checks, which read headers and symbols, not instructions.
func Retarget(raw []byte, arch string) []byte {
	out := append([]byte(nil), raw...)
	m := elf.EM_X86_64
	if arch == "arm64" {
		m = elf.EM_AARCH64
	}
	binary.LittleEndian.PutUint16(out[18:], uint16(m))
	return out
}

// Other is the architecture modules are built for that this machine is not.
func Other() string {
	if runtime.GOARCH == "arm64" {
		return "x86_64"
	}
	return "arm64"
}
