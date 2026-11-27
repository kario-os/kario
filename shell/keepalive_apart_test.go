package shell

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// keep-server's servers count the lines and the connections the same
// way: a connection the app closed is no longer open. A reset ends the
// open ones with an error on the app's side.
func TestKeepServerLines(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	in := &keepServer{ln: ln}
	go in.serve()
	apart, err := startKeepApart("127.0.0.1:0", 1)
	if err != nil {
		t.Fatal(err)
	}
	outside, err := startKeepOutside("127.0.0.1:0", filepath.Join(t.TempDir(), "keep.sock"), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]keepLines{"in the Shell": in, "apart": apart, "outside": outside} {
		t.Run(name, func(t *testing.T) {
			defer s.close()
			if err := s.send("x"); err == nil {
				t.Error("sent with nothing connected")
			}
			if err := s.reset(); err == nil {
				t.Error("reset with nothing connected")
			}
			conn := keepDial(t, s, "HELLO")
			keepLinesGot(t, s, "HELLO", 1)
			if err := s.send("hello there"); err != nil {
				t.Fatal(err)
			}
			keepRead(t, conn, "hello there")
			if n, open, err := s.accepted(); n != 1 || open != 1 || err != nil {
				t.Fatalf("accepted %d, %d open (%v), want 1 open", n, open, err)
			}
			conn.Close()
			conn = keepDial(t, s, "HELLO")
			keepLinesGot(t, s, "HELLO", 2)
			keepOpen(t, s, 2, 1)
			if err := s.reset(); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, syscall.ECONNRESET) {
				t.Errorf("the app read %v after the reset, want a reset", err)
			}
			keepOpen(t, s, 2, 0)
			if err := s.send("x"); err == nil {
				t.Error("sent after the reset")
			}
		})
	}
}

// The helper outlives the runner that started it when an in-place
// restart carries the control over: the next Shell's runner reaches the
// same server, which still has the same connection. Told to, or once its
// control closes, the helper ends.
func TestKeepServerApartCarried(t *testing.T) {
	k, err := startKeepApart("127.0.0.1:0", 1)
	if err != nil {
		t.Fatal(err)
	}
	if k.proc.Pid == os.Getpid() || !processAlive(k.proc.Pid) {
		t.Fatalf("no helper process: pid %d", k.proc.Pid)
	}
	conn := keepDial(t, k, "HELLO")
	keepLinesGot(t, k, "HELLO", 1)
	c, err := k.carry()
	if err != nil {
		t.Fatal(err)
	}
	if fl, _ := unix.FcntlInt(uintptr(c.FD), unix.F_GETFD, 0); fl&unix.FD_CLOEXEC != 0 {
		t.Fatal("the control would close at the restart's exec")
	}
	// The exec keeps the descriptor and drops the runner holding it: a
	// copy stands for it in the next Shell, and this runner's end closes.
	if c.FD, err = unix.Dup(c.FD); err != nil {
		t.Fatal(err)
	}
	k.ctl.Close()
	path := filepath.Join(t.TempDir(), "selftest-resume.json")
	raw, _ := json.Marshal(Resume{Line: 7, Case: "keep", KeepServer: c})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := LoadResume(path)
	if err != nil {
		t.Fatal(err)
	}
	if fl, _ := unix.FcntlInt(uintptr(c.FD), unix.F_GETFD, 0); fl&unix.FD_CLOEXEC == 0 {
		t.Error("the next Shell's children would inherit the helper's control")
	}
	again, err := res.KeepServer.take(1)
	if err != nil {
		t.Fatal(err)
	}
	if again.addr() != k.addr() {
		t.Fatalf("carried over at %s, started at %s", again.addr(), k.addr())
	}
	if n, err := again.count("HELLO"); n != 1 || err != nil {
		t.Fatalf("HELLO %d times (%v) after the carry", n, err)
	}
	if err := again.send("after the restart"); err != nil {
		t.Fatal(err)
	}
	keepRead(t, conn, "after the restart")
	keepOpen(t, again, 1, 1)
	if err := again.close(); err != nil {
		t.Fatal(err)
	}
	if processAlive(again.proc.Pid) {
		t.Fatal("the helper outlived keep-server off")
	}

	k, err = startKeepApart("127.0.0.1:0", 1)
	if err != nil {
		t.Fatal(err)
	}
	k.ctl.Close()
	done := make(chan struct{})
	go func() {
		_, _ = k.proc.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = k.proc.Kill()
		t.Fatal("the helper outlived its control")
	}

	if _, err := startKeepApart("127.0.0.1", 1); err == nil || !strings.Contains(err.Error(), "missing port") {
		t.Fatalf("a server that cannot listen: %v", err)
	}
	if c, err := (&Runner{keep: &keepApart{}}).keepCarry(true); c != nil || err != nil {
		t.Fatalf("apart's helper carried over a crash: %+v, %v", c, err)
	}
}

