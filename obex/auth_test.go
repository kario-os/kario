package obex

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

var seq = [nonceSize]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

const seqHex = "000102030405060708090a0b0c0d0e0f"

// Known answers of the digest (IrOBEX 1.2 §3.5.2: the MD5 of the nonce, a
// colon and the password), worked out with coreutils' md5sum and Python's
// hashlib, and the headers' bytes: a challenge's nonce (and options), a
// response's digest, user ID and the nonce it answers, as Android's OBEX
// (javax.obex) writes them.
func TestAuthDigest(t *testing.T) {
	for _, v := range []struct {
		nonce          []byte
		password, want string
	}{
		{seq[:], "1234", "82d59fb96d7d74afc3ab11c3bc77b2e7"},
		{[]byte("0123456789abcdef"), "0000", "e66d8139b5b39c9ecb1dcb2b978a60c1"},
		{bytes.Repeat([]byte{0xff}, nonceSize), "Kario-OBEX", "ea326a1ced80332258225e2eece2a131"},
		{seq[:], "", "4fccbadeeede45ba92007144a1cd96c7"},
	} {
		if d := digest(v.nonce, v.password); hex.EncodeToString(d[:]) != v.want {
			t.Errorf("%x, %q: %x, want %s", v.nonce, v.password, d, v.want)
		}
	}
	c := Challenge{Nonce: seq}
	if got := hex.EncodeToString(c.encode()); got != "0010"+seqHex {
		t.Errorf("challenge %s", got)
	}
	if got := hex.EncodeToString(Challenge{Nonce: seq, UserID: true, ReadOnly: true}.encode()); got != "0010"+seqHex+"010103" {
		t.Errorf("challenge with options %s", got)
	}
	if got := hex.EncodeToString(response(c, "", "1234").data); got != "0010"+"82d59fb96d7d74afc3ab11c3bc77b2e7"+"0210"+seqHex {
		t.Errorf("response %s", got)
	}
	if got := hex.EncodeToString(response(c, "kario", "1234").data); got != "0010"+"82d59fb96d7d74afc3ab11c3bc77b2e7"+"01056b6172696f"+"0210"+seqHex {
		t.Errorf("response with a user ID %s", got)
	}
	p := &packet{code: opConnect, prefix: connectPrefix(), headers: []header{response(c, "", "1234")}}
	if got := hex.EncodeToString(p.encode()); got != "80002e10007fff"+"4e0027"+"0010"+"82d59fb96d7d74afc3ab11c3bc77b2e7"+"0210"+seqHex {
		t.Errorf("CONNECT %s", got)
	}
	// javax.obex checks a response without the colon; IrOBEX has it.
	noColon, _ := hex.DecodeString("0010" + "1078467703519d755fdf9562ebd869c8" + "0210" + seqHex)
	if proves(noColon, seq, "1234") {
		t.Error("a digest without the colon proves the password")
	}
}

