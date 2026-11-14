package shell

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/kario-os/kario/sandbox"
	"github.com/kario-os/kario/ui"
)

// Apps start through phone-sandbox (see package sandbox for the layers).
// When the shell runs as root (the device) every app also gets its own uid
// (the Android model) and fresh mount, pid, ipc and uts namespaces, plus an
// empty network namespace unless it holds the "net" permission and the user
// has not switched its network off (Settings › App permissions › Network).
// Without root (a developer machine) only the unprivileged layers apply:
// rlimits, seccomp and, if the kernel has it, Landlock.

const firstAppUID = 10000

func (c *Core) Sandboxed() bool { return os.Geteuid() == 0 }

func (c *Core) appCommand(a *App, dataDir string) (*exec.Cmd, error) {
	if cmd := c.devCommand(a); cmd != nil {
		return cmd, nil
	}
	exe, args := c.resolveExec(a.M), c.execArgs(a.M)
	launcher := filepath.Join(c.cfg.AppBinDir, "phone-sandbox")
	if _, err := os.Stat(launcher); err != nil {
		if c.Sandboxed() {
			return nil, fmt.Errorf("refusing to start %s unconfined: %w", a.M.ID, err)
		}
		c.log.Printf("%s: no phone-sandbox, starting unconfined", a.M.ID)
		return exec.Command(exe, args...), nil
	}
	p := sandbox.Policy{
		DataDir:   dataDir,
		Code:      []string{"/usr", "/lib", "/lib64", "/bin", "/sbin", filepath.Dir(exe), a.M.dir},
		ReadOnly:  []string{"/etc", "/proc"},
		ReadFiles: []string{c.trustRoot()},
		Net:       c.netAllowed(a),
		Survive:   c.fdstore,
	}
	attr := &syscall.SysProcAttr{}
	if c.Sandboxed() {
		uid, err := c.appUID(a.M.ID)
		if err != nil {
			return nil, err
		}
		if err := c.ownDataDir(dataDir, uid); err != nil {
			return nil, err
		}
		if err := c.applyNetPolicy(); err != nil {
			c.log.Printf("traffic: %v", err)
		}
		p.UID, p.Namespaces = uid, true
		attr.Cloneflags = syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS
		if !p.Net {
			attr.Cloneflags |= syscall.CLONE_NEWNET
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(launcher, append([]string{string(raw), exe}, args...)...)
	cmd.SysProcAttr = attr
	return cmd, nil
}

// netAllowed: the app declares "net" and the user has not denied it. The
// permission is granted at install; denying it is the per-app firewall,
// "wifi" keeps the network but nftables drops what goes out elsewhere.
func (c *Core) netAllowed(a *App) bool {
	return a.M.Has(ui.PermNet) && c.netState(a.M.ID) != "denied"
}

func (c *Core) trustRoot() string { return filepath.Join(c.cfg.StateDir, "factory", "root.pub") }

func (c *Core) ownDataDir(dataDir string, uid uint32) error {
	// Ancestors must be traversable (not listable) for the app to reach its
	// own directory.
	for _, d := range []string{c.cfg.StateDir, filepath.Join(c.cfg.StateDir, "apps")} {
		if err := os.Chmod(d, 0o711); err != nil {
			return err
		}
	}
	if err := os.Chown(dataDir, int(uid), int(uid)); err != nil {
		return err
	}
	return os.Chmod(dataDir, 0o700)
}

// appUID returns a stable uid per app id, allocated on first launch.
func (c *Core) appUID(id string) (uint32, error) {
	path := filepath.Join(c.cfg.StateDir, "shell", "uids.json")
	uids := map[string]uint32{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &uids); err != nil {
			return 0, err
		}
	}
	if uid, ok := uids[id]; ok {
		return uid, nil
	}
	used := make([]int, 0, len(uids))
	for _, u := range uids {
		used = append(used, int(u))
	}
	sort.Ints(used)
	next := uint32(firstAppUID)
	if len(used) > 0 {
		next = uint32(used[len(used)-1] + 1)
	}
	uids[id] = next
	raw, _ := json.MarshalIndent(uids, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, err
	}
	return next, os.WriteFile(path, raw, 0o600)
}
