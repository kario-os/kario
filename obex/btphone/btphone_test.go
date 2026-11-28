package btphone

import (
	"bufio"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/obex"
	"golang.org/x/sys/unix"
)

var update = flag.Bool("update", false, "record the sessions in testdata anew")

var shanghai = time.FixedZone("CST", 8*3600)

// link serves the phone on one end of a stream (RFCOMM) or a packet link
// (L2CAP) and returns the other end.
func link(t *testing.T, p *Phone, packets bool) (io.ReadWriteCloser, int) {
	t.Helper()
	var a, b io.ReadWriteCloser
	mtu := 0
	if packets {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, fd := range fds {
			_ = unix.SetNonblock(fd, true)
		}
		a, b, mtu = os.NewFile(uintptr(fds[0]), "a"), os.NewFile(uintptr(fds[1]), "b"), 700
	} else {
		a, b = net.Pipe()
	}
	go func() {
		_ = p.Server().Serve(b, mtu)
		b.Close()
	}()
	t.Cleanup(func() { a.Close() })
	return a, mtu
}

// moved is what a move reads from a phone.
type moved struct {
	sizes    map[string]int
	owner    bool
	book     map[string][]string
	listed   map[string][]obex.Entry
	messages map[string]obex.BMessage
	mapErr   map[string]error
	// auth answers the phone's challenges.
	auth obex.Auth
}

// pull reads the phone book and call histories as a move does, a page of
// page entries at a time.
func pull(t *testing.T, rw io.ReadWriter, mtu, page int, m *moved) {
	t.Helper()
	pb, err := obex.DialPhoneBook(rw, mtu, obex.PBAP12, obex.Connect{Wait: time.Second, Timeout: 5 * time.Second, Auth: m.auth})
	if err != nil {
		t.Fatal(err)
	}
	m.sizes, m.book = map[string]int{}, map[string][]string{}
	for _, f := range []string{"pb", "ich", "och", "mch"} {
		if m.sizes[f], err = pb.Size("telecom/" + f + ".vcf"); err != nil {
			t.Fatal(f, err)
		}
	}
	if m.owner, err = pb.OwnerFirst(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"pb", "ich", "och", "mch"} {
		err := pb.PullAll("telecom/"+f+".vcf", m.sizes[f], page, func(cs []string) error {
			m.book[f] = append(m.book[f], cs...)
			return nil
		})
		if err != nil {
			t.Fatal(f, err)
		}
	}
	if err := pb.Close(); err != nil {
		t.Fatal(err)
	}
}

