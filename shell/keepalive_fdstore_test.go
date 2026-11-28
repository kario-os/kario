package shell

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// fakeStore is systemd's fd store as service.c (v259) keeps it, on the
// notify socket: at most size files; a full store refuses one (EXFULL,
// closing it) before it looks for a duplicate; a duplicate of a stored
// file is not stored again; a removal takes out every file of that name.
type fakeStore struct {
	t       *testing.T
	ln      *net.UnixConn
	size    int
	names   []string
	fds     []int
	refused []string
}

func newFakeStore(t *testing.T, size int) *fakeStore {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "notify")
	ln, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeStore{t: t, ln: ln, size: size}
	t.Cleanup(func() {
		ln.Close()
		for _, fd := range s.fds {
			syscall.Close(fd)
		}
	})
	t.Setenv("NOTIFY_SOCKET", sock)
	t.Setenv("FDSTORE", strconv.Itoa(size))
	return s
}

// sync takes in what the Shell sent so far.
func (s *fakeStore) sync() {
	buf, oob := make([]byte, 512), make([]byte, 512)
	for {
		_ = s.ln.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		n, oobn, _, _, err := s.ln.ReadMsgUnix(buf, oob)
		if err != nil {
			return
		}
		var fds []int
		if msgs, err := syscall.ParseSocketControlMessage(oob[:oobn]); err == nil {
			for _, m := range msgs {
				got, _ := syscall.ParseUnixRights(&m)
				fds = append(fds, got...)
			}
		}
		tags := strings.Split(string(buf[:n]), "\n")
		name := ""
		for _, tag := range tags {
			if v, ok := strings.CutPrefix(tag, "FDNAME="); ok {
				name = v
			}
		}
		switch {
		case slices.Contains(tags, "FDSTOREREMOVE=1"):
			for i := len(s.names) - 1; i >= 0; i-- {
				if s.names[i] == name {
					syscall.Close(s.fds[i])
					s.names, s.fds = slices.Delete(s.names, i, i+1), slices.Delete(s.fds, i, i+1)
				}
			}
		case slices.Contains(tags, "FDSTORE=1"):
			for _, fd := range fds {
				s.add(fd, name)
			}
			fds = nil
		}
		for _, fd := range fds {
			syscall.Close(fd)
		}
	}
}

func (s *fakeStore) add(fd int, name string) {
	if len(s.fds) >= s.size {
		syscall.Close(fd)
		s.refused = append(s.refused, name)
		return
	}
	var st, o syscall.Stat_t
	_ = syscall.Fstat(fd, &st)
	for _, have := range s.fds {
		if syscall.Fstat(have, &o) == nil && o.Dev == st.Dev && o.Ino == st.Ino {
			syscall.Close(fd)
			return
		}
	}
	s.names, s.fds = append(s.names, name), append(s.fds, fd)
}

// crash is what systemd hands a Shell it restarts after a crash: a copy
// of everything in the store (LISTEN_FDS), as InheritedApps reads it.
func (s *fakeStore) crash() []Inherited {
	s.t.Helper()
	var out []Inherited
	for i, name := range s.names {
		fd, err := unix.Dup(s.fds[i])
		if err != nil {
			s.t.Fatal(err)
		}
		in, ok := inherited(os.NewFile(uintptr(fd), name), true)
		if !ok {
			s.t.Fatalf("%q in the fd store", name)
		}
		out = append(out, in)
	}
	return out
}

// logBuf is a Shell's log, read while the Shell's loop writes it.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) count(s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.b.String(), s)
}

// fdRig is a Shell with systemd's fd store and apps started into it
// as spawn starts them, some with connections of their own.
type fdRig struct {
	*keepTest
	store *fakeStore
	log   *logBuf
	apps  []*fdApp
}

type fdApp struct {
	a      *App
	uiConn *os.File // the app's end of its connection to the Shell
	own    net.Conn // the app's copy of its own connection
}

func newFDRig(t *testing.T, size int) *fdRig {
	k := newKeepTest(t)
	r := &fdRig{keepTest: k, store: newFakeStore(t, size), log: &logBuf{}}
	k.c.fdstore, k.c.apps = true, nil
	k.c.log = log.New(r.log, "", 0)
	return r
}

func (r *fdRig) app(id string) *fdApp {
	for _, s := range r.apps {
		if s.a.M.ID == id {
			return s
		}
	}
	r.t.Fatalf("no app %s", id)
	return nil
}

