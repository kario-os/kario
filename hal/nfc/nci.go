package nfc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// NCI is a controller spoken to in NCI (the NFC Forum's NFC Controller
// Interface, 1.0 and 2.0), the language of NXP's PN7160 (NCI 2.0) and
// PN553 (NCI 1.0) class controllers, on a raw NCI device: NXP's nxpnfc or
// pn5xx_i2c drivers hand each packet through read and write, as
// Android's NCI stack uses them. Mainline Linux's nxp-nci driver keeps
// NCI in the kernel (net/nfc) instead, which does not offer host card
// emulation to userspace. Not run on a controller yet: the framing and
// the commands are checked against a fake controller only. Observe mode
// needs a vendor extension (Android's proprietary NCI messages), which
// is not here: Info().Observe is false.
type NCI struct {
	rw      io.ReadWriteCloser
	ev      chan Event
	rsp     chan Packet
	resetNt chan Packet
	deactNt chan Packet
	readAct chan Activation
	cmdMu   sync.Mutex
	opMu    sync.Mutex
	mu      sync.Mutex
	version byte
	info    Info
	// The listen-mode connection: its largest data payload, credits and
	// a command arriving in segments.
	maxData int
	credits int
	credit  chan struct{}
	partial []byte
	id      *Identity
	// discovering: RF discovery is running (RF_DEACTIVATE stops it).
	discovering bool
}

// Message types, the header's MT.
const (
	MTData = 0
	MTCmd  = 1
	MTRsp  = 2
	MTNtf  = 3
)

// Groups and opcodes used here.
const (
	GIDCore = 0x0
	GIDRF   = 0x1

	OIDCoreReset       = 0x00
	OIDCoreInit        = 0x01
	OIDCoreSetConfig   = 0x02
	OIDCoreConnCredits = 0x06

	OIDRFListenRouting = 0x01
	OIDRFDiscover      = 0x03
	OIDRFIntfActivated = 0x05
	OIDRFDeactivate    = 0x06
	OIDRFFieldInfo     = 0x07
)

// Configuration parameters: RF field notifications and the listen-mode
// NFC-A identity (NFCID1 is the UID; an empty one makes the controller
// pick a random one; SEL_RES is the SAK, 0x20 for ISO-DEP).
const (
	CfgRFFieldInfo = 0x80
	CfgLASelInfo   = 0x32
	CfgLANFCID1    = 0x33
)

// RF technologies and modes for RF_DISCOVER.
const (
	PollA   = 0x00
	PollB   = 0x01
	ListenA = 0x80
	ListenB = 0x81
)

// RF interfaces and protocols in RF_INTF_ACTIVATED_NTF.
const (
	IntfFrame      = 0x01
	IntfISODep     = 0x02
	ProtoISODep    = 0x04
	DeactIdle      = 0x00
	DeactSleep     = 0x01
	DeactDiscovery = 0x03
)

// Routing: entries by technology, protocol or AID, each to a route (the
// device host, 0x00, or a secure element) in power states. Only "switched
// on" is set and the controller is never told the screen went off or the
// phone locked (NCI 2.0's power sub-states): it keeps listening in every
// state, the host applies the lock and screen rules itself, and its
// interrupt wakes a suspended host (WakeSource).
const (
	RouteTech       = 0x00
	RouteProto      = 0x01
	RouteAID        = 0x02
	RouteDH         = 0x00
	PowerSwitchedOn = 0x01
)

// Packet is one NCI packet: a control message (command, response,
// notification: GID and OID) or data on a logical connection. PBF: more
// segments of the same message follow.
type Packet struct {
	MT      byte
	PBF     bool
	GID     byte
	OID     byte
	Conn    byte
	Payload []byte
}

func (p Packet) Bytes() []byte {
	b0, b1 := p.MT<<5, byte(0)
	if p.PBF {
		b0 |= 0x10
	}
	if p.MT == MTData {
		b0 |= p.Conn & 0x0F
	} else {
		b0 |= p.GID & 0x0F
		b1 = p.OID & 0x3F
	}
	return append([]byte{b0, b1, byte(len(p.Payload))}, p.Payload...)
}

// ParsePacket reads one packet from its bytes.
func ParsePacket(b []byte) (Packet, error) {
	if len(b) < 3 || len(b) != 3+int(b[2]) {
		return Packet{}, fmt.Errorf("nci: a packet of %d bytes", len(b))
	}
	p := Packet{MT: b[0] >> 5 & 7, PBF: b[0]&0x10 != 0, Payload: append([]byte(nil), b[3:]...)}
	if p.MT == MTData {
		p.Conn = b[0] & 0x0F
	} else {
		p.GID, p.OID = b[0]&0x0F, b[1]&0x3F
	}
	return p, nil
}

