// Package prebuilt finds, for the tests that run them, the programs make
// builds in this tree: phone-paint (make paint) and the staged system
// (make build's build/usr). A test skips when the program is missing or
// older than a file it is built from, so a tree changed since the last
// build is not tested against the old program. make test builds both
// before the Go tests and sets RequiredEnv: there such a test fails
// instead of skipping.
package prebuilt

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// RequiredEnv, when set, fails a test whose program is missing or stale.
const RequiredEnv = "KARIO_PREBUILT_REQUIRED"

// Root is the tree this package is in.
func Root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// Paint is phone-paint as make paint builds it. It is checked against the
// crate's Rust files and the catalogs it embeds: cargo does not relink for
// a Cargo.toml or Cargo.lock that is only newer, which would leave the
// program stale however often it is built.
func Paint(t testing.TB) string {
	t.Helper()
	root := Root()
	bin := filepath.Join(root, "paint", "target", "release", "phone-paint")
	need(t, "make paint", stale(root, bin, paintSource, "paint/src", "paint/locale"))
	return bin
}

// Prefix is make build's staged system. scripts/stage.sh installs
// bin/kario-system anew on every make build, which makes it as old as the
// prefix; it is checked against the module's Go files outside the tests,
// go.mod, go.sum and stage.sh.
func Prefix(t testing.TB) string {
	t.Helper()
	root := Root()
	usr := filepath.Join(root, "build", "usr")
	need(t, "make build", stale(root, filepath.Join(usr, "bin", "kario-system"), goSource, "."))
	return usr
}

func paintSource(rel string) bool {
	return strings.HasPrefix(rel, "paint/locale/") || strings.HasPrefix(rel, "paint/src/") && strings.HasSuffix(rel, ".rs")
}

func goSource(rel string) bool {
	switch rel {
	case "go.mod", "go.sum", "scripts/stage.sh":
		return true
	}
	return strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go")
}

// skipDir: what holds no sources of the programs (build output, the
// vendored modules, test data), and what the go command ignores.
func skipDir(name string) bool {
	switch name {
	case "build", "vendor", "target", "testdata", "node_modules":
		return true
	}
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// stale says why bin cannot stand for its sources, the files under dirs
// (relative to root) that source accepts: it is missing, or older than the
// newest of them, which it names; "" when it is not.
func stale(root, bin string, source func(rel string) bool, dirs ...string) string {
	rel := func(p string) string {
		if r, err := filepath.Rel(root, p); err == nil {
			return filepath.ToSlash(r)
		}
		return p
	}
	fi, err := os.Stat(bin)
	if err != nil {
		return "no " + rel(bin)
	}
	var newest string
	var at time.Time
	for _, d := range dirs {
		_ = filepath.WalkDir(filepath.Join(root, d), func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if e.IsDir() {
				if p != filepath.Join(root, d) && skipDir(e.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !e.Type().IsRegular() || !source(rel(p)) {
				return nil
			}
			if i, err := e.Info(); err == nil && i.ModTime().After(at) {
				newest, at = rel(p), i.ModTime()
			}
			return nil
		})
	}
	if at.After(fi.ModTime()) {
		return rel(bin) + " is older than " + newest
	}
	return ""
}

func need(t testing.TB, build, why string) {
	t.Helper()
	if why == "" {
		return
	}
	if os.Getenv(RequiredEnv) != "" {
		t.Fatalf("%s (%s)", why, build)
	}
	t.Skipf("%s (%s)", why, build)
}
