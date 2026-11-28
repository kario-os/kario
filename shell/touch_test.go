package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kario-os/kario/hal/input"
	"github.com/kario-os/kario/tools/prebuilt"
	"github.com/kario-os/kario/ui"
)

// touchRig is a Shell whose foreground app the test plays (anrRig), on a
// renderer the test plays too: what it says goes to GUI.read as from
// phone-paint on a phone with a touchscreen. The app answers pings and
// tells the test each action it gets ("<node> <action>").
type touchRig struct {
	t    *testing.T
	c    *Core
	a    *App
	acts chan string
	mu   sync.Mutex
	app  io.Writer
	out  *io.PipeWriter
	// closing: the test is over and the rig being taken down; a write to
	// the app's end may then fail, as it closes under an answer to a ping
	// the Shell sent just before.
	closing atomic.Bool
}

var touchList = ui.Screen{ID: "list", Title: "List", Right: "new", Actions: []ui.Action{{ID: "new", Label: "New"}}, Body: []ui.Node{
	{Kind: ui.KindItem, ID: "a", Title: "Alpha", Actions: []ui.Action{{ID: "open", Label: "Open", Primary: true}}},
	{Kind: ui.KindItem, ID: "b", Title: "Bravo", Actions: []ui.Action{{ID: "open", Label: "Open", Primary: true}, {ID: "prev", Label: "Previous", Key: "left"}, {ID: "next", Label: "Next", Key: "right"}}},
	{Kind: ui.KindItem, ID: "c", Title: "Charlie", Actions: []ui.Action{{ID: "open", Label: "Open", Primary: true}, {ID: "share", Label: "Share"}}},
	{Kind: ui.KindInput, ID: "q", Placeholder: "Search", Submit: "search"},
}}

func newTouchRig(t *testing.T, features ...string) *touchRig {
	t.Helper()
	c, a, rd, app := anrRig(t)
	r := &touchRig{t: t, c: c, a: a, acts: make(chan string, 16), app: app}
	go func() {
		for {
			line, err := rd.ReadBytes('\n')
			if err != nil {
				return
			}
			var m ui.Msg
			_ = json.Unmarshal(line, &m)
			switch m.T {
			case ui.MsgPing:
				r.send(ui.Msg{T: ui.MsgPong, Value: m.Value})
			case ui.MsgAction:
				r.acts <- m.Node + " " + m.Action
			}
		}
	}()
	outR, outW := io.Pipe()
	r.out = outW
	g := &GUI{c: c, in: nopWriter{}, enc: json.NewEncoder(nopWriter{}), info: RendererInfo{Features: features},
		ready: make(chan guiMsg, 1), acks: make(chan guiMsg, 4), done: make(chan struct{})}
	go g.read(outR, c.frameVersions())
	t.Cleanup(func() {
		_ = outW.Close()
		<-g.done
	})
	_ = c.onLoop(func() error {
		c.gui = g
		c.settings.A11y.LongPress = 400
		return nil
	})
	t.Cleanup(func() { _ = c.onLoop(func() error { c.gui = nil; return nil }) })
	t.Cleanup(func() { r.closing.Store(true) })
	r.push(touchList)
	return r
}

func (r *touchRig) send(m ui.Msg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, _ := json.Marshal(m)
	if _, err := r.app.Write(append(b, '\n')); err != nil && !r.closing.Load() {
		r.t.Error(err)
	}
}

// lateApp is a touchRig's app answering as on a busy machine: its first
// write waits until the rig has closed the app's end.
type lateApp struct {
	*os.File
	waiting, wrote chan struct{}
	once           sync.Once
}

func (l *lateApp) Write(p []byte) (int, error) {
	first := false
	l.once.Do(func() { first = true })
	if !first {
		return l.File.Write(p)
	}
	close(l.waiting)
	defer close(l.wrote)
	// A write of nothing fails only once the file is closed.
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if _, err := l.File.Write(nil); err != nil {
			break
		}
	}
	return l.File.Write(p)
}

// The rig's teardown fails no test whose body passed: on a busy machine
// the ping after the body's last action may be answered only once the rig
// has closed the app's end.
func TestTouchRigTeardownFailsNoTest(t *testing.T) {
	var r *touchRig
	var app *lateApp
	// Registered first, run last: the answer has been written and send
	// (which holds r.mu until it has reported the write's error) is done
	// before the test ends.
	t.Cleanup(func() {
		if app == nil {
			return
		}
		select {
		case <-app.wrote:
		case <-time.After(10 * time.Second):
		}
		r.mu.Lock()
		r.mu.Unlock()
	})
	r = newTouchRig(t, "touch")
	r.mu.Lock()
	app = &lateApp{File: r.app.(*os.File), waiting: make(chan struct{}), wrote: make(chan struct{})}
	r.app = app
	r.mu.Unlock()
	r.tap(120, 90, r.row(2))
	r.action("c open")
	select {
	case <-app.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("no ping after the action")
	}
}

// push: the app opens screen s; the renderer draws it (the frame the
// touches' hits are of).
func (r *touchRig) push(s ui.Screen) {
	r.t.Helper()
	r.send(ui.Msg{T: ui.MsgPush, Screen: &s})
	until(r.t, r.c, "the screen "+s.Title, func() bool { return title(r.a) == s.Title })
	r.drawn()
}

func (r *touchRig) drawn() {
	dev, _ := DeviceByID("bar-t9")
	_ = r.c.onLoop(func() error { r.c.Frame(dev); return nil })
}

