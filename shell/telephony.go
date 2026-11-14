package shell

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// Telephony comes from phone-telephonyd (C++23, docs/04). The shell keeps one
// subscription open: network status feeds the status bar and an incoming
// call raises the system call screen whatever is in front. SMS reach the
// shell from phone-smsd, which takes them from telephonyd (messaging.go).
// Apps reach telephony only through the broker. Without telephonyd the
// shell runs on with "no service".

const (
	appDialer   = "com.phone.dialer"
	appMessages = "com.phone.messages"
)

type telStatus struct {
	Registered bool   `json:"registered"`
	Operator   string `json:"operator"`
	RAT        string `json:"rat"`
	Signal     int    `json:"signal"`
	Own        string `json:"own"`
	Sim        string `json:"sim"`       // ready | pin | puk | locked | absent | error
	SimTries   int    `json:"sim_tries"` // PIN / PUK attempts left
	PinLock    bool   `json:"pin_lock"`  // the SIM asks for its PIN at power-on
	IMEI       string `json:"imei"`      // "" while the baseband has not said
	// The radio (off: airplane mode), the data switch, the data link
	// (connected | idle | off) and its interfaces (airplane.go).
	Radio      bool     `json:"radio"`
	Data       bool     `json:"data"`
	DataState  string   `json:"data_state"`
	DataIfaces []string `json:"data_ifaces"`
}