// The triplets are the peer's: one past the end, a nonce or digest of
// another size make the header malformed; the first of a tag counts,
// unknown tags are skipped; a response with another nonce, or for another
// password, proves nothing.
func TestAuthParse(t *testing.T) {
	nonce := append([]byte{tagNonce, nonceSize}, seq[:]...)
	for _, b := range [][]byte{nil, {}, nonce[:17], append([]byte{0, 15}, seq[:15]...), append(append([]byte{}, nonce...), 1),
		append(append([]byte{}, nonce...), 2, 5, 'a'), {1, 1, 1}, append([]byte{0, 17}, append(seq[:], 0)...)} {
		if c, err := parseChallenge(b); err == nil {
			t.Errorf("%x: %+v", b, c)
		}
	}
	for _, tc := range []struct {
		b    []byte
		want Challenge
	}{
		{nonce, Challenge{Nonce: seq}},
		{append(append([]byte{}, nonce...), 2, 6, 0, 'K', 'a', 'r', 'i', 'o'), Challenge{Nonce: seq, Realm: "Kario"}},
		{append(append([]byte{2, 2, 0, 'A'}, nonce...), 2, 2, 0, 'B'), Challenge{Nonce: seq, Realm: "A"}},
		{append([]byte{tagOptions, 1, optUserID}, nonce...), Challenge{Nonce: seq, UserID: true}},
		{append(append([]byte{tagOptions, 0, 0x7f, 3, 1, 2, 3}, nonce...), tagOptions, 1, optReadOnly), Challenge{Nonce: seq}},
		{append(append([]byte{}, nonce...), append([]byte{tagNonce, nonceSize}, bytes.Repeat([]byte{9}, nonceSize)...)...), Challenge{Nonce: seq}},
	} {
		if c, err := parseChallenge(tc.b); err != nil || c != tc.want {
			t.Errorf("%x: %+v %v, want %+v", tc.b, c, err, tc.want)
		}
	}
	right := response(Challenge{Nonce: seq}, "", "1234").data
	other := seq
	other[0] = 0xff
	for _, tc := range []struct {
		b    []byte
		want bool
	}{
		{right, true},
		{right[:18], true},
		{response(Challenge{Nonce: seq}, "kario", "1234").data, true},
		{response(Challenge{Nonce: seq}, "", "1235").data, false},
		{response(Challenge{Nonce: other}, "", "1234").data, false},
		{append(append([]byte{}, right[:18]...), tagAnswered, nonceSize-1), false},
		{append([]byte{tagDigest, 15}, right[2:17]...), false},
		{right[:17], false},
		{nil, false},
	} {
		if got := proves(tc.b, seq, "1234"); got != tc.want {
			t.Errorf("%x proves: %v", tc.b, got)
		}
	}
}

// A realm's first byte names its character set (IrOBEX 1.2 §3.5.2.1):
// ASCII, ISO-8859-1 to -9 and UTF-16 in network order decode to the text
// they hold (the bytes worked out with Python's codecs); a byte the set
// does not have, a lone surrogate or an odd byte of UTF-16 is U+FFFD;
// NULs at the end go; another set, or nothing after the set's byte, is
// no realm. The realm is the peer's text as it is: a line break or an
// escape stays for whoever shows it to deal with.
func TestAuthRealm(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
	}{
		{"\x00PBAP Server", "PBAP Server"},
		{"\x00Car\x80kit\x00\x00", "Car\uFFFDkit"},
		{"\x01\x43\x61\x66\xe9\x20\x4b\x61\x72\x69\x6f", "Café Kario"},
		{"\x02\xa3\xf3\x64\xbc", "Łódź"},
		{"\x03\x47\xb1\x61\x77\x64\x65\x78", "Għawdex"},
		{"\x03\xa5", "\uFFFD"},
		{"\x04\xa1\xbe\x75\x6f\x6c\x61\x73", "Ąžuolas"},
		{"\x05\xbf\xe0\xd8\xd2\xd5\xe2", "Привет"},
		{"\x06\xd3\xea\xc7\xd1\xc9", "سيارة"},
		{"\x07\xc5\xeb\xeb\xdc\xe4\xe1", "Ελλάδα"},
		{"\x08\xf9\xec\xe5\xed", "שלום"},
		{"\x09\xdd\x73\x74\x61\x6e\x62\x75\x6c\x20\xfd\x6c\xfd\x6b", "İstanbul ılık"},
		{"\xff\x8f\x66\x67\x3a\x00\x20\xd8\x3d\xde\x00\x00\x00", "\u8f66\u673a 😀"},
		{"\xff\x00K\x00", "K\uFFFD"},
		{"\xff\xd8\x3d\x00A", "\uFFFDA"},
		{"\xff\xde\x00\x00B\xd8\x00", "\uFFFDB\uFFFD"},
		{"\x00Line\nbreak\x1b[2J", "Line\nbreak\x1b[2J"},
		{"\x0aKario", ""},
		{"\x80Kario", ""},
		{"\xfeKario", ""},
		{"\x01", ""},
		{"", ""},
	} {
		b := append(append([]byte{}, Challenge{Nonce: seq}.encode()...), tagRealm, byte(len(tc.raw)))
		c, err := parseChallenge(append(b, tc.raw...))
		if err != nil || c.Realm != tc.want || c.Nonce != seq {
			t.Errorf("%x: %q %v, want %q", tc.raw, c.Realm, err, tc.want)
		}
	}
	long := append([]byte{realmUnicode}, bytes.Repeat([]byte{0x04, 0x10}, 127)...)
	if c, err := parseChallenge(append(append(Challenge{Nonce: seq}.encode(), tagRealm, byte(len(long))), long...)); err != nil || c.Realm != strings.Repeat("А", 127) {
		t.Errorf("the longest realm: %q %v", c.Realm, err)
	}
}

