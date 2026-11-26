package nfc

import (
	"bytes"
	"encoding/hex"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeNFCC answers the host the way an NCI controller would, records what
// the host sent, and lets a test send notifications and data.
type fakeNFCC struct {
	v2       bool
	fromHost *io.PipeReader
	toHost   *io.PipeWriter
	mu       sync.Mutex
	got      []Packet
}

type pipeRW struct {
	io.Reader
	io.Writer
	closers []io.Closer
}

func (p pipeRW) Close() error {
	for _, c := range p.closers {
		c.Close()
	}
	return nil
}

func newFake(t *testing.T, v2 bool) (*fakeNFCC, *NCI) {
	hr, fw := io.Pipe()
	fr, hw := io.Pipe()
	f := &fakeNFCC{v2: v2, fromHost: fr, toHost: fw}
	go f.run()
	n, err := NewNCI(pipeRW{Reader: hr, Writer: hw, closers: []io.Closer{hr, hw, fr, fw}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return f, n
}

func (f *fakeNFCC) send(p Packet) { _, _ = f.toHost.Write(p.Bytes()) }

func (f *fakeNFCC) rsp(gid, oid byte, pl ...byte) {
	f.send(Packet{MT: MTRsp, GID: gid, OID: oid, Payload: pl})
}

func (f *fakeNFCC) ntf(gid, oid byte, pl ...byte) {
	f.send(Packet{MT: MTNtf, GID: gid, OID: oid, Payload: pl})
}

func (f *fakeNFCC) run() {
	for {
		p, err := ReadPacket(f.fromHost)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.got = append(f.got, p)
		f.mu.Unlock()
		if p.MT != MTCmd {
			continue
		}
		switch {
		case p.GID == GIDCore && p.OID == OIDCoreReset && f.v2:
			f.rsp(GIDCore, OIDCoreReset, 0x00)
			f.ntf(GIDCore, OIDCoreReset, 0x02, 0x00, 0x20, 0x04, 0x00)
		case p.GID == GIDCore && p.OID == OIDCoreReset:
			f.rsp(GIDCore, OIDCoreReset, 0x00, 0x10, 0x00)
		case p.GID == GIDCore && p.OID == OIDCoreInit:
			f.rsp(GIDCore, OIDCoreInit, 0x00, 0, 0, 0, 0, 1, 0x00, 0x02, 0xFF, 0xFF, 1, 0, 0, 1, IntfISODep, 0)
		case p.GID == GIDCore && p.OID == OIDCoreSetConfig:
			f.rsp(GIDCore, OIDCoreSetConfig, 0x00, 0x00)
		case p.GID == GIDRF && p.OID == OIDRFDeactivate:
			f.rsp(GIDRF, OIDRFDeactivate, 0x00)
			f.ntf(GIDRF, OIDRFDeactivate, p.Payload[0], 0x00)
		default:
			f.rsp(p.GID, p.OID, 0x00)
		}
	}
}

// sent is what the host sent of this kind, as hex payloads.
func (f *fakeNFCC) sent(mt, gid, oid byte) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.got {
		if p.MT == mt && (mt == MTData || p.GID == gid && p.OID == oid) {
			out = append(out, strings.ToUpper(hex.EncodeToString(p.Payload)))
		}
	}
	return out
}

func (f *fakeNFCC) waitSent(t *testing.T, mt, gid, oid byte, payload string) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		for _, s := range f.sent(mt, gid, oid) {
			if s == payload {
				return
			}
		}
	}
	t.Fatalf("the host never sent %X/%X %s; it sent %v", gid, oid, payload, f.sent(mt, gid, oid))
}

