package shell

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// The timeline is Chrome Trace Event JSON: pages with the Shell's time on
// them, capability calls from request to answer, input to the next page,
// pings and an ANR, CPU and memory counters.
func TestTracer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	tr := newTracer(f, "com.example.app", time.Second, t0)
	screen := &ui.Screen{ID: "main", Title: "Main", Body: []ui.Node{{Kind: ui.KindText, Text: "1"}, {Kind: ui.KindText, Text: "2"}}}
	tr.in(ui.Msg{T: ui.MsgReset, Screen: screen}, at(10), 2*time.Millisecond)
	tr.out(ui.Msg{T: ui.MsgAction, ScreenID: "main", Node: "k:1", Action: "press"}, at(100))
	tr.in(ui.Msg{T: ui.MsgReq, Req: 7, Cap: "clipboard.write"}, at(110), 0)
	tr.out(ui.Msg{T: ui.MsgRes, Req: 7, OK: true}, at(135))
	tr.out(ui.Msg{T: ui.MsgRes, Req: 8, Err: "denied"}, at(137))
	tr.in(ui.Msg{T: ui.MsgReq, Req: 8, Cap: "location.get"}, at(136), 2*time.Millisecond)
	tr.in(ui.Msg{T: ui.MsgUpdate, Screen: screen}, at(140), time.Millisecond)
	tr.in(ui.Msg{T: ui.MsgUpdate, Screen: screen}, at(150), time.Millisecond)
	tr.out(ui.Msg{T: ui.MsgPing, Value: "1"}, at(200))
	tr.in(ui.Msg{T: ui.MsgPong, Value: "1"}, at(1700), 0)
	tr.out(ui.Msg{T: ui.MsgLifecycle, State: ui.StateBackground}, at(1800))
	tr.sample(42, 100*time.Millisecond, 3<<20, at(0))
	tr.sample(42, 150*time.Millisecond, 4<<20, at(250))
	tr.in(ui.Msg{T: ui.MsgRes, Req: 99}, at(1900), 0)
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	var events []TraceEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatalf("%v:\n%s", err, raw)
	}
	find := func(ph, name string) []TraceEvent {
		var out []TraceEvent
		for _, e := range events {
			if e.Ph == ph && e.Name == name {
				out = append(out, e)
			}
		}
		return out
	}
	if m := find("M", "process_name"); len(m) != 1 || m[0].Args["name"] != "com.example.app" {
		t.Fatalf("process name %+v", m)
	}
	pages := find("X", "update main")
	if len(pages) != 2 || pages[0].Ts != 140000 || *pages[0].Dur != 1000 || pages[0].Args["nodes"] != float64(2) {
		t.Fatalf("pages %+v", pages)
	}
	if b, e := find("b", "action press"), find("e", "action press"); len(b) != 1 || len(e) != 1 || b[0].Ts != 100000 || e[0].Ts != 140000 ||
		e[0].Args["latency_ms"] != float64(40) || b[0].ID != e[0].ID {
		t.Fatalf("input to update %+v %+v", b, e)
	}
	if b, e := find("b", "clipboard.write"), find("e", "clipboard.write"); len(b) != 1 || len(e) != 1 || e[0].Ts-b[0].Ts != 25000 || e[0].Args["ok"] != true {
		t.Fatalf("call %+v %+v", b, e)
	}
	if b, e := find("b", "location.get"), find("e", "location.get"); len(b) != 1 || len(e) != 1 || e[0].Ts-b[0].Ts != 1000 || e[0].Args["err"] != "denied" {
		t.Fatalf("a call answered while it was handled %+v %+v", b, e)
	}
	if p, anr := find("X", "ping"), find("i", "ANR"); len(p) != 1 || *p[0].Dur != 1500000 || len(anr) != 1 || anr[0].Ts != 1200000 {
		t.Fatalf("ping %+v, ANR %+v", p, anr)
	}
	if c := find("C", "CPU"); len(c) != 1 || c[0].Args["% of a core"] != float64(20) {
		t.Fatalf("CPU %+v", c)
	}
	if m := find("C", "memory"); len(m) != 2 || m[1].Args["MB"] != float64(4) {
		t.Fatalf("memory %+v", m)
	}
	if l := find("i", "lifecycle background"); len(l) != 1 {
		t.Fatalf("lifecycle %+v", l)
	}

	// Killed before Close, the file is the array without its bracket.
	f, _ = os.Create(path)
	tr = newTracer(f, "com.example.app", time.Second, t0)
	tr.in(ui.Msg{T: ui.MsgPush, Screen: screen}, at(5), 0)
	raw, _ = os.ReadFile(path)
	if err := json.Unmarshal([]byte(string(raw)+"\n]"), &events); err != nil || !strings.HasPrefix(string(raw), "[\n") {
		t.Fatalf("an unfinished trace: %v\n%s", err, raw)
	}
	_ = tr.Close()
}

