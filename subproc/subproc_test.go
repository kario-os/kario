package subproc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/subproc/subproctest"
)

func TestChildDiesWithTheProcess(t *testing.T) {
	if subproctest.Child(t) {
		if err := Start(Command(filepath.Join(os.Getenv("STANDIN_DIR"), "helper"))); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		return
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "helper"), []byte(subproctest.StandIn), 0o755); err != nil {
		t.Fatal(err)
	}
	if pid := subproctest.Survivor(t, "STANDIN_DIR="+dir); pid != 0 {
		t.Fatalf("the helper (pid %d) outlived its process", pid)
	}
}

// A goroutine locked to its thread that returns ends the thread, and Linux
// then sends the children started from it their parent-death signal.
func TestChildOutlivesTheThreadThatStartedIt(t *testing.T) {
	cmd := Command("sleep", "30")
	subproctest.OnEndingThread(t, func() {
		if err := Start(cmd); err != nil {
			t.Error(err)
		}
	})
	if cmd.Process == nil {
		t.FailNow()
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("the child ended with the thread that started it: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	_ = cmd.Process.Kill()
	<-done
}

func TestOutput(t *testing.T) {
	out, err := Output(Command("sh", "-c", "echo out; echo err >&2"))
	if err != nil || string(out) != "out\n" {
		t.Errorf("Output: %q, %v", out, err)
	}
	out, err = CombinedOutput(Command("sh", "-c", "echo out; echo err >&2; exit 3"))
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 || !strings.Contains(string(out), "out\n") || !strings.Contains(string(out), "err\n") {
		t.Errorf("CombinedOutput: %q, %v", out, err)
	}
	if _, err := Output(Command(filepath.Join(t.TempDir(), "missing"))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing program: %v", err)
	}
}
