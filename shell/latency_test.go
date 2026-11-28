package shell

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// sentLines keeps what the Shell sent a renderer, a line each.
type sentLines struct {
	mu    sync.Mutex
	lines []string
}

func (w *sentLines) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, l := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		w.lines = append(w.lines, l)
	}
	return len(p), nil
}

func (w *sentLines) Close() error { return nil }

func (w *sentLines) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.lines)
}

func (w *sentLines) last(t *testing.T) map[string]any {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	var m map[string]any
	if len(w.lines) == 0 || json.Unmarshal([]byte(w.lines[len(w.lines)-1]), &m) != nil {
		t.Fatalf("no message sent: %q", w.lines)
	}
	return m
}

// lockedLog is a Shell's log a test reads.
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// latencyGUI is a renderer that times inputs (features latency, patch and
// touch) for c: what the Shell sends it, and its own output to write to.
func latencyGUI(t *testing.T, c *Core) (*GUI, *sentLines, io.Writer) {
	t.Helper()
	w := &sentLines{}
	dev, _ := DeviceByID("bar-t9")
	g := &GUI{c: c, dev: dev, in: w, enc: json.NewEncoder(w), info: RendererInfo{Features: []string{"latency", "patch", "touch"}}, latency: true,
		sent: map[string]string{}, ready: make(chan guiMsg, 1), acks: make(chan guiMsg, 4), done: make(chan struct{})}
	outR, outW := io.Pipe()
	go g.read(outR, c.frameVersions())
	t.Cleanup(func() {
		_ = outW.Close()
		<-g.done
	})
	_ = c.onLoop(func() error {
		c.gui = g
		return nil
	})
	t.Cleanup(func() { _ = c.onLoop(func() error { c.gui = nil; return nil }) })
	return g, w, outW
}

