package nfc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Sim is a controller in memory, an observe-mode one: it reports the
// polling loop and answers as the identity the host presented last, from
// the loop after the host presents one; a field that comes while one is
// presented is answered at once, as a controller answers on its own. Tap
// holds the phone to a simulated reader, Hold puts a card on the phone for
// reader mode.
type Sim struct {
	ev   chan Event
	mu   sync.Mutex
	id   *Identity
	held *Tag
	// applet answers the held card's commands; read is the card Read
	// returned last, while it is activated for Exchange.
	applet  Applet
	read    *Tag
	waiting chan []byte
	busy    bool
}

// Applet is a simulated smart card's application: what it answers each
// command of a reader-mode session (ISO-DEP).
type Applet interface {
	Answer(cmd []byte) []byte
}

func NewSim() *Sim { return &Sim{ev: make(chan Event, 256)} }

func (s *Sim) Events() <-chan Event { return s.ev }

func (s *Sim) Info() Info {
	return Info{Name: "simulated controller", Observe: true, Simulated: true}
}

func (s *Sim) Close() error { return nil }

func (s *Sim) Present(id *Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = clone(id)
	return nil
}

func clone(id *Identity) *Identity {
	if id == nil {
		return nil
	}
	c := &Identity{UID: append([]byte(nil), id.UID...), HCE: id.HCE}
	for _, a := range id.AIDs {
		c.AIDs = append(c.AIDs, append([]byte(nil), a...))
	}
	return c
}

func (s *Sim) Respond(apdu []byte) error {
	s.mu.Lock()
	ch := s.waiting
	s.waiting = nil
	s.mu.Unlock()
	if ch == nil {
		return errors.New("no command is waiting for an answer")
	}
	ch <- append([]byte(nil), apdu...)
	return nil
}

// Hold puts a card on the back of the phone (nil takes it away).
func (s *Sim) Hold(t *Tag) { s.HoldApplet(t, nil) }

// HoldApplet puts a smart card on the phone whose commands a answers.
func (s *Sim) HoldApplet(t *Tag, a Applet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read = nil
	if t == nil {
		s.held, s.applet = nil, nil
		return
	}
	c := *t
	c.UID = append([]byte(nil), t.UID...)
	s.held, s.applet = &c, a
}

func (s *Sim) Read(ctx context.Context) (Tag, error) {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.Lock()
		t := s.held
		if t != nil {
			s.read = t
		}
		s.mu.Unlock()
		if t != nil {
			return *t, nil
		}
		select {
		case <-ctx.Done():
			return Tag{}, ErrNoCard
		case <-tick.C:
		}
	}
}

func (s *Sim) Exchange(cmd []byte) ([]byte, error) {
	s.mu.Lock()
	t, a := s.read, s.applet
	ok := t != nil && t == s.held
	s.mu.Unlock()
	switch {
	case !ok:
		return nil, ErrLost
	case !t.ISODep:
		return nil, ErrNotISODep
	case a == nil:
		return append([]byte(nil), SWUnknown...), nil
	}
	return a.Answer(append([]byte(nil), cmd...)), nil
}

func (s *Sim) Release() {
	s.mu.Lock()
	s.read = nil
	s.mu.Unlock()
}

// TransitApplet is a transit card of the simplified flow below (AID,
// SELECT, GET BALANCE), a physical card to read the balance of.
type TransitApplet struct {
	AID     []byte
	Balance int
}

func (t TransitApplet) Answer(cmd []byte) []byte {
	if aid := SelectAID(cmd); aid != nil {
		if bytes.Equal(aid, t.AID) {
			return append([]byte(nil), SWOK...)
		}
		return append([]byte(nil), SWNotFound...)
	}
	if bytes.Equal(cmd, GetBalance) {
		return append(binary.BigEndian.AppendUint32(nil, uint32(t.Balance)), SWOK...)
	}
	return []byte{0x6D, 0x00}
}

func (s *Sim) emit(e Event) { s.ev <- e }

// TapResult is how a tap went, as the reader saw it.
type TapResult struct {
	OK     bool   `json:"ok"`
	Note   string `json:"note"`
	UID    []byte `json:"uid,omitempty"`
	ISODep bool   `json:"iso_dep"`
	Loops  int    `json:"loops"`
}

// Polling loops a reader runs before it gives up, and the gap between two.
const (
	simLoops   = 10
	simLoopGap = 60 * time.Millisecond
)

