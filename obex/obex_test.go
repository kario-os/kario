package obex

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Non-ASCII names: a report and a contact (in Chinese).
const (
	report = "\u62a5\u544a.txt"
	zhang  = "\u5f20\u4e09"
)

type inbox struct {
	accept  bool
	offered []string
	got     map[string][]byte
}

func (in *inbox) Offer(name, mime string, size int64) bool {
	in.offered = append(in.offered, name+"|"+mime)
	return in.accept
}

func (in *inbox) Received(name, mime string, data []byte) error {
	if in.got == nil {
		in.got = map[string][]byte{}
	}
	in.got[name] = append([]byte{}, data...)
	return nil
}

// serve runs s on one end of a pipe and returns the other.
func serve(t *testing.T, s *Server, mtu int, packets bool) io.ReadWriteCloser {
	t.Helper()
	var a, b io.ReadWriteCloser
	if packets {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET, 0)
		if err != nil {
			t.Fatal(err)
		}
		a, b = os.NewFile(uintptr(fds[0]), "a"), os.NewFile(uintptr(fds[1]), "b")
	} else {
		a, b = net.Pipe()
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(a, mtu); a.Close() }()
	t.Cleanup(func() {
		b.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, os.ErrClosed) {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("the server did not end")
		}
	})
	return b
}

func TestPush(t *testing.T) {
	for _, packets := range []bool{false, true} {
		in := &inbox{accept: true}
		mtu := 0
		if packets {
			mtu = 700
		}
		link := serve(t, &Server{Inbox: in, MaxObject: 1 << 20}, mtu, packets)
		cl, err := Dial(link, mtu, nil)
		if err != nil {
			t.Fatal(err)
		}
		data := bytes.Repeat([]byte("kario-obex "), 9000)
		if err := cl.Put(report, "text/plain", data); err != nil {
			t.Fatal(err)
		}
		if err := cl.Put("empty.txt", "", nil); err != nil {
			t.Fatal(err)
		}
		if err := cl.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(in.got[report], data) || in.got["empty.txt"] == nil || len(in.offered) != 2 || in.offered[0] != report+"|text/plain" {
			t.Errorf("packets %v: offered %v, got %d bytes", packets, in.offered, len(in.got[report]))
		}
	}
}

