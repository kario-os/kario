package shell

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kario-os/kario/ui"
)

// Background apps are suspended shortly after they leave the screen, the way
// iOS suspends apps and Android freezes cached ones: they get the
// "background" lifecycle message, a grace period to save state, then SIGSTOP.
// Only apps declaring a background mode the system supports keep running:
// VoIP (with its permission), or audio while their media plays.
// Production uses the cgroup v2 freezer, which also covers child processes.

const defaultFreezeAfter = 10 * time.Second

func (c *Core) runsInBackground(a *App) bool {
	for _, m := range a.M.Background {
		if m == ui.PermVoIP && a.M.Has(ui.PermVoIP) {
			return true
		}
		// Background audio (like iOS): only while its media is playing.
		if m == "audio" && c.appAudioPlaying(a.M.ID) {
			return true
		}
	}
	return false
}

func (c *Core) freezeAfter() time.Duration {
	if c.cfg.FreezeAfter == 0 {
		return defaultFreezeAfter
	}
	return c.cfg.FreezeAfter
}

func (c *Core) freezeIdle() {
	after := c.freezeAfter()
	if after < 0 {
		return
	}
	for _, a := range c.apps {
		if !a.running || a.frozen || a == c.fg || a.proc == nil || a.bgSince.IsZero() ||
			time.Since(a.bgSince) < after || c.runsInBackground(a) || c.inCall(a) {
			continue
		}
		if err := c.freezeApp(a, true); err == nil {
			a.frozen = true
			c.log.Printf("froze %s after %s in background", a.M.ID, after)
		}
	}
}

func (c *Core) thaw(a *App) {
	a.bgSince = time.Time{}
	if a.frozen && a.proc != nil {
		_ = c.freezeApp(a, false)
		c.log.Printf("thawed %s", a.M.ID)
	}
	a.frozen = false
}

// freezeApp uses the app's cgroup freezer (the whole process tree), or
// SIGSTOP / SIGCONT without cgroups.
func (c *Core) freezeApp(a *App, on bool) error {
	c.freezeWeb(a, on)
	if c.cg != nil {
		return c.cg.freeze(a.M.ID, on)
	}
	return a.proc.Signal(map[bool]syscall.Signal{true: syscall.SIGSTOP, false: syscall.SIGCONT}[on])
}

func (c *Core) inCall(a *App) bool {
	for _, ci := range c.calls {
		if ci.app == a {
			return true
		}
	}
	return false
}

// ProcReport describes every app process for a failed selftest: whether it
// said hello, what it is executing (still phone-sandbox, or the app), and
// where the kernel has it waiting.
func (c *Core) ProcReport() []string {
	var out []string
	for _, a := range c.apps {
		if !a.running || a.proc == nil {
			continue
		}
		pid := strconv.Itoa(a.proc.Pid)
		read := func(name string) string {
			raw, err := os.ReadFile("/proc/" + pid + "/" + name)
			if err != nil {
				return "?"
			}
			return strings.TrimSpace(string(raw))
		}
		out = append(out, fmt.Sprintf("%s pid=%s ready=%v frozen=%v up=%s comm=%s wchan=%s", a.M.ID, pid, a.ready, a.frozen,
			time.Since(a.started).Round(time.Millisecond), read("comm"), read("wchan")))
		if !a.ready {
			for _, l := range strings.Split(read("stack"), "\n") {
				out = append(out, "    "+l)
			}
		}
	}
	return out
}

// KillApp ends an app process the way the system does under memory
// pressure (selftest).
func (c *Core) KillApp(id string) error {
	a := c.appByID(id)
	if a == nil || !a.running || a.proc == nil {
		return fmt.Errorf("%s is not running", id)
	}
	a.killed = true
	return a.proc.Kill()
}

// EndInBackground ends a background app the way the low-memory killer does,
// so it keeps a tombstone (selftest).
func (c *Core) EndInBackground(id string) error {
	a := c.appByID(id)
	if a == nil || !a.running || a.proc == nil || a == c.fg {
		return fmt.Errorf("%s is not running in the background", id)
	}
	a.ending = true
	c.kill(a)
	return nil
}

// SetFreezeAfter overrides the grace period (selftest); negative disables.
func (c *Core) SetFreezeAfter(d time.Duration) { c.cfg.FreezeAfter = d }

// ProcState reports what the kernel says about an app process: "frozen"
// (stopped), "running" or "stopped" (no process).
func (c *Core) ProcState(id string) string {
	a := c.appByID(id)
	if a == nil || !a.running || a.proc == nil {
		return "stopped"
	}
	if c.cg != nil {
		if c.cg.frozen(id) {
			return "frozen"
		}
		return "running"
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(a.proc.Pid) + "/stat")
	if err != nil {
		return "stopped"
	}
	s := string(raw)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) && (s[i+2] == 'T' || s[i+2] == 't') {
		return "frozen"
	}
	return "running"
}