// asker answers challenges with password and counts what it was asked.
type asker struct {
	mu       sync.Mutex
	password string
	asked    []Challenge
}

func (a *asker) ask(c Challenge) (string, string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, c)
	return "", a.password, a.password != ""
}

func (a *asker) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.asked)
}

// demands answers a CONNECT that has no response with Unauthorized and a
// challenge of nonce (and a realm), connects with the right response for
// password, worked out here, and answers a wrong one with Unauthorized.
func demands(nonce [nonceSize]byte, password string, then func(req *packet) []*packet) func(req *packet) []*packet {
	sum := md5.Sum(append(append([]byte{}, nonce[:]...), ":"+password...))
	want := "0010" + hex.EncodeToString(sum[:]) + "0210" + hex.EncodeToString(nonce[:])
	challenge := append(append([]byte{0, 16}, nonce[:]...), 2, 6, 0, 'K', 'a', 'r', 'i', 'o')
	return func(req *packet) []*packet {
		switch {
		case req.code == opConnect && req.get(hAuthResponse) == nil:
			return []*packet{{code: Unauthorized, prefix: connectPrefix(), headers: []header{{id: hAuthChallenge, data: challenge}}}}
		case req.code == opConnect && hex.EncodeToString(req.bytes(hAuthResponse)) == want:
			return []*packet{okConnect(5)}
		case req.code == opConnect:
			return []*packet{{code: Unauthorized, prefix: connectPrefix()}}
		}
		return then(req)
	}
}

func vcards(req *packet) []*packet {
	switch req.code {
	case opGet | final:
		return []*packet{{code: Success, headers: []header{{id: hEndOfBody, data: []byte("BEGIN:VCARD\r\nTEL:1\r\nEND:VCARD\r\n")}}}}
	}
	return []*packet{{code: Success}}
}