func nextEvent(t *testing.T, n *NCI) Event {
	t.Helper()
	select {
	case e := <-n.Events():
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
	return Event{}
}

func TestPacketBytes(t *testing.T) {
	if got := hex.EncodeToString(CoreReset(true).Bytes()); got != "20000100" {
		t.Fatalf("CORE_RESET_CMD is %s", got)
	}
	if got := hex.EncodeToString(RFDiscover(ListenA, ListenB).Bytes()); got != "2103050280018101" {
		t.Fatalf("RF_DISCOVER_CMD is %s", got)
	}
	d := Packet{MT: MTData, PBF: true, Conn: 0, Payload: []byte{0x90, 0x00}}
	p, err := ParsePacket(d.Bytes())
	if err != nil || p.MT != MTData || !p.PBF || p.Conn != 0 || !bytes.Equal(p.Payload, d.Payload) {
		t.Fatalf("data packet round trip: %+v %v", p, err)
	}
	if d.Bytes()[0] != 0x10 {
		t.Fatalf("a segmented data packet's first byte is %02X", d.Bytes()[0])
	}
	if _, err := ParsePacket([]byte{0x40, 0x00, 0x05, 0x00}); err == nil {
		t.Fatal("a packet shorter than its length parsed")
	}
}

func TestInit(t *testing.T) {
	for _, v2 := range []bool{true, false} {
		f, n := newFake(t, v2)
		f.waitSent(t, MTCmd, GIDCore, OIDCoreSetConfig, "01800101")
		init := f.sent(MTCmd, GIDCore, OIDCoreInit)
		want := map[bool]string{true: "0000", false: ""}[v2]
		if len(init) != 1 || init[0] != want {
			t.Fatalf("v2=%v: CORE_INIT_CMD payloads %v, want %q", v2, init, want)
		}
		if name := n.Info().Name; !strings.Contains(name, map[bool]string{true: "NCI 2.0", false: "NCI 1.0"}[v2]) {
			t.Fatalf("v2=%v: controller %q", v2, name)
		}
	}
}

func TestHCEExchange(t *testing.T) {
	f, n := newFake(t, true)
	aid := []byte{0xF0, 0x01, 0x02, 0x03, 0x04}
	if err := n.Present(&Identity{HCE: true, AIDs: [][]byte{aid}}); err != nil {
		t.Fatal(err)
	}
	f.waitSent(t, MTCmd, GIDCore, OIDCoreSetConfig, "023300320120")
	f.waitSent(t, MTCmd, GIDRF, OIDRFListenRouting, "0002010300010402070001F001020304")
	f.waitSent(t, MTCmd, GIDRF, OIDRFDiscover, "0280018101")
	f.ntf(GIDRF, OIDRFFieldInfo, 0x01)
	if e := nextEvent(t, n); e.Type != EventField || !e.On {
		t.Fatalf("want the field on, got %+v", e)
	}
	f.ntf(GIDRF, OIDRFIntfActivated, 0x01, IntfISODep, ProtoISODep, ListenA, 16, 1, 0, ListenA, 0, 0, 0)
	if e := nextEvent(t, n); e.Type != EventActivated || !e.ISODep {
		t.Fatalf("want an ISO-DEP activation, got %+v", e)
	}
	sel := Select(aid)
	f.send(Packet{MT: MTData, PBF: true, Payload: sel[:4]})
	f.send(Packet{MT: MTData, Payload: sel[4:]})
	if e := nextEvent(t, n); e.Type != EventAPDU || !bytes.Equal(e.APDU, sel) {
		t.Fatalf("want the reassembled SELECT, got %+v", e)
	}
	resp := append(bytes.Repeat([]byte{0xAB}, 20), 0x90, 0x00)
	done := make(chan error, 1)
	go func() { done <- n.Respond(resp) }()
	f.waitSent(t, MTData, 0, 0, strings.ToUpper(hex.EncodeToString(resp[:16])))
	select {
	case err := <-done:
		t.Fatalf("the second segment went without a credit: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	f.ntf(GIDCore, OIDCoreConnCredits, 0x01, 0x00, 0x01)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	data := f.sent(MTData, 0, 0)
	if len(data) != 2 || data[0]+data[1] != strings.ToUpper(hex.EncodeToString(resp)) {
		t.Fatalf("data sent: %v", data)
	}
	f.ntf(GIDRF, OIDRFDeactivate, DeactSleep, 0x01)
	if e := nextEvent(t, n); e.Type != EventDeactivated || !e.Sleep {
		t.Fatalf("want the reader's deselect, got %+v", e)
	}
}

func TestUIDCard(t *testing.T) {
	f, n := newFake(t, true)
	if err := n.Present(&Identity{UID: []byte{0x04, 0xD5, 0xE6, 0xF7}}); err != nil {
		t.Fatal(err)
	}
	f.waitSent(t, MTCmd, GIDCore, OIDCoreSetConfig, "02330404D5E6F7320108")
	f.waitSent(t, MTCmd, GIDRF, OIDRFDiscover, "018001")
	if err := n.Present(nil); err != nil {
		t.Fatal(err)
	}
	f.waitSent(t, MTCmd, GIDRF, OIDRFDeactivate, "00")
}

// A door card's UID with ISO-DEP besides it: the UID fixed, SAK 0x20, the
// AIDs routed to the host.
func TestDualIdentity(t *testing.T) {
	f, n := newFake(t, true)
	if err := n.Present(&Identity{UID: []byte{0x04, 0xD5, 0xE6, 0xF7}, HCE: true, AIDs: [][]byte{{0xF0, 1, 2, 3, 4}}}); err != nil {
		t.Fatal(err)
	}
	f.waitSent(t, MTCmd, GIDCore, OIDCoreSetConfig, "02330404D5E6F7320120")
	f.waitSent(t, MTCmd, GIDRF, OIDRFListenRouting, "0002010300010402070001F001020304")
	f.waitSent(t, MTCmd, GIDRF, OIDRFDiscover, "0280018101")
}

func TestReaderMode(t *testing.T) {
	f, n := newFake(t, true)
	if err := n.Present(&Identity{HCE: true}); err != nil {
		t.Fatal(err)
	}
	type res struct {
		tag Tag
		err error
	}
	done := make(chan res, 1)
	go func() {
		tag, err := n.Read(within(t, 2*time.Second))
		done <- res{tag, err}
	}()
	f.waitSent(t, MTCmd, GIDRF, OIDRFDiscover, "0200010101")
	tech := []byte{0x04, 0x00, 0x04, 0x04, 0xA1, 0xB2, 0xC3, 0x01, 0x08}
	f.ntf(GIDRF, OIDRFIntfActivated, append(append([]byte{0x01, IntfFrame, 0x80, PollA, 0xFF, 0x00, byte(len(tech))}, tech...), PollA, 0, 0, 0)...)
	r := <-done
	if r.err != nil || hex.EncodeToString(r.tag.UID) != "04a1b2c3" || r.tag.SAK != 0x08 || r.tag.ATQA != [2]byte{0x04, 0x00} {
		t.Fatalf("read %+v, %v", r.tag, r.err)
	}
	if got := f.sent(MTCmd, GIDRF, OIDRFDiscover); len(got) != 3 || got[2] != "0280018101" {
		t.Fatalf("after reading, the phone listens again as before: discovers %v", got)
	}
}

func TestListenRoutingSplits(t *testing.T) {
	var rs []Route
	for i := 0; i < 20; i++ {
		rs = append(rs, AIDRoute(bytes.Repeat([]byte{byte(i)}, 16)))
	}
	ps := ListenRouting(rs)
	if len(ps) < 2 {
		t.Fatalf("%d routes fit one command", len(rs))
	}
	total := 0
	for i, p := range ps {
		if len(p.Payload) > 255 {
			t.Fatalf("command %d carries %d bytes", i, len(p.Payload))
		}
		if more := p.Payload[0] == 0x01; more != (i < len(ps)-1) {
			t.Fatalf("command %d of %d: more=%v", i, len(ps), more)
		}
		total += int(p.Payload[1])
	}
	if total != len(rs) {
		t.Fatalf("%d entries sent of %d", total, len(rs))
	}
}
