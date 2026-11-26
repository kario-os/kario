package shell

import (
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/wire"
)

// endApp returns at once. A frozen app is thawed through its cgroup before
// it is asked (its connection ends); one that stays is killed once the
// grace is up, by what the timer posts to the loop.
func TestEndKillsWhatStaysAfterTheGrace(t *testing.T) {
	const id, grace = "test.deaf", 300 * time.Millisecond
	base := t.TempDir()
	freeze := filepath.Join(base, "app."+id, "cgroup.freeze")
	if err := os.MkdirAll(filepath.Dir(freeze), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(freeze, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	shellEnd, appEnd := wire.New(os.NewFile(uintptr(fds[0]), "shell")), wire.New(os.NewFile(uintptr(fds[1]), "app"))
	defer shellEnd.Close()
	defer appEnd.Close()

	c := &Core{log: log.New(io.Discard, "", 0), ev: make(chan func(), 4),
		cg: &cgroups{base: base, feat: cgFeatures{Freezer: true}}}
	a := &App{M: &Manifest{ID: id}, running: true, ready: true, frozen: true, proc: cmd.Process, conn: shellEnd}
	t0 := time.Now()
	c.endApp(a, grace)
	if took := time.Since(t0); took > 50*time.Millisecond {
		t.Fatalf("endApp took %v", took)
	}
	if raw, _ := os.ReadFile(freeze); string(raw) != "0" || a.frozen {
		t.Fatalf("cgroup.freeze %q, frozen %v: not thawed", raw, a.frozen)
	}
	if !a.killed || a.ready {
		t.Fatalf("killed %v, ready %v", a.killed, a.ready)
	}
	var m map[string]any
	if err := appEnd.Recv(&m); !errors.Is(err, io.EOF) {
		t.Fatalf("the app reads %v, not the end of its connection", err)
	}
	c.endApp(a, time.Millisecond)
	select {
	case f := <-c.ev:
		if waited := time.Since(t0); waited < grace {
			t.Fatalf("killed %v after it was asked, before the grace (%v) was up", waited, grace)
		}
		f()
	case <-time.After(5 * time.Second):
		t.Fatal("nothing came to kill it")
	}
	select {
	case err := <-exited:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
			t.Fatalf("it ended with %v, not killed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still running after the grace")
	}
	c.appEnded(a)
	if ended, itself := c.EndedItself(id); !ended || itself {
		t.Fatalf("EndedItself = %v, %v after a kill once the grace was up", ended, itself)
	}
}