// An old phone that asks for a password (OBEX authentication) in its
// answer to the CONNECT, an Unauthorized with a challenge (Ask gets its
// nonce and realm): with the right
// password the CONNECT goes again, its headers as before and then the
// Authenticate Response (the MD5 of the phone's nonce, a colon and the
// password; the nonce), and the pull works; with a wrong one it fails
// with ErrBadPassword; without a password, or when the user gives none,
// the Unauthorized stands, as before. Either failure ends the session
// with a DISCONNECT.
func TestClientAuth(t *testing.T) {
	nonce := [nonceSize]byte{0x5a, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 0xa5}
	for _, tc := range []struct {
		name     string
		password string
		ask      bool
		want     error
	}{
		{"right", "1234", true, nil},
		{"wrong", "0000", true, ErrBadPassword},
		{"none given", "", true, ResponseError(Unauthorized)},
		{"no password", "", false, ResponseError(Unauthorized)},
	} {
		a, b := link(t, false)
		p := playPeer(t, b, 0, demands(nonce, "1234", vcards))
		user := &asker{password: tc.password}
		cn := Connect{Target: PBAPTarget, Params: []byte{0x10, 4, 0, 0, 0, 3}, Wait: time.Second, Timeout: time.Second}
		if tc.ask {
			cn.Auth.Ask = user.ask
		}
		cl, err := DialWith(a, 0, cn)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v, want %v", tc.name, err, tc.want)
		}
		reqs := p.got()
		connects := 0
		for _, r := range reqs {
			if r.code == opConnect {
				connects++
			}
		}
		if wantConnects := map[bool]int{true: 2, false: 1}[tc.password != ""]; connects != wantConnects {
			t.Fatalf("%s: %d CONNECTs", tc.name, connects)
		}
		if ended := reqs[len(reqs)-1].code == opDisconnect; ended != (err != nil) {
			t.Errorf("%s: a DISCONNECT last %v, after %v", tc.name, ended, err)
		}
		if tc.ask && (user.count() != 1 || user.asked[0].Nonce != nonce || user.asked[0].UserID || user.asked[0].Realm != "Kario") {
			t.Errorf("%s: asked %+v", tc.name, user.asked)
		}
		if err != nil {
			continue
		}
		again := reqs[1]
		if len(again.headers) != 3 || again.headers[0].id != hTarget || again.headers[1].id != hAppParams || again.headers[2].id != hAuthResponse ||
			!bytes.Equal(again.bytes(hTarget), PBAPTarget) || !bytes.Equal(again.bytes(hAppParams), cn.Params) {
			t.Errorf("the CONNECT again: %+v", again.headers)
		}
		body, _, err := cl.Get("x-bt/phonebook", "telecom/pb.vcf", nil)
		if err != nil || !strings.Contains(string(body), "TEL:1") {
			t.Fatalf("pull: %q %v", body, err)
		}
		if err := cl.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// A server that asks for the password later, in its answer to the first
// packet of an operation (a GET, a SETPATH, a PUT of several packets): the
// packet goes again with the response, a PUT from its start, each packet
// within the server's size; the password the user gave answers the
// session's next challenges unasked. A wrong one fails the operation.
func TestClientAuthLater(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 100)
	a, b := link(t, false)
	var mu sync.Mutex
	n, wrong := byte(0), false
	var put []byte
	var nonce [nonceSize]byte
	challenge := func() []*packet {
		n++
		nonce = [nonceSize]byte{n, n, n, n, n, n, n, n, n, n, n, n, n, n, n, n}
		return []*packet{{code: Unauthorized, headers: []header{{id: hAuthChallenge, data: append([]byte{0, 16}, nonce[:]...)}}}}
	}
	answered := func(req *packet) bool {
		sum := md5.Sum(append(append([]byte{}, nonce[:]...), ":1234"...))
		return hex.EncodeToString(req.bytes(hAuthResponse)) == "0010"+hex.EncodeToString(sum[:])+"0210"+hex.EncodeToString(nonce[:]) && !wrong
	}
	p := playPeer(t, b, 0, func(req *packet) []*packet {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case req.code == opConnect:
			return []*packet{{code: Success, prefix: []byte{0x10, 0, 0x01, 0x2c}, headers: []header{{id: hConnID, n: 9}}}}
		case req.get(hName) != nil && req.get(hAuthResponse) == nil:
			return challenge()
		case req.get(hName) != nil && !answered(req):
			return []*packet{{code: Unauthorized}}
		case req.code == opPut || req.code == opPut|final:
			put = append(append(put, req.bytes(hBody)...), req.bytes(hEndOfBody)...)
			if req.code == opPut {
				return []*packet{{code: Continue}}
			}
		}
		return vcards(req)
	})
	user := &asker{password: "1234"}
	cl, err := DialWith(a, 0, Connect{Target: PBAPTarget, Auth: Auth{Ask: user.ask}})
	if err != nil {
		t.Fatal(err)
	}
	if body, _, err := cl.Get("x-bt/phonebook", "telecom/pb.vcf", nil); err != nil || !strings.Contains(string(body), "TEL:1") {
		t.Fatalf("pull: %q %v", body, err)
	}
	if err := cl.SetPath("telecom", false); err != nil {
		t.Fatal(err)
	}
	if err := cl.Put("a.txt", "text/plain", data); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := put
	mu.Unlock()
	if !bytes.Equal(got, data) || user.count() != 1 {
		t.Fatalf("put %d bytes, asked %d times", len(got), user.count())
	}
	p.mu.Lock()
	for _, raw := range p.raw {
		if len(raw) > 0x12c {
			t.Errorf("a %d-byte packet, the server takes 300", len(raw))
		}
	}
	p.mu.Unlock()
	mu.Lock()
	wrong = true
	mu.Unlock()
	if _, _, err := cl.Get("x-bt/phonebook", "telecom/pb.vcf", nil); !errors.Is(err, ErrBadPassword) {
		t.Errorf("a wrong password: %v", err)
	}
}

