package obex

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// link is a pair of connected ends: a byte stream (RFCOMM) or one packet
// per read and write (L2CAP).
func link(t *testing.T, packets bool) (a, b io.ReadWriteCloser) {
	t.Helper()
	if !packets {
		x, y := net.Pipe()
		t.Cleanup(func() { x.Close(); y.Close() })
		return x, y
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		_ = unix.SetNonblock(fd, true)
	}
	x, y := os.NewFile(uintptr(fds[0]), "a"), os.NewFile(uintptr(fds[1]), "b")
	t.Cleanup(func() { x.Close(); y.Close() })
	return x, y
}

func reqPrefix(code byte) int {
	switch code {
	case opConnect:
		return 4
	case opSetPath:
		return 2
	}
	return 0
}

// peer plays the other device on b: each request the client sends is kept
// and answered with what answer gives (none, one or several packets).
type peer struct {
	mu   sync.Mutex
	reqs []*packet
	raw  [][]byte
	sent int
}

func (p *peer) got() []*packet {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*packet(nil), p.reqs...)
}

func playPeer(t *testing.T, b io.ReadWriter, mtu int, answer func(req *packet) []*packet) *peer {
	p := &peer{}
	c := newConn(b, mtu)
	c.max = MaxPacket
	go func() {
		for {
			var hdr [3]byte
			if _, err := io.ReadFull(c.r, hdr[:]); err != nil {
				return
			}
			raw := make([]byte, int(binary.BigEndian.Uint16(hdr[1:])))
			copy(raw, hdr[:])
			if len(raw) < 3 {
				return
			}
			if _, err := io.ReadFull(c.r, raw[3:]); err != nil {
				return
			}
			req, err := decode(raw, reqPrefix(raw[0]))
			if err != nil {
				t.Errorf("the client sent a malformed packet %x", raw)
				return
			}
			p.mu.Lock()
			p.reqs, p.raw = append(p.reqs, req), append(p.raw, raw)
			p.mu.Unlock()
			for _, r := range answer(req) {
				if err := c.write(r); err != nil {
					return
				}
				p.mu.Lock()
				p.sent++
				p.mu.Unlock()
			}
		}
	}()
	return p
}

func okConnect(id uint32) *packet {
	return &packet{code: Success, prefix: connectPrefix(), headers: []header{{id: hConnID, n: id}, {id: hWho, data: PBAPTarget}}}
}

// GOEP 2.0 over L2CAP: the client asks for Single Response Mode in its
// first GET and, once the server's first answer turns it on, sends no more
// requests while the object comes; over a stream it does not ask, and
// with a server that does not turn it on it asks for each packet.
func TestClientSRMGet(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789"), 300)
	for _, tc := range []struct {
		name          string
		packets, srm  bool
		wantRequests  int
		wantSRMHeader bool
	}{
		{"l2cap", true, true, 2, true},
		{"l2cap, server without SRM", true, false, 2 + 4, true},
		{"rfcomm", false, false, 2 + 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := link(t, tc.packets)
			mtu := map[bool]int{true: 700}[tc.packets]
			var out [][]byte
			for rest := body; len(rest) > 0; {
				n := min(len(rest), 650)
				out, rest = append(out, rest[:n]), rest[n:]
			}
			p := playPeer(t, b, mtu, func(req *packet) []*packet {
				switch req.code {
				case opConnect:
					return []*packet{okConnect(7)}
				case opGet | final:
					var rs []*packet
					for i, chunk := range out {
						r := &packet{code: Continue, headers: []header{{id: hBody, data: chunk}}}
						if i == len(out)-1 {
							r = &packet{code: Success, headers: []header{{id: hEndOfBody, data: chunk}}}
						}
						if i == 0 && tc.srm && has1(req, hSRM, srmEnable) {
							r.headers = append([]header{{id: hSRM, n: srmEnable}}, r.headers...)
						}
						rs = append(rs, r)
						if !tc.srm {
							break
						}
					}
					if !tc.srm {
						out = out[1:]
					}
					return rs
				}
				return nil
			})
			cl, err := Dial(a, mtu, PBAPTarget)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := cl.Get("x-bt/phonebook", "telecom/pb.vcf", nil)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("got %d bytes, %v", len(got), err)
			}
			reqs := p.got()
			if len(reqs) != tc.wantRequests {
				t.Errorf("%d requests, want %d", len(reqs), tc.wantRequests)
			}
			if first := reqs[1]; has1(first, hSRM, srmEnable) != tc.wantSRMHeader {
				t.Errorf("SRM asked for: %v, want %v", !tc.wantSRMHeader, tc.wantSRMHeader)
			}
			for _, r := range reqs[1:] {
				if len(r.headers) == 0 || r.headers[0].id != hConnID || r.headers[0].n != 7 {
					t.Errorf("a request without the Connection ID first: %+v", r.headers)
				}
			}
		})
	}
}

