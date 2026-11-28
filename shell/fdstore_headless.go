package shell

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/godbus/dbus/v5"
)

// The fdstore step asks systemd what its fd store holds for the Shell's
// service (DumpFileDescriptorStore, systemd 254), not the Shell: a file
// the store refused (full) or let go (the other end hung up) is not there
// whatever the Shell believes.

// fdStoreEntry is one entry of DumpFileDescriptorStore's a(suuutuusu).
type fdStoreEntry struct {
	Name           string
	Mode           uint32
	Major, Minor   uint32
	Inode          uint64
	RMajor, RMinor uint32
	Path           string
	Flags          uint32
}

// fdStoreNames are the names of the files in the fd store of the service
// this process runs in.
func fdStoreNames(bus *dbus.Conn) ([]string, error) {
	var unit dbus.ObjectPath
	if err := bus.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1").Call(
		"org.freedesktop.systemd1.Manager.GetUnitByPID", 0, uint32(os.Getpid())).Store(&unit); err != nil {
		return nil, err
	}
	var entries []fdStoreEntry
	if err := bus.Object("org.freedesktop.systemd1", unit).Call(
		"org.freedesktop.systemd1.Service.DumpFileDescriptorStore", 0).Store(&entries); err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	slices.Sort(names)
	return names, nil
}

// fdStoreHas: names hold an app's connection (kind "app") or the copy of
// its own connection (kind "keep") for its process pid, or for any of its
// processes with pid 0.
func fdStoreHas(names []string, kind, id string, pid int) bool {
	for _, n := range names {
		var nid string
		var npid int
		var ok bool
		if kind == "keep" {
			nid, npid, ok = parseKeepName(n)
		} else {
			nid, npid, _, ok = parseConnName(n)
		}
		if ok && nid == id && (pid == 0 || npid == pid) {
			return true
		}
	}
	return false
}

func (r *Runner) fdStoreStep(arg string) error {
	rest, ms := lastNum(arg, 5000)
	f := strings.Fields(rest)
	if len(f) != 3 || f[1] != "app" && f[1] != "keep" || f[2] != "yes" && f[2] != "no" {
		return errors.New("usage: fdstore <app-id> app|keep yes|no [ms]")
	}
	if !r.C.fdstore {
		return errors.New("fdstore: the Shell keeps nothing in an fd store (require-fdstore)")
	}
	bus, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("fdstore: %w", err)
	}
	defer bus.Close()
	id, kind, want := f[0], f[1], f[2] == "yes"
	var names []string
	var askErr error
	if _, ok := r.waitFor(func(string) bool {
		pid := 0
		if want {
			if pid = r.C.AppPid(id); pid == 0 {
				return false
			}
		}
		names, askErr = fdStoreNames(bus)
		return askErr == nil && fdStoreHas(names, kind, id, pid) == want
	}, ms); !ok {
		if askErr != nil {
			return fmt.Errorf("fdstore: %w", askErr)
		}
		where := "in"
		if !want {
			where = "out of"
		}
		return fmt.Errorf("fdstore: want %s's %s connection %s the store, which holds [%s] (%d places)",
			id, kind, where, strings.Join(names, " "), r.C.fdStoreSize())
	}
	fmt.Fprintf(r.Out, "PHONE-FDSTORE-HOLDS: %d places: %s\n", r.C.fdStoreSize(), strings.Join(names, " "))
	return nil
}
