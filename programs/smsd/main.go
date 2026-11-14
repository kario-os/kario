// phone-smsd keeps the messages: received and sent SMS, their
// conversations, read and unread, the block list (a blocked sender's
// messages go to a folder of their own, without a notification), outgoing
// messages' delivery, verification codes found in them, and the emergency
// alerts (cell broadcasts) received. telephonyd stays the radio side: it
// sends, receives and holds each received message until smsd has stored it
// and acknowledged it (link.go). Only the shell talks to it
// (32-messaging.md).
//
//	phone-smsd --run-dir DIR --state-dir DIR
package smsd

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// maxParts: the longest message smsd sends, in SMS parts.
const maxParts = 10

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	runDir := flag.String("run-dir", "/run/phone", "directory for service sockets")
	stateDir := flag.String("state-dir", "/var/lib/phone", "persistent state")
	flag.Parse()
	log.SetPrefix("phone-smsd: ")
	if err := os.MkdirAll(*runDir, 0o755); err != nil {
		log.Fatal(err)
	}
	st, err := openStore(filepath.Join(*stateDir, "smsd"))
	if err != nil {
		log.Fatal(err)
	}
	var hub atomic.Pointer[wire.Hub]
	s := newService(st, func(ev svc.SMSEvent) {
		if h := hub.Load(); h != nil {
			h.Broadcast(ev)
		}
	})
	sock := filepath.Join(*runDir, svc.SmsdSock)
	h, err := wire.ServeHub(sock, s.handle)
	if err != nil {
		log.Fatal(err)
	}
	hub.Store(h)
	log.Printf("%d messages, %d alerts on %s", len(st.msgs), len(st.alerts), sock)
	go s.sweep()
	s.tel.run(filepath.Join(*runDir, svc.TelephonydSock))
}

type service struct {
	mu   sync.Mutex
	st   *store
	emit func(svc.SMSEvent)
	tel  *telLink
}

func newService(st *store, emit func(svc.SMSEvent)) *service {
	s := &service{st: st, emit: emit}
	s.tel = &telLink{s: s}
	return s
}

func (s *service) handle(method string, params json.RawMessage) (any, error) {
	switch method {
	case svc.SMSSend:
		var a struct{ To, Text, ID string }
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		return s.send(a.To, a.Text, a.ID)
	case svc.SMSState:
		s.mu.Lock()
		defer s.mu.Unlock()
		return svc.SMSStateRes{Pid: os.Getpid(), Messages: len(s.st.msgs), Alerts: len(s.st.alerts), Telephony: s.tel.up.Load()}, nil
	}
	var list ui.SMSListArgs
	var del ui.SMSDeleteArgs
	var blk ui.SMSBlockArgs
	for _, v := range []any{&list, &del, &blk} {
		_ = json.Unmarshal(params, v)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch method {
	case svc.SMSList:
		return s.st.list(list), nil
	case svc.SMSThreads:
		return s.st.threads(list.Folder), nil
	case svc.SMSAlerts:
		return s.st.alertList(), nil
	case svc.SMSBlocked:
		return s.st.blockedList(), nil
	case svc.SMSUnnotified:
		msgs, alerts := s.st.unnotified()
		return svc.SMSUnnotifiedRes{Messages: msgs, Alerts: alerts}, nil
	case svc.SMSNotified:
		var a struct{ IDs []string }
		_ = json.Unmarshal(params, &a)
		return map[string]bool{"ok": true}, s.st.notified(a.IDs)
	case svc.SMSRead:
		n, err := s.st.markRead(list.Peer)
		if n > 0 {
			s.emit(svc.SMSEvent{Event: svc.SMSEventChanged})
		}
		return map[string]int{"read": n}, err
	case svc.SMSDelete:
		n, err := s.st.remove(del.IDs, del.Peer)
		if n > 0 {
			s.emit(svc.SMSEvent{Event: svc.SMSEventChanged})
		}
		return map[string]int{"deleted": n}, err
	case svc.SMSBlock:
		if err := s.st.block(blk.Number, blk.On); err != nil {
			return nil, err
		}
		s.emit(svc.SMSEvent{Event: svc.SMSEventChanged})
		return s.st.blockedList(), nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

// send stores the message as sending, then hands it to telephonyd, whose
// events settle it; id sends a failed one again.
func (s *service) send(to, text, id string) (ui.SMS, error) {
	s.mu.Lock()
	var m *ui.SMS
	var err error
	if id != "" {
		old := s.st.byID[id]
		switch {
		case old == nil || old.Dir != "out":
			err = errors.New("no such message")
		case old.State != ui.SMSFailed:
			err = errors.New("only a message that was not sent can be sent again")
		default:
			to, text = old.Peer, old.Text
			m, err = s.st.setState(id, ui.SMSSending, true)
		}
	} else if err = checkMessage(to, text); err == nil {
		m, err = s.st.addOutgoing(strings.TrimSpace(to), text)
	}
	if err != nil {
		s.mu.Unlock()
		return ui.SMS{}, err
	}
	out := *m
	s.mu.Unlock()
	s.emit(svc.SMSEvent{Event: svc.SMSEventMessage, SMS: &out})
	if err := s.tel.call(svc.TelSMSSend, map[string]string{"to": to, "text": text, "id": out.ID}, nil); err != nil {
		_ = s.settle(out.ID, ui.SMSFailed)
		out.State = ui.SMSFailed
		return out, err
	}
	return out, nil
}

func checkMessage(to, text string) error {
	to = strings.TrimSpace(to)
	if to == "" || utf8.RuneCountInString(to) > 20 {
		return errors.New("enter a number")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("empty message")
	}
	if parts, _, _ := ui.SMSSegments(text); parts > maxParts {
		return fmt.Errorf("the message is too long (at most %d parts)", maxParts)
	}
	return nil
}

// settle records an outgoing message's state and tells the shell.
func (s *service) settle(id, state string) error {
	s.mu.Lock()
	m, err := s.st.setState(id, state, false)
	var out ui.SMS
	if m != nil {
		out = *m
	}
	s.mu.Unlock()
	if m != nil {
		s.emit(svc.SMSEvent{Event: svc.SMSEventMessage, SMS: &out})
	}
	return err
}

// sweep fails messages left sending by a telephonyd that went away.
func (s *service) sweep() {
	for {
		s.mu.Lock()
		ids := s.st.stale(s.st.now().Add(-3 * time.Minute).Unix())
		s.mu.Unlock()
		for _, id := range ids {
			log.Printf("%s: no outcome from telephonyd; not sent", id)
			if err := s.settle(id, ui.SMSFailed); err != nil {
				log.Print(err)
			}
		}
		time.Sleep(time.Minute)
	}
}