// A PUT in Single Response Mode: after the first answer the client sends
// without waiting, and the server answers only the last packet; one that
// asks it to wait (SRMP) gets one packet per answer meanwhile.
func TestClientSRMPut(t *testing.T) {
	data := bytes.Repeat([]byte("k"), 3000)
	for _, waits := range []int{0, 2} {
		a, b := link(t, true)
		answered := 0
		p := playPeer(t, b, 700, func(req *packet) []*packet {
			wait := []header{{id: hSRMP, n: srmpWait}}
			switch {
			case req.code == opConnect:
				return []*packet{{code: Success, prefix: connectPrefix()}}
			case req.code == opPut && answered == 0:
				answered++
				r := &packet{code: Continue, headers: []header{{id: hSRM, n: srmEnable}}}
				if waits > 0 {
					r.headers = append(r.headers, wait...)
				}
				return []*packet{r}
			case req.code == opPut && answered <= waits:
				answered++
				r := &packet{code: Continue}
				if answered <= waits {
					r.headers = wait
				}
				return []*packet{r}
			case req.code == opPut|final, req.code == opDisconnect:
				return []*packet{{code: Success}}
			}
			return nil
		})
		cl, err := Dial(a, 700, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := cl.Put("a.bin", "", data); err != nil {
			t.Fatalf("waits %d: %v", waits, err)
		}
		if err := cl.Close(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		p.mu.Lock()
		puts, sent := len(p.reqs)-2, p.sent
		p.mu.Unlock()
		if puts < 4 || answered != 1+waits || sent != 3+answered {
			t.Errorf("waits %d: %d packets, %d answers sent (%d to non-final ones)", waits, puts, sent, answered)
		}
	}
}

// Kario's server in Single Response Mode against its client, over an
// L2CAP-like link: a pushed file gets two answers, a phone book page
// takes one request; a push without a size that the inbox refuses midway
// gets that answer once, its packets still coming none, and the session
// stays in step.
func TestServerSRM(t *testing.T) {
	a, b := link(t, true)
	in := &inbox{accept: true}
	s := &Server{Inbox: in, Phonebook: book{allow: true}, MaxObject: 4000}
	go s.Serve(b, 300)
	rec := &countLink{ReadWriter: a}
	cl, err := Dial(rec, 300, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec.reset()
	if err := cl.Put("small.txt", "", bytes.Repeat([]byte("s"), 2000)); err != nil || len(in.got["small.txt"]) != 2000 {
		t.Fatal(err)
	}
	if w, r := rec.counts(); w < 6 || r != 2 {
		t.Errorf("put: %d packets sent, %d answers", w, r)
	}
	if err := cl.Close(); err != nil {
		t.Fatal(err)
	}

	a, b = link(t, true)
	go s.Serve(b, 300)
	c := newConn(a, 300)
	c.max = 300
	if err := c.write(&packet{code: opConnect, prefix: connectPrefix()}); err != nil {
		t.Fatal(err)
	}
	if r, err := c.read(func(byte) int { return 4 }); err != nil || r.code != Success {
		t.Fatalf("connect: %v", err)
	}
	chunk := bytes.Repeat([]byte("b"), 250)
	_ = c.write(&packet{code: opPut, headers: []header{textHeader(hName, "big.txt"), {id: hSRM, n: srmEnable}, {id: hBody, data: chunk}}})
	if r, err := c.read(noPrefix); err != nil || r.code != Continue || !has1(r, hSRM, srmEnable) {
		t.Fatalf("first answer: %v %+v", err, r)
	}
	for range 20 {
		_ = c.write(&packet{code: opPut, headers: []header{{id: hBody, data: chunk}}})
	}
	_ = c.write(&packet{code: opPut | final, headers: []header{{id: hEndOfBody, data: chunk}}})
	_ = c.write(&packet{code: opDisconnect})
	if r, err := c.read(noPrefix); err != nil || r.code != EntityTooLarge {
		t.Fatalf("refused: %v %+v", err, r)
	}
	if r, err := c.read(noPrefix); err != nil || r.code != Success {
		t.Fatalf("the disconnect's answer: %v %+v", err, r)
	}
	if in.got["big.txt"] != nil {
		t.Error("the refused file came in")
	}

	a, b = link(t, true)
	go s.Serve(b, 300)
	rec = &countLink{ReadWriter: a}
	cl, err = Dial(rec, 300, PBAPTarget)
	if err != nil {
		t.Fatal(err)
	}
	rec.reset()
	body, _, err := cl.Get("x-bt/phonebook", "telecom/pb.vcf", nil)
	if w, r := rec.counts(); err != nil || strings.Count(string(body), "BEGIN:VCARD") != 3 || w != 1 || r < 2 {
		t.Errorf("phone book: %v, %d requests for %d answers", err, w, r)
	}
}

// big is a service with one large object.
type big struct{}

func (big) Get(string, string, Params) ([]byte, Params, byte) {
	return bytes.Repeat([]byte("0123456789"), 300), Params{0x42: {1}}, Success
}
func (big) SetPath(string, bool) byte { return Success }

var bigTarget = []byte("big-object-test!")

type countLink struct {
	io.ReadWriter
	mu   sync.Mutex
	w, r int
}

func (l *countLink) Write(b []byte) (int, error) {
	l.mu.Lock()
	l.w++
	l.mu.Unlock()
	return l.ReadWriter.Write(b)
}

func (l *countLink) Read(b []byte) (int, error) {
	n, err := l.ReadWriter.Read(b)
	if n > 0 {
		l.mu.Lock()
		l.r++
		l.mu.Unlock()
	}
	return n, err
}

func (l *countLink) counts() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w, l.r
}

func (l *countLink) reset() {
	l.mu.Lock()
	l.w, l.r = 0, 0
	l.mu.Unlock()
}

// A GET asking the server to wait (SRMP) gets one response for that
// request, then the rest without more.
func TestServerSRMPWait(t *testing.T) {
	a, b := link(t, true)
	go (&Server{Services: map[string]func() Service{string(bigTarget): func() Service { return big{} }}}).Serve(b, 300)
	c := newConn(a, 300)
	c.max = 300
	if err := c.write(&packet{code: opConnect, prefix: connectPrefix(), headers: []header{{id: hTarget, data: bigTarget}}}); err != nil {
		t.Fatal(err)
	}
	if r, err := c.read(func(byte) int { return 4 }); err != nil || r.code != Success || !bytes.Equal(r.bytes(hWho), bigTarget) {
		t.Fatalf("connect: %v %+v", err, r)
	}
	get := &packet{code: opGet | final, headers: []header{{id: hConnID, n: connID}, typeHeader("x-test/big"), {id: hSRM, n: srmEnable}, {id: hSRMP, n: srmpWait}}}
	if err := c.write(get); err != nil {
		t.Fatal(err)
	}
	r, err := c.read(noPrefix)
	if err != nil || r.code != Continue || !has1(r, hSRM, srmEnable) || !bytes.Equal(r.bytes(hAppParams), []byte{0x42, 1, 1}) {
		t.Fatalf("first: %v %+v", err, r)
	}
	_ = a.(*os.File).SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := c.read(noPrefix); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("the server did not wait: %v", err)
	}
	_ = a.(*os.File).SetReadDeadline(time.Time{})
	if err := c.write(&packet{code: opGet | final, headers: []header{{id: hConnID, n: connID}}}); err != nil {
		t.Fatal(err)
	}
	n := 0
	for {
		r, err := c.read(noPrefix)
		if err != nil {
			t.Fatal(err)
		}
		n++
		if r.code == Success {
			break
		}
	}
	if n < 2 {
		t.Errorf("%d responses after the wait", n)
	}
}

// The server takes requests without a Connection ID (car kits leave it
// out) and refuses one with another (Service Unavailable).
func TestServerConnectionID(t *testing.T) {
	a, b := link(t, false)
	go (&Server{Phonebook: book{allow: true}}).Serve(b, 0)
	c := newConn(a, 0)
	c.max = MaxPacket
	_ = c.write(&packet{code: opConnect, prefix: connectPrefix(), headers: []header{{id: hTarget, data: PBAPTarget}}})
	r, err := c.read(func(byte) int { return 4 })
	if err != nil || r.code != Success || r.headers[0].id != hConnID || r.get(hWho) == nil || !bytes.Equal(r.bytes(hWho), PBAPTarget) {
		t.Fatalf("connect: %v %+v", err, r)
	}
	id := r.headers[0].n
	for _, tc := range []struct {
		hs   []header
		want byte
	}{
		{[]header{typeHeader("x-bt/phonebook"), textHeader(hName, "telecom/pb.vcf")}, Success},
		{[]header{{id: hConnID, n: id}, typeHeader("x-bt/phonebook"), textHeader(hName, "telecom/pb.vcf")}, Success},
		{[]header{{id: hConnID, n: id + 1}, typeHeader("x-bt/phonebook"), textHeader(hName, "telecom/pb.vcf")}, ServiceUnavailable},
	} {
		if err := c.write(&packet{code: opGet | final, headers: tc.hs}); err != nil {
			t.Fatal(err)
		}
		r, err := c.read(noPrefix)
		if err != nil || r.code != tc.want {
			t.Errorf("%+v: %v %x, want %x", tc.hs, err, r.code, tc.want)
		}
	}
}

// A car kit's property selector: in 2.1 only what it selects beside N and
// TEL, FN always in 3.0, the call time only when selected; none selects
// all.
func TestServerPropertySelector(t *testing.T) {
	cl, err := Dial(serve(t, &Server{Phonebook: book{allow: true}}, 0, false), 0, PBAPTarget)
	if err != nil {
		t.Fatal(err)
	}
	get := func(object string, sel uint64, v30 bool) string {
		p := Params{tagPropertySelector: U64(sel)}
		if v30 {
			p[tagFormat] = []byte{1}
		}
		body, _, err := cl.Get("x-bt/phonebook", object, p.Encode())
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	if s := get("telecom/pb.vcf", PropN|PropTel, false); strings.Contains(s, "FN") || !strings.Contains(s, "N;CHARSET=UTF-8:"+zhang+";;;;") || !strings.Contains(s, "TEL;CELL:") {
		t.Errorf("N and TEL in 2.1:\n%s", s)
	}
	if s := get("telecom/pb.vcf", PropN|PropTel, true); !strings.Contains(s, "FN:"+zhang) {
		t.Errorf("3.0 without FN:\n%s", s)
	}
	if s := get("telecom/mch.vcf", PropN|PropTel, false); strings.Contains(s, "X-IRMC-CALL-DATETIME") {
		t.Errorf("an unselected call time:\n%s", s)
	}
	for _, sel := range []uint64{0, PropN | PropTel | PropCallDateTime} {
		if s := get("telecom/mch.vcf", sel, false); !strings.Contains(s, "X-IRMC-CALL-DATETIME;MISSED:") || strings.Contains(s, "FN") != (sel == 0) {
			t.Errorf("selector %x:\n%s", sel, s)
		}
	}
}

// What the clients send, byte for byte where the profiles fix it: the
// CONNECT with its target (and PBAP 1.2's features only to a 1.2 server),
// the Connection ID first in every request after it, a pull's type, name
// and application parameters (the property selector by the profile's bit
// numbers), MAP's folders and GetMessage's mandatory parameters, SRM over
// L2CAP only.
func TestClientConformance(t *testing.T) {
	// PBAP 1.2 §5.1.4.1: VERSION 0, FN 1, N 2, TEL 7, ORG 16, NOTE 17,
	// X-IRMC-CALL-DATETIME 28.
	if PullProps != 1<<0|1<<1|1<<2|1<<7|1<<16|1<<17|1<<28 || PropPhoto != 1<<3 {
		t.Fatalf("property bits %x", uint64(PullProps))
	}
	for _, tc := range []struct {
		version  uint16
		packets  bool
		features bool
	}{{0, false, false}, {0x0101, false, false}, {0x0102, true, true}, {0x0103, false, true}} {
		a, b := link(t, tc.packets)
		mtu := map[bool]int{true: 700}[tc.packets]
		p := playPeer(t, b, mtu, func(req *packet) []*packet {
			switch req.code &^ final {
			case opConnect &^ final:
				return []*packet{okConnect(0x10203)}
			case opGet:
				return []*packet{{code: Success, headers: []header{{id: hAppParams, data: Params{tagPhonebookSize: U16(5)}.Encode()}, {id: hEndOfBody}}}}
			}
			return []*packet{{code: Success}}
		})
		pb, err := DialPhoneBook(a, mtu, tc.version, time.Second, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := pb.Size("telecom/ich.vcf"); n != 5 || err != nil {
			t.Fatalf("size %d %v", n, err)
		}
		if _, err := pb.Pull("telecom/pb.vcf", 200, 100); err != nil {
			t.Fatal(err)
		}
		if err := pb.Close(); err != nil {
			t.Fatal(err)
		}
		reqs := p.got()
		p.mu.Lock()
		connect := hex.EncodeToString(p.raw[0])
		p.mu.Unlock()
		want := "80001a10007fff460013" + hex.EncodeToString(PBAPTarget)
		if tc.features {
			want = "80002310007fff460013" + hex.EncodeToString(PBAPTarget) + "4c0009" + "100400000003"
		}
		if connect != want {
			t.Errorf("version %04x: CONNECT %s, want %s", tc.version, connect, want)
		}
		for _, r := range reqs[1:] {
			if r.headers[0].id != hConnID || r.headers[0].n != 0x10203 {
				t.Errorf("%x without the Connection ID first", r.code)
			}
			if has1(r, hSRM, srmEnable) != (tc.packets && r.code == opGet|final) {
				t.Errorf("SRM in %x on packets %v", r.code, tc.packets)
			}
		}
		pull := reqs[2]
		if typeOf(pull) != "x-bt/phonebook" || hex.EncodeToString(pull.bytes(hType)) != hex.EncodeToString([]byte("x-bt/phonebook\x00")) {
			t.Errorf("type %q", pull.bytes(hType))
		}
		if name := hex.EncodeToString(pull.bytes(hName)); name != hex.EncodeToString(append(utf16be("telecom/pb.vcf"), 0, 0)) {
			t.Errorf("name %s", name)
		}
		if ps := hex.EncodeToString(pull.bytes(hAppParams)); ps != "04020064050200c80608000000001003008707010"+"0" {
			t.Errorf("pull parameters %s", ps)
		}
		if ps := hex.EncodeToString(reqs[1].bytes(hAppParams)); ps != "04020000" {
			t.Errorf("size parameters %s", ps)
		}
		if reqs[len(reqs)-1].code != opDisconnect {
			t.Errorf("last %x", reqs[len(reqs)-1].code)
		}
	}

	a, b := link(t, false)
	p := playPeer(t, b, 0, func(req *packet) []*packet {
		switch req.code &^ final {
		case opConnect &^ final:
			return []*packet{{code: Success, prefix: connectPrefix(), headers: []header{{id: hConnID, n: 1}, {id: hWho, data: MASTarget}}}}
		case opGet:
			return []*packet{{code: Success, headers: []header{{id: hEndOfBody, data: []byte("BEGIN:BMSG\r\nTYPE:SMS_GSM\r\nBEGIN:BENV\r\nBEGIN:BBODY\r\nBEGIN:MSG\r\nhi\r\nEND:MSG\r\nEND:BBODY\r\nEND:BENV\r\nEND:BMSG\r\n")}}}}
		}
		return []*packet{{code: Success}}
	})
	m, err := DialMessages(a, 0, time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.List("inbox", 256, 256, time.UTC); err != nil {
		t.Fatal(err)
	}
	if msg, err := m.Message("100000000000005"); err != nil || msg.Text != "hi" {
		t.Fatalf("message %+v %v", msg, err)
	}
	reqs := p.got()
	p.mu.Lock()
	connect := hex.EncodeToString(p.raw[0])
	p.mu.Unlock()
	if connect != "80001a10007fff460013"+hex.EncodeToString(MASTarget) {
		t.Errorf("MAP CONNECT %s", connect)
	}
	for i, f := range []string{"telecom", "msg"} {
		r := reqs[1+i]
		if r.code != opSetPath || !bytes.Equal(r.prefix, []byte{0x02, 0}) || r.headers[0].id != hConnID {
			t.Errorf("SETPATH %d: %x %x %+v", i, r.code, r.prefix, r.headers)
		}
		if name, _ := r.text(hName); name != f {
			t.Errorf("SETPATH to %q", name)
		}
	}
	list, get := reqs[3], reqs[4]
	if name, _ := list.text(hName); typeOf(list) != "x-bt/MAP-msg-listing" || name != "inbox" || hex.EncodeToString(list.bytes(hAppParams)) != "010201000202010003010"+"4" {
		t.Errorf("listing: %q %q %x", typeOf(list), name, list.bytes(hAppParams))
	}
	if name, _ := get.text(hName); typeOf(get) != "x-bt/message" || name != "100000000000005" || hex.EncodeToString(get.bytes(hAppParams)) != "0a01001401"+"01" {
		t.Errorf("message: %q %q %x", typeOf(get), name, get.bytes(hAppParams))
	}
}

func utf16be(s string) []byte {
	var b []byte
	for _, r := range s {
		b = append(b, 0, byte(r))
	}
	return b
}

// A phone that does not answer in time (its user did not decide) ends the
// CONNECT with ErrNoAnswer; one that refuses gives its answer.
func TestClientConsent(t *testing.T) {
	a, b := link(t, false)
	go io.Copy(io.Discard, b)
	start := time.Now()
	if _, err := DialPhoneBook(a, 0, 0, 50*time.Millisecond, time.Second); !errors.Is(err, ErrNoAnswer) || time.Since(start) > 2*time.Second {
		t.Errorf("no answer: %v after %v", err, time.Since(start))
	}
	a, b = link(t, true)
	playPeer(t, b, 700, func(*packet) []*packet { return []*packet{{code: Forbidden, prefix: connectPrefix()}} })
	if _, err := DialMessages(a, 700, time.Second, time.Second); !errors.Is(err, ResponseError(Forbidden)) {
		t.Errorf("refused: %v", err)
	}
	// A wait for the user and none after it: an answer slower than that
	// wait still comes.
	a, b = link(t, false)
	playPeer(t, b, 0, func(req *packet) []*packet {
		if req.code == opConnect {
			return []*packet{okConnect(1)}
		}
		time.Sleep(150 * time.Millisecond)
		return []*packet{{code: Success, headers: []header{{id: hEndOfBody, data: []byte("x")}}}}
	})
	cl, err := DialWith(a, 0, Connect{Target: PBAPTarget, Wait: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cl.Get("x-bt/phonebook", "telecom/pb.vcf", nil); err != nil {
		t.Errorf("after the wait: %v", err)
	}
}

// A phone book page past the client's bound ends the pull.
func TestClientMaxObject(t *testing.T) {
	a, b := link(t, false)
	chunk := bytes.Repeat([]byte("x"), 30000)
	playPeer(t, b, 0, func(req *packet) []*packet {
		if req.code == opConnect {
			return []*packet{okConnect(1)}
		}
		return []*packet{{code: Continue, headers: []header{{id: hBody, data: chunk}}}}
	})
	pb, err := DialPhoneBook(a, 0, 0, time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pb.cl.MaxObject = 100000
	if _, err := pb.Pull("telecom/pb.vcf", 0, 100); !errors.Is(err, ErrTooLarge) {
		t.Errorf("an endless object: %v", err)
	}
}
