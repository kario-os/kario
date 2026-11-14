package shell

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The low-memory killer (Android's lmkd, iOS's jetsam): when the system
// runs short, the shell ends background apps itself, least recently used
// first, one at a time, before the kernel's OOM killer picks a victim that
// may be the app on the screen. The foreground app, apps working in the
// background (a call, audio playing, VoIP) and system apps are never
// chosen. An ended app is cold-started the next time it is opened; frozen
// apps lose nothing they had not already been told to save ("background").
//
// Pages go first: a background app's web process (a page can use hundreds
// of MB) is ended before any app, system apps' included, and the app keeps
// running; its pages load again when it is back on the screen, the way a
// browser discards background tabs.

const (
	lowMemFree    = 10   // percent of MemTotal still available
	lowMemPSI     = 20.0 // percent of time some task stalled on memory (avg10)
	lowMemEvery   = time.Second
	lowMemSettled = 2 * time.Second // after a kill, before judging again
)

type memState struct {
	totalKB, availKB int
	psi              float64 // -1 without PSI
}

func (m memState) low() bool {
	return (m.totalKB > 0 && m.availKB*100 < m.totalKB*lowMemFree) || m.psi >= lowMemPSI
}

// readMem reads /proc/meminfo and memory PSI: the apps' delegated group when
// there is one (pressure from the apps themselves), else the whole system.
func (c *Core) readMem() memState {
	m := memState{psi: -1}
	raw, _ := os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(raw), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.Atoi(f[1])
		switch f[0] {
		case "MemTotal:":
			m.totalKB = n
		case "MemAvailable:":
			m.availKB = n
		}
	}
	path := "/proc/pressure/memory"
	if c.cg != nil {
		path = filepath.Join(c.cg.base, "memory.pressure")
	}
	if raw, err := os.ReadFile(path); err == nil {
		m.psi = parsePSI(string(raw))
	}
	return m
}

// parsePSI returns avg10 of the "some" line of a pressure file.
func parsePSI(s string) float64 {
	for _, l := range strings.Split(s, "\n") {
		rest, ok := strings.CutPrefix(l, "some ")
		if !ok {
			continue
		}
		for _, f := range strings.Fields(rest) {
			if v, ok := strings.CutPrefix(f, "avg10="); ok {
				x, err := strconv.ParseFloat(v, 64)
				if err == nil {
					return x
				}
			}
		}
	}
	return -1
}

// recentKept is how many of the most recently used background apps are
// ended last (switching back to them is the common case).
const recentKept = 2

// lowMemVictim is the background app to end first. By class: apps never
// brought to the screen (started in the background by a push or a job),
// then those used longer ago, then the recentKept most recent; within a
// class the one using the most memory, which frees the most.
func (c *Core) lowMemVictim() *App {
	ok := func(a *App) bool {
		return a.running && !a.ending && a.proc != nil && !a.system && a != c.fg && !c.runsInBackground(a) && !c.inCall(a)
	}
	class := c.bgClass()
	var best *App
	var bestMem int64
	for _, a := range c.apps {
		if !ok(a) {
			continue
		}
		mem := c.appMemory(a)
		if best == nil || class[a] < class[best] || (class[a] == class[best] && mem > bestMem) {
			best, bestMem = a, mem
		}
	}
	return best
}

// appMemory is what the app uses: its cgroup's memory.current, else the
// resident set of its process.
var appMemoryOf = func(c *Core, a *App) int64 {
	if c.cg != nil {
		if v, err := strconv.ParseInt(c.cg.value(a.M.ID, "memory.current"), 10, 64); err == nil {
			return v
		}
	}
	return statmRSS(a.proc.Pid)
}

func (c *Core) appMemory(a *App) int64 { return appMemoryOf(c, a) }

// bgClass orders background apps for the low-memory killer: 0 never
// brought to the screen, 1 used longer ago, 2 the recentKept most recent.
func (c *Core) bgClass() map[*App]int {
	class := map[*App]int{}
	recent := 0
	for _, a := range c.mru {
		if a == c.fg || !a.running {
			continue
		}
		class[a] = 1
		if recent < recentKept {
			class[a] = 2
		}
		recent++
	}
	return class
}

// lowMemWebVictim is the web process to end before any app: a background
// app's, by the same classes as the apps, the biggest within a class.
func (c *Core) lowMemWebVictim() *webProc {
	class := c.bgClass()
	var best *webProc
	var bestMem int64
	for _, a := range c.apps {
		p := c.webprocs[a.M.ID]
		if p == nil || p.ended || a == c.fg || !a.running || c.runsInBackground(a) || c.inCall(a) {
			continue
		}
		mem := webMemoryOf(c, p)
		if best == nil || class[a] < class[best.app] || (class[a] == class[best.app] && mem > bestMem) {
			best, bestMem = p, mem
		}
	}
	return best
}

// webMemoryOf is what an app's pages use: their cgroup's memory.current,
// else the resident set of the web process.
var webMemoryOf = func(c *Core, p *webProc) int64 {
	if c.cg != nil {
		if v, err := strconv.ParseInt(c.cg.value(webGroup(p.app), "memory.current"), 10, 64); err == nil {
			return v
		}
	}
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return statmRSS(p.cmd.Process.Pid)
}

func statmRSS(pid int) int64 {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(raw))
	if len(f) < 2 {
		return 0
	}
	pages, _ := strconv.ParseInt(f[1], 10, 64)
	return pages * int64(os.Getpagesize())
}

// checkMemory runs with the shell's housekeeping.
func (c *Core) checkMemory() {
	now := time.Now()
	var m memState
	if c.lowMem.simulate > 0 {
		c.lowMem.simulate--
		m = memState{totalKB: 1 << 20, availKB: 1 << 10, psi: -1}
	} else {
		if now.Sub(c.lowMem.checked) < lowMemEvery || now.Sub(c.lowMem.killed) < lowMemSettled {
			return
		}
		c.lowMem.checked = now
		m = c.readMem()
	}
	if !m.low() {
		return
	}
	if p := c.lowMemWebVictim(); p != nil {
		c.log.Printf("low memory (available %d of %d kB, psi %.1f): ending the pages of background app %s", m.availKB, m.totalKB, m.psi, p.app.M.ID)
		c.lowMem.killed, c.lowMem.webCount = now, c.lowMem.webCount+1
		c.stopWebProc(p)
		return
	}
	a := c.lowMemVictim()
	if a == nil {
		// Said once a minute: the pressure may be the foreground's own.
		if now.Sub(c.lowMem.idle) >= time.Minute {
			c.lowMem.idle = now
			c.log.Printf("low memory (available %d of %d kB, psi %.1f): no background app to end", m.availKB, m.totalKB, m.psi)
		}
		return
	}
	c.log.Printf("low memory (available %d of %d kB, psi %.1f): ending background app %s", m.availKB, m.totalKB, m.psi, a.M.ID)
	c.lowMem.killed, c.lowMem.count = now, c.lowMem.count+1
	a.ending = true
	c.kill(a)
}

// SimulateLowMemory makes the next n memory checks read as low, without the
// pauses between them (selftest).
func (c *Core) SimulateLowMemory(n int) { c.lowMem.simulate = n }

// LowMemoryKills counts apps ended for low memory.
func (c *Core) LowMemoryKills() int { return c.lowMem.count }

// LowMemoryPageStops counts background apps' pages ended for low memory.
func (c *Core) LowMemoryPageStops() int { return c.lowMem.webCount }
