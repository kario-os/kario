package shell

import (
	"strconv"
	"time"

	"github.com/kario-os/kario/ui"
)

// ANR ("application not responding", as on Android): after the user acts in
// the foreground app the shell sends it a ping, which the SDK answers from
// the loop that runs the app's handlers. An app still silent after ANRAfter
// is stuck (an endless loop, a blocking call in a handler), and the system,
// not the app, offers to close it or to wait. An app waiting for one of its
// capability requests (a consent dialog the user is reading) is not stuck.
// Background and frozen apps are not probed: nobody is waiting for them.

const defaultANRAfter = 5 * time.Second

func (c *Core) anrAfter() time.Duration {
	if c.cfg.ANRAfter > 0 {
		return c.cfg.ANRAfter
	}
	return defaultANRAfter
}

// probe pings the app unless a ping is already out.
func (c *Core) probe(a *App) {
	if !a.running || !a.ready || !a.ping.sent.IsZero() {
		return
	}
	a.ping.seq++
	a.ping.sent = c.now()
	a.send(ui.Msg{T: ui.MsgPing, Value: strconv.Itoa(a.ping.seq)})
}

func (c *Core) pong(a *App, v string) {
	if v != strconv.Itoa(a.ping.seq) {
		return
	}
	a.ping.sent = time.Time{}
	for _, o := range c.overlays {
		if o, ok := o.(*anrOverlay); ok && o.app == a {
			c.popOverlay(o)
			c.log.Printf("%s answers again", a.M.ID)
		}
	}
}

type anrOverlay struct {
	confirmOverlay
	app *App
}

func (o *anrOverlay) key(c *Core, k Key) {
	switch k.Name {
	case KeyEnter:
		c.popOverlay(o)
		o.onYes()
	case KeyEsc:
		c.popOverlay(o)
		o.onNo()
	}
}

func (c *Core) checkANR() {
	a := c.fg
	if a == nil || a.ping.sent.IsZero() {
		return
	}
	if a.waiting > 0 {
		// Waiting on the system (a dialog the user has not answered), not stuck.
		a.ping.sent = c.now()
		return
	}
	if c.now().Sub(a.ping.sent) < c.anrAfter() {
		return
	}
	for _, o := range c.overlays {
		if _, ok := o.(*anrOverlay); ok {
			return
		}
	}
	c.log.Printf("%s has not answered for %s (ANR)", a.M.ID, c.now().Sub(a.ping.sent).Round(time.Millisecond))
	o := &anrOverlay{app: a}
	o.confirmOverlay = confirmOverlay{
		text: tr.T("“%s” is not responding\nClose it, or wait a little longer?", a.M.Name), yes: tr.T("Close app"), no: tr.T("Wait"),
		onYes: func() {
			c.log.Printf("%s closed by the user after an ANR", a.M.ID)
			c.kill(a)
		},
		// Waiting starts the clock again; the same ping is still out.
		onNo: func() { a.ping.sent = c.now() },
	}
	c.pushOverlay(o)
}
