package shell

import (
	"testing"
	"time"
)

// Opening an app the system has just ended, before its exit is in, starts
// it afresh on top once the old process is gone, instead of putting the
// dying one on screen and falling back to the home screen when it exits.
func TestLaunchWhileEnding(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newHostileRig(t, map[string]string{})
	a := r.launch(probeID)
	if !a.ready {
		t.Fatal("the probe did not come up")
	}
	old := a.proc.Pid
	r.c.kill(a)
	r.c.Launch(a, "")
	if !r.until(5*time.Second, func() bool { return a.running && a.ready && a.proc != nil && a.proc.Pid != old }) {
		t.Fatalf("not started again: running %v, ready %v", a.running, a.ready)
	}
	if r.c.fg != a {
		t.Fatalf("the foreground is %v, want the relaunched app", r.c.fg)
	}
	r.pump(200 * time.Millisecond)
	if r.c.fg != a || !a.running {
		t.Fatal("the old process's exit took the new one off the screen")
	}
	r.c.kill(a)
	r.ended(a, 5*time.Second)
}
