package smsd

import (
	"encoding/json"
	"net"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// fakeTel answers what smsd asks telephonyd: the pending queue, its
// acknowledgements, sending (it reports the message sent at once) and the
// old store.
type fakeTel struct {
	mu      sync.Mutex
	ln      net.Listener
	conns   []*wire.Conn
	subs    []*wire.Conn
	pending []map[string]any
	legacy  []ui.SMS
	acked   []string
	sent    []string
}

func newFakeTel(t *testing.T, sock string) *fakeTel {
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeTel{ln: ln}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			c := wire.New(nc)
			f.mu.Lock()
			f.conns = append(f.conns, c)
			f.mu.Unlock()
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeTel) close() {
	f.ln.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		c.Close()
	}
	f.conns, f.subs = nil, nil
}

func (f *fakeTel) serve(c *wire.Conn) {
	for {
		var req wire.RPC
		if c.Recv(&req) != nil {
			return
		}
		var p struct {
			To, Text, ID string
			Keys         []string
		}
		_ = json.Unmarshal(req.Params, &p)
		var res any = map[string]bool{"ok": true}
		f.mu.Lock()
		switch req.Method {
		case wire.Subscribe:
			f.subs = append(f.subs, c)
		case svc.TelSMSPending:
			res = append([]map[string]any{}, f.pending...)
		case svc.TelSMSAck:
			f.acked = append(f.acked, p.Keys...)
			f.pending = slices.DeleteFunc(f.pending, func(e map[string]any) bool { return slices.Contains(p.Keys, e["key"].(string)) })
		case svc.TelSMSLegacy:
			res = f.legacy
		case svc.TelSMSLegacyDrop:
			f.legacy = nil
		case svc.TelSMSSend:
			f.sent = append(f.sent, p.ID)
		}
		f.mu.Unlock()
		_ = c.Send(wire.RPC{ID: req.ID, OK: true, Result: mustJSON(res)})
		if req.Method == svc.TelSMSSend {
			f.deliver(map[string]any{"event": "sms", "key": p.ID + "@sent", "sms": ui.SMS{ID: p.ID, Peer: p.To, Dir: "out", Text: p.Text, State: ui.SMSSent}})
		}
	}
}

func mustJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

func (f *fakeTel) set(pending []map[string]any, legacy []ui.SMS) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending, f.legacy = pending, legacy
}

func (f *fakeTel) deliver(ev map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, ev)
	for _, c := range f.subs {
		_ = c.Send(ev)
	}
}

func (f *fakeTel) state() (pending int, acked []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pending), append([]string(nil), f.acked...)
}

type runningSMSD struct {
	s      *service
	events chan svc.SMSEvent
	stop   chan struct{}
	done   chan struct{}
}

func startSMSD(t *testing.T, dir, sock string) *runningSMSD {
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := &runningSMSD{events: make(chan svc.SMSEvent, 64), stop: make(chan struct{}), done: make(chan struct{})}
	r.s = newService(st, func(ev svc.SMSEvent) { r.events <- ev })
	r.s.tel.stop = r.stop
	go func() {
		defer close(r.done)
		r.s.tel.run(sock)
	}()
	return r
}

func (r *runningSMSD) halt() {
	close(r.stop)
	<-r.done
	_ = r.s.st.close()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("no %s", what)
		}
	}
}

func (r *runningSMSD) event(t *testing.T, want string) svc.SMSEvent {
	t.Helper()
	for {
		select {
		case ev := <-r.events:
			if ev.Event == want {
				return ev
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no %s event", want)
		}
	}
}

// A message telephonyd holds reaches smsd whenever smsd comes up, and goes
// from telephonyd once stored; after smsd restarts it is not stored twice.
func TestNothingIsLostBetweenTelephonydAndSMSD(t *testing.T) {
	tmp := t.TempDir()
	sock, dir := filepath.Join(tmp, "telephonyd.sock"), filepath.Join(tmp, "smsd")
	tel := newFakeTel(t, sock)
	defer tel.close()
	tel.set([]map[string]any{{"event": "sms", "key": "m1", "sms": ui.SMS{ID: "m1", Peer: "10690000", Dir: "in", Text: "Your code is 4321", At: 5}}},
		[]ui.SMS{{ID: "s1", Peer: "10086", Dir: "in", Text: "from before", At: 1, Read: true}})

	d := startSMSD(t, dir, sock)
	ev := d.event(t, svc.SMSEventMessage)
	if !ev.New || ev.SMS.ID != "m1" || ev.SMS.Code != "4321" {
		t.Fatalf("first event: %+v", ev)
	}
	waitFor(t, "acknowledgement", func() bool { n, acked := tel.state(); return n == 0 && slices.Equal(acked, []string{"m1"}) })
	waitFor(t, "the old store taken over", func() bool { tel.mu.Lock(); defer tel.mu.Unlock(); return tel.legacy == nil })

	sent, err := d.s.send("10010", "balance?", "")
	if err != nil || sent.State != ui.SMSSending {
		t.Fatalf("send: %+v %v", sent, err)
	}
	waitFor(t, "the sent report", func() bool {
		d.s.mu.Lock()
		defer d.s.mu.Unlock()
		return d.s.st.byID[sent.ID].State == ui.SMSSent
	})
	d.halt()

	// While smsd is down: a new message, and the first one again (the
	// modem reported it once more).
	tel.close()
	tel = newFakeTel(t, sock)
	defer tel.close()
	tel.set([]map[string]any{
		{"event": "sms", "key": "m1", "sms": ui.SMS{ID: "m1", Peer: "10690000", Dir: "in", Text: "Your code is 4321", At: 5}},
		{"event": "sms", "key": "m2", "sms": ui.SMS{ID: "m2", Peer: "+86 10690000", Dir: "in", Text: "second", At: 6}},
		{"event": "cb", "key": "b1", "cb": map[string]any{"id": "b1", "channel": 4370, "serial": 3, "text": "Drill", "at": time.Now().Unix()}},
	}, nil)
	d = startSMSD(t, dir, sock)
	defer d.halt()
	if ev := d.event(t, svc.SMSEventMessage); ev.SMS.ID != "m2" {
		t.Fatalf("after the restart: %+v", ev)
	}
	if ev := d.event(t, svc.SMSEventAlert); ev.Alert.Level != ui.AlertPresidential || ev.Alert.Text != "Drill" {
		t.Fatalf("alert: %+v", ev.Alert)
	}
	waitFor(t, "acknowledgements", func() bool { n, _ := tel.state(); return n == 0 })
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	threads := d.s.st.threads("")
	if len(d.s.st.msgs) != 4 || len(threads) != 3 || threads[1].Thread != "10690000" || threads[1].Count != 2 || threads[1].Unread != 2 {
		t.Fatalf("%d messages, threads %+v", len(d.s.st.msgs), threads)
	}
}
