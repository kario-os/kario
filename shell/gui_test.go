package shell

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFakeRenderer is not a test: run as a child process it plays a
// renderer of the protocol version in PHONE_FAKE_RENDERER.
func TestFakeRenderer(t *testing.T) {
	mode := os.Getenv("PHONE_FAKE_RENDERER")
	if mode == "" {
		t.Skip("helper process")
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Scan()
	var hello struct{ T, Proto string }
	if json.Unmarshal(sc.Bytes(), &hello) != nil || hello.T != "hello" || hello.Proto != RendererProto {
		os.Exit(3)
	}
	ready := map[string]any{"t": "ready", "features": []string{"snap"},
		"renderer": map[string]string{"name": "fake", "version": "9", "license": "LicenseRef-Proprietary"},
		"display":  map[string]int{"width": 800, "height": 352}}
	if p := map[string]string{"same": "phone.frame/1.4", "next": "phone.frame/2"}[mode]; p != "" {
		ready["proto"] = p
	}
	_ = json.NewEncoder(os.Stdout).Encode(ready)
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"quit"`) {
			break
		}
	}
	os.Exit(0)
}

func TestRendererHandshake(t *testing.T) {
	dev, _ := DeviceByID("bar-t9")
	for _, tc := range []struct{ mode, err string }{
		{"same", ""},
		{"next", "speaks phone.frame/2"},
		{"silent", "did not say which protocol"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			apps := t.TempDir()
			if err := os.MkdirAll(filepath.Join(apps, "x"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(apps, "x", "manifest.json"), []byte(`{"id": "com.test.x", "name": "x", "exec": "true"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			c, err := New(Config{W: 40, H: 24, AppsDir: apps, RunDir: t.TempDir(), StateDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Shutdown()
			t.Setenv("PHONE_FAKE_RENDERER", tc.mode)
			g, err := StartGUI(c, os.Args[0], dev, "-test.run=^TestFakeRenderer$")
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("got %v, want an error with %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			if info := g.Info(); info.Name != "fake" || !info.Has("snap") || info.Has("kms") {
				t.Fatalf("renderer info %+v", info)
			}
			if c.screenDev.Width != 800 || c.screenDev.Height != 352 {
				t.Fatalf("display not adopted: %dx%d", c.screenDev.Width, c.screenDev.Height)
			}
			if !strings.Contains(c.about()["renderer"].(string), "fake 9") {
				t.Fatalf("about: %v", c.about()["renderer"])
			}
		})
	}
}

// A whole phone's panel reaches the renderer in the handshake (its size as
// drawn and its scale), and the Shell lays out for it: the bar phone's menu
// columns, a web page's surface in the body at that scale.
func TestWholePhoneHandshake(t *testing.T) {
	apps := t.TempDir()
	_ = os.MkdirAll(filepath.Join(apps, "x"), 0o755)
	_ = os.WriteFile(filepath.Join(apps, "x", "manifest.json"), []byte(`{"id": "com.test.x", "name": "x", "exec": "true"}`), 0o644)
	c, err := New(Config{W: 40, H: 24, AppsDir: apps, RunDir: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Shutdown()
	dir := t.TempDir()
	hello := filepath.Join(dir, "hello.json")
	renderer := filepath.Join(dir, "renderer")
	script := "#!/bin/sh\nread hello\necho \"$hello\" > " + hello + "\necho '{\"t\":\"ready\",\"proto\":\"phone.frame/1\",\"renderer\":{\"name\":\"x\"},\"features\":[\"snap\"]}'\ncat >/dev/null\n"
	if err := os.WriteFile(renderer, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sku := SKU{ID: "oneplus-fajita-t9", Device: "bar-t9", Width: 1080, Height: 2180, Scale: 4.5}
	dev, _ := sku.SKUDevice()
	g, err := StartGUI(c, renderer, dev)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	var got struct{ Device Device }
	raw, _ := os.ReadFile(hello)
	if err := json.Unmarshal(raw, &got); err != nil || got.Device.Width != 1080 || got.Device.Height != 2180 || got.Device.Scale != 4.5 || got.Device.Keypad != "t9" {
		t.Fatalf("hello %s: %v", raw, err)
	}
	if n := c.menuColumns(); n != 3 {
		t.Errorf("menu columns %d", n)
	}
	// The body less phone-paint's margins and bars (webSize), in logical
	// pixels times 4.5; float rounding may cost a pixel.
	if w, h, s := c.webSize(); w != 1080-54 || h < 2180-288-1 || h > 2180-288 || s != 4.5 {
		t.Errorf("web surface %dx%d at %g", w, h, s)
	}
}

func fakeRenderer(t *testing.T, name string, crash bool) string {
	t.Helper()
	tail := "cat >/dev/null"
	if crash {
		tail = "exit 1"
	}
	p := filepath.Join(t.TempDir(), name)
	script := "#!/bin/sh\nread hello\necho '{\"t\":\"ready\",\"proto\":\"phone.frame/1\",\"renderer\":{\"name\":\"" + name + "\"},\"features\":[\"snap\"]}'\n" + tail + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRendererWatchdog(t *testing.T) {
	apps := t.TempDir()
	_ = os.MkdirAll(filepath.Join(apps, "x"), 0o755)
	_ = os.WriteFile(filepath.Join(apps, "x", "manifest.json"), []byte(`{"id": "com.test.x", "name": "x", "exec": "true"}`), 0o644)
	c, err := New(Config{W: 40, H: 24, AppsDir: apps, RunDir: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Shutdown()
	dev, _ := DeviceByID("bar-t9")
	w, err := WatchRenderer(c, dev, fakeRenderer(t, "crashy", true), fakeRenderer(t, "steady", false))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxCrashes; i++ {
		if w.G.Info().Name != "crashy" {
			t.Fatalf("restart %d: on %q before %d crashes", i, w.G.Info().Name, maxCrashes)
		}
		select {
		case <-w.G.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the crashing renderer did not exit")
		}
		if err := w.Restart(); err != nil {
			t.Fatal(err)
		}
	}
	if w.G.Info().Name != "steady" {
		t.Fatalf("after %d crashes still on %q, want the fallback", maxCrashes, w.G.Info().Name)
	}
	select {
	case <-w.G.Done():
		t.Fatal("the fallback renderer exited")
	case <-time.After(300 * time.Millisecond):
	}
	w.G.Close()
}
