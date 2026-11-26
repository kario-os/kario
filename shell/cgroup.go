package shell

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
// any process of the same user can undo it). An app over its memory limit,
// or stalled at it (memstall.go), is ended alone and the user is told why.
// Without a delegated group (a developer machine) apps share the shell's
// group and freezing is SIGSTOP.

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
	feat   cgFeatures
	// stalls: the apps' groups watched for a stall at their memory limit,
	// poll their triggers (memstall.go).
	stalls map[string]*stallWatch
	poll   *stallPoll
}

// cgFeatures is what the kernel gives the groups (24-sku-framework.md
// §7.5); without one the Shell does it another way or goes without.
type cgFeatures struct {
	Freezer    bool `json:"freezer"`           // cgroup.freeze, Linux 5.2: else SIGSTOP
	Kill       bool `json:"kill"`              // cgroup.kill, 5.14: else SIGKILL each
	CloneInto  bool `json:"clone_into_cgroup"` // 5.7: else held until moved (held)
	Memory     bool `json:"memory_max"`
	MemoryHigh bool `json:"memory_high"`
	Pids       bool `json:"pids"`
	CPU        bool `json:"cpu"`
	PSI        bool `json:"psi"`         // memory.pressure, 4.20
	PSITrigger bool `json:"psi_trigger"` // a trigger on it, 5.2: else the housekeeping samples it
}

func cgRead(path string) string {
	raw, _ := os.ReadFile(path)
	return strings.TrimSpace(string(raw))
}

// cgWrite never creates: a file cgroupfs does not have is a feature the
// kernel lacks.
func cgWrite(path, v string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(v)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func cgHas(dir, file string) bool {
	_, err := os.Stat(filepath.Join(dir, file))
	return err == nil
}

// cgHost is what setupCgroups looks at: this system, or a test's tree.
type cgHost struct {
	root      string // where cgroup2 is mounted
	self      string // /proc/self/cgroup
	euid      int
	delegated func(dir string) bool
	// cloneInto starts a process in the group as apps start.
	cloneInto func(group *os.File) error
}

func thisHost() cgHost {
	return cgHost{root: "/sys/fs/cgroup", self: cgRead("/proc/self/cgroup"), euid: os.Geteuid(),
		delegated: delegated, cloneInto: cloneIntoProbe}
}

// cgMode is the hierarchy /proc/self/cgroup shows: v2 (unified), hybrid
// (v1 controllers beside an empty v2 tree), v1, or none.
func cgMode(self string) string {
	switch {
	case self == "":
		return "none"
	case strings.HasPrefix(self, "0::") && !strings.Contains(self, "\n"):
		return "v2"
	case strings.HasPrefix(self, "0::") || strings.Contains(self, "\n0::"):
		return "hybrid"
	}
	return "v1"
}

// cloneIntoProbe starts a shell that does nothing in group the way apps
// start: kernels before 5.7 refuse CLONE_INTO_CGROUP (ENOSYS, E2BIG,
// EINVAL).
func cloneIntoProbe(group *os.File) error {
	cmd := exec.Command("/bin/sh", "-c", ":")
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(group.Fd())}
	if err := StartProcess(cmd); err != nil {
		return err
	}
	return cmd.Wait()
}

// setupCgroups takes over the shell's delegated group, or reports why not.
func setupCgroups() (*cgroups, error) { return setupCgroupsOn(thisHost()) }

