package shell

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"testing"
)

// A simulated SKU keeps its hardware but not its id, so the developer
// machine's Shell never powers the computer off.
func TestSimulatedSKUIsNotThePhone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sku.json")
	if err := os.WriteFile(path, []byte(`{"id":"qemu-t9","device":"bar-t9","memory_mb":2048}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := SimulatedSKU(path)
	if err != nil || s.ID != "" || s.MemoryMB != 2048 {
		t.Fatalf("%+v, %v", s, err)
	}
	if d, ok := s.SKUDevice(); !ok || d.ID != "bar-t9" {
		t.Fatalf("device %+v %v", d, ok)
	}
}

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
	if _, err := LoadSKUFile(filepath.Join(dir, "none.json")); err == nil {
		t.Fatal("a named sku.json that is not there")
	}
	write(`{"device":"bar-t9","memory_mb":1024,"fn":{"w":"1"}}`)
	if s, err := LoadSKUFile(filepath.Join(dir, "sku.json")); err != nil || s.ID != "" || s.MemoryMB != 1024 || s.Fn["w"] != "1" {
		t.Fatalf("a simulated SKU: %+v, %v", s, err)
	}
}

// A whole phone's SKU (24-sku-framework.md §7.4) gives its panel as drawn
// and the scale: the form factor keeps its keypad, the layout its logical
// width, documents their columns.
func TestWholePhoneSKU(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sku.json")
	load := func(body string) (SKU, error) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return LoadSKUFile(path)
	}
	s, err := load(`{"id":"oneplus-fajita-t9","device":"bar-t9","width":1080,"height":2180,"scale":4.5,"keypad":"hid","modem":"modemmanager"}`)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := s.SKUDevice()
	t9, _ := DeviceByID("bar-t9")
	if !ok || d.Keypad != "t9" || d.Width != 1080 || d.Height != 2180 || d.Scale != 4.5 || d.DocCols != t9.DocCols {
		t.Fatalf("device %+v", d)
	}
	if got, ok := DeviceByID("bar-t9:1080x2180@4.5"); !ok || got.Width != d.Width || got.Height != d.Height || got.Scale != d.Scale || got.DocCols != d.DocCols {
		t.Fatalf("--device bar-t9:1080x2180@4.5 = %+v %v, the SKU's %+v", got, ok, d)
	}
	// A landscape form factor on a panel turned sideways, wider than its own.
	s, err = load(`{"id":"x","device":"bar-qwerty","width":1920,"height":1080,"scale":4.5,"rotate":90}`)
	if d, _ := s.SKUDevice(); err != nil || d.Keypad != "qwerty" || d.DocCols != 57 {
		t.Fatalf("turned: %+v %v", d, err)
	}
	for _, bad := range []string{
		`{"device":"bar-t9","width":1080,"height":2180,"scale":0.5}`,
		`{"device":"bar-t9","width":1080,"height":2180,"scale":9}`,
		`{"device":"bar-t9","width":1080,"scale":4.5}`,
		`{"device":"bar-t9","width":-1,"height":2180}`,
	} {
		if _, err := load(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	for _, bad := range []string{"bar-t9@x", "bar-t9@0.5", "bar-t9:0x10", "bar-t9:1080x2180@"} {
		if d, ok := DeviceByID(bad); ok {
			t.Errorf("--device %s = %+v", bad, d)
		}
	}
}

// A whole phone with a keypad of its own (a KaiOS phone's matrix) names
// its codes; the Shell checks them and gives the renderer the codes of the
// keys they are.
func TestBoardKeymap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sku.json")
	load := func(body string) (SKU, error) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return LoadSKUFile(path)
	}
	// KEY_MENU, KEY_BACK, KEY_SEND, KEY_POWER (the red key is the power
	// key), KEY_1, KEY_OK.
	s, err := load(`{"id":"x","device":"bar-t9","keypad":"builtin","keymap":{"139":"soft-left","158":"soft-right","231":"call","116":"end","2":"1","352":"ok"}}`)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := s.SKUDevice()
	want := map[int]int{139: 59, 158: 60, 231: 63, 116: 62, 2: 513, 352: 28}
	if !ok || !maps.Equal(d.Keymap, want) {
		t.Fatalf("remap %v, want %v", d.Keymap, want)
	}
	for _, bad := range []string{
		`{"device":"bar-t9","keymap":{"139":"menu"}}`,
		`{"device":"bar-t9","keymap":{"0":"ok"}}`,
		`{"device":"bar-t9","keymap":{"768":"ok"}}`,
		`{"device":"bar-t9","keymap":{"KEY_MENU":"ok"}}`,
		`{"device":"bar-t9","keymap":{"0139":"ok"}}`,
	} {
		if _, err := load(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	// Without one (a HID keyboard and the phone's buttons) nothing is
	// remapped.
	s, _ = load(`{"id":"oneplus-fajita-t9","device":"bar-t9","width":1080,"height":2180,"scale":4.5,"keypad":"hid"}`)
	if d, _ := s.SKUDevice(); d.Keymap != nil {
		t.Fatalf("remap %v without a keymap", d.Keymap)
	}
}

// The renderer learns the keymap in the handshake, whatever form factor
// the Shell draws.
func TestKeymapHandshake(t *testing.T) {
	apps := t.TempDir()
	_ = os.MkdirAll(filepath.Join(apps, "x"), 0o755)
	_ = os.WriteFile(filepath.Join(apps, "x", "manifest.json"), []byte(`{"id": "com.test.x", "name": "x", "exec": "true"}`), 0o644)
	c, err := New(Config{W: 40, H: 24, AppsDir: apps, RunDir: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Shutdown()
	dir := t.TempDir()
	hello, renderer := filepath.Join(dir, "hello.json"), filepath.Join(dir, "renderer")
	script := "#!/bin/sh\nread hello\necho \"$hello\" > " + hello + "\necho '{\"t\":\"ready\",\"proto\":\"phone.frame/1\",\"renderer\":{\"name\":\"x\"},\"features\":[\"snap\"]}'\ncat >/dev/null\n"
	if err := os.WriteFile(renderer, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sku := SKU{ID: "x", Device: "bar-t9", Keymap: map[string]string{"139": "soft-left", "2": "1"}}
	dev, _ := sku.SKUDevice()
	g, err := StartGUI(c, renderer, dev)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	var got struct {
		Device struct {
			Keymap map[string]int `json:"keymap"`
		} `json:"device"`
	}
	raw, _ := os.ReadFile(hello)
	if err := json.Unmarshal(raw, &got); err != nil || !maps.Equal(got.Device.Keymap, map[string]int{"139": 59, "2": 513}) {
		t.Fatalf("hello %s: %v", raw, err)
	}
}
