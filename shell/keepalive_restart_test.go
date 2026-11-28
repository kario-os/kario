package shell

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// restartRig is an app with a connection of its own on a Shell about to
// restart: its process (a sleep), the app's end of its Shell connection,
// and what an exec of the Shell leaves the next one.
type restartRig struct {
	*keepTest
	uiConn *os.File
	rd     *bufio.Reader
}

func newRestartRig(t *testing.T) *restartRig {
	t.Helper()
	k := newKeepTest(t)
	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	end := os.NewFile(uintptr(fds[0]), "app")
	t.Cleanup(func() { end.Close() })
	k.a.conn, k.a.proc = wire.New(os.NewFile(uintptr(fds[1]), "shell")), sleep.Process
	return &restartRig{keepTest: k, uiConn: end, rd: bufio.NewReader(end)}
}

// handOver is the in-place restart up to the next Shell's start: what
// PHONE_HANDOFF lists stays open across the exec, the Shell's own copies
// of the sockets (close-on-exec) go, and the next Shell collects the
// connections.
func (r *restartRig) handOver() []Inherited {
	r.t.Helper()
	spec, files, err := r.c.handoff()
	if err != nil {
		r.t.Fatal(err)
	}
	var passed []string
	for _, it := range spec {
		fdText, name, _ := strings.Cut(it, "=")
		fd, _ := strconv.Atoi(fdText)
		dup, err := unix.Dup(fd)
		if err != nil {
			r.t.Fatal(err)
		}
		passed = append(passed, fmt.Sprintf("%d=%s", dup, name))
	}
	closeFiles(files)
	for _, k := range r.c.keep.conns {
		k.conn.Close()
	}
	r.a.conn.Close()
	r.t.Setenv(handoffEnv, strings.Join(passed, ","))
	return InheritedApps()
}

// next is the Shell after the restart, on the same state and fd store, its
// clock where the previous one's was; its loop runs on a goroutine of its
// own.
func (r *restartRig) next(list []Inherited) *Core {
	r.t.Helper()
	return nextShell(r.t, r.c, []*App{r.a}, list)
}

// nextShell is the Shell after prev restarted, with apps installed, on
// prev's state and fd store and its clock; its loop runs on a goroutine of
// its own.
func nextShell(t *testing.T, prev *Core, apps []*App, list []Inherited) *Core {
	t.Helper()
	root := t.TempDir()
	for _, a := range apps {
		dir := filepath.Join(root, "apps", a.M.ID)
		_ = os.MkdirAll(dir, 0o755)
		m, _ := json.Marshal(map[string]any{"id": a.M.ID, "name": a.M.Name, "exec": "none", "permissions": a.M.Permissions})
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), m, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run, err := os.MkdirTemp("", "keep-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	c, err := New(Config{W: 40, H: 24, AppsDir: filepath.Join(root, "apps"), AppBinDir: filepath.Join(root, "bin"), RunDir: run,
		StateDir: prev.cfg.StateDir, FreezeAfter: -1, FDStore: prev.fdstore, Log: prev.log.Writer()})
	if err != nil {
		t.Fatal(err)
	}
	runLoop(t, c)
	_ = c.onLoop(func() error {
		c.SetClockSkew(prev.clockSkew)
		c.Adopt(list)
		return nil
	})
	return c
}

// storeOn turns the fd store on, with the app's connection to the Shell in
// it, as spawn puts it there.
func (r *restartRig) storeOn() {
	r.c.fdstore = true
	r.c.fds.apps = map[string]bool{r.a.M.ID: true}
}

// appEvents reads the keepalive events the Shell sends the app until
// want came, or for wait.
func (r *restartRig) appEvents(want int, wait time.Duration) []string {
	r.t.Helper()
	var out []string
	_ = r.uiConn.SetReadDeadline(time.Now().Add(wait))
	for len(out) < want {
		line, err := r.rd.ReadBytes('\n')
		if err != nil {
			break
		}
		var m ui.Msg
		if json.Unmarshal(line, &m) == nil && m.T == ui.MsgEvent && m.Event == ui.EventKeepalive {
			out = append(out, m.Value)
		}
	}
	return out
}

// beforeRestart connects the app and gives it a wake of its own and a
// heartbeat riding the push heartbeat.
func (r *restartRig) beforeRestart() ui.KeepaliveUse {
	r.t.Helper()
	c := r.c
	r.connect(600)
	r.later(600 * time.Second)
	c.keepaliveTick()
	r.later(400 * time.Second)
	c.pushUp = true
	c.pushWindow(28 * time.Minute)
	if u := r.use(); u.Own != 1 || u.Shared != 1 {
		r.t.Fatalf("before the restart: %+v", u)
	}
	return r.use()
}

