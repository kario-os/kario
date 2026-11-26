// phone-smsd keeps the messages: received and sent SMS and multimedia
// messages (mms.go), their conversations (groups too), read and unread,
// the block list (a blocked sender's messages, and those holding a word of
// the list, go to a folder of their own, without a notification; the shell
// blocks calls by the same list), each conversation's draft, messages
// scheduled for later (schedule.go), outgoing messages' delivery,
// verification codes found in them, class 0
// messages until the shell showed them (flash.go), and the emergency
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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// maxParts: the longest message smsd sends, in SMS parts; maxImport: the
// most messages one import takes (the switch protocol's batch).
const (
	maxParts  = 10
	maxImport = 500
)

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
	go s.mmsLoop()
	go s.schedLoop()
	s.kickMMS()
	s.tel.run(filepath.Join(*runDir, svc.TelephonydSock))
}

type service struct {
	mu   sync.Mutex
	st   *store
	emit func(svc.SMSEvent)
	tel  *telLink
	// flash: class 0 messages held for the shell (flash.go); simMMSC: the
	// selftest's MMSC (svc.SMSMMSSim), ownMMSC the simulated baseband's
	// otherwise (apn.go); mmsKick wakes the downloader, schedKick the
	// scheduled messages' sender (schedule.go).
	flash     []*flashMsg
	simMMSC   string
	ownMMSC   string
	mmsKick   chan struct{}
	schedKick chan struct{}
	// The MMS exchanges asking for the MMS access point's link, and
	// whether telephonyd brought one up for them (apn.go).
	linkMu    sync.Mutex
	linkUsers int
	linkHeld  bool
}

func newService(st *store, emit func(svc.SMSEvent)) *service {
	s := &service{st: st, emit: emit, mmsKick: make(chan struct{}, 1), schedKick: make(chan struct{}, 1)}
	s.tel = &telLink{s: s}
	return s
}

