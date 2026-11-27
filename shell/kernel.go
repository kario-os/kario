package shell

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/kario-os/kario/cts"
	"github.com/kario-os/kario/sandbox"
)

// A phone may ship an old vendor kernel (24-sku-framework.md §7.5). What
// the sandbox and the app lifecycle use is probed once at start, not
// assumed: where something is missing the Shell does it another way or
// goes without, and says which protections are not in force, in its log,
// the dump and the compatibility report. Without seccomp filters,
// no_new_privs or the apps' namespaces it starts no app at all (as without
// phone-sandbox): those layers have no stand-in.

type kernelReport struct {
	Release string `json:"release"`
	// Cgroups is the hierarchy: v2, hybrid, v1 or none.
	Cgroups string         `json:"cgroups"`
	Sandbox sandbox.Kernel `json:"sandbox"`
	// Groups is what the per-app groups have; nil without them.
	Groups *cgFeatures `json:"groups,omitempty"`
	// Missing are the protections not in force, Fallback what is done
	// another way with nothing lost; by name, each with why.
	Missing  map[string]string `json:"missing"`
	Fallback map[string]string `json:"fallback"`
	// Refuse says why no app starts; empty when they do.
	Refuse string `json:"refuse,omitempty"`
	// Foreign says why the running kernel is not one this system ships
	// (foreignKernel), empty when it is: what it lacks is the other
	// system's.
	Foreign string `json:"foreign,omitempty"`
}

// kernelFacts is what assessKernel weighs.
type kernelFacts struct {
	release, cgroups string
	sandbox          sandbox.Kernel
	groups           *cgroups
	cgOff            string // why there are no groups
	root             bool
	psi              bool // /proc/pressure/memory
	memAvailable     bool // MemAvailable in /proc/meminfo (3.14)
}

func (c *Core) setupKernel() {
	if os.Getenv("PHONE_APP_SCOPES") == "1" {
		if g, err := setupScopes(); err == nil {
			c.cg = g
		} else {
			c.cgOff = "no app scopes: " + err.Error()
		}
	} else if g, err := setupCgroups(); err == nil {
		c.cg = g
	} else {
		c.cgOff = err.Error()
	}
	raw, _ := os.ReadFile("/proc/meminfo")
	_, _, estimated := parseMeminfo(string(raw))
	release := kernelRelease()
	c.kernel = assessKernel(kernelFacts{release: release, cgroups: cgMode(cgRead("/proc/self/cgroup")),
		sandbox: sandbox.ProbeKernel(), groups: c.cg, cgOff: c.cgOff, root: c.Sandboxed(),
		psi: cgHas("/proc/pressure", "memory"), memAvailable: !estimated})
	c.kernel.Foreign = foreignKernel(modulesDir, release)
	c.logKernel()
}

// modulesDir holds the modules of the kernels the system ships, one
// directory per release.
const modulesDir = "/lib/modules"

// foreignKernel says why the running kernel is not one this system ships,
// "" when it is: the image installs its kernel's under modulesDir, and a
// kernel without its directory there is another system's (the system this
// root is mounted in runs its own).
func foreignKernel(modules, release string) string {
	dir := filepath.Join(modules, release)
	if _, err := os.Stat(dir); release == "" || err == nil {
		return ""
	}
	return "not a kernel this system ships (no " + dir + ")"
}