// An in-place restart hands the app's own connection over with the app's
// (PHONE_HANDOFF): the next Shell watches the same socket, keeps the
// heartbeat's schedule and the hour's budget, and Settings › Battery
// counts each wake once. The Shell used to keep its copy, the schedule
// and the counts in memory alone: after a restart the app kept its socket
// but was woken neither for data nor for heartbeats, and the counts were
// gone.
func TestKeepaliveSurvivesHandoff(t *testing.T) {
	r := newRestartRig(t)
	id := r.a.M.ID
	before := r.beforeRestart()
	beat := r.c.keep.conns[id].beat
	left := r.c.keep.budget(&r.c.keep.own, id, keepaliveOwnPerHour).Left(r.c.now())
	// The app made its socket blocking; the Shell's copy is the same open
	// file.
	setNonblock(t, r.app, false)
	c := r.next(r.handOver())
	if nonblock(t, r.app) {
		t.Errorf("the next Shell made the app's socket non-blocking")
	}

	var a *App
	_ = c.onLoop(func() error {
		a = c.appByID(id)
		k := c.keep.conns[id]
		switch {
		case !c.Adopted(id):
			t.Errorf("the app was not adopted")
		case k == nil:
			t.Errorf("the next Shell has no copy of the app's connection")
		case k.beat.Every != beat.Every || !k.beat.Last.Equal(beat.Last):
			t.Errorf("heartbeat every %s, last %s; before the restart every %s, last %s", k.beat.Every, k.beat.Last, beat.Every, beat.Last)
		}
		if u := c.KeepaliveUse(id); u == nil || *u != before {
			t.Errorf("Settings › Battery after the restart %+v, before %+v", u, before)
		}
		if n := c.keep.budget(&c.keep.own, id, keepaliveOwnPerHour).Left(c.now()); n != left {
			t.Errorf("%d wakes of its own left this hour, %d before the restart", n, left)
		}
		listed := 0
		for _, e := range c.energyView() {
			if e.App == id && e.Keepalive != nil && e.Keepalive.Own == before.Own {
				listed++
			}
		}
		if listed != 1 {
			t.Errorf("Settings › Battery lists %+v", c.energyView())
		}
		return nil
	})
	if t.Failed() {
		return
	}

	_ = c.onLoop(func() error { a.frozen = true; c.keepaliveFrozen(a); return nil })
	if _, err := r.peer.Write([]byte("after the restart\n")); err != nil {
		t.Fatal(err)
	}
	if ev := r.appEvents(1, 5*time.Second); len(ev) != 1 || ev[0] != ui.KeepaliveData {
		t.Fatalf("data on the connection after the restart: %q", ev)
	}
	_ = c.onLoop(func() error {
		if u := c.KeepaliveUse(id); u.Data != 1 || u.Own != before.Own || u.Shared != before.Shared {
			t.Errorf("after the data wake: %+v", u)
		}
		return nil
	})
	line, err := bufio.NewReader(r.app).ReadString('\n')
	if err != nil || line != "after the restart\n" {
		t.Fatalf("the app read %q, %v", line, err)
	}
}

// Where the socket did not come along (systemd's fd store holds at most
// FileDescriptorStoreMax and lets a socket that hung up go), the next
// Shell tells the app (keepalive lost), once, so that it calls
// net.keepalive again; the counts stay.
func TestKeepaliveLostAcrossRestart(t *testing.T) {
	r := newRestartRig(t)
	id := r.a.M.ID
	before := r.beforeRestart()
	var apps []Inherited
	for _, in := range r.handOver() {
		if strings.HasPrefix(in.File.Name(), "app-") {
			apps = append(apps, in)
		} else {
			in.File.Close()
		}
	}
	c := r.next(apps)
	if ev := r.appEvents(2, time.Second); len(ev) != 1 || ev[0] != ui.KeepaliveLost {
		t.Fatalf("the app's events after the restart: %q", ev)
	}
	_ = c.onLoop(func() error {
		if c.KeepaliveOpen(id) {
			t.Errorf("a connection of its own without the socket")
		}
		if u := c.KeepaliveUse(id); u == nil || *u != before {
			t.Errorf("Settings › Battery after the restart %+v, before %+v", u, before)
		}
		return nil
	})
}