// screen is the frame's screen the hits name.
func (r *touchRig) screen() string {
	var s string
	_ = r.c.onLoop(func() error { s = r.c.current().top().frameScreen; return nil })
	return s
}

// say is a line from the renderer.
func (r *touchRig) say(phase string, x, y float64, hit *TouchHit) {
	r.t.Helper()
	m := map[string]any{"t": "touch", "phase": phase, "x": x, "y": y}
	if hit != nil {
		m["hit"] = hit
	}
	b, _ := json.Marshal(m)
	if _, err := r.out.Write(append(b, '\n')); err != nil {
		r.t.Fatal(err)
	}
}

func (r *touchRig) tap(x, y float64, hit TouchHit) {
	r.say("down", x, y, &hit)
	r.say("up", x, y, nil)
}

// idle: what the renderer said has been taken.
func (r *touchRig) idle() {
	time.Sleep(50 * time.Millisecond)
	_ = r.c.onLoop(func() error { return nil })
}

func (r *touchRig) action(want string) {
	r.t.Helper()
	select {
	case got := <-r.acts:
		if got != want {
			r.t.Fatalf("the app got %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		r.t.Fatalf("the app got no action (want %q)", want)
	}
}

func (r *touchRig) noAction() {
	r.t.Helper()
	select {
	case got := <-r.acts:
		r.t.Fatalf("the app got %q", got)
	case <-time.After(300 * time.Millisecond):
	}
}

func (r *touchRig) focus() string {
	var f string
	_ = r.c.onLoop(func() error { f = r.c.current().top().focus; return nil })
	return f
}

func ip(i int) *int { return &i }

func (r *touchRig) row(i int) TouchHit {
	return TouchHit{Area: "body", Screen: r.screen(), Node: ip(i), Tile: ip(0)}
}

// A tap on a list's row puts the focus there and presses OK: the app gets
// the row's primary action; a tap on an input field only focuses it.
func TestTouchTapIsFocusAndOK(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	r.tap(120, 90, r.row(2))
	r.action("c open")
	if f := r.focus(); f != "c" {
		t.Fatalf("focus on %q, want c", f)
	}
	r.drawn()
	r.tap(120, 60, r.row(1))
	r.action("b open")
	r.drawn()
	r.tap(120, 120, r.row(3))
	r.noAction()
	if f := r.focus(); f != "q" {
		t.Fatalf("focus on %q, want the field", f)
	}
	r.tap(120, 200, TouchHit{Area: "body", Screen: r.screen()})
	r.noAction()
	r.tap(120, 60, TouchHit{Area: "body", Screen: "another/screen", Node: ip(0), Tile: ip(0)})
	r.noAction()
}

// The soft keys' bar: the right soft key runs the page's action, the left
// one opens Options; a tap around the menu closes it, as Back does.
func TestTouchSoftKeysAndAroundAMenu(t *testing.T) {
	r := newTouchRig(t, "touch")
	r.tap(230, 310, TouchHit{Area: "soft", Soft: 2})
	r.action("a new")
	r.tap(10, 310, TouchHit{Area: "soft", Soft: 0})
	until(t, r.c, "the Options menu", func() bool { m, ok := overlayOf[*menuOverlay](r.c); return ok && m.title == tr.T("Options") })
	r.tap(120, 30, TouchHit{Area: "outside", Kind: "menu"})
	until(t, r.c, "the menu closed", func() bool { return len(r.c.overlays) == 0 })
}

// Back: a tap on the title bar, a swipe to the right from the left edge.
// Elsewhere a swipe is ← or →, which a row may bind (a player's previous
// and next).
func TestTouchBackAndSwipes(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	detail := ui.Screen{ID: "detail", Title: "Detail", Body: []ui.Node{{Kind: ui.KindText, Text: "Words"}}}
	r.push(detail)
	r.tap(120, 26, TouchHit{Area: "title"})
	until(t, r.c, "back on the list", func() bool { return title(r.a) == "List" })
	r.push(detail)
	r.say("down", 3, 150, &TouchHit{Area: "body", Screen: r.screen()})
	r.say("move", 30, 152, nil)
	r.say("up", 70, 155, nil)
	until(t, r.c, "back on the list by the edge", func() bool { return title(r.a) == "List" })
	r.drawn()
	r.tap(120, 60, r.row(1))
	r.action("b open")
	r.drawn()
	swipe := func(x0, x1 float64) {
		r.say("down", x0, 60, &TouchHit{Area: "body", Screen: r.screen(), Node: ip(1), Tile: ip(0)})
		r.say("move", (x0+x1)/2, 62, nil)
		r.say("up", x1, 64, nil)
	}
	swipe(60, 160)
	r.action("b prev")
	swipe(160, 60)
	r.action("b next")
	// Not far enough, or further up than sideways: neither.
	swipe(100, 120)
	r.say("down", 100, 60, &TouchHit{Area: "body", Screen: r.screen()})
	r.say("move", 110, 62, nil)
	r.say("up", 140, 110, nil)
	r.noAction()
	if got := title(r.a); got != "List" {
		t.Fatalf("on %q", got)
	}
}

// With the focus on a text field a finger dragged sideways moves the caret
// with it, a character each touchCaret, while it goes (report 008 §1.3:
// before, a swipe to the left was → and the caret went right); a row's
// swipe stays ← or →.
func TestTouchDragMovesTheCaretWithTheFinger(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	r.tap(120, 120, r.row(3))
	until(t, r.c, "the focus on the field", func() bool { return r.c.current().top().focus == "q" })
	until(t, r.c, "the text typed", func() bool {
		v := r.c.current().top()
		v.setText("q", v.edit("q"), []rune("hello"), 5)
		return v.edit("q").caret == 5
	})
	caretAt := func(want int, what string) {
		t.Helper()
		r.idle()
		until(t, r.c, fmt.Sprintf("the caret at %d after %s", want, what), func() bool { return r.c.current().top().edit("q").caret == want })
	}
	field := TouchHit{Area: "body", Screen: r.screen(), Node: ip(3), Tile: ip(0)}
	r.say("down", 200, 120, &field)
	r.say("move", 188, 121, nil)
	caretAt(4, "12 px to the left, the finger still down")
	r.say("move", 160, 122, nil)
	r.say("up", 164, 122, nil)
	caretAt(2, "36 px to the left")
	r.say("down", 100, 120, &field)
	r.say("move", 112, 121, nil)
	r.say("up", 126, 121, nil)
	caretAt(4, "26 px to the right")
	r.noAction()
	r.tap(120, 60, r.row(1))
	r.action("b open")
	r.drawn()
	r.say("down", 160, 60, &TouchHit{Area: "body", Screen: r.screen(), Node: ip(1), Tile: ip(0)})
	r.say("move", 110, 62, nil)
	r.say("up", 60, 64, nil)
	r.action("b next")
}

// A swipe as a thumb makes it on a phone (report 007, §4 A): short, or an
// arc that goes up a good part of the way, is still ← or →; one that
// began sideways is a swipe however the finger wavers first.
func TestTouchSwipeAsAThumbMakesIt(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	r.tap(120, 60, r.row(1))
	r.action("b open")
	r.drawn()
	swipe := func(pts ...[2]float64) {
		r.say("down", pts[0][0], pts[0][1], &TouchHit{Area: "body", Screen: r.screen(), Node: ip(1), Tile: ip(0)})
		for _, p := range pts[1 : len(pts)-1] {
			r.say("move", p[0], p[1], nil)
		}
		p := pts[len(pts)-1]
		r.say("up", p[0], p[1], nil)
	}
	// 30 px: 8 mm on the 6T.
	swipe([2]float64{150, 60}, [2]float64{138, 63}, [2]float64{120, 70})
	r.action("b next")
	// An arc to the right, rising more than half as far as it goes.
	swipe([2]float64{60, 90}, [2]float64{80, 86}, [2]float64{130, 70}, [2]float64{170, 30})
	r.action("b prev")
}

// From the left edge as a finger comes in from the side (first felt a few
// millimetres in) a short swipe is Back: on the notification centre, and
// over a full-screen overlay (the viewfinder), whose ← is nothing (report
// 007, §3.1).
func TestTouchEdgeSwipeIsBackEverywhere(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	edge := func(hit TouchHit) {
		r.say("down", 15, 150, &hit)
		r.say("move", 30, 151, nil)
		r.say("up", 47, 153, nil)
	}
	cancelled := make(chan bool, 1)
	o := &captureOverlay{app: r.a, shoot: func() {}, cancel: func() { cancelled <- true }}
	_ = r.c.onLoop(func() error { r.c.pushOverlay(o); return nil })
	edge(TouchHit{Area: "overlay", Kind: "capture"})
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the viewfinder stayed")
	}
	_ = r.c.onLoop(func() error { r.c.openSystem("sys.notifs"); return nil })
	r.drawn()
	edge(TouchHit{Area: "body", Screen: r.screen()})
	until(t, r.c, "out of the notification centre", func() bool { return r.c.current().top().S.ID != "sys.notifs" })
}

// Back is one step (report 007, §3.8): on an app's first page, opened from
// the main menu, it goes back to the menu, by the title bar as by the key;
// the end key goes to standby.
func TestBackFromAnAppIsOneStep(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	open := func() {
		opened := false
		_ = r.c.onLoop(func() error {
			r.c.GoHome()
			r.c.openSystem("sys.menu")
			r.c.systemAction(r.c.sys.top(), &ui.Node{ID: "app:" + r.a.M.ID}, ui.Action{ID: "open"})
			opened = r.c.fg == r.a
			return nil
		})
		if !opened {
			t.Fatal("the menu did not open the app")
		}
	}
	onSys := func() string {
		if r.c.fg == nil {
			return r.c.sys.top().S.ID
		}
		return ""
	}
	sys := func() string {
		var s string
		_ = r.c.onLoop(func() error { s = onSys(); return nil })
		return s
	}
	open()
	r.drawn()
	r.tap(120, 26, TouchHit{Area: "title"})
	until(t, r.c, "back on the menu", func() bool { return onSys() != "" })
	if s := sys(); s != "sys.menu" {
		t.Fatalf("the title bar went to %s, not the menu", s)
	}
	open()
	_ = r.c.onLoop(func() error { r.c.Key(Named(KeyEsc)); return nil })
	if s := sys(); s != "sys.menu" {
		t.Fatalf("Back went to %q, not the menu", s)
	}
	open()
	_ = r.c.onLoop(func() error { r.c.Key(Named(KeyF4)); return nil })
	if s := sys(); s != "sys.idle" {
		t.Fatalf("the end key went to %q, not standby", s)
	}
}

// laidOut: the renderer laid the page on top out so: its nodes at ys of
// height h, the body area as tall as area.
func (r *touchRig) laidOut(area float64, ys []float64, h float64) {
	r.drawn()
	_ = r.c.onLoop(func() error {
		v := r.c.current().top()
		l := &Layout{Screen: v.frameScreen, Area: area, Top: 41, Left: 6, Width: 228}
		for _, y := range ys {
			l.Nodes = append(l.Nodes, LayoutNode{Y: y, H: h})
			l.Content = y + h + 3
		}
		r.c.layout = l
		return nil
	})
}

func (r *touchRig) frame() *Frame {
	var f *Frame
	dev, _ := DeviceByID("bar-t9")
	_ = r.c.onLoop(func() error { f = r.c.Frame(dev); return nil })
	return f
}

// A drag on a page that shows all of it (the main menu) moves nothing; on
// a longer one the content follows the finger and the focus stays on what
// shows, moving on only when its row scrolls out; a key goes on from there
// (report 007, §3.5).
func TestTouchDragScrollsTheBody(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	r.laidOut(240, []float64{0, 30, 60, 90}, 27)
	drag := func(y0, y1 float64) {
		r.say("down", 120, y0, &TouchHit{Area: "body", Screen: r.screen(), Node: ip(0), Tile: ip(0)})
		for y := y0; y != y1; {
			y += math.Copysign(math.Min(6, math.Abs(y1-y)), y1-y)
			r.say("move", 121, y, nil)
		}
		r.say("up", 121, y1, nil)
		r.idle()
	}
	drag(200, 120)
	r.noAction()
	if f := r.focus(); f != "a" {
		t.Fatalf("a drag on a page that shows all of it moved the focus to %q", f)
	}
	if f := r.frame(); f.Scroll.Offset != nil {
		t.Fatalf("and scrolled it to %v", *f.Scroll.Offset)
	}
	long := ui.Screen{ID: "long", Title: "Long"}
	var ys []float64
	for i := range 20 {
		long.Body = append(long.Body, ui.Node{Kind: ui.KindItem, ID: fmt.Sprint("r", i), Title: fmt.Sprint("Row ", i), Actions: []ui.Action{{ID: "open", Label: "Open", Primary: true}}})
		ys = append(ys, float64(i*30))
	}
	r.push(long)
	r.laidOut(100, ys, 27)
	drag(200, 170)
	if f := r.focus(); f != "r1" {
		t.Fatalf("30 px up: focus on %q, want r1 (r0 went out)", f)
	}
	if f := r.frame(); f.Scroll.Offset == nil || *f.Scroll.Offset != 30 {
		t.Fatalf("30 px up: the frame's offset %v", f.Scroll.Offset)
	}
	drag(200, 140)
	if f := r.focus(); f != "r3" {
		t.Fatalf("60 px more: focus on %q, want r3", f)
	}
	if f := r.frame(); *f.Scroll.Offset != 90 {
		t.Fatalf("60 px more: offset %v", *f.Scroll.Offset)
	}
	drag(100, 300)
	if f := r.focus(); f != "r2" {
		t.Fatalf("dragged down to the top: focus on %q, want r2 (the last row that shows)", f)
	}
	if f := r.frame(); *f.Scroll.Offset != 0 {
		t.Fatalf("dragged down past the top: offset %v", *f.Scroll.Offset)
	}
	_ = r.c.onLoop(func() error { r.c.Key(Named(KeyDown)); return nil })
	if f := r.frame(); f.Scroll.Offset != nil || r.focus() != "r3" {
		t.Fatalf("a key after the drag: offset %v, focus %q", f.Scroll.Offset, r.focus())
	}
}

// A grid scrolled by a finger keeps the focus in the column it was in.
func TestDragFocusInAGrid(t *testing.T) {
	s := ui.Screen{ID: "grid", Columns: 3}
	for i := range 12 {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, Style: ui.StyleTile, ID: fmt.Sprint("t", i), Title: fmt.Sprint(i), Actions: []ui.Action{{ID: "open", Label: "Open", Primary: true}}})
	}
	v := newView(s)
	v.focus = "t1"
	v.frameIDs = [][]string{{"t0", "t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10", "t11"}}
	v.frameBody = []int{0}
	l := &Layout{Area: 120, Nodes: []LayoutNode{{Y: 0, H: 240}}, Content: 243}
	dragFocus(v, l, 70)
	if v.focus != "t7" {
		t.Fatalf("its row out of the top: focus on %s, want t7 (the first row that shows, column 2)", v.focus)
	}
	dragFocus(v, l, 0)
	if v.focus != "t4" {
		t.Fatalf("its row out of the bottom: focus on %s, want t4", v.focus)
	}
}

