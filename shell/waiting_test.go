package shell

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// A case that leaves an alarm or a scheduled notification waiting fails at
// its end: in a serial run it comes due in whatever case runs then (CI
// run 46031493993: the weekly event's reminder over the screen-time case
// of another part). The run goes on, the next case as after a pass; what
// waited as the case began is not its.
func TestCaseLeavesNoReminderWaiting(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	at := c.now().Add(48 * time.Hour).Format(ui.ScheduledAtLayout)
	if _, err := c.sched.schedule("test.still", ui.ScheduledNotif{ID: "before", At: at, Title: "before"}, c.now()); err != nil {
		t.Fatal(err)
	}
	script := "sleep 10\n#part a\n== sets\nalarm-in 600 left\n== goes on\nrequire-previous\nsleep 10\n" +
		"== rings\nalarm-in 60 rang\ntimeshift 120\nsleep 1500\nclock-reset\n"
	var out strings.Builder
	r := &Runner{C: c, Out: &out}
	if r.Run(script) {
		t.Errorf("the run passed:\n%s", out.String())
	}
	if line := caseLine(r.Cases); line != "sets FAIL, goes on PASS, rings PASS" {
		t.Fatalf("cases %s\n%s", line, out.String())
	}
	if m := r.Cases[0].Message; !strings.HasPrefix(m, leavesWaiting+`: com.phone.clock's alarm a1 ("left" at `) || strings.Contains(m, "before") {
		t.Errorf("sets: %s", m)
	}
}

// What a case leaves waiting may still be taken back by the app after its
// last step (d101-device: the calendar deletes an event, shows the search
// page, then cancels the event's reminder): that case passes; one that
// leaves a reminder for good still fails.
func TestCaseEndWaitsForALateCancel(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	cancelled := make(chan bool, 1)
	script := "sleep 10\n#part a\n== takes back\nalarm-in 600 late\n== keeps\nalarm-in 900 kept\n"
	var out strings.Builder
	r := &Runner{C: c, Out: &out}
	time.AfterFunc(400*time.Millisecond, func() {
		c.Post(func() { cancelled <- c.alarms.cancel("com.phone.clock", "a1") })
	})
	if r.Run(script) {
		t.Errorf("the run passed:\n%s", out.String())
	}
	for took, late := false, time.After(5*time.Second); !took; {
		select {
		case f := <-c.Events():
			f()
		case ok := <-cancelled:
			if !ok {
				t.Fatalf("no alarm a1 to cancel:\n%s", out.String())
			}
			took = true
		case <-late:
			t.Fatalf("the cancel never ran:\n%s", out.String())
		}
	}
	if line := caseLine(r.Cases); line != "takes back PASS, keeps FAIL" {
		t.Fatalf("cases %s\n%s", line, out.String())
	}
	if m := r.Cases[1].Message; !strings.HasPrefix(m, leavesWaiting+`: com.phone.clock's alarm a2 ("kept" at `) {
		t.Errorf("keeps: %s", m)
	}
}

// fakeScheduler is phone-smsd as far as scheduled messages go: a message
// sent with a time waits, deleting its conversation drops it, scheduled
// counts those waiting (asked: how often); old is an smsd from before
// scheduled, which does not know it.
type fakeScheduler struct {
	mu    sync.Mutex
	peers []string
	asked int
	old   bool
}

func (f *fakeScheduler) serve(t *testing.T, dir string) {
	t.Helper()
	_, err := wire.ServeHub(filepath.Join(dir, svc.SmsdSock), func(method string, params json.RawMessage) (any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case method == svc.SMSSend:
			var a svc.SMSSendReq
			if err := json.Unmarshal(params, &a); err != nil || a.At == 0 {
				return nil, fmt.Errorf("not a scheduled message: %s", params)
			}
			f.peers = append(f.peers, a.To)
			return ui.SMS{ID: fmt.Sprintf("o%d", len(f.peers)), Peer: a.To, Dir: "out", Text: a.Text, State: ui.SMSScheduled, Due: a.At}, nil
		case method == svc.SMSDelete:
			var a ui.SMSDeleteArgs
			_ = json.Unmarshal(params, &a)
			n := len(f.peers)
			f.peers = slices.DeleteFunc(f.peers, func(p string) bool { return p == a.Peer })
			return map[string]int{"deleted": n - len(f.peers)}, nil
		case method == svc.SMSScheduled && !f.old:
			f.asked++
			return map[string]int{"scheduled": len(f.peers)}, nil
		}
		return nil, fmt.Errorf("unknown method %q", method)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fakeScheduler) state() (waiting, asked int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.peers), f.asked
}

// A case that leaves a message scheduled in phone-smsd fails at its end
// too: smsd sends it in whatever case runs then. smsd says how many it
// keeps, asked as a case begins and as it ends, not for a case not
// selected; one that deletes the message it scheduled passes. With an
// smsd from before that answer the case is not checked.
func TestCaseLeavesNoMessageScheduled(t *testing.T) {
	if !alone(t) {
		return
	}
	c := snapCore(t)
	f := &fakeScheduler{}
	f.serve(t, c.cfg.RunDir)
	c.tel = &telStatus{Backend: "sim"}
	cases := []string{"== schedules\nsms later 3600 13800000013 see you tomorrow\n",
		"== goes on\nrequire-previous\nsleep 10\n",
		"== cancels\nsms later 600 13800000014 never mind\nsms drop 13800000014\n"}
	run := func(only string, cases ...string) *Runner {
		t.Helper()
		var out strings.Builder
		r := &Runner{C: c, Out: &out}
		if only != "" {
			r.Only = regexp.MustCompile(only)
		}
		r.Run("sleep 10\n#part a\n" + strings.Join(cases, ""))
		t.Log("\n" + out.String())
		return r
	}

	r := run("", cases[0], cases[1], cases[2]+"sms scheduled 1\n")
	if line := caseLine(r.Cases); line != "schedules FAIL, goes on PASS, cancels PASS" {
		t.Fatalf("cases %s", line)
	}
	if m := r.Cases[0].Message; m != leavesWaiting+": phone-smsd's scheduled messages (1; 0 as the case began)" {
		t.Errorf("schedules: %s", m)
	}
	// Twice a case, and once for sms scheduled.
	if waiting, asked := f.state(); waiting != 1 || asked != 7 {
		t.Errorf("%d scheduled, asked %d times", waiting, asked)
	}

	r = run("cancels", cases...)
	if line := caseLine(r.Cases); line != "schedules SKIP, goes on SKIP, cancels PASS" {
		t.Fatalf("with only: cases %s", line)
	}
	if _, asked := f.state(); asked != 9 {
		t.Errorf("with only: asked %d times in all", asked)
	}

	f.mu.Lock()
	f.old = true
	f.mu.Unlock()
	r = run("", cases...)
	if line := caseLine(r.Cases); line != "schedules PASS, goes on PASS, cancels PASS" {
		t.Fatalf("an older smsd: cases %s", line)
	}
	if waiting, _ := f.state(); waiting != 2 {
		t.Errorf("an older smsd: %d scheduled", waiting)
	}
}
