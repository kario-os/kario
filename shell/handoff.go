package shell

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// A Shell that restarts keeps the apps: their connections go to the next
// Shell, which takes them over and asks each app to send its pages again
// (ui.MsgResync). Two ways carry the connections:
//
//   - systemd's file descriptor store (FileDescriptorStoreMax=): with
//     PHONE_FDSTORE=1 the Shell stores every app connection as it starts the
//     app, so a crashed Shell that systemd restarts finds them in LISTEN_FDS;
//   - an in-place restart (an update, the selftest): the Shell execs itself
//     with the connections left open, listed in PHONE_HANDOFF.
//
// A connection is named app-<app id>-<pid>, and app-<app id>-<pid>.v<major>
// for an app that speaks an older major of the protocol (protover.go); the
// Shell's copy of an app's own connection (keepalive.go) keep-<app id>-<pid>.
//
// The fd store has FileDescriptorStoreMax places, which systemd passes in
// $FDSTORE; past them it refuses a file without a word to the Shell. An
// app's connection gets a place while there is one. The apps' own
// connections take at most 1/keepStoreShare of the store, and only the
// places the apps' connections leave (keepaliveStore): an app started into
// a full store takes the place of the own connection gone longest unused,
// never another app's.

const handoffEnv = "PHONE_HANDOFF"

const (
	// fdStoreFallback: the store's size where systemd does not say (before
	// systemd 254).
	fdStoreFallback = 64
	keepStoreShare  = 4
)

// fdStore is what this Shell put into systemd's fd store: the apps whose
// connection is there. full: said that their connections alone fill it.
type fdStore struct {
	size int
	apps map[string]bool
	full bool
}

// Inherited is an app connection a previous Shell left; Major is the older
// major the app speaks, -1 for the Shell's own. Keep: the app's own
// connection, not its connection to the Shell. Stored: it came from the fd
// store (LISTEN_FDS), so it is in it.
type Inherited struct {
	ID     string
	Pid    int
	File   *os.File
	Major  int
	Keep   bool
	Stored bool
}

func connName(id string, pid int) string { return "app-" + id + "-" + strconv.Itoa(pid) }

func keepName(id string, pid int) string { return "keep-" + id + "-" + strconv.Itoa(pid) }

func parseKeepName(name string) (string, int, bool) {
	rest, ok := strings.CutPrefix(name, "keep-")
	i := strings.LastIndexByte(rest, '-')
	if !ok || i <= 0 {
		return "", 0, false
	}
	pid, err := strconv.Atoi(rest[i+1:])
	return rest[:i], pid, err == nil && pid > 0
}

// appConnName is a's connection's name, with the major it speaks.
func appConnName(a *App, pid int) string {
	if a.proto != nil {
		return connName(a.M.ID, pid) + ".v" + strconv.Itoa(a.proto.major)
	}
	return connName(a.M.ID, pid)
}

func parseConnName(name string) (string, int, int, bool) {
	rest, ok := strings.CutPrefix(name, "app-")
	i := strings.LastIndexByte(rest, '-')
	if !ok || i <= 0 {
		return "", 0, 0, false
	}
	pidText, majorText, versioned := strings.Cut(rest[i+1:], ".v")
	major := -1
	if versioned {
		n, err := strconv.Atoi(majorText)
		if err != nil || n < 0 {
			return "", 0, 0, false
		}
		major = n
	}
	pid, err := strconv.Atoi(pidText)
	return rest[:i], pid, major, err == nil && pid > 0
}

// inherited is what a connection passed on is, by its name.
func inherited(f *os.File, stored bool) (Inherited, bool) {
	if id, pid, major, ok := parseConnName(f.Name()); ok {
		return Inherited{ID: id, Pid: pid, File: f, Major: major, Stored: stored}, true
	}
	if id, pid, ok := parseKeepName(f.Name()); ok {
		return Inherited{ID: id, Pid: pid, File: f, Major: -1, Keep: true, Stored: stored}, true
	}
	return Inherited{}, false
}

// InheritedApps collects the connections passed to this process, from
// systemd (LISTEN_FDS) and from an in-place restart, and clears the
// variables so the apps the Shell starts do not see them.
func InheritedApps() []Inherited {
	var out []Inherited
	add := func(fd int, name string, stored bool) {
		syscall.CloseOnExec(fd)
		f := os.NewFile(uintptr(fd), name)
		if in, ok := inherited(f, stored); ok {
			out = append(out, in)
		} else {
			f.Close()
		}
	}
	if os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid()) {
		n, _ := strconv.Atoi(os.Getenv("LISTEN_FDS"))
		names := strings.Split(os.Getenv("LISTEN_FDNAMES"), ":")
		for i := 0; i < n; i++ {
			name := ""
			if i < len(names) {
				name = names[i]
			}
			add(3+i, name, true)
		}
	}
	for _, it := range strings.Split(os.Getenv(handoffEnv), ",") {
		fdText, name, ok := strings.Cut(it, "=")
		if fd, err := strconv.Atoi(fdText); ok && err == nil && fd > 2 {
			add(fd, name, false)
		}
	}
	for _, v := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES", handoffEnv} {
		os.Unsetenv(v)
	}
	return out
}

