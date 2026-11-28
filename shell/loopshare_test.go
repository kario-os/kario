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
	// handled waits until done holds on the loop and says when it saw that.
	handled := func(what string, since time.Time, done func() bool) time.Time {
		t.Helper()
		for {
			var ok bool
			_ = c.onLoop(func() error { ok = done(); return nil })
			if ok {
				return time.Now()
			}
			if time.Since(since) > 5*time.Second {
				t.Fatalf("%s was never read", what)
			}
			time.Sleep(time.Millisecond)
		}
	}
	_ = c.onLoop(func() error { c.probe(a); return nil })
	seq := nextPing(t, rd, app)
	var most time.Duration
	for i := range 3 {
		mark, sent := briefMark+strconv.Itoa(i), time.Now()
		send(map[string]any{"t": mark})
		at := handled("the brief message", sent, func() bool { return a.guard.unknown[mark] })
		// The loop took the message up once it was sent and a rest before
		// it was over: what it cost the loop is at most the time since.
		from := sent
		if held := time.Unix(0, a.heldUntil.Load()); held.After(from) {
			from = held
		}
		took := at.Sub(from)
		most = max(most, took)
		// Before the next, the app's share earns back what the message took
		// of the loop, however long a busy machine made it.
		time.Sleep(took * (1 + appLoopShare))
	}
	t0 := time.Now()
	send(ui.Msg{T: ui.MsgPong, Value: seq})
	d := handled("the answer", t0, func() bool { return a.ping.sent.IsZero() }).Sub(t0)
	// With those pauses the share owes no rest unless one message alone took
	// more of the loop than appLoopBurst: a machine busy enough to stretch a
	// brief one that far is rightly rested.
	if a.heldUntil.Load() != 0 {
		if most <= appLoopBurst {
			t.Fatalf("the answer was read %v after it was sent, behind a rest, though no message took over %v of the loop", d, appLoopBurst)
		}
		t.Logf("a brief message held the loop up to %v, over the burst of %v: rightly rested", most, appLoopBurst)
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
