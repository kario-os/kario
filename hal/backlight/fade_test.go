package backlight

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kario-os/kario/hal"
)

func TestFadeCurve(t *testing.T) {
	for _, y := range []float64{0, 0.001, 0.008, 0.05, 0.2, 0.5, 1} {
		if got := luminance(lightness(y)); math.Abs(got-y) > 1e-9 {
			t.Errorf("luminance(lightness(%v)) = %v", y, got)
		}
	}
	for _, dark := range []bool{false, true} {
		if a, b := fadeCurve(0.8, 0.2, 0, dark), fadeCurve(0.8, 0.2, 1, dark); math.Abs(a-0.8) > 1e-9 || math.Abs(b-0.2) > 1e-9 {
			t.Errorf("dark %v: ends %v, %v", dark, a, b)
		}
		prev := 1.0
		for i := 0; i <= 100; i++ {
			v := fadeCurve(1, 0.01, float64(i)/100, dark)
			if v > prev+1e-12 {
				t.Fatalf("dark %v: brighter at %d%% of the way", dark, i)
			}
			prev = v
		}
	}
	// Half way to the eye is far below half the light: a linear fade would
	// look like it waits and then drops.
	if mid := luminance((lightness(1) + lightness(0.01)) / 2); mid > 0.25 {
		t.Errorf("the lightness midpoint of 100%% and 1%% is %v of the light", mid)
	}
	if v := fadeCurve(0.2, 1, 0.25, false); lightness(v)-lightness(0.2) < 0.5*(lightness(1)-lightness(0.2)) {
		t.Errorf("lighting up eases out: a quarter of the time is only %.0f%%", v*100)
	}
	if v := fadeCurve(0.2, 0, 0.25, true); lightness(0.2)-lightness(v) > 0.1*lightness(0.2) {
		t.Errorf("going dark eases in: a quarter of the time is already at %.1f%%", v*100)
	}
}

// fakePanel is a backlight class device in a temporary sysfs tree, 4095
// steps, lit at full brightness.
func fakePanel(t *testing.T) (dir string, s *Sysfs) {
	t.Helper()
	root := t.TempDir()
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	dir = filepath.Join(root, "class", "backlight", "wled")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "max_brightness"), []byte("4095\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "brightness"), []byte("4095\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "bl_power"), []byte("0\n"), 0o644)
	return dir, Open("wled")
}

func file(t *testing.T, dir, name string) string {
	t.Helper()
	raw, _ := os.ReadFile(filepath.Join(dir, name))
	return strings.TrimSpace(string(raw))
}

// recorder logs every level written to the backlight under it (0..1).
type recorder struct {
	*Sysfs
	mu  sync.Mutex
	got []float64
}

func (r *recorder) log(v float64) {
	r.mu.Lock()
	r.got = append(r.got, v)
	r.mu.Unlock()
}

func (r *recorder) Set(p int) error             { r.log(float64(p) / 100); return r.Sysfs.Set(p) }
func (r *recorder) setFraction(f float64) error { r.log(f); return r.Sysfs.setFraction(f) }

func (r *recorder) writes() []float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]float64(nil), r.got...)
}

