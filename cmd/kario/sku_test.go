package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/shell"
)

// The SKUs kario carries are the generator's, each a form factor the Shell
// knows, with the screen and keypad of that form factor.
func TestSKUs(t *testing.T) {
	all, err := loadSKUs()
	if err != nil || len(all) == 0 {
		t.Fatalf("%d SKUs: %v", len(all), err)
	}
	for _, s := range all {
		d, ok := shell.DeviceByID(s.info.Device)
		if !ok || d.Width != s.Width || d.Height != s.Height || s.info.MemoryMB < 1024 {
			t.Errorf("%s: %+v, the Shell's %+v", s.ID, s.entry(), d)
		}
	}
	s, err := skuByID("qemu-t9-1g")
	if err != nil || s.info.Device != "bar-t9" || s.Form != "t9" || s.info.MemoryMB != 1024 {
		t.Fatalf("qemu-t9-1g: %+v %v", s.entry(), err)
	}
	if _, err := skuByID("nokia-3310"); err == nil || !strings.Contains(err.Error(), "qemu-t9") {
		t.Fatalf("an unknown SKU: %v", err)
	}
}

// -sku hands the Shell a sku.json it reads as the image's own, without the
// id that would make it treat this machine as a phone.
func TestRunSKU(t *testing.T) {
	s, err := skuByID("kario-qwerty-sd662-4g")
	if err != nil {
		t.Fatal(err)
	}
	p, err := writeSKU(s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := shell.LoadSKUFile(p)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := got.SKUDevice()
	if got.ID != "" || got.MemoryMB != 4096 || got.Region != "cn" || !ok || d.ID != "bar-qwerty" || d.Fn["w"] != "1" {
		t.Fatalf("sku.json %+v, device %+v", got, d)
	}
	args := shellArgs("/w/a/build", nil, "", runOptions{skuFile: p})
	if strings.Join(args, " ") != "--dev-app /w/a/build --sku "+p {
		t.Fatalf("args %q", args)
	}
	dir := newTestProject(t)
	t.Setenv("KARIO_SHELL", filepath.Join(t.TempDir(), "none"))
	if err := runApp(dir, runOptions{engine: "node", sku: "kario-qwerty-sd662-4g", device: "bar-t9"}); err == nil || !strings.Contains(err.Error(), "give one") {
		t.Fatalf("-sku with another -device: %v", err)
	}
	if err := runApp(dir, runOptions{engine: "node", sku: "x"}); err == nil || !strings.Contains(err.Error(), "not a SKU") {
		t.Fatalf("-sku x: %v", err)
	}
}

// kario skus -json is what Kario Studio's picker lists.
func TestListSKUsJSON(t *testing.T) {
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	err := listSKUs(true)
	os.Stdout = stdout
	w.Close()
	var got []skuEntry
	if err != nil || json.NewDecoder(r).Decode(&got) != nil || len(got) < 2 {
		t.Fatalf("%v %+v", err, got)
	}
	for _, e := range got {
		if e.ID == "qemu-qwerty" && (e.Device != "bar-qwerty" || e.Width != 640 || !e.Emulator) {
			t.Fatalf("%+v", e)
		}
	}
}