// ReadPacket reads a header, then its payload, as the NCI drivers want.
func ReadPacket(r io.Reader) (Packet, error) {
	h := make([]byte, 3)
	if _, err := io.ReadFull(r, h); err != nil {
		return Packet{}, err
	}
	b := append(h, make([]byte, h[2])...)
	if _, err := io.ReadFull(r, b[3:]); err != nil {
		return Packet{}, err
	}
	return ParsePacket(b)
}

func cmd(gid, oid byte, payload ...byte) Packet {
	return Packet{MT: MTCmd, GID: gid, OID: oid, Payload: payload}
}

// CoreReset resets the controller, keeping its configuration or not.
func CoreReset(keep bool) Packet {
	if keep {
		return cmd(GIDCore, OIDCoreReset, 0x00)
	}
	return cmd(GIDCore, OIDCoreReset, 0x01)
}

// CoreInit: NCI 2.0 adds two bytes of features to enable (none here).
func CoreInit(v2 bool) Packet {
	if v2 {
		return cmd(GIDCore, OIDCoreInit, 0x00, 0x00)
	}
	return cmd(GIDCore, OIDCoreInit)
}

type Param struct {
	ID    byte
	Value []byte
}

func CoreSetConfig(ps ...Param) Packet {
	pl := []byte{byte(len(ps))}
	for _, p := range ps {
		pl = append(append(pl, p.ID, byte(len(p.Value))), p.Value...)
	}
	return cmd(GIDCore, OIDCoreSetConfig, pl...)
}

// RFDiscover starts discovery in these modes, each every period.
func RFDiscover(modes ...byte) Packet {
	pl := []byte{byte(len(modes))}
	for _, m := range modes {
		pl = append(pl, m, 0x01)
	}
	return cmd(GIDRF, OIDRFDiscover, pl...)
}

func RFDeactivate(t byte) Packet { return cmd(GIDRF, OIDRFDeactivate, t) }

type Route struct {
	Type  byte
	Value []byte
}

func AIDRoute(aid []byte) Route {
	return Route{Type: RouteAID, Value: append([]byte{RouteDH, PowerSwitchedOn}, aid...)}
}

func ProtoRoute(proto byte) Route {
	return Route{Type: RouteProto, Value: []byte{RouteDH, PowerSwitchedOn, proto}}
}

// ListenRouting is the routing table in as many RF_SET_LISTEN_MODE_ROUTING
// commands as it takes (each says whether more follow).
func ListenRouting(rs []Route) []Packet {
	var out []Packet
	for len(rs) > 0 || len(out) == 0 {
		pl, n := []byte{0x00, 0x00}, 0
		for n < len(rs) && len(pl)+2+len(rs[n].Value) <= 255 {
			pl = append(append(pl, rs[n].Type, byte(len(rs[n].Value))), rs[n].Value...)
			n++
		}
		rs = rs[n:]
		if len(rs) > 0 {
			pl[0] = 0x01
		}
		pl[1] = byte(n)
		out = append(out, cmd(GIDRF, OIDRFListenRouting, pl...))
		if n == 0 {
			break
		}
	}
	return out
}

// DataPackets splits data for connection conn into segments of at most max.
func DataPackets(conn byte, data []byte, max int) []Packet {
	var out []Packet
	for {
		n := min(len(data), max)
		out = append(out, Packet{MT: MTData, Conn: conn, PBF: n < len(data), Payload: append([]byte(nil), data[:n]...)})
		data = data[n:]
		if len(data) == 0 {
			return out
		}
	}
}

// Activation is RF_INTF_ACTIVATED_NTF: a reader activated the phone
// (listen modes, 0x80 and up) or the phone a card (poll modes; NFC-A
// gives the card's UID, SAK and ATQA).
type Activation struct {
	Interface, Protocol, Mode byte
	MaxData, Credits          int
	UID                       []byte
	SAK                       byte
	ATQA                      [2]byte
}

func ParseActivation(pl []byte) (Activation, error) {
	if len(pl) < 7 || len(pl) < 7+int(pl[6]) {
		return Activation{}, errors.New("nci: short RF_INTF_ACTIVATED_NTF")
	}
	a := Activation{Interface: pl[1], Protocol: pl[2], Mode: pl[3], MaxData: int(pl[4]), Credits: int(pl[5])}
	tech := pl[7 : 7+int(pl[6])]
	if a.Mode == PollA && len(tech) >= 3 {
		a.ATQA = [2]byte{tech[0], tech[1]}
		n := int(tech[2])
		if len(tech) >= 3+n {
			a.UID = append([]byte(nil), tech[3:3+n]...)
			if len(tech) >= 5+n && tech[3+n] >= 1 {
				a.SAK = tech[4+n]
			}
		}
	}
	return a, nil
}

