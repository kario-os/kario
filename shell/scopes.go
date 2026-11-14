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
	if _, ok := strings.CutPrefix(cgRead("/proc/self/cgroup"), "0::"); !ok {
		return nil, errors.New("not on the unified (v2) hierarchy")
	}
	bus, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	return &cgroups{scopes: true, bus: bus, dirs: map[string]string{}}, nil
}

func pidGroup(pid int) string {
	rel, _ := strings.CutPrefix(cgRead("/proc/"+strconv.Itoa(pid)+"/cgroup"), "0::")
	return rel
}

// attach moves a just started app into a new scope with the app limits and
// waits until it is there.
func (g *cgroups) attach(id string, pid int) error { return g.attachLimits(id, pid, appLimits) }

func (g *cgroups) attachLimits(id string, pid int, l limits) error {
	name := fmt.Sprintf("app-%s-%d.scope", id, pid)
	props := []dbusProp{
		{"Description", dbus.MakeVariant("phone app " + id)},
		{"PIDs", dbus.MakeVariant([]uint32{uint32(pid)})},
		{"Slice", dbus.MakeVariant(appsSlice)},
		{"MemoryMax", dbus.MakeVariant(uint64(l.mem))},
		{"MemorySwapMax", dbus.MakeVariant(uint64(0))},
		{"TasksMax", dbus.MakeVariant(uint64(l.pids))},
		{"CPUWeight", dbus.MakeVariant(uint64(cpuFG))},
		{"CollectMode", dbus.MakeVariant("inactive-or-failed")},
	}
	var job dbus.ObjectPath
	call := g.bus.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1").Call(
		"org.freedesktop.systemd1.Manager.StartTransientUnit", 0, name, "fail", props, []dbusAux{})
	if err := call.Store(&job); err != nil {
		return err
	}
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if rel := pidGroup(pid); strings.HasSuffix(rel, "/"+name) {
			g.dirs[id] = filepath.Join("/sys/fs/cgroup", rel)
			return nil
		}
	}
	return fmt.Errorf("%s did not move into %s", id, name)
}

// adopt finds the scope of an app a previous shell started.
func (g *cgroups) adopt(id string, pid int) {
	if rel := pidGroup(pid); strings.Contains(rel, "/"+appsSlice+"/") && strings.HasSuffix(rel, ".scope") {
		g.dirs[id] = filepath.Join("/sys/fs/cgroup", rel)
	}
}
