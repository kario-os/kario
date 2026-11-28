package shell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPSITotal(t *testing.T) {
	psi := "some avg10=91.20 avg60=40.11 avg300=9.02 total=73912034\nfull avg10=88.41 avg60=38.90 avg300=8.70 total=71021991\n"
	if v, ok := psiTotal(psi, "full"); !ok || v != 71021991 {
		t.Fatalf("full = %v %v", v, ok)
	}
	if v, ok := psiTotal(psi, "some"); !ok || v != 73912034 {
		t.Fatalf("some = %v %v", v, ok)
	}
	// Kernels before 5.13 had no full line in the system's file; a
	// group's always has one.
	if _, ok := psiTotal("some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n", "full"); ok {
		t.Fatal("full without a full line")
	}
	if cgKey("low 0\nhigh 0\nmax 1234\noom 2\noom_kill 1\noom_group_kill 1\n", "max") != 1234 {
		t.Fatal("memory.events max")
	}
}

// stallSeries is a group sampled every period for d: its tasks all
// stalled share of the time, and its usage hitting the limit maxPerSec
// times a second.
func stallSeries(t0 time.Time, period, d time.Duration, share float64, maxPerSec int64) []stallSample {
	var out []stallSample
	for el := time.Duration(0); el <= d; el += period {
		out = append(out, stallSample{at: t0.Add(el), full: int64(float64(el.Microseconds()) * share), max: maxPerSec * el.Milliseconds() / 1000})
	}
	return out
}

// stalledAt feeds the samples to a watch: when it would end the app, or
// -1 if never.
func stalledAt(w *stallWatch, s []stallSample) time.Duration {
	for _, x := range s {
		if w.stalled(w.next(x)) {
			return x.at.Sub(s[0].at)
		}
	}
	return -1
}

// stallShares is a group at its limit sampled every period, its tasks all
// stalled shares[i] of the i-th interval, shares repeating.
func stallShares(t0 time.Time, period time.Duration, n int, shares ...float64) []stallSample {
	s := []stallSample{{at: t0}}
	for i := 0; i < n; i++ {
		last := s[len(s)-1]
		s = append(s, stallSample{at: last.at.Add(period), full: last.full + int64(float64(period.Microseconds())*shares[i%len(shares)]), max: last.max + 100})
	}
	return s
}

func TestStallWatch(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	tick, off := 500*time.Millisecond, 5*time.Second
	for _, c := range []struct {
		name    string
		samples []stallSample
		want    time.Duration
	}{
		// The memory limit case on QEMU while it thrashed: full stall
		// 49-69% of every 2 s, the limit hit hundreds of times a second.
		{"thrashing at the limit", stallSeries(t0, tick, 10*time.Second, 0.6, 700), stallFor},
		{"thrashing with dips", stallShares(t0, tick, 20, 0.6, 0.6, 0.3, 0.6), stallFor},
		{"just at the share", stallSeries(t0, tick, 10*time.Second, stallShare, 700), stallFor},
		{"screen off: one long interval", stallSeries(t0, off, 20*time.Second, 0.6, 700), off},
		// Reading files through a page cache at the limit on QEMU: the
		// limit hit all the time, full stall at most a fifth of 2 s.
		{"reading files at the limit", stallSeries(t0, tick, 30*time.Second, 0.2, 700), -1},
		{"a second stalled, then quiet", stallShares(t0, tick, 40, 0.9, 0.9, 0.05, 0.05, 0.05, 0.05), -1},
		{"stalled below the limit", stallSeries(t0, tick, 10*time.Second, 0.95, 0), -1},
		{"just below the share", stallSeries(t0, tick, 10*time.Second, stallShare-0.01, 700), -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := stalledAt(&stallWatch{trigger: -1}, c.samples); got != c.want {
				t.Fatalf("ended at %v, want %v", got, c.want)
			}
		})
	}

	// Once below its limit (max did not go up), the 2 s start again.
	s := stallSeries(t0, tick, 1500*time.Millisecond, 0.9, 700)
	last := s[len(s)-1]
	s = append(s, stallSample{at: last.at.Add(tick), full: last.full + 450_000, max: last.max})
	s = append(s, stallShares(last.at.Add(tick), tick, 4, 0.9)[1:]...)
	for i := len(s) - 4; i < len(s); i++ {
		s[i].full += last.full + 450_000
		s[i].max += last.max
	}
	if got := stalledAt(&stallWatch{trigger: -1}, s); got != 4*time.Second {
		t.Fatalf("below the limit for half a second: ended at %v, want 4s", got)
	}

	// A sample right after another is not judged; a group made afresh
	// (its counters back at zero) starts over, and so does a frozen one.
	w := &stallWatch{trigger: -1}
	s = stallSeries(t0, tick, 1500*time.Millisecond, 0.9, 700)
	last = s[len(s)-1]
	if stalledAt(w, s) != -1 || len(w.at) != 4 {
		t.Fatalf("after 1.5 s stalled: %d samples", len(w.at))
	}
	if d := w.next(stallSample{at: last.at.Add(50 * time.Millisecond), full: last.full + 50_000, max: last.max + 1}); d != 0 || len(w.at) != 4 {
		t.Fatalf("a sample 50 ms later judged: %v, %d samples", d, len(w.at))
	}
	if d := w.next(stallSample{at: last.at.Add(tick), full: 1000, max: 1}); d != 0 || len(w.at) != 1 {
		t.Fatalf("counters went back: %v, %d samples", d, len(w.at))
	}
	w.pause()
	if d := w.next(stallSample{at: last.at.Add(2 * tick), full: 2_000_000, max: 50}); d != 0 || len(w.at) != 1 {
		t.Fatalf("after a pause: %v, %d samples", d, len(w.at))
	}
}

