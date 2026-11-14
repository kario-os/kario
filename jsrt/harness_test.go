package jsrt

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// binary is kario-jsrt as make jsrt builds it, or $KARIO_JSRT. Without one
// the runtime's tests are skipped, unless KARIO_JSRT_REQUIRED is set (make
// test sets it when it built the runtime).
func binary(t testing.TB) string {
	p := os.Getenv("KARIO_JSRT")
	if p == "" {
		p, _ = filepath.Abs(filepath.Join("..", "build", "jsrt", Program))
	}
	if _, err := os.Stat(p); err != nil {
		if os.Getenv("KARIO_JSRT_REQUIRED") != "" {
			t.Fatalf("no kario-jsrt at %s", p)
		}
		t.Skipf("no kario-jsrt at %s (make jsrt)", p)
	}
	return p
}

// shell stands in for phone-shell: one app process on a socket pair.
type shell struct {
	t      testing.TB
	cmd    *exec.Cmd
	conn   *wire.Conn
	msgs   chan ui.Msg
	done   chan error
	mu     sync.Mutex
	stderr bytes.Buffer
}

type lockedWriter struct{ s *shell }

func (w lockedWriter) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	return w.s.stderr.Write(p)
}

// launch starts bin with args the way the Shell does (fd 3, PHONE_*).
func launch(t testing.TB, dataDir, lang string, bin string, args ...string) *shell {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	parent, child := os.NewFile(uintptr(fds[0]), "ui-parent"), os.NewFile(uintptr(fds[1]), "ui-child")
	s := &shell{t: t, msgs: make(chan ui.Msg, 256), done: make(chan error, 1)}
	s.cmd = exec.Command(bin, args...)
	s.cmd.ExtraFiles = []*os.File{child}
	s.cmd.Stderr = lockedWriter{s}
	s.cmd.Env = []string{"PHONE_UI_FD=3", "PHONE_APP_ID=com.example.test", "PHONE_DATA_DIR=" + dataDir, "PHONE_LANG=" + lang,
		"HOME=" + dataDir, "PATH=" + os.Getenv("PATH"), "GOMEMLIMIT=144MiB"}
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child.Close()
	s.conn = wire.New(parent)
	go func() {
		for {
			var m ui.Msg
			if err := s.conn.Recv(&m); err != nil {
				close(s.msgs)
				s.done <- s.cmd.Wait()
				return
			}
			s.msgs <- m
		}
	}()
	t.Cleanup(func() {
		_ = s.cmd.Process.Kill()
		s.conn.Close()
	})
	return s
}

func (s *shell) log() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stderr.String()
}

func (s *shell) send(m ui.Msg) {
	s.t.Helper()
	if err := s.conn.Send(m); err != nil {
		s.t.Fatalf("send %s: %v", m.T, err)
	}
}

// expect waits for the next message of type t, answering pings and
// skipping other kinds.
func (s *shell) expect(t string) ui.Msg {
	s.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-s.msgs:
			if !ok {
				s.t.Fatalf("app exited waiting for %s; log:\n%s", t, s.log())
			}
			if m.T == t {
				return m
			}
		case <-timeout:
			s.t.Fatalf("no %s message; log:\n%s", t, s.log())
		}
	}
}

// screenText waits for a page message (reset, push, update, replace)
// whose screen shows want, and returns the screen's text.
func (s *shell) screenText(want string) string {
	s.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-s.msgs:
			if !ok {
				s.t.Fatalf("app exited waiting for %q; log:\n%s", want, s.log())
			}
			if m.Screen == nil {
				continue
			}
			text := screenString(m.Screen)
			if strings.Contains(text, want) {
				return text
			}
		case <-timeout:
			s.t.Fatalf("no screen with %q; log:\n%s", want, s.log())
		}
	}
}

func screenString(sc *ui.Screen) string {
	parts := []string{sc.ID, sc.Title}
	for _, n := range sc.Body {
		parts = append(parts, n.Text, n.Title, n.Subtitle, n.Placeholder)
	}
	return strings.Join(parts, " | ")
}

// hello waits for the app's hello and starts it, as the Shell does.
func (s *shell) hello(arg string, data any) {
	s.t.Helper()
	h := s.expect(ui.MsgHello)
	if h.Proto != ui.Proto || h.App != "com.example.test" {
		s.t.Fatalf("hello %+v", h)
	}
	m := ui.Msg{T: ui.MsgStart, Value: arg}
	if data != nil {
		m.Data, _ = json.Marshal(data)
	}
	s.send(m)
	s.send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateForeground})
}

// exit waits for the process to end and returns its exit code.
func (s *shell) exit() int {
	s.t.Helper()
	for range s.msgs {
	}
	select {
	case err := <-s.done:
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		if err != nil {
			s.t.Fatal(err)
		}
		return 0
	case <-time.After(5 * time.Second):
		s.t.Fatalf("app did not exit; log:\n%s", s.log())
	}
	return -1
}