// A tap on a player's progress bar, or a swipe along it, goes to that
// tenth of it by the digits the page binds; a bar on a page without them
// is a bar (report 007, §3.7).
func TestTouchSeekOnAProgressBar(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	player := ui.Screen{ID: "play", Title: "Player", Body: []ui.Node{
		{Kind: ui.KindMedia, ID: "video", Actions: []ui.Action{{ID: "toggle", Label: "Pause", Primary: true}, {ID: "back5", Label: "Back", Key: "left"}, {ID: "fwd5", Label: "Forward", Key: "right"}}},
		{Kind: ui.KindProgress, Text: "0:03 / 0:15", Fraction: 0.2},
	}}
	for d := range 10 {
		player.Actions = append(player.Actions, ui.Action{ID: fmt.Sprint("jump", d), Label: "Go", Key: fmt.Sprint(d), Hidden: true})
	}
	r.push(player)
	r.laidOut(240, []float64{0, 150}, 24)
	bar := TouchHit{Area: "body", Screen: r.screen(), Node: ip(1), Tile: ip(0)}
	r.tap(6+228*0.45, 200, bar)
	r.action("video jump4")
	r.say("down", 6+228*0.2, 200, &bar)
	r.say("move", 6+228*0.5, 201, nil)
	r.say("up", 6+228*0.78, 202, nil)
	r.action("video jump7")
	r.tap(6+228*0.999, 200, bar)
	r.action("video jump9")
	// Elsewhere on the page a swipe is still ← or →.
	r.say("down", 160, 100, &TouchHit{Area: "body", Screen: r.screen(), Node: ip(0), Tile: ip(0)})
	r.say("up", 100, 102, nil)
	r.action("video fwd5")
	player.Actions = nil
	player.ID, player.Title = "play2", "Player 2"
	r.push(player)
	r.laidOut(240, []float64{0, 150}, 24)
	r.tap(6+228*0.45, 200, TouchHit{Area: "body", Screen: r.screen(), Node: ip(1), Tile: ip(0)})
	r.noAction()
}