// pullMessages reads the inbox and sent folders and their messages.
func pullMessages(t *testing.T, rw io.ReadWriter, mtu int, m *moved) {
	t.Helper()
	ms, err := obex.DialMessages(rw, mtu, obex.Connect{Wait: time.Second, Timeout: 5 * time.Second, Auth: m.auth})
	if err != nil {
		t.Fatal(err)
	}
	m.listed, m.messages, m.mapErr = map[string][]obex.Entry{}, map[string]obex.BMessage{}, map[string]error{}
	for _, f := range []string{"inbox", "sent"} {
		n, err := ms.Size(f)
		if err != nil {
			m.mapErr[f] = err
			continue
		}
		err = ms.ListAll(f, n, 2, shanghai, func(es []obex.Entry) error {
			m.listed[f] = append(m.listed[f], es...)
			for _, e := range es {
				b, err := ms.Message(e.Handle)
				if err != nil {
					return err
				}
				m.messages[e.Handle] = b
			}
			return nil
		})
		if err != nil {
			t.Fatal(f, err)
		}
	}
	if err := ms.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAndroid(t *testing.T) {
	for _, packets := range []bool{false, true} {
		p, err := New("android")
		if err != nil {
			t.Fatal(err)
		}
		var m moved
		rw, mtu := link(t, p, packets)
		pull(t, rw, mtu, 2, &m)
		rw, mtu = link(t, p, packets)
		pullMessages(t, rw, mtu, &m)
		if want := map[string]int{"pb": 4, "ich": 2, "och": 1, "mch": 1}; !mapsEqual(m.sizes, want) || !m.owner {
			t.Errorf("packets %v: sizes %v, owner %v", packets, m.sizes, m.owner)
		}
		if len(m.book["pb"]) != 4 || !strings.Contains(m.book["pb"][0], "Redmi Note 9") || len(m.book["ich"]) != 2 {
			t.Errorf("packets %v: book %d cards, %d received calls", packets, len(m.book["pb"]), len(m.book["ich"]))
		}
		if len(m.listed["inbox"]) != 3 || len(m.listed["sent"]) != 1 || len(m.messages) != 4 || len(m.mapErr) != 0 {
			t.Errorf("packets %v: listed %d+%d, %d messages, %v", packets, len(m.listed["inbox"]), len(m.listed["sent"]), len(m.messages), m.mapErr)
		}
		if mms := m.messages["200000000000002"]; mms.Type != "MMS" || !strings.HasPrefix(mms.Text, "\u770b\u770b") {
			t.Errorf("mms %+v", mms)
		}
		asked := p.Asked()
		want := "pbap GET x-bt/phonebook telecom/pb.vcf 04=0002 05=0002 06=0000000010030087 07=00"
		if !slices.Contains(asked, want) || !slices.Contains(asked, "pbap GET x-bt/vcard-listing telecom/pb 01=00 04=0001") ||
			!slices.Contains(asked, "map GET x-bt/message 100000000000006 0a=00 14=01") || asked[0] != "pbap CONNECT" {
			t.Errorf("packets %v: asked\n%s", packets, strings.Join(asked, "\n"))
		}
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// The iPhone: vCard 3.0 though 2.1 is asked for, no own card, a message
// server with an empty inbox and no sent folder; with IgnoreOffset the
// whole phone book comes for every page, which ends the pull.
func TestIPhone(t *testing.T) {
	p, err := New("iphone")
	if err != nil {
		t.Fatal(err)
	}
	p.IgnoreOffset = true
	var m moved
	rw, mtu := link(t, p, true)
	pull(t, rw, mtu, 1, &m)
	if m.owner || len(m.book["pb"]) != 2 || !strings.Contains(m.book["pb"][0], "VERSION:3.0") || len(m.book["och"]) != 1 {
		t.Errorf("owner %v, book %q", m.owner, m.book)
	}
	n := 0
	for _, a := range p.Asked() {
		if strings.HasPrefix(a, "pbap GET x-bt/phonebook telecom/pb.vcf 04=0001") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d pulls of the phone book with an offset it ignores", n)
	}
	rw, mtu = link(t, p, false)
	pullMessages(t, rw, mtu, &m)
	if len(m.listed["inbox"]) != 0 || !errors.Is(m.mapErr["sent"], obex.ResponseError(obex.NotFound)) {
		t.Errorf("messages: %+v, %v", m.listed, m.mapErr)
	}
}

// The phone's user decides when a session opens: a no refuses it.
func TestAsk(t *testing.T) {
	p, _ := New("android")
	answers := make(chan bool, 2)
	p.Ask = func(service string) bool { return <-answers }
	answers <- false
	rw, mtu := link(t, p, false)
	if _, err := obex.DialPhoneBook(rw, mtu, 0, obex.Connect{Wait: time.Second, Timeout: time.Second}); !errors.Is(err, obex.ResponseError(obex.Forbidden)) {
		t.Errorf("refused: %v", err)
	}
	rw, mtu = link(t, p, true)
	if _, err := obex.DialMessages(rw, mtu, obex.Connect{Wait: 50 * time.Millisecond, Timeout: time.Second}); !errors.Is(err, obex.ErrNoAnswer) {
		t.Errorf("not answered: %v", err)
	}
	answers <- true
	if !slices.Equal(p.Asked()[:2], []string{"pbap CONNECT", "map CONNECT"}) {
		t.Errorf("asked %v", p.Asked())
	}
}

// A phone that asks for a password (OBEX authentication) before its user
// decides: a wrong one is ErrBadPassword and none the Unauthorized, the
// phone's user not asked; the right one moves the phone book and the
// messages over either link, asked for once a session.
func TestPassword(t *testing.T) {
	p, _ := New("android")
	p.Password = "0000"
	var mu sync.Mutex
	asks := 0
	answer := func(password string) obex.Auth {
		return obex.Auth{Ask: func(c obex.Challenge) (string, string, bool) {
			mu.Lock()
			asks++
			mu.Unlock()
			return "", password, password != ""
		}}
	}
	for _, tc := range []struct {
		password string
		want     error
	}{{"1234", obex.ErrBadPassword}, {"", obex.ResponseError(obex.Unauthorized)}} {
		rw, mtu := link(t, p, false)
		if _, err := obex.DialPhoneBook(rw, mtu, obex.PBAP12, obex.Connect{Wait: time.Second, Timeout: time.Second, Auth: answer(tc.password)}); !errors.Is(err, tc.want) {
			t.Errorf("%q: %v, want %v", tc.password, err, tc.want)
		}
	}
	if len(p.Asked()) != 0 {
		t.Errorf("asked %v before the password", p.Asked())
	}
	asks = 0
	m := moved{auth: answer("0000")}
	rw, mtu := link(t, p, true)
	pull(t, rw, mtu, 2, &m)
	rw, mtu = link(t, p, false)
	pullMessages(t, rw, mtu, &m)
	if len(m.book["pb"]) != 4 || len(m.messages) != 4 || asks != 2 || p.Asked()[0] != "pbap CONNECT" {
		t.Errorf("book %d cards, %d messages, asked for the password %d times, the phone asked %v", len(m.book["pb"]), len(m.messages), asks, p.Asked())
	}
}

// Recorded sessions: a move's whole PBAP session with the simulated
// Android phone over L2CAP (Single Response Mode) and its MAP session
// over RFCOMM, packet by packet (testdata/*.session, made with -update).
// Played back, the clients must send exactly the recorded requests. A
// session recorded from a real phone (btmon) in the same form plays the
// same way.
func TestRecordedSessions(t *testing.T) {
	for _, s := range []struct {
		file    string
		packets bool
		run     func(io.ReadWriter, int, *moved)
	}{
		{"testdata/android-pbap-l2cap.session", true, func(rw io.ReadWriter, mtu int, m *moved) { pull(t, rw, mtu, 2, m) }},
		{"testdata/android-map-rfcomm.session", false, func(rw io.ReadWriter, mtu int, m *moved) { pullMessages(t, rw, mtu, m) }},
	} {
		if *update {
			p, _ := New("android")
			rw, mtu := link(t, p, s.packets)
			rec := &recorder{rw: rw, mtu: mtu}
			s.run(rec, mtu, &moved{})
			if err := os.WriteFile(s.file, rec.text(), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		pl, err := loadSession(s.file)
		if err != nil {
			t.Fatal(err)
		}
		var m moved
		s.run(pl, pl.mtu, &m)
		if pl.next < len(pl.steps) {
			t.Errorf("%s: %d recorded packets left", s.file, len(pl.steps)-pl.next)
		}
		if len(m.book["pb"])+len(m.messages) == 0 {
			t.Errorf("%s: nothing read", s.file)
		}
	}
}

// recorder keeps the packets each way.
type recorder struct {
	rw  io.ReadWriter
	mtu int
	mu  sync.Mutex
	log []string
}

func (r *recorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	r.log = append(r.log, "> "+hex.EncodeToString(b))
	r.mu.Unlock()
	return r.rw.Write(b)
}

func (r *recorder) Read(b []byte) (int, error) {
	n, err := r.rw.Read(b)
	if n > 0 {
		r.mu.Lock()
		r.log = append(r.log, "< "+hex.EncodeToString(b[:n]))
		r.mu.Unlock()
	}
	return n, err
}

func (r *recorder) text() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# A move's session with the simulated Android phone (btphone), recorded by TestRecordedSessions -update.\n")
	fmt.Fprintf(&b, "# > the client's packets, < the phone's, in hex; mtu 0 is a stream (RFCOMM).\nmtu %d\n", r.mtu)
	for _, l := range r.log {
		b.WriteString(l + "\n")
	}
	return []byte(b.String())
}

// player answers with the recorded packets and checks the client's.
type player struct {
	file  string
	mtu   int
	steps []string
	next  int
	buf   []byte
}

func loadSession(file string) (*player, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	p := &player{file: file}
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		switch {
		case l == "" || strings.HasPrefix(l, "#"):
		case strings.HasPrefix(l, "mtu "):
			fmt.Sscanf(l, "mtu %d", &p.mtu)
		case strings.HasPrefix(l, "> "), strings.HasPrefix(l, "< "):
			p.steps = append(p.steps, l)
		default:
			return nil, fmt.Errorf("%s: %q", file, l)
		}
	}
	return p, sc.Err()
}

func (p *player) Write(b []byte) (int, error) {
	if p.next >= len(p.steps) || !strings.HasPrefix(p.steps[p.next], "> ") {
		return 0, fmt.Errorf("%s: the client sent %x where the phone sent next", p.file, b)
	}
	if want := p.steps[p.next][2:]; want != hex.EncodeToString(b) {
		return 0, fmt.Errorf("%s: packet %d is\n%x\nrecorded\n%s", p.file, p.next, b, want)
	}
	p.next++
	return len(b), nil
}

func (p *player) Read(b []byte) (int, error) {
	if len(p.buf) == 0 {
		if p.next >= len(p.steps) || !strings.HasPrefix(p.steps[p.next], "< ") {
			return 0, io.EOF
		}
		p.buf, _ = hex.DecodeString(p.steps[p.next][2:])
		p.next++
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}
