package shell

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
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

// The board's GPU policy for the pixel renderer (tools/sku.py writes it):
// the path by default and the GPU's render node reach the device, also a
// form factor chosen on the command line; anything else is refused.
func TestPaintGPU(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sku.json")
	load := func(body string) (SKU, error) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return LoadSKUFile(path)
	}
	s, err := load(`{"id":"x","device":"bar-t9","paint_gpu":"raster","gpu_render":"panfrost"}`)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := s.SKUDevice(); d.GPU != "raster" || d.GPURender != "panfrost" {
		t.Fatalf("device %+v", d)
	}
	other, _ := DeviceByID("bar-qwerty")
	if d := s.GPU(other); d.GPU != "raster" || d.GPURender != "panfrost" || d.ID != "bar-qwerty" {
		t.Fatalf("--device bar-qwerty: %+v", d)
	}
	if s, err := load(`{"id":"x","device":"bar-t9"}`); err != nil {
		t.Fatal(err)
	} else if d, _ := s.SKUDevice(); d.GPU != "" || d.GPURender != "" {
		t.Fatalf("no policy: %+v", d)
	}
	for _, good := range []string{`{"paint_gpu":"off"}`, `{"paint_gpu":"compose"}`, `{"gpu_render":"powervr"}`} {
		if _, err := load(good); err != nil {
			t.Errorf("%s: %v", good, err)
		}
	}
	for _, bad := range []string{`{"paint_gpu":"on"}`, `{"paint_gpu":"1"}`, `{"gpu_render":"/dev/dri/renderD128"}`, `{"gpu_render":"Panfrost"}`} {
		if _, err := load(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
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
	sku := SKU{ID: "x", Device: "bar-t9", Keymap: map[string]string{"139": "soft-left", "2": "1"}, PaintGPU: "compose"}
	dev, _ := sku.SKUDevice()
	g, err := StartGUI(c, renderer, dev)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	var got struct {
		Device struct {
			Keymap map[string]int `json:"keymap"`
			GPU    string         `json:"gpu"`
			Touch  bool           `json:"touch"`
		} `json:"device"`
	}
	raw, _ := os.ReadFile(hello)
	if err := json.Unmarshal(raw, &got); err != nil || !maps.Equal(got.Device.Keymap, map[string]int{"139": 59, "2": 513}) || got.Device.GPU != "compose" {
		t.Fatalf("hello %s: %v", raw, err)
	}
	if got.Device.Touch {
		t.Fatalf("hello %s: a touchscreen the SKU does not have", raw)
	}
}

// A display with a touchscreen (sku.json's touch): the renderer learns of
// it in the handshake, as the form factor's device.
func TestTouchSKU(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sku.json")
	if err := os.WriteFile(path, []byte(`{"id":"oneplus-fajita-t9","device":"bar-t9","width":1080,"height":2180,"scale":4.5,"keypad":"hid","touch":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSKUFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := s.SKUDevice()
	if !ok || !d.Touch {
		t.Fatalf("device %+v", d)
	}
	raw, _ := json.Marshal(d)
	if !strings.Contains(string(raw), `"touch":true`) {
		t.Fatalf("the handshake's device %s", raw)
	}
	if d, _ := (SKU{Device: "bar-t9"}).SKUDevice(); d.Touch {
		t.Fatal("a touchscreen without one in the SKU")
	}
}

// A sound switch (sku.json's sound_switch, the input device's name): the
// renderer learns its name in the handshake, to open that device; the
// developer machine's test SKU has one too.
func TestSoundSwitchSKU(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sku.json")
	if err := os.WriteFile(path, []byte(`{"id":"oneplus-fajita-t9","device":"bar-t9","keypad":"hid","sound_switch":"Alert slider"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSKUFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := s.SKUDevice()
	if !ok || d.SoundSwitch != "Alert slider" {
		t.Fatalf("device %+v", d)
	}
	if raw, _ := json.Marshal(d); !strings.Contains(string(raw), `"sound_switch":"Alert slider"`) {
		t.Fatalf("the handshake's device %s", raw)
	}
	if d, _ := (SKU{Device: "bar-t9"}).SKUDevice(); d.SoundSwitch != "" {
		t.Fatal("a sound switch without one in the SKU")
	}
	if raw, _ := json.Marshal(Device{}); strings.Contains(string(raw), "sound_switch") {
		t.Fatalf("a device without a switch says %s", raw)
	}
	if s, err := SimulatedSKU(filepath.Join("testdata", "sku-sound-switch.json")); err != nil || s.SoundSwitch == "" {
		t.Fatalf("the test SKU: %+v, %v", s, err)
	}
}

// A flip's lid switch and outer display (tools/sku.py writes both): read
// as written, a malformed outer display refused.
func TestFlipSKU(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sku.json")
	load := func(body string) (SKU, error) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return LoadSKUFile(path)
	}
	s, err := load(`{"id":"ref-flip-qcm6490-4g","device":"bar-t9","lid":"Lid","fingerprint_at":"ok-key",
		"outer":{"display":"ls013b7dh05","width":144,"height":168,"kind":"memory-lcd","color":"mono","drm_driver":"sharp_memory_display","width_mm":20,"height_mm":24}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := SKUOuter{Display: "ls013b7dh05", Width: 144, Height: 168, WidthMM: 20, HeightMM: 24, Kind: "memory-lcd", Color: "mono", DRMDriver: "sharp_memory_display"}
	if s.Lid != "Lid" || s.Outer != want {
		t.Fatalf("lid %q, outer %+v", s.Lid, s.Outer)
	}
	if s, err := load(`{"device":"bar-t9"}`); err != nil || s.Outer != (SKUOuter{}) || s.Lid != "" {
		t.Fatalf("no flip: %+v, %v", s, err)
	}
	good := `"display":"x","width":144,"height":168,"kind":"oled","color":"rgb","drm_driver":"virtio_gpu"`
	if _, err := load(`{"outer":{` + good + `}}`); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"outer":{"width":144}}`,
		`{"outer":{` + good + `,"width":0}}`,
		`{"outer":{` + good + `,"width_mm":20}}`,
		`{"outer":{` + good + `,"kind":"crt"}}`,
		`{"outer":{` + good + `,"kind":""}}`,
		`{"outer":{` + good + `,"color":"sepia"}}`,
		`{"outer":{` + good + `,"drm_driver":"/dev/dri/card1"}}`,
		`{"outer":{` + good + `,"drm_driver":""}}`,
		`{"lid":"Switches","sound_switch":"Switches"}`,
	} {
		if _, err := load(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
