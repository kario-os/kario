package obex

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// peerLink is a link to a fuzzed peer: it reads what the peer sent and
// keeps what this side answers.
type peerLink struct {
	io.Reader
	sent int
}

func (l *peerLink) Write(p []byte) (int, error) {
	l.sent += len(p)
	return len(p), nil
}

func packets(ps ...*packet) []byte {
	var b []byte
	for _, p := range ps {
		b = append(b, p.encode()...)
	}
	return b
}

func sessionSeeds() [][]byte {
	connect := func(target []byte) *packet {
		p := &packet{code: opConnect, prefix: connectPrefix()}
		if target != nil {
			p.headers = []header{{id: hTarget, data: target}}
		}
		return p
	}
	params := Params{tagMaxListCount: U16(2), tagListStartOffset: U16(1), tagFormat: {1}, tagSearchValue: []byte("li")}.Encode()
	return [][]byte{
		packets(connect(nil),
			&packet{code: opPut, headers: []header{textHeader(hName, report), {id: hLength, n: 4}, typeHeader("text/plain"), {id: hBody, data: []byte("ab")}}},
			&packet{code: opPut | final, headers: []header{{id: hEndOfBody, data: []byte("cd")}}},
			&packet{code: opDisconnect}),
		packets(connect(PBAPTarget),
			&packet{code: opGet | final, headers: []header{{id: hConnID, n: 1}, typeHeader("x-bt/phonebook"), textHeader(hName, "telecom/pb.vcf"), {id: hAppParams, data: params}}},
			&packet{code: opGet | final, headers: []header{{id: hConnID, n: 1}}},
			&packet{code: opSetPath, prefix: []byte{2, 0}, headers: []header{textHeader(hName, "telecom")}},
			&packet{code: opSetPath, prefix: []byte{2, 0}, headers: []header{textHeader(hName, "cch")}},
			&packet{code: opGet, headers: []header{typeHeader("x-bt/vcard-listing")}},
			&packet{code: opGet | final, headers: []header{{id: hAppParams, data: params}}},
			&packet{code: opGet | final, headers: []header{typeHeader("x-bt/vcard"), textHeader(hName, "1.vcf")}},
			&packet{code: opSetPath, prefix: []byte{3, 0}},
			&packet{code: opAbort}),
	}
}

// FuzzServe is a Bluetooth peer talking to the phone's Object Push inbox
// and phone book: any packets end in answers, and the session ends with
// the link.
func FuzzServe(f *testing.F) {
	for _, s := range sessionSeeds() {
		f.Add(s, uint16(0))
		f.Add(s, uint16(300))
	}
	f.Fuzz(func(t *testing.T, data []byte, mtu uint16) {
		if len(data) > 1<<16 {
			return
		}
		in := &inbox{accept: true}
		s := &Server{Inbox: in, Phonebook: book{allow: true}, MaxObject: 4096}
		link := &peerLink{Reader: bytes.NewReader(data)}
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.Serve(link, int(mtu))
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the session outlived the link")
		}
		for name, b := range in.got {
			if len(b) > int(s.MaxObject) {
				t.Fatalf("%s: %d bytes taken, the inbox takes %d", name, len(b), s.MaxObject)
			}
		}
	})
}

// FuzzDecode is one packet: what decodes encodes to a packet that
// decodes the same.
func FuzzDecode(f *testing.F) {
	for _, s := range sessionSeeds() {
		for len(s) >= 3 {
			n := min(int(s[1])<<8|int(s[2]), len(s))
			f.Add(s[:n], uint8(0))
			s = s[max(n, 3):]
		}
	}
	f.Add((&packet{code: opConnect, prefix: connectPrefix()}).encode(), uint8(4))
	f.Fuzz(func(t *testing.T, data []byte, prefix uint8) {
		p, err := decode(data, int(prefix%8))
		if err != nil {
			return
		}
		raw := p.encode()
		q, err := decode(raw, len(p.prefix))
		if err != nil {
			t.Fatalf("%x decodes, its encoding %x does not: %v", data, raw, err)
		}
		if q.code != p.code || !bytes.Equal(q.prefix, p.prefix) || len(q.headers) != len(p.headers) {
			t.Fatalf("round trip: %+v, then %+v", p, q)
		}
		for i := range p.headers {
			a, b := p.headers[i], q.headers[i]
			if a.id != b.id || a.n != b.n || !bytes.Equal(a.data, b.data) {
				t.Fatalf("header %d: %+v, then %+v", i, a, b)
			}
		}
	})
}

// FuzzParams is PBAP's application parameters: what parses encodes to
// parameters that parse the same.
func FuzzParams(f *testing.F) {
	f.Add(Params{tagMaxListCount: U16(0)}.Encode())
	f.Add(Params{tagFormat: {1}, tagListStartOffset: U16(1), tagMaxListCount: U16(1), tagSearchValue: []byte("zh")}.Encode())
	f.Add([]byte{4, 2, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		p := ParseParams(data)
		if q := ParseParams(p.Encode()); !reflect.DeepEqual(p, q) {
			t.Fatalf("round trip: %v, then %v", p, q)
		}
	})
}

