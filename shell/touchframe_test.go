package shell

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/ui"
)

// The fake touch renderer's frame, in logical pixels: the status and
// title bars 20 high each, the body from fakeTop, the soft keys' bar
// fakeSoft high at the bottom and the on-screen keypad right above it.
const (
	fakeTop  = 43.0
	fakeSoft = 30.0
)

// fakeLayout is how the fake touch renderer lays out f on a w × h frame:
// the body's nodes 30 high, 60 when their text runs over 24 characters,
// 3 apart; the keypad's rows 30 high, 36 when a key has letters under its
// digit. cells are the keypad's keys and the gaps between them.
func fakeLayout(f *Frame, w, h float64) (l *Layout, cells []LayoutKey) {
	bottom := h - fakeSoft
	l = &Layout{Screen: f.Screen, Top: fakeTop, Left: 6, Width: w - 12}
	if k := f.Keypad; k != nil && len(k.Rows) > 0 {
		rh := 30.0
		for _, row := range k.Rows {
			for _, key := range row {
				if key.Sub != "" {
					rh = 36
				}
			}
		}
		bottom -= rh * float64(len(k.Rows))
		for i, row := range k.Rows {
			kw := w / float64(len(row))
			for j, key := range row {
				cell := LayoutKey{ID: key.ID, X: float64(j) * kw, Y: bottom + float64(i)*rh, W: kw, H: rh}
				cells = append(cells, cell)
				if key.ID != "" {
					l.Keys = append(l.Keys, cell)
				}
			}
		}
	}
	l.Area = bottom - fakeTop
	for _, n := range f.Body {
		nh := 30.0
		if utf8.RuneCountInString(n.Title+n.Text+n.Placeholder+n.Value) > 24 {
			nh = 60
		}
		l.Nodes = append(l.Nodes, LayoutNode{Y: l.Content, H: nh})
		l.Content += nh + 3
	}
	return l, cells
}

// fakeHit is what a touch at (x, y) is on, in f laid out as l.
func fakeHit(f *Frame, l *Layout, cells []LayoutKey, x, y, w, h float64) TouchHit {
	switch {
	case f.Boot != nil:
		return TouchHit{Area: "boot"}
	case f.Overlay != nil:
		return TouchHit{Area: "overlay", Kind: f.Overlay.Kind}
	case y >= h-fakeSoft:
		return TouchHit{Area: "soft", Soft: min(2, int(x/(w/3)))}
	case y < 20:
		return TouchHit{Area: "status"}
	case f.Idle != nil:
		return TouchHit{Area: "idle"}
	case y < 40 && f.Title != "":
		return TouchHit{Area: "title"}
	}
	for _, c := range cells {
		if x >= c.X && x < c.X+c.W && y >= c.Y && y < c.Y+c.H {
			return TouchHit{Area: "keypad", Key: c.ID}
		}
	}
	hit := TouchHit{Area: "body", Screen: f.Screen}
	for i, n := range l.Nodes {
		if top := l.Top + n.Y - l.Offset; y >= top && y < top+n.H && y < l.Top+l.Area {
			hit.Node, hit.Tile = ip(i), ip(0)
		}
	}
	return hit
}

