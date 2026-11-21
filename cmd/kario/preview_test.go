package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kario-os/kario/ui"
)

// The previews are evaluated with their sample state, without the app
// starting: the template's two states give two different pages.
func TestPreviewEval(t *testing.T) {
	n := node(t)
	dir := newTestProject(t)
	out := t.TempDir()
	app := filepath.Join(out, "app")
	if _, err := build(dir, buildOptions{out: app, quiet: true}); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(out, "preview.mjs")
	if err := bundlePreviews(dir, bundle); err != nil {
		t.Fatal(err)
	}
	rt := filepath.Join(out, "rt")
	if err := writeNode(rt); err != nil {
		t.Fatal(err)
	}
	screens, err := evalPreviews(n, rt, bundle, app, "es")
	if err != nil {
		t.Fatal(err)
	}
	text := func(s *ui.Screen) string {
		var b strings.Builder
		for _, nd := range s.Body {
			b.WriteString(nd.Text + nd.Title + "|")
		}
		return b.String()
	}
	if len(screens) != 2 || screens[0].Name != "new" || screens[1].Name != "tapped" || screens[0].Screen.ID != "main" ||
		!strings.Contains(text(screens[1].Screen), "42") || text(screens[0].Screen) == text(screens[1].Screen) || screens[0].Screen.Right != "reset" {
		t.Fatalf("%+v", screens)
	}
	if main, _ := os.ReadFile(filepath.Join(app, "main.js")); strings.Contains(string(main), "renderPreviews") || strings.Contains(string(main), "tapped") {
		t.Fatal("the app's own build carries its previews")
	}

	replace(t, dir, "src/previews.ts", `count.set(42)`, `{ throw new Error("no sample") }`)
	if err := bundlePreviews(dir, bundle); err != nil {
		t.Fatal(err)
	}
	if _, err := evalPreviews(n, rt, bundle, app, "en"); err == nil || !strings.Contains(err.Error(), `preview "tapped"`) || !strings.Contains(err.Error(), "no sample") {
		t.Fatalf("a preview that throws: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "src", "previews.ts"))
	if err := bundlePreviews(dir, bundle); err == nil || !strings.Contains(err.Error(), "no src/previews.ts") {
		t.Fatalf("no previews: %v", err)
	}
	if got := strings.Join(slugs([]string{"three notes", "three notes", "", "a/b"}), " "); got != "three-notes three-notes-2 preview a-b" {
		t.Fatalf("slugs %q", got)
	}
}

var staged struct {
	once       sync.Once
	dir, shell string
	err        string
}

func TestMain(m *testing.M) {
	code := m.Run()
	if staged.dir != "" {
		_ = os.RemoveAll(staged.dir)
	}
	os.Exit(code)
}

// stagedShell is phone-shell built from this tree, in a prefix whose
// services, apps and data are make build's (build/usr); with phone-paint.
func stagedShell(t *testing.T) (shell, paint string) {
	t.Helper()
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	usr := filepath.Join(root, "build", "usr")
	paint = filepath.Join(root, "paint", "target", "release", "phone-paint")
	for _, p := range []string{filepath.Join(usr, "bin", "phone-mediad"), filepath.Join(usr, "share", "phone"), paint} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("no %s (make build, make paint)", p)
		}
	}
	staged.once.Do(func() {
		dir, err := os.MkdirTemp("", "kario-staged-")
		if err != nil {
			staged.err = err.Error()
			return
		}
		staged.dir = dir
		bin := filepath.Join(dir, "bin")
		if msg, err := exec.Command("go", "build", "-o", filepath.Join(bin, "phone-shell"), filepath.Join(root, "cmd", "phone-shell")).CombinedOutput(); err != nil {
			staged.err = string(msg)
			return
		}
		entries, _ := os.ReadDir(filepath.Join(usr, "bin"))
		for _, e := range entries {
			if e.Name() != "phone-shell" {
				_ = os.Symlink(filepath.Join(usr, "bin", e.Name()), filepath.Join(bin, e.Name()))
			}
		}
		for _, d := range []string{"lib", "share"} {
			_ = os.Symlink(filepath.Join(usr, d), filepath.Join(dir, d))
		}
		staged.shell = filepath.Join(bin, "phone-shell")
	})
	if staged.err != "" {
		t.Fatalf("building phone-shell: %s", staged.err)
	}
	return staged.shell, paint
}

func decodePNG(t *testing.T, file string) image.Image {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return img
}

// kario preview draws each preview on each form factor and SKU asked for:
// pictures of the form factor's size, not blank, different for different
// states.
func TestPreviewImages(t *testing.T) {
	n := node(t)
	shell, paint := stagedShell(t)
	dir := newTestProject(t)
	images, err := previewApp(dir, previewOptions{shell: shell, gui: paint, node: n, devices: []string{"bar-t9"}, skus: []string{"qemu-qwerty"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 4 {
		t.Fatalf("%+v", images)
	}
	byKey := map[string]image.Image{}
	for _, im := range images {
		pic := decodePNG(t, im.File)
		b := pic.Bounds()
		want := map[string][2]int{"bar-t9": {480, 640}, "qemu-qwerty": {640, 480}}[im.Target]
		if b.Dx() != want[0] || b.Dy() != want[1] || im.Width != want[0] || im.Height != want[1] {
			t.Errorf("%s/%s: %v, want %v", im.Target, im.Name, b, want)
		}
		colours := map[[3]uint32]bool{}
		for y := b.Min.Y; y < b.Max.Y; y += 7 {
			for x := b.Min.X; x < b.Max.X; x += 7 {
				r, g, bl, _ := pic.At(x, y).RGBA()
				colours[[3]uint32{r >> 8, g >> 8, bl >> 8}] = true
			}
		}
		if len(colours) < 3 {
			t.Errorf("%s/%s: a blank picture (%d colours)", im.Target, im.Name, len(colours))
		}
		byKey[im.Target+"/"+im.Name] = pic
	}
	for _, target := range []string{"bar-t9", "qemu-qwerty"} {
		a, b := byKey[target+"/new"], byKey[target+"/tapped"]
		if a == nil || b == nil {
			t.Fatalf("%s: %v", target, byKey)
		}
		diff := 0
		for y := 0; y < a.Bounds().Dy(); y++ {
			for x := 0; x < a.Bounds().Dx(); x++ {
				if a.At(x, y) != b.At(x, y) {
					diff++
				}
			}
		}
		if diff < 50 {
			t.Errorf("%s: the two states differ in %d pixels", target, diff)
		}
	}
}
