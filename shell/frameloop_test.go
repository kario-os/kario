package shell

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// timedLines keeps when each line was sent to a renderer.
type timedLines struct {
	mu sync.Mutex
	at []time.Time
}

func (w *timedLines) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.at = append(w.at, time.Now())
	return len(p), nil
}

func (w *timedLines) Close() error { return nil }

func (w *timedLines) sent() []time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Time(nil), w.at...)
}

// framesCore is a Shell on a list with a renderer that keeps when frames
// came, and the device mode's loop running it (RunFrames).
func framesCore(t *testing.T) (*Core, *timedLines) {
	t.Helper()
	apps := t.TempDir()
	_ = os.MkdirAll(filepath.Join(apps, "sys"), 0o755)
	_ = os.WriteFile(filepath.Join(apps, "sys", "manifest.json"), []byte(`{"id": "com.test.sys", "name": "Sys", "exec": "true"}`), 0o644)
	c, err := New(Config{W: 40, H: 24, AppsDir: apps, RunDir: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	c.booted = time.Now().Add(-bootDuration)
	var ids []string
	for i := range 40 {
		ids = append(ids, strconv.Itoa(i))
	}
	v := newView(ui.Screen{ID: "test", Title: "List", Body: items(ids...)})
	v.focus = "0"
	c.sys.stack = append(c.sys.stack, v)
	w := &timedLines{}
	dev, _ := DeviceByID("bar-t9")
	c.gui = &GUI{c: c, dev: dev, in: w, enc: json.NewEncoder(w), info: RendererInfo{Features: []string{"patch"}},
		sent: map[string]string{}, ready: make(chan guiMsg, 1), acks: make(chan guiMsg, 4), done: make(chan struct{})}
	watch := &RendererWatch{c: c, G: c.gui}
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		watch.RunFrames(func(error) {})
	}()
	t.Cleanup(func() {
		c.Post(func() { c.quit = true })
		<-ended
	})
	return c, w
}

// framesAfter waits until more than n frames were sent and returns them.
func framesAfter(t *testing.T, w *timedLines, n int) []time.Time {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if s := w.sent(); len(s) > n {
			return s
		}
	}
	t.Fatalf("no frame after the %dth", n)
	return nil
}

// A key on an idle phone is sent to the renderer as soon as the Shell has
// handled it, not a frame period later (which every input waited before,
// report 008 §1.5).
func TestAnInputAfterIdleIsSentAtOnce(t *testing.T) {
	c, w := framesCore(t)
	framesAfter(t, w, 0)
	best := time.Hour
	for i := range 5 {
		time.Sleep(3 * framePeriod)
		n := len(w.sent())
		key := map[bool]string{false: KeyDown, true: KeyUp}[i%2 == 1]
		t0 := time.Now()
		c.Post(func() { c.Key(Named(key)) })
		best = min(best, framesAfter(t, w, n)[n].Sub(t0))
	}
	if best >= framePeriod/2 {
		t.Fatalf("a key's frame went %s after it at best, want at once (under %s)", best, framePeriod/2)
	}
}

// Keys coming faster than the panel's frames are sent at most one frame a
// period: the frames of a burst are merged, not sent one a key. The burst
// is judged by how long it really lasted, up to the frame that shows its
// last key: a busy machine stretches the sleeps between the keys.
func TestABurstOfInputsIsSentAFrameAPeriod(t *testing.T) {
	c, w := framesCore(t)
	framesAfter(t, w, 0)
	time.Sleep(3 * framePeriod)
	t0 := time.Now()
	n := len(w.sent())
	handled := make(chan time.Time, 1)
	for i := range 30 {
		c.Post(func() {
			c.Key(Named(KeyDown))
			if i == 29 {
				handled <- time.Now()
			}
		})
		time.Sleep(2 * time.Millisecond)
	}
	last := <-handled
	elapsed := last.Sub(t0)
	s := w.sent()[n:]
	for len(s) == 0 || s[len(s)-1].Before(last) {
		s = framesAfter(t, w, n+len(s))[n:]
	}
	s = s[:slices.IndexFunc(s, func(at time.Time) bool { return !at.Before(last) })+1]
	if len(s) > int(elapsed/framePeriod)+2 {
		t.Fatalf("%d frames for 30 keys in %s", len(s), elapsed.Round(time.Millisecond))
	}
	for i := 1; i < len(s); i++ {
		if gap := s[i].Sub(s[i-1]); gap < framePeriod {
			t.Errorf("frames %d and %d of the burst %s apart, under %s", i-1, i, gap, framePeriod)
		}
	}
}
