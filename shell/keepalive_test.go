package shell

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/ui"
)

type keepTest struct {
	t    *testing.T
	c    *Core
	a    *App
	srv  net.Listener
	peer net.Conn // the server's end
	app  net.Conn // the app's copy
}

func newKeepTest(t *testing.T) *keepTest {
	t.Helper()
	a := &App{M: &Manifest{ID: "org.example.chat", Name: "Chat", Permissions: []string{ui.PermNet, ui.PermKeepalive}},
		running: true, ready: true, out: make(chan outbound, 64)}
	c := &Core{cfg: Config{StateDir: t.TempDir()}, log: log.New(io.Discard, "", 0), ev: make(chan func(), 16), apps: []*App{a},
		sys: &App{system: true, M: &Manifest{ID: "system"}}}
	c.broker = &broker{c: c, perms: map[string]map[string]string{}, stats: map[string]*CapStat{}, usage: map[string]map[string]*permUse{}}
	srv, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return &keepTest{t: t, c: c, a: a, srv: srv}
}

func (k *keepTest) pump(what string, done func() bool) {
	k.t.Helper()
	for end := time.After(10 * time.Second); !done(); {
		select {
		case f := <-k.c.ev:
			f()
		case <-end:
			k.t.Fatalf("no %s", what)
		}
	}
}

func (k *keepTest) call(args ui.KeepaliveArgs) (ui.Msg, []*os.File) {
	k.t.Helper()
	raw, _ := json.Marshal(args)
	var got *ui.Msg
	var files []*os.File
	k.c.broker.handleFor(k.a, ui.Msg{T: ui.MsgReq, Req: 1, Cap: ui.CapNetKeepalive, Args: raw}, func(m ui.Msg, f []*os.File) { got, files = &m, f })
	k.pump("answer", func() bool { return got != nil })
	return *got, files
}

// connect opens the app's connection with a heartbeat every every_s.
func (k *keepTest) connect(everySec int) ui.KeepaliveRes {
	k.t.Helper()
	port := k.srv.Addr().(*net.TCPAddr).Port
	m, files := k.call(ui.KeepaliveArgs{Host: "127.0.0.1", Port: port, EverySec: everySec})
	if !m.OK || len(files) != 1 {
		k.t.Fatalf("net.keepalive: %+v, %d files", m, len(files))
	}
	conn, err := net.FileConn(files[0])
	files[0].Close()
	if err != nil {
		k.t.Fatal(err)
	}
	k.t.Cleanup(func() { conn.Close() })
	k.app = conn
	if k.peer, err = k.srv.Accept(); err != nil {
		k.t.Fatal(err)
	}
	k.t.Cleanup(func() { k.peer.Close() })
	var res ui.KeepaliveRes
	_ = json.Unmarshal(m.Data, &res)
	return res
}

// events are the keepalive events the app got since the last look.
func (k *keepTest) events() []string {
	var out []string
	for {
		m, ok := next(k.a)
		if !ok {
			return out
		}
		if m.T == ui.MsgEvent && m.Event == ui.EventKeepalive {
			out = append(out, m.Value)
		}
	}
}

func (k *keepTest) later(d time.Duration) { k.c.clockSkew += d }

func (k *keepTest) use() ui.KeepaliveUse {
	k.t.Helper()
	u := k.c.KeepaliveUse(k.a.M.ID)
	if u == nil {
		k.t.Fatal("no keepalive use")
	}
	return *u
}

