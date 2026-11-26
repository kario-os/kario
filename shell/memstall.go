package shell

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// An app stalled at its memory limit (Android's lmkd, for one app). At
// memory.max the kernel first reclaims what of the group it can: with no
// swap, the file pages charged to it, the app's own program and libraries,
// which it faults straight back in. The kernel declares OOM only after
// rounds of reclaim that free nothing, so an app can thrash like that for
// minutes, frozen, before it is ended. The Shell ends such an app itself,
// as the kernel's OOM kill would (appExited tells the user the same):
// when all its tasks were stalled on memory (PSI "full" in the group's
// memory.pressure) for stallShare of the last stallFor or more, while it
// was at its limit all along (memory.events "max" went up between every
// two samples). Reading files at the limit reclaims too, but clean page
// cache goes cheaply and is not read back: little full stall.
//
// A memory.pressure trigger (Linux 5.2) wakes the Shell when an app has
// stalled that long within a second; the housekeeping samples too (every
// half second with the screen on, every 5 s off), and is all there is
// without triggers.

// On QEMU a thrashing group stalled 0.60 of the time (median over 2 s; 5th
// percentile 0.56, lowest 0.49), a group reading files at its limit 0.19 at
// most; when the kernel's OOM kill comes promptly, it comes within a
// second of the limit.
const (
	stallShare  = 0.5
	stallFor    = 2 * time.Second
	stallWindow = time.Second
	// stallMinGap: a sample this soon after the last one (a trigger just
	// after the housekeeping) is not judged.
	stallMinGap = 200 * time.Millisecond
	// stallNote: how often an app at its limit but not stalled enough to
	// be ended is logged, with its stall (what the thresholds are set by).
	stallNote = 10 * time.Second
)

// stallSample is what a group's memory.pressure (the full total, µs) and
// memory.events (max) said at a time.
type stallSample struct {
	at        time.Time
	full, max int64
}

// readStallSample reads them from the group's directory.
func readStallSample(dir string, at time.Time) (stallSample, error) {
	full, ok := psiTotal(cgRead(filepath.Join(dir, "memory.pressure")), "full")
	if !ok {
		return stallSample{}, errors.New("no full line in memory.pressure")
	}
	return stallSample{at: at, full: full, max: cgKey(cgRead(filepath.Join(dir, "memory.events")), "max")}, nil
}

// psiTotal is the total= of the some or full line of a pressure file.
func psiTotal(s, kind string) (int64, bool) {
	for _, l := range strings.Split(s, "\n") {
		rest, ok := strings.CutPrefix(l, kind+" ")
		if !ok {
			continue
		}
		for _, f := range strings.Fields(rest) {
			if v, ok := strings.CutPrefix(f, "total="); ok {
				n, err := strconv.ParseInt(v, 10, 64)
				return n, err == nil
			}
		}
	}
	return 0, false
}

// stallWatch is one app group's samples while it is at its limit, and
// what the last judgement found.
type stallWatch struct {
	// at: the samples since the group was last seen below its limit, the
	// oldest the newest one at least stallFor before the last.
	at []stallSample
	// share: the stall over the window last judged, hits the limit's.
	share float64
	hits  int64
	// noted: when it was last logged at its limit without being ended.
	noted time.Time
	// trigger: the memory.pressure trigger's fd, -1 without one.
	trigger int
	// ended: the Shell ended the app at its limit.
	ended bool
}

// next takes a sample and returns how long the app has been at its limit,
// at least stallFor (share and hits are of that window), or 0.
func (w *stallWatch) next(s stallSample) time.Duration {
	n := len(w.at)
	switch {
	case n > 0 && s.at.Sub(w.at[n-1].at) < stallMinGap && s.full >= w.at[n-1].full && s.max >= w.at[n-1].max:
		return 0
	case n == 0 || s.max <= w.at[n-1].max || s.full < w.at[n-1].full:
		w.at = append(w.at[:0], s)
		return 0
	}
	w.at = append(w.at, s)
	for len(w.at) > 2 && s.at.Sub(w.at[1].at) >= stallFor {
		w.at = slices.Delete(w.at, 0, 1)
	}
	first := w.at[0]
	d := s.at.Sub(first.at)
	if d < stallFor {
		return 0
	}
	w.share, w.hits = float64(s.full-first.full)/float64(d.Microseconds()), s.max-first.max
	return d
}

// stalled: the window next returned ends the app.
func (w *stallWatch) stalled(d time.Duration) bool { return d >= stallFor && w.share >= stallShare }

// pause forgets the samples (a frozen app cannot stall; it starts over
// when thawed).
func (w *stallWatch) pause() { w.at = w.at[:0] }

// stallPoll waits on the groups' memory.pressure triggers, all in one
// epoll, and hands the group whose trigger fired to fired.
type stallPoll struct {
	epfd  int
	mu    sync.Mutex
	ids   map[int32]string
	fired func(id string)
}

func newStallPoll(fired func(id string)) (*stallPoll, error) {
	fd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		return nil, err
	}
	p := &stallPoll{epfd: fd, ids: map[int32]string{}, fired: fired}
	go p.run()
	return p, nil
}

