package nfc

import (
	"bytes"
	"io"
	"reflect"
	"testing"
)

type controllerStream struct{ io.Reader }

func (controllerStream) Write(p []byte) (int, error) { return len(p), nil }
func (controllerStream) Close() error                { return nil }

// listening is an NCI as NewNCI makes it, without the start-up exchange.
func listening(r io.Reader) *NCI {
	return &NCI{rw: controllerStream{r}, ev: make(chan Event, 256), rsp: make(chan Packet, 1), resetNt: make(chan Packet, 1),
		deactNt: make(chan Packet, 1), readAct: make(chan Activation, 1), credit: make(chan struct{}, 1)}
}

// run reads the controller's packets to their end, as readLoop does, and
// returns the events.
func run(n *NCI) []Event {
	var evs []Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range n.ev {
			evs = append(evs, e)
		}
	}()
	n.readLoop()
	<-done
	return evs
}

func stream(ps ...Packet) []byte {
	var b []byte
	for _, p := range ps {
		b = append(b, p.Bytes()...)
	}
	return b
}

// FuzzNCI is what a controller (and through it a reader in the field)
// sends the host: notifications, responses, command segments.
func FuzzNCI(f *testing.F) {
	act := []byte{1, IntfISODep, ProtoISODep, ListenA, 0xFF, 1, 0}
	poll := []byte{1, IntfFrame, 0x02, PollA, 0xFF, 1, 9, 0x04, 0x00, 4, 1, 2, 3, 4, 1, 0x08}
	f.Add(stream(Packet{MT: MTNtf, GID: GIDRF, OID: OIDRFFieldInfo, Payload: []byte{1}},
		Packet{MT: MTNtf, GID: GIDRF, OID: OIDRFIntfActivated, Payload: act},
		Packet{MT: MTData, PBF: true, Payload: Select([]byte{0xF0, 1, 2, 3, 4})[:4]},
		Packet{MT: MTData, Payload: Select([]byte{0xF0, 1, 2, 3, 4})[4:]},
		Packet{MT: MTNtf, GID: GIDCore, OID: OIDCoreConnCredits, Payload: []byte{1, 0, 1}},
		Packet{MT: MTNtf, GID: GIDRF, OID: OIDRFDeactivate, Payload: []byte{DeactSleep, 0}}))
	f.Add(stream(Packet{MT: MTRsp, GID: GIDCore, OID: OIDCoreReset, Payload: []byte{0}},
		Packet{MT: MTNtf, GID: GIDCore, OID: OIDCoreReset, Payload: []byte{2, 0, 0x20, 4}},
		Packet{MT: MTNtf, GID: GIDRF, OID: OIDRFIntfActivated, Payload: poll}))
	f.Fuzz(func(t *testing.T, data []byte) {
		n := listening(bytes.NewReader(data))
		for _, e := range run(n) {
			if len(e.APDU) > MaxAPDU {
				t.Fatalf("a command of %d bytes", len(e.APDU))
			}
		}
		if len(n.partial) > MaxAPDU+255 {
			t.Fatalf("%d bytes of a command kept", len(n.partial))
		}
	})
}

// A reader that chains segments without end does not fill the host's
// memory: the command is dropped once it is over MaxAPDU.
func TestEndlessChaining(t *testing.T) {
	seg := Packet{MT: MTData, PBF: true, Payload: bytes.Repeat([]byte{0x80}, 255)}
	var b []byte
	for range MaxAPDU/255 + 10 {
		b = append(b, seg.Bytes()...)
	}
	b = append(b, Packet{MT: MTData, Payload: []byte{0x90, 0}}.Bytes()...)
	b = append(b, Packet{MT: MTData, Payload: Select([]byte{0xF0, 1, 2, 3, 4})}.Bytes()...)
	n := listening(bytes.NewReader(b))
	evs := run(n)
	if len(evs) != 1 || !bytes.Equal(evs[0].APDU, Select([]byte{0xF0, 1, 2, 3, 4})) || n.partial != nil {
		t.Fatalf("events %d, kept %d bytes", len(evs), len(n.partial))
	}
}

// FuzzPacket: what parses encodes to the same bytes.
func FuzzPacket(f *testing.F) {
	f.Add(CoreSetConfig(Param{CfgRFFieldInfo, []byte{1}}).Bytes())
	f.Add(Packet{MT: MTData, PBF: true, Conn: 1, Payload: []byte{1, 2}}.Bytes())
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := ParsePacket(data)
		if err != nil {
			return
		}
		q, err := ParsePacket(p.Bytes())
		if err != nil || !reflect.DeepEqual(p, q) {
			t.Fatalf("%x: %+v, then %+v (%v)", data, p, q, err)
		}
	})
}

// FuzzActivation is RF_INTF_ACTIVATED_NTF: a card's UID, SAK and ATQA.
func FuzzActivation(f *testing.F) {
	f.Add([]byte{1, IntfFrame, 0x02, PollA, 0xFF, 1, 9, 0x04, 0x00, 4, 1, 2, 3, 4, 1, 0x08})
	f.Add([]byte{1, IntfISODep, ProtoISODep, ListenA, 0xFF, 1, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		a, err := ParseActivation(data)
		if err == nil && len(a.UID) > len(data) {
			t.Fatalf("a %d-byte UID from %d bytes", len(a.UID), len(data))
		}
	})
}

// FuzzCard is what a card held to the phone answers (reader mode): its
// TLVs, its status word, the AID a SELECT names.
func FuzzCard(f *testing.F) {
	f.Add([]byte{0x6F, 0x10, 0x84, 0x0E, 0x32, 0x50, 0x41, 0x59, 0xA5, 0x08, 0xBF, 0x0C, 0x05, 0x61, 0x03, 0x4F, 0x01, 0xA0, 0x90, 0x00})
	f.Add(Select([]byte{0xA0, 0, 0, 0, 3, 0x10, 0x10}))
	f.Add([]byte{0x9F, 0x38, 0x81, 0x02, 1, 2})
	f.Fuzz(func(t *testing.T, data []byte) {
		total := 0
		for _, tag := range []uint16{0x4F, 0x9F38, 0x84} {
			for _, v := range TLVFind(data, tag) {
				total += len(v)
			}
		}
		if total > len(data)*len(data)+len(data) {
			t.Fatalf("%d bytes of values from %d", total, len(data))
		}
		SW(data)
		if aid := SelectAID(data); aid != nil && len(aid) > len(data)-5 {
			t.Fatalf("an AID of %d bytes from %d", len(aid), len(data))
		}
	})
}
