package main

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// fakeShell answers display.hint as the Shell does (shell/display.go): one
// kind of hint at a time, from a bucket of burst (ui.DisplayHintBurst when
// 0) refilled at ui.DisplayHintPerSecond, or from none when unlimited. A
// request reaches it latency after it was sent, many at once as over the
// app's socket; it takes busy over each, one at a time.
type fakeShell struct {
	latency, busy time.Duration
	burst         int
	unlimited     bool

	mu      sync.Mutex
	tokens  float64
	at      time.Time
	granted int
}

func (f *fakeShell) hint(args, out any) error {
	a := args.(ui.DisplayHintArgs)
	time.Sleep(f.latency)
	f.mu.Lock()
	defer f.mu.Unlock()
	time.Sleep(f.busy)
	set := 0
	for _, on := range []bool{a.AnimatingMS > 0, a.FPS > 0, a.Clear} {
		if on {
			set++
		}
	}
	if set != 1 {
		return errors.New(ui.CapDisplayHint + ": display.hint takes one of animating_ms, fps, clear")
	}
	burst := float64(f.burst)
	if f.burst == 0 {
		burst = ui.DisplayHintBurst
	}
	now := time.Now()
	if f.at.IsZero() {
		f.tokens = burst
	} else {
		f.tokens = min(burst, f.tokens+now.Sub(f.at).Seconds()*ui.DisplayHintPerSecond)
	}
	f.at = now
	if f.tokens < 1 && !f.unlimited {
		return errors.New(ui.CapDisplayHint + ": " + ui.ErrDisplayHintRate)
	}
	f.tokens--
	f.granted++
	if res, ok := out.(*ui.DisplayHintRes); ok {
		*res = ui.DisplayHintRes{Hz: 120, Rates: []int{60, 120}}
	}
	return nil
}

func (f *fakeShell) Call(_ string, args, out any) error { return f.hint(args, out) }

func (f *fakeShell) Go(_ string, args, out any, done func(error)) {
	go func() { done(f.hint(args, out)) }()
}

// The check decides by what the Shell granted, not by how fast it answers:
// a round trip of 300 ms (the emulator while the host builds) refills the
// bucket between answers, yet a Shell that limits passes and one that does
// not, or lets more through than its bucket holds, fails; a Shell that
// takes 300 ms over each hint refills it for every one and is not failed.
func TestDisplayCheck(t *testing.T) {
	const slow = 300 * time.Millisecond
	for _, c := range []struct {
		name  string
		shell *fakeShell
		ok    bool
	}{
		{"fast", &fakeShell{}, true},
		{"fast, no limit", &fakeShell{unlimited: true}, false},
		{"fast, a bucket of 12", &fakeShell{burst: 12}, false},
		{"300 ms a round trip", &fakeShell{latency: slow}, true},
		{"300 ms a round trip, no limit", &fakeShell{latency: slow, unlimited: true}, false},
		{"300 ms over each hint", &fakeShell{busy: slow}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if err := displayCheck(c.shell); (err == nil) != c.ok {
				t.Errorf("check: %v, with %d hints granted", err, c.shell.granted)
			}
		})
	}
}