func say(t *testing.T, out io.Writer, m map[string]any) {
	t.Helper()
	b, _ := json.Marshal(m)
	if _, err := out.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

func named(m map[string]any) []float64 {
	var ids []float64
	for _, v := range asList(m["in"]) {
		ids = append(ids, v.(float64))
	}
	return ids
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// The renderer's keys and touches carry ids (feature "latency"); the frame
// that shows what one did names it back, as a whole frame or a patch, and
// an input that showed nothing (a finger coming down on nothing) is named
// by no later frame, which would time it to something else.
func TestFramesNameTheInputsTheyShow(t *testing.T) {
	c, _ := bumpCore(t, 1, items("a", "b", "c")...)
	g, w, out := latencyGUI(t, c)
	push := func() {
		t.Helper()
		if err := c.onLoop(g.Push); err != nil {
			t.Fatal(err)
		}
	}
	waiting := func(n int) {
		t.Helper()
		until(t, c, "the input taken", func() bool { return len(g.awaiting) == n })
	}
	push()
	if m := w.last(t); m["t"] != "frame" || m["in"] != nil {
		t.Fatalf("first frame: %v", m)
	}
	say(t, out, map[string]any{"t": "key", "key": "down", "text": "", "in": 41})
	waiting(1)
	push()
	if m := w.last(t); m["t"] != "patch" || len(named(m)) != 1 || named(m)[0] != 41 {
		t.Fatalf("the frame of the key: %v", m)
	}
	n := w.count()
	say(t, out, map[string]any{"t": "touch", "phase": "down", "x": 200, "y": 300, "in": 42})
	waiting(1)
	push()
	if w.count() != n || len(g.awaiting) != 0 {
		t.Fatalf("a touch that showed nothing: %d frames sent, %d inputs waiting", w.count()-n, len(g.awaiting))
	}
	say(t, out, map[string]any{"t": "touch", "phase": "cancel", "x": 200, "y": 300})
	say(t, out, map[string]any{"t": "key", "key": "down", "text": "", "in": 43})
	waiting(1)
	push()
	if m := w.last(t); len(named(m)) != 1 || named(m)[0] != 43 {
		t.Fatalf("the next frame names %v, want 43 alone", m["in"])
	}
	// A renderer without the feature is sent no ids.
	_ = c.onLoop(func() error {
		g.latency = false
		g.taken(44, time.Now())
		if len(g.awaiting) != 0 {
			t.Error("an id kept for a renderer without latency")
		}
		return nil
	})
}

// An app's first page after its process started is named with the start
// on CLOCK_MONOTONIC (the renderer times it to the panel); its skeleton
// is not its page, a later page is no start, nor is the page of an app
// taken over from the Shell before or one started long before in the
// background. The renderer's answer goes to the log.
func TestFirstPageMarksTheAppStart(t *testing.T) {
	c := loopCore(t, t.TempDir())
	g, w, out := latencyGUI(t, c)
	logs := &lockedLog{}
	page := func(title string) []*View {
		return []*View{newView(ui.Screen{ID: "main", Title: title, Body: []ui.Node{{ID: "n", Kind: ui.KindText, Text: title}}})}
	}
	startOf := func() map[string]any {
		t.Helper()
		s, _ := w.last(t)["start"].(map[string]any)
		return s
	}
	_ = c.onLoop(func() error {
		c.log = log.New(logs, "", 0)
		c.booted = time.Now().Add(-bootDuration)
		c.GoHome()
		_ = g.Push()
		a := &App{M: &Manifest{ID: "com.example.notes", Name: "Notes"}, running: true, started: time.Now().Add(-150 * time.Millisecond)}
		c.fg = a
		_ = g.Push()
		if s := startOf(); s != nil {
			t.Errorf("the skeleton named a start: %v", s)
		}
		a.stack = page("Shopping")
		_ = g.Push()
		s := startOf()
		want := float64(monoMicros(time.Now()) - 150_000)
		if s["app"] != "com.example.notes" || s["at"].(float64) < want-50_000 || s["at"].(float64) > want+1000 {
			t.Errorf("the first page's start %v, want about %v", s, want)
		}
		a.stack = page("Errands")
		_ = g.Push()
		if s := startOf(); s != nil {
			t.Errorf("a later page named a start: %v", s)
		}
		// Started again: its first page is a start again.
		a.started, a.stack = time.Now(), nil
		_ = g.Push()
		a.stack = page("Again")
		_ = g.Push()
		if s := startOf(); s == nil {
			t.Error("the first page after a restart named no start")
		}
		b := &App{M: &Manifest{ID: "com.example.taken", Name: "Taken"}, running: true, adopted: true, started: time.Now(), stack: page("Taken")}
		c.fg = b
		_ = g.Push()
		if s := startOf(); s != nil {
			t.Errorf("an adopted app's page named a start: %v", s)
		}
		bg := &App{M: &Manifest{ID: "com.example.bg", Name: "Bg"}, running: true, started: time.Now().Add(-time.Minute), stack: page("Bg")}
		c.fg = bg
		_ = g.Push()
		if s := startOf(); s != nil {
			t.Errorf("an app started a minute before named a start: %v", s)
		}
		c.fg = nil
		return nil
	})
	say(t, out, map[string]any{"t": "shown", "app": "com.example.notes", "ms": 212})
	until(t, c, "the start logged", func() bool {
		return strings.Contains(logs.String(), "com.example.notes first page shown 212ms after start")
	})
}

// phone-shell --dump asks a renderer that measures for its numbers; hello
// says where they go (the run directory's paint-stats.json).
func TestDumpAsksTheRendererForItsNumbers(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_, w, _ := latencyGUI(t, c)
	if err := c.onLoop(c.WriteDump); err != nil {
		t.Fatal(err)
	}
	if m := w.last(t); m["t"] != "stats" {
		t.Fatalf("sent %v, want stats", m)
	}

	dir := t.TempDir()
	hello, renderer := filepath.Join(dir, "hello.json"), filepath.Join(dir, "renderer")
	script := "#!/bin/sh\nread hello\necho \"$hello\" > " + hello + "\necho '{\"t\":\"ready\",\"proto\":\"phone.frame/1\",\"renderer\":{\"name\":\"x\"},\"features\":[\"latency\"]}'\ncat >/dev/null\n"
	if err := os.WriteFile(renderer, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	dev, _ := DeviceByID("bar-t9")
	g, err := StartGUI(c, renderer, dev)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	var got struct{ Stats string }
	raw, _ := os.ReadFile(hello)
	if err := json.Unmarshal(raw, &got); err != nil || got.Stats != filepath.Join(c.cfg.RunDir, PaintStatsFile) || !g.latency {
		t.Fatalf("hello %s (%v), latency %v", raw, err, g.latency)
	}
}
