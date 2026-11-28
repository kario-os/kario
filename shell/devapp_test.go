package shell

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kario-os/kario/ui"
)

// The developer's app (kario run) loads after the system's, takes the place
// of the one with its id, and starts under the runner; the others do not.
func TestDevApp(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	apps, dev := filepath.Join(root, "apps"), filepath.Join(root, "work", "calc", "build")
	write(filepath.Join(apps, "calc", "manifest.json"), `{"id":"com.phone.calc","name":"Calculator","exec":"phone-app-calc"}`)
	write(filepath.Join(apps, "clock", "manifest.json"), `{"id":"com.phone.clock","name":"Clock","exec":"phone-app-clock"}`)
	write(filepath.Join(dev, "manifest.json"), `{"id":"com.phone.calc","name":"Calculator","runtime":"js","main":"main.js"}`)
	runner := []string{"node", "--inspect=127.0.0.1:9229", "/rt/run.mjs"}
	c := &Core{cfg: Config{AppsDir: apps, AppBinDir: "/usr/lib/phone/bin", StateDir: root, DevApp: dev + "/", DevRunner: runner},
		log: log.New(io.Discard, "", 0), profile: &Profile{}}
	if err := c.loadManifests(); err != nil {
		t.Fatal(err)
	}
	var calc, clock *App
	n := 0
	for _, a := range c.apps {
		switch a.M.ID {
		case "com.phone.calc":
			calc, n = a, n+1
		case "com.phone.clock":
			clock = a
		}
	}
	if n != 1 || calc == nil || calc.M.dir != dev || clock == nil {
		t.Fatalf("apps %d, calc %+v", n, calc)
	}
	cmd := c.devCommand(calc)
	if cmd == nil || !slices.Equal(cmd.Args, append(slices.Clone(runner), filepath.Join(dev, "main.js"))) {
		t.Fatalf("dev command %v", cmd)
	}
	if c.devCommand(clock) != nil {
		t.Fatal("a system app under the developer's runner")
	}
	c.cfg.DevRunner = nil
	if c.devCommand(calc) != nil {
		t.Fatal("a runner without --dev-runner")
	}
	// kario preview: a page shown as the developer's app's, its process not started.
	if err := c.ShowDevPage(ui.Screen{ID: "main", Title: "Preview", Body: []ui.Node{{Kind: ui.KindText, Text: "sample"}}}); err != nil {
		t.Fatal(err)
	}
	if c.current() != calc || calc.running || !calc.stale || calc.top().S.Title != "Preview" {
		t.Fatalf("shown: fg %v running %v stale %v", c.current() == calc, calc.running, calc.stale)
	}
	c.cfg.DevApp = ""
	if err := c.ShowDevPage(ui.Screen{ID: "main"}); err == nil {
		t.Fatal("a page shown without a developer's app")
	}
}

// The developer's app written in C++ (kario run of a native app) runs its
// own program, though the system has one of the same name.
func TestDevAppProgram(t *testing.T) {
	root := t.TempDir()
	bin, dev := filepath.Join(root, "lib"), filepath.Join(root, "work", "hello")
	for p, s := range map[string]string{
		filepath.Join(root, "apps", "hellocpp", "manifest.json"): `{"id":"com.phone.hellocpp","name":"C++","exec":"phone-app-hellocpp"}`,
		filepath.Join(dev, "manifest.json"):                      `{"id":"com.example.hello","name":"Hello","exec":"phone-app-hellocpp"}`,
		filepath.Join(bin, "phone-app-hellocpp"):                 "system",
		filepath.Join(dev, "phone-app-hellocpp"):                 "mine",
	} {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := &Core{cfg: Config{AppsDir: filepath.Join(root, "apps"), AppBinDir: bin, StateDir: root, DevApp: dev},
		log: log.New(io.Discard, "", 0), profile: &Profile{}}
	if err := c.loadManifests(); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range c.apps {
		got[a.M.ID] = c.appExec(a)
	}
	if got["com.example.hello"] != filepath.Join(dev, "phone-app-hellocpp") || got["com.phone.hellocpp"] != filepath.Join(bin, "phone-app-hellocpp") {
		t.Fatalf("programs %v", got)
	}
	c.cfg.DevRunner = []string{"gdbserver", ":1234"}
	for _, a := range c.apps {
		if cmd := c.devCommand(a); cmd != nil && !slices.Equal(cmd.Args, []string{"gdbserver", ":1234", filepath.Join(dev, "phone-app-hellocpp")}) {
			t.Fatalf("under a runner %v", cmd.Args)
		}
	}
}
