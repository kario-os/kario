package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// jsrtBinary is kario-jsrt as make jsrt builds it, or $KARIO_JSRT; without
// one these tests are skipped unless KARIO_JSRT_REQUIRED is set.
func jsrtBinary(t testing.TB) string {
	p := os.Getenv("KARIO_JSRT")
	if p == "" {
		p, _ = filepath.Abs(filepath.Join("..", "..", "build", "jsrt", jsrt.Program))
	}
	if _, err := os.Stat(p); err != nil {
		if os.Getenv("KARIO_JSRT_REQUIRED") != "" {
			t.Fatalf("no kario-jsrt at %s", p)
		}
		t.Skipf("no kario-jsrt at %s (make jsrt)", p)
	}
	return p
}

// shell stands in for phone-shell in front of a host running in this
// process: it keeps the host's page stack as the Shell does (a pop sends
// popped) and hands the test every message.
type shell struct {
	t     *testing.T
	h     *host
	conn  *wire.Conn
	msgs  chan ui.Msg
	mu    sync.Mutex
	stack []ui.Screen
	seen  []ui.Msg
	done  chan struct{}
}

const testHostID = "com.phone.miniapp"

// startHost runs a host with progs (the built-in ones when none) and
// waits for its home screen. The connection is inherited as the Shell
// hands it over: not close-on-exec until the host says so.
func startHost(t *testing.T, progs ...*program) *shell {
	t.Helper()
	bin := jsrtBinary(t)
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	syscall.CloseOnExec(fds[0])
	t.Setenv("PHONE_UI_FD", strconv.Itoa(fds[1]))
	t.Setenv("PHONE_APP_ID", testHostID)
	t.Setenv("PHONE_DATA_DIR", t.TempDir())
	app, err := sdk.New()
	if err != nil {
		t.Fatal(err)
	}
	closeOnExec()
	h := newHost(app)
	h.jsrt = bin
	if len(progs) == 0 {
		if err := h.load(); err != nil {
			t.Fatal(err)
		}
	} else {
		h.progs = progs
		h.announce()
	}
	s := &shell{t: t, h: h, conn: wire.New(os.NewFile(uintptr(fds[0]), "shell")), msgs: make(chan ui.Msg, 4096), done: make(chan struct{})}
	go func() {
		_ = h.run()
		close(s.done)
	}()
	go s.read()
	t.Cleanup(func() {
		s.on(func(h *host) { h.stopAll() })
		h.wg.Wait()
		s.on(func(*host) {})
		s.conn.Close()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Error("the host did not end")
		}
	})
	if m := s.expect(ui.MsgHello); m.App != testHostID {
		t.Fatalf("hello %+v", m)
	}
	s.send(ui.Msg{T: ui.MsgStart})
	s.expect(ui.MsgReset)
	return s
}

func (s *shell) read() {
	for {
		var m ui.Msg
		if err := s.conn.Recv(&m); err != nil {
			close(s.msgs)
			return
		}
		s.mu.Lock()
		s.seen = append(s.seen, m)
		switch m.T {
		case ui.MsgReset:
			s.stack = []ui.Screen{*m.Screen}
		case ui.MsgPush:
			s.stack = append(s.stack, *m.Screen)
		case ui.MsgUpdate:
			for i := range s.stack {
				if s.stack[i].ID == m.Screen.ID {
					s.stack[i] = *m.Screen
				}
			}
		case ui.MsgPop:
			if n := len(s.stack); n > 1 {
				id := s.stack[n-1].ID
				s.stack = s.stack[:n-1]
				_ = s.conn.Send(ui.Msg{T: ui.MsgPopped, ScreenID: id})
			}
		}
		s.mu.Unlock()
		s.msgs <- m
	}
}

func (s *shell) send(m ui.Msg) {
	s.t.Helper()
	if err := s.conn.Send(m); err != nil {
		s.t.Fatalf("send %s: %v", m.T, err)
	}
}

const wait = 5 * time.Second

// expect waits for the next message of type t, skipping others.
func (s *shell) expect(t string) ui.Msg {
	s.t.Helper()
	timeout := time.After(wait)
	for {
		select {
		case m, ok := <-s.msgs:
			if !ok {
				s.t.Fatalf("the host closed its connection waiting for %s", t)
			}
			if m.T == t {
				return m
			}
		case <-timeout:
			s.t.Fatalf("no %s message", t)
		}
	}
}

// page waits for a page message whose screen shows want.
func (s *shell) page(want string) ui.Screen {
	s.t.Helper()
	timeout := time.After(wait)
	for {
		select {
		case m, ok := <-s.msgs:
			if !ok {
				s.t.Fatalf("the host closed its connection waiting for %q", want)
			}
			if m.Screen != nil && strings.Contains(screenText(*m.Screen), want) {
				return *m.Screen
			}
		case <-timeout:
			s.t.Fatalf("no page with %q; top: %s", want, screenText(s.top()))
		}
	}
}

