package shell

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// fakeDisplayd stands in for phone-displayd: it keeps the calls the Shell
// makes and answers each with its state; events go to the subscribers.
type fakeDisplayd struct {
	mu    sync.Mutex
	calls []string
	st    svc.DisplayStatus
	hub   *wire.Hub
}

func (f *fakeDisplayd) handle(method string, params json.RawMessage) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method+" "+string(params))
	return f.st, nil
}

// took is the calls since the last look whose text holds each of want, in
// order.
func (f *fakeDisplayd) took(want ...string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		ok := true
		for _, w := range want {
			ok = ok && strings.Contains(c, w)
		}
		if ok {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeDisplayd) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

func (f *fakeDisplayd) send(st svc.DisplayStatus) {
	f.mu.Lock()
	f.st = st
	f.mu.Unlock()
	f.hub.Broadcast(svc.DisplayEvent{Event: svc.DisplayEventRate, DisplayStatus: st})
}

type displayTest struct {
	*locTest
	d *fakeDisplayd
}

func newDisplayTest(t *testing.T, apps ...*App) *displayTest {
	lt := newLocTest(t, apps...)
	c := lt.c
	c.log = log.New(io.Discard, "", 0)
	c.cfg.RunDir = t.TempDir()
	c.ev = make(chan func(), 512)
	c.broker.clients = map[string]*wire.Client{}
	f := &fakeDisplayd{st: svc.DisplayStatus{Seq: 1, Panel: "sim", Rates: []int{60, 120}, Hz: 60, Want: 60, Reason: "idle", Setting: ui.RefreshAuto}}
	hub, err := wire.ServeHub(filepath.Join(c.cfg.RunDir, svc.DisplaydSock), f.handle)
	if err != nil {
		t.Fatal(err)
	}
	f.hub = hub
	t.Cleanup(func() { c.stopping.Store(true) })
	c.startDisplay()
	dt := &displayTest{lt, f}
	dt.pump("the Shell to reach displayd", func() bool { return c.display.up && len(f.took(svc.DisplayState)) > 0 })
	return dt
}

// wait pumps the loop until displayd has a call holding each of want.
func (dt *displayTest) wait(want ...string) {
	dt.t.Helper()
	dt.pump(fmt.Sprintf("a call to displayd with %q", want), func() bool { return len(dt.d.took(want...)) > 0 })
}

// Keys pass on as interactions, at most one each displayKeyEvery; the
// renderer's motion as animating and end, once each way.
func TestDisplayKeysAndMotion(t *testing.T) {
	dt := newDisplayTest(t)
	c := dt.c
	dt.d.reset()
	start := time.Now()
	for range 5 {
		c.displayInput()
	}
	took := time.Since(start)
	dt.wait(svc.DisplayHint, `"source":"input"`, svc.DisplayInteraction)
	time.Sleep(50 * time.Millisecond)
	dt.pump("calls", func() bool { return true })
	n := len(dt.d.took(svc.DisplayInteraction))
	if most := 1 + int(took/displayKeyEvery); n > most {
		t.Fatalf("5 keys in %v passed on as %d interactions, at most %d", took, n, most)
	}
	time.Sleep(displayKeyEvery)
	c.displayInput()
	dt.pump("the next interaction", func() bool { return len(dt.d.took(svc.DisplayInteraction)) == n+1 })
	c.displayMotion(true)
	c.displayMotion(true)
	dt.wait(`"source":"renderer"`, svc.DisplayAnimating)
	c.displayMotion(false)
	dt.wait(`"source":"renderer"`, `"kind":"end"`)
	if n := len(dt.d.took(`"source":"renderer"`)); n != 2 {
		t.Fatalf("the renderer's motion passed on %d times, want twice", n)
	}
	// A renderer that died mid-animation: its successor's start ends it.
	dt.d.reset()
	c.displayMotion(true)
	dt.wait(`"source":"renderer"`, svc.DisplayAnimating)
	c.gui = &GUI{c: c}
	c.displayRenderer()
	dt.wait(`"source":"renderer"`, `"kind":"end"`)
}

// The setting and the low battery's cap go to displayd when they change;
// the developer machine's stand-in battery caps nothing, a test's does.
func TestDisplaySettingAndBattery(t *testing.T) {
	dt := newDisplayTest(t)
	c := dt.c
	dt.wait(svc.DisplaySetting, ui.RefreshAuto)
	dt.wait(svc.DisplayCap, svc.DisplayCapBattery, `"hz":0`)
	if err := c.setRefreshRate(2); err != nil {
		t.Fatal(err)
	}
	dt.wait(svc.DisplaySetting, ui.RefreshHigh)
	if c.setRefreshRate(3) == nil || c.setRefreshRate(0.5) == nil {
		t.Fatal("a refresh rate out of the choices was taken")
	}
	dt.d.reset()
	c.powerOverride = &Power{Level: 9, Simulated: true}
	c.checkDisplay()
	dt.wait(svc.DisplayCap, svc.DisplayCapBattery, `"hz":60`)
	c.powerOverride = &Power{Level: 9, Charging: true, Simulated: true}
	c.checkDisplay()
	dt.wait(svc.DisplayCap, svc.DisplayCapBattery, `"hz":0`)
	c.checkDisplay()
	time.Sleep(50 * time.Millisecond)
	dt.pump("calls", func() bool { return true })
	if n := len(dt.d.took(svc.DisplayCap)); n != 2 {
		t.Fatalf("the cap went %d times for two changes", n)
	}
	if got := c.displayView()[ui.SettingRefreshRate]; got != 2 {
		t.Fatalf("settings.get shows the refresh rate as %v", got)
	}
}

// display.hint: the app in front only, its hint under its own name, at
// most DisplayHintBurst at once; it goes when the app leaves the front.
func TestDisplayHintFromApps(t *testing.T) {
	fg, bg := locApp("com.fg"), locApp("com.bg")
	dt := newDisplayTest(t, fg, bg)
	c := dt.c
	c.fg = fg
	hint := func(a *App, args ui.DisplayHintArgs) ui.Msg {
		dt.req(a, ui.CapDisplayHint, args)
		return dt.reply(a)
	}
	if m := hint(bg, ui.DisplayHintArgs{AnimatingMS: 500}); m.OK || !strings.HasSuffix(m.Err, ui.ErrDisplayNotFront) {
		t.Fatalf("an app in the background hinted: %+v", m)
	}
	for _, bad := range []ui.DisplayHintArgs{{}, {AnimatingMS: 500, FPS: 30}, {AnimatingMS: ui.DisplayHintMaxMS + 1}, {FPS: ui.DisplayHintMaxFPS + 1}} {
		if m := hint(fg, bad); m.OK {
			t.Fatalf("%+v taken", bad)
		}
	}
	dt.d.mu.Lock()
	dt.d.st.Hz, dt.d.st.Seq = 120, 2
	dt.d.mu.Unlock()
	start := time.Now()
	m := hint(fg, ui.DisplayHintArgs{AnimatingMS: 800})
	var res ui.DisplayHintRes
	if !m.OK || json.Unmarshal(m.Data, &res) != nil || res.Hz != 120 || len(res.Rates) != 2 {
		t.Fatalf("the front app's hint: %+v", m)
	}
	dt.wait(svc.DisplayHint, `"source":"app:com.fg"`, `"kind":"animating"`, `"ms":800`)
	if m := hint(fg, ui.DisplayHintArgs{FPS: 24}); !m.OK {
		t.Fatalf("a content hint: %+v", m)
	}
	dt.wait(`"source":"app:com.fg"`, `"kind":"content"`, `"fps":24`)
	// The burst is judged by the hints granted against what the bucket
	// holds plus what it refills while they are answered: a busy machine
	// stretches the answers and rightly gets more.
	granted, sent := 2, 2+2*ui.DisplayHintBurst
	for range sent - granted {
		switch m := hint(fg, ui.DisplayHintArgs{AnimatingMS: 100}); {
		case m.OK:
			granted++
		case !strings.HasSuffix(m.Err, ui.ErrDisplayHintRate):
			t.Fatalf("refused for %q", m.Err)
		case granted < ui.DisplayHintBurst:
			t.Fatalf("refused after %d hints, within the burst of %d", granted, ui.DisplayHintBurst)
		}
	}
	took := time.Since(start)
	if most := ui.DisplayHintBurst + took.Seconds()*ui.DisplayHintPerSecond; float64(granted) > most {
		t.Fatalf("%d of %d hints granted in %v, the bucket allows %d", granted, sent, took.Round(time.Millisecond), int(most))
	}
	if granted == sent {
		t.Logf("all %d hints granted in %v, too slow to see the limit", sent, took.Round(time.Millisecond))
	}
	c.fg = bg
	c.displayApps()
	dt.wait(svc.DisplayHint, `"source":"app:com.fg"`, `"kind":"clear"`)
	if c.display.apps["com.fg"] {
		t.Fatal("the app that left still counts as hinting")
	}
	// The bucket fills again with time.
	b := &hintBucket{}
	now := time.Now()
	for range ui.DisplayHintBurst {
		if !b.take(now) {
			t.Fatal("the burst was refused")
		}
	}
	if b.take(now) || !b.take(now.Add(time.Second/ui.DisplayHintPerSecond)) {
		t.Fatal("the bucket does not refill at its rate")
	}
}

// displayd's choice goes to a renderer that switches rates, once a
// change, and to a new renderer again; the renderer's answer goes back.
// DisplaySeen finds the rates the panel went to between looks.
func TestDisplayRateReachesTheRenderer(t *testing.T) {
	dt := newDisplayTest(t)
	c := dt.c
	r, w := io.Pipe()
	c.gui = &GUI{c: c, in: w, info: RendererInfo{Features: []string{"rate", "kms"}}}
	lines := make(chan string, 16)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if err != nil {
				return
			}
			for _, l := range strings.Split(strings.TrimSpace(string(buf[:n])), "\n") {
				lines <- l
			}
		}
	}()
	got := func() string {
		t.Helper()
		var l string
		dt.pump("a line to the renderer", func() bool {
			select {
			case l = <-lines:
				return true
			default:
				return false
			}
		})
		return l
	}
	c.displayRenderer()
	dt.wait(svc.DisplayPanel, `"kms":true`)
	if l := got(); l != `{"hz":60,"t":"rate"}` {
		t.Fatalf("a new renderer got %s", l)
	}
	dt.d.send(svc.DisplayStatus{Seq: 2, Rates: []int{60, 120}, Hz: 60, Want: 120, Reason: "input"})
	if l := got(); l != `{"hz":120,"t":"rate"}` {
		t.Fatalf("the renderer got %s", l)
	}
	c.displayApplied(120, "")
	dt.wait(svc.DisplayPanel, `"applied":120`)
	dt.d.send(svc.DisplayStatus{Seq: 3, Rates: []int{60, 120}, Hz: 120, Want: 120, Reason: "input"})
	dt.d.send(svc.DisplayStatus{Seq: 4, Rates: []int{60, 120}, Hz: 60, Want: 60, Reason: "idle"})
	if l := got(); l != `{"hz":60,"t":"rate"}` {
		t.Fatalf("the renderer got %s, not once a change", l)
	}
	dt.pump("the rate seen", func() bool { hz, _ := c.DisplayRate(); return hz == 60 })
	if !c.DisplaySeen(120) || c.DisplaySeen(120) {
		t.Fatalf("seen %v (looked at %d): 120 once since the start, not again", c.display.seen, c.display.looked)
	}
	if !c.DisplaySeen(60) {
		t.Fatalf("seen %v: 60 after 120", c.display.seen)
	}
	// An answer older than the event before it changes nothing.
	c.displayRate(svc.DisplayStatus{Seq: 3, Rates: []int{60, 120}, Hz: 120, Want: 120, Reason: "input"})
	if hz, _ := c.DisplayRate(); hz != 60 || c.display.rendered != 60 {
		t.Fatalf("an old answer took the rate back to %d (the renderer told %d)", hz, c.display.rendered)
	}
}
