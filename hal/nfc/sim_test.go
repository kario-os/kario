package nfc

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"testing"
	"time"
)

// host plays the phone's side against a Sim: it presents id once it has
// seen a polling loop and answers commands with card.
type host struct {
	mu      sync.Mutex
	s       *Sim
	id      *Identity
	card    func(apdu []byte) []byte
	frames  []PollingFrame
	actives int
	halted  bool
	stop    chan struct{}
}

func runHost(s *Sim, id *Identity, card func([]byte) []byte) *host {
	h := &host{s: s, id: id, card: card, stop: make(chan struct{})}
	go func() {
		for {
			select {
			case e := <-s.Events():
				h.mu.Lock()
				switch e.Type {
				case EventFrame:
					h.frames = append(h.frames, e.Frame)
					if e.Frame.Type == FrameOff {
						_ = s.Present(h.id)
					}
				case EventActivated:
					h.actives++
				case EventDeactivated:
					h.halted = h.halted || e.Sleep
				case EventAPDU:
					if h.card == nil {
						break
					}
					if r := h.card(e.APDU); r != nil {
						_ = s.Respond(r)
					}
				}
				h.mu.Unlock()
			case <-h.stop:
				return
			}
		}
	}()
	return h
}

func (h *host) done() { close(h.stop) }

func (h *host) seen() (int, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.actives, h.halted
}

func TestDoorReader(t *testing.T) {
	home, office := []byte{0x04, 0xA1, 0xB2, 0xC3}, []byte{0x04, 0xD5, 0xE6, 0xF7}
	door := DoorReader{Name: "office", Accept: [][]byte{office}}
	s := NewSim()
	h := runHost(s, &Identity{UID: home}, nil)
	r := s.Tap(door, time.Second)
	if r.OK || r.ISODep {
		t.Fatalf("the office door opened for the home card: %+v", r)
	}
	time.Sleep(20 * time.Millisecond)
	if actives, halted := h.seen(); actives != 2 || halted {
		t.Fatalf("a refused card is polled again, not halted: %d activations, halted %v", actives, halted)
	}
	h.mu.Lock()
	hint := ReaderHint(h.frames[2])
	h.mu.Unlock()
	if hint != HintAccess {
		t.Fatalf("the door says it is %d, frames %+v", hint, h.frames)
	}
	h.done()
	s = NewSim()
	h = runHost(s, &Identity{UID: office}, nil)
	if r := s.Tap(door, time.Second); !r.OK || !bytes.Equal(r.UID, office) {
		t.Fatalf("the office card: %+v", r)
	}
	time.Sleep(20 * time.Millisecond)
	if actives, halted := h.seen(); actives != 1 || !halted {
		t.Fatalf("an accepted card is halted: %d activations, halted %v", actives, halted)
	}
	h.done()
	s = NewSim()
	h = runHost(s, &Identity{HCE: true}, func([]byte) []byte { return SWNotFound })
	if r := s.Tap(door, time.Second); r.OK || r.UID[0] != 0x08 {
		t.Fatalf("HCE answers with a random UID the door does not know: %+v", r)
	}
	h.done()
}

func TestTransitGate(t *testing.T) {
	aid, _ := hex.DecodeString("F04651545241")
	balance := 300
	card := func(apdu []byte) []byte {
		switch {
		case SelectAID(apdu) != nil && bytes.Equal(SelectAID(apdu), aid):
			return SWOK
		case SelectAID(apdu) != nil:
			return SWNotFound
		case bytes.Equal(apdu, GetBalance):
			return append(binary.BigEndian.AppendUint32(nil, uint32(balance)), SWOK...)
		case len(apdu) == 14 && apdu[1] == 0x54:
			fare := int(binary.BigEndian.Uint32(apdu[5:9]))
			if fare > balance {
				return SWInsufficient
			}
			balance -= fare
			return append(binary.BigEndian.AppendUint32(nil, uint32(balance)), SWOK...)
		}
		return SWUnknown
	}
	s := NewSim()
	h := runHost(s, &Identity{HCE: true, AIDs: [][]byte{aid}}, card)
	defer h.done()
	gate := TransitGate{Name: "gate", AID: aid, Fare: 200}
	if r := s.Tap(gate, time.Second); !r.OK || r.Note != "balance 100" {
		t.Fatalf("first trip: %+v", r)
	}
	if r := s.Tap(gate, time.Second); r.OK || r.Note != "insufficient balance" {
		t.Fatalf("second trip with ¥1 left: %+v", r)
	}
	other := TransitGate{Name: "elsewhere", AID: []byte{0xF0, 9, 9, 9, 9}, Fare: 200}
	if r := s.Tap(other, time.Second); r.OK {
		t.Fatalf("another city's gate took the card: %+v", r)
	}
}

