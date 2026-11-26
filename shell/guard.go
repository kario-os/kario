package shell

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
	"golang.org/x/sys/unix"
)

// What one app may cost the Shell (22-developer-api.md §8.1). An app over a
// limit is slowed down, refused or ended with a crash record naming the
// limit; the system's own screens and the other apps keep their share of the
// event loop, and the Shell's memory stays bounded whatever an app sends.
const (
	// Messages an app may send per second on average, and in a burst; past
	// that the Shell reads its socket more slowly, so the app's own writes
	// block (it slows only itself down).
	appMsgRate  = 250
	appMsgBurst = 500
	// An app's messages read but not yet handled by the event loop, in units
	// of appQueueUnit bytes (a message counts at least one): the loop's
	// queue cannot fill up with one app's messages, nor memory with its large
	// ones (at most appQueueUnits*appQueueUnit, or one message of up to
	// wire.MaxLine).
	appQueueUnits = 8
	appQueueUnit  = 64 << 10
	// An app's messages may take about 1/(1+appLoopShare) of the event
	// loop's time: once they have taken appLoopRest of it, the Shell rests
	// appLoopShare times as long before reading the app on.
	appLoopShare = 4
	appLoopRest  = time.Millisecond
	// Messages the Shell has queued for an app that does not read them: an
	// app that is not frozen is ended once the queue is full.
	appOutQueue = 256
	// Lines that are not JSON or do not fit the protocol's types are dropped;
	// at appBadMsgs in one run the app is ended. A line over wire.MaxLine
	// ends it at once: the framing is lost.
	appBadMsgs = 16
	// Capability requests of one app not answered yet; more are refused at
	// once with ui.ErrBusy. Requests that open a system dialog: one at a time.
	appMaxPending = 64
	appMaxDialogs = 1
	// An app whose connection closed has connGoneGrace to exit before it is
	// killed; once its process has exited, a child still holding the socket
	// no longer speaks for it.
	connGoneGrace = 5 * time.Second
	// Lines an app's messages may add to the Shell's log per appLogWindow.
	appLogBurst  = 20
	appLogWindow = 10 * time.Second
	// Toasts and notifications are a line or a few: longer text is cut
	// (every frame carries it while it shows).
	maxToastRunes    = 300
	maxNotifTitle    = 200
	maxNotifBody     = 2000
	maxNotifActions  = 8
	maxScreenActions = 64
)

// What one page may carry besides maxScreenNodes nodes: every frame carries
// the page on screen whole to the pixel renderer, and a doc node's Markdown
// is parsed on the event loop when the page arrives and laid out again for
// every frame (each about 0.5 ms a KB). Longer text is cut, further actions
// and canvases are left out.
const (
	maxNodeText       = 64 << 10
	maxDocText        = 64 << 10
	maxNodeField      = 4 << 10
	maxNodeActions    = 64
	maxScreenCanvases = 8
)

// capBytes cuts s to at most n bytes on a character boundary, ending in an
// ellipsis.
func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// limitContent applies the limits above to a page; false: nothing was cut.
func limitContent(s *ui.Screen) bool {
	cut := false
	field := func(p *string, n int) {
		if len(*p) > n {
			*p, cut = capBytes(*p, n), true
		}
	}
	actions := func(acts []ui.Action) []ui.Action {
		if len(acts) > maxNodeActions {
			acts, cut = acts[:maxNodeActions], true
		}
		for i := range acts {
			for _, p := range []*string{&acts[i].ID, &acts[i].Label, &acts[i].Key, &acts[i].Group, &acts[i].Confirm, &acts[i].Input} {
				field(p, maxNodeField)
			}
		}
		return acts
	}
	for _, p := range []*string{&s.ID, &s.Title, &s.Right, &s.Center, &s.Focus, &s.Layout} {
		field(p, maxNodeField)
	}
	s.Actions = actions(s.Actions)
	canvases := 0
	for i := range s.Body {
		n := &s.Body[i]
		text := maxNodeText
		if n.Kind == ui.KindDoc {
			text = maxDocText
		}
		field(&n.Text, text)
		field(&n.Value, maxNodeText)
		for _, p := range []*string{&n.Kind, &n.ID, &n.Style, &n.Title, &n.Subtitle, &n.Trailing, &n.Badge, &n.Icon,
			&n.Placeholder, &n.Submit, &n.Keyboard, &n.Src, &n.Stream, &n.Pip, &n.Label, &n.Role, &n.A11yLabel} {
			field(p, maxNodeField)
		}
		n.Actions = actions(n.Actions)
		if n.Canvas == nil {
			continue
		}
		if canvases++; canvases > maxScreenCanvases {
			n.Canvas, cut = nil, true
			continue
		}
		if len(n.Canvas.Ops) > ui.MaxCanvasOps {
			n.Canvas.Ops, cut = n.Canvas.Ops[:ui.MaxCanvasOps], true
		}
	}
	return cut
}

