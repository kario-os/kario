package shell

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/keystore"
)

func TestRendererOverride(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "renderer")
	if err := os.WriteFile(own, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if RendererOverride(dir) != "" {
		t.Fatal("the owner's renderer is used on a locked device")
	}
	ks, err := keystore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ks.SetUnlocked(true); err != nil {
		t.Fatal(err)
	}
	if RendererOverride(dir) != own {
		t.Fatal("the owner's renderer is not used on an unlocked device")
	}
	_ = os.Chmod(own, 0o644)
	if RendererOverride(dir) != "" {
		t.Fatal("a file that cannot run is used as the renderer")
	}
}