func TestPaymentTerminal(t *testing.T) {
	aid, _ := hex.DecodeString("F0465150415901")
	ppse, _ := hex.DecodeString(ppseHex)
	fci := []byte{0x6F, 0x00, 0x84, byte(len(ppse))}
	fci = append(fci, ppse...)
	entry := append([]byte{0x61, byte(2 + len(aid)), 0x4F, byte(len(aid))}, aid...)
	fci = append(fci, 0xA5, byte(3+len(entry)), 0xBF, 0x0C, byte(len(entry)))
	fci = append(fci, entry...)
	fci[1] = byte(len(fci) - 2)
	card := func(apdu []byte) []byte {
		switch sel := SelectAID(apdu); {
		case bytes.Equal(sel, ppse):
			return append(append([]byte(nil), fci...), SWOK...)
		case bytes.Equal(sel, aid):
			return SWOK
		case sel != nil:
			return SWNotFound
		case apdu[1] == 0x70:
			return append(bytes.Repeat([]byte{0x5A}, 8), SWOK...)
		}
		return SWUnknown
	}
	s := NewSim()
	h := runHost(s, &Identity{HCE: true, AIDs: [][]byte{ppse, aid}}, card)
	defer h.done()
	if r := s.Tap(PaymentTerminal{Name: "till", AID: aid, Amount: 1500}, time.Second); !r.OK {
		t.Fatalf("payment: %+v", r)
	}
	if r := s.Tap(PaymentTerminal{Name: "till", AID: []byte{0xA0, 0, 0, 3, 0x33}, Amount: 1500}, time.Second); r.OK || r.Note != "card not supported" {
		t.Fatalf("a terminal for another scheme: %+v", r)
	}
}

func TestSilentHost(t *testing.T) {
	s := NewSim()
	h := runHost(s, nil, nil)
	defer h.done()
	if r := s.Tap(DoorReader{Name: "d"}, time.Second); r.OK || r.Loops != simLoops {
		t.Fatalf("a phone that presents nothing: %+v", r)
	}
	h.mu.Lock()
	h.id = &Identity{HCE: true}
	h.mu.Unlock()
	start := time.Now()
	if r := s.Tap(TransitGate{Name: "g", AID: []byte{1, 2, 3, 4, 5}, Fare: 1}, 100*time.Millisecond); r.OK || r.Note == "" {
		t.Fatalf("an app that never answers: %+v", r)
	}
	if time.Since(start) > time.Second {
		t.Fatal("the reader waited past its patience")
	}
}

// An identity set before the field is answered at once, as a controller
// answers on its own while the host sleeps; a fixed UID with ISO-DEP (a
// dual-interface card) opens a UID door and pays a transit gate.
func TestAnswersOnItsOwn(t *testing.T) {
	home, aid := []byte{0x04, 0xA1, 0xB2, 0xC3}, []byte{0xF0, 0x46, 0x51, 0x54, 0x52}
	s := NewSim()
	go func() {
		for e := range s.Events() {
			if e.Type == EventAPDU {
				_ = s.Respond(append(binary.BigEndian.AppendUint32(nil, 500), SWOK...))
			}
		}
	}()
	_ = s.Present(&Identity{UID: home, HCE: true, AIDs: [][]byte{aid}})
	r := s.Tap(DoorReader{Name: "home", Accept: [][]byte{home}}, time.Second)
	if !r.OK || r.Loops != 1 || !bytes.Equal(r.UID, home) || !r.ISODep {
		t.Fatalf("the door, the identity set before the field: %+v", r)
	}
	if r := s.Tap(TransitGate{Name: "gate", AID: aid, Fare: 200}, time.Second); !r.OK || r.Loops != 1 {
		t.Fatalf("the gate, the same identity: %+v", r)
	}
}

