package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/statefile"
	"github.com/kario-os/kario/ui"
)

// An app's own long connection (net.keepalive, 21-push.md §8), for clients
// that will not do without their own channel (a WeChat-class messenger):
// the system push connection stays the way in, this one is allowed to an
// app holding the keepalive permission, which the store reviews. One per
// app. The Shell dials it as the app (its uid: the firewall and data usage
// see the app's traffic) and hands the app the socket; it keeps a copy of
// its own, so the connection outlives nothing the app does not, and looks
// at it while the app is frozen: the kernel keeps the TCP connection, and
// data arriving thaws the app for keepaliveDataWake (as push data does),
// keepaliveDataPerHour times an hour; past that the data waits in the
// kernel until the app is awake anyway (its next heartbeat, the
// foreground). The app's heartbeats are the system's: a "keepalive" event
// thaws it for keepaliveBeatWake in a wake that comes anyway (push.Beat:
// a push heartbeat, a push, a batch of jobs, data on such a connection),
// and only when none came by the deadline does the system wake for it,
// keepaliveOwnPerHour times an hour, with pushd's keep-alive riding that
// wake. Kernel TCP keep-alive probes on the socket would wake the radio
// behind the system's back: they are turned off whenever the app freezes.
// Settings › Battery shows each app's wakes and their estimated charge.
// A TypeScript app's runtime makes the call itself, for the app's keepalive
// WebSocket (21-push.md §8.3), and speaks WebSocket on the socket.
//
// A Shell that restarts (handoff.go) passes its copy on beside the app's
// connection, named keep-<app id>-<pid>, and keeps the schedules and the
// last day's wakes in keepalive.json: the next one watches the socket on
// the same schedule and goes on counting. Where the socket did not come
// along, the app is told why (ui.KeepaliveLost, ui.KeepaliveLostFull...)
// and calls net.keepalive again. For a crash the copies wait in the fd
// store, within its budget (keepaliveStore).

const (
	keepaliveOwnPerHour  = 6
	keepaliveDataPerHour = pushWakesPerHour
	keepaliveBeatWake    = 2 * time.Second
	keepaliveDataWake    = pushWakeBudget
	keepaliveDialTimeout = 10 * time.Second
	keepaliveUseKept     = 24 * time.Hour
)

// What an app's own connection's wakes were (ui.KeepaliveUse).
const (
	keepOwn    = "own"
	keepShared = "shared"
	keepData   = "data"
)

type keepConn struct {
	addr string
	// conn: the Shell's copy of the socket the app holds; pid: the app's
	// process holding it.
	conn *net.TCPConn
	pid  int
	beat push.Beat
	// arm asks the watcher to look for data (the app froze).
	arm chan struct{}
	// starved, overData: the log said the budget is used up.
	starved, overData bool
	// used: it connected, or data came on it; stored: it is in the fd
	// store; full: left out of it for want of room.
	used         time.Time
	stored, full bool
}

type keepUse struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
}

type keepState struct {
	conns map[string]*keepConn
	// own, data: each app's wakes over the last hour.
	own, data map[string]*push.WakeBudget
	use       map[string][]keepUse
	// pushEvery, lastPush: pushd's heartbeat interval, and the last line
	// on its gateway connection (by the Shell's clock); savedEvery,
	// savedPush: the ones keepalive.json has.
	pushEvery, savedEvery time.Duration
	lastPush, savedPush   time.Time
	// left: the connections the previous Shell had, until Adopt.
	left map[string]keepLeft
}

// keepSaved is keepalive.json.
type keepSaved struct {
	Conns     map[string]keepLeft  `json:"conns,omitempty"`
	Use       map[string][]keepUse `json:"use,omitempty"`
	PushEvery int                  `json:"push_every_s,omitempty"`
	LastPush  time.Time            `json:"last_push"`
}

type keepLeft struct {
	Pid      int       `json:"pid"`
	Addr     string    `json:"addr"`
	EverySec int       `json:"every_s"`
	Last     time.Time `json:"last"`
	Used     time.Time `json:"used"`
	Full     bool      `json:"full,omitempty"`
}