// A renderer that times its frames: the Shell asks for it in its hello
// while it traces, and each frame is a slice from its sending to the
// renderer's report on a track of its own, the renderer's steps inside it
// in order; a frame the renderer does not report is left out. Without a
// trace the Shell does not ask.
func TestTracerFrames(t *testing.T) {
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
	script := "#!/bin/sh\nread hello\necho \"$hello\" > " + hello + "\n" +
		"echo '{\"t\":\"ready\",\"proto\":\"phone.frame/1\",\"renderer\":{\"name\":\"x\"},\"features\":[\"snap\",\"timing\"]}'\n" +
		"n=0\nwhile read line; do n=$((n+1)); [ $n = 2 ] && continue; " +
		"echo '{\"t\":\"drawn\",\"n\":'$n',\"parse_ms\":0.1,\"layout_ms\":0.4,\"draw_ms\":[1.5,0.5],\"transition_ms\":[0.25]}'; done\n"
	if err := os.WriteFile(renderer, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	asked := func() bool {
		raw, _ := os.ReadFile(hello)
		return strings.Contains(string(raw), `"timing":true`)
	}

	g, err := StartGUI(c, renderer, Device{Width: 480, Height: 640, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	if asked() || g.timing {
		t.Fatal("asked for the frames' times without a trace")
	}
	g.Close()

	path := filepath.Join(t.TempDir(), "trace.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tr := newTracer(f, "com.example.app", time.Second, time.Now())
	active.Store(tr)
	defer active.CompareAndSwap(tr, nil)
	g, err = StartGUI(c, renderer, Device{Width: 480, Height: 640, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if !asked() || !g.timing {
		t.Fatal("the hello of a traced Shell did not ask for the frames' times")
	}
	for i := 0; i < 3; i++ {
		c.showToast(strings.Repeat("x", i+1))
		if err := g.Push(); err != nil {
			t.Fatal(err)
		}
	}
	pumpUntil(t, c, func() bool { return len(g.timed) == 0 })
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var events []TraceEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatalf("%v:\n%s", err, raw)
	}
	var frames []string
	var steps []TraceEvent
	names := 0
	for _, e := range events {
		switch {
		case e.Pid == tracePidRenderer && e.Ph == "M":
			names++
		case e.Pid == tracePidRenderer && strings.HasPrefix(e.Name, "frame "):
			frames = append(frames, e.Name)
			if e.Args["layout_ms"] != 0.4 || e.Args["draw_ms"] != 2.0 || e.Args["draw_steps"] != 2.0 || e.Args["transition_steps"] != 1.0 || e.Args["patch"] == nil {
				t.Errorf("%s: %v", e.Name, e.Args)
			}
		case e.Pid == tracePidRenderer:
			steps = append(steps, e)
		}
	}
	if names != 2 || strings.Join(frames, ",") != "frame 1,frame 3" {
		t.Fatalf("renderer track: %d names, frames %v", names, frames)
	}
	var order []string
	for i, s := range steps[:5] {
		order = append(order, s.Name)
		if i > 0 && s.Ts < steps[i-1].Ts+*steps[i-1].Dur-1 {
			t.Errorf("%s at %v overlaps %s", s.Name, s.Ts, steps[i-1].Name)
		}
	}
	if strings.Join(order, ",") != "parse,layout,draw,draw,transition step" || *steps[2].Dur != 1500 {
		t.Fatalf("steps %v, the first draw %v µs", order, *steps[2].Dur)
	}
}