func (s *service) handle(method string, params json.RawMessage) (any, error) {
	switch method {
	case svc.SMSSend:
		var a svc.SMSSendReq
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		return s.send(a)
	case svc.SMSState:
		s.mu.Lock()
		defer s.mu.Unlock()
		return svc.SMSStateRes{Pid: os.Getpid(), Messages: len(s.st.msgs), Alerts: len(s.st.alerts), Telephony: s.tel.up.Load(),
			MMSWait: s.st.waitingJobs(), Flash: len(s.flash)}, nil
	case svc.SMSFlashSave, svc.SMSDownload:
		var a ui.SMSIDArgs
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		if method == svc.SMSDownload {
			return map[string]bool{"ok": true}, s.redownload(a.ID)
		}
		return s.flashSave(a.ID)
	case svc.SMSPart:
		var a svc.SMSPartArgs
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		return s.part(a)
	case svc.SMSMMSSim:
		var a struct{ MMSC string }
		_ = json.Unmarshal(params, &a)
		if s.tel.statusNow().Backend != "sim" {
			return nil, errors.New("only with the simulated baseband")
		}
		s.mu.Lock()
		s.simMMSC = a.MMSC
		s.mu.Unlock()
		return map[string]bool{"ok": true}, nil
	case svc.SMSScheduleSim:
		if s.tel.statusNow().Backend != "sim" {
			return nil, errors.New("only with the simulated baseband")
		}
		return map[string]int{"sent": s.sendScheduled()}, nil
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
		return svc.SMSUnnotifiedRes{Messages: msgs, Alerts: alerts, Flash: s.flashWaiting()}, nil
	case svc.SMSNotified:
		var a struct{ IDs []string }
		_ = json.Unmarshal(params, &a)
		keys, rest := s.flashShown(a.IDs)
		s.tel.ack(keys...)
		return map[string]bool{"ok": true}, s.st.notified(rest)
	case svc.SMSPartMedia:
		var a svc.SMSPartArgs
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		if err := s.st.setPartMedia(a.ID, a.Index, a.Media); err != nil {
			return nil, err
		}
		s.emit(svc.SMSEvent{Event: svc.SMSEventChanged})
		return map[string]bool{"ok": true}, nil
	case svc.SMSRegion:
		var a struct{ Region string }
		_ = json.Unmarshal(params, &a)
		return map[string]string{"region": s.st.reg()}, s.st.setRegion(a.Region)
	case svc.SMSKnown:
		var a struct{ Numbers []string }
		_ = json.Unmarshal(params, &a)
		s.st.known = map[string]bool{}
		for _, n := range a.Numbers {
			if k := s.st.key(n); k != "" {
				s.st.known[k] = true
			}
		}
		return map[string]int{"known": len(s.st.known)}, nil
	case svc.SMSOnly:
		var a struct {
			On      bool
			Numbers []string
		}
		_ = json.Unmarshal(params, &a)
		return map[string]bool{"on": a.On}, s.st.setOnly(a.On, a.Numbers)
	case svc.SMSBlockList:
		return s.st.blockList(), nil
	case svc.SMSBlockSet:
		var a ui.BlockSetArgs
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		if err := s.st.setBlock(a); err != nil {
			return nil, err
		}
		s.emit(svc.SMSEvent{Event: svc.SMSEventChanged})
		return s.st.blockList(), nil
	case svc.SMSImport:
		var a svc.SMSImportArgs
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		if len(a.Messages) > maxImport {
			return nil, fmt.Errorf("at most %d messages an import", maxImport)
		}
		ids, err := s.st.importMessages(a.Messages)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(ids, func(id string) bool { return id != "" }) {
			s.emit(svc.SMSEvent{Event: svc.SMSEventChanged})
		}
		return svc.SMSImportRes{IDs: ids}, nil
	case svc.SMSDeleteImported:
		n, err := s.st.removeImported(del.IDs)
		if n > 0 {
			s.emit(svc.SMSEvent{Event: svc.SMSEventChanged})
		}
		return map[string]int{"deleted": n}, err
	case svc.SMSDraft:
		var a ui.SMSDraftArgs
		if err := json.Unmarshal(params, &a); err != nil {
			return nil, err
		}
		if err := s.st.setDraft(a.Peer, a.Text); err != nil {
			return nil, err
		}
		s.emit(svc.SMSEvent{Event: svc.SMSEventChanged})
		return map[string]bool{"ok": true}, nil
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
// events settle it, or for a multimedia one (pictures, a subject, several
// recipients) to the MMSC; an id sends a failed or a scheduled one now. A
// due time keeps it scheduled until then (schedule.go).
func (s *service) send(req svc.SMSSendReq) (ui.SMS, error) {
	to, text, id := req.To, req.Text, req.ID
	if id == "" && req.At != 0 {
		if err := s.checkDue(req.At, req.Zone); err != nil {
			return ui.SMS{}, err
		}
	}
	if id == "" {
		if peers := splitPeers(to); len(peers) > 1 || len(req.Parts) > 0 || strings.TrimSpace(req.Subject) != "" {
			if err := checkRecipients(peers); err != nil {
				return ui.SMS{}, err
			}
			return s.sendMMS(peers, req)
		}
	}
	s.mu.Lock()
	var m *ui.SMS
	var err error
	if id != "" {
		old := s.st.byID[id]
		switch {
		case old == nil || old.Dir != "out":
			err = errors.New("no such message")
		case old.State != ui.SMSFailed && old.State != ui.SMSScheduled:
			err = errors.New("only a message that was not sent can be sent again")
		default:
			to, text = old.Peer, old.Text
			m, err = s.st.setState(id, ui.SMSSending, true)
		}
	} else if err = checkMessage(to, text); err == nil {
		m, err = s.st.addOutgoing(strings.TrimSpace(to), text, req.At, req.Zone)
	}
	if err != nil {
		s.mu.Unlock()
		return ui.SMS{}, err
	}
	out := *m
	s.mu.Unlock()
	s.emit(svc.SMSEvent{Event: svc.SMSEventMessage, SMS: &out})
	if out.State == ui.SMSScheduled {
		s.kickSchedule()
		return out, nil
	}
	if out.Kind == ui.SMSKindMMS {
		go s.postMMS(out.ID)
		return out, nil
	}
	if err := s.tel.call(svc.TelSMSSend, map[string]string{"to": to, "text": text, "id": out.ID}, nil); err != nil {
		_ = s.settle(out.ID, ui.SMSFailed)
		out.State = ui.SMSFailed
		return out, err
	}
	return out, nil
}

func checkRecipients(peers []string) error {
	if len(peers) == 0 {
		return errors.New("enter a number")
	}
	if len(peers) > ui.MaxGroup {
		return fmt.Errorf("at most %d recipients", ui.MaxGroup)
	}
	for _, p := range peers {
		if utf8.RuneCountInString(p) > 20 {
			return fmt.Errorf("%q is not a number", p)
		}
	}
	return nil
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
