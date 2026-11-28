package shell

import "time"

// How the Shell ends an app that broke no rule: it asks first, closing its
// side of the app's connection, and kills it only if it is still running
// once the grace is up. Every runtime takes the end of the connection as
// the signal to go: kario-jsrt and the Node engine once the storage write
// under way is on disk, the Go SDK's Run once the messages it read are
// handled, the mini-program host once its runtimes have saved theirs.
// Killed at once, a runtime loses a save it had already acknowledged (the
// file keeps its last whole version). SIGTERM is not the ask: a Go app
// that does not catch it dies at once, and as the first process of its pid
// namespace on the device kario-jsrt cannot end itself with it. A few ends
// do not ask (kill).
const (
	// endGrace: updated, uninstalled, restarted under a new setting or
	// language, stopped by the user. Nothing waits for its memory: as long
	// as at shutdown.
	endGrace = stopGrace
	// shortGrace: the low-memory killer, which judges again only after
	// lowMemSettled anyway, and an app the user closes for not responding
	// or for its background CPU, whose loop may never read the end. Enough
	// for slow flash to sync a store of 1 MB.
	shortGrace = time.Second
)

// endApp asks a running app to end and kills it if it has not ended within
// grace, without holding up the loop. A frozen app is thawed first, as it
// would not see the end. What it still sends is read (the state it hands
// over), nothing more is sent to it (it is no longer ready), and asked
// again its first deadline stands.
func (c *Core) endApp(a *App, grace time.Duration) {
	if !a.running || a.proc == nil || a.killed {
		return
	}
	a.killed, a.ready = true, false
	c.thaw(a)
	if a.conn != nil {
		_ = a.conn.CloseWrite()
	}
	if c.ends == nil {
		c.ends = map[string]*endRecord{}
	}
	rec := &endRecord{}
	c.ends[a.M.ID] = rec
	p := a.proc
	time.AfterFunc(grace, func() {
		c.Post(func() {
			if a.proc == p && a.running {
				c.log.Printf("%s: still running %s after it was asked to end; killing it", a.M.ID, grace)
				rec.killed = true
				c.kill(a)
			}
		})
	})
}

// endRecord is how the last end the Shell asked of an app went.
type endRecord struct{ exited, killed bool }

// appEnded is appExited's part for apps asked to end.
func (c *Core) appEnded(a *App) {
	if rec := c.ends[a.M.ID]; rec != nil {
		rec.exited = true
	}
	c.wakeAfterEnd(a)
}

// EndedItself: the app the Shell last asked to end has exited (ended),
// and by itself, not killed once its grace was up (selftest).
func (c *Core) EndedItself(id string) (ended, itself bool) {
	rec := c.ends[id]
	return rec != nil && rec.exited, rec != nil && !rec.killed
}

// wakeAfterEnd starts again in the background an app that was sent a push
// or a job while it was ending (deliverBackground), unless it opens on the
// screen at once (relaunched) or is no longer installed.
func (c *Core) wakeAfterEnd(a *App) {
	budget := a.wakeAfter
	a.wakeAfter = 0
	if budget == 0 || len(a.pushQ) == 0 || (a.relaunch && c.fg == a) || c.appByID(a.M.ID) != a {
		return
	}
	last := a.pushQ[len(a.pushQ)-1]
	a.pushQ = a.pushQ[:len(a.pushQ)-1]
	c.deliverBackground(a, last, budget, true, "held while it ended")
}