// outside's helper outlives the Shell that started it however that ends:
// a crash closes the control without a word, and the Shell systemd starts
// next finds the helper by the path in the resume file, with the server
// and its connection as they were; the resume file also names the run's
// directory for it (ResumeBase).
func TestKeepServerOutsideCarried(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "keep.sock")
	k, err := startKeepOutside("127.0.0.1:0", sock, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn := keepDial(t, k, "HELLO")
	keepLinesGot(t, k, "HELLO", 1)
	c, err := (&Runner{keep: k}).keepCarry(true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != sock || c.Pid != k.proc.Pid || c.Addr != k.addr() {
		t.Fatalf("carried %+v", c)
	}
	k.ctl.Close()
	before, _ := unix.FcntlInt(0, unix.F_GETFD, 0)
	path := filepath.Join(t.TempDir(), "selftest-resume.json")
	raw, _ := json.Marshal(Resume{Line: 7, Case: "keep", KeepServer: c, Base: "/tmp/phone-selftest-1"})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResumeBase(path); got != "/tmp/phone-selftest-1" {
		t.Errorf("ResumeBase %q", got)
	}
	res, err := LoadResume(path)
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := unix.FcntlInt(0, unix.F_GETFD, 0); after != before {
		t.Error("loading outside's helper changed descriptor 0")
	}
	again, err := res.KeepServer.take(1)
	if err != nil {
		t.Fatal(err)
	}
	if again.addr() != k.addr() {
		t.Fatalf("taken over at %s, started at %s", again.addr(), k.addr())
	}
	if err := again.send("after the crash"); err != nil {
		t.Fatal(err)
	}
	keepRead(t, conn, "after the crash")
	keepOpen(t, again, 1, 1)
	if err := again.close(); err != nil {
		t.Fatal(err)
	}
	if processAlive(again.proc.Pid) {
		t.Fatal("the helper outlived keep-server off")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("the control socket stayed: %v", err)
	}
	if ResumeBase(filepath.Join(t.TempDir(), "none.json")) != "" {
		t.Error("a base without a resume file")
	}
}

// With no Shell to come back, outside's helper ends once the wait for the
// next control is over, and takes its socket along.
func TestKeepServerOutsideEnds(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "keep.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	f, err := ln.File()
	ln.Close()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- serveKeepOutside("127.0.0.1:0", f, "100ms") }()
	ctl, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	_ = ctl.SetReadDeadline(time.Now().Add(5 * time.Second))
	if hello, err := bufio.NewReader(ctl).ReadString('\n'); !strings.HasPrefix(hello, "addr 127.0.0.1:") {
		t.Fatalf("hello %q, %v", hello, err)
	}
	ctl.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the helper waited on with no control")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("the control socket stayed: %v", err)
	}
}

func keepDial(t *testing.T, s keepLines, hello string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", s.addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := conn.Write([]byte(hello + "\n")); err != nil {
		t.Fatal(err)
	}
	return conn
}

func keepRead(t *testing.T, conn net.Conn, want string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if line, err := bufio.NewReader(conn).ReadString('\n'); line != want+"\n" {
		t.Fatalf("the app got %q (%v), want %q", line, err, want)
	}
}

func keepLinesGot(t *testing.T, s keepLines, line string, want int) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		n, err := s.count(line)
		if err != nil {
			t.Fatal(err)
		}
		if n == want {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("the server got %q %d times, want %d", line, n, want)
		}
	}
}

func keepOpen(t *testing.T, s keepLines, want, open int) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		n, o, err := s.accepted()
		if err != nil {
			t.Fatal(err)
		}
		if n == want && o == open {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("accepted %d, %d open; want %d, %d open", n, o, want, open)
		}
	}
}
