package shell

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// slowLog stands in for slow work on the Shell's loop for an app's message
// (a dictionary built on first use, a durable write on a busy disk): a log
// line naming slowMark takes slowFor.
type slowLog struct{}

const (
	slowMark = "slow-on-the-loop"
	slowFor  = 400 * time.Millisecond
)

func (slowLog) Write(p []byte) (int, error) {
	if strings.Contains(string(p), slowMark) {
		time.Sleep(slowFor)
	}
	return len(p), nil
}

// anrRig is a Shell whose foreground app the test plays over the app's
// connection (adopted from a "previous Shell"), its loop on a goroutine.
func anrRig(t *testing.T) (*Core, *App, *bufio.Reader, *os.File) {
	t.Helper()
	root := t.TempDir()
	apps := filepath.Join(root, "apps")
	dir := filepath.Join(apps, "test.slow")
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id":"test.slow","name":"Slow","exec":"none"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run, err := os.MkdirTemp("", "anr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	c, err := New(Config{W: 40, H: 24, AppsDir: apps, AppBinDir: filepath.Join(root, "bin"), RunDir: run, StateDir: filepath.Join(root, "state"),
		Log: slowLog{}, FreezeAfter: -1, ANRAfter: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case f := <-c.Events():
				f()
			case <-stop:
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = c.onLoop(func() error { c.Shutdown(); return nil })
		close(stop)
	})
	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	app, shell := os.NewFile(uintptr(fds[0]), "app"), os.NewFile(uintptr(fds[1]), "shell")
	t.Cleanup(func() { app.Close() })
	var a *App
	_ = c.onLoop(func() error {
		c.Adopt([]Inherited{{ID: "test.slow", Pid: sleep.Process.Pid, File: shell, Major: ui.ProtoMajor}})
		a = c.appByID("test.slow")
		c.setForeground(a)
		return nil
	})
	return c, a, bufio.NewReader(app), app
}

// nextPing reads what the Shell sends the app up to its next ping.
func nextPing(t *testing.T, rd *bufio.Reader, app *os.File) string {
	t.Helper()
	_ = app.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			t.Fatalf("no ping: %v", err)
		}
		var m ui.Msg
		if json.Unmarshal(line, &m) == nil && m.T == ui.MsgPing {
			return m.Value
		}
	}
}

func anrShown(c *Core) bool {
	for _, o := range c.overlays {
		if _, ok := o.(*anrOverlay); ok {
			return true
		}
	}
	return false
}

// A message whose handling holds the Shell's loop (Contacts' first save,
// which spells the name with a dictionary built on first use) makes the
// Shell rest before it reads the app on (guard.go): the app answered the
// ping at once, so that rest is not the app failing to answer. An app that
// does not answer is still reported.
func TestANRNotForTheShellsOwnRest(t *testing.T) {
	if !alone(t) {
		return
	}
	c, a, rd, app := anrRig(t)
	send := func(m any) {
		b, _ := json.Marshal(m)
		if _, err := app.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	_ = c.onLoop(func() error { c.probe(a); return nil })
	seq := nextPing(t, rd, app)
	send(map[string]any{"t": slowMark})
	// It answers once the Shell is done with the message, as Contacts
	// answers once its save is answered.
	time.Sleep(slowFor + 100*time.Millisecond)
	send(ui.Msg{T: ui.MsgPong, Value: seq})
	answered := time.Now().Add(slowFor*(1+appLoopShare) + 2*time.Second)
	for {
		var out, shown bool
		_ = c.onLoop(func() error { out, shown = !a.ping.sent.IsZero(), anrShown(c); return nil })
		if shown {
			t.Fatal("reported as not responding while the Shell held its answer back")
		}
		if !out {
			break
		}
		if time.Now().After(answered) {
			t.Fatal("the answer to the ping never reached the loop")
		}
		time.Sleep(20 * time.Millisecond)
	}

	_ = c.onLoop(func() error { c.probe(a); return nil })
	nextPing(t, rd, app)
	send(map[string]any{"t": slowMark + "-again"})
	until(t, c, "the ANR dialog for an app that does not answer", func() bool { return anrShown(c) })
}

// While the Shell's loop itself stands still (work for another app, a
// process starved of the CPU), the app's answer waits behind it: the ANR clock
// does not run on, so the first check after the stall, before the answer
// is read, does not report the app.
func TestANRNotForTheLoopsOwnStall(t *testing.T) {
	if !alone(t) {
		return
	}
	c, a, rd, app := anrRig(t)
	_ = c.onLoop(func() error { c.probe(a); return nil })
	seq := nextPing(t, rd, app)
	stalled := make(chan struct{})
	c.Post(func() {
		time.Sleep(3 * time.Second / 2)
		close(stalled)
	})
	<-stalled
	b, _ := json.Marshal(ui.Msg{T: ui.MsgPong, Value: seq})
	if _, err := app.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var out, shown bool
		_ = c.onLoop(func() error { out, shown = !a.ping.sent.IsZero(), anrShown(c); return nil })
		if shown {
			t.Fatal("reported as not responding for the time the Shell's loop stood still")
		}
		if !out {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the answer to the ping never reached the loop")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
