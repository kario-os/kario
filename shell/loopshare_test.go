package shell

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// An app's messages that each hold the loop some milliseconds (a start's
// first pages, the answer to a key) are read as they come, within the
// app's burst: the Shell rested 4 times each one's cost before it read the
// next, and the page the user waited for waited behind that rest.
func TestAnAppsBurstIsReadWithoutRests(t *testing.T) {
	if !alone(t) {
		return
	}
	c, a, rd, app := anrRig(t)
	send := func(m any) {
		b, _ := json.Marshal(m)
		if _, err := app.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	_ = c.onLoop(func() error { c.probe(a); return nil })
	seq := nextPing(t, rd, app)
	for i := range 3 {
		send(map[string]any{"t": briefMark + strconv.Itoa(i)})
		time.Sleep(150 * time.Millisecond)
	}
	t0 := time.Now()
	send(ui.Msg{T: ui.MsgPong, Value: seq})
	for {
		var out bool
		_ = c.onLoop(func() error { out = !a.ping.sent.IsZero(); return nil })
		if !out {
			break
		}
		if time.Since(t0) > 5*time.Second {
			t.Fatal("the answer was never read")
		}
		time.Sleep(time.Millisecond)
	}
	if d := time.Since(t0); d > 2*briefFor {
		t.Fatalf("the answer was read %v after it was sent, behind a rest", d)
	}
}

// The burst does not raise an app's share: an app flooding the loop with
// messages of 5 ms each for a minute gets a fifth of it, and runs at most
// its burst (with what its share earns meanwhile) before a rest.
func TestAnAppKeepsAFifthOfTheLoop(t *testing.T) {
	now := time.Unix(0, 0)
	s := loopShare{credit: appLoopBurst, at: now}
	const each = 5 * time.Millisecond
	var used, run, most time.Duration
	for end := now.Add(time.Minute); now.Before(end); {
		now = now.Add(each)
		used, run = used+each, run+each
		if r := s.rest(each, now); r >= appLoopRest {
			most, run = max(most, run), 0
			now = now.Add(r)
		}
	}
	if share := float64(used) / float64(time.Minute); share < 0.19 || share > 0.21 {
		t.Errorf("the app had %.3f of the loop, want a fifth", share)
	}
	if limit := appLoopBurst*(1+appLoopShare)/appLoopShare + each; most > limit {
		t.Errorf("it ran %v at once, over %v", most, limit)
	}
}