func (k *keepState) budget(m *map[string]*push.WakeBudget, id string, perHour int) *push.WakeBudget {
	if *m == nil {
		*m = map[string]*push.WakeBudget{}
	}
	b := (*m)[id]
	if b == nil {
		b = &push.WakeBudget{PerHour: perHour}
		(*m)[id] = b
	}
	return b
}

func (c *Core) keepaliveCap(a *App, m ui.Msg, reply func(any, error)) {
	var args ui.KeepaliveArgs
	_ = json.Unmarshal(m.Args, &args)
	if args.Close {
		c.keepaliveEnd(a, "the app closed it")
		reply(ui.KeepaliveRes{}, nil)
		return
	}
	switch {
	case !c.netAllowed(a):
		reply(nil, errors.New("the network is off for this app"))
		return
	case args.Host == "" || args.Port < 1 || args.Port > 65535:
		reply(nil, errors.New("net.keepalive needs a host and a port"))
		return
	case args.EverySec < ui.KeepaliveMinSec || args.EverySec > ui.KeepaliveMaxSec:
		reply(nil, fmt.Errorf("every_s must be %d to %d", ui.KeepaliveMinSec, ui.KeepaliveMaxSec))
		return
	}
	addr := net.JoinHostPort(args.Host, strconv.Itoa(args.Port))
	uid := -1
	if c.Sandboxed() {
		u, err := c.appUID(a.M.ID)
		if err != nil {
			reply(nil, err)
			return
		}
		if err := c.applyNetPolicy(); err != nil {
			c.log.Printf("traffic: %v", err)
		}
		uid = int(u)
	}
	proc := a.proc
	go func() {
		conn, err := dialAs(uid, addr)
		c.Post(func() {
			if err == nil && (a.proc != proc || !a.running) {
				conn.Close()
				err = errors.New("the app ended")
			}
			if err != nil {
				reply(nil, err)
				return
			}
			f, err := conn.File()
			if err != nil {
				conn.Close()
				reply(nil, err)
				return
			}
			c.keepaliveEnd(a, "replaced")
			if c.keep.conns == nil {
				c.keep.conns = map[string]*keepConn{}
			}
			k := &keepConn{addr: addr, conn: conn, beat: push.Beat{Every: time.Duration(args.EverySec) * time.Second, Last: c.now()},
				arm: make(chan struct{}, 1), used: c.now()}
			if proc != nil {
				k.pid = proc.Pid
			}
			c.keep.conns[a.M.ID] = k
			if !c.keepaliveStore() {
				c.keepaliveSave()
			}
			go c.watchKeepalive(a.M.ID, k)
			c.log.Printf("keepalive: %s connected to %s, heartbeat every %s", a.M.ID, addr, k.beat.Every)
			reply(withFiles{data: ui.KeepaliveRes{EverySec: args.EverySec, PushSec: int(c.keep.pushEvery / time.Second), Addr: addr},
				files: []*os.File{f}}, nil)
		})
	}()
}

// dialAs connects to addr with a socket owned by uid (-1: the Shell's), as
// sendAs sends. No TCP keep-alive: only the system wakes the radio. No fast
// fallback either: it dials a name's IPv4 and IPv6 addresses in goroutines
// of their own, on threads without the app's fsuid.
func dialAs(uid int, addr string) (*net.TCPConn, error) {
	d := net.Dialer{Timeout: keepaliveDialTimeout, KeepAlive: -1, FallbackDelay: -1}
	type result struct {
		conn net.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		if uid < 0 {
			conn, err := d.Dial("tcp", addr)
			done <- result{conn, err}
			return
		}
		// The thread is not unlocked if fsuid cannot be put back: it then
		// ends with this goroutine instead of running other code.
		runtime.LockOSThread()
		tid := unix.Gettid()
		d.Control = func(string, string, syscall.RawConn) error {
			if unix.Gettid() != tid {
				return errors.New("the socket was made on another thread, not as the app")
			}
			return nil
		}
		prev, _ := unix.SetfsuidRetUid(uid)
		conn, err := d.Dial("tcp", addr)
		if back, _ := unix.SetfsuidRetUid(prev); back != uid {
			if conn != nil {
				conn.Close()
			}
			done <- result{nil, os.ErrPermission}
			return
		}
		runtime.UnlockOSThread()
		done <- result{conn, err}
	}()
	r := <-done
	if r.err != nil {
		return nil, r.err
	}
	return r.conn.(*net.TCPConn), nil
}