// A page the app puts in for the one beside it, at ← or → (the next
// photo), slides in as a page pushed or popped does; one replaced for
// another reason just shows (report 007, §3.6).
func TestTheNextPageSlidesIn(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	_ = r.c.onLoop(func() error { r.c.booted = time.Now().Add(-bootDuration); return nil })
	photo := func(n int) ui.Screen {
		return ui.Screen{ID: fmt.Sprint("view:", n), Title: fmt.Sprint("Photo ", n), Actions: []ui.Action{{ID: "prev", Label: "Previous", Key: "left"}, {ID: "next", Label: "Next", Key: "right"}},
			Body: []ui.Node{{Kind: ui.KindText, Text: fmt.Sprint("Picture ", n)}}}
	}
	r.push(photo(1))
	turn := func(key string, n int) string {
		t.Helper()
		before := r.place()
		_ = r.c.onLoop(func() error { r.c.Key(Named(key)); return nil })
		r.action(map[string]string{KeyRight: " next", KeyLeft: " prev"}[key])
		s := photo(n)
		r.send(ui.Msg{T: ui.MsgReplace, Screen: &s})
		until(t, r.c, s.Title, func() bool { return title(r.a) == s.Title })
		return transitionFor(before, r.place())
	}
	if tr := turn(KeyRight, 2); tr != transPush {
		t.Errorf("→: %q, want a push", tr)
	}
	if tr := turn(KeyLeft, 1); tr != transPop {
		t.Errorf("←: %q, want a pop", tr)
	}
	before := r.place()
	s := photo(3)
	r.send(ui.Msg{T: ui.MsgReplace, Screen: &s})
	until(t, r.c, s.Title, func() bool { return title(r.a) == s.Title })
	if tr := transitionFor(before, r.place()); tr != "" {
		t.Errorf("replaced by itself: %q", tr)
	}
}

