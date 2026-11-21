package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// An app still writing its storage when the Shell stops (the runtime's
// writer thread waits for fsync, which takes seconds on slow flash) gets to
// finish before it is killed.
func TestShutdownWaitsForAnAppFinishingAWrite(t *testing.T) {
	done := filepath.Join(t.TempDir(), "written")
	cmd := exec.Command("sh", "-c", `trap 'sleep 1.5; echo ok > "$0"; exit 0' TERM; while :; do sleep 0.05; done`, done)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	c := &Core{apps: []*App{{running: true, proc: cmd.Process}}}
	t0 := time.Now()
	c.Shutdown()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the app was killed %v after SIGTERM, before it finished: %v", time.Since(t0).Round(10*time.Millisecond), err)
	}
	if _, err := os.Stat(done); err != nil {
		t.Fatalf("the app's write did not happen: %v", err)
	}
}