// FuzzOldPhone is an old phone Kario moves from: whatever its phone book
// and message servers answer, the pulls end with a result or an error,
// and what the clients take stays within their bound.
func FuzzOldPhone(f *testing.F) {
	ok := &packet{code: Success, prefix: connectPrefix(), headers: []header{{id: hConnID, n: 1}}}
	f.Add(packets(ok,
		&packet{code: Success, headers: []header{{id: hAppParams, data: Params{tagPhonebookSize: U16(2)}.Encode()}, {id: hEndOfBody}}},
		&packet{code: Continue, headers: []header{{id: hSRM, n: srmEnable}, {id: hBody, data: []byte("BEGIN:VCARD\r\nTEL:1\r\n")}}},
		&packet{code: Success, headers: []header{{id: hEndOfBody, data: []byte("END:VCARD\r\n")}}},
		&packet{code: Success, headers: []header{{id: hEndOfBody, data: []byte(`<vCard-listing><card handle="0.vcf"/></vCard-listing>`)}}}), uint16(0))
	f.Add(packets(ok, &packet{code: Success}, &packet{code: Success},
		&packet{code: Success, headers: []header{{id: hEndOfBody, data: []byte(`<MAP-msg-listing><msg handle="1" datetime="20261116T101500"/></MAP-msg-listing>`)}}},
		&packet{code: Success, headers: []header{{id: hEndOfBody, data: []byte("BEGIN:BMSG\r\nBEGIN:BENV\r\nBEGIN:BBODY\r\nBEGIN:MSG\r\nx\r\nEND:MSG\r\nEND:BBODY\r\nEND:BENV\r\nEND:BMSG\r\n")}}}), uint16(700))
	f.Fuzz(func(t *testing.T, data []byte, mtu uint16) {
		if len(data) > 1<<16 {
			return
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			if pb, err := DialPhoneBook(&peerLink{Reader: bytes.NewReader(data)}, int(mtu), PBAP12, 0, 0); err == nil {
				pb.cl.MaxObject = 4096
				n, _ := pb.Size("telecom/pb.vcf")
				_ = pb.PullAll("telecom/pb.vcf", n, 2, func(cs []string) error {
					for _, c := range cs {
						if len(c) > 2*4096+2 {
							t.Errorf("a card of %d bytes", len(c))
						}
						_, _ = ParseCall(c, "cch", time.UTC)
					}
					return nil
				})
				_, _ = pb.OwnerFirst()
			}
			if m, err := DialMessages(&peerLink{Reader: bytes.NewReader(data)}, int(mtu), 0, 0); err == nil {
				_ = m.ListAll("inbox", -1, 2, time.UTC, func(es []Entry) error {
					for _, e := range es {
						_, _ = m.Message(e.Handle)
					}
					return nil
				})
			}
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the pull outlived the link")
		}
	})
}

// FuzzParsers is what an old phone's objects hold: the parsers end, and
// a message's text is valid UTF-8.
func FuzzParsers(f *testing.F) {
	for _, name := range []string{"android/msg/100000000000005.bmsg", "android/msg/200000000000002.bmsg", "android/inbox.xml", "android/ich.vcf", "iphone/mch.vcf"} {
		if b, err := os.ReadFile("btphone/fixtures/" + name); err == nil {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if m, err := ParseBMessage(data); err == nil && !utf8.ValidString(m.Text) {
			t.Fatalf("text %q", m.Text)
		}
		_ = ParseListing(data, time.UTC)
		for _, c := range SplitVCards(data) {
			if !strings.HasSuffix(c, "\r\n") {
				t.Fatalf("card %q", c)
			}
			_, _ = ParseCall(c, "ich", time.UTC)
		}
	})
}

// FuzzClient is a peer the phone sends a file to (Object Push) or reads
// from: whatever it answers ends the exchange with a result or an error.
func FuzzClient(f *testing.F) {
	ok := &packet{code: Success, prefix: connectPrefix()}
	f.Add(packets(ok, &packet{code: Continue}, &packet{code: Success}, &packet{code: Success}))
	f.Add(packets(&packet{code: Success, prefix: []byte{0x10, 0, 0, 0xff}, headers: []header{{id: hConnID, n: 1}}},
		&packet{code: Continue, headers: []header{{id: hBody, data: []byte("BEGIN:VCARD")}}},
		&packet{code: Success, headers: []header{{id: hAppParams, data: Params{tagPhonebookSize: U16(3)}.Encode()}, {id: hEndOfBody, data: []byte("END:VCARD")}}}))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			link := &peerLink{Reader: bytes.NewReader(data)}
			cl, err := Dial(link, 0, nil)
			if err != nil {
				return
			}
			cl.Put("a.txt", "text/plain", bytes.Repeat([]byte("x"), 600))
			cl.Get("x-bt/phonebook", "telecom/pb.vcf", nil)
			cl.SetPath("telecom", false)
			cl.Close()
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the exchange outlived the link")
		}
	})
}
