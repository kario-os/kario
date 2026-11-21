package shell

import (
	"os"
	"path/filepath"
	"reflect"
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