// start runs an app (a sleep) and puts its connection into the store.
func (r *fdRig) start(id string) {
	r.t.Helper()
	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	end, shellEnd := os.NewFile(uintptr(fds[0]), "app"), os.NewFile(uintptr(fds[1]), "shell")
	r.t.Cleanup(func() { end.Close() })
	a := &App{M: &Manifest{ID: id, Name: id, Permissions: []string{ui.PermNet, ui.PermKeepalive}},
		proc: sleep.Process, running: true, ready: true, out: make(chan outbound, 64)}
	r.c.apps = append(r.c.apps, a)
	r.apps = append(r.apps, &fdApp{a: a, uiConn: end})
	r.c.storeConn(a, sleep.Process.Pid, shellEnd)
	a.conn = wire.New(shellEnd)
}

// end is the app's process ending: what appExited does with the store.
func (r *fdRig) end(id string) {
	a := r.app(id).a
	_ = a.proc.Kill()
	r.c.unstoreConn(a)
	r.c.keepaliveEnd(a, "the app ended")
	a.running = false
}

// connect opens id's own connection.
func (r *fdRig) connect(id string) {
	r.t.Helper()
	s := r.app(id)
	raw, _ := json.Marshal(ui.KeepaliveArgs{Host: "127.0.0.1", Port: r.srv.Addr().(*net.TCPAddr).Port, EverySec: 600})
	var got *ui.Msg
	var files []*os.File
	r.c.broker.handleFor(s.a, ui.Msg{T: ui.MsgReq, Req: 1, Cap: ui.CapNetKeepalive, Args: raw}, func(m ui.Msg, f []*os.File) { got, files = &m, f })
	r.pump("answer", func() bool { return got != nil })
	if !got.OK || len(files) != 1 {
		r.t.Fatalf("net.keepalive for %s: %+v", id, *got)
	}
	conn, err := net.FileConn(files[0])
	files[0].Close()
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { conn.Close() })
	s.own = conn
	peer, err := r.srv.Accept()
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { peer.Close() })
}

// data: data came on id's own connection while it was frozen.
func (r *fdRig) data(id string) {
	a := r.app(id).a
	a.frozen = true
	r.c.keepaliveReadable(id, r.c.keep.conns[id], false)
}

// stored checks the store: every running app's connection, and the own
// connections of keep and of no other app; nothing refused.
func (r *fdRig) stored(when string, keep ...string) {
	r.t.Helper()
	r.store.sync()
	if len(r.store.refused) > 0 {
		r.t.Fatalf("%s: the fd store refused %q (it holds %q)", when, r.store.refused, r.store.names)
	}
	for _, s := range r.apps {
		pid := s.a.proc.Pid
		if s.a.running && !slices.Contains(r.store.names, connName(s.a.M.ID, pid)) {
			r.t.Fatalf("%s: %s's connection is not in the fd store (%q)", when, s.a.M.ID, r.store.names)
		}
		if in, want := slices.Contains(r.store.names, keepName(s.a.M.ID, pid)), slices.Contains(keep, s.a.M.ID); in != want {
			r.t.Fatalf("%s: %s's own connection in the fd store %v, want %v (%q)", when, s.a.M.ID, in, want, r.store.names)
		}
	}
}

// keepEvents reads the keepalive events the next Shell sends an app (value
// and text) for wait.
func keepEvents(t *testing.T, end *os.File, wait time.Duration) []string {
	t.Helper()
	var out []string
	_ = end.SetReadDeadline(time.Now().Add(wait))
	rd := bufio.NewReader(end)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return out
		}
		var m ui.Msg
		if json.Unmarshal(line, &m) == nil && m.T == ui.MsgEvent && m.Event == ui.EventKeepalive {
			out = append(out, m.Value+" "+m.Text)
		}
	}
}