// Tap holds the phone to r until the reader is done: every command waits
// at most patience for the host's answer, as a reader's frame waiting
// time (with the controller's waiting time extensions) would.
func (s *Sim) Tap(r Reader, patience time.Duration) TapResult {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return TapResult{Note: "another reader is in the field"}
	}
	s.busy = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()
	s.emit(Event{Type: EventField, On: true})
	defer s.emit(Event{Type: EventField, On: false})
	var id *Identity
	loops := 0
	for ; loops < simLoops && id == nil; loops++ {
		for _, f := range r.Loop() {
			s.emit(Event{Type: EventFrame, Frame: f})
		}
		end := time.Now().Add(simLoopGap)
		for id == nil && time.Now().Before(end) {
			time.Sleep(5 * time.Millisecond)
			s.mu.Lock()
			id = clone(s.id)
			s.mu.Unlock()
		}
	}
	if id == nil {
		return TapResult{Note: "no card answered", Loops: loops}
	}
	l := &simLink{s: s, id: id, uid: id.UID, patience: patience}
	if len(l.uid) == 0 {
		l.uid = RandomUID()
	}
	l.activate()
	ok, note := r.Talk(l)
	return TapResult{OK: ok, Note: note, UID: l.uid, ISODep: id.HCE, Loops: loops}
}

type simLink struct {
	s        *Sim
	id       *Identity
	uid      []byte
	patience time.Duration
}

func (l *simLink) activate() {
	l.s.emit(Event{Type: EventActivated, ISODep: l.id.HCE, UID: append([]byte(nil), l.uid...)})
}

func (l *simLink) UID() []byte  { return l.uid }
func (l *simLink) ISODep() bool { return l.id.HCE }
func (l *simLink) Halt()        { l.s.emit(Event{Type: EventDeactivated, Sleep: true}) }
func (l *simLink) Repoll()      { l.activate() }

// Transceive hands every command to the host, as a controller does with
// ISO-DEP routed to the host (the host answers AIDs it has no card for).
func (l *simLink) Transceive(cmd []byte) ([]byte, error) {
	if !l.id.HCE {
		return nil, ErrNotISODep
	}
	ch := make(chan []byte, 1)
	l.s.mu.Lock()
	l.s.waiting = ch
	l.s.mu.Unlock()
	l.s.emit(Event{Type: EventAPDU, APDU: append([]byte(nil), cmd...)})
	select {
	case r := <-ch:
		return r, nil
	case <-time.After(l.patience):
		l.s.mu.Lock()
		if l.s.waiting == ch {
			l.s.waiting = nil
		}
		l.s.mu.Unlock()
		return nil, ErrLost
	}
}

// Link is the reader's side of a card in its field.
type Link interface {
	UID() []byte
	ISODep() bool
	Transceive(cmd []byte) ([]byte, error)
	// Halt puts the card to sleep (HLTA, or DESELECT for ISO-DEP): done.
	Halt()
	// Repoll polls again, and the card answers again: what a reader does
	// with a card it refused.
	Repoll()
}

// Reader is a simulated reader: the polling loop it sends (the same at
// every tap: its signature) and what it does with a card.
type Reader interface {
	Loop() []PollingFrame
	Talk(c Link) (ok bool, note string)
}

// What a reader says it is in its vendor frame. The simulated readers put
// "KP", one of these and an id of their own in their loop, the way
// Apple's ECP frames tell an iPhone (in Apple's format) whether a reader
// is a transit gate, a door or a payment terminal.
const (
	HintTransit byte = 1
	HintAccess  byte = 2
	HintPayment byte = 3
)

// ReaderFrame is the vendor frame of a simulated reader called name.
func ReaderFrame(hint byte, name string) PollingFrame {
	sum := sha256.Sum256([]byte(name))
	return PollingFrame{Type: FrameCustom, Data: append([]byte{'K', 'P', hint}, sum[:4]...)}
}

// ReaderHint is what kind of reader a frame names (0 when it is not a
// simulated reader's).
func ReaderHint(f PollingFrame) byte {
	if f.Type != FrameCustom || len(f.Data) < 3 || f.Data[0] != 'K' || f.Data[1] != 'P' {
		return 0
	}
	return f.Data[2]
}

func loop(hint byte, name string, techs ...FrameType) []PollingFrame {
	out := []PollingFrame{{Type: FrameOn}}
	for _, t := range techs {
		out = append(out, PollingFrame{Type: t})
	}
	return append(out, ReaderFrame(hint, name), PollingFrame{Type: FrameOff})
}

// DoorReader opens for the UIDs it accepts (a UID card system: the
// reader checks the card's UID and nothing else). It halts a card it
// accepts and polls a refused one again.
type DoorReader struct {
	Name   string
	Accept [][]byte
}

func (d DoorReader) Loop() []PollingFrame { return loop(HintAccess, d.Name, FrameA) }

func (d DoorReader) Talk(c Link) (bool, string) {
	for _, u := range d.Accept {
		if bytes.Equal(u, c.UID()) {
			c.Halt()
			return true, "door open"
		}
	}
	c.Repoll()
	return false, fmt.Sprintf("access denied (UID %X)", c.UID())
}