// OpenNCI opens a raw NCI device (/dev/nxpnfc, /dev/pn544) and brings the
// controller up.
func OpenNCI(path string) (*NCI, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return NewNCI(f)
}

// NewNCI brings up the controller on rw: reset, init, field notifications.
func NewNCI(rw io.ReadWriteCloser) (*NCI, error) {
	n := &NCI{rw: rw, ev: make(chan Event, 256), rsp: make(chan Packet, 1), resetNt: make(chan Packet, 1),
		deactNt: make(chan Packet, 1), readAct: make(chan Activation, 1), credit: make(chan struct{}, 1)}
	go n.readLoop()
	if err := n.init(); err != nil {
		rw.Close()
		return nil, err
	}
	return n, nil
}

const nciTimeout = time.Second

func (n *NCI) init() error {
	rsp, err := n.command(CoreReset(true))
	if err != nil {
		return err
	}
	manufacturer := byte(0)
	if len(rsp.Payload) >= 3 {
		n.version = rsp.Payload[1]
	} else {
		select {
		case nt := <-n.resetNt:
			if len(nt.Payload) < 4 {
				return errors.New("nci: short CORE_RESET_NTF")
			}
			n.version, manufacturer = nt.Payload[2], nt.Payload[3]
		case <-time.After(nciTimeout):
			return errors.New("nci: no CORE_RESET_NTF")
		}
	}
	if _, err := n.command(CoreInit(n.version >= 0x20)); err != nil {
		return err
	}
	if _, err := n.command(CoreSetConfig(Param{CfgRFFieldInfo, []byte{0x01}})); err != nil {
		return err
	}
	n.info = Info{Name: fmt.Sprintf("NCI %d.%d controller (manufacturer %02X)", n.version>>4, n.version&0x0F, manufacturer)}
	return nil
}

// command sends one command and waits for its response (NCI allows one
// in flight); a status other than OK is an error.
func (n *NCI) command(p Packet) (Packet, error) {
	n.cmdMu.Lock()
	defer n.cmdMu.Unlock()
	if _, err := n.rw.Write(p.Bytes()); err != nil {
		return Packet{}, err
	}
	select {
	case r := <-n.rsp:
		if r.GID != p.GID || r.OID != p.OID {
			return r, fmt.Errorf("nci: response %X/%X to command %X/%X", r.GID, r.OID, p.GID, p.OID)
		}
		if len(r.Payload) == 0 || r.Payload[0] != 0x00 {
			return r, fmt.Errorf("nci: command %X/%X: status %X", p.GID, p.OID, r.Payload)
		}
		return r, nil
	case <-time.After(nciTimeout):
		return Packet{}, fmt.Errorf("nci: no response to %X/%X", p.GID, p.OID)
	}
}

func (n *NCI) readLoop() {
	defer close(n.ev)
	for {
		p, err := ReadPacket(n.rw)
		if err != nil {
			return
		}
		switch p.MT {
		case MTRsp:
			select {
			case n.rsp <- p:
			default:
			}
		case MTNtf:
			n.notification(p)
		case MTData:
			n.data(p)
		}
	}
}

func (n *NCI) notification(p Packet) {
	switch {
	case p.GID == GIDCore && p.OID == OIDCoreReset:
		select {
		case n.resetNt <- p:
		default:
		}
	case p.GID == GIDCore && p.OID == OIDCoreConnCredits && len(p.Payload) >= 3:
		n.mu.Lock()
		for i := 0; i < int(p.Payload[0]) && 2+2*i < len(p.Payload); i++ {
			if p.Payload[1+2*i] == 0 {
				n.credits += int(p.Payload[2+2*i])
			}
		}
		n.mu.Unlock()
		select {
		case n.credit <- struct{}{}:
		default:
		}
	case p.GID == GIDRF && p.OID == OIDRFFieldInfo && len(p.Payload) >= 1:
		n.ev <- Event{Type: EventField, On: p.Payload[0] == 0x01}
	case p.GID == GIDRF && p.OID == OIDRFIntfActivated:
		a, err := ParseActivation(p.Payload)
		if err != nil {
			return
		}
		if a.Mode&0x80 == 0 {
			select {
			case n.readAct <- a:
			default:
			}
			return
		}
		n.mu.Lock()
		n.maxData, n.credits, n.partial = a.MaxData, a.Credits, nil
		n.mu.Unlock()
		n.ev <- Event{Type: EventActivated, ISODep: a.Interface == IntfISODep}
	case p.GID == GIDRF && p.OID == OIDRFDeactivate && len(p.Payload) >= 1:
		select {
		case n.deactNt <- p:
		default:
		}
		if p.Payload[0] == DeactSleep {
			n.ev <- Event{Type: EventDeactivated, Sleep: true}
		}
	}
}

