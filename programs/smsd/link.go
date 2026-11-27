package smsd

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// telLink is smsd's side of telephonyd. It subscribes to telephonyd's
// events and asks for what is pending (what came while smsd was down, or
// before either side restarted). Each received message, cell broadcast and
// outgoing message's outcome is stored and synced before smsd acknowledges
// it; until then telephonyd (with ModemManager, the modem's own storage)
// keeps it and offers it again. A class 0 message is acknowledged once the
// shell showed it (flash.go). On each connection smsd also takes over the
// store telephonyd kept before smsd existed, and reads its status: this
// phone's number (group messages), the backend and the SIM's operator
// (the MMS access point, apn.go).
type telLink struct {
	s      *service
	mu     sync.Mutex
	cl     *wire.Client
	up     atomic.Bool
	status telStatus
	stop   chan struct{} // closed: run returns (tests)
}

type telEvent struct {
	ID     int             `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Event  string          `json:"event"`
	Key    string          `json:"key"`
	SMS    *telSMS         `json:"sms"`
	CB     *telCB          `json:"cb"`
	Status *telStatus      `json:"status"`
}

// telSMS is a message as telephonyd reports it: an 8-bit one's user data
// in hex (Data: a WAP push), and the message class when the network gave
// one (0: flash).
type telSMS struct {
	ui.SMS
	Data  string `json:"data,omitempty"`
	Class *int   `json:"class,omitempty"`
}

type telCB struct {
	ID      string `json:"id"`
	Channel int    `json:"channel"`
	Serial  int    `json:"serial"`
	Text    string `json:"text"`
	At      int64  `json:"at"`
	WAC     string `json:"wac,omitempty"` // warning area coordinates, hex
}

// telStatus is what smsd reads of telephonyd's status. SimMNC comes with
// telephonyd's APN database; without it the operator's name has to do.
type telStatus struct {
	Own      string `json:"own"`
	Backend  string `json:"backend"`
	Operator string `json:"operator"`
	SimMCC   string `json:"sim_mcc"`
	SimMNC   string `json:"sim_mnc"`
}

const (
	pendingReq = 2
	statusReq  = 3
)

func (t *telLink) run(sock string) {
	for {
		t.session(sock)
		select {
		case <-t.stop:
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (t *telLink) session(sock string) {
	nc, err := net.Dial("unix", sock)
	if err != nil {
		return
	}
	defer nc.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-t.stop:
			nc.Close()
		case <-done:
		}
	}()
	cl, err := wire.Dial(sock)
	if err != nil {
		return
	}
	t.mu.Lock()
	t.cl = cl
	t.mu.Unlock()
	t.up.Store(true)
	defer func() {
		t.up.Store(false)
		t.mu.Lock()
		t.cl = nil
		t.mu.Unlock()
		cl.Close()
	}()
	conn := wire.New(nc)
	if conn.Send(wire.RPC{ID: 1, Method: wire.Subscribe}) != nil || conn.Send(wire.RPC{ID: pendingReq, Method: svc.TelSMSPending}) != nil ||
		conn.Send(wire.RPC{ID: statusReq, Method: svc.TelStatus}) != nil {
		return
	}
	log.Print("connected to telephonyd")
	go t.takeOver(cl)
	for {
		var ev telEvent
		if err := conn.Recv(&ev); err != nil {
			log.Printf("telephonyd went away: %v", err)
			return
		}
		switch {
		case ev.ID == statusReq:
			var st telStatus
			if ev.OK && json.Unmarshal(ev.Result, &st) == nil {
				t.setStatus(st)
			}
			continue
		case ev.Event == svc.TelEventStatus && ev.Status != nil:
			t.setStatus(*ev.Status)
			continue
		case ev.ID != pendingReq:
			t.take(cl, ev)
			continue
		}
		var pending []telEvent
		if ev.OK {
			_ = json.Unmarshal(ev.Result, &pending)
		}
		if len(pending) > 0 {
			log.Printf("%d pending in telephonyd", len(pending))
		}
		for _, p := range pending {
			t.take(cl, p)
		}
	}
}

func (t *telLink) setStatus(st telStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status = st
}

func (t *telLink) statusNow() telStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

// take stores what an event carries and acknowledges it; what could not
// be stored stays with telephonyd. Events without a key are not smsd's.
func (t *telLink) take(cl *wire.Client, ev telEvent) {
	if ev.Key == "" {
		return
	}
	var err error
	switch {
	case ev.Event == svc.TelEventSMS && ev.SMS != nil && ev.SMS.Dir == "in":
		var hold bool
		hold, err = t.s.receivedTel(*ev.SMS, ev.Key)
		if hold && err == nil {
			return
		}
	case ev.Event == svc.TelEventSMS && ev.SMS != nil:
		err = t.s.settle(ev.SMS.ID, ev.SMS.State)
	case ev.Event == svc.TelEventCB && ev.CB != nil:
		err = t.s.broadcast(*ev.CB)
	default:
		return
	}
	if err != nil {
		log.Printf("%s not stored: %v", ev.Key, err)
		return
	}
	go t.ackWith(cl, ev.Key)
}

func (t *telLink) ackWith(cl *wire.Client, keys ...string) {
	if err := cl.Call(svc.TelSMSAck, map[string][]string{"keys": keys}, nil); err != nil {
		log.Printf("acknowledging %v: %v", keys, err)
	}
}

// ack lets telephonyd forget events smsd took later (class 0 messages the
// shell showed); without telephonyd it offers them again when it is back.
func (t *telLink) ack(keys ...string) {
	t.mu.Lock()
	cl := t.cl
	t.mu.Unlock()
	if cl != nil && len(keys) > 0 {
		go t.ackWith(cl, keys...)
	}
}

func (t *telLink) takeOver(cl *wire.Client) {
	var old []ui.SMS
	if err := cl.Call(svc.TelSMSLegacy, nil, &old); err != nil || len(old) == 0 {
		return
	}
	t.s.mu.Lock()
	n, err := t.s.st.importOld(old)
	t.s.mu.Unlock()
	if err != nil {
		log.Printf("taking over telephonyd's messages: %v", err)
		return
	}
	log.Printf("took over %d of telephonyd's %d stored messages", n, len(old))
	if n > 0 {
		t.s.emit(svc.SMSEvent{Event: svc.SMSEventChanged})
	}
	if err := cl.Call(svc.TelSMSLegacyDrop, nil, nil); err != nil {
		log.Printf("telephonyd keeps its old messages: %v", err)
	}
}

var errNoTel = errors.New("no telephony service")

func (t *telLink) call(method string, params, out any) error {
	return t.callTimeout(method, params, out, 0)
}

// callTimeout fails with wire.ErrTimeout after d (0: no deadline).
func (t *telLink) callTimeout(method string, params, out any, d time.Duration) error {
	t.mu.Lock()
	cl := t.cl
	t.mu.Unlock()
	if cl == nil {
		return errNoTel
	}
	return cl.CallTimeout(method, params, out, d)
}

// receivedTel takes a received message: an 8-bit one is a WAP push (an
// MMS notification or delivery report, a service indication), a class 0
// one is shown, not stored (hold: acknowledged later), any other one is
// stored.
func (s *service) receivedTel(m telSMS, key string) (hold bool, err error) {
	if m.Data != "" {
		raw, err := hex.DecodeString(m.Data)
		if err != nil || len(raw) > maxDataSMS {
			log.Printf("%s: unreadable data message from %s: dropped", m.ID, m.Peer)
			return false, nil
		}
		return false, s.receivedData(m.SMS, raw)
	}
	if m.Class != nil && *m.Class == 0 {
		return s.flashIn(m.SMS, key)
	}
	return false, s.received(m.SMS)
}

func (s *service) received(m ui.SMS) error {
	s.mu.Lock()
	stored, err := s.st.receive(m)
	var out ui.SMS
	if stored != nil {
		out = *stored
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if stored != nil {
		s.emit(svc.SMSEvent{Event: svc.SMSEventMessage, SMS: &out, New: out.Folder == ""})
	}
	return nil
}

func (s *service) broadcast(cb telCB) error {
	a := ui.SMSAlert{ID: cb.ID, Level: alertLevel(cb.Channel), Channel: cb.Channel, Text: cb.Text, At: cb.At}
	if cb.WAC != "" {
		raw, err := hex.DecodeString(cb.WAC)
		if err == nil {
			a.Area, _, err = ui.ParseWAC(raw)
		}
		if err != nil {
			// Shown everywhere rather than nowhere.
			log.Printf("alert %s: warning area unreadable (%v); shown wherever the phone is", cb.ID, err)
			a.Area = nil
		}
	}
	s.mu.Lock()
	stored, err := s.st.addAlert(a, cb.Serial)
	var out ui.SMSAlert
	if stored != nil {
		out = *stored
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if stored != nil {
		s.emit(svc.SMSEvent{Event: svc.SMSEventAlert, Alert: &out})
	}
	return nil
}
