package jsrt

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/jsrt/nativetest"
	"github.com/kario-os/kario/ui"
)

// slowFS is testdata/slowfs (nativetest.SlowFS): preloaded into a
// runtime, each fsync takes longer and says which thread made it.
type slowFS struct{ lib, log string }

func newSlowFS(t *testing.T) slowFS {
	t.Helper()
	return slowFS{lib: nativetest.SlowFS(t), log: filepath.Join(t.TempDir(), "fsync.log")}
}

// start starts main on the engine (argv: the program and what goes before
// the module) with each fsync delay longer.
func (f slowFS) start(t *testing.T, argv []string, data, main string, delay time.Duration) *shell {
	args := []string{"LD_PRELOAD=" + f.lib, "SLOWFS_LOG=" + f.log, "SLOWFS_DELAY_MS=" + strconv.FormatInt(delay.Milliseconds(), 10)}
	args = append(append(args, argv...), main)
	return launch(t, data, "en", "/usr/bin/env", args...)
}

// syncs is the thread of each fsync so far, in order.
func (f slowFS) syncs() []string {
	raw, _ := os.ReadFile(f.log)
	return strings.Fields(string(raw))
}

func jsrtArgv(t *testing.T) []string { return []string{binary(t)} }

func storageApp(t *testing.T) string { return filepath.Join(appCopy(t, "storage"), "main.js") }

func storageAction(id, value string) ui.Msg {
	return ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: id, Value: value}
}

// stored is key's JSON text in the storage file ("" without the file or
// the key); a file that is not whole JSON fails the test.
func stored(t *testing.T, data, key string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(data, StorageFile))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("a torn storage file: %v\n%s", err, raw)
	}
	return m[key]
}

// waitStored waits for the storage file to hold want as key's JSON text.
func waitStored(t *testing.T, data, key, want string) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); stored(t, data, key) != want; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("storage %s = %q, want %q", key, stored(t, data, key), want)
		}
	}
}

// ended waits for the process to end and returns how it ended.
func (s *shell) ended() syscall.WaitStatus {
	s.t.Helper()
	for range s.msgs {
	}
	select {
	case err := <-s.done:
		if err == nil {
			return 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.Sys().(syscall.WaitStatus)
		}
		s.t.Fatal(err)
	case <-time.After(10 * time.Second):
		s.t.Fatalf("app did not exit; log:\n%s", s.log())
	}
	return 0
}

// A save returns at once and its write (the fsync above all) runs on a
// thread of its own: on flash that takes 3 s a sync, the action's toast and
// the ping's pong come at once, not after the write (the Shell's ANR
// dialog), and get reads the new value meanwhile.
func TestStorageSlowWrite(t *testing.T) { testStorageSlowWrite(t, jsrtArgv(t)) }

