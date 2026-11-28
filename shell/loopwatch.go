package shell

import (
	"bytes"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// The loop's watchdog (WatchLoop, phone-shell's device mode): everything
// the Shell does runs on its loop, so a call there that never returns (a
// sysfs read parked for good, docs/39-oneplus-6t.md §16.10) stops all of
// it without a word: no keys, no screen timeout, no log line. A beat
// posted every loopBeat notes when the loop last ran one; once it has not
// for loopStuck, one line says where the loop is (its goroutine's state
// and calls, as the runtime has them) and what waits for it, and another
// line when it turns again.

var (
	loopBeat  = time.Second
	loopStuck = 5 * time.Second
)

// loopFrames: the calls of the loop's goroutine a line names, innermost
// first.
const loopFrames = 16

// loopEpoch: the watchdog's times are durations since it (monotonic: a
// suspend does not count).
var loopEpoch = time.Now()

func sinceLoopEpoch() time.Duration { return time.Since(loopEpoch) }

// loopWatch is what the watchdog knows: when the loop last ran a beat,
// the goroutine it ran on, the renderer's keys posted to the loop and not
// taken yet, and when the last power key came.
type loopWatch struct {
	turned  atomic.Int64
	gid     atomic.Int64
	keys    atomic.Int32
	powerAt atomic.Int64
}

// WatchLoop starts the loop's watchdog, until Shutdown.
func (c *Core) WatchLoop() {
	w := &c.loop
	w.turned.Store(int64(sinceLoopEpoch()))
	beat, stuckAfter := loopBeat, loopStuck
	go func() {
		var pending atomic.Bool
		stuck, stuckAt := false, time.Duration(0)
		for !c.stopping.Load() {
			time.Sleep(beat)
			if pending.CompareAndSwap(false, true) {
				select {
				case c.ev <- func() {
					w.turned.Store(int64(sinceLoopEpoch()))
					w.gid.Store(goroutineID())
					pending.Store(false)
				}:
				default:
					pending.Store(false)
				}
			}
			now, turned := sinceLoopEpoch(), time.Duration(w.turned.Load())
			switch {
			case now-turned >= stuckAfter && !stuck:
				stuck, stuckAt = true, turned
				c.screenLog("loop: has not turned for %s; %s", (now - turned).Round(time.Second), w.where())
			case now-turned < stuckAfter && stuck:
				stuck = false
				c.screenLog("loop: turned again after %s", (turned - stuckAt).Round(time.Second))
			}
		}
	}()
}

// keyIn: a key from the renderer is on its way to the loop; the power
// key's time is kept.
func (w *loopWatch) keyIn(power bool) {
	w.keys.Add(1)
	if power {
		w.powerAt.Store(int64(sinceLoopEpoch()))
	}
}

func (w *loopWatch) keyTaken() { w.keys.Add(-1) }

// where is the loop's goroutine and what waits for it.
func (w *loopWatch) where() string {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	s := "the loop's goroutine " + goroutineCalls(buf, w.gid.Load())
	if n := w.keys.Load(); n > 0 {
		s += fmt.Sprintf("; %d keys from the renderer waiting", n)
	}
	if at := w.powerAt.Load(); at > 0 {
		s += fmt.Sprintf("; the last power key %s ago", (sinceLoopEpoch() - time.Duration(at)).Round(time.Second))
	}
	return s
}

// goroutineID is the calling goroutine's number, from its stack's header
// ("goroutine 7 [running]:").
func goroutineID() int64 {
	var b [64]byte
	f := bytes.Fields(b[:runtime.Stack(b[:], false)])
	if len(f) < 2 {
		return 0
	}
	id, _ := strconv.ParseInt(string(f[1]), 10, 64)
	return id
}

// goroutineCalls is goroutine id of a dump of all stacks (runtime.Stack's)
// in one line: its state ("[IO wait, 3 minutes]") and its calls, innermost
// first, each package by its last element.
func goroutineCalls(dump []byte, id int64) string {
	head := fmt.Sprintf("goroutine %d ", id)
	for _, g := range strings.Split(string(dump), "\n\n") {
		if !strings.HasPrefix(g, head+"[") {
			continue
		}
		lines := strings.Split(g, "\n")
		state := strings.TrimSuffix(strings.TrimPrefix(lines[0], head), ":")
		var calls []string
		for _, l := range lines[1:] {
			if l == "" || strings.HasPrefix(l, "\t") || strings.HasPrefix(l, "created by ") {
				continue
			}
			if len(calls) == loopFrames {
				calls = append(calls, "…")
				break
			}
			if i := strings.LastIndex(l, "("); i > 0 && strings.HasSuffix(l, ")") {
				l = l[:i]
			}
			if i := strings.LastIndex(l, "/"); i >= 0 {
				l = l[i+1:]
			}
			calls = append(calls, l)
		}
		return state + " " + strings.Join(calls, " < ")
	}
	return fmt.Sprintf("%d: not found", id)
}