func within(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestRead(t *testing.T) {
	s := NewSim()
	if _, err := s.Read(within(t, 30*time.Millisecond)); err != ErrNoCard {
		t.Fatalf("nothing held: %v", err)
	}
	s.Hold(&Tag{UID: []byte{1, 2, 3, 4}, SAK: 0x08, Encrypted: true})
	tag, err := s.Read(within(t, time.Second))
	if err != nil || !tag.Encrypted || tag.SAK != 0x08 {
		t.Fatalf("read %+v %v", tag, err)
	}
	if _, err := s.Exchange(GetBalance); err != ErrNotISODep {
		t.Fatalf("a UID card took a command: %v", err)
	}
}

// A read stops as soon as it is cancelled, not when its time is up.
func TestReadCancelled(t *testing.T) {
	s := NewSim()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	time.AfterFunc(50*time.Millisecond, cancel)
	t0 := time.Now()
	if _, err := s.Read(ctx); err != ErrNoCard || time.Since(t0) > 2*time.Second {
		t.Fatalf("cancelled read: %v after %v", err, time.Since(t0))
	}
}

// A smart card read in reader mode answers commands until it is taken
// away or released.
func TestExchange(t *testing.T) {
	s := NewSim()
	aid := []byte{0xF0, 0x46, 0x51, 0x54, 0x52, 0x4E, 0x53}
	if _, err := s.Exchange(GetBalance); err != ErrLost {
		t.Fatalf("nothing read: %v", err)
	}
	s.HoldApplet(&Tag{UID: []byte{4, 1, 2, 3}, SAK: 0x20, ISODep: true}, TransitApplet{AID: aid, Balance: 1234})
	if _, err := s.Read(within(t, time.Second)); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Exchange(Select(aid)); err != nil || SW(r) != 0x9000 {
		t.Fatalf("select: %X %v", r, err)
	}
	if r, err := s.Exchange(Select([]byte{1, 2, 3, 4, 5})); err != nil || SW(r) != 0x6A82 {
		t.Fatalf("select of another AID: %X %v", r, err)
	}
	r, err := s.Exchange(GetBalance)
	if err != nil || len(r) != 6 || binary.BigEndian.Uint32(r) != 1234 {
		t.Fatalf("balance: %X %v", r, err)
	}
	s.Release()
	if _, err := s.Exchange(GetBalance); err != ErrLost {
		t.Fatalf("released: %v", err)
	}
	if _, err := s.Read(within(t, time.Second)); err != nil {
		t.Fatal(err)
	}
	s.HoldApplet(&Tag{UID: []byte{4, 1, 2, 3}, SAK: 0x20, ISODep: true}, TransitApplet{AID: aid})
	if _, err := s.Exchange(GetBalance); err != ErrLost {
		t.Fatalf("another card put on the phone answered for the one read: %v", err)
	}
}

func TestTLVFind(t *testing.T) {
	data, _ := hex.DecodeString("6F1A840E325041592E5359532E4444463031A508BF0C056103" + "4F01AA")
	if got := TLVFind(data, 0x4F); len(got) != 1 || got[0][0] != 0xAA {
		t.Fatalf("4F in %X: %X", data, got)
	}
	if SelectAID(Select([]byte{1, 2, 3, 4, 5})) == nil || SelectAID([]byte{0x80, 0xA4, 0x04, 0, 1, 1}) != nil {
		t.Fatal("SelectAID")
	}
}