// TestFakeTouchRenderer is not a test: run as a child process it plays a
// headless renderer that takes touches (features layout and touch) on a
// machine so busy that the layout of a frame that changed reaches the
// Shell PHONE_FAKE_TOUCH_LATE ms after it; a touch it answers at once, on
// the frame it has. A frame that differs from the one before only in its
// status bar it lays out at once, behind the late ones: else the clock's
// minute, every frame late, could keep the frame on screen ahead of the
// layouts. PHONE_FAKE_TOUCH "numbered" sends a layout for every frame and
// says which it is of; "plain", a renderer from before the frames'
// numbers, one when the geometry changed, without.
func TestFakeTouchRenderer(t *testing.T) {
	mode := os.Getenv("PHONE_FAKE_TOUCH")
	if mode == "" {
		t.Skip("helper process")
	}
	late, _ := strconv.Atoi(os.Getenv("PHONE_FAKE_TOUCH_LATE"))
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	sc.Scan()
	var hello struct{ Device Device }
	_ = json.Unmarshal(sc.Bytes(), &hello)
	w, h := float64(hello.Device.Width)/hello.Device.Scale, float64(hello.Device.Height)/hello.Device.Scale
	var mu sync.Mutex
	enc := json.NewEncoder(os.Stdout)
	say := func(m any) {
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(m)
	}
	say(map[string]any{"t": "ready", "proto": RendererProto, "renderer": map[string]string{"name": "fake-touch"}, "features": []string{"layout", "touch"}})
	type due struct {
		at  time.Time
		msg map[string]any
	}
	later := make(chan due, 1024)
	go func() {
		for d := range later {
			time.Sleep(time.Until(d.at))
			say(d.msg)
		}
	}()
	var f Frame
	var l, sent *Layout
	var cells []LayoutKey
	var before []byte
	n := 0
	for sc.Scan() {
		var m struct {
			T, Phase string
			X, Y     float64
		}
		_ = json.Unmarshal(sc.Bytes(), &m)
		switch m.T {
		case "frame":
			n++
			f = Frame{}
			_ = json.Unmarshal(sc.Bytes(), &f)
			l, cells = fakeLayout(&f, w, h)
			body := f
			body.Status = FrameStatus{}
			b, _ := json.Marshal(body)
			wait := time.Duration(late) * time.Millisecond
			if bytes.Equal(b, before) {
				wait = 0
			}
			before = b
			if mode == "plain" && reflect.DeepEqual(l, sent) {
				continue
			}
			sent = l
			msg := map[string]any{"t": "layout", "layout": l}
			if mode == "numbered" {
				msg["n"] = n
			}
			later <- due{time.Now().Add(wait), msg}
		case "touch":
			reply := map[string]any{"t": "touch", "phase": m.Phase, "x": m.X, "y": m.Y}
			if m.Phase == "down" && l != nil {
				reply["hit"] = fakeHit(&f, l, cells, m.X, m.Y, w, h)
			}
			say(reply)
		case "quit":
			os.Exit(0)
		}
	}
	os.Exit(0)
}

// lateLayout is the ms a busy renderer's layouts come late: longer than
// the fallback's quiet wait lasts at most (settleLayout, 10 × 150 ms at
// scale 1), shorter than a touch step waits for the layout (3 s).
const lateLayout = 2200

// fakeTouchSteps is the selftest's runner on the fake touch renderer in
// mode, its layouts late ms, with s on screen (appSteps), the boot screen
// gone: its frames, each laid out late, would keep the frame on screen
// ahead of the layouts.
func fakeTouchSteps(t *testing.T, mode string, late int, s ui.Screen) (*Runner, func(cmd, arg string), func(ui.Screen)) {
	t.Helper()
	t.Setenv("PHONE_FAKE_TOUCH", mode)
	t.Setenv("PHONE_FAKE_TOUCH_LATE", strconv.Itoa(late))
	r, step, show := appSteps(t, s, os.Args[0], "-test.run=^TestFakeTouchRenderer$")
	r.touchReady()
	return r, step, show
}

// hinted is a screen like the call's: a hint over three fields. A hint of
// more than 24 characters takes two lines in the fake touch renderer.
func hinted(hint string) ui.Screen {
	return ui.Screen{ID: "call", Title: "Call", Body: []ui.Node{
		{Kind: ui.KindText, ID: "hint", Text: hint},
		{Kind: ui.KindInput, ID: "first", Placeholder: "First"},
		{Kind: ui.KindInput, ID: "second", Placeholder: "Second"},
		{Kind: ui.KindInput, ID: "third", Placeholder: "Third"},
	}}
}

// comesTo waits for ok, as the runner goes on (a touch's answer is taken
// after the step).
func comesTo(t *testing.T, r *Runner, what string, ok func() bool) {
	t.Helper()
	if _, done := r.waitFor(func(string) bool { return ok() }, 3000); !done {
		t.Fatalf("not %s", what)
	}
}

func focusedOn(t *testing.T, r *Runner, id string) {
	t.Helper()
	comesTo(t, r, "focused on "+id, func() bool { return r.C.current().top().focus == id })
}

// The touch steps aim with the layout of the frame on screen, the last
// pushed, by the number the renderer gives it, however late it comes.
// With speakerphone on, the call screen swapped its two-line hint for a
// short one, as many nodes as before, and a step that aimed with the
// layout from before touched the row below (night pass 34). A frame of
// the same geometry as the one before has a layout of its own, which is
// waited for as well.
func TestTouchAimsAtTheFrameOnScreen(t *testing.T) {
	if !alone(t) {
		return
	}
	r, step, show := fakeTouchSteps(t, "numbered", lateLayout, hinted("Hold the phone to your ear to talk"))
	step("touch", "tap row Third")
	focusedOn(t, r, "third")
	show(hinted("Speaker"))
	step("wait", "Speaker")
	step("touch", "tap row First")
	focusedOn(t, r, "first")
	show(hinted("Speaker on"))
	step("wait", "Speaker on")
	step("touch", "tap row Second")
	focusedOn(t, r, "second")
}