// watchKeepalive looks at the connection each time it is armed (the app
// froze) until data or the end of it is there, without reading it.
func (c *Core) watchKeepalive(app string, k *keepConn) {
	rc, err := k.conn.SyscallConn()
	if err != nil {
		return
	}
	buf := make([]byte, 1)
	for range k.arm {
		eof := false
		err := rc.Read(func(fd uintptr) bool {
			// The app shares the socket and may have made it blocking.
			n, _, err := unix.Recvfrom(int(fd), buf, unix.MSG_PEEK|unix.MSG_DONTWAIT)
			if err == unix.EAGAIN || err == unix.EINTR {
				return false
			}
			eof = err != nil || n == 0
			return true
		})
		if err != nil {
			return
		}
		c.Post(func() { c.keepaliveReadable(app, k, eof) })
	}
}

// keepaliveFrozen: the app froze; its connection is looked at for data.
func (c *Core) keepaliveFrozen(a *App) {
	k := c.keep.conns[a.M.ID]
	if k == nil {
		return
	}
	if rc, err := k.conn.SyscallConn(); err == nil {
		_ = rc.Control(func(fd uintptr) {
			if on, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_KEEPALIVE); err == nil && on != 0 {
				_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_KEEPALIVE, 0)
				c.log.Printf("keepalive: %s had turned on TCP keep-alive; off while it is frozen", a.M.ID)
			}
		})
	}
	select {
	case k.arm <- struct{}{}:
	default:
	}
}

func (c *Core) keepaliveReadable(app string, k *keepConn, eof bool) {
	a := c.appByID(app)
	if c.keep.conns[app] != k || a == nil || !a.running {
		return
	}
	if eof {
		c.keepaliveEnd(a, "the server closed it")
		c.keepaliveWake(a, ui.KeepaliveClosed, keepaliveDataWake)
		return
	}
	k.used = c.now()
	if !a.frozen {
		// Awake, it reads the data itself; looked at again when it freezes.
		return
	}
	now := c.now()
	if !c.keep.budget(&c.keep.data, app, keepaliveDataPerHour).Take(now) {
		if !k.overData {
			c.log.Printf("keepalive: data for %s over its %d wakes an hour: it waits until the app is awake anyway", app, keepaliveDataPerHour)
		}
		k.overData = true
		return
	}
	k.overData = false
	c.keepaliveUsed(app, keepData)
	c.keepaliveSave()
	c.log.Printf("keepalive: data on %s's connection; woke it for %s", app, keepaliveDataWake)
	c.keepaliveWake(a, ui.KeepaliveData, keepaliveDataWake)
	c.keepaliveWindow("data for " + app)
}

// keepaliveWake hands the app a keepalive event and lets it run for d.
func (c *Core) keepaliveWake(a *App, value string, d time.Duration) {
	if !a.running || !a.ready || a.killed {
		return
	}
	a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventKeepalive, Value: value})
	c.wake(a, d)
}

// keepaliveLost tells the app the Shell did not take its connection over,
// and why (ui.KeepaliveLostFull...).
func (c *Core) keepaliveLost(a *App, why string) {
	if !a.running || !a.ready || a.killed {
		return
	}
	a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventKeepalive, Value: ui.KeepaliveLost, Text: why})
	c.wake(a, keepaliveDataWake)
}

// keepaliveWindow: the radio is up for something else (why); every
// heartbeat whose window is open rides it.
func (c *Core) keepaliveWindow(why string) {
	now, rode := c.now(), false
	for id, k := range c.keep.conns {
		a := c.appByID(id)
		if a == nil || !a.running || !k.beat.Rides(now) {
			continue
		}
		k.beat.Last, rode = now, true
		if a != c.fg {
			c.keepaliveUsed(id, keepShared)
			c.log.Printf("keepalive: %s's heartbeat rides %s", id, why)
		}
		c.keepaliveWake(a, ui.KeepaliveBeat, keepaliveBeatWake)
	}
	if rode {
		c.keepaliveSave()
	}
}

