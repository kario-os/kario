package shell

import (
	"errors"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Reader mode for apps (ui.CapNFCRead): the system sheet asks for the
// card while phone-nfcd waits for one; the app gets what was read. An
// ISO-DEP card stays for the app's nfc.transceive while the sheet says it
// is being read, until the app closes the session, the user presses Back,
// the card goes or ui.NFCReaderHold is up (nfcd's reader.end event takes
// the sheet away). Only the app in front, as on Android and iOS: one that
// ends or leaves the foreground ends its session (checkNFC).

// readerOverlay is the sheet of one session.
type readerOverlay struct {
	app     *App
	session string
	// kept: a card is being read; stopped: the user ended it.
	kept, stopped bool
}

func (o *readerOverlay) lines() []string {
	if o.kept {
		return []string{tr.T("Keep the card on the phone"), tr.T("“%s” is reading it", o.app.M.Name)}
	}
	return []string{tr.T("Hold the card to the back of the phone"), tr.T("For “%s”", o.app.M.Name)}
}

func (o *readerOverlay) key(c *Core, k Key) {
	if k.Name == KeyEsc {
		c.stopReader(o)
	}
}

func (o *readerOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	lines := o.lines()
	h := len(lines) + 5
	y0 := (H - h) / 2
	can.Box(2, y0, W-4, h, st(colTrusted, colMenu), true)
	can.Text(4, y0+1, tr.T("Read an NFC card"), bold(st(colTrusted, colMenu)), W-8)
	for i, l := range lines {
		s := st(colFG, colMenu)
		if i == 0 {
			s = bold(s)
		}
		can.Text(4, y0+2+i, cv.Truncate(l, W-8), s, W-8)
	}
	can.Text(4, y0+h-2, tr.T("Esc stop"), st(colDim, colMenu), W-8)
}

func (o *readerOverlay) frame(c *Core) *FrameOverlay {
	return &FrameOverlay{Kind: "confirm", Title: tr.T("Read an NFC card"), Lines: o.lines(), Footer: []string{tr.T("Back stop")}}
}

// endReader takes the sheet away.
func (c *Core) endReader(o *readerOverlay) {
	c.popOverlay(o)
	if c.nfc.reader == o {
		c.nfc.reader = nil
	}
}

// stopReader is the user ending the session: nfcd ends the wait (which
// answers the app "cancelled") or lets the card go.
func (c *Core) stopReader(o *readerOverlay) {
	o.stopped = true
	c.endReader(o)
	if o.session != "" {
		args := svc.NFCReaderArgs{Session: o.session, App: o.app.M.ID}
		go func() { _ = c.nfcRPC(svc.NFCReaderClose, args, nil) }()
	}
}

func (c *Core) nfcReaderEnded(ev svc.NFCEvent) {
	if o := c.nfc.reader; o != nil && o.session == ev.Session {
		c.endReader(o)
	}
}

func (c *Core) nfcRead(a *App, args ui.NFCReadArgs, reply func(any, error)) {
	switch {
	case !c.nfc.up:
		reply(nil, errors.New("this phone has no NFC"))
		return
	case c.fg != a:
		reply(nil, errors.New("only the app in front can read NFC cards"))
		return
	case c.nfc.reader != nil:
		reply(nil, errors.New("a card is being read already"))
		return
	}
	secs := args.Seconds
	if secs <= 0 {
		secs = 20
	}
	o := &readerOverlay{app: a}
	c.nfc.reader = o
	c.pushOverlay(o)
	b := c.broker
	var open map[string]string
	b.call(svc.Nfcd, svc.NFCReaderOpen, svc.NFCReaderArgs{App: a.M.ID}, &open, func(err error) {
		switch {
		case err != nil:
			c.endReader(o)
			reply(nil, err)
			return
		case o.stopped:
			args := svc.NFCReaderArgs{Session: open["session"], App: a.M.ID}
			go func() { _ = c.nfcRPC(svc.NFCReaderClose, args, nil) }()
			reply(nil, errors.New(ui.ErrCancelled))
			return
		}
		o.session = open["session"]
		var tag ui.NFCTag
		b.call(svc.Nfcd, svc.NFCReaderWait, svc.NFCReaderArgs{Session: o.session, App: a.M.ID, Seconds: min(secs, 60)}, &tag, func(err error) {
			if err != nil || tag.Session == "" || o.stopped {
				c.endReader(o)
			} else {
				o.kept = true
			}
			c.log.Printf("nfc read for %s: iso-dep=%v session=%q err=%v", a.M.ID, tag.ISODep, tag.Session, err)
			reply(tag, err)
		})
	})
}

func (c *Core) nfcTransceive(a *App, args ui.NFCTransceiveArgs, reply func(any, error)) {
	o := c.nfc.reader
	if o == nil || o.app != a || o.session == "" || o.session != args.Session || !o.kept {
		reply(nil, errors.New("no reader session of this app is open (it ended)"))
		return
	}
	ra := svc.NFCReaderArgs{Session: args.Session, App: a.M.ID, APDU: args.APDU}
	go func() {
		var res ui.NFCTransceiveRes
		var err error
		if args.APDU != "" {
			err = c.nfcRPC(svc.NFCReaderExchange, ra, &res)
		}
		if args.Close {
			_ = c.nfcRPC(svc.NFCReaderClose, ra, nil)
		}
		c.Post(func() { reply(res, err) })
	}()
}

// NFCDeactivated is the last nfc.deactivated the shell delivered and how
// many it did (selftest).
func (c *Core) NFCDeactivated() (svc.NFCEvent, int) { return c.nfc.lastLeft, c.nfc.left }