// A socket passed on without the schedule the previous Shell kept for it
// (its state not written) is no use to the next one: the app is told.
func TestKeepaliveLostWithoutItsSchedule(t *testing.T) {
	r := newRestartRig(t)
	r.beforeRestart()
	list := r.handOver()
	if err := os.Remove(r.c.keepalivePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	c := r.next(list)
	if ev := r.appEvents(2, time.Second); len(ev) != 1 || ev[0] != ui.KeepaliveLost {
		t.Fatalf("the app's events after the restart: %q", ev)
	}
	_ = c.onLoop(func() error {
		if c.KeepaliveOpen(r.a.M.ID) {
			t.Errorf("a connection of its own without a schedule")
		}
		return nil
	})
}

// notifyRig is systemd's notify socket, for what goes into the fd store.
type notifyRig struct {
	t  *testing.T
	ln *net.UnixConn
}

func newNotifyRig(t *testing.T) *notifyRig {
	sock := filepath.Join(t.TempDir(), "notify")
	ln, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	t.Setenv("NOTIFY_SOCKET", sock)
	return &notifyRig{t: t, ln: ln}
}

func (n *notifyRig) recv() (string, []*os.File) {
	n.t.Helper()
	buf, oob := make([]byte, 256), make([]byte, 256)
	_ = n.ln.SetReadDeadline(time.Now().Add(3 * time.Second))
	got, oobn, _, _, err := n.ln.ReadMsgUnix(buf, oob)
	if err != nil {
		n.t.Fatalf("nothing for the fd store: %v", err)
	}
	var files []*os.File
	if msgs, err := syscall.ParseSocketControlMessage(oob[:oobn]); err == nil {
		for _, m := range msgs {
			fds, _ := syscall.ParseUnixRights(&m)
			for _, fd := range fds {
				f := os.NewFile(uintptr(fd), "stored")
				n.t.Cleanup(func() { f.Close() })
				files = append(files, f)
			}
		}
	}
	return string(buf[:got]), files
}

// With systemd's fd store the Shell stores the app's own connection
// beside the app's (keep-<app id>-<pid>), for a Shell systemd restarts
// after a crash to find in LISTEN_FDS, and takes it out when it ends:
// a copy left in the store would hold the connection open.
func TestKeepaliveFDStore(t *testing.T) {
	n := newNotifyRig(t)
	r := newRestartRig(t)
	r.storeOn()
	name := keepName(r.a.M.ID, r.a.proc.Pid)
	r.connect(600)
	msg, files := n.recv()
	if msg != "FDSTORE=1\nFDNAME="+name || len(files) != 1 {
		t.Fatalf("stored %q with %d descriptors", msg, len(files))
	}
	var a, b unix.Stat_t
	rc, _ := r.c.keep.conns[r.a.M.ID].conn.SyscallConn()
	_ = rc.Control(func(fd uintptr) { _ = unix.Fstat(int(fd), &a) })
	if err := unix.Fstat(int(files[0].Fd()), &b); err != nil || a.Ino != b.Ino {
		t.Fatalf("the stored descriptor is socket %d, the connection %d (%v)", b.Ino, a.Ino, err)
	}
	r.c.keepaliveEnd(r.a, "the app ended")
	if msg, _ := n.recv(); msg != "FDSTOREREMOVE=1\nFDNAME="+name {
		t.Fatalf("at the end: %q", msg)
	}
}

// A socket passed on for an app that ended while no Shell was there is let
// go, and taken out of the fd store, with the app's connection.
func TestKeepaliveOfAnAppGone(t *testing.T) {
	n := newNotifyRig(t)
	r := newRestartRig(t)
	r.storeOn()
	r.connect(600)
	n.recv()
	pid := r.a.proc.Pid
	_ = r.a.proc.Kill()
	for end := time.Now().Add(5 * time.Second); processAlive(pid); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the app did not end")
		}
	}
	c := r.next(r.handOver())
	for _, want := range []string{connName(r.a.M.ID, pid), keepName(r.a.M.ID, pid)} {
		if msg, _ := n.recv(); msg != "FDSTOREREMOVE=1\nFDNAME="+want {
			t.Fatalf("after the restart: %q, want %s taken out", msg, want)
		}
	}
	_ = c.onLoop(func() error {
		if c.KeepaliveOpen(r.a.M.ID) {
			t.Errorf("a connection of its own for an app that is gone")
		}
		return nil
	})
}

func nonblock(t *testing.T, conn net.Conn) bool {
	t.Helper()
	rc, err := conn.(syscall.Conn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	flags := 0
	_ = rc.Control(func(fd uintptr) { flags, err = unix.FcntlInt(fd, unix.F_GETFL, 0) })
	if err != nil {
		t.Fatal(err)
	}
	return flags&unix.O_NONBLOCK != 0
}

func setNonblock(t *testing.T, conn net.Conn, on bool) {
	t.Helper()
	rc, err := conn.(syscall.Conn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Control(func(fd uintptr) { err = unix.SetNonblock(int(fd), on) })
	if err != nil {
		t.Fatal(err)
	}
}