// pushWindow: a line came on pushd's gateway connection (its heartbeat
// every iv).
func (c *Core) pushWindow(iv time.Duration) {
	c.keep.pushEvery, c.keep.lastPush = iv, c.now()
	c.keepalivePushed("the push heartbeat")
}

// keepalivePushed: a line or a push came on pushd's gateway connection
// (lastPush). The heartbeats whose window is open ride it, and the time is
// written once it is push.KeepaliveSlack or more from the one in
// keepalive.json, or the interval changed: a Shell restarted after a crash
// expects pushd's next heartbeat less than the slack early, which makes no
// wake of its own (push.Beat.OwnWake) and at worst brings a heartbeat the
// slack late, as waiting for the push heartbeat does; a busy push
// connection costs a write a slack, not one a push. Only own connections
// use the time: without any it is not written (nor read, loadKeepalive).
func (c *Core) keepalivePushed(why string) {
	c.keepaliveWindow(why)
	off := c.keep.lastPush.Sub(c.keep.savedPush).Abs()
	if len(c.keep.conns) > 0 && (off >= push.KeepaliveSlack || c.keep.pushEvery != c.keep.savedEvery) {
		c.keepaliveSave()
	}
}

// nextPushWindow is when pushd's next heartbeat is expected (zero: not
// known); traffic on its connection puts it a full interval on.
func (c *Core) nextPushWindow() time.Time {
	if c.keep.pushEvery <= 0 || c.keep.lastPush.IsZero() || !c.pushUp {
		return time.Time{}
	}
	return c.keep.lastPush.Add(c.keep.pushEvery)
}

// keepaliveTick runs with the housekeeping: the system wakes for the
// heartbeats no other wake carried by their deadline, within the budget.
func (c *Core) keepaliveTick() {
	now, next := c.now(), c.nextPushWindow()
	for id, k := range c.keep.conns {
		a := c.appByID(id)
		if a == nil || !a.running || now.Before(k.beat.OwnWake(next)) {
			continue
		}
		if a == c.fg {
			k.beat.Last = now
			c.keepaliveSave()
			c.keepaliveWake(a, ui.KeepaliveBeat, keepaliveBeatWake)
			continue
		}
		if !c.keep.budget(&c.keep.own, id, keepaliveOwnPerHour).Take(now) {
			if !k.starved {
				c.log.Printf("keepalive: %s used its %d wakes of its own this hour; its heartbeat waits for a wake that comes anyway", id, keepaliveOwnPerHour)
			}
			k.starved = true
			continue
		}
		k.starved = false
		k.beat.Last = now
		c.keepaliveUsed(id, keepOwn)
		c.keepaliveSave()
		c.log.Printf("keepalive: woke for %s's heartbeat (every %s)", id, k.beat.Every)
		c.keepaliveWake(a, ui.KeepaliveBeat, keepaliveBeatWake)
		// pushd's keep-alive rides this wake, and so do the other apps'.
		c.radioActive()
		c.keepaliveWindow("the wake for " + id)
	}
}

// keepaliveNextWake is the earliest wake the system makes for a heartbeat
// alone (zero: none); an app whose budget is used up waits for one that
// comes anyway.
func (c *Core) keepaliveNextWake() time.Time {
	var wake time.Time
	now, next := c.now(), c.nextPushWindow()
	for id, k := range c.keep.conns {
		if c.keep.budget(&c.keep.own, id, keepaliveOwnPerHour).Left(now) == 0 {
			continue
		}
		if t := k.beat.OwnWake(next); wake.IsZero() || t.Before(wake) {
			wake = t
		}
	}
	return wake
}

// keepaliveEnd lets the system's copy of the app's connection go, the one
// in the fd store too: it would hold the connection open.
func (c *Core) keepaliveEnd(a *App, why string) {
	k := c.keep.conns[a.M.ID]
	if k == nil {
		return
	}
	delete(c.keep.conns, a.M.ID)
	k.conn.Close()
	close(k.arm)
	c.unstoreKeep(a.M.ID, k.pid)
	if !c.keepaliveStore() {
		c.keepaliveSave()
	}
	c.log.Printf("keepalive: %s's connection to %s ended: %s", a.M.ID, k.addr, why)
}

