package push

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// day plays a day of the push connection (heartbeat p; the k-th arrives
// lag(k) after the gateway sent it) beside an app's own connection
// (heartbeat every), batched as the Shell batches them: the radio's wakes,
// and the app's heartbeats in wakes of their own and in shared ones.
func day(p, every time.Duration, lag func(k int) time.Duration) (radio, own, shared int) {
	t0 := time.Unix(1_800_000_000, 0)
	end := t0.Add(24 * time.Hour)
	b := Beat{Every: every, Last: t0}
	send, lastPush := t0.Add(p), t0
	var lastRide time.Time
	for k := 0; ; {
		arrive := send.Add(lag(k))
		wake := b.OwnWake(lastPush.Add(p))
		if arrive.After(end) && wake.After(end) {
			return
		}
		if wake.Before(arrive) {
			radio, own = radio+1, own+1
			b.Last = wake
			// pushd rides the wake: the gateway answers on the connection
			// and its next heartbeat waits a full interval.
			if lastRide.IsZero() || wake.Sub(lastRide) > p/2 {
				lastRide, send, lastPush = wake, wake.Add(p), wake
			}
			continue
		}
		radio++
		k++
		lastPush, send = arrive, send.Add(p)
		if b.Rides(arrive) {
			shared++
			b.Last = arrive
		}
	}
}

func TestBatchedHeartbeats(t *testing.T) {
	steady := func(int) time.Duration { return 0 }
	// The arrival time wanders by up to 3 seconds: a heartbeat due just
	// before the push one waits for it.
	wander := func(k int) time.Duration { return time.Duration(k%4) * time.Second }
	for _, c := range []struct {
		p, every   time.Duration
		lag        func(int) time.Duration
		radio, own int
	}{
		// As long as the push heartbeat: every one carries the app's.
		{28 * time.Minute, 28 * time.Minute, steady, 51, 0},
		{28 * time.Minute, 28 * time.Minute, wander, 51, 0},
		// Longer: every other push heartbeat carries it.
		{28 * time.Minute, 60 * time.Minute, wander, 51, 0},
		// A network whose NAT needs 5 minutes: the push heartbeats come
		// often enough for the app's 28.
		{5 * time.Minute, 28 * time.Minute, wander, 287, 0},
		// Shorter than the push heartbeat: the system wakes for the app,
		// and the push connection's keep-alive rides those wakes, so the
		// radio wakes 144 times, not 144 + 51.
		{28 * time.Minute, 10 * time.Minute, wander, 144, 144},
		{28 * time.Minute, time.Minute, steady, 1440, 1440},
	} {
		radio, own, shared := day(c.p, c.every, c.lag)
		unbatched := int(WakesPerDay(c.p) + WakesPerDay(c.every))
		if radio != c.radio || own != c.own {
			t.Errorf("push %s, app %s: radio woke %d times (want %d, %d unbatched), %d heartbeats of its own (want %d), %d shared",
				c.p, c.every, radio, c.radio, unbatched, own, c.own, shared)
		}
		if beats := own + shared; float64(beats) < WakesPerDay(c.every)-1 && c.every >= c.p {
			t.Errorf("push %s, app %s: %d heartbeats, fewer than one per interval", c.p, c.every, beats)
		}
	}
}

// The deadline holds: no heartbeat comes later than its interval, or than
// KeepaliveSlack past it when the push heartbeat came just after it.
func TestOwnWake(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	b := Beat{Every: 10 * time.Minute, Last: t0}
	due := t0.Add(10 * time.Minute)
	for _, c := range []struct {
		next time.Time
		want time.Time
	}{
		{time.Time{}, due},
		{t0.Add(3 * time.Minute), due},                        // before the window: it carries nothing
		{t0.Add(7 * time.Minute), due},                        // in it: the deadline only if it does not come
		{due.Add(-5 * time.Second), due.Add(5 * time.Second)}, // about the deadline: waited for
		{due.Add(8 * time.Second), due.Add(KeepaliveSlack)},   // a little after it: the slack at most
		{due.Add(time.Minute), due},                           // well after it: not waited for
		{due.Add(KeepaliveSlack + time.Second), due},          // past the slack
		{due.Add(-KeepaliveSlack - time.Second), due},         // well before it
		{due.Add(-KeepaliveSlack), due},                       // just before the slack's reach
		{due.Add(-KeepaliveSlack + time.Second), due.Add(time.Second)},
	} {
		if got := b.OwnWake(c.next); !got.Equal(c.want) {
			t.Errorf("push expected %s after the last heartbeat: own wake %s after it, want %s", c.next.Sub(t0), got.Sub(t0), c.want.Sub(t0))
		}
	}
	if b.Rides(t0.Add(5*time.Minute-time.Second)) || !b.Rides(t0.Add(5*time.Minute)) {
		t.Error("the window opens half the interval after the last heartbeat")
	}
}

func TestWakeBudget(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	w := WakeBudget{PerHour: 6}
	for i := 0; i < 6; i++ {
		if !w.Take(t0.Add(time.Duration(i) * time.Minute)) {
			t.Fatalf("wake %d refused", i+1)
		}
	}
	if w.Take(t0.Add(10*time.Minute)) || w.Left(t0.Add(10*time.Minute)) != 0 {
		t.Fatal("a seventh wake within the hour")
	}
	// The first wake leaves the hour.
	if !w.Take(t0.Add(time.Hour+time.Second)) || w.Left(t0.Add(time.Hour+time.Second)) != 0 {
		t.Fatal("the hour moved on, the budget did not")
	}
	if w.Left(t0.Add(3*time.Hour)) != 6 {
		t.Fatal("hours later the budget is not whole again")
	}
	// A clock set back does not count wakes from the future.
	if w.Left(t0) != 6 {
		t.Fatal("wakes after now counted")
	}
}

// The power table of 28-battery.md §8: mAh a day for a heartbeat every 1,
// 5, 10 and 28 minutes on LTE with a 5, 10 and 20 second tail.
func TestPowerTable(t *testing.T) {
	want := map[time.Duration][3]float64{
		time.Minute:      {396.1, 655.8, 1175.3},
		5 * time.Minute:  {79.2, 131.2, 235.1},
		10 * time.Minute: {39.6, 65.6, 117.5},
		28 * time.Minute: {14.1, 23.4, 42.0},
	}
	for every, row := range want {
		for i, tail := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second} {
			c := LTEWake
			c.Tail = tail
			got := math.Round(WakesPerDay(every)*c.MAh()*10) / 10
			if got != row[i] {
				t.Errorf("every %s, tail %s: %.1f mAh a day, the table says %.1f", every, tail, got, row[i])
			}
		}
	}
	if got := fmt.Sprintf("%.3f", LTEWake.MAh()); got != "0.455" {
		t.Errorf("one LTE wake: %s mAh", got)
	}
}
