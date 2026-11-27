package shell

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
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
	if !alone(t) {
		return
	}
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

// fakeFrame0 is a phone.frame/0 made up for the tests (there is none
// before 1): its display is "screen" and its quit "bye".
var fakeFrame0Form = ui.ProtoForm{Major: 0, Rules: []ui.ProtoRule{{T: "display", As: "screen"}, {T: "quit", As: "bye"}}}

var fakeFrame0 = frameVersion{major: 0, in: fakeFrame0Form.FromMajor, out: fakeFrame0Form.ToMajor}

// TestFakeRendererMajors is not a test: run as a child process it plays a
// renderer of the made-up phone.frame/0 alone (PHONE_FAKE_FRAME "old",
// which reads only hello's proto, as a renderer that does not negotiate),
// or of phone.frame/1 and 0 ("both", which picks the newest of protos). It
// writes the hello it got and the message types it got after to
// PHONE_FAKE_FRAME_OUT.
func TestFakeRendererMajors(t *testing.T) {
	mode, out := os.Getenv("PHONE_FAKE_FRAME"), os.Getenv("PHONE_FAKE_FRAME_OUT")
	if mode == "" {
		t.Skip("helper process")
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Scan()
	seen := []string{sc.Text()}
	var hello struct {
		Proto  string
		Protos []string
	}
	_ = json.Unmarshal(sc.Bytes(), &hello)
	speak := ""
	switch {
	case mode == "old" && hello.Proto == "phone.frame/0":
		speak = hello.Proto
	case mode == "both" && len(hello.Protos) > 0:
		speak = hello.Protos[0]
	case mode == "both" && (hello.Proto == "phone.frame/1" || hello.Proto == "phone.frame/0"):
		speak = hello.Proto
	}
	if speak == "" {
		os.Exit(3)
	}
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(map[string]any{"t": "ready", "proto": speak, "renderer": map[string]string{"name": "fake-" + mode}, "features": []string{"snap"}})
	display := map[string]any{"t": "display", "display": map[string]int{"width": 640, "height": 480}}
	if speak == "phone.frame/0" {
		display["t"] = "screen"
	}
	_ = enc.Encode(display)
	for sc.Scan() {
		var m struct{ T string }
		_ = json.Unmarshal(sc.Bytes(), &m)
		seen = append(seen, m.T)
		if m.T == "quit" || m.T == "bye" {
			break
		}
	}
	_ = os.WriteFile(out, []byte(strings.Join(seen, "\n")), 0o644)
	os.Exit(0)
}

// During a transition the Shell's hello offers its major and the one
// before (proto the older, protos both): a renderer of the older alone is
// taken and spoken to in its form both ways (its display is adopted, the
// Shell's quit reaches it as its bye), one of both gets the newer; with
// one major only, the older renderer refuses the hello (exit 3) and one
// naming a major the Shell lacks is refused with both versions.
func TestRendererMajors(t *testing.T) {
	if !alone(t) {
		return
	}
	dev, _ := DeviceByID("bar-t9")
	transition := []frameVersion{{major: ownFrameMajor()}, fakeFrame0}
	for _, tc := range []struct {
		name, mode   string
		vers         []frameVersion
		hello, speak string
		got          string
		err          string
	}{
		{"older renderer in a transition", "old", transition, `"proto":"phone.frame/0","protos":["phone.frame/1","phone.frame/0"]`, "phone.frame/0", "bye", ""},
		{"renderer of both in a transition", "both", transition, `"proto":"phone.frame/0","protos":["phone.frame/1","phone.frame/0"]`, "phone.frame/1", "quit", ""},
		{"renderer of both, one major", "both", nil, `"proto":"phone.frame/1"`, "phone.frame/1", "quit", ""},
		{"older renderer, one major", "old", nil, "", "", "", "renderer exited during start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apps := t.TempDir()
			_ = os.MkdirAll(filepath.Join(apps, "x"), 0o755)
			_ = os.WriteFile(filepath.Join(apps, "x", "manifest.json"), []byte(`{"id": "com.test.x", "name": "x", "exec": "true"}`), 0o644)
			c, err := New(Config{W: 40, H: 24, AppsDir: apps, RunDir: t.TempDir(), StateDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Shutdown()
			c.frames = tc.vers
			out := filepath.Join(t.TempDir(), "seen")
			t.Setenv("PHONE_FAKE_FRAME", tc.mode)
			t.Setenv("PHONE_FAKE_FRAME_OUT", out)
			g, err := StartGUI(c, os.Args[0], dev, "-test.run=^TestFakeRendererMajors$")
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("got %v, want an error with %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if g.Info().Proto != tc.speak {
				t.Errorf("speaks %s, want %s", g.Info().Proto, tc.speak)
			}
			for end := time.Now().Add(5 * time.Second); c.screenDev.Width != 640; {
				select {
				case f := <-c.Events():
					f()
				case <-time.After(time.Until(end)):
					t.Fatalf("the renderer's display was not adopted: %dx%d", c.screenDev.Width, c.screenDev.Height)
				}
			}
			g.Close()
			raw, _ := os.ReadFile(out)
			seen := strings.Split(string(raw), "\n")
			if !strings.Contains(seen[0], tc.hello) || seen[len(seen)-1] != tc.got {
				t.Errorf("the renderer got %q, want a hello with %s and then %s", seen, tc.hello, tc.got)
			}
		})
	}
	if _, err := pickFrame(transition, "phone.frame/2"); err == nil || err.Error() != "renderer speaks phone.frame/2, this shell speaks phone.frame/1 and phone.frame/0" {
		t.Errorf("a renderer of phone.frame/2 in a transition: %v", err)
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

// A script's key acts on the screen the renderer has laid out, however fast
// the keys come: the frame of each state reaches it before the next key, as
// it would with a person pressing them (where a list scrolls follows the
// frames the renderer had).
func TestScriptKeysFollowTheFrames(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	dir := t.TempDir()
	frames, renderer := filepath.Join(dir, "frames.jsonl"), filepath.Join(dir, "renderer")
	script := "#!/bin/sh\nread hello\necho '{\"t\":\"ready\",\"proto\":\"phone.frame/1\",\"renderer\":{\"name\":\"log\"},\"features\":[\"snap\"]}'\ncat >" + frames + "\n"
	if err := os.WriteFile(renderer, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	dev, _ := DeviceByID("bar-t9")
	g, err := StartGUI(c, renderer, dev)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r := &Runner{C: c, GUI: g, Out: &out}
	ok := r.Run("== keys\nsleep 1600\nat sys.idle 8000\nkey enter\nat sys.menu\nkey right 2\nsleep 200\n")
	g.Close()
	if !ok {
		t.Fatalf("script:\n%s", out.String())
	}
	raw, err := os.ReadFile(frames)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var f struct {
			Screen string
			Body   []struct {
				Kind string
				Sel  int
			}
		}
		if json.Unmarshal([]byte(line), &f) != nil || f.Screen != "system/sys.menu" {
			continue
		}
		for _, n := range f.Body {
			if n.Kind == "grid" {
				seen[n.Sel] = true
			}
		}
	}
	for sel := 0; sel < 2; sel++ {
		if !seen[sel] {
			t.Errorf("the menu with tile %d selected never reached the renderer before the next key (drawn: %v)", sel, seen)
		}
	}
}

func TestRendererWatchdog(t *testing.T) {
	if !alone(t) {
		return
	}
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

// A renderer that writes a line of its error output in pieces, as phone-paint
// does, while the shell logs on the same stderr: the shell's line comes
// before the renderer's, not inside it, where the GUI drive would read
// neither (a phone.gui=1 boot's console had "phone-paint: panel phone-shell:
// power key: ..." and then "on in 2 ms").
func TestRendererStderrWholeLines(t *testing.T) {
	if !alone(t) {
		return
	}
	apps := t.TempDir()
	_ = os.MkdirAll(filepath.Join(apps, "x"), 0o755)
	_ = os.WriteFile(filepath.Join(apps, "x", "manifest.json"), []byte(`{"id": "com.test.x", "name": "x", "exec": "true"}`), 0o644)
	run := t.TempDir()
	c, err := New(Config{W: 40, H: 24, AppsDir: apps, RunDir: run, StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Shutdown()
	stderr := &syncBuffer{}
	GUIStderr = stderr
	defer func() { GUIStderr = nil }()
	rest := filepath.Join(t.TempDir(), "rest")
	p := filepath.Join(t.TempDir(), "pieces")
	script := "#!/bin/sh\nread hello\necho '{\"t\":\"ready\",\"proto\":\"phone.frame/1\",\"renderer\":{\"name\":\"pieces\"},\"features\":[\"snap\"]}'\n" +
		"printf 'phone-paint: panel ' >&2\nwhile [ ! -e " + rest + " ]; do sleep 0.01; done\nprintf 'on in 2 ms\\n' >&2\ncat >/dev/null\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	dev, _ := DeviceByID("bar-t9")
	g, err := StartGUI(c, p, dev)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	until := func(what string, have func() bool) {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); !have(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("no %s", what)
			}
		}
	}
	until("first piece in gui.log", func() bool {
		b, _ := os.ReadFile(filepath.Join(run, "gui.log"))
		return strings.Contains(string(b), "phone-paint: panel ")
	})
	time.Sleep(100 * time.Millisecond)
	_, _ = stderr.Write([]byte("phone-shell: power key: short press\n"))
	if err := os.WriteFile(rest, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	until("rest of the line on stderr", func() bool { return strings.Contains(stderr.String(), "2 ms") })
	if got, want := stderr.String(), "phone-shell: power key: short press\nphone-paint: panel on in 2 ms\n"; got != want {
		t.Fatalf("stderr %q, want %q", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(run, "gui.log")); string(b) != "phone-paint: panel on in 2 ms\n" {
		t.Fatalf("gui.log %q", b)
	}
}
