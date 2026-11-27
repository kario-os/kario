package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

func TestTransitionFor(t *testing.T) {
	idle := place{screen: "system/sys.idle", app: "system", depth: 1, idle: true}
	list := place{screen: "com.phone.chat/list", app: "com.phone.chat", depth: 1}
	thread := place{screen: "com.phone.chat/thread", app: "com.phone.chat", depth: 2}
	menu := thread
	menu.overlay = "menu"
	call := thread
	call.overlay = "incoming"
	other := place{screen: "com.phone.settings/root", app: "com.phone.settings", depth: 1}
	same := thread
	same.screen = "com.phone.chat/thread2"
	for _, c := range []struct {
		name      string
		prev, cur place
		want      string
	}{
		{"the first frame", place{}, idle, ""},
		{"boot", place{screen: "x", boot: true}, idle, ""},
		{"an app opened from standby", idle, list, transPush},
		{"deeper", list, thread, transPush},
		{"back", thread, list, transPop},
		{"home", thread, idle, transPop},
		{"another app", thread, other, transPush},
		{"a page replaced at the same depth", thread, same, ""},
		{"the same page", thread, thread, ""},
		{"a menu opens", thread, menu, transRise},
		{"it closes", menu, thread, transSink},
		{"an incoming call covers the screen", thread, call, ""},
		{"and goes", call, thread, ""},
	} {
		if got := transitionFor(c.prev, c.cur); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// An app that has not sent its first page is pushed in as the outline of
// one under its name (docs/37-motion.md §3); its first page then takes
// the outline's place, without a second push.
func TestAppSkeleton(t *testing.T) {
	c := loopCore(t, t.TempDir())
	dev, _ := DeviceByID("bar-t9")
	_ = c.onLoop(func() error {
		c.booted = time.Now().Add(-bootDuration)
		c.GoHome()
		before := c.placeOf(c.Frame(dev))
		a := &App{M: &Manifest{ID: "com.example.notes", Name: "Notes"}, running: true}
		c.fg = a
		f := c.Frame(dev)
		if !f.Skeleton || f.Title != "Notes" || f.Screen != "" || len(f.Body) != 0 || f.Loading == "" {
			t.Errorf("starting: %+v", f)
		}
		start := c.placeOf(f)
		if tr := transitionFor(before, start); tr != transPush {
			t.Errorf("opening it: %q", tr)
		}
		a.stack = []*View{newView(ui.Screen{ID: "main", Title: "Notes", Body: []ui.Node{{ID: "n", Kind: ui.KindText, Text: "Shopping"}}})}
		f = c.Frame(dev)
		if f.Skeleton || f.Loading != "" || len(f.Body) != 1 {
			t.Errorf("its first page: skeleton %v, loading %q, %d nodes", f.Skeleton, f.Loading, len(f.Body))
		}
		if tr := transitionFor(start, c.placeOf(f)); tr != "" {
			t.Errorf("its first page came in with %q", tr)
		}
		c.fg = nil
		return nil
	})
}

// An alarm's pulse (docs/37-motion.md §3): a renderer that runs the phase
// itself is sent its start and the period, so the frame stays the same
// while it rings and none is sent for it; another is sent the phase
// stepped, as before, and no period.
func TestOverlayPhase(t *testing.T) {
	c := &Core{}
	o := &alarmOverlay{entry: &alarmEntry{Alarm: ui.Alarm{Time: "07:00"}}, at: time.Now().Add(-1250 * time.Millisecond)}
	stepped := o.frame(c)
	if stepped.Period != 0 || stepped.Phase < 0.2 || stepped.Phase > 0.5 {
		t.Errorf("without the feature: phase %v, period %d", stepped.Phase, stepped.Period)
	}
	c.renderer = &RendererInfo{Features: []string{"snap", "phase"}}
	first := o.frame(c)
	time.Sleep(20 * time.Millisecond)
	later := o.frame(c)
	if first.Phase != 0 || first.Period != 1000 || !reflect.DeepEqual(first, later) {
		t.Errorf("with the feature: %+v, then %+v", first, later)
	}
	if p, ms := c.overlayPhase(time.Now().Add(-time.Second), scanCycle); p != 0 || ms != 2400 {
		t.Errorf("the scan line: phase %v, period %d", p, ms)
	}
}

// bumpCore is a Shell with a page of the system's in front, laid out.
func bumpCore(t *testing.T, cols int, body ...ui.Node) (*Core, *View) {
	c := loopCore(t, t.TempDir())
	v := newView(ui.Screen{ID: "test", Title: "List", Columns: cols, Body: body})
	v.focus = body[0].ID
	_ = c.onLoop(func() error {
		c.booted = time.Now().Add(-bootDuration)
		c.sys.stack = append(c.sys.stack, v)
		c.Render()
		return nil
	})
	return c, v
}

// pick is an action that leads nowhere: → on its node does nothing.
var pick = []ui.Action{{ID: "pick", Label: "Pick", Primary: true}}

func items(ids ...string) []ui.Node {
	var out []ui.Node
	for _, id := range ids {
		out = append(out, ui.Node{Kind: ui.KindItem, ID: id, Title: "Item " + id, Actions: pick})
	}
	return out
}

// pressed presses the keys in turn, the screen drawn after each, and says
// which way each bumped (docs/37-motion.md §3): "" where the key moved
// the focus or scrolled.
func pressed(c *Core, keys ...string) []string {
	var out []string
	_ = c.onLoop(func() error {
		for _, k := range keys {
			c.Key(Named(k))
			c.Render()
			b, s := c.takeBump(), ""
			if b != nil {
				s = fmt.Sprintf("%d,%d", b.DX, b.DY)
			}
			out = append(out, s)
		}
		return nil
	})
	return out
}

// The end of a list (docs/37-motion.md §3): a key that moves neither the
// focus nor the scroll is a bump that way; one that moves the focus or
// scrolls the page (text after the last item) is not. ↑ ↓ in a list, the
// first and last item keys and the page keys; in a grid → on its last
// tile (→ goes on to the next row, ← on the first goes back); each bump
// numbered anew.
func TestEndOfListBump(t *testing.T) {
	c, _ := bumpCore(t, 0, items("a", "b", "c")...)
	if got := pressed(c, KeyDown, KeyUp, KeyUp, KeyLast, KeyDown, KeyLast, KeyFirst, KeyFirst, KeyPageDown, KeyPageDown, KeyPageUp); fmt.Sprint(got) != fmt.Sprint([]string{"", "", "0,-1", "", "0,1", "0,1", "", "0,-1", "", "0,1", ""}) {
		t.Errorf("a list: %q", got)
	}
	var first, second uint64
	_ = c.onLoop(func() error {
		c.bump(0, 1)
		first = c.takeBump().N
		c.bump(0, 1)
		second = c.takeBump().N
		if c.takeBump() != nil {
			t.Error("a bump went out twice")
		}
		return nil
	})
	if second != first+1 {
		t.Errorf("numbered %d then %d", first, second)
	}

	text := append(items("a", "b"), ui.Node{Kind: ui.KindText, Text: strings.Repeat("A long paragraph after the last item. ", 40)})
	c, v := bumpCore(t, 0, text...)
	got := pressed(c, KeyDown)
	for i := 0; i < 40 && got[len(got)-1] == ""; i++ {
		got = append(got, pressed(c, KeyDown)...)
	}
	if len(got) < 3 || got[0] != "" || got[1] != "" || got[len(got)-1] != "0,1" || v.focus != "b" || v.scroll == 0 {
		t.Errorf("the text after the last item scrolls first, then bumps: %q, focus %q, scroll %d", got, v.focus, v.scroll)
	}

	tiles := make([]ui.Node, 5)
	for i := range tiles {
		tiles[i] = ui.Node{Kind: ui.KindItem, Style: ui.StyleTile, ID: fmt.Sprint("t", i), Title: fmt.Sprint(i), Actions: pick}
	}
	c, _ = bumpCore(t, 3, tiles...)
	if got := pressed(c, KeyRight, KeyRight, KeyRight, KeyDown, KeyRight, KeyDown); fmt.Sprint(got) != fmt.Sprint([]string{"", "", "", "", "1,0", "0,1"}) {
		t.Errorf("a grid (→ goes on to the next row): %q", got)
	}
	if got := pressed(c, KeyLeft, KeyLeft); got[1] != "" {
		t.Errorf("← at a grid's first column goes back, no bump: %q", got)
	}
}

// The screen reader says where the focus stopped (it says only what
// changes, and a bump changes nothing); on demand it says nothing.
func TestBumpSpoken(t *testing.T) {
	c, _ := bumpCore(t, 0, items("a", "b")...)
	_ = c.onLoop(func() error {
		if err := c.settingsSet(ui.SettingScreenReader, 1); err != nil {
			t.Fatal(err)
		}
		c.Render()
		return nil
	})
	_, n := c.spokenSince(0)
	pressed(c, KeyUp, KeyDown, KeyDown)
	if got := said(c, n); len(got) != 3 || got[0] != tr.T("Top of list") || !strings.HasPrefix(got[1], "Item b") || got[2] != tr.T("End of list") {
		t.Errorf("said %q", got)
	}
	_ = c.onLoop(func() error { return c.settingsSet(ui.SettingReaderOnDemand, 1) })
	_, n = c.spokenSince(0)
	pressed(c, KeyDown)
	if got := said(c, n); len(got) != 0 {
		t.Errorf("on demand it said %q", got)
	}
}

// The frame after a bump says so, once; the frames after it do not.
func TestBumpFrame(t *testing.T) {
	c, _ := bumpCore(t, 0, items("a", "b")...)
	dir := t.TempDir()
	frames, renderer := filepath.Join(dir, "frames"), filepath.Join(dir, "renderer")
	script := "#!/bin/sh\nread hello\n" +
		"echo '{\"t\":\"ready\",\"proto\":\"phone.frame/1\",\"renderer\":{\"name\":\"x\"},\"features\":[\"snap\"]}'\n" +
		"while read line; do echo \"$line\" >> " + frames + "; done\n"
	if err := os.WriteFile(renderer, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := StartGUI(c, renderer, Device{Width: 480, Height: 640, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, k := range []string{KeyUp, KeyDown, KeyDown, KeyDown} {
		_ = c.onLoop(func() error {
			c.Key(Named(k))
			c.Render()
			return g.Push()
		})
	}
	var got []string
	until(t, c, "four frames", func() bool {
		raw, _ := os.ReadFile(frames)
		got = nil
		for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var f Frame
			if json.Unmarshal([]byte(l), &f) == nil && f.T == "frame" {
				s := ""
				if f.Bump != nil {
					s = fmt.Sprintf("%d,%d,%d", f.Bump.DX, f.Bump.DY, f.Bump.N)
				}
				got = append(got, s)
			}
		}
		return len(got) == 4
	})
	if fmt.Sprint(got) != fmt.Sprint([]string{"0,-1,1", "", "0,1,2", "0,1,3"}) {
		t.Errorf("frames' bumps: %q", got)
	}
}

// tallCore: on the panel the pixel renderer is, a row and the focus on a
// block 900 pixels high in a body 200 high.
func tallCore(t *testing.T) (*Core, *View) {
	c, v := bumpCore(t, 0, ui.Node{Kind: ui.KindItem, ID: "a", Title: "A", Actions: pick}, ui.Node{Kind: ui.KindDoc, ID: "doc", Text: "Long", Actions: pick})
	dev, _ := DeviceByID("bar-t9")
	_ = c.onLoop(func() error {
		v.focus = "doc"
		c.Frame(dev)
		c.renderer = &RendererInfo{Features: []string{"kms", "layout"}}
		c.layout = &Layout{Screen: v.frameScreen, Area: 200, Nodes: []LayoutNode{{Y: 0, H: 26}, {Y: 29, H: 900}}}
		return nil
	})
	return c, v
}

// At the end of a tall block, the last focusable, ↓ keeps its end shown
// (it went back to the block's top, and down through it again).
func TestTallBlockEndStays(t *testing.T) {
	c, v := tallCore(t)
	at := []float64{}
	for range 8 {
		_ = c.onLoop(func() error {
			c.Key(Named(KeyDown))
			c.Render()
			at = append(at, v.pfrac)
			return nil
		})
	}
	if last := at[len(at)-3:]; last[0] != 1 || last[1] != 1 || last[2] != 1 {
		t.Errorf("down a 900 px block in 200 px, past its end: %v", at)
	}
}

// There each ↓ past the end is a bump; the steps through it are not.
func TestBumpAtATallBlocksEnd(t *testing.T) {
	c, v := tallCore(t)
	got := pressed(c, KeyDown)
	for i := 0; i < 10 && got[len(got)-1] == ""; i++ {
		got = append(got, pressed(c, KeyDown)...)
	}
	if len(got) < 5 || got[len(got)-1] != "0,1" || v.pfrac != 1 {
		t.Fatalf("down a 900 px block in 200 px: %q, at %v", got, v.pfrac)
	}
	if got := pressed(c, KeyDown, KeyUp); got[0] != "0,1" || got[1] != "" || v.pfrac >= 1 {
		t.Errorf("again, then up: %q, at %v", got, v.pfrac)
	}
}

// The selftest's report of the pixel renderer's steps: quantiles over
// every line, a half line (a renderer killed while writing) left out,
// and nothing judged.
func TestMotionReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "motion.jsonl")
	lines := `{"draw":[1,2,3],"transition":[]}
{"draw":[4,30],"transition":[0.1,0.2,0.3]}
{"draw":[5,6],"transi`
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := MotionReport(path)
	want := "5 steps drawn as on a panel (p50 3.00 ms, p95 30.00 ms, max 30.00 ms), 3 transition steps (p50 0.20 ms, p95 0.30 ms), 1 later than 25 ms; this machine's times, not checked"
	if err != nil || got != want {
		t.Errorf("got %q, %v\nwant %q", got, err, want)
	}
	if _, err := MotionReport(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("a renderer that timed nothing has a report")
	}
}

// Settings › Animations, and standard motion reduced on a low battery
// (not on a simulated one, nor while charging).
func TestMotionSetting(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		if m := c.motion(); m != "" {
			t.Errorf("default %q", m)
		}
		for _, s := range []struct {
			v    float64
			want string
		}{{1, motionReduced}, {2, motionOff}, {0, ""}} {
			if err := c.settingsSet(ui.SettingMotion, s.v); err != nil || c.motion() != s.want {
				t.Errorf("set %v: %q, %v", s.v, c.motion(), err)
			}
		}
		if err := c.settingsSet(ui.SettingMotion, 3); err == nil {
			t.Error("a fourth level")
		}
		c.powerOverride = &Power{Level: 9}
		if m := c.motion(); m != motionReduced {
			t.Errorf("9%%: %q", m)
		}
		c.powerOverride = &Power{Level: 9, Charging: true}
		if m := c.motion(); m != "" {
			t.Errorf("9%%, charging: %q", m)
		}
		c.powerOverride = &Power{Level: 9}
		_ = c.settingsSet(ui.SettingMotion, 2)
		if m := c.motion(); m != motionOff {
			t.Errorf("off stays off on a low battery: %q", m)
		}
		c.powerOverride = nil
		return nil
	})
}
