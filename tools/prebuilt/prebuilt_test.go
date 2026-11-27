package prebuilt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// tree writes files under a new root, each with its age.
func tree(t *testing.T, files map[string]time.Duration) string {
	t.Helper()
	root := t.TempDir()
	now := time.Now()
	for f, age := range files {
		p := filepath.Join(root, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// A renderer from before its sources changed (a tree updated, the
// program not built again) is stale; the newest source is named.
func TestPaintStale(t *testing.T) {
	bin := "paint/target/release/phone-paint"
	files := map[string]time.Duration{
		bin:                     time.Hour,
		"paint/src/main.rs":     2 * time.Hour,
		"paint/locale/es.json":  3 * time.Hour,
		"paint/Cargo.toml":      time.Minute,
		"paint/Cargo.lock":      time.Minute,
		"paint/src/README.txt":  time.Minute,
		"paint/target/x/new.rs": time.Minute,
	}
	root := tree(t, files)
	check := func() string { return stale(root, filepath.Join(root, bin), paintSource, "paint/src", "paint/locale") }
	if why := check(); why != "" {
		t.Fatalf("built after its sources: %q", why)
	}
	for _, src := range []string{"paint/locale/es.json", "paint/src/main.rs"} {
		_ = os.Chtimes(filepath.Join(root, src), time.Now(), time.Now())
		if why, want := check(), bin+" is older than "+src; why != want {
			t.Fatalf("%s newer: %q, want %q", src, why, want)
		}
	}
	_ = os.Remove(filepath.Join(root, bin))
	if why := check(); why != "no "+bin {
		t.Fatalf("missing: %q", why)
	}
}

// The staged system goes stale with the programs' Go sources, the module
// files and the staging script; not with tests, test data, the vendored
// modules or build output.
func TestPrefixStale(t *testing.T) {
	bin := "build/usr/bin/kario-system"
	old := map[string]time.Duration{
		bin:                         time.Hour,
		"shell/core.go":             2 * time.Hour,
		"shell/core_test.go":        time.Minute,
		"shell/testdata/fake.go":    time.Minute,
		"vendor/x/y.go":             time.Minute,
		"build/bin/kario-system.go": time.Minute,
		".git/hooks/x.go":           time.Minute,
		"_old/x.go":                 time.Minute,
		"docs/13-prototype.md":      time.Minute,
		"paint/src/main.rs":         time.Minute,
	}
	root := tree(t, old)
	if why := stale(root, filepath.Join(root, bin), goSource, "."); why != "" {
		t.Fatalf("staged after its sources: %q", why)
	}
	for _, src := range []string{"go.sum", "scripts/stage.sh", "apps/settings/main.go"} {
		root := tree(t, map[string]time.Duration{bin: time.Hour, src: time.Minute})
		if why, want := stale(root, filepath.Join(root, bin), goSource, "."), bin+" is older than "+src; why != want {
			t.Fatalf("%q, want %q", why, want)
		}
	}
}

type fakeTB struct {
	testing.TB
	skipped, failed string
}

func (f *fakeTB) Helper() {}

func (f *fakeTB) Skipf(format string, args ...any) {
	f.skipped = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func (f *fakeTB) Fatalf(format string, args ...any) {
	f.failed = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// A stale or missing program skips the test, and fails it where make test
// built the programs first and says so.
func TestNeed(t *testing.T) {
	run := func(required, why string) *fakeTB {
		t.Setenv(RequiredEnv, required)
		f := &fakeTB{TB: t}
		done := make(chan struct{})
		go func() {
			defer close(done)
			need(f, "make paint", why)
		}()
		<-done
		return f
	}
	if f := run("", ""); f.skipped != "" || f.failed != "" {
		t.Fatalf("up to date: %+v", f)
	}
	why := "paint/target/release/phone-paint is older than paint/src/touch.rs"
	if f := run("", why); f.skipped != why+" (make paint)" || f.failed != "" {
		t.Fatalf("stale: %+v", f)
	}
	if f := run("1", why); f.failed != why+" (make paint)" || f.skipped != "" {
		t.Fatalf("stale under make test: %+v", f)
	}
	if f := run("1", ""); f.skipped != "" || f.failed != "" {
		t.Fatalf("up to date under make test: %+v", f)
	}
}

func TestRoot(t *testing.T) {
	for _, f := range []string{"go.mod", "paint/Cargo.toml", "scripts/stage.sh"} {
		if _, err := os.Stat(filepath.Join(Root(), f)); err != nil {
			t.Error(err)
		}
	}
}
