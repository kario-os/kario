package shell

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMediaStore(t *testing.T) {
	dir := t.TempDir()
	m := openMedia(dir, "")
	if len(m.items) != 3 {
		t.Fatalf("%d sample photos, want 3", len(m.items))
	}
	if _, err := m.add([]byte("not a png"), "x", time.Now()); err == nil {
		t.Fatal("accepted data that is not a PNG")
	}
	raw, _ := encodePNG(samplePhoto("gen:t", 64, 48))
	it, err := m.add(raw, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	thumb, err := os.ReadFile(m.thumb(it.ID))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(thumb))
	if err != nil || cfg.Width != thumbSide || cfg.Height != thumbSide {
		t.Fatalf("thumbnail %dx%d, %v", cfg.Width, cfg.Height, err)
	}
	m.grant("com.example", it)
	again := openMedia(dir, "")
	got := again.get("media:" + it.ID)
	if got == nil || len(got.Grants) != 1 || got.Grants[0] != "com.example" {
		t.Fatalf("grant not kept: %+v", got)
	}
	next, err := again.add(raw, "next", time.Now())
	if err != nil || next.ID == it.ID {
		t.Fatalf("reopened store reused id %s", it.ID)
	}
}

func TestOfferVideoAddsDownloadToAlbum(t *testing.T) {
	apps := t.TempDir()
	_ = os.MkdirAll(filepath.Join(apps, "x"), 0o755)
	_ = os.WriteFile(filepath.Join(apps, "x", "manifest.json"), []byte(`{"id": "com.test.x", "name": "x", "exec": "true"}`), 0o644)
	c, err := New(Config{W: 40, H: 24, AppsDir: apps, RunDir: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Shutdown()
	f, err := c.files.add("clip.webm", []byte("webm bytes"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !playableVideo(f.Name) || playableVideo("clip.mp4") {
		t.Fatal("playableVideo")
	}
	before := len(c.media.items)
	c.offerVideo(f)
	if len(c.overlays) == 0 {
		t.Fatal("no confirmation")
	}
	o, ok := c.overlays[len(c.overlays)-1].(*confirmOverlay)
	if !ok {
		t.Fatalf("top overlay %T, want the confirmation", c.overlays[len(c.overlays)-1])
	}
	o.onYes()
	if len(c.media.items) != before+1 {
		t.Fatalf("%d items, want %d", len(c.media.items), before+1)
	}
	it := c.media.items[len(c.media.items)-1]
	if !it.video() || it.Name != "clip" || it.Size != int64(len("webm bytes")) {
		t.Fatalf("added %+v", it)
	}
}
