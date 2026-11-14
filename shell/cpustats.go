package shell

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/ui"
)

// Background CPU, for the battery: what each app uses while it is not on
// the screen, from its cgroup's cpu.stat (or its process's /proc stat
// without cgroups), kept for a day. Frozen apps use none; the ones that
// keep running (a job, a push, the seconds before freezing, a background
// mode) are counted. One that averages more than bgCPUAlert of a core over
// bgCPUWindow while not in a call or playing is named to the user once a
// day, with a way to stop it, as iOS and Android name apps draining the
// battery in the background.

const (
	cpuEvery    = 30 * time.Second
	bgCPUWindow = 10 * time.Minute
	bgCPUAlert  = 0.05 // of one core
	cpuKeep     = 24 * time.Hour
	clockTicks  = 100 // USER_HZ, the unit of /proc stat times on Linux
)

type cpuSample struct {
	at     time.Time
	bg, fg time.Duration
}

type appCPU struct {
	last    time.Duration // cumulative CPU at the last look
	pid     int           // the process it was read from
	samples []cpuSample
	alerted time.Time
}

type cpuStats struct {
	apps    map[string]*appCPU
	checked time.Time
	// charge: CPU added as if an app had used it (selftest).
	charge map[string]time.Duration
}

// appCPUTime is the app's cumulative CPU time.
var appCPUTime = func(c *Core, a *App) (time.Duration, bool) {
	if c.cg != nil {
		for _, l := range strings.Split(c.cg.value(a.M.ID, "cpu.stat"), "\n") {
			if v, ok := strings.CutPrefix(l, "usage_usec "); ok {
				n, err := strconv.ParseInt(v, 10, 64)
				return time.Duration(n) * time.Microsecond, err == nil
			}
		}
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(a.proc.Pid) + "/stat")
	if err != nil {
		return 0, false
	}
	return procCPU(string(raw))
}

// procCPU is utime plus stime of a /proc/<pid>/stat line (fields 14 and
// 15). The command name in parentheses may hold spaces and parentheses:
// the fields count from after the last closing one.
func procCPU(stat string) (time.Duration, bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(stat[i+1:])
	if len(f) < 13 {
		return 0, false
	}
	ut, err1 := strconv.ParseInt(f[11], 10, 64)
	st, err2 := strconv.ParseInt(f[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return time.Duration(ut+st) * time.Second / clockTicks, true
}

// checkCPU runs with the housekeeping.
func (c *Core) checkCPU() {
	now := c.now()
	if now.Sub(c.cpu.checked) < cpuEvery {
		return
	}
	c.cpu.checked = now
	if c.cpu.apps == nil {
		c.cpu.apps = map[string]*appCPU{}
	}
	for _, a := range c.apps {
		if !a.running || a.proc == nil {
			continue
		}
		cur, ok := appCPUTime(c, a)
		if !ok {
			continue
		}
		cur += c.cpu.charge[a.M.ID]
		s := c.cpu.apps[a.M.ID]
		if s == nil || s.pid != a.proc.Pid {
			// A new process starts from its own count.
			if s == nil {
				s = &appCPU{}
				c.cpu.apps[a.M.ID] = s
			}
			s.pid, s.last = a.proc.Pid, cur
			continue
		}
		d := max(cur-s.last, 0)
		s.last = cur
		smp := cpuSample{at: now}
		if a == c.fg {
			smp.fg = d
		} else {
			smp.bg = d
		}
		s.samples = append(s.samples, smp)
		for len(s.samples) > 0 && now.Sub(s.samples[0].at) > cpuKeep {
			s.samples = s.samples[1:]
		}
		c.checkBackgroundCPU(a, s, now)
	}
}

func (c *Core) checkBackgroundCPU(a *App, s *appCPU, now time.Time) {
	if a == c.fg || c.inCall(a) || c.runsInBackground(a) || now.Sub(s.alerted) < cpuKeep {
		return
	}
	var bg time.Duration
	for _, x := range s.samples {
		if now.Sub(x.at) <= bgCPUWindow {
			bg += x.bg
		}
	}
	share := float64(bg) / float64(bgCPUWindow)
	if share <= bgCPUAlert {
		return
	}
	s.alerted = now
	pct := int(share*100 + 0.5)
	c.log.Printf("%s: %d%% of a core in the background over %s", a.M.ID, pct, bgCPUWindow)
	c.addNotif(c.sys, ui.Notification{ID: "bgcpu:" + a.M.ID,
		Title:   tr.T("“%s” is using the battery in the background", a.M.Name),
		Body:    tr.T("%d%% of a core over the last 10 minutes", pct),
		Actions: []ui.Action{{ID: "stop", Label: tr.T("Stop it"), Key: "s"}}})
}

// sysNotifAction is an action on one of the system's own notifications.
func (c *Core) sysNotifAction(id, action string) {
	if app, ok := strings.CutPrefix(id, "bgcpu:"); ok && action == "stop" {
		if a := c.appByID(app); a != nil && a.running && a != c.fg {
			c.log.Printf("%s: stopped by the user for its background CPU", app)
			a.ending = true
			c.kill(a)
		}
	}
}

// AppCPU is an app's CPU over the last day, in the background and on the
// screen (Settings › Battery, selftest).
type AppCPU struct {
	ID, Name   string
	Background time.Duration
	Foreground time.Duration
}

// CPUUsage lists the apps by their background CPU, most first.
func (c *Core) CPUUsage() []AppCPU {
	now := c.now()
	var out []AppCPU
	for id, s := range c.cpu.apps {
		u := AppCPU{ID: id}
		if a := c.appByID(id); a != nil {
			u.Name = a.M.Name
		}
		for _, x := range s.samples {
			if now.Sub(x.at) <= cpuKeep {
				u.Background += x.bg
				u.Foreground += x.fg
			}
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Background != out[j].Background {
			return out[i].Background > out[j].Background
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// energyView is the Battery page's list: the apps that used the CPU.
func (c *Core) energyView() []ui.EnergyEntry {
	out := []ui.EnergyEntry{}
	for _, u := range c.CPUUsage() {
		if u.Foreground+u.Background < 100*time.Millisecond {
			continue
		}
		out = append(out, ui.EnergyEntry{App: u.ID, Name: firstNonEmpty(u.Name, u.ID),
			ForegroundMS: u.Foreground.Milliseconds(), BackgroundMS: u.Background.Milliseconds()})
	}
	return out
}

// ChargeCPU adds CPU to an app as if it had used it (selftest).
func (c *Core) ChargeCPU(id string, d time.Duration) {
	if c.cpu.charge == nil {
		c.cpu.charge = map[string]time.Duration{}
	}
	c.cpu.charge[id] += d
}
