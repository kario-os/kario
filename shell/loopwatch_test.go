package shell

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a log that goroutines write and the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// screenLogTo has the Shell's screen lines (the journal's on a phone) go
// to a buffer for the test.
func screenLogTo(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	saved := ScreenLog
	ScreenLog = buf
	t.Cleanup(func() { ScreenLog = saved })
	return buf
}

func waitLog(t *testing.T, log *syncBuffer, want ...string) string {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		s := log.String()
		ok := true
		for _, w := range want {
			ok = ok && strings.Contains(s, w)
		}
		if ok {
			return s
		}
	}
	t.Fatalf("the log lacks %q:\n%s", want, log.String())
	return ""
}

// stuckForTheWatchdog is a call on the loop that does not return until
// released.
func stuckForTheWatchdog(release <-chan struct{}) { <-release }

// The loop held up (report 006: a sysfs read that never returned): the
// watchdog says so once, with where the loop is and the keys waiting for
// it, and again when it turns.
func TestTheWatchdogSaysWhereTheLoopIs(t *testing.T) {
	if !alone(t) {
		return
	}
	log := screenLogTo(t)
	savedBeat, savedStuck := loopBeat, loopStuck
	loopBeat, loopStuck = 20*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { loopBeat, loopStuck = savedBeat, savedStuck })
	c := loopCore(t, t.TempDir())
	c.WatchLoop()
	for end := time.Now().Add(3 * time.Second); c.loop.gid.Load() == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("no beat ran on the loop")
		}
	}
	release := make(chan struct{})
	c.Post(func() { stuckForTheWatchdog(release) })
	c.loop.keyIn(true)
	c.Post(c.loop.keyTaken)
	s := waitLog(t, log, "loop: has not turned for ", "shell.stuckForTheWatchdog", "1 keys from the renderer waiting", "the last power key")
	if !strings.Contains(s, "[chan receive") {
		t.Errorf("the loop's state is not named: %s", s)
	}
	t.Log(s)
	if n := strings.Count(s, "loop: has not turned"); n != 1 {
		t.Errorf("%d lines for one stall:\n%s", n, s)
	}
	close(release)
	waitLog(t, log, "loop: turned again after ")
}

// The power key is logged as it comes from the renderer, before the loop
// takes it: here no loop runs at all.
func TestThePowerKeyIsLoggedAsItComes(t *testing.T) {
	log := screenLogTo(t)
	apps := t.TempDir()
	_ = os.MkdirAll(filepath.Join(apps, "sys"), 0o755)
	_ = os.WriteFile(filepath.Join(apps, "sys", "manifest.json"), []byte(`{"id": "com.test.sys", "name": "Sys", "exec": "true"}`), 0o644)
	c, err := New(Config{W: 40, H: 24, AppsDir: apps, RunDir: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.stopping.Store(true) })
	r, w := io.Pipe()
	g := &GUI{c: c, ready: make(chan guiMsg, 1), acks: make(chan guiMsg, 4), done: make(chan struct{})}
	go g.read(r, c.frameVersions())
	raw, _ := json.Marshal(map[string]any{"t": "key", "key": "power", "text": "", "long": true, "ms": 650})
	_, _ = w.Write(append(raw, '\n'))
	_ = w.Close()
	<-g.done
	waitLog(t, log, "power key: from the renderer: long press (held 650 ms)")
	if n := c.loop.keys.Load(); n != 1 {
		t.Errorf("%d keys waiting for the loop, want 1", n)
	}
}
