package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// App scopes: with PHONE_APP_SCOPES=1 every app runs in a transient systemd
// scope of its own (phone-apps.slice/app-<id>-<pid>.scope) instead of a
// group under the shell's service. The limits are the same, set as the
// scope's properties; the freezer and the OOM count are the scope group's
// files. With the apps outside the shell's group, systemd can restart a
// crashed shell (its group holds no process and enables no controllers)
// while the apps live on and wait in the fd store.

const appsSlice = "phone-apps.slice"

type dbusProp struct {
	Name  string
	Value dbus.Variant
}

type dbusAux struct {
	Name  string
	Props []dbusProp
}

func setupScopes() (*cgroups, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("not root")
	}
	rel, ok := strings.CutPrefix(cgRead("/proc/self/cgroup"), "0::")
	if !ok {
		return nil, errors.New("not on the unified (v2) hierarchy")
	}
	bus, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	// The scopes' files are the kernel's as the Shell's own group has them;
	// systemd turns on for the slice what the root group offers.
	own, root := filepath.Join("/sys/fs/cgroup", rel), " "+cgRead("/sys/fs/cgroup/cgroup.controllers")+" "
	feat := cgFeatures{Freezer: cgHas(own, "cgroup.freeze"), Kill: cgHas(own, "cgroup.kill"), PSI: cgHas(own, "memory.pressure"),
		Memory: strings.Contains(root, " memory "), MemoryHigh: strings.Contains(root, " memory "),
		Pids: strings.Contains(root, " pids "), CPU: strings.Contains(root, " cpu ")}
	feat.PSITrigger = feat.PSI && psiTriggers(own)
	return &cgroups{scopes: true, bus: bus, dirs: map[string]string{}, feat: feat}, nil
}

func pidGroup(pid int) string {
	rel, _ := strings.CutPrefix(cgRead("/proc/"+strconv.Itoa(pid)+"/cgroup"), "0::")
	return rel
}

// attach moves a just started app into a new scope with the app limits and
// waits until it is there; without scopes, into its group (held).
func (g *cgroups) attach(id string, pid int) error { return g.attachLimits(id, pid, appLimits) }

func (g *cgroups) attachLimits(id string, pid int, l limits) error {
	if !g.scopes {
		return g.moveIn(id, pid, l)
	}
	dir, err := g.scope(fmt.Sprintf("app-%s-%d.scope", id, pid), pid,
		dbusProp{"Description", dbus.MakeVariant("phone app " + id)},
		dbusProp{"Slice", dbus.MakeVariant(appsSlice)},
		dbusProp{"MemoryMax", dbus.MakeVariant(uint64(l.mem))},
		dbusProp{"MemorySwapMax", dbus.MakeVariant(uint64(0))},
		dbusProp{"TasksMax", dbus.MakeVariant(uint64(l.pids))},
		dbusProp{"CPUWeight", dbus.MakeVariant(uint64(cpuFG))})
	if err != nil {
		return err
	}
	g.dirs[id] = dir
	return nil
}

// scope moves a process into a new transient scope with those properties
// and waits until it is there; it returns the scope's group.
func (g *cgroups) scope(name string, pid int, props ...dbusProp) (string, error) {
	props = append(props, dbusProp{"PIDs", dbus.MakeVariant([]uint32{uint32(pid)})},
		dbusProp{"CollectMode", dbus.MakeVariant("inactive-or-failed")})
	var job dbus.ObjectPath
	call := g.bus.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1").Call(
		"org.freedesktop.systemd1.Manager.StartTransientUnit", 0, name, "fail", props, []dbusAux{})
	if err := call.Store(&job); err != nil {
		return "", err
	}
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if rel := pidGroup(pid); strings.HasSuffix(rel, "/"+name) {
			return filepath.Join("/sys/fs/cgroup", rel), nil
		}
	}
	return "", fmt.Errorf("pid %d did not move into %s", pid, name)
}

// adopt finds the scope of an app a previous shell started.
func (g *cgroups) adopt(id string, pid int) {
	if rel := pidGroup(pid); strings.Contains(rel, "/"+appsSlice+"/") && strings.HasSuffix(rel, ".scope") {
		g.dirs[id] = filepath.Join("/sys/fs/cgroup", rel)
	}
}