// capRunes cuts s to n runes, ending in an ellipsis.
func capRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// limitNotif keeps a notification within what the centre and a banner show.
func limitNotif(n *ui.Notification) {
	n.Title, n.Body = capRunes(n.Title, maxNotifTitle), capRunes(n.Body, maxNotifBody)
	if len(n.Actions) > maxNotifActions {
		n.Actions = n.Actions[:maxNotifActions]
	}
}

// dialogCaps open a system dialog for every request (appMaxDialogs).
var dialogCaps = map[string]bool{
	ui.CapAuthConfirm: true, ui.CapScanQR: true, ui.CapCameraCapture: true, ui.CapMediaPick: true,
	ui.CapFilePick: true, ui.CapFileSave: true, ui.CapMediaDelete: true, ui.CapShareOpen: true,
	ui.CapNFCRead: true, ui.CapWallpaperSet: true, ui.CapTelDial: true,
	ui.CapContactsSave: true, ui.CapContactsDelete: true, ui.CapContactsImport: true, ui.CapContactsExport: true,
	ui.CapContactsGroup: true, ui.CapContactsMerge: true, ui.CapContactsPick: true,
	ui.CapSMSBackup: true, ui.CapSMSRestore: true, ui.CapSMSVCard: true, ui.CapSMSOTP: true,
}

// appGuard is one run of an app as the limits see it (loop goroutine).
type appGuard struct {
	hello     bool
	cut       bool
	violation string
	bad       int
	dialogs   int
	unknown   map[string]bool
	dropped   int
	throttled int
	logSince  time.Time
	logLines  int
	logMuted  int
}

// rateGate is a token bucket: take says how long to wait before the next
// message may go on.
type rateGate struct {
	tokens float64
	last   time.Time
}

func (g *rateGate) take(now time.Time) time.Duration {
	g.tokens = min(appMsgBurst, g.tokens+now.Sub(g.last).Seconds()*appMsgRate)
	g.last = now
	if g.tokens >= 1 {
		g.tokens--
		return 0
	}
	wait := time.Duration((1 - g.tokens) / appMsgRate * float64(time.Second))
	g.tokens, g.last = 0, now.Add(wait)
	return wait
}

// malformed: the line was read whole but is not a message of the protocol.
func malformed(err error) bool {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	return errors.As(err, &syn) || errors.As(err, &typ) || errors.Is(err, errNotTurned)
}

// readApp hands an app's messages to the loop until its connection ends or
// it breaks the framing. ver is the older major an adopted app speaks;
// an app the Shell started names its own in hello, which the loop checks
// (speaks) as this reads on in it.
func (c *Core) readApp(a *App, conn *wire.Conn, ver *protoVersion) {
	units := make(chan struct{}, appQueueUnits)
	gate := rateGate{tokens: appMsgBurst, last: time.Now()}
	bad, slowed := 0, 0
	var reported time.Time
	var cost atomic.Int64
	negotiated := ver != nil
	for {
		var m ui.Msg
		err := recvApp(conn, ver, &m)
		if err == nil && m.T == ui.MsgHello && !negotiated {
			negotiated = true
			if v, ok := c.negotiate(m); ok && v.in != nil {
				ver = &v
			}
		}
		if err != nil && malformed(err) {
			bad++
			n := bad
			c.Post(func() { c.badMessage(a, conn, n, err) })
			if bad >= appBadMsgs {
				return
			}
			continue
		}
		if errors.Is(err, bufio.ErrTooLong) {
			c.Post(func() {
				c.violation(a, conn, fmt.Sprintf("protocol violation: a message over %d MB", wire.MaxLine>>20))
			})
		}
		if err != nil {
			return
		}
		if d := gate.take(time.Now()); d > 0 {
			if slowed++; time.Since(reported) > time.Second {
				n, first := slowed, reported.IsZero()
				slowed, reported = 0, time.Now()
				c.Post(func() { c.throttled(a, conn, n, first) })
			}
			time.Sleep(d)
		}
		if d := time.Duration(cost.Load()); d >= appLoopRest {
			cost.Add(-int64(d))
			a.heldUntil.Store(time.Now().Add(d * appLoopShare).UnixNano())
			time.Sleep(d * appLoopShare)
		}
		w := min(appQueueUnits, 1+conn.LastLen()/appQueueUnit)
		for range w {
			units <- struct{}{}
		}
		c.Post(func() {
			for range w {
				<-units
			}
			t0 := time.Now()
			c.appMsg(a, m)
			cost.Add(int64(time.Since(t0)))
			traceIn(a, m, t0, time.Since(t0))
		})
	}
}

func (c *Core) throttled(a *App, conn *wire.Conn, n int, first bool) {
	if a.conn != conn {
		return
	}
	a.guard.throttled += n
	if first {
		c.log.Printf("%s: over %d messages a second; reading it more slowly", a.M.ID, appMsgRate)
	}
}