type telEvent struct {
	ID     int             `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Event  string          `json:"event"`
	Call   *ui.TelCall     `json:"call"`
	Status *telStatus      `json:"status"`
	Err    string          `json:"err"`
	// A USSD event's: notify | input | done, and what the network says.
	State string `json:"state"`
	Text  string `json:"text"`
}

func (c *Core) telSock() string { return filepath.Join(c.cfg.RunDir, svc.TelephonydSock) }

// startTelephony subscribes to telephonyd, reconnecting if it restarts.
func (c *Core) startTelephony() {
	go func() {
		for !c.stopping.Load() {
			nc, err := net.Dial("unix", c.telSock())
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			conn := wire.New(nc)
			_ = conn.Send(wire.RPC{ID: 1, Method: wire.Subscribe})
			_ = conn.Send(wire.RPC{ID: 2, Method: "status"})
			for {
				var ev telEvent
				if err := conn.Recv(&ev); err != nil {
					break
				}
				c.Post(func() { c.telEvent(ev) })
			}
			nc.Close()
			c.Post(func() { c.tel = nil })
			time.Sleep(500 * time.Millisecond)
		}
	}()
}

func (c *Core) telEvent(ev telEvent) {
	switch {
	case ev.ID == 2 && ev.OK:
		var st telStatus
		if json.Unmarshal(ev.Result, &st) == nil {
			c.tel = &st
			c.simCheck()
			c.syncRadio()
		}
	case ev.Event == svc.TelEventStatus && ev.Status != nil:
		c.tel = ev.Status
		c.simCheck()
		c.applyDataCut()
	case ev.Event == svc.TelEventUnlock:
		c.simUnlocked(ev.OK)
	case ev.Event == svc.TelEventPin:
		c.simPinSet(ev.OK, ev.Err)
	case ev.Event == svc.TelEventCall && ev.Call != nil:
		c.cellCall(*ev.Call)
	case ev.Event == svc.TelEventUSSD:
		c.networkUSSD(ui.USSDReply{State: ev.State, Text: ev.Text})
	}
}

// networkUSSD is a USSD message the network started (a notice, or a
// question waiting for a reply): the dialer shows it, in front, as phones
// pop such messages up.
func (c *Core) networkUSSD(r ui.USSDReply) {
	d := c.appByID(appDialer)
	if d == nil {
		return
	}
	raw, _ := json.Marshal(r)
	if r.State != "done" {
		c.Launch(d, "")
	}
	c.deliver(d, ui.Msg{T: ui.MsgEvent, Event: ui.EventUSSD, Value: string(raw)})
}

func (c *Core) cellCall(call ui.TelCall) {
	c.emergencyRadio(call)
	call.Name = c.contactName(call.Number)
	if c.cells == nil {
		c.cells = map[string]ui.TelCall{}
	}
	c.cellSound(call)
	if call.State == "ended" {
		delete(c.cells, call.ID)
	} else {
		c.cells[call.ID] = call
	}
	for _, o := range c.overlays {
		if in, ok := o.(*cellIncomingOverlay); ok && in.call.ID == call.ID && call.State != "incoming" {
			c.popOverlay(o)
			break
		}
	}
	if call.Dir == "in" && call.State == "incoming" {
		c.pushOverlay(&cellIncomingOverlay{call: call, at: time.Now()})
	}
	dialer := c.appByID(appDialer)
	if call.State == "ended" && call.Dir == "in" && call.Reason == "missed" && dialer != nil {
		c.notifyAs(dialer, ui.Notification{ID: "missed:" + call.Number, Title: tr.T("Missed call"), Body: displayName(call)})
	}
	if dialer != nil && dialer.running {
		raw, _ := json.Marshal(call)
		dialer.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventTelCall, Value: string(raw)})
	}
}

// notifyAs posts a notification on behalf of a system app (the shell owns
// the telephony events, the app owns the notification).
func (c *Core) notifyAs(a *App, n ui.Notification) {
	for i, x := range c.notifs {
		if x.app == a && x.n.ID == n.ID {
			c.notifs = append(c.notifs[:i], c.notifs[i+1:]...)
			break
		}
	}
	c.notifs = append([]*notif{{app: a, n: n, at: time.Now(), seen: c.fg == a && !c.screen.off}}, c.notifs...)
	if c.fg != a {
		c.banner = c.notifBanner(a, n)
		c.ping()
	}
	c.notified()
}

func displayName(call ui.TelCall) string {
	if call.Name != "" {
		return call.Name + "  " + call.Number
	}
	return call.Number
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// telRPC is a one-off request to telephonyd (calls are rare; a fresh
// connection keeps the subscription stream free of replies).
func (c *Core) telRPC(method string, params, out any) error {
	if _, err := os.Stat(c.telSock()); err != nil {
		return errors.New("no telephony service")
	}
	cl, err := wire.Dial(c.telSock())
	if err != nil {
		return err
	}
	defer cl.Close()
	return cl.Call(method, params, out)
}

// HasTelephony reports whether telephonyd is connected (loop goroutine).
func (c *Core) HasTelephony() bool { return c.tel != nil }

// TelHook drives the simulated baseband from selftest scripts.
func (c *Core) TelHook(method string, params any) error {
	return c.telRPC(method, params, nil)
}

// activeCell is the connected cellular call, if any.
func (c *Core) activeCell() *ui.TelCall {
	for _, call := range c.cells {
		if call.State == "active" || call.State == "held" {
			return &call
		}
	}
	return nil
}

func (c *Core) cellOnScreen() bool {
	d := c.appByID(appDialer)
	if d == nil || c.fg != d {
		return false
	}
	v := d.top()
	return v != nil && v.S.ID == "call"
}

// cellIncomingOverlay is the system incoming-call screen for the SIM line;
// the dialer only gets the call once the user answered.
type cellIncomingOverlay struct {
	call ui.TelCall
	at   time.Time
}

func (o *cellIncomingOverlay) key(c *Core, k Key) {
	switch k.Name {
	case KeyEnter:
		c.popOverlay(o)
		id := o.call.ID
		go func() {
			err := c.telRPC(svc.TelAnswer, ui.CallIDArgs{CallID: id}, nil)
			c.Post(func() {
				if err != nil {
					c.showToast(tr.T("Answering failed: %v", err))
					return
				}
				if d := c.appByID(appDialer); d != nil {
					c.Launch(d, "call:"+id)
					d.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventTelOpen, Value: id})
				}
			})
		}()
	case KeyEsc:
		c.popOverlay(o)
		id := o.call.ID
		go func() { _ = c.telRPC(svc.TelHangup, ui.CallIDArgs{CallID: id}, nil) }()
	}
}

func (o *cellIncomingOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	bg := cv.RGB{R: 16, G: 40, B: 30}
	can.Fill(0, 1, W, H-1, st(colFG, bg))
	center := func(y int, s string, sty cv.Style) {
		can.Text((W-cv.StringWidth(s))/2, y, s, sty, W)
	}
	center(4, c.incomingLine(), st(colDim, bg))
	center(7, firstNonEmpty(o.call.Name, o.call.Number), bold(st(colFG, bg)))
	if o.call.Name != "" {
		center(9, o.call.Number, st(colDim, bg))
	}
	dots := []string{"●  ○  ○", "○  ●  ○", "○  ○  ●"}
	center(12, dots[int(time.Since(o.at).Milliseconds()/500)%3], st(colOK, bg))
	if n := o.ringNote(c); n != "" {
		center(14, n, st(colDim, bg))
	}
	center(H-4, tr.T("Enter answer        Esc decline"), bold(st(colFG, bg)))
}

// ringNote says why the phone is not ringing aloud.
func (o *cellIncomingOverlay) ringNote(c *Core) string {
	switch {
	case c.silenced["ring:"+o.call.ID]:
		return tr.T("Ringtone silenced")
	case c.settings.Silent:
		return tr.T("Silent mode · vibrate")
	}
	return ""
}

// incomingLine heads the incoming-call screen with the network it came on.
func (c *Core) incomingLine() string {
	op := tr.T("SIM card")
	if c.tel != nil && c.tel.Operator != "" {
		op = c.tel.Operator
	}
	return tr.T("Incoming call · %s", op)
}

func (o *cellIncomingOverlay) frame(c *Core) *FrameOverlay {
	lines := []string{c.incomingLine()}
	if o.call.Name != "" {
		lines = append(lines, o.call.Number)
	}
	if n := o.ringNote(c); n != "" {
		lines = append(lines, n)
	}
	return &FrameOverlay{Kind: "incoming", Title: firstNonEmpty(o.call.Name, o.call.Number), Lines: lines,
		Choices: []string{tr.T("Answer"), tr.T("Decline")}, Phase: float64(time.Since(o.at).Milliseconds()%1500) / 1500,
		Footer: []string{tr.T("OK answer · Back decline")}}
}

// Contacts are a system store (prototype: a JSON file); apps read them with
// the "contacts" runtime permission, the dialer and Messages also for names.
func (c *Core) contactsPath() string { return filepath.Join(c.cfg.StateDir, "shell", "contacts.json") }

// sampleContacts are a new phone's contacts (demo data).
//
//go:embed sample_contacts.json
var sampleContacts []byte

func (c *Core) contacts() []ui.Contact {
	if c.contactList == nil {
		raw, err := os.ReadFile(c.contactsPath())
		if err != nil || json.Unmarshal(raw, &c.contactList) != nil {
			c.contactList = nil
			_ = json.Unmarshal(sampleContacts, &c.contactList)
			raw, _ = json.MarshalIndent(c.contactList, "", "  ")
			_ = os.MkdirAll(filepath.Dir(c.contactsPath()), 0o700)
			_ = os.WriteFile(c.contactsPath(), raw, 0o600)
		}
	}
	return c.contactList
}

func (c *Core) contactName(number string) string {
	n := strings.TrimPrefix(number, "+86")
	for _, x := range c.contacts() {
		if x.Number == n {
			return x.Name
		}
	}
	return ""
}

func (c *Core) telRight() string {
	if c.settings.Airplane {
		return airplaneMark
	}
	if n := c.simNet(); n != "" {
		return n
	}
	if c.tel == nil || !c.tel.Registered {
		return tr.T("No service")
	}
	bars := []string{"▁", "▂", "▄", "▆", "█"}
	s := ""
	for i := 0; i < 4; i++ {
		if i < c.tel.Signal {
			s += bars[i+1]
		}
	}
	if net := c.dataNet(); net != "" {
		return net + " " + s
	}
	return s
}

// emergencyCall dials from the locked keypad: no unlocking, and the call
// screen is the dialer's, as for any call an app without the phone
// permission places.
func (c *Core) emergencyCall(number string) {
	if !c.HasTelephony() {
		c.showToast(tr.T("Cannot call %s: no phone service", number))
		return
	}
	c.log.Printf("emergency call %s from the locked keypad", number)
	var res map[string]string
	c.broker.call(svc.Telephonyd, svc.TelDial, ui.DialArgs{Number: number}, &res, func(err error) {
		if err != nil {
			c.showToast(tr.T("Emergency call failed: %v", err))
			return
		}
		if d := c.appByID(appDialer); d != nil {
			c.Launch(d, "call:"+res["call_id"])
		}
	})
}