func testStorageSlowWrite(t *testing.T, argv []string) {
	fs := newSlowFS(t)
	data := t.TempDir()
	s := fs.start(t, argv, data, storageApp(t), 3*time.Second)
	s.hello("", nil)
	s.screenText("storage")
	t0 := time.Now()
	s.send(storageAction("save", "x"))
	s.send(ui.Msg{T: ui.MsgPing, Value: "1"})
	var got []string
	for len(got) < 2 {
		select {
		case m, ok := <-s.msgs:
			if !ok {
				t.Fatalf("the app exited; log:\n%s", s.log())
			}
			switch m.T {
			case ui.MsgToast:
				got = append(got, m.Text)
			case ui.MsgPong:
				got = append(got, "pong "+m.Value)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("got %v; log:\n%s", got, s.log())
		}
	}
	if took := time.Since(t0); strings.Join(got, ",") != "saved x,pong 1" || took > 1500*time.Millisecond {
		t.Fatalf("got %v after %v (each fsync takes 3 s)", got, took)
	}
	waitStored(t, data, "v", `"x"`)
	if syncs := fs.syncs(); strings.Join(syncs, ",") != "worker" {
		t.Fatalf("fsyncs on %v, want one on a worker thread", syncs)
	}
}

// Saves made while a write is under way coalesce: the next write carries
// the newest content. Fifty saves in one handler take two writes at most,
// twenty actions saving one after the other a few; the file is whole at
// every look and what it holds never goes back.
func TestStorageCoalesces(t *testing.T) { testStorageCoalesces(t, jsrtArgv(t)) }

func testStorageCoalesces(t *testing.T, argv []string) {
	fs := newSlowFS(t)
	data := t.TempDir()
	s := fs.start(t, argv, data, storageApp(t), 300*time.Millisecond)
	s.hello("", nil)
	s.screenText("storage")
	s.send(storageAction("many", "50"))
	if m := s.expect(ui.MsgToast); m.Text != "set 50" {
		t.Fatalf("toast %q", m.Text)
	}
	waitStored(t, data, "n", "50")
	if n := len(fs.syncs()); n < 1 || n > 2 {
		t.Fatalf("%d writes for 50 saves in one handler, want the first one's and the rest's at most", n)
	}

	stop, seen := make(chan struct{}), make(chan []int, 1)
	go func() {
		var vals []int
		for {
			select {
			case <-stop:
				seen <- vals
				return
			default:
			}
			if raw, err := os.ReadFile(filepath.Join(data, StorageFile)); err == nil {
				var m map[string]string
				if json.Unmarshal(raw, &m) != nil {
					vals = append(vals, -1)
				} else if v, ok := m["v"]; ok {
					n, _ := strconv.Atoi(strings.Trim(v, `"`))
					vals = append(vals, n)
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()
	before := len(fs.syncs())
	for i := 1; i <= 20; i++ {
		s.send(storageAction("save", strconv.Itoa(i)))
	}
	for i := 1; i <= 20; i++ {
		if m := s.expect(ui.MsgToast); m.Text != "saved "+strconv.Itoa(i) {
			t.Fatalf("toast %q, want saved %d", m.Text, i)
		}
	}
	waitStored(t, data, "v", `"20"`)
	close(stop)
	vals := <-seen
	for i, v := range vals {
		if v < 0 {
			t.Fatalf("a torn storage file (look %d of %d)", i, len(vals))
		}
		if i > 0 && v < vals[i-1] {
			t.Fatalf("the file went back from %d to %d", vals[i-1], v)
		}
	}
	if n := len(fs.syncs()) - before; n > 4 {
		t.Fatalf("%d writes for 20 saves made during one write", n)
	}
}

// What was saved is on disk before the process acknowledges leaving:
// the state it hands over on "background" (the Shell may end it right
// after), its exit when the Shell closes the connection, and SIGTERM (the
// system shutting down) wait for the write under way; flush() resolves
// once it is done, and SIGTERM with nothing to write ends the process at
// once.
func TestStorageDurable(t *testing.T) { testStorageDurable(t, jsrtArgv(t)) }

func testStorageDurable(t *testing.T, argv []string) {
	fs := newSlowFS(t)
	main := storageApp(t)
	start := func(t *testing.T, value string) (*shell, string) {
		data := t.TempDir()
		s := fs.start(t, argv, data, main, time.Second)
		s.hello("", nil)
		s.screenText("storage")
		s.send(storageAction("save", value))
		if m := s.expect(ui.MsgToast); m.Text != "saved "+value {
			t.Fatalf("toast %q", m.Text)
		}
		return s, data
	}
	t.Run("background", func(t *testing.T) {
		s, data := start(t, "b1")
		s.send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateBackground})
		st := s.expect(ui.MsgState)
		if got := stored(t, data, "v"); got != `"b1"` {
			t.Fatalf("the state came before the write: storage %q", got)
		}
		if string(st.Data) != `{"v":"b1"}` {
			t.Fatalf("state %s", st.Data)
		}
	})
	t.Run("stop", func(t *testing.T) {
		s, data := start(t, "s1")
		s.conn.Close()
		if code := s.exit(); code != 0 {
			t.Fatalf("exit %d; log:\n%s", code, s.log())
		}
		if got := stored(t, data, "v"); got != `"s1"` {
			t.Fatalf("exited before the write: storage %q", got)
		}
	})
	t.Run("SIGTERM", func(t *testing.T) {
		s, data := start(t, "t1")
		if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if ws := s.ended(); !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
			t.Fatalf("ended %v; log:\n%s", ws, s.log())
		}
		if got := stored(t, data, "v"); got != `"t1"` {
			t.Fatalf("ended before the write: storage %q", got)
		}
	})
	t.Run("SIGTERM idle", func(t *testing.T) {
		s, data := start(t, "i1")
		waitStored(t, data, "v", `"i1"`)
		t0 := time.Now()
		if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if ws := s.ended(); !ws.Signaled() || ws.Signal() != syscall.SIGTERM || time.Since(t0) > 900*time.Millisecond {
			t.Fatalf("ended %v after %v (nothing left to write)", ws, time.Since(t0))
		}
	})
	t.Run("flush", func(t *testing.T) {
		data := t.TempDir()
		s := fs.start(t, argv, data, main, time.Second)
		s.hello("", nil)
		s.screenText("storage")
		s.send(storageAction("flush", "f1"))
		if m := s.expect(ui.MsgToast); m.Text != "flushed f1" {
			t.Fatalf("toast %q; log:\n%s", m.Text, s.log())
		}
		if got := stored(t, data, "v"); got != `"f1"` {
			t.Fatalf("flush resolved before the write: storage %q", got)
		}
	})
}

// A write that fails (a read-only data directory) does not fail the save,
// whose value the memory keeps: the runtime says so on its log, flush()
// rejects with the reason, and "background" still hands over the state.
func TestStorageWriteFails(t *testing.T) { testStorageWriteFails(t, jsrtArgv(t)) }

func testStorageWriteFails(t *testing.T, argv []string) {
	data := t.TempDir()
	if os.Geteuid() == 0 {
		// Root writes into a read-only directory, not over a directory.
		if err := os.Mkdir(filepath.Join(data, StorageFile+".tmp"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(data, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(data, 0o755) })
	s := launch(t, data, "en", argv[0], append(argv[1:], storageApp(t))...)
	s.hello("", nil)
	s.screenText("storage")
	s.send(storageAction("flush", "x"))
	if m := s.expect(ui.MsgToast); !strings.HasPrefix(m.Text, "flush failed x storage: ") {
		t.Fatalf("toast %q; log:\n%s", m.Text, s.log())
	}
	if !s.logHas(StorageNotSaved) {
		t.Fatalf("log:\n%s", s.log())
	}
	s.send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateBackground})
	if st := s.expect(ui.MsgState); string(st.Data) != `{"v":"x"}` {
		t.Fatalf("state %s", st.Data)
	}
	if _, err := os.Stat(filepath.Join(data, StorageFile)); err == nil {
		t.Fatal("a storage file in a read-only directory")
	}
}
