package shell

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

func TestProfileLayers(t *testing.T) {
	image, dev := t.TempDir(), t.TempDir()
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(image, "00-base.json", `{"product": "Prototype", "app_order": {"com.phone.music": 5}, "settings": {"auto_lock": 30}}`)
	write(image, "50-elder.json", `{"product": "Senior phone", "hidden_apps": ["com.phone.camera"], "settings": {"ime_fuzzy": true}}`)
	write(dev, "90-dev.json", `{"providers": {"location.get": "service:gnss"}, "app_order": null}`)

	p, err := LoadProfile(image)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	_ = json.Unmarshal(p.Settings, &st)
	if p.Product != "Senior phone" || !p.hidden("com.phone.camera") || p.AppOrder["com.phone.music"] != 5 || st["auto_lock"] != 30.0 || st["ime_fuzzy"] != true {
		t.Fatalf("image layers: %+v %v", p, st)
	}
	if !strings.HasSuffix(p.Origin["product"], "50-elder.json") || !strings.HasSuffix(p.Origin["app_order"], "00-base.json") {
		t.Fatalf("origins %v", p.Origin)
	}
	if p.provider("location.get") != "" {
		t.Fatal("the developer layer counted without developer mode")
	}
	p, err = LoadProfile(image, dev)
	if err != nil {
		t.Fatal(err)
	}
	if p.provider("location.get") != "gnss" || p.AppOrder != nil {
		t.Fatalf("developer layer: %+v", p)
	}
	if !strings.Contains(p.Dump(), "providers: "+filepath.Join(dev, "90-dev.json")) {
		t.Fatalf("dump:\n%s", p.Dump())
	}
	write(dev, "91-bad.json", `{"providers": {"location.get": "/tmp/evil.sock"}}`)
	if _, err := LoadProfile(image, dev); err == nil {
		t.Fatal("a provider outside service:<name> accepted")
	}
}

func TestProviderRouting(t *testing.T) {
	run := t.TempDir()
	got := make(chan map[string]any, 1)
	go func() {
		_ = wire.Serve(filepath.Join(run, "gnss.sock"), func(method string, params json.RawMessage) (any, error) {
			var p map[string]any
			_ = json.Unmarshal(params, &p)
			p["method"] = method
			got <- p
			return map[string]any{"lat": 31.23, "lon": 121.47}, nil
		})
	}()
	c := &Core{ev: make(chan func(), 8), cfg: Config{RunDir: run}, profile: &Profile{Providers: map[string]string{ui.CapLocationGet: "service:gnss"}}}
	b := &broker{c: c, clients: map[string]*wire.Client{}}
	var reply any
	var rerr error
	done := false
	b.dispatch(&App{M: &Manifest{ID: "com.example.map"}}, ui.Msg{Cap: ui.CapLocationGet, Args: json.RawMessage(`{"accuracy":"fine"}`)}, func(v any, err error) {
		reply, rerr, done = v, err, true
	})
	select {
	case f := <-c.ev:
		f()
	case <-time.After(5 * time.Second):
		t.Fatal("the provider never answered")
	}
	if !done || rerr != nil {
		t.Fatalf("reply: %v %v", done, rerr)
	}
	raw, _ := json.Marshal(reply)
	if string(raw) != `{"lat":31.23,"lon":121.47}` {
		t.Fatalf("reply %s", raw)
	}
	p := <-got
	if p["method"] != "capability" || p["cap"] != ui.CapLocationGet || p["app"] != "com.example.map" {
		t.Fatalf("the provider got %v", p)
	}
}
