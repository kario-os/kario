package shell

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
)

// jsrtBin is kario-jsrt as make jsrt builds it, or $KARIO_JSRT.
func jsrtBin(t *testing.T) string {
	p := os.Getenv("KARIO_JSRT")
	if p == "" {
		p, _ = filepath.Abs(filepath.Join("..", "build", "jsrt", jsrt.Program))
	}
	if _, err := os.Stat(p); err != nil {
		if os.Getenv("KARIO_JSRT_REQUIRED") != "" {
			t.Fatalf("no kario-jsrt at %s", p)
		}
		t.Skipf("no kario-jsrt at %s (make jsrt)", p)
	}
	return p
}

// waitBuilt waits until n compiles have finished.
func waitBuilt(t *testing.T, c *Core, n int) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var built int
		_ = c.onLoop(func() error { built = c.jsc.built; return nil })
		if built >= n {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("%d compiles finished, want %d", built, n)
		}
	}
}

// A store TypeScript app is precompiled after installation into the
// system's cache and starts from that bytecode; a cache changed, compiled
// from another version or by another engine, or not the system's file is
// ignored and built again; an update replaces it, an uninstall removes it.
func TestJSCache(t *testing.T) {
	if !alone(t) {
		return
	}
	bin := t.TempDir()
	if err := os.Symlink(jsrtBin(t), filepath.Join(bin, jsrt.Program)); err != nil {
		t.Fatal(err)
	}
	c := loopCore(t, bin)
	src, err := os.ReadFile(filepath.Join("..", "jsrt", "testdata", "app", "main.js"))
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(t.TempDir(), "devstore")
	files := func(version, main string) map[string]string {
		return map[string]string{
			"manifest.json": `{"id": "com.example.jsc", "name": "JSC", "runtime": "js", "version": "` + version + `"}`,
			"main.js":       main,
		}
	}
	install := func(raw []byte) {
		t.Helper()
		if err := c.onLoop(func() error {
			p, err := pkg.Open(raw, c.storeRoots())
			if err != nil {
				return err
			}
			m, err := c.checkPackage(p)
			if err != nil {
				return err
			}
			return c.unpack(p, m, c.appByID(m.ID))
		}); err != nil {
			t.Fatal(err)
		}
	}
	check := func() (string, error) {
		var path string
		err := c.onLoop(func() error {
			var err error
			path, err = c.checkJSCache(c.appByID("com.example.jsc"))
			return err
		})
		return path, err
	}
	raw, key := devPackage(t, store, files("1.0", string(src)))
	_ = c.onLoop(func() error {
		c.settings.DeveloperMode = true
		_, err := c.devTrust(key)
		return err
	})
	install(raw)
	waitBuilt(t, c, 1)
	path, err := check()
	if err != nil {
		t.Fatalf("no bytecode after installation: %v", err)
	}
	if !strings.HasPrefix(path, filepath.Join(c.cfg.StateDir, jsCacheDir)) {
		t.Fatalf("bytecode at %s, outside the system's cache", path)
	}
	good, _ := os.ReadFile(path)
	if !bytes.HasPrefix(good, []byte("KJSC")) {
		t.Fatal("not bytecode")
	}
	st, _ := os.Stat(filepath.Dir(path))
	if st.Mode().Perm() != 0o711 {
		t.Fatalf("cache directory %v", st.Mode().Perm())
	}

	// It is what the app starts with: started once (and verified), its
	// source broken, it still shows its page.
	var args []string
	_ = c.onLoop(func() error {
		a := c.appByID("com.example.jsc")
		cmd, err := c.appCommand(a, t.TempDir())
		if err == nil {
			args = cmd.Args
		}
		c.Launch(a, "")
		return err
	})
	if !slices.Contains(args, jsrt.BytecodeFlag) || !slices.Contains(args, path) {
		t.Fatalf("starts as %v", args)
	}
	waitPage := func() {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			var shown bool
			_ = c.onLoop(func() error { a := c.appByID("com.example.jsc"); shown = a.ready && len(a.stack) > 0; return nil })
			if shown {
				return
			}
			if time.Now().After(end) {
				log, _ := os.ReadFile(filepath.Join(c.cfg.RunDir, "com.example.jsc.log"))
				t.Fatalf("no page; log:\n%s", log)
			}
		}
	}
	waitPage()
	_ = c.onLoop(func() error { c.kill(c.appByID("com.example.jsc")); return nil })
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		var running bool
		_ = c.onLoop(func() error { running = c.appByID("com.example.jsc").running; return nil })
		if !running {
			break
		}
	}
	mainPath := filepath.Join(c.packagesDir(), "com.example.jsc", "main.js")
	_ = os.WriteFile(mainPath, []byte("this is not JavaScript ("), 0o644)
	_ = c.onLoop(func() error { c.Launch(c.appByID("com.example.jsc"), ""); return nil })
	waitPage()
	_ = os.WriteFile(mainPath, src, 0o644)

	built := 1
	for _, bad := range []struct {
		name  string
		spoil func()
		why   string
	}{
		{"a changed byte", func() {
			b := bytes.Clone(good)
			b[len(b)/2] ^= 1
			_ = os.WriteFile(path, b, 0o644)
		}, "changed since it was compiled"},
		{"another version's", func() {
			recPath := filepath.Join(filepath.Dir(path), jsCacheRecord)
			raw, _ := os.ReadFile(recPath)
			var rec jsCacheRec
			_ = json.Unmarshal(raw, &rec)
			rec.Source = strings.Repeat("0", 64)
			raw, _ = json.Marshal(rec)
			_ = os.WriteFile(recPath, raw, 0o644)
		}, "another version"},
		{"without every statement's position", func() {
			recPath := filepath.Join(filepath.Dir(path), jsCacheRecord)
			raw, _ := os.ReadFile(recPath)
			var rec jsCacheRec
			_ = json.Unmarshal(raw, &rec)
			rec.Positions = false
			raw, _ = json.Marshal(rec)
			_ = os.WriteFile(recPath, raw, 0o644)
		}, "every statement's position"},
		{"writable by others", func() { _ = os.Chmod(path, 0o666) }, "only the system writes"},
		{"a link", func() {
			other := filepath.Join(t.TempDir(), "x.jsc")
			_ = os.WriteFile(other, good, 0o644)
			_ = os.Remove(path)
			_ = os.Symlink(other, path)
		}, "only the system writes"},
	} {
		bad.spoil()
		if _, err := check(); err == nil || !strings.Contains(err.Error(), bad.why) {
			t.Fatalf("%s: %v, want %q", bad.name, err, bad.why)
		}
		var args []string
		_ = c.onLoop(func() error { args, _ = c.withBytecode(c.appByID("com.example.jsc"), nil); return nil })
		if len(args) != 0 {
			t.Fatalf("%s: starts with %v", bad.name, args)
		}
		built++
		waitBuilt(t, c, built)
		if _, err := check(); err != nil {
			t.Fatalf("%s: not built again: %v", bad.name, err)
		}
		if again, _ := os.ReadFile(path); !bytes.Equal(again, good) {
			t.Fatalf("%s: built again differently", bad.name)
		}
	}
	_ = c.onLoop(func() error {
		c.jsc.engine = "kario-jsrt (QuickJS-ng 0.0.1)"
		defer func() { c.jsc.engine = ""; c.jsc.probed = false }()
		if _, err := c.checkJSCache(c.appByID("com.example.jsc")); err == nil || !strings.Contains(err.Error(), "compiled by") {
			t.Errorf("another engine's bytecode: %v", err)
		}
		return nil
	})

	recOf := func() jsCacheRec {
		raw, _ := os.ReadFile(filepath.Join(c.jsCachePath("com.example.jsc"), jsCacheRecord))
		var rec jsCacheRec
		_ = json.Unmarshal(raw, &rec)
		return rec
	}
	before := recOf()
	raw, _ = devPackage(t, store, files("1.1", string(src)+"\n// 1.1\n"))
	install(raw)
	// The update drops the old bytecode at once and compiles the new
	// version in the background, which may already have finished.
	if _, err := check(); err == nil && recOf().Source == before.Source {
		t.Fatal("the old version's bytecode outlived the update")
	}
	built++
	waitBuilt(t, c, built)
	if _, err := check(); err != nil {
		t.Fatalf("no bytecode after the update: %v", err)
	}
	if after := recOf(); after.Source == before.Source {
		t.Fatal("the update's bytecode has the old module's digest")
	}

	_ = c.onLoop(func() error { c.removeApp(c.appByID("com.example.jsc")); return nil })
	if _, err := os.Stat(c.jsCachePath("com.example.jsc")); !os.IsNotExist(err) {
		t.Fatalf("bytecode left after uninstalling: %v", err)
	}
}
