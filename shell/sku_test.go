package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSKU(t *testing.T) {
	dir := t.TempDir()
	if s, err := LoadSKU(dir); err != nil || s.ID != "" {
		t.Fatalf("no sku.json: %+v, %v", s, err)
	}
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "sku.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"id":"kario-qwerty-sd662-4g","device":"bar-qwerty","memory_mb":4096,"web_gpu":true,"rotate":90}`)
	s, err := LoadSKU(dir)
	if err != nil || s.MemoryMB != 4096 || !s.WebGPU {
		t.Fatalf("%+v, %v", s, err)
	}
	d, ok := s.SKUDevice()
	if !ok || d.ID != "bar-qwerty" || d.Width != 640 || d.Height != 480 {
		t.Fatalf("device %+v %v", d, ok)
	}
	write(`{"id":"x","device":"flip"}`)
	if _, err := LoadSKU(dir); err == nil {
		t.Fatal("an unknown form factor was accepted")
	}
}
