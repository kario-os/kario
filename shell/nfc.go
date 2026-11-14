package shell

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// NFC through phone-nfcd (30-nfc.md). The shell tells nfcd what the phone
// is doing (locked, screen on, the app in front, its clock) and which
// cards apps declare; nfcd decides which card a reader sees. A reader's
// field keeps the phone from suspending and wakes the simulated sleep (on
// a phone the controller's interrupt wakes it). A reader's command for an
// app's card reaches the app as an "nfc.apdu" event, which wakes a frozen
// app or starts a stopped one in the background as push data does (a tap
// is the user's doing: no wake budget), and the app's nfc.reply goes
// back. The card pack is a system app (apps/cards, through nfc.state and
// nfc.set); the quick card view and the countdown are cards.go.

const nfcWakeBudget = 5 * time.Second

// appCards is the card pack.
const appCards = "com.phone.cards"

type nfcShell struct {
	up bool
	// sent: the policy last handed to the sender; applied: the last one
	// nfcd took; slept: the sleeps it was sent after.
	sent, applied string
	slept         int
	out           chan svc.NFCPolicyArgs
	state         ui.NFCState
	// field: a reader's field is on the phone now.
	field bool
	// last is the last result nfcd reported, results how many (selftest).
	last    svc.NFCEvent
	results int
	readers map[string]svc.NFCTapArgs
	bad     map[string]bool
	present *presentOverlay
	// The power key's last press, and the end key's (endStart: it may
	// start a double press); noPicker turns the end key's double press
	// off (selftest).
	powerAt, endAt     time.Time
	endStart, noPicker bool
	// The holiday calendar kept (holidays.go), the last try to fetch it
	// and whether one is under way.
	holidays      *holidayCache
	holidaysTried time.Time
	holidaysBusy  bool
	// reader is the reader sheet of an app's session (nfcread.go);
	// left counts the nfc.deactivated events delivered, lastLeft the last.
	reader   *readerOverlay
	left     int
	lastLeft svc.NFCEvent
}

func (c *Core) nfcSock() string { return filepath.Join(c.cfg.RunDir, svc.NfcdSock) }

// HasNFC: phone-nfcd is there (selftest's require-nfc).
func (c *Core) HasNFC() bool { return c.nfc.up }

func (c *Core) startNFC() {
	c.nfc.holidays = loadHolidays(c.holidaysPath())
	c.nfc.out = make(chan svc.NFCPolicyArgs, 1)
	go func() {
		for p := range c.nfc.out {
			raw, _ := json.Marshal(p)
			if err := c.nfcRPC(svc.NFCPolicy, p, nil); err == nil {
				c.Post(func() { c.nfc.applied = string(raw) })
			}
		}
	}()
	go func() {
		for !c.stopping.Load() {
			nc, err := net.Dial("unix", c.nfcSock())
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			conn := wire.New(nc)
			_ = conn.Send(wire.RPC{ID: 1, Method: wire.Subscribe})
			c.Post(func() {
				c.nfc.up, c.nfc.sent = true, ""
				c.syncNFC()
				c.refreshCards()
			})
			for {
				var ev svc.NFCEvent
				if err := conn.Recv(&ev); err != nil {
					break
				}
				switch ev.Event {
				case svc.NFCEventField:
					c.Post(func() { c.nfcField(ev.On) })
				case svc.NFCEventAPDU:
					c.Post(func() { c.nfcAPDU(ev) })
				case svc.NFCEventResult:
					c.Post(func() { c.nfcResult(ev) })
				case svc.NFCEventDeactivated:
					c.Post(func() { c.nfcDeactivated(ev) })
				case svc.NFCEventReaderEnd:
					c.Post(func() { c.nfcReaderEnded(ev) })
				}
			}
			nc.Close()
			c.Post(func() { c.nfc.up, c.nfc.field = false, false })
			time.Sleep(500 * time.Millisecond)
		}
	}()
}

