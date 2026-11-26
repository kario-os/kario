package shell

import (
	"errors"
	"time"

	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// sms.otp (12-super-app-capabilities.md §2): an app signing the user in
// asks the system for the verification code its server is about to text.
// The system waits for the next message with a code (phone-smsd finds
// codes, 32-messaging.md §6) and asks the user whether this app may have
// it, showing the app, the sender, the message and the code; only a yes
// hands the code over, nothing else of the messages. As Android's SMS User
// Consent API: no permission, one wait an app at a time (a new one
// replaces it), a message from before the wait does not count. The
// foreground app's wait is answered first.

type otpWait struct {
	app   *App
	conn  *wire.Conn
	reply func(any, error)
	timer *time.Timer
	seq   int
}

// live: the app is still the run that asked (a reply to a run that ended
// would go to its successor).
func (w *otpWait) live() bool { return w.app.running && w.app.conn == w.conn }

func (c *Core) smsOTPWait(a *App, args ui.SMSOTPArgs, reply func(any, error)) {
	secs := args.Seconds
	if secs <= 0 {
		secs = ui.SMSOTPWait
	}
	secs = min(secs, ui.SMSOTPMaxWait)
	if c.sms.otp == nil {
		c.sms.otp = map[string]*otpWait{}
	}
	if old := c.sms.otp[a.M.ID]; old != nil {
		old.timer.Stop()
		if old.live() {
			old.reply(nil, errors.New(ui.ErrCancelled))
		}
	}
	c.sms.otpSeq++
	w := &otpWait{app: a, conn: a.conn, reply: reply, seq: c.sms.otpSeq}
	w.timer = time.AfterFunc(time.Duration(secs)*time.Second, func() {
		c.Post(func() {
			if c.sms.otp[a.M.ID] != w {
				return
			}
			delete(c.sms.otp, a.M.ID)
			if w.live() {
				reply(nil, errors.New(tr.T("No verification code came")))
			}
		})
	})
	c.sms.otp[a.M.ID] = w
}

// smsOTPOffer offers a new message's code to the app waiting for one, if
// any: the foreground app, else the one waiting longest. Behind a PIN
// lock no code is shown; the wait goes on.
func (c *Core) smsOTPOffer(m ui.SMS) {
	if c.secureLocked() {
		return
	}
	var w *otpWait
	for id, x := range c.sms.otp {
		if !x.live() {
			x.timer.Stop()
			delete(c.sms.otp, id)
			continue
		}
		if w == nil || x.app == c.fg || (w.app != c.fg && x.seq < w.seq) {
			w = x
		}
	}
	if w == nil {
		return
	}
	w.timer.Stop()
	delete(c.sms.otp, w.app.M.ID)
	who := firstNonEmpty(m.Name, ui.NumberText(m.Peer))
	code, from := m.Code, m.Peer
	c.Wake("otp")
	c.pushOverlay(&confirmOverlay{
		text:  tr.T("“%s” asks for the verification code of this message:\n%s: %s\nGive it the code %s?", w.app.M.Name, who, firstLine(smsPreview(m), 80), code),
		yes:   tr.T("Give"),
		trust: true,
		onYes: func() {
			if w.live() {
				c.log.Printf("%s: verification code from %s handed over", w.app.M.ID, from)
				w.reply(ui.SMSOTP{Code: code, From: from}, nil)
			}
		},
		onNo: func() {
			if w.live() {
				w.reply(nil, errors.New(ui.ErrCancelled))
			}
		},
	})
}
