package push

import "time"

// An app's own long connection (net.keepalive, 21-push.md §8) keeps its
// socket while the app is frozen, but the system decides when the radio
// wakes for it. Its heartbeat rides any wake that comes anyway (a push
// heartbeat, a push, a batch of jobs, data on a connection) once its
// window is open, half its interval after the last one; only when none
// came does the system wake for the heartbeat at its deadline, and pushd's
// keep-alive rides that wake in turn (its next one moves a full interval
// on), so the two connections go on sharing their wakes. As Android
// batches inexact alarms, the phone then wakes about as often as the more
// frequent of the two needs, not as often as both together.

// KeepaliveSlack is how late a heartbeat may come to land in the push
// connection's heartbeat expected about the time it is due.
const KeepaliveSlack = 10 * time.Second

// Beat is the heartbeat schedule of an app's own connection.
type Beat struct {
	Every time.Duration
	// Last: the last heartbeat, or when it connected.
	Last time.Time
}

// Opens is when a wake made for something else may carry the heartbeat.
func (b Beat) Opens() time.Time { return b.Last.Add(b.Every / 2) }

// Due is the heartbeat's deadline.
func (b Beat) Due() time.Time { return b.Last.Add(b.Every) }

// Rides: a wake at t carries the heartbeat.
func (b Beat) Rides(t time.Time) bool { return !t.Before(b.Opens()) }

// OwnWake is when the system has to wake for the heartbeat alone, given
// when the push connection's next heartbeat is expected (zero: not known).
// One expected in the window carries it: the deadline holds only should it
// not come; one expected about the deadline is waited for, up to
// KeepaliveSlack past it.
func (b Beat) OwnWake(nextPush time.Time) time.Time {
	due := b.Due()
	if nextPush.IsZero() || nextPush.Before(b.Opens()) || nextPush.After(due.Add(KeepaliveSlack)) {
		return due
	}
	late := nextPush.Add(KeepaliveSlack)
	switch {
	case late.Before(due):
		return due
	case late.After(due.Add(KeepaliveSlack)):
		return due.Add(KeepaliveSlack)
	}
	return late
}

// WakeBudget counts wakes over the last hour against PerHour.
type WakeBudget struct {
	PerHour int
	at      []time.Time
}

func (w *WakeBudget) prune(now time.Time) {
	cut := now.Add(-time.Hour)
	kept := w.at[:0]
	for _, t := range w.at {
		if t.After(cut) && !t.After(now) {
			kept = append(kept, t)
		}
	}
	w.at = kept
}

// Take counts a wake at now if the last hour has room for it.
func (w *WakeBudget) Take(now time.Time) bool {
	w.prune(now)
	if len(w.at) >= w.PerHour {
		return false
	}
	w.at = append(w.at, now)
	return true
}

// Left is how many wakes the last hour still has room for.
func (w *WakeBudget) Left(now time.Time) int {
	w.prune(now)
	return max(w.PerHour-len(w.at), 0)
}

// WakeCost is what one wake of the cellular radio for a small exchange (a
// heartbeat, a short message) costs: the promotion to the connected state,
// the tail the network keeps the radio connected after the last packet
// (its inactivity timer, which the operator sets), and the time the phone
// stays awake after a wake (awakeAfterWake in shell/sleep.go). The numbers
// are assumptions from published measurements, not measured on a Kario
// phone (28-battery.md §8).
type WakeCost struct {
	Promote  time.Duration
	PromoteW float64
	Tail     time.Duration
	TailW    float64
	Awake    time.Duration
	AwakeW   float64
	// Volts: the battery's nominal voltage, which turns energy into charge.
	Volts float64
}

// LTEWake is the cost assumed for LTE with a 10-second tail.
var LTEWake = WakeCost{Promote: 260 * time.Millisecond, PromoteW: 1.2, Tail: 10 * time.Second, TailW: 0.5,
	Awake: 10 * time.Second, AwakeW: 0.1, Volts: 3.85}

// Joules is the energy of one wake.
func (c WakeCost) Joules() float64 {
	return c.Promote.Seconds()*c.PromoteW + c.Tail.Seconds()*c.TailW + c.Awake.Seconds()*c.AwakeW
}

// MAh is the charge of one wake.
func (c WakeCost) MAh() float64 { return c.Joules() / c.Volts / 3.6 }

// WakesPerDay is how many wakes a day one every interval makes.
func WakesPerDay(every time.Duration) float64 { return float64(24*time.Hour) / float64(every) }
