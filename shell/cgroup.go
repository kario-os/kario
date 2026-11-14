package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

// Every app gets its own cgroup v2 group (R8) under the group systemd
// delegates to the shell (Delegate=yes in its unit): limits on memory and
// processes, a CPU weight that favours the foreground app, and the freezer,
// which stops the whole process tree at once (SIGSTOP misses children, and
// any process of the same user can undo it). An app over its memory limit is
// killed alone and the user is told why. Without a delegated group (a
// developer machine) apps share the shell's group and freezing is SIGSTOP.

const (
	appMemoryMax = 192 << 20
	appPidsMax   = 128
	cpuFG, cpuBG = 400, 50 // cpu.weight: default 100
)

// goMemLimit is the soft limit for Go apps (the SDK's language): their
// collector works harder well before the kernel's hard limit kills them.
// A quarter of headroom covers memory outside the Go heap (stacks, cgo,
// mapped files).
var goMemLimit = strconv.Itoa(appMemoryMax/4*3>>20) + "MiB"

type cgroups struct {
	base string // the delegated group; the shell itself moves to base/shell
	// scopes: every app in a systemd scope of its own (scopes.go); dirs
	// are their groups.
	scopes bool
	bus    *dbus.Conn
	dirs   map[string]string
}

func cgRead(path string) string {
	raw, _ := os.ReadFile(path)
	return strings.TrimSpace(string(raw))
}

func cgWrite(path, v string) error { return os.WriteFile(path, []byte(v), 0o644) }