// The transit gate's commands, a simplified flow of our own (no keys, no
// MACs, not an operator's): SELECT the card's AID, GET BALANCE, DEBIT.
// Amounts are 4-byte big-endian cents.
var GetBalance = []byte{0x80, 0x5C, 0x00, 0x02, 0x04}

func Debit(fare int, terminal uint32) []byte {
	cmd := []byte{0x80, 0x54, 0x01, 0x00, 0x08}
	cmd = binary.BigEndian.AppendUint32(cmd, uint32(fare))
	cmd = binary.BigEndian.AppendUint32(cmd, terminal)
	return append(cmd, 0x04)
}

// Purchase is the payment terminal's one command after the selects (ours,
// not EMV's): the amount and the terminal; the card answers with an
// 8-byte cryptogram.
func Purchase(amount int, terminal uint32) []byte {
	cmd := []byte{0x80, 0x70, 0x00, 0x00, 0x08}
	cmd = binary.BigEndian.AppendUint32(cmd, uint32(amount))
	cmd = binary.BigEndian.AppendUint32(cmd, terminal)
	return append(cmd, 0x08)
}

func terminalID(name string) uint32 {
	sum := sha256.Sum256([]byte(name))
	return binary.BigEndian.Uint32(sum[:4])
}

// TransitGate debits Fare from the card with AID.
type TransitGate struct {
	Name string
	AID  []byte
	Fare int
}

func (g TransitGate) Loop() []PollingFrame { return loop(HintTransit, g.Name, FrameA, FrameB) }

func (g TransitGate) Talk(c Link) (bool, string) {
	if !c.ISODep() {
		c.Repoll()
		return false, "not a transit card"
	}
	r, err := c.Transceive(Select(g.AID))
	if err != nil || SW(r) != 0x9000 {
		return false, fmt.Sprintf("card not supported (%04X)", SW(r))
	}
	r, err = c.Transceive(GetBalance)
	if err != nil || SW(r) != 0x9000 || len(r) != 6 {
		return false, fmt.Sprintf("no balance (%04X)", SW(r))
	}
	r, err = c.Transceive(Debit(g.Fare, terminalID(g.Name)))
	switch {
	case err != nil:
		return false, err.Error()
	case SW(r) == 0x9401:
		return false, "insufficient balance"
	case SW(r) != 0x9000 || len(r) != 6:
		return false, fmt.Sprintf("debit refused (%04X)", SW(r))
	}
	c.Halt()
	return true, fmt.Sprintf("balance %d", binary.BigEndian.Uint32(r))
}

// PaymentTerminal charges Amount to a card that lists AID in its PPSE.
type PaymentTerminal struct {
	Name   string
	AID    []byte
	Amount int
}

func (p PaymentTerminal) Loop() []PollingFrame { return loop(HintPayment, p.Name, FrameA, FrameB) }

func (p PaymentTerminal) Talk(c Link) (bool, string) {
	if !c.ISODep() {
		c.Repoll()
		return false, "not a payment card"
	}
	ppse, _ := hex.DecodeString(ppseHex)
	r, err := c.Transceive(Select(ppse))
	if err != nil || SW(r) != 0x9000 {
		return false, fmt.Sprintf("no payment application (%04X)", SW(r))
	}
	listed := false
	for _, aid := range TLVFind(r[:len(r)-2], 0x4F) {
		listed = listed || bytes.Equal(aid, p.AID)
	}
	if !listed {
		return false, "card not supported"
	}
	if r, err = c.Transceive(Select(p.AID)); err != nil || SW(r) != 0x9000 {
		return false, fmt.Sprintf("card not supported (%04X)", SW(r))
	}
	r, err = c.Transceive(Purchase(p.Amount, terminalID(p.Name)))
	if err != nil || SW(r) != 0x9000 || len(r) != 10 {
		return false, fmt.Sprintf("declined (%04X)", SW(r))
	}
	c.Halt()
	return true, fmt.Sprintf("approved %d", p.Amount)
}

const ppseHex = "325041592E5359532E4444463031"

// TLVFind is every value of a BER-TLV tag (one or two bytes) inside data,
// constructed tags searched too.
func TLVFind(data []byte, tag uint16) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		t := uint16(data[0])
		constructed := data[0]&0x20 != 0
		i := 1
		if data[0]&0x1F == 0x1F {
			if len(data) < 2 {
				return out
			}
			t, i = t<<8|uint16(data[1]), 2
		}
		if i >= len(data) {
			return out
		}
		n := int(data[i])
		i++
		if n == 0x81 && i < len(data) {
			n, i = int(data[i]), i+1
		}
		if i+n > len(data) {
			return out
		}
		v := data[i : i+n]
		if t == tag {
			out = append(out, v)
		}
		if constructed {
			out = append(out, TLVFind(v, tag)...)
		}
		data = data[i+n:]
	}
	return out
}