// Back slides as a pop wherever it leads (report 008 §1.3: the user saw
// every Back slide in from the right): to a page that came in for the one
// beside it (→), which kept its slide, and out of an app's first page to
// the menu it was opened from, a change of app; opening the app is a push.
func TestBackIsAPop(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	_ = r.c.onLoop(func() error { r.c.booted = time.Now().Add(-bootDuration); return nil })
	photo := func(n int) ui.Screen {
		return ui.Screen{ID: fmt.Sprint("view:", n), Title: fmt.Sprint("Photo ", n), Actions: []ui.Action{{ID: "next", Label: "Next", Key: "right"}},
			Body: []ui.Node{{Kind: ui.KindText, Text: fmt.Sprint("Picture ", n)}}}
	}
	r.push(photo(1))
	_ = r.c.onLoop(func() error { r.c.Key(Named(KeyRight)); return nil })
	r.action(" next")
	s := photo(2)
	r.send(ui.Msg{T: ui.MsgReplace, Screen: &s})
	until(t, r.c, s.Title, func() bool { return title(r.a) == s.Title })
	r.push(ui.Screen{ID: "detail", Title: "Detail", Body: []ui.Node{{Kind: ui.KindText, Text: "Words"}}})
	before := r.place()
	r.tap(120, 26, TouchHit{Area: "title"})
	until(t, r.c, "back on the photo", func() bool { return title(r.a) == s.Title })
	if tr := transitionFor(before, r.place()); tr != transPop {
		t.Errorf("Back to the page that came in by →: %q, want a pop", tr)
	}

	r.send(ui.Msg{T: ui.MsgReset, Screen: &touchList})
	until(t, r.c, "the list alone", func() bool { return title(r.a) == "List" && len(r.a.stack) == 1 })
	var menu place
	_ = r.c.onLoop(func() error {
		r.c.GoHome()
		r.c.openSystem("sys.menu")
		return nil
	})
	menu = r.place()
	_ = r.c.onLoop(func() error {
		r.c.systemAction(r.c.sys.top(), &ui.Node{ID: "app:" + r.a.M.ID}, ui.Action{ID: "open"})
		return nil
	})
	opened := r.place()
	if tr := transitionFor(menu, opened); tr != transPush {
		t.Errorf("the app opened from the menu: %q, want a push", tr)
	}
	for _, back := range []func(){
		func() { r.tap(120, 26, TouchHit{Area: "title"}) },
		func() { _ = r.c.onLoop(func() error { r.c.Key(Named(KeyEsc)); return nil }) },
	} {
		_ = r.c.onLoop(func() error {
			r.c.systemAction(r.c.sys.top(), &ui.Node{ID: "app:" + r.a.M.ID}, ui.Action{ID: "open"})
			return nil
		})
		until(t, r.c, "the app in front", func() bool { return r.c.fg == r.a })
		r.drawn()
		before := r.place()
		back()
		until(t, r.c, "back on the menu", func() bool { return r.c.fg == nil && r.c.sys.top().S.ID == "sys.menu" })
		if tr := transitionFor(before, r.place()); tr != transPop {
			t.Errorf("Back out of the app to the menu: %q, want a pop", tr)
		}
	}
}