func setupCgroupsOn(h cgHost) (*cgroups, error) {
	if h.euid != 0 {
		return nil, errors.New("not root")
	}
	if m := cgMode(h.self); m != "v2" {
		return nil, fmt.Errorf("not on the unified (v2) hierarchy (%s)", m)
	}
	rel := strings.TrimPrefix(h.self, "0::")
	base := filepath.Join(h.root, rel)
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
	if !h.delegated(base) {
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
			return &cgroups{base: base, feat: probeGroups(h, base, shellGroup)}, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("enable %v: %w", enable, err)
}

// probeGroups looks at the delegated group and, with its controllers on,
// at the shell's own child group.
func probeGroups(h cgHost, base, shellGroup string) cgFeatures {
	f := cgFeatures{Freezer: cgHas(base, "cgroup.freeze"), Kill: cgHas(base, "cgroup.kill"), PSI: cgHas(base, "memory.pressure"),
		Memory: cgHas(shellGroup, "memory.max"), MemoryHigh: cgHas(shellGroup, "memory.high"),
		Pids: cgHas(shellGroup, "pids.max"), CPU: cgHas(shellGroup, "cpu.weight")}
	if d, err := os.Open(shellGroup); err == nil {
		f.CloneInto = h.cloneInto(d) == nil
		d.Close()
	}
	f.PSITrigger = f.PSI && psiTriggers(base)
	return f
}

// held: a new process cannot be born in its group (a systemd scope, or a
// kernel before 5.7 without CLONE_INTO_CGROUP), so the launcher waits
// until the Shell has moved it in (attach), and nothing of it runs outside.
func (g *cgroups) held() bool { return g.scopes || !g.feat.CloneInto }

// holdLauncher makes the sandbox launcher cmd wait before it does anything
// until release.
func holdLauncher(cmd *exec.Cmd) (release func(), err error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, r)
	cmd.Env = append(cmd.Env, "PHONE_SANDBOX_HOLD="+strconv.Itoa(2+len(cmd.ExtraFiles)))
	return func() {
		_, _ = w.Write([]byte{1})
		w.Close()
		r.Close()
	}, nil
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
	g.unwatch(id)
	d := g.dir(id)
	// A group left from an earlier start goes first, with whatever of that
	// start is still in it: the page cache a dead tree leaves stays charged
	// to its group and would count against the new one. One that will not
	// go is reused, as before.
	if _, err := os.Stat(d); err == nil && !removeEmpty(d, 0) {
		g.killAll(d, 3*time.Second)
		removeEmpty(d, 3*time.Second)
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return nil, err
	}
	// memory.oom.group (4.19): the kernel's OOM kill ends the whole group,
	// not only its biggest process (a helper, leaving the app half there
	// and the Shell none the wiser). No memory.high: above it the kernel
	// throttles the app, slowing it down instead of ending it.
	for file, v := range map[string]string{"memory.max": strconv.FormatInt(l.mem, 10), "memory.swap.max": "0", "memory.oom.group": "1",
		"pids.max": strconv.Itoa(l.pids), "cpu.weight": strconv.Itoa(cpuFG), "cgroup.freeze": "0"} {
		if _, err := os.Stat(filepath.Join(d, file)); err == nil {
			if err := cgWrite(filepath.Join(d, file), v); err != nil {
				return nil, fmt.Errorf("%s: %w", file, err)
			}
		}
	}
	return os.Open(d)
}

var errNoFreezer = errors.New("no cgroup freezer (Linux 5.2)")

func (g *cgroups) freeze(id string, on bool) error {
	if !g.feat.Freezer {
		return errNoFreezer
	}
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
	return int(cgKey(cgRead(filepath.Join(g.dir(id), "memory.events")), "oom_kill"))
}

// cgKey is one value of a flat-keyed group file (memory.events,
// memory.stat).
func cgKey(file, name string) int64 {
	for _, l := range strings.Split(file, "\n") {
		if n, ok := strings.CutPrefix(l, name+" "); ok {
			v, _ := strconv.ParseInt(n, 10, 64)
			return v
		}
	}
	return 0
}

// release kills what is left of the app's tree and removes the group.
func (g *cgroups) release(id string) {
	g.unwatch(id)
	g.killAll(g.dir(id), time.Second)
	if g.scopes {
		// systemd collects the empty scope.
		delete(g.dirs, id)
		return
	}
	removeEmpty(g.dir(id), time.Second)
}

// killAll ends every process in the group dir: cgroup.kill, or before
// Linux 5.14 SIGKILL to each until none is left (one may fork meanwhile),
// for at most wait.
func (g *cgroups) killAll(dir string, wait time.Duration) {
	if g.feat.Kill {
		_ = cgWrite(filepath.Join(dir, "cgroup.kill"), "1")
		return
	}
	for end := time.Now().Add(wait); ; time.Sleep(10 * time.Millisecond) {
		pids := strings.Fields(cgRead(filepath.Join(dir, "cgroup.procs")))
		for _, p := range pids {
			if pid, err := strconv.Atoi(p); err == nil && pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		if len(pids) == 0 || time.Now().After(end) {
			return
		}
	}
}

// moveIn puts a process the launcher holds (held) into the group made
// for it.
func (g *cgroups) moveIn(id string, pid int, l limits) error {
	f, err := g.prepareLimits(id, l)
	if err != nil {
		return err
	}
	f.Close()
	return cgWrite(filepath.Join(g.dir(id), "cgroup.procs"), strconv.Itoa(pid))
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