// processAlive is false for a zombie too: an app that died while no Shell
// waited for it.
func processAlive(pid int) bool {
	if raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		s := string(raw)
		if i := strings.LastIndexByte(s, ')'); i > 0 && i+2 < len(s) {
			return s[i+2] != 'Z'
		}
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// waitGone stands in for Wait on an app this Shell did not start: its
// connection has closed, so it is exiting.
func waitGone(p *os.Process) error {
	for i := 0; i < 50 && processAlive(p.Pid); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if processAlive(p.Pid) {
		_ = p.Kill()
	}
	return nil
}

// Adopt takes over connections left by a previous Shell. The apps keep
// running; frozen ones are thawed to answer, and all count as background.
func (c *Core) Adopt(list []Inherited) {
	for _, in := range list {
		if in.Keep {
			continue
		}
		a := c.appByID(in.ID)
		if a == nil || a.running || a.system || !processAlive(in.Pid) {
			c.dropConn(in)
			continue
		}
		a.proto = nil
		if in.Major >= 0 && in.Major != c.versions()[0].major {
			v, ok := c.version(in.Major)
			if !ok {
				c.log.Printf("%s pid=%d speaks phone.ui/%d, which this Shell does not: its connection is closed", in.ID, in.Pid, in.Major)
				c.dropConn(in)
				continue
			}
			a.proto = &v
		}
		if c.fdstore {
			if c.fds.apps == nil {
				c.fds.apps = map[string]bool{}
			}
			c.fds.apps[a.M.ID] = true
		}
		proc, _ := os.FindProcess(in.Pid)
		a.proc, a.adopted, a.conn, a.running, a.ready, a.started = proc, true, wire.New(in.File), true, true, time.Now()
		if c.cg != nil && c.cg.scopes {
			c.cg.adopt(a.M.ID, in.Pid)
		}
		a.frozen = true
		c.thaw(a)
		a.bgSince = time.Now()
		c.serveApp(a, func() error { return waitGone(proc) }, false)
		a.send(ui.Msg{T: ui.MsgResync})
		c.log.Printf("adopted %s pid=%d", a.M.ID, in.Pid)
	}
	c.keepaliveAdopt(list)
}

// dropConn closes an app connection passed on that this Shell does not
// take over, the copy in the fd store too: it would keep a place there and
// the app waiting for a Shell that never reads it.
func (c *Core) dropConn(in Inherited) {
	in.File.Close()
	if c.fdstore {
		_ = sdNotify("FDSTOREREMOVE=1\nFDNAME=" + in.File.Name())
	}
}

// Adopted reports whether an app was taken over from a previous Shell.
func (c *Core) Adopted(id string) bool {
	a := c.appByID(id)
	return a != nil && a.running && a.adopted
}

// AppPid is a running app's process id (0 if it is not running).
func (c *Core) AppPid(id string) int {
	if a := c.appByID(id); a != nil && a.running && a.proc != nil && processAlive(a.proc.Pid) {
		return a.proc.Pid
	}
	return 0
}

// ExecHandoff restarts the Shell in place: the same process execs exe with
// the app connections left open and listed in PHONE_HANDOFF.
func (c *Core) ExecHandoff(exe string, args, extraEnv []string) error {
	spec, keep, err := c.handoff()
	if err != nil {
		closeFiles(keep)
		return err
	}
	env := []string{handoffEnv + "=" + strings.Join(spec, ",")}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, handoffEnv+"=") {
			env = append(env, kv)
		}
	}
	err = execOnProcThread(exe, args, append(env, extraEnv...))
	closeFiles(keep)
	return err
}