// unauthorized is a device's Unauthorized answer challenging for nonce;
// prefix is a CONNECT answer's.
func unauthorized(nonce [nonceSize]byte, prefix []byte) *packet {
	return &packet{code: Unauthorized, prefix: prefix, headers: []header{{id: hAuthChallenge, data: append([]byte{0, 16}, nonce[:]...)}}}
}

// responds reports whether req carries the response to nonce for
// password, worked out here.
func responds(req *packet, nonce [nonceSize]byte, password string) bool {
	sum := md5.Sum(append(append([]byte{}, nonce[:]...), ":"+password...))
	return hex.EncodeToString(req.bytes(hAuthResponse)) == "0010"+hex.EncodeToString(sum[:])+"0210"+hex.EncodeToString(nonce[:])
}

var opNames = map[byte]string{opConnect: "CONNECT", opDisconnect: "DISCONNECT", opPut: "PUT", opPut | final: "PUT"}

// Kario sending a file (Object Push, bt.send) to a device that asks for
// its code (4711) in its answer to the CONNECT, or later in its answer to
// the PUT. The right code, typed in more time than the link's waits: the
// request goes again with the response and without a challenge of
// Kario's (it answers, it does not challenge back), and the file goes. A
// wrong code, the user cancelling, or no code in time: the user asked
// once, the request not sent a third time, a DISCONNECT tells the device
// the session is over, and Close sends nothing more. A device that wants
// Kario's own challenge too (mutual authentication) and refuses the
// answer without it: with another challenge it is ErrBadPassword (not
// asked again), with Forbidden that, and one that never answers is
// ErrNoAnswer within Timeout.
func TestClientAuthPush(t *testing.T) {
	nonce := [nonceSize]byte{'K', 'a', 'r', 'i', 'o', 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	const wait = 250 * time.Millisecond
	slow := 3 * wait
	for _, tc := range []struct {
		name  string
		later bool // the device asks at the PUT
		// mutual: the device's answer to the right response without a
		// challenge of Kario's (empty: none); nil: it wants none.
		mutual []*packet
		code   string // the user's ("": none)
		slow   bool   // the user takes longer than the link's waits
		want   error
		ops    string // the requests the device got
	}{
		{name: "right", code: "4711", ops: "CONNECT CONNECT PUT DISCONNECT"},
		{name: "right, typed slowly", code: "4711", slow: true, ops: "CONNECT CONNECT PUT DISCONNECT"},
		{name: "wrong", code: "1234", want: ErrBadPassword, ops: "CONNECT CONNECT DISCONNECT"},
		{name: "cancelled", want: ResponseError(Unauthorized), ops: "CONNECT DISCONNECT"},
		{name: "none in time", slow: true, want: ResponseError(Unauthorized), ops: "CONNECT DISCONNECT"},
		{name: "at the PUT, right", later: true, code: "4711", ops: "CONNECT PUT PUT DISCONNECT"},
		{name: "at the PUT, wrong", later: true, code: "1234", want: ErrBadPassword, ops: "CONNECT PUT PUT DISCONNECT"},
		{name: "at the PUT, cancelled", later: true, want: ResponseError(Unauthorized), ops: "CONNECT PUT DISCONNECT"},
		{name: "mutual, challenging again", mutual: []*packet{unauthorized(nonce, connectPrefix())}, code: "4711", want: ErrBadPassword, ops: "CONNECT CONNECT DISCONNECT"},
		{name: "mutual, forbidden", mutual: []*packet{{code: Forbidden, prefix: connectPrefix()}}, code: "4711", want: ResponseError(Forbidden), ops: "CONNECT CONNECT"},
		{name: "mutual, not answering", mutual: []*packet{}, code: "4711", want: ErrNoAnswer, ops: "CONNECT CONNECT"},
	} {
		a, b := link(t, false)
		var mu sync.Mutex
		var got []byte
		p := playPeer(t, b, 0, func(req *packet) []*packet {
			switch {
			case req.code == opDisconnect:
				return []*packet{{code: Success}}
			case req.code == opConnect && tc.later:
				return []*packet{{code: Success, prefix: connectPrefix()}}
			case req.code == opConnect && req.get(hAuthResponse) == nil:
				return []*packet{unauthorized(nonce, connectPrefix())}
			case req.code == opConnect && !responds(req, nonce, "4711"):
				return []*packet{{code: Unauthorized, prefix: connectPrefix()}}
			case req.code == opConnect && tc.mutual != nil && req.get(hAuthChallenge) == nil:
				return tc.mutual
			case req.code == opConnect:
				return []*packet{{code: Success, prefix: connectPrefix()}}
			case tc.later && req.get(hAuthResponse) == nil:
				return []*packet{unauthorized(nonce, nil)}
			case tc.later && !responds(req, nonce, "4711"):
				return []*packet{{code: Unauthorized}}
			}
			mu.Lock()
			got = append(got, req.bytes(hEndOfBody)...)
			mu.Unlock()
			return []*packet{{code: Success}}
		})
		user := &asker{password: tc.code}
		ask := user.ask
		if tc.slow {
			ask = func(c Challenge) (string, string, bool) {
				time.Sleep(slow)
				return user.ask(c)
			}
		}
		start := time.Now()
		cl, err := DialWith(a, 0, Connect{Wait: wait, Timeout: wait, Auth: Auth{Ask: ask}})
		if err == nil {
			if err = cl.Put("notes.txt", "text/plain", []byte("hello")); err == nil {
				err = cl.Close()
			} else if cerr := cl.Close(); cerr != nil {
				t.Errorf("%s: closing after the PUT failed: %v", tc.name, cerr)
			}
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
		if took := time.Since(start); took > slow+4*wait {
			t.Errorf("%s: took %v", tc.name, took)
		}
		var ops []string
		reqs := p.got()
		for _, r := range reqs {
			ops = append(ops, opNames[r.code])
		}
		if strings.Join(ops, " ") != tc.ops {
			t.Errorf("%s: the device got %v, want %s", tc.name, ops, tc.ops)
		}
		if n := user.count(); n != 1 {
			t.Errorf("%s: asked %d times", tc.name, n)
		}
		for _, r := range reqs {
			if r.get(hAuthChallenge) != nil {
				t.Errorf("%s: Kario challenged the device: %+v", tc.name, r.headers)
			}
		}
		mu.Lock()
		if sent := string(got); (tc.want == nil) != (sent == "hello") {
			t.Errorf("%s: the device got %q", tc.name, sent)
		}
		mu.Unlock()
	}
}

// Kario challenging the server (a Password in Connect.Auth): the CONNECT
// carries the challenge, and only an answer with the response to it
// connects; one without, for another password or another nonce, is
// ErrNotProven.
func TestClientAuthServer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		password string
		nonce    func([]byte) []byte
		want     error
	}{
		{"proven", "5678", func(n []byte) []byte { return n }, nil},
		{"another password", "1111", func(n []byte) []byte { return n }, ErrNotProven},
		{"another nonce", "5678", func(n []byte) []byte { return append([]byte{n[0] ^ 1}, n[1:]...) }, ErrNotProven},
		{"no response", "", nil, ErrNotProven},
	} {
		a, b := link(t, false)
		playPeer(t, b, 0, func(req *packet) []*packet {
			ch := req.bytes(hAuthChallenge)
			if req.code != opConnect || len(ch) != 18 || ch[0] != 0 || ch[1] != 16 {
				t.Errorf("%s: %x without the challenge: %+v", tc.name, req.code, req.headers)
				return []*packet{{code: BadRequest, prefix: connectPrefix()}}
			}
			ok := okConnect(1)
			if tc.password != "" {
				n := tc.nonce(ch[2:])
				sum := md5.Sum(append(append([]byte{}, ch[2:]...), ":"+tc.password...))
				ok.headers = append(ok.headers, header{id: hAuthResponse, data: append(append(append([]byte{0, 16}, sum[:]...), 2, 16), n...)})
			}
			return []*packet{ok}
		})
		if _, err := DialWith(a, 0, Connect{Target: PBAPTarget, Auth: Auth{Password: "5678"}}); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
}

// Kario's server asking for a password (Server.Auth.Password, as the
// simulated old phone does): before the right response it serves nothing
// (Unauthorized, the inbox not asked); a CONNECT without the response
// gets a challenge, each with a nonce of its own; a wrong response, or a
// replayed one, gets Unauthorized without a challenge, and the third ends
// the session. The right one opens it.
func TestServerAuth(t *testing.T) {
	in := &inbox{accept: true}
	s := &Server{Inbox: in, Phonebook: book{allow: true}, Auth: Auth{Password: "1234"}}
	a, b := link(t, false)
	done := make(chan error, 1)
	go func() { done <- s.Serve(b, 0) }()
	c := newConn(a, 0)
	c.max = MaxPacket
	ask := func(p *packet) *packet {
		t.Helper()
		if err := c.write(p); err != nil {
			t.Fatal(err)
		}
		r, err := c.read(func(byte) int { return map[bool]int{true: 4}[p.code == opConnect] })
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	connect := func(hs ...header) *packet {
		return ask(&packet{code: opConnect, prefix: connectPrefix(), headers: append([]header{{id: hTarget, data: PBAPTarget}}, hs...)})
	}
	nonceOf := func(r *packet) [nonceSize]byte {
		t.Helper()
		var n [nonceSize]byte
		ch := r.bytes(hAuthChallenge)
		if r.code != Unauthorized || len(ch) != 18 || ch[0] != 0 || ch[1] != 16 {
			t.Fatalf("no challenge: %x %+v", r.code, r.headers)
		}
		copy(n[:], ch[2:])
		return n
	}
	answer := func(n [nonceSize]byte, password string) header {
		sum := md5.Sum(append(append([]byte{}, n[:]...), ":"+password...))
		return header{id: hAuthResponse, data: append(append([]byte{0, 16}, sum[:]...), append([]byte{2, 16}, n[:]...)...)}
	}
	if r := ask(&packet{code: opPut | final, headers: []header{textHeader(hName, "a.txt"), {id: hEndOfBody, data: []byte("x")}}}); r.code != Unauthorized || len(in.offered) != 0 {
		t.Fatalf("a put first: %x, offered %v", r.code, in.offered)
	}
	first := nonceOf(connect())
	if r := ask(&packet{code: opGet | final, headers: []header{typeHeader("x-bt/phonebook"), textHeader(hName, "telecom/pb.vcf")}}); r.code != Unauthorized {
		t.Fatalf("a pull while challenged: %x", r.code)
	}
	if r := connect(answer(first, "0000")); r.code != Unauthorized || r.get(hAuthChallenge) != nil {
		t.Fatalf("a wrong response: %x %+v", r.code, r.headers)
	}
	second := nonceOf(connect())
	if second == first {
		t.Fatal("the same nonce twice")
	}
	if r := connect(answer(first, "1234")); r.code != Unauthorized {
		t.Fatalf("a replayed response: %x", r.code)
	}
	if r := connect(answer(nonceOf(connect()), "1234")); r.code != Success || !bytes.Equal(r.bytes(hWho), PBAPTarget) || r.get(hConnID) == nil {
		t.Fatalf("the right response: %x %+v", r.code, r.headers)
	}
	if r := ask(&packet{code: opGet | final, headers: []header{typeHeader("x-bt/phonebook"), textHeader(hName, "telecom/pb.vcf")}}); r.code != Success {
		t.Fatalf("a pull after: %x", r.code)
	}

	a, b = link(t, false)
	go func() { done <- s.Serve(b, 0) }()
	c = newConn(a, 0)
	c.max = MaxPacket
	for range maxAuthTries {
		connect(answer(nonceOf(connect()), "9999"))
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session took a fourth try")
	}
}

// Kario's client and server: the right password connects and serves, a
// wrong one is ErrBadPassword, none the Unauthorized; a server with Ask
// proves itself to a client that challenges it, at the same time as it
// challenges the client; one without Ask leaves the challenge unanswered.
func TestAuthBothSides(t *testing.T) {
	s := &Server{Phonebook: book{allow: true}, Auth: Auth{Password: "1234"}}
	for _, tc := range []struct {
		password string
		want     error
	}{{"1234", nil}, {"4321", ErrBadPassword}, {"", ResponseError(Unauthorized)}} {
		cl, err := DialWith(serve(t, s, 0, false), 0, Connect{Target: PBAPTarget, Auth: Auth{Ask: (&asker{password: tc.password}).ask}})
		if !errors.Is(err, tc.want) {
			t.Fatalf("%q: %v, want %v", tc.password, err, tc.want)
		}
		if err == nil {
			if body, _, err := cl.Get("x-bt/phonebook", "telecom/pb.vcf", nil); err != nil || strings.Count(string(body), "BEGIN:VCARD") != 3 {
				t.Fatalf("pull: %v", err)
			}
		}
	}
	proving := &Server{Phonebook: book{allow: true}, Auth: Auth{Password: "1234", Ask: (&asker{password: "5678"}).ask}}
	for _, tc := range []struct {
		s        *Server
		password string
		want     error
	}{{proving, "5678", nil}, {proving, "8765", ErrNotProven}, {&Server{Phonebook: book{allow: true}}, "5678", ErrNotProven}} {
		_, err := DialWith(serve(t, tc.s, 300, true), 300, Connect{Target: PBAPTarget, Auth: Auth{Ask: (&asker{password: "1234"}).ask, Password: tc.password}})
		if !errors.Is(err, tc.want) {
			t.Errorf("challenging for %q: %v, want %v", tc.password, err, tc.want)
		}
	}
	for _, tc := range []struct {
		s    *Server
		hs   []header
		want byte
	}{
		{&Server{Phonebook: book{allow: true}, Auth: Auth{Ask: (&asker{password: "5678"}).ask}}, []header{{id: hAuthChallenge, data: []byte{0, 16, 1}}}, BadRequest},
		{proving, []header{{id: hAuthResponse, data: []byte{0, 16}}}, Unauthorized},
	} {
		c := newConn(serve(t, tc.s, 0, false), 0)
		c.max = MaxPacket
		for _, hs := range [][]header{nil, tc.hs} {
			if tc.s.Auth.Password == "" && hs == nil {
				continue
			}
			if err := c.write(&packet{code: opConnect, prefix: connectPrefix(), headers: hs}); err != nil {
				t.Fatal(err)
			}
			if r, err := c.read(func(byte) int { return 4 }); err != nil || (hs != nil && r.code != tc.want) {
				t.Fatalf("%+v: %v %+v, want %x", hs, err, r, tc.want)
			}
		}
	}
}