func TestPushRefused(t *testing.T) {
	in := &inbox{}
	cl, err := Dial(serve(t, &Server{Inbox: in, MaxObject: 100}, 0, false), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.Put("a.txt", "", []byte("no")); !errors.Is(err, ResponseError(Forbidden)) {
		t.Errorf("refused put: %v", err)
	}
	if err := cl.Put("big.bin", "", make([]byte, 200)); !errors.Is(err, ResponseError(EntityTooLarge)) {
		t.Errorf("too large: %v", err)
	}
	if len(in.offered) != 1 || in.got != nil {
		t.Errorf("offered %v, got %v", in.offered, in.got)
	}
	if _, err := Dial(serve(t, &Server{}, 0, false), 0, nil); !errors.Is(err, ResponseError(Forbidden)) {
		t.Errorf("no inbox: %v", err)
	}
}

type book struct{ allow bool }

func (b book) Allowed() bool { return b.allow }
func (b book) Owner() Card   { return Card{Name: "Kario phone", Number: "13800138000"} }
func (b book) Contacts() []Card {
	return []Card{{Name: zhang, Number: "13900000001"}, {Name: "Li; Si", Number: "13900000002"}}
}
func (b book) Calls(kind string) []Card {
	at := time.Date(2026, 11, 14, 9, 30, 0, 0, time.Local)
	missed := Card{Name: zhang, Number: "13900000001", At: at, Call: "MISSED"}
	dialed := Card{Number: "10086", At: at.Add(-time.Hour), Call: "DIALED"}
	return map[string][]Card{"mch": {missed}, "och": {dialed}, "cch": {missed, dialed}}[kind]
}

func TestPhonebook(t *testing.T) {
	if _, err := Dial(serve(t, &Server{Phonebook: book{}}, 0, false), 0, PBAPTarget); !errors.Is(err, ResponseError(Forbidden)) {
		t.Errorf("not allowed: %v", err)
	}
	cl, err := Dial(serve(t, &Server{Phonebook: book{allow: true}}, 400, true), 400, PBAPTarget)
	if err != nil {
		t.Fatal(err)
	}
	_, ans, err := cl.Get("x-bt/phonebook", "telecom/pb.vcf", Params{tagMaxListCount: U16(0)}.Encode())
	if err != nil || ParseParams(ans).uint16(tagPhonebookSize, -1) != 3 {
		t.Fatalf("size: %v %x", err, ans)
	}
	body, _, err := cl.Get("x-bt/phonebook", "telecom/pb.vcf", nil)
	pb := string(body)
	if err != nil || strings.Count(pb, "BEGIN:VCARD") != 3 || !strings.Contains(pb, "FN;CHARSET=UTF-8:"+zhang+"\r\n") ||
		!strings.Contains(pb, `FN:Li\; Si`) || !strings.Contains(pb, "TEL;CELL:13900000002") || !strings.Contains(pb, "VERSION:2.1") {
		t.Errorf("phone book (%v):\n%s", err, pb)
	}
	body, _, _ = cl.Get("x-bt/phonebook", "telecom/pb.vcf", Params{tagFormat: {1}, tagListStartOffset: U16(1), tagMaxListCount: U16(1)}.Encode())
	if s := string(body); strings.Count(s, "BEGIN:VCARD") != 1 || !strings.Contains(s, "VERSION:3.0") || !strings.Contains(s, "FN:"+zhang) || !strings.Contains(s, "TEL;TYPE=CELL:") {
		t.Errorf("page in 3.0:\n%s", s)
	}
	body, _, _ = cl.Get("x-bt/phonebook", "telecom/mch.vcf", nil)
	if s := string(body); !strings.Contains(s, "X-IRMC-CALL-DATETIME;MISSED:20261114T093000") {
		t.Errorf("missed calls:\n%s", s)
	}
	if _, _, err := cl.Get("x-bt/phonebook", "SIM1/telecom/pb.vcf", nil); !errors.Is(err, ResponseError(NotFound)) {
		t.Errorf("SIM phone book: %v", err)
	}
	for _, f := range []string{"telecom", "pb"} {
		if err := cl.SetPath(f, false); err != nil {
			t.Fatal(f, err)
		}
	}
	body, _, err = cl.Get("x-bt/vcard-listing", "", Params{tagSearchValue: []byte(zhang[:3])}.Encode())
	if s := string(body); err != nil || !strings.Contains(s, `<card handle="1.vcf" name="`+zhang+`"/>`) || strings.Contains(s, "Li") {
		t.Errorf("listing (%v):\n%s", err, s)
	}
	body, _, err = cl.Get("x-bt/vcard", "2.vcf", nil)
	if s := string(body); err != nil || !strings.Contains(s, "13900000002") {
		t.Errorf("entry (%v):\n%s", err, s)
	}
	if err := cl.SetPath("", true); err != nil {
		t.Fatal(err)
	}
	if err := cl.SetPath("cch", false); err != nil {
		t.Fatal(err)
	}
	body, _, _ = cl.Get("x-bt/vcard-listing", "", nil)
	if s := string(body); !strings.Contains(s, `<card handle="1.vcf" name="`+zhang+`"/>`) || !strings.Contains(s, `<card handle="2.vcf" name="10086"/>`) {
		t.Errorf("call history listing:\n%s", s)
	}
	if err := cl.SetPath("nope", false); !errors.Is(err, ResponseError(NotFound)) {
		t.Errorf("no such folder: %v", err)
	}
	if err := cl.Put("x.vcf", "", []byte("x")); !errors.Is(err, ResponseError(Forbidden)) {
		t.Errorf("put to the phone book: %v", err)
	}
	if err := cl.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMalformed(t *testing.T) {
	for _, b := range [][]byte{{0x02, 0}, {0x02, 0, 4, 0x01}, {0x02, 0, 6, 0x01, 0, 9}, {0x02, 0, 5, 0xcb, 0}} {
		if _, err := decode(b, 0); err == nil {
			t.Errorf("%x decoded", b)
		}
	}
	p := &packet{code: opPut, headers: []header{textHeader(hName, "a"), {id: hLength, n: 7}, {id: 0x97, n: 1}, {id: hBody, data: []byte("xy")}}}
	q, err := decode(p.encode(), 0)
	if err != nil || len(q.headers) != 4 || q.get(hLength).n != 7 || q.get(0x97).n != 1 || string(q.bytes(hBody)) != "xy" {
		t.Errorf("round trip: %v %+v", err, q)
	}
	if name, _ := q.text(hName); name != "a" {
		t.Errorf("name %q", name)
	}
}