// data reassembles the reader's command on the static RF connection (0).
func (n *NCI) data(p Packet) {
	if p.Conn != 0 {
		return
	}
	n.mu.Lock()
	n.partial = append(n.partial, p.Payload...)
	whole := n.partial
	if p.PBF {
		n.mu.Unlock()
		return
	}
	n.partial = nil
	n.mu.Unlock()
	n.ev <- Event{Type: EventAPDU, APDU: whole}
}

func (n *NCI) Events() <-chan Event { return n.ev }
func (n *NCI) Info() Info           { return n.info }
func (n *NCI) Close() error         { return n.rw.Close() }

// Respond sends the answer in segments, each spending a credit.
func (n *NCI) Respond(apdu []byte) error {
	n.mu.Lock()
	max := n.maxData
	n.mu.Unlock()
	if max <= 0 {
		max = 255
	}
	for _, p := range DataPackets(0, apdu, max) {
		for {
			n.mu.Lock()
			ok := n.credits > 0
			if ok {
				n.credits--
			}
			n.mu.Unlock()
			if ok {
				break
			}
			select {
			case <-n.credit:
			case <-time.After(nciTimeout):
				return errors.New("nci: no credit to send on")
			}
		}
		if _, err := n.rw.Write(p.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

// stop ends discovery: the response, then the controller's notification.
func (n *NCI) stop() error {
	if !n.discovering {
		return nil
	}
	n.discovering = false
	select {
	case <-n.deactNt:
	default:
	}
	if _, err := n.command(RFDeactivate(DeactIdle)); err != nil {
		return err
	}
	select {
	case <-n.deactNt:
	case <-time.After(nciTimeout):
	}
	return nil
}

// Present programs the listen-mode identity and starts listening: a UID
// card answers as NFC-A with that NFCID1 (SAK 0x08; a controller may need
// vendor settings for it), HCE as ISO-DEP on NFC-A and NFC-B with ISO-DEP
// and the AIDs routed to the host.
func (n *NCI) Present(id *Identity) error {
	n.opMu.Lock()
	defer n.opMu.Unlock()
	n.mu.Lock()
	n.id = clone(id)
	n.mu.Unlock()
	return n.listen(id)
}

func (n *NCI) listen(id *Identity) error {
	if err := n.stop(); err != nil {
		return err
	}
	if id == nil {
		return nil
	}
	modes := []byte{ListenA}
	if id.HCE {
		if _, err := n.command(CoreSetConfig(Param{CfgLANFCID1, id.UID}, Param{CfgLASelInfo, []byte{0x20}})); err != nil {
			return err
		}
		routes := []Route{ProtoRoute(ProtoISODep)}
		for _, a := range id.AIDs {
			routes = append(routes, AIDRoute(a))
		}
		for _, p := range ListenRouting(routes) {
			if _, err := n.command(p); err != nil {
				return err
			}
		}
		modes = append(modes, ListenB)
	} else if _, err := n.command(CoreSetConfig(Param{CfgLANFCID1, id.UID}, Param{CfgLASelInfo, []byte{0x08}})); err != nil {
		return err
	}
	if _, err := n.command(RFDiscover(modes...)); err != nil {
		return err
	}
	n.discovering = true
	return nil
}

// Read polls NFC-A and NFC-B for a card, then listens as before.
func (n *NCI) Read(ctx context.Context) (Tag, error) {
	n.opMu.Lock()
	defer n.opMu.Unlock()
	if err := n.stop(); err != nil {
		return Tag{}, err
	}
	select {
	case <-n.readAct:
	default:
	}
	if _, err := n.command(RFDiscover(PollA, PollB)); err != nil {
		return Tag{}, err
	}
	n.discovering = true
	var t Tag
	err := ErrNoCard
	select {
	case a := <-n.readAct:
		t, err = Tag{UID: a.UID, ATQA: a.ATQA, SAK: a.SAK, ISODep: a.Protocol == ProtoISODep}, nil
	case <-ctx.Done():
	}
	n.mu.Lock()
	id := clone(n.id)
	n.mu.Unlock()
	if lerr := n.listen(id); lerr != nil && err == nil {
		err = lerr
	}
	return t, err
}
