package mms

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// FakeMMSC is an operator's MMSC for tests and the simulated baseband (an
// http.Handler): it hands out the messages offered to it by their
// location, takes m-send.req and answers m-send.conf with a message ID,
// and records the notify responses and acknowledgements.
type FakeMMSC struct {
	mu      sync.Mutex
	stored  map[string][]byte
	next    int
	sent    []*PDU
	acks    []*PDU
	fetched []string
	fail    bool
	// OnSend runs (in its own goroutine) after a message was taken, with
	// its message ID: a test reports its delivery from there.
	OnSend func(req *PDU, messageID string)
}

// Sent is the m-send.req taken, Acks the m-notifyresp.ind and
// m-acknowledge.ind, Fetched the locations handed out.
func (f *FakeMMSC) Sent() []*PDU {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*PDU(nil), f.sent...)
}

func (f *FakeMMSC) Acks() []*PDU {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*PDU(nil), f.acks...)
}

func (f *FakeMMSC) Fetched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.fetched...)
}

// SetFail: m-send.req is answered with an error status from now on.
func (f *FakeMMSC) SetFail(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = on
}

// Offer stores an m-retrieve.conf and gives the location it is fetched
// from under base (the server's URL).
func (f *FakeMMSC) Offer(base string, conf *PDU) (string, error) {
	raw, err := Encode(conf)
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stored == nil {
		f.stored = map[string][]byte{}
	}
	f.next++
	id := fmt.Sprintf("m%d", f.next)
	f.stored[id] = raw
	return strings.TrimSuffix(base, "/") + "/mms/" + id, nil
}

func (f *FakeMMSC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		id := strings.TrimPrefix(r.URL.Path, "/mms/")
		f.mu.Lock()
		raw, ok := f.stored[id]
		if ok {
			f.fetched = append(f.fetched, r.URL.Path)
		}
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", TypeMMS)
		_, _ = w.Write(raw)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p, err := Decode(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch p.Type {
	case SendReq:
		f.next++
		conf := &PDU{Type: SendConf, TransactionID: p.TransactionID, ResponseStatus: ResponseOK, MessageID: fmt.Sprintf("kario-%d", f.next)}
		if !f.fail {
			f.sent = append(f.sent, p)
		}
		if f.fail {
			conf.ResponseStatus, conf.MessageID, conf.ResponseText = 0xE0, "", "refused"
		}
		raw, _ := Encode(conf)
		w.Header().Set("Content-Type", TypeMMS)
		_, _ = w.Write(raw)
		if on := f.OnSend; on != nil && !f.fail {
			go on(p, conf.MessageID)
		}
	case NotifyRespInd, AcknowledgeInd:
		f.acks = append(f.acks, p)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected message", http.StatusBadRequest)
	}
}

// NotificationPush is the WAP push an operator sends a phone for a new
// message: an m-notification.ind with the sender, subject, size, expiry
// (seconds) and where to fetch it.
func NotificationPush(txid, from, subject, location string, size, expiry int64) []byte {
	raw, _ := Encode(&PDU{Type: NotificationInd, TransactionID: txid, From: from, Subject: subject, Class: "personal",
		Size: size, ExpiryAfter: expiry, ContentLocation: location})
	return EncodePush(1, TypeMMS, raw)
}

// DeliveryPush is the WAP push of a delivery report: an m-delivery.ind for
// the message ID, the recipient and their status (StatusRetrieved:
// delivered).
func DeliveryPush(messageID, to string, status int, date int64) []byte {
	raw, _ := Encode(&PDU{Type: DeliveryInd, MessageID: messageID, To: []string{to}, Date: date, Status: status})
	return EncodePush(2, TypeMMS, raw)
}

// RetrieveConfOf is an m-retrieve.conf of the message an m-send.req
// carried, as the MMSC gives it to a recipient: from the sender, with the
// same body.
func RetrieveConfOf(from string, to []string, subject string, date int64, parts []Part, related string) *PDU {
	start := ""
	for _, p := range parts {
		if strings.EqualFold(p.ContentType, TypeSMIL) {
			start = "<" + p.ContentID + ">"
		}
	}
	return &PDU{Type: RetrieveConf, MessageID: fmt.Sprintf("r%d", date), From: from, To: to, Subject: subject, Date: date,
		RetrieveStatus: RetrieveOK, ContentType: TypeRelated, Start: start, Related: related, Parts: parts}
}
