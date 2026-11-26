package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"runtime"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/push"
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
	// conn: the Shell's copy of the socket the app holds.
	conn *net.TCPConn
	beat push.Beat
	// arm asks the watcher to look for data (the app froze).
	arm chan struct{}
	// starved, overData: the log said the budget is used up.
	starved, overData bool
}

type keepUse struct {
	at   time.Time
	kind string
}

type keepState struct {
	conns map[string]*keepConn
	// own, data: each app's wakes over the last hour.
	own, data map[string]*push.WakeBudget
	use       map[string][]keepUse
	// pushEvery, lastPush: pushd's heartbeat interval, and the last line
	// on its gateway connection (by the Shell's clock).
	pushEvery time.Duration
	lastPush  time.Time
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
	case a.M.Runtime != "":
		reply(nil, errors.New("net.keepalive hands over a socket: native apps only (Go, C)"))
		return
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
				arm: make(chan struct{}, 1)}
			c.keep.conns[a.M.ID] = k
			go c.watchKeepalive(a.M.ID, k)
			c.log.Printf("keepalive: %s connected to %s, heartbeat every %s", a.M.ID, addr, k.beat.Every)
			reply(withFiles{data: ui.KeepaliveRes{EverySec: args.EverySec, PushSec: int(c.keep.pushEvery / time.Second), Addr: addr},
				files: []*os.File{f}}, nil)
		})
	}()
}

// dialAs connects to addr with a socket owned by uid (-1: the Shell's), as
// sendAs sends. No TCP keep-alive: only the system wakes the radio.
func dialAs(uid int, addr string) (*net.TCPConn, error) {
	d := net.Dialer{Timeout: keepaliveDialTimeout, KeepAlive: -1}
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

// keepaliveWindow: the radio is up for something else (why); every
// heartbeat whose window is open rides it.
func (c *Core) keepaliveWindow(why string) {
	now := c.now()
	for id, k := range c.keep.conns {
		a := c.appByID(id)
		if a == nil || !a.running || !k.beat.Rides(now) {
			continue
		}
		k.beat.Last = now
		if a != c.fg {
			c.keepaliveUsed(id, keepShared)
			c.log.Printf("keepalive: %s's heartbeat rides %s", id, why)
		}
		c.keepaliveWake(a, ui.KeepaliveBeat, keepaliveBeatWake)
	}
}

// pushWindow: a line came on pushd's gateway connection (its heartbeat
// every iv).
func (c *Core) pushWindow(iv time.Duration) {
	c.keep.pushEvery, c.keep.lastPush = iv, c.now()
	c.keepaliveWindow("the push heartbeat")
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

// keepaliveEnd lets the system's copy of the app's connection go.
func (c *Core) keepaliveEnd(a *App, why string) {
	k := c.keep.conns[a.M.ID]
	if k == nil {
		return
	}
	delete(c.keep.conns, a.M.ID)
	k.conn.Close()
	close(k.arm)
	c.log.Printf("keepalive: %s's connection to %s ended: %s", a.M.ID, k.addr, why)
}

// keepaliveForget: the app was uninstalled.
func (c *Core) keepaliveForget(a *App) {
	c.keepaliveEnd(a, "uninstalled")
	delete(c.keep.use, a.M.ID)
	delete(c.keep.own, a.M.ID)
	delete(c.keep.data, a.M.ID)
}

func (c *Core) keepaliveUsed(id, kind string) {
	if c.keep.use == nil {
		c.keep.use = map[string][]keepUse{}
	}
	now := c.now()
	kept := c.keep.use[id][:0]
	for _, u := range c.keep.use[id] {
		if now.Sub(u.at) < keepaliveUseKept {
			kept = append(kept, u)
		}
	}
	c.keep.use[id] = append(kept, keepUse{at: now, kind: kind})
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
		if now.Sub(x.at) >= keepaliveUseKept {
			continue
		}
		switch x.kind {
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