func assessKernel(f kernelFacts) kernelReport {
	sb := f.sandbox
	k := kernelReport{Release: f.release, Cgroups: f.cgroups, Sandbox: sb, Missing: map[string]string{}, Fallback: map[string]string{}}
	miss := func(name, why string, a ...any) { k.Missing[name] = fmt.Sprintf(why, a...) }
	fall := func(name, why string, a ...any) { k.Fallback[name] = fmt.Sprintf(why, a...) }
	var refuse []string
	if !sb.SeccompFilter {
		miss("seccomp", "no seccomp filters (CONFIG_SECCOMP_FILTER)")
		refuse = append(refuse, "seccomp filters")
	}
	if !sb.NoNewPrivs {
		miss("no-new-privs", "no PR_SET_NO_NEW_PRIVS (Linux 3.5)")
		refuse = append(refuse, "no_new_privs")
	}
	if len(sb.NoNamespaces) > 0 {
		miss("namespaces", "no %s namespace", strings.Join(sb.NoNamespaces, ", "))
		refuse = append(refuse, strings.Join(sb.NoNamespaces, ", ")+" namespaces")
	}
	if !f.root {
		miss("uid", "the Shell is not root: apps run as its user, without namespaces (a developer machine)")
	}
	switch {
	case sb.Landlock == 0 && sb.LandlockOff:
		miss("landlock", "Landlock is built but not enabled at boot (lsm=): files are guarded by the apps' uids alone")
	case sb.Landlock == 0:
		miss("landlock", "no Landlock (Linux 5.13): files are guarded by the apps' uids alone")
	case sb.Landlock < 4:
		miss("landlock-tcp", "Landlock ABI %d: an app with the network may listen on TCP ports (ABI 4, Linux 6.7)", sb.Landlock)
	}
	if sb.Landlock > 0 && sb.Landlock < 6 {
		miss("landlock-scope", "Landlock ABI %d: an app with the network may reach abstract sockets outside its sandbox, and signals are limited by uid alone (ABI 6, Linux 6.12)", sb.Landlock)
	}
	if !sb.MemfdSeal {
		miss("memfd-seal", "no sealed memfd (Linux 3.17): sound, video, the camera and web pages do not work (nothing is shared unsealed)")
	}
	if sb.SeccompFilter && !sb.SeccompTSync {
		fall("seccomp-tsync", "no seccomp(2) (Linux 3.17): the launcher installs its filter with prctl on the thread that runs the app; the USB sharing helper cannot filter its own threads and does not start")
	}
	if sb.SeccompFilter && !sb.KillProcess {
		fall("seccomp-kill-process", "no SECCOMP_RET_KILL_PROCESS (Linux 4.14): a call of another architecture ends its thread, not the whole app")
	}
	if !sb.Pidfd {
		fall("pidfd", "no pidfd (Linux 5.3): processes are signalled by pid")
	}
	if !f.memAvailable {
		fall("mem-available", "no MemAvailable (Linux 3.14): available memory is estimated from MemFree, Buffers and Cached")
	}
	psi := f.psi
	if g := f.groups; g == nil {
		miss("cgroups", "%s: no per-app memory, process or CPU limit; background apps stop with SIGSTOP", f.cgOff)
	} else {
		feat := g.feat
		k.Groups, psi = &feat, feat.PSI
		if !feat.Freezer {
			miss("cgroup-freezer", "no cgroup.freeze (Linux 5.2): background apps stop with SIGSTOP to their process group")
		}
		if !feat.Memory {
			miss("cgroup-memory", "no memory controller for the apps' groups: no per-app memory limit")
		}
		if !feat.Pids {
			miss("cgroup-pids", "no pids controller for the apps' groups: no per-app process limit (the per-uid one of 256 stays)")
		}
		if !feat.CPU {
			miss("cgroup-cpu", "no cpu controller for the apps' groups: the app on the screen gets no larger share")
		}
		if !g.scopes && !feat.CloneInto {
			fall("clone-into-cgroup", "no CLONE_INTO_CGROUP (Linux 5.7): a new process waits in the launcher until the Shell has moved it into its group")
		}
		if !feat.Kill {
			fall("cgroup-kill", "no cgroup.kill (Linux 5.14): the Shell kills a group's processes one by one")
		}
		if feat.PSI && !feat.PSITrigger {
			fall("psi-trigger", "no PSI triggers (Linux 5.2): an app stalled at its memory limit is noticed by the housekeeping, every 5 s with the screen off")
		}
	}
	if !psi {
		fall("psi", "no memory pressure (PSI, Linux 4.20): low memory is judged by available memory alone, and an app stalled at its memory limit waits for the kernel's OOM killer")
	}
	if f.root && len(refuse) > 0 {
		k.Refuse = "the kernel has no " + strings.Join(refuse, ", ")
	}
	return k
}

func (c *Core) logKernel() {
	k := c.kernel
	c.log.Printf("kernel: Linux %s, cgroups %s, Landlock ABI %d; %d protections missing, %d done another way",
		k.Release, k.Cgroups, k.Sandbox.Landlock, len(k.Missing), len(k.Fallback))
	for _, name := range slices.Sorted(maps.Keys(k.Missing)) {
		c.log.Printf("kernel: missing %s: %s", name, k.Missing[name])
	}
	for _, name := range slices.Sorted(maps.Keys(k.Fallback)) {
		c.log.Printf("kernel: fallback %s: %s", name, k.Fallback[name])
	}
	if k.Refuse != "" {
		c.log.Printf("kernel: no app will start: %s", k.Refuse)
	}
	if k.Foreign != "" {
		c.log.Printf("kernel: Linux %s is %s: what it lacks is that system's", k.Release, k.Foreign)
	}
	if c.cg == nil {
		return
	}
	var has []string
	f := c.cg.feat
	if f.Memory {
		has = append(has, fmt.Sprintf("memory %d MB", appMemoryMax>>20))
	}
	if f.Pids {
		has = append(has, fmt.Sprintf("%d processes", appPidsMax))
	}
	if f.CPU {
		has = append(has, "CPU weight")
	}
	if f.Freezer {
		has = append(has, "freezer")
	}
	where := "group per app under " + c.cg.base
	if c.cg.scopes {
		where = "systemd scope per app in " + appsSlice
	}
	c.log.Printf("cgroups: one %s (%s)", where, strings.Join(has, ", "))
}

// kernelRefusal says why no app's process may start, if none may: the
// device (root) lacks a layer the launcher cannot do without.
func (c *Core) kernelRefusal() error {
	if c.Sandboxed() && c.kernel.Refuse != "" {
		return fmt.Errorf("%s (24-sku-framework.md §7.5)", c.kernel.Refuse)
	}
	return nil
}

// compat is the report's account of the sandbox on this device.
func (k kernelReport) compat() *cts.Sandbox {
	return &cts.Sandbox{Kernel: k.Release, Missing: maps.Clone(k.Missing), Fallback: maps.Clone(k.Fallback), Foreign: k.Foreign}
}

func kernelRelease() string {
	var u syscall.Utsname
	if syscall.Uname(&u) != nil {
		return ""
	}
	b := make([]byte, 0, len(u.Release))
	for _, ch := range u.Release {
		if ch == 0 {
			break
		}
		b = append(b, byte(ch))
	}
	return string(b)
}