func (r *touchRig) place() place {
	var p place
	dev, _ := DeviceByID("bar-t9")
	_ = r.c.onLoop(func() error { p = r.c.placeOf(r.c.Frame(dev)); return nil })
	return p
}

// Without a layout of the page from the renderer, a finger dragged up
// moves the focus down a step each touchStep, the list following it;
// dragged back, up again; lifted, it is no tap. From the soft keys' bar a
// drag moves nothing.
func TestTouchDragMovesTheFocus(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	if f := r.focus(); f != "a" {
		t.Fatalf("focus on %q at first", f)
	}
	r.say("down", 120, 200, &TouchHit{Area: "body", Screen: r.screen(), Node: ip(0), Tile: ip(0)})
	r.say("move", 121, 190, nil)
	r.idle()
	if f := r.focus(); f != "a" {
		t.Fatalf("10 px moved the focus to %q", f)
	}
	r.say("move", 121, 200-touchStep-1, nil)
	r.idle()
	if f := r.focus(); f != "b" {
		t.Fatalf("a step up: focus on %q, want b", f)
	}
	r.say("move", 122, 200-2*touchStep-1, nil)
	r.idle()
	if f := r.focus(); f != "c" {
		t.Fatalf("two steps: focus on %q, want c", f)
	}
	r.say("move", 122, 200-touchStep-1, nil)
	r.say("up", 122, 200-touchStep-1, nil)
	r.noAction()
	if f := r.focus(); f != "b" {
		t.Fatalf("back a step: focus on %q, want b", f)
	}
	r.say("down", 120, 310, &TouchHit{Area: "soft", Soft: 1})
	r.say("move", 120, 200, nil)
	r.say("up", 120, 200, nil)
	r.noAction()
	if f := r.focus(); f != "b" {
		t.Fatalf("a drag from the soft keys moved the focus to %q", f)
	}
}

// A long press on a row puts the focus there and opens Options, the row's
// actions first, while the finger is still down; letting go does nothing
// more. A tap on its entry picks it.
func TestTouchLongPressAndAMenuEntry(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	r.say("down", 120, 90, &TouchHit{Area: "body", Screen: r.screen(), Node: ip(2), Tile: ip(0)})
	until(t, r.c, "Options while held", func() bool { m, ok := overlayOf[*menuOverlay](r.c); return ok && m.title == tr.T("Options") })
	r.say("up", 120, 90, nil)
	r.noAction()
	if f := r.focus(); f != "c" {
		t.Fatalf("focus on %q, want c", f)
	}
	var item int
	_ = r.c.onLoop(func() error {
		m, _ := overlayOf[*menuOverlay](r.c)
		for i, it := range m.frame(r.c).Items {
			if it.Text == "Share" {
				item = i
			}
		}
		return nil
	})
	if item == 0 {
		t.Fatal("no Share entry")
	}
	r.tap(120, 250, TouchHit{Area: "overlay", Kind: "menu", Item: ip(item)})
	r.action("c share")
	until(t, r.c, "the menu closed", func() bool { return len(r.c.overlays) == 0 })
}

// Power off and Restart tapped in the power menu are asked again, in a
// dialog that says which (report 008 §1.4: Power off tapped, the phone
// restarted); the screen going off closes the dialog as it does the menu.
// Chosen with the keys, they act at once.
func TestTouchPowerMenuAsksAgain(t *testing.T) {
	r := newTouchRig(t, "touch")
	stays := tr.T("Not a phone: it stays on")
	tapEntry := func(label string) {
		t.Helper()
		item := -1
		_ = r.c.onLoop(func() error {
			r.c.toast = nil
			r.c.openPowerMenu()
			m, _ := powerMenu(r.c)
			for i, it := range m.frame(r.c).Items {
				if it.Text == label {
					item = i
				}
			}
			return nil
		})
		if item < 0 {
			t.Fatalf("no %q in the power menu", label)
		}
		r.tap(120, 250, TouchHit{Area: "overlay", Kind: "menu", Item: ip(item)})
	}
	asked := func(text string) {
		t.Helper()
		until(t, r.c, "the question "+text, func() bool {
			d, ok := overlayOf[*confirmOverlay](r.c)
			if ok {
				r.c.overlayShown[d] = time.Now().Add(-time.Second)
			}
			return ok && d.text == text
		})
		_ = r.c.onLoop(func() error {
			if r.c.toast != nil && r.c.toast.body == stays {
				t.Error("powered off before the answer")
			}
			return nil
		})
	}
	for _, tc := range []struct{ entry, text string }{
		{tr.T("Power off"), tr.T("Power off the phone?")},
		{tr.T("Restart"), tr.T("Restart the phone?")},
	} {
		tapEntry(tc.entry)
		asked(tc.text)
		r.tap(120, 170, TouchHit{Area: "overlay", Kind: "confirm", Choice: ip(0)})
		until(t, r.c, tc.entry+" once answered", func() bool { return r.c.toast != nil && r.c.toast.body == stays })
	}

	tapEntry(tr.T("Power off"))
	asked(tr.T("Power off the phone?"))
	_ = r.c.onLoop(func() error {
		r.c.screenOff("timeout")
		if d, ok := overlayOf[*confirmOverlay](r.c); ok && d.power {
			t.Error("the screen went off over the question, which stayed")
		}
		r.c.Wake("test")
		r.c.toast = nil
		r.c.openPowerMenu()
		r.c.Key(Named(KeyDown))
		r.c.Key(Named(KeyEnter))
		if _, ok := overlayOf[*confirmOverlay](r.c); ok {
			t.Error("Power off chosen with the keys asked again")
		}
		if r.c.toast == nil || r.c.toast.body != stays {
			t.Error("Power off chosen with the keys did not power off")
		}
		return nil
	})
}