func (c *Core) nfcRPC(method string, params, out any) error {
	if _, err := os.Stat(c.nfcSock()); err != nil {
		return errors.New("no NFC service")
	}
	return c.broker.rpc(svc.Nfcd, method, params, out)
}

func offeredLang(l string) bool {
	for _, x := range i18n.Languages() {
		if x.ID == l {
			return true
		}
	}
	return false
}

// nfcCardName is a card's description in the device's language.
func nfcCardName(m *Manifest, d ui.NFCCard) string {
	base := m.Lang
	if base == "" {
		base = i18n.DefaultLang
	}
	for _, l := range i18n.Chain(tr.Lang()) {
		if l == base {
			break
		}
		if s := d.Locales[l]; s != "" {
			return s
		}
	}
	return d.Description
}

func (c *Core) nfcPolicy() svc.NFCPolicyArgs {
	_, offset := c.now().Zone()
	p := svc.NFCPolicyArgs{Locked: c.locked(), ScreenOn: !c.screen.off, Cards: []svc.NFCAppCard{},
		ClockSkewMS: c.clockSkew.Milliseconds(), ZoneOffset: offset}
	p.DaysOff, p.Workdays = c.nfc.holidays.regionDays(c.region())
	if c.fg != nil {
		p.Foreground = c.fg.M.ID
	}
	for _, a := range c.apps {
		if len(a.M.NFC) == 0 || !a.M.Has(ui.PermNFC) {
			continue
		}
		if err := ui.CheckNFCCards(a.M.NFC, a.M.Permissions, offeredLang); err != nil {
			if c.nfc.bad == nil {
				c.nfc.bad = map[string]bool{}
			}
			if !c.nfc.bad[a.M.ID] {
				c.nfc.bad[a.M.ID] = true
				c.log.Printf("nfc: %s: %v; its cards are left out", a.M.ID, err)
			}
			continue
		}
		for _, d := range a.M.NFC {
			p.Cards = append(p.Cards, svc.NFCAppCard{App: a.M.ID, AppName: a.M.Name, Card: d.ID, Name: nfcCardName(a.M, d),
				Category: d.Category, AIDs: d.AIDs, RequiresUnlock: d.RequiresUnlock, Express: d.Express})
		}
	}
	return p
}

// syncNFC hands nfcd the policy when it changed, through one sender so
// that the last one wins.
func (c *Core) syncNFC() {
	if !c.nfc.up {
		return
	}
	p := c.nfcPolicy()
	raw, _ := json.Marshal(p)
	if string(raw) == c.nfc.sent {
		return
	}
	c.nfc.sent = string(raw)
	select {
	case <-c.nfc.out:
	default:
	}
	c.nfc.out <- p
}

// checkNFC runs with the housekeeping. After a sleep the policy goes
// again as it is: nfcd sets up what the controller answers as by itself
// for the time it is now (its own minute timer stood still meanwhile).
func (c *Core) checkNFC() {
	if n, _ := c.Sleeps(); n != c.nfc.slept {
		c.nfc.slept, c.nfc.sent = n, ""
	}
	c.syncNFC()
	c.checkPresent()
	c.checkHolidays()
	if o := c.nfc.reader; o != nil && (!o.app.running || c.fg != o.app) {
		c.stopReader(o)
	}
}

// nfcField: a reader's field came or went. Nothing lights the screen: a
// door card or the express transit card answers with it off.
func (c *Core) nfcField(on bool) {
	c.nfc.field = on
	if on {
		c.sleepWake("NFC field")
	}
}

func (c *Core) nfcAPDU(ev svc.NFCEvent) {
	a := c.appByID(ev.App)
	if a == nil || !a.M.Has(ui.PermNFC) {
		c.log.Printf("nfc %s: a command for %s, which has no cards", ev.Session, ev.App)
		return
	}
	raw, _ := json.Marshal(ui.NFCAPDU{Session: ev.Session, Card: ev.Card, AID: ev.AID, APDU: ev.APDU})
	c.deliverBackground(a, ui.Msg{T: ui.MsgEvent, Event: ui.EventNFCAPDU, Value: string(raw)}, nfcWakeBudget, true, "nfc "+ev.Session)
}

