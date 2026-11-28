package shell

import (
	"sync"
	"time"
)

// What Shutdown ends besides the apps: every helper process the Shell
// starts and leaves running, each the way its own stop does, and it waits
// for each, so that a Shell that shuts down and goes on in the same
// process (the end-to-end tests, a restart in place) leaves none behind.
// Shutdown may run off the loop (phone-shell's SIGTERM handler), so the
// helpers are kept in a set with a lock of its own and Shutdown touches
// none of their other state: each one's end on the loop (usbHelperEnded,
// webProcEnded, a compile's) tidies up after it, and nothing starts again
// once the Shell is stopping. The renderer is its starter's (StartGUI):
// every one closes it, and Close waits.

// killedWait is how long a killed helper may take to be gone: one in
// uninterruptible I/O cannot end at once.
const killedWait = time.Second

// helperProc is a helper process that has started.
type helperProc struct {
	done <-chan struct{} // closed once it is gone (waited for)
	stop func()          // asks it to end, the way its own stop does
	kill func() error
	app  bool // an app's (its web process): it ends after the apps
}

// wait waits for the helper until by, then kills it.
func (h helperProc) wait(by time.Time) {
	if h.done == nil {
		return
	}
	select {
	case <-h.done:
		return
	case <-time.After(time.Until(by)):
	}
	if h.kill != nil {
		_ = h.kill()
	}
	select {
	case <-h.done:
	case <-time.After(killedWait):
	}
}

// helperSet are the helpers running.
type helperSet struct {
	mu    sync.Mutex
	l     []helperProc
	ended bool
}

// add keeps h until it is gone; once Shutdown has ended the helpers, it
// stops h at once.
func (s *helperSet) add(h helperProc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		h.stop()
		return
	}
	live := s.l[:0]
	for _, o := range s.l {
		select {
		case <-o.done:
		default:
			live = append(live, o)
		}
	}
	s.l = append(live, h)
}

// end stops the apps' helpers (app) or the others; from then on add stops
// what it is given.
func (s *helperSet) end(app bool) []helperProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
	var out []helperProc
	rest := s.l[:0]
	for _, h := range s.l {
		if h.app != app {
			rest = append(rest, h)
			continue
		}
		h.stop()
		out = append(out, h)
	}
	s.l = rest
	return out
}

// endHelpers stops the helpers no app uses.
func (c *Core) endHelpers() []helperProc {
	out := c.helpers.end(false)
	if w, ok := c.wifi.(*iwdRadio); ok {
		out = append(out, w.stopEAP())
	}
	return out
}