// A dialog's choices: the one selected is OK's, the last Back's, another is
// selected with the arrows first (the location permission's Approximate).
func TestTouchDialogChoices(t *testing.T) {
	r := newTouchRig(t, "touch")
	got := make(chan string, 4)
	confirm := func() *confirmOverlay {
		o := &confirmOverlay{text: "Delete it?", onYes: func() { got <- "yes" }, onNo: func() { got <- "no" }}
		_ = r.c.onLoop(func() error {
			r.c.pushOverlay(o)
			r.c.overlayShown[o] = time.Now().Add(-time.Second)
			return nil
		})
		return o
	}
	answer := func(want string) {
		t.Helper()
		select {
		case g := <-got:
			if g != want {
				t.Fatalf("answered %q, want %q", g, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no answer (want %q)", want)
		}
	}
	confirm()
	r.tap(180, 170, TouchHit{Area: "overlay", Kind: "confirm", Choice: ip(1)})
	answer("no")
	confirm()
	r.tap(60, 170, TouchHit{Area: "overlay", Kind: "confirm", Choice: ip(0)})
	answer("yes")
	confirm()
	r.tap(60, 140, TouchHit{Area: "overlay", Kind: "confirm"})
	r.tap(60, 170, TouchHit{Area: "overlay", Kind: "perm", Choice: ip(0)})
	r.idle()
	if len(got) != 0 || len(r.c.overlays) != 1 {
		t.Fatalf("the dialog's text or another kind's choice answered it")
	}
	r.tap(120, 30, TouchHit{Area: "outside", Kind: "confirm"})
	answer("no")
	decided := make(chan string, 1)
	p := &permOverlay{app: r.a, perms: []string{ui.PermLocation}, location: true}
	p.decide = func(yes bool) { decided <- fmt.Sprintf("%v %v", yes, p.approx) }
	_ = r.c.onLoop(func() error {
		r.c.pushOverlay(p)
		r.c.overlayShown[p] = time.Now().Add(-time.Second)
		return nil
	})
	r.tap(120, 200, TouchHit{Area: "overlay", Kind: "perm", Choice: ip(1)})
	select {
	case d := <-decided:
		if d != "true true" {
			t.Fatalf("decided %s, want approximate", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Approximate decided nothing")
	}
}

// A dialog that rose while the finger was down takes no answer from it,
// nor from one in its first moment; the locked keypad, a call (the phone
// at the ear) and the dark screen take no touch, and a renderer that did
// not say it reads one is not heard.
func TestTouchIsNotAnAnswerByChance(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newTouchRig(t, "touch")
	yes := make(chan bool, 4)
	o := &confirmOverlay{text: "Pay?", onYes: func() { yes <- true }}
	r.say("down", 60, 170, &TouchHit{Area: "body", Screen: r.screen()})
	r.idle()
	_ = r.c.onLoop(func() error { r.c.pushOverlay(o); return nil })
	r.say("up", 60, 170, nil)
	r.tap(60, 170, TouchHit{Area: "overlay", Kind: "confirm", Choice: ip(0)})
	r.idle()
	if len(yes) != 0 {
		t.Fatal("answered by a tap in its first moment")
	}
	_ = r.c.onLoop(func() error { r.c.popOverlay(o); r.c.keylock = true; return nil })
	r.tap(230, 310, TouchHit{Area: "soft", Soft: 2})
	r.noAction()
	_ = r.c.onLoop(func() error {
		r.c.keylock = false
		r.c.cells = map[string]ui.TelCall{"1": {ID: "1", State: "active"}}
		return nil
	})
	r.tap(230, 310, TouchHit{Area: "soft", Soft: 2})
	r.noAction()
	_ = r.c.onLoop(func() error { r.c.cells = nil; return nil })
	r.tap(230, 310, TouchHit{Area: "soft", Soft: 2})
	r.action("a new")
	_ = r.c.onLoop(func() error { r.c.screenOff("test"); return nil })
	r.tap(230, 310, TouchHit{Area: "soft", Soft: 2})
	r.noAction()
	var off bool
	_ = r.c.onLoop(func() error { off = r.c.ScreenOff(); return nil })
	if !off {
		t.Fatal("a touch lit the screen")
	}

	deaf := newTouchRig(t)
	deaf.tap(230, 310, TouchHit{Area: "soft", Soft: 2})
	deaf.noAction()
}

// touchSteps is the selftest's runner on phone-paint as built here (make
// paint; skipped without it or when it is older than its sources),
// headless as in the selftest, and a step of its script. The menu has 40
// apps, more than its screen shows.
func touchSteps(t *testing.T) (*Runner, func(cmd, arg string)) {
	t.Helper()
	paint := prebuilt.Paint(t)
	apps := t.TempDir()
	for i := range 40 {
		dir := filepath.Join(apps, fmt.Sprintf("a%02d", i))
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(fmt.Sprintf(`{"id": "com.test.a%02d", "name": "App %02d", "exec": "true"}`, i, i)), 0o644)
	}
	run, err := os.MkdirTemp("", "touch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	c, err := New(Config{W: 40, H: 24, AppsDir: apps, RunDir: run, StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Shutdown)
	dev, _ := DeviceByID("bar-t9")
	g, err := StartGUI(c, paint, dev, "--headless", "--snap-dir", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	r := &Runner{C: c, GUI: g, Out: io.Discard, Scale: 1}
	return r, func(cmd, arg string) {
		t.Helper()
		if err := r.step(cmd, arg); err != nil {
			t.Fatalf("%s %s: %v", cmd, arg, err)
		}
	}
}

// The selftest's touch steps: they aim on the frame's logical pixels, the
// renderer says what is there, the Shell acts. The first touch waits for
// the boot screen to go: a touch on it is on the boot screen, not on the
// standby screen under it.
func TestTouchStepsOnPhonePaint(t *testing.T) {
	if !alone(t) {
		return
	}
	r, step := touchSteps(t)
	c := r.C
	focus := func() string { return c.current().top().focus }
	shown := func() float64 {
		if v := c.current().top(); v.dragged {
			return v.dragOff
		}
		return -1
	}
	step("key", "f4")
	step("at", "sys.idle")
	step("touch", "tap 50% -6")
	step("at", "sys.menu")
	if got := focus(); got != "app:com.test.a00" {
		t.Fatalf("the menu opened on %q", got)
	}
	step("touch", "swipe 60 150 60 122")
	if got, off := focus(), shown(); got != "app:com.test.a03" || math.Abs(off-28) > 1 {
		t.Fatalf("a drag up of 28 px: the focus on %q, not the tile under the first, the menu at %v", got, off)
	}
	step("touch", "swipe 60 270 60 60")
	if got := focus(); got == "app:com.test.a03" || got == "" || shown() < 200 {
		t.Fatalf("dragged further, the focus is on %q, the menu at %v", got, shown())
	}
	step("touch", "swipe 60 50 60 290")
	if off := shown(); off != 0 {
		t.Fatalf("dragged back, the menu is at %v, not its top", off)
	}
	step("touch", "tap 50% 26")
	step("at", "sys.idle")
	if _, err := r.gestureOf("tap row Nothing here"); err == nil {
		t.Fatal("a row the screen does not have was aimed at")
	}
	for _, bad := range []string{"tap 1", "swipe 1 2 3", "pinch 1 2", "tap x 2", "tap bar 150%", "long row"} {
		if _, err := r.gestureOf(bad); err == nil {
			t.Errorf("touch %s: no error", bad)
		}
	}
}

// The touchscreen step takes the gesture it asked for as the device
// reports it. In QEMU the harness once answered the request before again
// (the heartbeat had quoted it) just ahead of the step's own: a touch that
// comes down elsewhere first is left alone, the step ends when its finger
// lifts and takes nothing after it, and a finger still down when it gives
// up is let go, not held into a long press.
func TestTouchscreenStepTakesItsOwnGesture(t *testing.T) {
	if !alone(t) {
		return
	}
	r, step := touchSteps(t)
	c := r.C
	c.settings.A11y.LongPress = 400
	step("key", "f4")
	step("at", "sys.idle")
	step("touch", "tap 50% -6")
	step("at", "sys.menu")
	fingers := make(chan fingerAt, 64)
	put := func(p input.Phase, x, y float64) { fingers <- fingerAt{p, x, y} }
	// The title bar, Back; the swipe asked for, the menu scrolled; the
	// next touch's finger.
	put(input.Down, 120, 26)
	put(input.Up, 120, 26)
	put(input.Down, 60, 150)
	for y := 146.5; y > 122; y -= 3.5 {
		put(input.Move, 60, y)
	}
	put(input.Up, 60, 122)
	put(input.Down, 120, 100)
	g, err := r.gestureOf("swipe 60 150 60 122")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.follow(g, fingers, func() {}, 3000); err != nil {
		t.Fatal(err)
	}
	r.pump(c.touchLong() + 300*time.Millisecond)
	switch v := c.current().top(); {
	case v.S.ID != "sys.menu":
		t.Fatalf("on %s, not the menu: the touch before the swipe was taken", v.S.ID)
	case !v.dragged || v.dragOff == 0:
		t.Fatal("the swipe scrolled nothing")
	case len(c.overlays) > 0:
		t.Fatal("the finger after the swipe was held into a long press")
	}
	fingers = make(chan fingerAt, 4)
	put(input.Down, 120, 100)
	if g, err = r.gestureOf("tap 120 100"); err != nil {
		t.Fatal(err)
	}
	if err := r.follow(g, fingers, func() {}, 300); err == nil {
		t.Fatal("a finger that never lifted ended the step")
	}
	r.pump(c.touchLong() + 300*time.Millisecond)
	if len(c.overlays) > 0 {
		t.Fatal("the finger the step gave up on was held into a long press")
	}
}

// Until its own finger comes down the touchscreen step says its request
// again as time passes (ask): the console can lose it or damage it past
// mending. Another touch's finger is not its own.
func TestTouchscreenAsksAgain(t *testing.T) {
	if !alone(t) {
		return
	}
	r := &Runner{C: snapCore(t), Out: io.Discard}
	g, err := r.gestureOf("tap 120 100")
	if err != nil {
		t.Fatal(err)
	}
	fingers := make(chan fingerAt, 4)
	fingers <- fingerAt{input.Down, 10, 10}
	asked := 0
	if err := r.follow(g, fingers, func() { asked++ }, 300); err == nil || errors.Is(err, errUntouched) {
		t.Fatalf("another touch's finger: %v", err)
	}
	if asked < 2 {
		t.Fatalf("asked again %d times in 300 ms with no finger of its own", asked)
	}
}