func (p *stallPoll) run() {
	events := make([]unix.EpollEvent, 16)
	for {
		n, err := unix.EpollWait(p.epfd, events, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return
		}
		for _, ev := range events[:n] {
			p.mu.Lock()
			id := p.ids[ev.Fd]
			// A trigger whose group is going reports an error until it
			// is closed: once is enough.
			if ev.Events&(unix.EPOLLERR|unix.EPOLLHUP) != 0 {
				_ = unix.EpollCtl(p.epfd, unix.EPOLL_CTL_DEL, int(ev.Fd), nil)
				delete(p.ids, ev.Fd)
			}
			p.mu.Unlock()
			if id != "" {
				p.fired(id)
			}
		}
	}
}

// psiTrigger opens a trigger on a group's memory.pressure: an event when
// all its tasks have stalled stallShare of a stallWindow.
func psiTrigger(dir string) (int, error) {
	fd, err := unix.Open(filepath.Join(dir, "memory.pressure"), unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	window := stallWindow.Microseconds()
	if _, err := unix.Write(fd, fmt.Appendf(nil, "full %d %d", int64(float64(window)*stallShare), window)); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// psiTriggers: the kernel takes a trigger on the group's memory.pressure.
func psiTriggers(dir string) bool {
	fd, err := psiTrigger(dir)
	if err == nil {
		unix.Close(fd)
	}
	return err == nil
}

// add starts waiting on a trigger.
func (p *stallPoll) add(fd int, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := unix.EpollCtl(p.epfd, unix.EPOLL_CTL_ADD, fd, &unix.EpollEvent{Events: unix.EPOLLPRI, Fd: int32(fd)}); err != nil {
		return err
	}
	p.ids[int32(fd)] = id
	return nil
}

// remove stops waiting on a trigger and closes it.
func (p *stallPoll) remove(fd int) {
	p.mu.Lock()
	_ = unix.EpollCtl(p.epfd, unix.EPOLL_CTL_DEL, fd, nil)
	delete(p.ids, int32(fd))
	p.mu.Unlock()
	unix.Close(fd)
}

// stall is the app group's watch, made (with its trigger where the kernel
// has them) on first use.
func (g *cgroups) stall(id string, fired func(id string)) *stallWatch {
	if w := g.stalls[id]; w != nil {
		return w
	}
	if g.stalls == nil {
		g.stalls = map[string]*stallWatch{}
	}
	w := &stallWatch{trigger: -1}
	g.stalls[id] = w
	if !g.feat.PSITrigger {
		return w
	}
	if g.poll == nil {
		p, err := newStallPoll(fired)
		if err != nil {
			return w
		}
		g.poll = p
	}
	if fd, err := psiTrigger(g.dir(id)); err == nil {
		if g.poll.add(fd, id) == nil {
			w.trigger = fd
		} else {
			unix.Close(fd)
		}
	}
	return w
}

// unwatch drops the group's watch and closes its trigger: before the
// group goes, whose trigger would report errors from then on.
func (g *cgroups) unwatch(id string) {
	w := g.stalls[id]
	if w == nil {
		return
	}
	if w.trigger >= 0 {
		g.poll.remove(w.trigger)
	}
	delete(g.stalls, id)
}

// overMemoryLimit: the app ended over its memory limit, by the kernel's OOM
// killer or the Shell (stalled there).
func (g *cgroups) overMemoryLimit(id string) bool {
	return g.oomKills(id) > 0 || g.stalls[id] != nil && g.stalls[id].ended
}

// checkStalls runs with the housekeeping: every app in its group is
// sampled.
func (c *Core) checkStalls() {
	if c.cg == nil || !c.cg.feat.PSI || !c.cg.feat.Memory {
		return
	}
	now := time.Now()
	for _, a := range c.apps {
		c.checkStall(a, now)
	}
}

// stallFired is a trigger's event, from the poll's goroutine.
func (c *Core) stallFired(id string) {
	c.Post(func() {
		if a := c.appByID(id); a != nil {
			c.checkStall(a, time.Now())
		}
	})
}

func (c *Core) checkStall(a *App, now time.Time) {
	id := a.M.ID
	if !a.running || a.proc == nil || a.killed || a.ending {
		return
	}
	w := c.cg.stall(id, c.stallFired)
	if w.ended {
		return
	}
	if a.frozen {
		w.pause()
		return
	}
	s, err := readStallSample(c.cg.dir(id), now)
	if err != nil {
		return
	}
	d := w.next(s)
	if !w.stalled(d) {
		if d >= stallFor && now.Sub(w.noted) >= stallNote {
			w.noted = now
			c.log.Printf("app %s: at its memory limit (%d MB): all its tasks waited on memory %.0f%% of the last %.1f s, the limit hit %d times",
				id, appMemoryMax>>20, w.share*100, d.Seconds(), w.hits)
		}
		return
	}
	c.log.Printf("app %s: stalled at its memory limit (%d MB): all its tasks waited on memory %.0f%% of the last %.1f s, the limit hit %d times, %d MB of file pages left; ending it",
		id, appMemoryMax>>20, w.share*100, d.Seconds(), w.hits, cgKey(c.cg.value(id, "memory.stat"), "file")>>20)
	w.ended = true
	c.kill(a)
}