func (c *Core) badMessage(a *App, conn *wire.Conn, n int, err error) {
	if a.conn != conn {
		return
	}
	a.guard.bad = n
	c.appLogf(a, "%s: malformed message dropped (%d of %d allowed): %v", a.M.ID, n, appBadMsgs-1, err)
	if n >= appBadMsgs {
		c.violation(a, conn, fmt.Sprintf("protocol violation: %d malformed messages (last: %v)", n, err))
	}
}

// violation ends an app that broke the protocol or a limit; the reason
// becomes its crash record (appExited).
func (c *Core) violation(a *App, conn *wire.Conn, reason string) {
	if a.conn != conn || a.guard.violation != "" || !a.running {
		return
	}
	a.guard.violation = reason
	c.log.Printf("%s: %s; ending it", a.M.ID, reason)
	c.kill(a)
}

// outboundFull: the app's queue is full because it does not read. A frozen
// app cannot; it is ended as the low-memory killer ends one (it keeps its
// tombstone) rather than miss what it was sent.
func (c *Core) outboundFull(a *App, conn *wire.Conn) {
	if a.conn != conn || !a.running {
		return
	}
	if a.guard.dropped++; a.guard.dropped > 1 {
		return
	}
	if a.debugging {
		c.log.Printf("%s: %d messages queued while paused in the debugger; dropping what comes", a.M.ID, appOutQueue)
		return
	}
	if a.frozen {
		c.log.Printf("%s: %d messages queued while frozen; ending it (tombstone)", a.M.ID, appOutQueue)
		a.ending = true
		c.endApp(a, endGrace)
		return
	}
	c.violation(a, conn, fmt.Sprintf("not reading its connection (%d messages queued)", appOutQueue))
}

// appLogf logs a line an app's messages caused, at most appLogBurst per
// appLogWindow for each app.
func (c *Core) appLogf(a *App, format string, args ...any) {
	g, now := &a.guard, time.Now()
	if now.Sub(g.logSince) > appLogWindow {
		if g.logMuted > 0 {
			c.log.Printf("%s: %d more lines not logged", a.M.ID, g.logMuted)
		}
		g.logSince, g.logLines, g.logMuted = now, 0, 0
	}
	if g.logLines >= appLogBurst {
		g.logMuted++
		return
	}
	g.logLines++
	c.log.Printf(format, args...)
}

// speaks checks hello's protocol version (protover.go): any minor version
// of a major the Shell speaks is accepted (minor versions only add), the
// newest the app offers; an app naming none is taken to speak the Shell's
// own; with no major in common the app is ended.
func (c *Core) speaks(a *App, m ui.Msg) bool {
	if m.Proto == "" && len(m.Protos) == 0 {
		c.appLogf(a, "%s: hello names no protocol version; taken as %s", a.M.ID, ui.ProtoName(c.versions()[0].major))
	}
	if v, ok := c.negotiate(m); ok {
		a.proto = nil
		if v.in != nil || v.out != nil {
			a.proto = &v
			c.log.Printf("%s speaks %s, an older major the Shell turns to and from its own: %s", a.M.ID, ui.ProtoName(v.major), strings.Join(v.changes, "; "))
			c.restoreConn(a)
		}
		return true
	}
	proto := strings.Join(append([]string{m.Proto}, m.Protos...), " ")
	proto = strings.TrimSpace(proto)
	if len(proto) > 32 {
		proto = proto[:32]
	}
	c.violation(a, a.conn, fmt.Sprintf("protocol version: the app speaks %q, the system %s", proto, c.spokenNames()))
	c.showToast(tr.T("“%s” does not work with this system version", a.M.Name))
	return false
}

// unknownMessage: a message type this Shell does not know (a newer app's)
// is ignored; each type is logged once per run.
func (c *Core) unknownMessage(a *App, t string) {
	if a.guard.unknown == nil {
		a.guard.unknown = map[string]bool{}
	}
	if a.guard.unknown[t] || len(a.guard.unknown) >= 8 {
		return
	}
	a.guard.unknown[t] = true
	if len(t) > 32 {
		t = t[:32]
	}
	c.log.Printf("%s: unknown message %q ignored", a.M.ID, t)
}

// endGroup waits until a child of the Shell has exited, without reaping it,
// and kills what it left running in its process group: while the child is
// not reaped its id, and so the group's, cannot be reused.
func endGroup(pid int) {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err == nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		if err != syscall.EINTR {
			return
		}
	}
}

// killTree ends a child of the Shell and what it started in its process
// group (the developer machine has no pid namespace or cgroup to take them
// along). The group is only signalled while the child is not reaped, so its
// id cannot have been reused.
func killTree(p *os.Process) {
	if p == nil {
		return
	}
	if p.Signal(syscall.Signal(0)) == nil {
		if pg, err := syscall.Getpgid(p.Pid); err == nil && pg == p.Pid {
			_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
		}
	}
	_ = p.Kill()
}
