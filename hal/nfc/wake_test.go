package nfc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/hal"
)

func TestWakeSource(t *testing.T) {
	root := t.TempDir()
	saved := hal.SysRoot
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = saved })
	// /dev/null stands in for the controller's node (character device 1:3).
	if err := WakeSource("/dev/null"); err == nil {
		t.Fatal("a device without the attribute became a wake-up source")
	}
	attr := filepath.Join(root, "dev", "char", "1:3", "device", "power", "wakeup")
	if err := os.MkdirAll(filepath.Dir(attr), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attr, []byte("disabled\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WakeSource("/dev/null"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(attr); string(raw) != "enabled" {
		t.Fatalf("power/wakeup is %q", raw)
	}
	if err := WakeSource(t.TempDir()); err == nil {
		t.Fatal("a directory became a wake-up source")
	}
}