// Filling the fd store (8 places: own connections at most 2) past its
// budget: an app's connection always gets a place, the own connection
// whose app used it longest ago gives its place up (not the one that
// connected first), each said once in the log; one comes back when an app
// ends, the one used last. After a crash the next Shell takes over the own
// connection that had a place, and tells the two that had none, once
// each, that they were lost for want of room. The Shell used to store
// every own connection as it connected: the store filled with them, the
// next apps' connections were refused, and those apps would have been
// lost in a crash; the apps whose own connections were refused were told
// nothing more than lost.
func TestKeepaliveFDStoreBudget(t *testing.T) {
	r := newFDRig(t, 8)
	chat1, chat2, chat3 := "org.example.chat1", "org.example.chat2", "org.example.chat3"
	for _, id := range []string{"org.example.f1", "org.example.f2", "org.example.f3", chat1, chat2, chat3} {
		r.start(id)
	}
	r.connect(chat1)
	r.later(time.Minute)
	r.connect(chat2)
	r.stored("two own connections", chat1, chat2)
	r.later(time.Minute)
	r.data(chat1)
	r.later(time.Minute)
	r.connect(chat3)
	r.stored("a third own connection: chat2's waited longest for data", chat1, chat3)
	r.later(time.Minute)
	r.start("org.example.f4")
	r.stored("a seventh app", chat3)
	r.later(time.Minute)
	r.start("org.example.f5")
	r.stored("an eighth app")
	r.later(time.Minute)
	r.data(chat2)
	r.later(time.Minute)
	r.end("org.example.f5")
	r.stored("the eighth app ended: chat2 had data last", chat2)
	for _, id := range []string{chat1, chat2, chat3} {
		if n := r.log.count("no room for " + id + "'s connection in the fd store"); n != 1 {
			t.Errorf("the log says %d times that %s had no room, want once", n, id)
		}
	}
	if n := r.log.count(chat2 + "'s connection has a place in the fd store again"); n != 1 {
		t.Errorf("the log says %d times that chat2 is back, want once", n)
	}
	if t.Failed() {
		return
	}

	before := slices.Clone(r.store.names)
	list := r.store.crash()
	var running []*App
	for _, s := range r.apps {
		running = append(running, s.a)
		if s.a.conn != nil {
			s.a.conn.Close()
		}
	}
	for _, k := range r.c.keep.conns {
		k.conn.Close()
	}
	c := nextShell(t, r.c, running, list)
	for _, id := range []string{chat1, chat2, chat3} {
		ev := keepEvents(t, r.app(id).uiConn, time.Second)
		want := []string{ui.KeepaliveLost + " " + ui.KeepaliveLostFull}
		if id == chat2 {
			want = nil
		}
		if !slices.Equal(ev, want) {
			t.Errorf("after the crash %s got %q, want %q", id, ev, want)
		}
		told := r.log.count(id + "'s connection had no room in the fd store; told the app to connect again (full)")
		if want := len(want); told != want {
			t.Errorf("the next Shell's log tells %s %d times, want %d", id, told, want)
		}
	}
	_ = c.onLoop(func() error {
		for _, id := range []string{chat1, chat2, chat3} {
			if open := c.KeepaliveOpen(id); open != (id == chat2) {
				t.Errorf("the next Shell has %s's own connection: %v", id, open)
			}
		}
		return nil
	})
	r.store.sync()
	if !slices.Equal(r.store.names, before) || len(r.store.refused) > 0 {
		t.Errorf("the next Shell changed the fd store: %q, before the crash %q (refused %q)", r.store.names, before, r.store.refused)
	}
}

// pushd's heartbeat and the pushes on its connection are written to
// keepalive.json once the time moved push.KeepaliveSlack or more, not
// with every push: a Shell restarted after a crash at any moment has the
// last push's time less than the slack early, and a minute of a push a
// second writes it six times. Without own connections nothing uses it:
// it is neither written nor read. The Shell used to write it only with
// other changes: after a crash it had the time of the last of those, as
// old as anything.
func TestKeepaliveLastPushAfterACrash(t *testing.T) {
	k := newKeepTest(t)
	c := k.c
	k.connect(600)
	c.pushUp = true
	restored := func() time.Time {
		n := &Core{cfg: c.cfg, log: c.log}
		n.loadKeepalive()
		return n.keep.lastPush
	}
	k.later(time.Minute)
	c.pushWindow(28 * time.Minute)
	if got := restored(); !got.Equal(c.keep.lastPush) {
		t.Fatalf("pushd's heartbeat came at %v; a Shell restarted then has %v", c.keep.lastPush, got)
	}
	writes, last := 0, restored()
	for i := range 60 {
		k.later(time.Second)
		c.pushMsg(push.Message{ID: strconv.Itoa(i), App: "org.example.other"})
		got := restored()
		if off := c.keep.lastPush.Sub(got); off < 0 || off >= push.KeepaliveSlack {
			t.Fatalf("push %d came at %v; a Shell restarted then has %v, %v early", i+1, c.keep.lastPush, got, off)
		}
		if !got.Equal(last) {
			writes, last = writes+1, got
		}
	}
	if want := int(time.Minute / push.KeepaliveSlack); writes != want {
		t.Fatalf("a push a second for a minute wrote the time %d times, want %d", writes, want)
	}
	c.keepaliveEnd(k.a, "the app closed it")
	k.later(time.Minute)
	c.pushWindow(28 * time.Minute)
	if got := restored(); !got.IsZero() {
		t.Fatalf("without own connections a restarted Shell takes the push time %v", got)
	}
}
