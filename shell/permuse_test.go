package shell

import (
	"testing"

	"github.com/kario-os/kario/ui"
)

func TestPermissionUsage(t *testing.T) {
	c := &Core{cfg: Config{StateDir: t.TempDir()}}
	b, err := newBroker(c)
	if err != nil {
		t.Fatal(err)
	}
	c.broker = b
	b.used("com.example.a", []string{ui.PermCamera, ui.PermNet})
	b.used("com.example.a", []string{ui.PermCamera})
	b.used("com.example.a/mini", []string{ui.PermLocation})
	if e := b.entry("com.example.a", "A", ui.PermCamera, permLabel(ui.PermCamera), "granted"); e.Uses != 2 || e.LastUsed == 0 {
		t.Fatalf("camera: %+v", e)
	}
	if _, ok := b.usage["com.example.a"][ui.PermNet]; ok {
		t.Fatal("an install-time permission was recorded")
	}
	again, _ := newBroker(c)
	if e := again.entry("com.example.a/mini", "A mini", ui.PermLocation, permLabel(ui.PermLocation), "granted"); e.Uses != 1 {
		t.Fatalf("not kept across a restart: %+v", e)
	}
	again.forgetUsage("com.example.a")
	if len(again.usage) != 0 {
		t.Fatalf("usage left after uninstall: %v", again.usage)
	}
}
