package sandbox

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// The helper's layers are tried in a child process too, started before
// TestMain runs: init sees the variable first.
func init() {
	if dir := os.Getenv("SANDBOX_HELPER_CHILD"); dir != "" {
		runtime.LockOSThread()
		helperChild(dir)
		os.Exit(0)
	}
}

func errName(err error) string {
	var e unix.Errno
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &e):
		return unix.ErrnoName(e)
	}
	return err.Error()
}

// helperChild holds a listening socket (fd 3) and a pipe's read end (fd 4)
// from its parent, as phone-usbshared does, confines itself and reports.
func helperChild(dir string) {
	exe, _ := os.Executable()
	if os.Getenv("SANDBOX_HELPER_LAUNCHER") != "" {
		applied, err := Apply(Policy{Helper: true, Code: []string{exe}})
		if err != nil {
			fmt.Println("apply:", err)
			os.Exit(1)
		}
		fmt.Println("applied=" + strings.Join(applied, ","))
	}
	ln, err := net.FileListener(os.NewFile(3, "listener"))
	if err != nil {
		fmt.Println("listener:", err)
		os.Exit(1)
	}
	in := os.NewFile(4, "pipe")
	if err := DescriptorsOnly(); err != nil {
		fmt.Println("fd-only:", err)
		os.Exit(1)
	}
	_, err = os.ReadFile("/etc/passwd")
	fmt.Println("open=" + errName(err))
	fmt.Println("create=" + errName(os.WriteFile(filepath.Join(dir, "made"), []byte("x"), 0o600)))
	fmt.Println("remove=" + errName(os.Remove(filepath.Join(dir, "keep"))))
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.Close(fd)
	}
	fmt.Println("socket=" + errName(err))
	_, err = net.Dial("unix", filepath.Join(dir, "nowhere"))
	fmt.Println("dial=" + errName(err))
	// Neither exists: allowed, they would fail with ENOENT and ESRCH.
	fmt.Println("exec=" + errName(unix.Exec(filepath.Join(dir, "none"), []string{"none"}, nil)))
	fmt.Println("ptrace=" + errName(unix.PtraceAttach(0)))
	b := make([]byte, 5)
	n, err := in.Read(b)
	fmt.Println("pipe=" + errName(err) + ":" + string(b[:n]))
	c, err := ln.Accept()
	if err == nil {
		_, err = c.Write([]byte("hi"))
		c.Close()
	}
	fmt.Println("accept=" + errName(err))
}

func runHelperChild(t *testing.T, launcher bool) map[string]string {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	lf, _ := ln.(*net.TCPListener).File()
	defer lf.Close()
	r, w, _ := os.Pipe()
	defer r.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "SANDBOX_HELPER_CHILD="+dir)
	if launcher {
		cmd.Env = append(cmd.Env, "SANDBOX_HELPER_LAUNCHER=1")
	}
	cmd.ExtraFiles = []*os.File{lf, r}
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("hello"))
	w.Close()
	if c, err := net.Dial("tcp", ln.Addr().String()); err == nil {
		b := make([]byte, 2)
		c.Read(b)
		c.Close()
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v\n%s", err, out.String())
	}
	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			got[k] = v
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "keep")); err != nil {
		t.Errorf("the helper removed a file by path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "made")); err == nil {
		t.Errorf("the helper made a file by path")
	}
	return got
}

// A helper keeps its handed descriptors (accepting on the listener, reading
// the pipe) and loses paths, sockets, programs and debugging, without root.
func TestDescriptorsOnly(t *testing.T) {
	got := runHelperChild(t, false)
	want := map[string]string{"open": "EPERM", "create": "EPERM", "remove": "EPERM", "socket": "EPERM",
		"dial": "EPERM", "exec": "EPERM", "ptrace": "EPERM", "pipe": "ok:hello", "accept": "ok"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// Through the launcher's helper policy: no temporary directory is made,
// and with Landlock the program may still run but read nothing.
func TestHelperPolicy(t *testing.T) {
	got := runHelperChild(t, true)
	t.Logf("applied: %s", got["applied"])
	if got["accept"] != "ok" || got["pipe"] != "ok:hello" || got["open"] != "EPERM" {
		t.Errorf("under the helper policy: %v", got)
	}
	if _, err := os.Stat("tmp"); err == nil {
		t.Errorf("the helper policy made a temporary directory")
	}
}

func TestHelperFilterKeepsTheAppFilter(t *testing.T) {
	f := denyFilter(append(append([]uintptr{}, denied...), helperDenied...))
	has := map[uint32]bool{}
	for _, ins := range f {
		if ins.Code == unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K {
			has[ins.K] = true
		}
	}
	for _, nr := range append(append([]uintptr{}, denied...), unix.SYS_EXECVE, unix.SYS_OPENAT, unix.SYS_SOCKET, unix.SYS_CLONE) {
		if !has[uint32(nr)] {
			t.Errorf("syscall %d is not in the helper's filter", nr)
		}
	}
}