// The on-screen keypad's keys too: the text keypad came up under the
// name, then the focus went to the time and the digits' keypad came up
// in its place, its rows lower (no letters under the digits); aimed with
// the keys from before, 2 touched the row above the keypad.
func TestSoftKeypadKeysAimAtTheFrameOnScreen(t *testing.T) {
	if !alone(t) {
		return
	}
	r, step, _ := fakeTouchSteps(t, "numbered", lateLayout, keypadForm)
	step("buttons-only", "on")
	step("keypad", "t9")
	step("soft-keypad", "text")
	step("touch", "tap row Time")
	step("soft-keypad", "digits")
	step("touch", "tap key 2")
	step("touch", "tap key 4")
	comesTo(t, r, "24 typed", func() bool { return r.C.current().top().inputs["time"] == "24" })
}

// A renderer from before the frames' numbers (its layouts without n, one
// only when the geometry changed) is aimed with as before: its last
// layout once none has come for a while, not one from before the keypad
// came or went.
func TestTouchAimsWithoutFrameNumbers(t *testing.T) {
	if !alone(t) {
		return
	}
	r, step, show := fakeTouchSteps(t, "plain", 0, hinted("Hold the phone to your ear to talk"))
	step("touch", "tap row Third")
	focusedOn(t, r, "third")
	show(hinted("Speaker"))
	step("wait", "Speaker")
	step("touch", "tap row First")
	focusedOn(t, r, "first")
	step("buttons-only", "on")
	step("keypad", "t9")
	step("soft-keypad", "text")
	step("touch", "tap row Second")
	focusedOn(t, r, "second")
	if l := r.C.layout; l == nil || l.numbered() {
		t.Fatalf("the layout %+v", l)
	}
}

// A renderer started again (the watchdog's, after a crash) counts frames
// from 1: the last layout of the one before, of a higher number, is not
// taken for the frame on screen.
func TestTouchAimsAfterTheRendererStartsAgain(t *testing.T) {
	if !alone(t) {
		return
	}
	r, step, show := fakeTouchSteps(t, "numbered", 0, hinted("Hold the phone to your ear to talk"))
	for _, row := range []string{"Third", "Second", "Third"} {
		step("touch", "tap row "+row)
	}
	t.Setenv("PHONE_FAKE_TOUCH_LATE", strconv.Itoa(lateLayout))
	dev, _ := DeviceByID("bar-t9")
	g, err := StartGUI(r.C, os.Args[0], dev, "-test.run=^TestFakeTouchRenderer$")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	r.GUI = g
	show(hinted("Speaker"))
	step("wait", "Speaker")
	step("touch", "tap row First")
	focusedOn(t, r, "first")
}

// A layout's n is the frame it is of; one from a renderer that is not the
// Shell's (one the watchdog replaced, a screenshot's: devshot.go) is not
// taken.
func TestTouchLayoutSaysItsFrame(t *testing.T) {
	r := newTouchRig(t, "touch")
	layout := func(w io.Writer, n int) {
		t.Helper()
		if _, err := fmt.Fprintf(w, `{"t":"layout","n":%d,"layout":{"screen":%q,"area":200,"offset":0,"top":43,"nodes":[{"y":0,"h":30}]}}`+"\n", n, r.screen()); err != nil {
			t.Fatal(err)
		}
		r.idle()
	}
	frame := func() int {
		n := -1
		_ = r.c.onLoop(func() error {
			if l := r.c.layout; l != nil {
				n = l.Frame
			}
			return nil
		})
		return n
	}
	layout(r.out, 7)
	if n := frame(); n != 7 {
		t.Fatalf("the layout is of frame %d, want 7", n)
	}
	goneR, goneW := io.Pipe()
	gone := &GUI{c: r.c, in: nopWriter{}, enc: json.NewEncoder(nopWriter{}), ready: make(chan guiMsg, 1), acks: make(chan guiMsg, 4), done: make(chan struct{})}
	go gone.read(goneR, r.c.frameVersions())
	t.Cleanup(func() {
		_ = goneW.Close()
		<-gone.done
	})
	layout(goneW, 99)
	if n := frame(); n != 7 {
		t.Fatalf("a layout from a renderer gone was taken: of frame %d", n)
	}
}