// The capability needs a native app with the permission and a heartbeat
// within limits.
func TestKeepaliveRefused(t *testing.T) {
	k := newKeepTest(t)
	port := k.srv.Addr().(*net.TCPAddr).Port
	if m, _ := k.call(ui.KeepaliveArgs{Host: "127.0.0.1", Port: port, EverySec: 30}); m.OK || !strings.Contains(m.Err, "every_s") {
		t.Fatalf("a heartbeat every 30 s: %+v", m)
	}
	k.a.M.Runtime = "js"
	if m, _ := k.call(ui.KeepaliveArgs{Host: "127.0.0.1", Port: port, EverySec: 600}); m.OK || !strings.Contains(m.Err, "native apps only") {
		t.Fatalf("a TypeScript app: %+v", m)
	}
	k.a.M.Runtime = ""
	k.a.M.Permissions = []string{ui.PermNet}
	if m, _ := k.call(ui.KeepaliveArgs{Host: "127.0.0.1", Port: port, EverySec: 600}); m.OK {
		t.Fatalf("without the keepalive permission: %+v", m)
	}
	if k.c.KeepaliveOpen(k.a.M.ID) {
		t.Fatal("a connection was kept")
	}
}

// A heartbeat rides a push heartbeat once its window is open, not before;
// with no push heartbeat before its deadline the system wakes for it, at
// most keepaliveOwnPerHour times an hour, and pushd's next heartbeat is
// waited for when it comes within the slack.
func TestKeepaliveBeats(t *testing.T) {
	k := newKeepTest(t)
	c := k.c
	k.connect(600)
	c.pushUp = true
	c.pushWindow(28 * time.Minute)
	if ev := k.events(); len(ev) != 0 {
		t.Fatalf("a heartbeat right after connecting: %q", ev)
	}
	k.later(400 * time.Second)
	c.pushWindow(28 * time.Minute)
	if ev := k.events(); len(ev) != 1 || ev[0] != ui.KeepaliveBeat {
		t.Fatalf("in the window: %q", ev)
	}
	if u := k.use(); u.Shared != 1 || u.Own != 0 {
		t.Fatalf("use %+v", u)
	}

	// The next push heartbeat is 28 min away: the system wakes at the
	// deadline, and the sleep is told.
	conn := c.keep.conns[k.a.M.ID]
	if w := c.keepaliveNextWake(); !w.Equal(conn.beat.Due()) {
		t.Fatalf("next wake %v, deadline %v", w, conn.beat.Due())
	}
	k.later(599 * time.Second)
	c.keepaliveTick()
	if ev := k.events(); len(ev) != 0 {
		t.Fatalf("before the deadline: %q", ev)
	}
	k.later(time.Second)
	c.keepaliveTick()
	if ev := k.events(); len(ev) != 1 || ev[0] != ui.KeepaliveBeat {
		t.Fatalf("at the deadline: %q", ev)
	}
	if u := k.use(); u.Own != 1 || u.MAh != 0.5 {
		t.Fatalf("use %+v", u)
	}

	// A push heartbeat due within the slack past the deadline is waited for.
	c.keep.lastPush = c.now().Add(605*time.Second - 28*time.Minute)
	k.later(600 * time.Second)
	c.keepaliveTick()
	if ev := k.events(); len(ev) != 0 {
		t.Fatalf("the push heartbeat was not waited for: %q", ev)
	}
	k.later(5 * time.Second)
	c.pushWindow(28 * time.Minute)
	if ev := k.events(); len(ev) != 1 {
		t.Fatalf("on the push heartbeat: %q", ev)
	}

	// The budget: keepaliveOwnPerHour own wakes, then the heartbeat waits.
	for i := range keepaliveOwnPerHour + 2 {
		conn.beat.Last = c.now().Add(-conn.beat.Every)
		c.keepaliveTick()
		if ev := k.events(); len(ev) != 1 && i < keepaliveOwnPerHour-1 || len(ev) != 0 && i >= keepaliveOwnPerHour-1 {
			t.Fatalf("own wake %d: %q", i+2, ev)
		}
	}
	if !c.keepaliveNextWake().IsZero() {
		t.Fatal("the sleep wakes for an app over its budget")
	}
	// Other wakes still carry it.
	c.runJobs()
	c.keepaliveWindow("a batch of jobs")
	if ev := k.events(); len(ev) != 1 {
		t.Fatalf("over the budget, a wake made anyway: %q", ev)
	}
	k.later(time.Hour)
	if u := k.use(); u.Own != keepaliveOwnPerHour || u.Shared != 3 {
		t.Fatalf("use %+v", u)
	}
	if c.keepaliveNextWake().IsZero() {
		t.Fatal("an hour on, the budget is back")
	}
	found := false
	for _, e := range c.energyView() {
		if e.App == k.a.M.ID && e.Keepalive != nil && e.Keepalive.Own == keepaliveOwnPerHour {
			found = true
		}
	}
	if !found {
		t.Fatalf("Settings › Battery: %+v", c.energyView())
	}
}