// toast waits for a toast that says want.
func (s *shell) toast(want string) string {
	s.t.Helper()
	timeout := time.After(wait)
	for {
		select {
		case m, ok := <-s.msgs:
			if !ok {
				s.t.Fatalf("the host closed its connection waiting for a toast %q", want)
			}
			if m.T == ui.MsgToast && strings.Contains(m.Text, want) {
				return m.Text
			}
		case <-timeout:
			s.t.Fatalf("no toast with %q; toasts: %v", want, s.toasts())
		}
	}
}

// req waits for a capability request the host makes.
func (s *shell) req(capName string) ui.Msg {
	s.t.Helper()
	for {
		if m := s.expect(ui.MsgReq); m.Cap == capName {
			return m
		}
	}
}

func (s *shell) reply(req int, data any, err string) {
	s.t.Helper()
	m := ui.Msg{T: ui.MsgRes, Req: req, OK: err == "", Err: err}
	if data != nil {
		m.Data, _ = json.Marshal(data)
	}
	s.send(m)
}

func (s *shell) top() ui.Screen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stack[len(s.stack)-1]
}

func (s *shell) depth() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.stack)
}

// waitDepth waits until the Shell holds n screens.
func (s *shell) waitDepth(n int) {
	s.t.Helper()
	deadline := time.Now().Add(wait)
	for s.depth() != n {
		if time.Now().After(deadline) {
			s.t.Fatalf("%d screens, want %d; toasts %v", s.depth(), n, s.toasts())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// toastsSay waits until toasts saying each of want have come, in any order.
func (s *shell) toastsSay(want ...string) {
	s.t.Helper()
	deadline := time.Now().Add(wait)
	for {
		missing := ""
		got := strings.Join(s.toasts(), "\n")
		for _, w := range want {
			if !strings.Contains(got, w) {
				missing = w
			}
		}
		if missing == "" {
			return
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("no toast with %q; toasts %v", missing, s.toasts())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *shell) toasts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.seen {
		if m.T == ui.MsgToast {
			out = append(out, m.Text)
		}
	}
	return out
}

func (s *shell) count(t string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.seen {
		if m.T == t {
			n++
		}
	}
	return n
}

// open starts a program from the home screen and waits for its index page.
func (s *shell) open(id string) ui.Screen {
	s.t.Helper()
	for len(s.msgs) > 0 {
		<-s.msgs
	}
	s.send(ui.Msg{T: ui.MsgAction, ScreenID: "home", Node: "p:" + id, Action: ui.ActionOpen})
	return s.pushed("mp:" + id + ":index:")
}

// pushed waits for a page pushed with an ID that starts with prefix.
func (s *shell) pushed(prefix string) ui.Screen {
	s.t.Helper()
	timeout := time.After(wait)
	for {
		select {
		case m, ok := <-s.msgs:
			if !ok {
				s.t.Fatalf("the host closed its connection waiting for %s", prefix)
			}
			if m.T == ui.MsgPush && strings.HasPrefix(m.Screen.ID, prefix) {
				return *m.Screen
			}
		case <-timeout:
			s.t.Fatalf("no page %s; toasts %v", prefix, s.toasts())
		}
	}
}

func (s *shell) act(screen, node, action, value string) {
	s.t.Helper()
	s.send(ui.Msg{T: ui.MsgAction, ScreenID: screen, Node: node, Action: action, Value: value})
}

// on runs f on the host's dispatch goroutine, where its state lives.
func (s *shell) on(f func(h *host)) {
	done := make(chan struct{})
	s.h.app.Post(func() {
		f(s.h)
		close(done)
	})
	select {
	case <-done:
	case <-time.After(wait):
		s.t.Fatal("the host did not run a function posted to it")
	}
}

// runtimePid is the process of the program's runtime, 0 when there is none.
func (s *shell) runtimePid(id string) int {
	pid := 0
	s.on(func(h *host) {
		if p := h.progByID(id); p != nil && p.run != nil {
			pid = p.run.cmd.Process.Pid
		}
	})
	return pid
}

func screenText(sc ui.Screen) string {
	parts := []string{sc.ID, sc.Title}
	for _, n := range sc.Body {
		parts = append(parts, n.Text, n.Title, n.Subtitle, n.Trailing, n.Placeholder)
	}
	return strings.Join(parts, " | ")
}

// testProgram is a program from files, read as an installed one is.
func testProgram(t *testing.T, id string, files fstest.MapFS) *program {
	t.Helper()
	p, err := readProgramFS(files, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// jsProgram is a program of one app.js with the permissions given.
func jsProgram(t *testing.T, id, js string, perms ...string) *program {
	t.Helper()
	app, _ := json.Marshal(map[string]any{"name": id, "permissions": perms})
	return testProgram(t, id, fstest.MapFS{"app.json": {Data: app}, "app.js": {Data: []byte(js)}})
}

// stored waits for the store in dir to hold want: kario-jsrt writes it on a
// thread of its own, after the save returned.
func stored(t *testing.T, dir, want string) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		raw, err := os.ReadFile(filepath.Join(dir, jsrt.StorageFile))
		if err == nil && strings.Contains(string(raw), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the store in %s: %s (%v), want %q", dir, raw, err, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// gone waits for pid to exit.
func gone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err != nil || strings.Contains(string(raw), ") Z ") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d still runs", pid)
}