// setupCgroups takes over the shell's delegated group, or reports why not.
func setupCgroups() (*cgroups, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("not root")
	}
	self := cgRead("/proc/self/cgroup")
	rel, ok := strings.CutPrefix(self, "0::")
	if !ok || strings.Contains(self, "\n") {
		return nil, errors.New("not on the unified (v2) hierarchy")
	}
	base := filepath.Join("/sys/fs/cgroup", rel)
	if rel == "/" {
		return nil, errors.New("the shell runs in the root group")
	}
	// With DelegateSubgroup=shell systemd starts the shell in base/shell
	// itself: then the service group's root never holds a process, which a
	// restart needs (it cannot put the new process into a group whose
	// controllers are enabled for its children).
	if strings.HasSuffix(rel, "/shell") {
		base = filepath.Dir(base)
	}
	// Root may write any group, so writability says nothing: systemd marks
	// the groups it delegates (Delegate=yes) with trusted.delegate.
	if !delegated(base) {
		return nil, fmt.Errorf("group %s is not delegated to the shell (Delegate=yes)", rel)
	}
	// No processes may stay in a group that has controllers for its
	// children: everything in the service (the shell, the boot script,
	// private services) moves to base/shell.
	shellGroup := filepath.Join(base, "shell")
	if err := os.MkdirAll(shellGroup, 0o755); err != nil {
		return nil, err
	}
	var enable []string
	avail := " " + cgRead(filepath.Join(base, "cgroup.controllers")) + " "
	for _, ctl := range []string{"memory", "pids", "cpu"} {
		if strings.Contains(avail, " "+ctl+" ") {
			enable = append(enable, "+"+ctl)
		}
	}
	// The services keep starting children meanwhile (mediad's ffprobe,
	// audiod's aplay): one forked by a process not moved yet stays in base
	// and the controllers cannot go on, so move until none is left.
	var err error
	for try := 0; try < 50; try++ {
		for _, pid := range strings.Fields(cgRead(filepath.Join(base, "cgroup.procs"))) {
			_ = cgWrite(filepath.Join(shellGroup, "cgroup.procs"), pid)
		}
		if err = cgWrite(filepath.Join(base, "cgroup.subtree_control"), strings.Join(enable, " ")); err == nil {
			return &cgroups{base: base}, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("enable %v: %w", enable, err)
}

func (g *cgroups) dir(id string) string {
	if g.scopes {
		if d := g.dirs[id]; d != "" {
			return d
		}
		return filepath.Join("/sys/fs/cgroup", "no-scope", id)
	}
	return filepath.Join(g.base, "app."+id)
}

// limits are a group's memory and process limits.
type limits struct {
	mem  int64
	pids int
}

var appLimits = limits{mem: appMemoryMax, pids: appPidsMax}

// prepare creates (or resets) the app's group and returns it open, for
// starting the process directly inside it (CLONE_INTO_CGROUP).
func (g *cgroups) prepare(id string) (*os.File, error) { return g.prepareLimits(id, appLimits) }

func (g *cgroups) prepareLimits(id string, l limits) (*os.File, error) {
	d := g.dir(id)
	// A group left from an earlier start goes first, with whatever of that
	// start is still in it: the page cache a dead tree leaves stays charged
	// to its group and would count against the new one. One that will not
	// go is reused, as before.
	if _, err := os.Stat(d); err == nil && !removeEmpty(d, 0) {
		_ = cgWrite(filepath.Join(d, "cgroup.kill"), "1")
		removeEmpty(d, 3*time.Second)
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return nil, err
	}
	for file, v := range map[string]string{"memory.max": strconv.FormatInt(l.mem, 10), "memory.swap.max": "0",
		"pids.max": strconv.Itoa(l.pids), "cpu.weight": strconv.Itoa(cpuFG), "cgroup.freeze": "0"} {
		if _, err := os.Stat(filepath.Join(d, file)); err == nil {
			if err := cgWrite(filepath.Join(d, file), v); err != nil {
				return nil, fmt.Errorf("%s: %w", file, err)
			}
		}
	}
	return os.Open(d)
}

func (g *cgroups) freeze(id string, on bool) error {
	return cgWrite(filepath.Join(g.dir(id), "cgroup.freeze"), map[bool]string{true: "1", false: "0"}[on])
}

func (g *cgroups) frozen(id string) bool {
	return strings.Contains(cgRead(filepath.Join(g.dir(id), "cgroup.events")), "frozen 1")
}

func (g *cgroups) weight(id string, fg bool) {
	_ = cgWrite(filepath.Join(g.dir(id), "cpu.weight"), strconv.Itoa(map[bool]int{true: cpuFG, false: cpuBG}[fg]))
}

// oomKills counts the processes the kernel killed for the group's memory limit.
func (g *cgroups) oomKills(id string) int {
	for _, l := range strings.Split(cgRead(filepath.Join(g.dir(id), "memory.events")), "\n") {
		if n, ok := strings.CutPrefix(l, "oom_kill "); ok {
			v, _ := strconv.Atoi(n)
			return v
		}
	}
	return 0
}

// release kills what is left of the app's tree and removes the group.
func (g *cgroups) release(id string) {
	_ = cgWrite(filepath.Join(g.dir(id), "cgroup.kill"), "1")
	if g.scopes {
		// systemd collects the empty scope.
		delete(g.dirs, id)
		return
	}
	removeEmpty(g.dir(id), time.Second)
}

// removeEmpty removes a group once its processes are gone (cgroup.kill
// does not wait for them); the memory still charged to it moves to the
// parent. False if they are still there after wait.
func removeEmpty(dir string, wait time.Duration) bool {
	end := time.Now().Add(wait)
	for {
		err := os.Remove(dir)
		if err == nil || os.IsNotExist(err) {
			return true
		}
		if time.Now().After(end) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Value reads one of the app group's files (selftest, reports).
func (g *cgroups) value(id, file string) string { return cgRead(filepath.Join(g.dir(id), file)) }

func delegated(dir string) bool {
	buf := make([]byte, 8)
	for _, attr := range []string{"trusted.delegate", "user.delegate"} {
		if n, err := syscall.Getxattr(dir, attr, buf); err == nil && string(buf[:n]) == "1" {
			return true
		}
	}
	return false
}