// waitWrites waits until n levels have been written.
func (r *recorder) waitWrites(t *testing.T, n int) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); len(r.writes()) < n; {
		if time.Now().After(end) {
			t.Fatalf("%d writes, want %d", len(r.writes()), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// gate lets a fade take its steps only when the test says.
type gate struct {
	mu      sync.Mutex
	c       *sync.Cond
	allowed int
}

func newGate() *gate {
	g := &gate{}
	g.c = sync.NewCond(&g.mu)
	return g
}

func (g *gate) sleep(time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.allowed == 0 {
		g.c.Wait()
	}
	g.allowed--
}

func (g *gate) step(n int) {
	g.mu.Lock()
	g.allowed += n
	g.c.Broadcast()
	g.mu.Unlock()
}

// Dimming from full to 20% over 300 ms: a step per frame, each smaller
// than a quarter of the way to the eye, eased out, ending exactly at what
// Set(20) writes; Level says 20% from the start.
func TestFadeSteps(t *testing.T) {
	dir, s := fakePanel(t)
	r := &recorder{Sysfs: s}
	f := NewFader(r)
	var slept time.Duration
	f.sleep = func(d time.Duration) { slept += d }
	f.Fade(20, 300*time.Millisecond)
	if p, on := f.Level(); p != 20 || !on {
		t.Errorf("Level() = %d, %v while fading", p, on)
	}
	f.wait()
	got := r.writes()
	if len(got) != 19 || slept < 290*time.Millisecond || slept > 300*time.Millisecond {
		t.Fatalf("%d steps over %v, want 19 over 300 ms", len(got), slept)
	}
	if v := file(t, dir, "brightness"); v != "819" {
		t.Errorf("ends at %s, want 819 (20%% of 4095)", v)
	}
	prev, way := 1.0, lightness(1)-lightness(0.2)
	for i, v := range got {
		step := lightness(prev) - lightness(v)
		if step < 0 || step > way/4 {
			t.Errorf("step %d: %.3f -> %.3f (%.0f%% of the way)", i, prev, v, 100*step/way)
		}
		prev = v
	}
	if first := lightness(1) - lightness(got[0]); first < way/10 {
		t.Errorf("the first step is only %.0f%% of the way", 100*first/way)
	}
	f.Fade(55, 0)
	if v := file(t, dir, "brightness"); v != "2252" {
		t.Errorf("no time: %s at once, want 2252", v)
	}
}

// A new level mid-way starts from the level shown: no jump back, and the
// old fade writes nothing more.
func TestFadeInterrupted(t *testing.T) {
	dir, s := fakePanel(t)
	r := &recorder{Sysfs: s}
	f := NewFader(r)
	g := newGate()
	f.sleep = g.sleep
	f.Fade(10, 160*time.Millisecond)
	g.step(4)
	r.waitWrites(t, 4)
	at := r.writes()[3]
	if at >= 1 || at <= 0.1 {
		t.Fatalf("4 steps of 10 to 10%%: %v", at)
	}
	f.Fade(100, 160*time.Millisecond)
	g.step(100)
	f.wait()
	got := r.writes()[4:]
	if len(got) != 10 {
		t.Errorf("%d writes after the new level, want its 10", len(got))
	}
	prev := at
	for i, v := range got {
		if v < prev {
			t.Errorf("step %d of the new fade: %v after %v", i, v, prev)
		}
		prev = v
	}
	if got[0] > 0.9 {
		t.Errorf("the new fade jumped to %v", got[0])
	}
	if v := file(t, dir, "brightness"); v != "4095" {
		t.Errorf("ends at %s", v)
	}
}

// Going dark: Level says off at once, the light fades to the darkest
// level and only then is switched off; coming back it is lit at the
// darkest and fades up.
func TestFadeOffAndOn(t *testing.T) {
	dir, s := fakePanel(t)
	f := NewFader(s)
	done := make(chan struct{})
	g := newGate()
	f.sleep = g.sleep
	f.FadeOff(300*time.Millisecond, func() { close(done) })
	if _, on := f.Level(); on {
		t.Error("Level says on while going dark")
	}
	if v := file(t, dir, "bl_power"); v != "0" {
		t.Errorf("switched off before fading: bl_power %s", v)
	}
	g.step(100)
	<-done
	f.wait()
	if p, b := file(t, dir, "bl_power"), file(t, dir, "brightness"); p != "4" || b != "1" {
		t.Errorf("after the fade: bl_power %s, brightness %s", p, b)
	}
	f.FadeOn(80, 150*time.Millisecond)
	if p, b := file(t, dir, "bl_power"), file(t, dir, "brightness"); p != "0" || b != "1" {
		t.Errorf("lighting up starts at the darkest: bl_power %s, brightness %s", p, b)
	}
	if lv, on := f.Level(); lv != 80 || !on {
		t.Errorf("Level() = %d, %v", lv, on)
	}
	g.step(100)
	f.wait()
	if b := file(t, dir, "brightness"); b != "3276" {
		t.Errorf("lit at %s, want 3276 (80%%)", b)
	}
	f.FadeOff(0, nil)
	f.Fade(40, time.Second)
	if p, b := file(t, dir, "bl_power"), file(t, dir, "brightness"); p != "4" || b != "3276" {
		t.Errorf("a level while off only waits for FadeOn: bl_power %s, brightness %s", p, b)
	}
	if lv, on := f.Level(); lv != 40 || on {
		t.Errorf("Level() = %d, %v while off", lv, on)
	}
	f.FadeOn(0, 0)
	if lv, _ := f.Level(); lv != 0 || file(t, dir, "brightness") != "1" {
		t.Errorf("FadeOn(0, 0): %d%%, brightness %s", lv, file(t, dir, "brightness"))
	}
}

// A key while the screen is going dark: the light turns round from where
// it is, never off, and the off callback does not run.
func TestWakeWhileFadingOut(t *testing.T) {
	dir, s := fakePanel(t)
	r := &recorder{Sysfs: s}
	f := NewFader(r)
	g := newGate()
	f.sleep = g.sleep
	var called atomic.Bool
	f.FadeOff(320*time.Millisecond, func() { called.Store(true) })
	g.step(8)
	r.waitWrites(t, 8)
	at := r.writes()[7]
	if at >= 1 || at < 0.5 {
		t.Fatalf("8 of 20 steps going dark, eased in: %v", at)
	}
	f.FadeOn(90, 160*time.Millisecond)
	g.step(100)
	f.wait()
	if called.Load() {
		t.Error("the off callback ran after a wake")
	}
	if v := file(t, dir, "bl_power"); v != "0" {
		t.Errorf("bl_power %s", v)
	}
	got := r.writes()[8:]
	if len(got) == 0 || got[0] < at {
		t.Errorf("turned round from %v to %v", at, got)
	}
	if v := file(t, dir, "brightness"); v != "3686" {
		t.Errorf("ends at %s, want 3686 (90%%)", v)
	}
}

// An LED backlight lit by FadeOn starts at its darkest, not at the level
// it had before it went off.
func TestFadeLED(t *testing.T) {
	root := t.TempDir()
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	d := fakeLED(t, root, "pm8xxx::kbd_backlight", "255", "200")
	l := OpenLED("pm8xxx::kbd_backlight")
	f := NewFader(l)
	g := newGate()
	f.sleep = g.sleep
	f.Power(false)
	f.FadeOn(50, 64*time.Millisecond)
	if b := brightness(t, d); b != "1" {
		t.Errorf("lit at %s, want the darkest (1)", b)
	}
	g.step(100)
	f.wait()
	if b := brightness(t, d); b != "128" {
		t.Errorf("faded to %s, want 128", b)
	}
}