// Data arriving while the app is frozen thaws it (keepalive data), the
// kernel's TCP keep-alive is turned off when it freezes, and the server
// closing the connection is told the same way.
func TestKeepaliveData(t *testing.T) {
	k := newKeepTest(t)
	c := k.c
	k.connect(600)
	if rc, err := k.app.(*net.TCPConn).SyscallConn(); err == nil {
		_ = rc.Control(func(fd uintptr) { _ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_KEEPALIVE, 1) })
	}
	k.a.frozen = true
	c.keepaliveFrozen(k.a)
	conn := c.keep.conns[k.a.M.ID]
	rc, _ := conn.conn.SyscallConn()
	on := -1
	_ = rc.Control(func(fd uintptr) { on, _ = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_KEEPALIVE) })
	if on != 0 {
		t.Fatalf("SO_KEEPALIVE %d while frozen", on)
	}

	if _, err := k.peer.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	var ev []string
	k.pump("data event", func() bool { ev = append(ev, k.events()...); return len(ev) > 0 })
	if ev[0] != ui.KeepaliveData || k.a.frozen || k.use().Data != 1 {
		t.Fatalf("data: %q, frozen %v, %+v", ev, k.a.frozen, k.use())
	}
	line, err := bufio.NewReader(k.app).ReadString('\n')
	if err != nil || line != "hello\n" {
		t.Fatalf("the app read %q, %v", line, err)
	}

	k.a.frozen = true
	c.keepaliveFrozen(k.a)
	k.peer.Close()
	ev = nil
	k.pump("closed event", func() bool { ev = append(ev, k.events()...); return len(ev) > 0 })
	if ev[0] != ui.KeepaliveClosed || c.KeepaliveOpen(k.a.M.ID) {
		t.Fatalf("closed: %q, open %v", ev, c.KeepaliveOpen(k.a.M.ID))
	}
	if u := k.use(); u.MAh != float64(int(push.LTEWake.MAh()*10+0.5))/10 {
		t.Fatalf("use %+v", u)
	}
}

// Over its budget for data, the data waits in the kernel.
func TestKeepaliveDataBudget(t *testing.T) {
	k := newKeepTest(t)
	c := k.c
	k.connect(600)
	b := c.keep.budget(&c.keep.data, k.a.M.ID, keepaliveDataPerHour)
	for range keepaliveDataPerHour {
		b.Take(c.now())
	}
	k.a.frozen = true
	c.keepaliveFrozen(k.a)
	if _, err := k.peer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	k.pump("look at the data", func() bool { return c.keep.conns[k.a.M.ID].overData })
	if ev := k.events(); len(ev) != 0 || !k.a.frozen {
		t.Fatalf("over the budget: %q, frozen %v", ev, k.a.frozen)
	}
}

// The app ending lets the connection go; uninstalled, its use goes too.
func TestKeepaliveEnds(t *testing.T) {
	k := newKeepTest(t)
	k.connect(600)
	k.c.keepaliveEnd(k.a, "the app ended")
	if k.c.KeepaliveOpen(k.a.M.ID) {
		t.Fatal("still open")
	}
	_ = k.app.Close()
	_ = k.peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := k.peer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("the server's end: %v", err)
	}
	k.c.keepaliveUsed(k.a.M.ID, keepOwn)
	k.c.keepaliveForget(k.a)
	if u := k.c.KeepaliveUse(k.a.M.ID); u != nil || len(k.c.energyView()) != 0 {
		t.Fatalf("uninstalled, Settings › Battery still has %+v", k.c.energyView())
	}
}