func (c *Core) unstoreKeep(id string, pid int) {
	if c.fdstore && pid > 0 {
		_ = sdNotify("FDSTOREREMOVE=1\nFDNAME=" + keepName(id, pid))
	}
}

// keepaliveStore settles which own connections are in the fd store: only
// those of apps whose connection to the Shell is there (the next Shell
// takes no other over), at most 1/keepStoreShare of the store, and only the
// places the apps' connections leave. When they do not all fit, the ones
// their apps used last get the places and the rest wait outside (full),
// each said once; should the Shell crash, their apps are told why
// (ui.KeepaliveLostFull). Those that go out are written as such before
// they go, and taken out before any goes in, so that the store never holds
// more than it may. Reports whether it wrote keepalive.json.
func (c *Core) keepaliveStore() bool {
	if !c.fdstore {
		return false
	}
	size := c.fdStoreSize()
	var ids []string
	for id, k := range c.keep.conns {
		if k.pid > 0 && c.fds.apps[id] {
			ids = append(ids, id)
		} else if k.stored {
			k.stored = false
			c.unstoreKeep(id, k.pid)
		}
	}
	slices.SortFunc(ids, func(x, y string) int {
		if n := c.keepaliveLastUse(y).Compare(c.keepaliveLastUse(x)); n != 0 {
			return n
		}
		return strings.Compare(x, y)
	})
	room := max(0, min(size/keepStoreShare, size-len(c.fds.apps), len(ids)))
	in, out := ids[:room], ids[room:]
	wrote := false
	for _, id := range out {
		if k := c.keep.conns[id]; !k.full {
			k.full, wrote = true, true
			c.log.Printf("keepalive: no room for %s's connection in the fd store (%d places, %d of them apps' connections, at most %d own connections): should the Shell crash, the app is told to connect again",
				id, size, len(c.fds.apps), size/keepStoreShare)
		}
	}
	if wrote {
		c.keepaliveSave()
	}
	for _, id := range out {
		if k := c.keep.conns[id]; k.stored {
			k.stored = false
			c.unstoreKeep(id, k.pid)
		}
	}
	back := false
	for _, id := range in {
		k := c.keep.conns[id]
		if k.stored {
			continue
		}
		if err := c.storeKeep(id, k); err != nil {
			c.log.Printf("fd store: %s's connection: %v", id, err)
			continue
		}
		k.stored = true
		if k.full {
			k.full, back = false, true
			c.log.Printf("keepalive: %s's connection has a place in the fd store again", id)
		}
	}
	if back {
		c.keepaliveSave()
	}
	return wrote || back
}

// storeKeep puts the Shell's copy of an app's own connection into the fd
// store: its own descriptor, as an os.File's Fd would make the app's socket
// blocking (keepaliveDup).
func (c *Core) storeKeep(id string, k *keepConn) error {
	rc, err := k.conn.SyscallConn()
	if err != nil {
		return err
	}
	if cerr := rc.Control(func(fd uintptr) { err = sdNotifyFDs("FDSTORE=1\nFDNAME="+keepName(id, k.pid), int(fd)) }); cerr != nil {
		return cerr
	}
	return err
}

// keepaliveLastUse is when the app last used its own connection, as far as
// the Shell sees: it connected, data came on it, or the app was brought to
// the screen (now while it is there).
func (c *Core) keepaliveLastUse(id string) time.Time {
	if a := c.appByID(id); a != nil && a == c.fg {
		return c.now()
	}
	t := c.keep.conns[id].used
	if ms, ok := c.appUse().Opened[id]; ok && time.UnixMilli(ms).After(t) {
		t = time.UnixMilli(ms)
	}
	return t
}

// keepaliveForget: the app was uninstalled.
func (c *Core) keepaliveForget(a *App) {
	c.keepaliveEnd(a, "uninstalled")
	if _, used := c.keep.use[a.M.ID]; !used {
		return
	}
	delete(c.keep.use, a.M.ID)
	delete(c.keep.own, a.M.ID)
	delete(c.keep.data, a.M.ID)
	c.keepaliveSave()
}

func (c *Core) keepaliveUsed(id, kind string) {
	if c.keep.use == nil {
		c.keep.use = map[string][]keepUse{}
	}
	now := c.now()
	kept := c.keep.use[id][:0]
	for _, u := range c.keep.use[id] {
		if now.Sub(u.At) < keepaliveUseKept {
			kept = append(kept, u)
		}
	}
	c.keep.use[id] = append(kept, keepUse{At: now, Kind: kind})
}