// nfcDeactivated tells an app the reader is done with its card. One that
// stopped meanwhile is not started for it.
func (c *Core) nfcDeactivated(ev svc.NFCEvent) {
	a := c.appByID(ev.App)
	if a == nil || !a.running || !a.M.Has(ui.PermNFC) {
		return
	}
	raw, _ := json.Marshal(ui.NFCDeactivated{Session: ev.Session, Card: ev.Card, Reason: ev.Reason})
	c.nfc.left, c.nfc.lastLeft = c.nfc.left+1, ev
	c.deliverBackground(a, ui.Msg{T: ui.MsgEvent, Event: ui.EventNFCDeactivated, Value: string(raw)}, time.Second, true, "nfc "+ev.Session)
}

// nfcResult tells the user how a tap went: nothing to say on success
// (the overlay shows it is done), the card the next tap presents after a
// refusal, or why the card the reader wanted stayed silent.
func (c *Core) nfcResult(ev svc.NFCEvent) {
	c.nfc.last = ev
	c.nfc.results++
	c.log.Printf("nfc: %s %q ok=%v next=%q reason=%q", ev.Card, ev.Name, ev.OK, ev.NextName, ev.Reason)
	o := c.nfc.present
	switch {
	case ev.OK && o != nil:
		o.done = time.Now()
	case ev.OK:
	case ev.Reason == "locked":
		c.Wake("nfc")
		c.showToast(tr.T("Unlock the phone to use “%s”", ev.Name))
	case ev.Reason == "screen":
		c.Wake("nfc")
		c.showToast(tr.T("Turn the screen on to use “%s”", ev.Name))
	case ev.Next != "":
		c.showToast(tr.T("Switched to “%s”: hold the phone to the reader again", ev.NextName))
		if o != nil {
			o.card, o.name, o.until = ev.Next, ev.NextName, time.Now().Add(presentFor)
		}
	default:
		c.showToast(tr.T("“%s” was not accepted", ev.Name))
	}
}

func (b *broker) dispatchNFC(a *App, m ui.Msg, reply func(any, error)) {
	c := b.c
	switch m.Cap {
	case ui.CapNFCReply:
		var args ui.NFCReplyArgs
		_ = json.Unmarshal(m.Args, &args)
		b.call(svc.Nfcd, svc.NFCReply, map[string]string{"session": args.Session, "app": a.M.ID, "apdu": args.APDU}, nil,
			func(err error) { reply(map[string]bool{"ok": err == nil}, err) })
	case ui.CapNFCRead:
		var args ui.NFCReadArgs
		_ = json.Unmarshal(m.Args, &args)
		c.nfcRead(a, args, reply)
	case ui.CapNFCTransceive:
		var args ui.NFCTransceiveArgs
		_ = json.Unmarshal(m.Args, &args)
		c.nfcTransceive(a, args, reply)
	case ui.CapNFCState:
		var st ui.NFCState
		b.call(svc.Nfcd, svc.NFCState, nil, &st, func(err error) { reply(st, err) })
	case ui.CapNFCSet:
		var args ui.NFCSetArgs
		_ = json.Unmarshal(m.Args, &args)
		switch args.Key {
		case ui.NFCSetPresent:
			c.presentCard(args.Card, func(err error) { reply(map[string]bool{"ok": err == nil}, err) })
			return
		case ui.NFCSetOpen:
			cards := c.appByID(appCards)
			if cards == nil {
				reply(nil, errors.New("no card pack on this phone"))
				return
			}
			c.Launch(cards, "")
			reply(map[string]bool{"ok": true}, nil)
			return
		}
		var st ui.NFCState
		b.call(svc.Nfcd, svc.NFCSet, args, &st, func(err error) {
			if err == nil {
				c.nfc.state = st
				c.nfc.state.Read = nil
			}
			reply(st, err)
		})
	}
}