// writeGroup lays out what a group's files say: memory.pressure's full
// total (and some, at least as much) and memory.events' max. Each file is
// replaced whole, as the kernel's always read whole.
func writeGroup(dir string, full, max int64) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, body := range map[string]string{
		"memory.pressure": fmt.Sprintf("some avg10=0.00 avg60=0.00 avg300=0.00 total=%d\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=%d\n", full+full/10, full),
		"memory.events":   fmt.Sprintf("low 0\nhigh 0\nmax %d\noom 0\noom_kill 0\noom_group_kill 0\n", max),
	} {
		tmp := filepath.Join(dir, "."+name)
		if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// An app whose tasks all wait on memory at its limit is ended by the
// Shell's housekeeping, and the user hears it went over its limit, as
// when the kernel's OOM killer ends it; an app reading files at its
// limit, with little stall, keeps running.
func TestAppStalledAtItsLimitIsEnded(t *testing.T) {
	if !alone(t) {
		return
	}
	c := loopCore(t, t.TempDir())
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "memory.pressure"), []byte("some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	start := func(id string) (*App, *exec.Cmd) {
		cmd := exec.Command("sleep", "60")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
		return &App{M: &Manifest{ID: id, Name: id}, running: true, ready: true, proc: cmd.Process}, cmd
	}
	hog, hogCmd := start("com.test.hog")
	reader, _ := start("com.test.reader")
	_ = c.onLoop(func() error {
		c.cg = &cgroups{base: base, feat: cgFeatures{Memory: true, PSI: true}}
		c.apps = append(c.apps, hog, reader)
		c.fg = hog
		return nil
	})
	// The kernel's counters grow all the time; these every 10 ms.
	t0 := time.Now()
	grow := func() error {
		el := time.Since(t0)
		if err := writeGroup(filepath.Join(base, "app."+hog.M.ID), el.Microseconds()*9/10, el.Milliseconds()*3); err != nil {
			return err
		}
		return writeGroup(filepath.Join(base, "app."+reader.M.ID), el.Microseconds()/5, el.Milliseconds()*3)
	}
	if err := grow(); err != nil {
		t.Fatal(err)
	}
	stop, grown := make(chan struct{}), make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				grown <- nil
				return
			case <-time.After(10 * time.Millisecond):
				if err := grow(); err != nil {
					grown <- err
					return
				}
			}
		}
	}()
	ended := func(a *App) (yes bool) { _ = c.onLoop(func() error { yes = a.killed; return nil }); return }
	for time.Since(t0) < 3*stallFor && !ended(hog) {
		_ = c.onLoop(func() error { c.checkMemory(); return nil })
		time.Sleep(onTick / 2)
	}
	close(stop)
	if err := <-grown; err != nil {
		t.Fatal(err)
	}
	if !ended(hog) {
		t.Fatalf("the app stalled at its limit runs on after %v", time.Since(t0))
	}
	if el := time.Since(t0); el < stallFor {
		t.Fatalf("ended after %v, before %v stalled", el, stallFor)
	}
	if ended(reader) {
		t.Fatal("the app reading files at its limit was ended")
	}
	err := hogCmd.Wait()
	_ = c.onLoop(func() error { c.appExited(hog, err); return nil })
	_ = c.onLoop(func() error {
		want := tr.T("“%s” went over its memory limit (%d MB) and was stopped by the system", hog.M.Name, appMemoryMax>>20)
		if c.toast == nil || c.toast.body != want {
			t.Errorf("toast %+v, want %q", c.toast, want)
		}
		if n := len(c.Crashes()); n != 0 {
			t.Errorf("%d crash records for a system kill", n)
		}
		return nil
	})
}