// KeepaliveUse is what an app's own connection cost over the last day
// (Settings › Battery, selftest); nil for an app that had none.
func (c *Core) KeepaliveUse(id string) *ui.KeepaliveUse {
	uses, ok := c.keep.use[id]
	if !ok {
		if _, open := c.keep.conns[id]; !open {
			return nil
		}
	}
	u := &ui.KeepaliveUse{}
	now := c.now()
	for _, x := range uses {
		if now.Sub(x.At) >= keepaliveUseKept {
			continue
		}
		switch x.Kind {
		case keepOwn:
			u.Own++
		case keepShared:
			u.Shared++
		case keepData:
			u.Data++
		}
	}
	u.MAh = math.Round(float64(u.Own+u.Data)*push.LTEWake.MAh()*10) / 10
	return u
}

// KeepaliveOpen: the app has its own connection (selftest).
func (c *Core) KeepaliveOpen(id string) bool { return c.keep.conns[id] != nil }

// keepaliveApps are the apps whose own connections the Battery page lists.
func (c *Core) keepaliveApps() []string {
	seen := map[string]bool{}
	var out []string
	for id := range c.keep.use {
		seen[id] = true
		out = append(out, id)
	}
	for id := range c.keep.conns {
		if !seen[id] {
			out = append(out, id)
		}
	}
	return out
}

func (c *Core) keepalivePath() string {
	return filepath.Join(c.cfg.StateDir, "shell", "keepalive.json")
}

func (c *Core) keepaliveSave() {
	st := keepSaved{Use: c.keep.use, PushEvery: int(c.keep.pushEvery / time.Second), LastPush: c.keep.lastPush}
	for id, k := range c.keep.conns {
		if st.Conns == nil {
			st.Conns = map[string]keepLeft{}
		}
		st.Conns[id] = keepLeft{Pid: k.pid, Addr: k.addr, EverySec: int(k.beat.Every / time.Second), Last: k.beat.Last, Used: k.used, Full: k.full}
	}
	raw, _ := json.Marshal(st)
	_ = statefile.Write(c.keepalivePath(), raw, 0o600)
	c.keep.savedEvery, c.keep.savedPush = c.keep.pushEvery, c.keep.lastPush
}

// loadKeepalive takes up what the previous Shell left: the last day's
// wakes (the hour's budgets are the own and data ones among them), and
// its connections, for Adopt.
func (c *Core) loadKeepalive() {
	var st keepSaved
	if ok, _ := readState(c.log.Printf, c.keepalivePath(), &st); !ok {
		return
	}
	now := c.now()
	for id, uses := range st.Use {
		var kept []keepUse
		for _, u := range uses {
			if now.Sub(u.At) >= keepaliveUseKept {
				continue
			}
			kept = append(kept, u)
			switch u.Kind {
			case keepOwn:
				c.keep.budget(&c.keep.own, id, keepaliveOwnPerHour).Take(u.At)
			case keepData:
				c.keep.budget(&c.keep.data, id, keepaliveDataPerHour).Take(u.At)
			}
		}
		if kept != nil {
			if c.keep.use == nil {
				c.keep.use = map[string][]keepUse{}
			}
			c.keep.use[id] = kept
		}
	}
	c.keep.left = st.Conns
	c.keep.pushEvery = time.Duration(st.PushEvery) * time.Second
	c.keep.savedEvery, c.keep.savedPush = c.keep.pushEvery, st.LastPush
	// Kept within push.KeepaliveSlack only while there were own
	// connections (keepalivePushed); else not known.
	if len(st.Conns) > 0 {
		c.keep.lastPush = st.LastPush
	}
}