// handoff opens what an in-place restart leaves the next Shell, each
// descriptor without close-on-exec, and lists them for PHONE_HANDOFF; the
// files are the caller's to close once the exec is done (or failed).
func (c *Core) handoff() ([]string, []*os.File, error) {
	if len(c.keep.conns) > 0 {
		c.keepaliveSave()
	}
	var spec []string
	var keep []*os.File
	pass := func(f *os.File, name string) error {
		// Kept referenced: a collected *os.File closes its descriptor.
		keep = append(keep, f)
		fd := int(f.Fd())
		if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, 0); e != 0 {
			return e
		}
		spec = append(spec, fmt.Sprintf("%d=%s", fd, name))
		return nil
	}
	for _, a := range c.apps {
		if !a.running || a.conn == nil || a.proc == nil {
			continue
		}
		f, err := a.conn.File()
		if err != nil {
			return nil, keep, err
		}
		if err := pass(f, appConnName(a, a.proc.Pid)); err != nil {
			return nil, keep, err
		}
	}
	for id, k := range c.keep.conns {
		a := c.appByID(id)
		if a == nil || !a.running || a.conn == nil || a.proc == nil || a.proc.Pid != k.pid {
			continue
		}
		fd, err := keepaliveDup(k.conn)
		if err != nil {
			return nil, keep, err
		}
		keep = append(keep, os.NewFile(uintptr(fd), keepName(id, k.pid)))
		spec = append(spec, fmt.Sprintf("%d=%s", fd, keepName(id, k.pid)))
	}
	return spec, keep, nil
}

// ExecFresh starts the Shell over in place without the apps (a restart of
// the phone on a developer machine): nothing is handed over.
func ExecFresh(exe string, args, extraEnv []string) error {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, handoffEnv+"=") {
			env = append(env, kv)
		}
	}
	return execOnProcThread(exe, args, append(env, extraEnv...))
}

// sdNotify sends a state change to systemd, with files attached for the
// fd store.
func sdNotify(state string, files ...*os.File) error {
	fds := make([]int, len(files))
	for i, f := range files {
		fds[i] = int(f.Fd())
	}
	return sdNotifyFDs(state, fds...)
}

func sdNotifyFDs(state string, fds ...int) error {
	sock := os.Getenv("NOTIFY_SOCKET")
	if sock == "" {
		return errors.New("NOTIFY_SOCKET not set")
	}
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var oob []byte
	if len(fds) > 0 {
		oob = syscall.UnixRights(fds...)
	}
	// "@" is the abstract namespace.
	return syscall.Sendmsg(fd, []byte(state), oob, &syscall.SockaddrUnix{Name: sock}, 0)
}

// fdStoreSize is how many files the fd store keeps.
func (c *Core) fdStoreSize() int {
	if c.fds.size == 0 {
		c.fds.size = fdStoreFallback
		if n, err := strconv.Atoi(os.Getenv("FDSTORE")); err == nil && n > 0 {
			c.fds.size = n
		}
	}
	return c.fds.size
}

// storeConn puts an app's connection into systemd's fd store, in the place
// of an own connection when there is no other (keepaliveStore).
func (c *Core) storeConn(a *App, pid int, f *os.File) {
	if size := c.fdStoreSize(); len(c.fds.apps) >= size {
		if !c.fds.full {
			c.fds.full = true
			c.log.Printf("fd store: its %d places all hold apps' connections: %s, and any app started before one ends, would not outlive a crash of the Shell", size, a.M.ID)
		}
		return
	}
	if c.fds.apps == nil {
		c.fds.apps = map[string]bool{}
	}
	c.fds.apps[a.M.ID] = true
	c.keepaliveStore()
	c.storeConnNamed(connName(a.M.ID, pid), f)
}

func (c *Core) storeConnNamed(name string, f *os.File) {
	if err := sdNotify("FDSTORE=1\nFDNAME="+name, f); err != nil {
		c.log.Printf("fd store: %v", err)
	}
}

func (c *Core) unstoreConn(a *App) {
	if !c.fdstore || a.proc == nil {
		return
	}
	_ = sdNotify("FDSTOREREMOVE=1\nFDNAME=" + appConnName(a, a.proc.Pid))
	if c.fds.apps[a.M.ID] {
		delete(c.fds.apps, a.M.ID)
		c.fds.full = false
		c.keepaliveStore()
	}
}

// restoreConn names an app's stored connection again with the older major
// it said it speaks, so that a Shell systemd restarts speaks it too. The
// store keeps one copy of a file, so the old name goes first.
func (c *Core) restoreConn(a *App) {
	if !c.fdstore || a.proc == nil || a.conn == nil || !c.fds.apps[a.M.ID] {
		return
	}
	f, err := a.conn.File()
	if err != nil {
		c.log.Printf("fd store: %v", err)
		return
	}
	defer f.Close()
	_ = sdNotify("FDSTOREREMOVE=1\nFDNAME=" + connName(a.M.ID, a.proc.Pid))
	c.storeConnNamed(appConnName(a, a.proc.Pid), f)
}
