package smsd

import (
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
// keeps it and offers it again. On each connection smsd also takes over
// the store telephonyd kept before smsd existed.
type telLink struct {
	s    *service
	mu   sync.Mutex
	cl   *wire.Client
	up   atomic.Bool
	stop chan struct{} // closed: run returns (tests)
}

type telEvent struct {
	ID     int             `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Event  string          `json:"event"`
	Key    string          `json:"key"`
	SMS    *ui.SMS         `json:"sms"`
	CB     *telCB          `json:"cb"`
}

type telCB struct {
	ID      string `json:"id"`
	Channel int    `json:"channel"`
	Serial  int    `json:"serial"`
	Text    string `json:"text"`
	At      int64  `json:"at"`
}

const pendingReq = 2

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
	if conn.Send(wire.RPC{ID: 1, Method: wire.Subscribe}) != nil || conn.Send(wire.RPC{ID: pendingReq, Method: svc.TelSMSPending}) != nil {
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
		if ev.ID != pendingReq {
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

// take stores what an event carries and acknowledges it; what could not
// be stored stays with telephonyd. Events without a key are not smsd's.
func (t *telLink) take(cl *wire.Client, ev telEvent) {
	if ev.Key == "" {
		return
	}
	var err error
	switch {
	case ev.Event == svc.TelEventSMS && ev.SMS != nil && ev.SMS.Dir == "in":
		err = t.s.received(*ev.SMS)
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
	go func() {
		if err := cl.Call(svc.TelSMSAck, map[string][]string{"keys": {ev.Key}}, nil); err != nil {
			log.Printf("acknowledging %s: %v", ev.Key, err)
		}
	}()
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

func (t *telLink) call(method string, params, out any) error {
	t.mu.Lock()
	cl := t.cl
	t.mu.Unlock()
	if cl == nil {
		return errors.New("no telephony service")
	}
	return cl.Call(method, params, out)
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
	s.mu.Lock()
	a, err := s.st.addAlert(ui.SMSAlert{ID: cb.ID, Level: alertLevel(cb.Channel), Channel: cb.Channel, Text: cb.Text, At: cb.At}, cb.Serial)
	var out ui.SMSAlert
	if a != nil {
		out = *a
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if a != nil {
		s.emit(svc.SMSEvent{Event: svc.SMSEventAlert, Alert: &out})
	}
	return nil
}