// keepaliveAdopt takes over the apps' own connections the previous Shell
// passed on, for the apps Adopt took over, on the schedules it left. An
// app whose socket or schedule did not come along is told to connect
// again, and why; a socket whose app is gone is let go. A socket from the
// fd store is in it; one passed in place is where the schedule says.
func (c *Core) keepaliveAdopt(list []Inherited) {
	left := c.keep.left
	c.keep.left = nil
	if len(left) == 0 && !slices.ContainsFunc(list, func(in Inherited) bool { return in.Keep }) {
		return
	}
	if left == nil {
		left = map[string]keepLeft{}
	}
	ours := func(id string, pid int) *App {
		if a := c.appByID(id); a != nil && a.adopted && a.running && a.proc != nil && a.proc.Pid == pid {
			return a
		}
		return nil
	}
	unsaved := map[string]bool{}
	for _, in := range list {
		if !in.Keep {
			continue
		}
		l, saved := left[in.ID]
		a := ours(in.ID, in.Pid)
		if a != nil && saved && l.Pid == in.Pid && c.keep.conns[in.ID] == nil {
			conn, err := keepaliveConn(in.File)
			if err == nil {
				delete(left, in.ID)
				if c.keep.conns == nil {
					c.keep.conns = map[string]*keepConn{}
				}
				used := l.Used
				if used.IsZero() {
					used = l.Last
				}
				k := &keepConn{addr: l.Addr, conn: conn, pid: in.Pid, beat: push.Beat{Every: time.Duration(l.EverySec) * time.Second, Last: l.Last},
					arm: make(chan struct{}, 1), used: used, stored: in.Stored || !l.Full, full: l.Full && !in.Stored}
				c.keep.conns[in.ID] = k
				go c.watchKeepalive(in.ID, k)
				c.log.Printf("keepalive: took over %s's connection to %s, heartbeat every %s", in.ID, l.Addr, k.beat.Every)
				continue
			}
			c.log.Printf("keepalive: %s's connection: %v", in.ID, err)
		} else {
			in.File.Close()
		}
		if k := c.keep.conns[in.ID]; k == nil || k.pid != in.Pid {
			c.unstoreKeep(in.ID, in.Pid)
		}
		if a != nil && (!saved || l.Pid != in.Pid) {
			left[in.ID] = keepLeft{Pid: in.Pid}
			unsaved[in.ID] = true
		}
	}
	for id, l := range left {
		a := ours(id, l.Pid)
		if a == nil || c.keep.conns[id] != nil {
			continue
		}
		why, what := ui.KeepaliveLostDropped, "did not come along"
		switch {
		case unsaved[id]:
			why, what = ui.KeepaliveLostUnsaved, "came without its schedule"
		case l.Full:
			why, what = ui.KeepaliveLostFull, "had no room in the fd store"
		}
		c.log.Printf("keepalive: %s's connection %s; told the app to connect again (%s)", id, what, why)
		c.keepaliveLost(a, why)
	}
	if !c.keepaliveStore() {
		c.keepaliveSave()
	}
}

// keepaliveDup is a descriptor of conn without close-on-exec. Not
// conn.File(): its Fd makes the socket blocking, the app's copy too.
func keepaliveDup(conn *net.TCPConn) (int, error) {
	rc, err := conn.SyscallConn()
	if err != nil {
		return -1, err
	}
	fd := -1
	if cerr := rc.Control(func(s uintptr) { fd, err = unix.Dup(int(s)) }); cerr != nil {
		return -1, cerr
	}
	return fd, err
}

// keepaliveConn is the Shell's copy of a connection passed on, left in the
// blocking mode the app's copy (the same open file) is in: net.FileConn
// makes both non-blocking.
func keepaliveConn(f *os.File) (*net.TCPConn, error) {
	defer f.Close()
	rc, err := f.SyscallConn()
	if err != nil {
		return nil, err
	}
	flags := 0
	if cerr := rc.Control(func(fd uintptr) { flags, err = unix.FcntlInt(fd, unix.F_GETFL, 0) }); cerr != nil || err != nil {
		return nil, errors.Join(cerr, err)
	}
	nc, err := net.FileConn(f)
	if err != nil {
		return nil, err
	}
	conn, ok := nc.(*net.TCPConn)
	if !ok {
		nc.Close()
		return nil, errors.New("not a TCP connection")
	}
	if flags&unix.O_NONBLOCK == 0 {
		if rc, err := conn.SyscallConn(); err == nil {
			_ = rc.Control(func(fd uintptr) { _ = unix.SetNonblock(int(fd), false) })
		}
	}
	return conn, nil
}
