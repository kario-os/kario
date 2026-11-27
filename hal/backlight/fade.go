package backlight

import (
	"math"
	"sync"
	"time"
)

// Fader moves a backlight to a new level over a short time instead of at
// once (docs/37-motion.md §3): the screen lighting up, dimming before it
// goes off, going dark, a brightness change. Each step is the same size to
// the eye (the level moves along CIE lightness, not linearly in light) and
// is written at the hardware's own resolution where it has finer steps
// than a percent. A new level mid-way starts from the level shown, so a
// fade never jumps. Level reports where the backlight is going.
type Fader struct {
	b  Backlight
	mu sync.Mutex
	// shown is the level on the hardware now, 0..1 of the maximum; lit
	// whether the hardware is on (still, while it fades out).
	shown  float64
	lit    bool
	target int
	on     bool
	// gen tells a fade from the one that replaced it.
	gen  uint64
	runs sync.WaitGroup
	// sleep waits between two steps (tests replace it).
	sleep func(time.Duration)
}

// fadeStep is the time between two writes: the panel's 60 Hz.
const fadeStep = 16 * time.Millisecond

// fine is a backlight that takes levels finer than a percent.
type fine interface {
	setFraction(f float64) error
}

// NewFader fades b, starting from its level now.
func NewFader(b Backlight) *Fader {
	p, on := b.Level()
	return &Fader{b: b, shown: float64(p) / 100, lit: on, target: p, on: on, sleep: time.Sleep}
}

// Set is the level at once.
func (f *Fader) Set(percent int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gen++
	f.target = min(max(percent, 0), 100)
	f.shown = float64(f.target) / 100
	return f.b.Set(f.target)
}

// Power is on or off at once.
func (f *Fader) Power(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gen++
	f.on, f.lit = on, on
	f.shown = float64(f.target) / 100
	if on {
		_ = f.b.Set(f.target)
	}
	return f.b.Power(on)
}

// Level is where the backlight is going: the last level asked for.
func (f *Fader) Level() (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.target, f.on
}

// Fade moves to percent over d, at once when d is not positive. Off (or
// fading out), it only keeps the level for FadeOn.
func (f *Fader) Fade(percent int, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	percent = min(max(percent, 0), 100)
	switch {
	case !f.on:
		f.target = percent
	case d <= 0:
		f.gen++
		f.target, f.shown = percent, float64(percent)/100
		_ = f.b.Set(percent)
	case f.target != percent || f.shown != float64(percent)/100:
		f.start(percent, d, false, nil)
	}
}

// FadeOn lights the backlight at its dimmest and fades it up to percent
// over d; still lit (fading out), it fades back from where it is.
func (f *Fader) FadeOn(percent int, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	percent = min(max(percent, 0), 100)
	if d <= 0 {
		f.gen++
		f.target, f.shown, f.on, f.lit = percent, float64(percent)/100, true, true
		_ = f.b.Set(percent)
		_ = f.b.Power(true)
		return
	}
	if !f.lit {
		f.shown, f.lit = 0, true
		f.write(0)
		_ = f.b.Power(true)
	}
	f.on = true
	f.start(percent, d, false, nil)
}

// FadeOff dims to the darkest level over d, then turns the backlight off
// and calls done (on its own goroutine), unless another call came first.
// Level says off at once; the level comes back with FadeOn. A backlight
// that is off already calls done at once; one still fading out calls the
// done of the fade it is in.
func (f *Fader) FadeOff(d time.Duration, done func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.on {
		if !f.lit && done != nil {
			go done()
		}
		return
	}
	f.on = false
	if d <= 0 || !f.lit {
		f.gen++
		f.lit = false
		_ = f.b.Power(false)
		if done != nil {
			go done()
		}
		return
	}
	f.start(0, d, true, done)
}

// start runs a fade from the level shown to `to` (percent); dark: to the
// darkest level, then off. Called with mu held.
func (f *Fader) start(to int, d time.Duration, dark bool, done func()) {
	f.gen++
	gen, from := f.gen, f.shown
	if !dark {
		f.target = to
	}
	n := max(1, int((d+fadeStep/2)/fadeStep))
	f.runs.Add(1)
	go func() {
		defer f.runs.Done()
		end := float64(to) / 100
		for i := 1; i <= n; i++ {
			f.sleep(d / time.Duration(n))
			f.mu.Lock()
			if f.gen != gen {
				f.mu.Unlock()
				return
			}
			switch {
			case i < n:
				f.shown = fadeCurve(from, end, float64(i)/float64(n), dark)
				f.write(f.shown)
			case dark:
				f.shown, f.lit = 0, false
				f.write(0)
				_ = f.b.Power(false)
			default:
				f.shown = end
				_ = f.b.Set(to)
			}
			f.mu.Unlock()
		}
		if done != nil {
			done()
		}
	}()
}

// write puts level (0..1) on the hardware as finely as it takes it.
func (f *Fader) write(level float64) {
	if fb, ok := f.b.(fine); ok {
		_ = fb.setFraction(level)
		return
	}
	_ = f.b.Set(int(math.Round(level * 100)))
}

// wait returns when no fade is running.
func (f *Fader) wait() { f.runs.Wait() }

// fadeCurve is the level at time t (0..1) of a fade from a to b along CIE
// lightness: eased out (quick to answer, settling gently), or eased in
// when going dark (what leaves eases in).
func fadeCurve(a, b, t float64, dark bool) float64 {
	t = min(max(t, 0), 1)
	if dark {
		t = t * t * t
	} else {
		t = 1 - math.Pow(1-t, 3)
	}
	la, lb := lightness(a), lightness(b)
	return luminance(la + (lb-la)*t)
}

// lightness is CIE 1976 L* (0..100) of a relative luminance y (0..1).
func lightness(y float64) float64 {
	y = min(max(y, 0), 1)
	if y <= 216.0/24389 {
		return y * 24389 / 27
	}
	return 116*math.Cbrt(y) - 16
}

// luminance is lightness's inverse.
func luminance(l float64) float64 {
	l = min(max(l, 0), 100)
	if l <= 8 {
		return l * 27 / 24389
	}
	return math.Pow((l+16)/116, 3)
}
