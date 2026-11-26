package nfcd

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kario-os/kario/hal/nfc"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Reader mode, one use at a time: the card pack reading a door card to
// copy (readDoor, app ""), or an app's session (ui.CapNFCRead), which
// waits for a card and keeps an ISO-DEP one for the app's commands, where
// the controller can (nfc.Exchanger), until the app or the user closes
// it, the card goes, or ui.NFCReaderHold is up. Either stops at once when
// cancelled while it waits.

var errReaderBusy = errors.New("the NFC reader is in use")

// readerHold is how long a session keeps its card (tests shorten it).
var readerHold = ui.NFCReaderHold * time.Second

type readerSession struct {
	id, app string
	ctx     context.Context
	cancel  context.CancelFunc
	// kept: a card is activated for the app's commands until timer.
	kept  bool
	timer *time.Timer
}

func (e *engine) readerBegin(app string) (*readerSession, error) {
	if e.reader != nil {
		return nil, errReaderBusy
	}
	e.seq++
	ctx, cancel := context.WithCancel(context.Background())
	r := &readerSession{id: fmt.Sprintf("r%d", e.seq), app: app, ctx: ctx, cancel: cancel}
	e.reader = r
	return r, nil
}

// readerEnd ends r if it is still the one in use; its app hears why.
func (e *engine) readerEnd(r *readerSession, why string) {
	if e.reader != r {
		return
	}
	e.reader = nil
	r.cancel()
	if r.timer != nil {
		r.timer.Stop()
	}
	if x, ok := e.ctl.(nfc.Exchanger); ok {
		x.Release()
	}
	if r.app != "" {
		e.emit(svc.NFCEvent{Event: svc.NFCEventReaderEnd, Session: r.id, App: r.app, Reason: why})
	}
}

// cancelRead stops the card pack's read while it waits.
func (e *engine) cancelRead() {
	if r := e.reader; r != nil && r.app == "" {
		r.cancel()
	}
}

func (e *engine) session(id, app string) *readerSession {
	if r := e.reader; r != nil && app != "" && r.id == id && r.app == app {
		return r
	}
	return nil
}

func (e *engine) readerOpen(app string) (map[string]string, error) {
	if app == "" {
		return nil, errors.New("a reader session is an app's")
	}
	r, err := e.readerBegin(app)
	if err != nil {
		return nil, err
	}
	return map[string]string{"session": r.id}, nil
}

// readerWait waits, outside the engine's lock, for a card on the phone.
func (e *engine) readerWait(a svc.NFCReaderArgs) (ui.NFCTag, error) {
	e.mu.Lock()
	r := e.session(a.Session, a.App)
	e.mu.Unlock()
	if r == nil || r.kept {
		return ui.NFCTag{}, errors.New(ui.ErrCancelled)
	}
	ctx, cancel := context.WithTimeout(r.ctx, time.Duration(min(max(a.Seconds, 1), 60))*time.Second)
	t, err := e.ctl.Read(ctx)
	cancel()
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case e.reader != r || r.ctx.Err() != nil:
		e.readerEnd(r, "closed")
		return ui.NFCTag{}, errors.New(ui.ErrCancelled)
	case err != nil || !uidSize(len(t.UID)):
		e.readerEnd(r, "done")
		return ui.NFCTag{}, errors.New(ui.ErrNFCNoCard)
	}
	tag := ui.NFCTag{UID: strings.ToUpper(hex.EncodeToString(t.UID)), ATQA: strings.ToUpper(hex.EncodeToString(t.ATQA[:])),
		SAK: fmt.Sprintf("%02X", t.SAK), ISODep: t.ISODep}
	if _, ok := e.ctl.(nfc.Exchanger); !ok || !t.ISODep {
		e.readerEnd(r, "done")
		return tag, nil
	}
	r.kept = true
	r.timer = time.AfterFunc(readerHold, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.readerEnd(r, "timeout")
	})
	tag.Session, tag.Until = r.id, e.now().Add(readerHold).UnixMilli()
	return tag, nil
}

// readerExchange sends the app's command to the card it keeps.
func (e *engine) readerExchange(a svc.NFCReaderArgs) (ui.NFCTransceiveRes, error) {
	cmd, err := hex.DecodeString(a.APDU)
	if err != nil || len(cmd) < 4 || len(cmd) > 4096 {
		return ui.NFCTransceiveRes{}, errors.New("a command of 4 to 4096 bytes in hex")
	}
	e.mu.Lock()
	r := e.session(a.Session, a.App)
	x, ok := e.ctl.(nfc.Exchanger)
	e.mu.Unlock()
	if r == nil || !r.kept || !ok {
		return ui.NFCTransceiveRes{}, errors.New("this reader session has no card (it ended)")
	}
	resp, err := x.Exchange(cmd)
	if err != nil {
		e.mu.Lock()
		e.readerEnd(r, "lost")
		e.mu.Unlock()
		return ui.NFCTransceiveRes{}, errors.New("the card was taken away")
	}
	return ui.NFCTransceiveRes{APDU: strings.ToUpper(hex.EncodeToString(resp))}, nil
}

func (e *engine) readerClose(a svc.NFCReaderArgs) {
	if r := e.session(a.Session, a.App); r != nil {
		e.readerEnd(r, "closed")
	}
}
