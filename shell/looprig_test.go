package shell

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// runLoop runs c's event loop on a goroutine of its own, as phone-shell's
// does, until the test ends: then c shuts down on its loop and the loop
// ends before the cleanups registered before runLoop (the Shell's
// directories) run.
func runLoop(t *testing.T, c *Core) {
	t.Helper()
	stop, ended := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(ended)
		for {
			select {
			case f := <-c.Events():
				f()
			case <-stop:
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = c.onLoop(func() error { c.Shutdown(); return nil })
		// The loop may still take a tick after Shutdown (screen time
		// saved): it must be over before the directories go.
		close(stop)
		<-ended
	})
}

// Once runLoop's cleanup returns nothing runs on the loop. A select with
// an event waiting as well as the stop takes either: the rigs' loops went
// on to the screen tick, which saved screen time into the state while
// t.TempDir removed it ("directory not empty").
func TestRunLoopIsOverWithItsCleanup(t *testing.T) {
	for i := range 10 {
		var over atomic.Bool
		var late atomic.Int32
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			c := &Core{ev: make(chan func(), 16)}
			t.Cleanup(func() { over.Store(true) })
			runLoop(t, c)
			// An event is always waiting, as on a busy Shell (the
			// screen's ticks, an app's messages).
			var busy func()
			busy = func() {
				time.Sleep(time.Millisecond)
				if over.Load() {
					late.Add(1)
				}
				c.Post(busy)
			}
			c.Post(busy)
		})
		// What a loop still running takes ends within a few events' time.
		time.Sleep(10 * time.Millisecond)
		if n := late.Load(); n > 0 {
			t.Fatalf("round %d: %d events ran on the loop after its cleanup", i, n)
		}
	}
}
