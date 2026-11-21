package switchrecv_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/switchrecv"
	"github.com/kario-os/kario/switchrecv/oldphone"
)

// memSink keeps what arrives; a contact whose vCard holds "DUP" is one
// the destination has, one holding "BAD" one it cannot read.
type memSink struct {
	mu       sync.Mutex
	vcards   []string
	calls    []switchrecv.CallEntry
	msgs     []switchrecv.Message
	events   []string
	files    map[string][]byte
	fileMeta []switchrecv.File
	down     bool
}

func (m *memSink) Contacts(v []string) ([]switchrecv.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return nil, errors.New("down")
	}
	out := make([]switchrecv.Result, len(v))
	for i, c := range v {
		switch {
		case strings.Contains(c, "DUP"):
			out[i].Outcome = switchrecv.Duplicate
		case strings.Contains(c, "BAD"):
			out[i] = switchrecv.Result{Outcome: switchrecv.Rejected, Reason: "no name or number"}
		default:
			m.vcards = append(m.vcards, c)
		}
	}
	return out, nil
}

func (m *memSink) CallLog(v []switchrecv.CallEntry) ([]switchrecv.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, v...)
	return make([]switchrecv.Result, len(v)), nil
}

func (m *memSink) SMS(v []switchrecv.Message) ([]switchrecv.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, v...)
	return make([]switchrecv.Result, len(v)), nil
}

func (m *memSink) Calendar(v []string) ([]switchrecv.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, v...)
	return make([]switchrecv.Result, len(v)), nil
}

func (m *memSink) File(f switchrecv.File) error {
	raw, err := os.ReadFile(f.Path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[f.ID] = raw
	m.fileMeta = append(m.fileMeta, f)
	return nil
}

type rig struct {
	t    *testing.T
	r    *switchrecv.Receiver
	s    *switchrecv.Session
	sink *memSink
	srv  *httptest.Server
	p    oldphone.Pairing
	dir  string
}

// newRig starts a receiver on a loopback HTTP server with one session
// taking accept.
func newRig(t *testing.T, accept []string, tweak func(*switchrecv.Config)) *rig {
	t.Helper()
	g := &rig{t: t, r: switchrecv.NewReceiver(t.Logf), sink: &memSink{}}
	g.srv = httptest.NewServer(g.r)
	t.Cleanup(g.srv.Close)
	_, port, _ := net.SplitHostPort(g.srv.Listener.Addr().String())
	n, _ := strconv.Atoi(port)
	cfg := switchrecv.Config{Name: "Kario test", Host: "127.0.0.1", Port: n, SSID: "Kario-TEST", PSK: "kario-pass-1", Accept: accept,
		Sink: g.sink, Dir: t.TempDir() + "/staging", MaxTotal: 64 << 20}
	if tweak != nil {
		tweak(&cfg)
	}
	g.dir = cfg.Dir
	var err error
	if g.s, err = g.r.Open(cfg); err != nil {
		t.Fatal(err)
	}
	if g.p, err = oldphone.ParsePairing(g.s.Pairing().URL()); err != nil {
		t.Fatal(err)
	}
	return g
}

// phone is a new app instance (a new key pair) that has said hello.
func (g *rig) phone(counts map[string]int) *oldphone.Client {
	g.t.Helper()
	c, err := oldphone.New(g.p, nil)
	if err != nil {
		g.t.Fatal(err)
	}
	if _, err := c.SayHello(oldphone.Device{Brand: "Xiaomi", Model: "Redmi Note 12", OS: "android", OSVersion: "14"}, counts); err != nil {
		g.t.Fatal(err)
	}
	return c
}

func wantCode(t *testing.T, err error, status int, code string) *oldphone.Error {
	t.Helper()
	var e *oldphone.Error
	if !errors.As(err, &e) || e.Status != status || e.Code != code {
		t.Fatalf("got %v, want %d %s", err, status, code)
	}
	return e
}

func vcard(name, tel string) any {
	return map[string]string{"vcard": "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:" + name + "\r\nTEL;TYPE=CELL:" + tel + "\r\nEND:VCARD\r\n"}
}

// Everything once, as the app sends it, then done.
func TestHappyPath(t *testing.T) {
	g := newRig(t, switchrecv.Kinds, nil)
	c := g.phone(map[string]int{"contacts": 2, "call_log": 3})
	if !slices.Equal(c.Hello.Accept, switchrecv.Kinds) || c.Hello.MaxBatch != 500 || c.Hello.Chunk != 1<<20 || c.Hello.MaxBody != 4<<20 {
		t.Fatalf("hello %+v", c.Hello)
	}
	if p := g.s.Progress(); p.State != switchrecv.StateReceiving || p.Device.Model != "Redmi Note 12" || p.Kinds[0].Expected != 2 {
		t.Fatalf("progress %+v", p)
	}
	at := time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC).Unix()
	steps := []struct {
		kind  string
		items []any
	}{
		{"contacts", []any{vcard("Zhang San", "13800138000"), vcard("Li Si", "13800138001")}},
		{"call_log", []any{
			map[string]any{"number": "13800138000", "name": "Zhang San", "type": "in", "at": at, "duration": 61},
			map[string]any{"number": "13800138001", "type": "out", "at": at + 60, "duration": 5},
			map[string]any{"number": "10086", "type": "missed", "at": at + 120, "duration": 0}}},
		{"sms", []any{
			map[string]any{"address": "13800138000", "body": "hello", "type": "in", "at": at * 1000, "read": true},
			map[string]any{"address": "13800138000", "body": "hi", "type": "out", "at": at*1000 + 5000, "read": true}}},
		{"calendar", []any{map[string]string{"ics": "BEGIN:VEVENT\nUID:1\nDTSTART;TZID=Asia/Shanghai:20261111T090000\nSUMMARY:x\nEND:VEVENT\n"}}},
	}
	for _, st := range steps {
		stored, skipped, err := c.Items(st.kind, st.items)
		if err != nil || stored != len(st.items) || skipped != 0 {
			t.Fatalf("%s: %d %d %v", st.kind, stored, skipped, err)
		}
	}
	photo := bytes.Repeat([]byte("jpeg"), (1<<20)/2+100)
	if err := c.SendFile("IMG_1", "photo", "DCIM/Camera/IMG_1.jpg", "image/jpeg", at, photo); err != nil {
		t.Fatal(err)
	}
	done, err := c.Done()
	if err != nil {
		t.Fatal(err)
	}
	if done.Contacts != 2 || done.CallLog != 3 || done.SMS != 2 || done.Calendar != 1 || done.Photos != 1 || done.Videos != 0 || done.Failed == nil || len(done.Failed) != 0 {
		t.Fatalf("done %+v", done)
	}
	if !bytes.Equal(g.sink.files["IMG_1"], photo) || g.sink.fileMeta[0].Name != "IMG_1.jpg" || g.sink.fileMeta[0].TakenAt != at || g.sink.fileMeta[0].Mime != "image/jpeg" {
		t.Fatalf("file %+v", g.sink.fileMeta)
	}
	if e := g.sink.events[0]; e != "BEGIN:VEVENT\r\nUID:1\r\nDTSTART;TZID=Asia/Shanghai:20261111T090000\r\nSUMMARY:x\r\nEND:VEVENT" {
		t.Fatalf("event %q", e)
	}
	if g.sink.calls[2].Type != "missed" || g.sink.msgs[1].At != at*1000+5000 || !g.sink.msgs[0].Read {
		t.Fatalf("calls %+v msgs %+v", g.sink.calls, g.sink.msgs)
	}
	<-g.s.Done()
	if p := g.s.Progress(); p.State != switchrecv.StateDone {
		t.Fatalf("after done %+v", p)
	}
	// The token is void.
	if _, err := c.Done(); err == nil {
		t.Fatal("a request after done went through")
	}
	if _, err := os.Stat(g.dir); !os.IsNotExist(err) {
		t.Fatalf("the staging directory is left: %v", err)
	}
}

// On a Wi-Fi network both phones share, the code names it instead of a
// hotspot and the session runs as on the hotspot; a session cannot have
// both.
func TestWifiSession(t *testing.T) {
	g := newRig(t, []string{switchrecv.KindContacts}, func(c *switchrecv.Config) {
		c.SSID, c.PSK, c.Wifi = "", "", "Kario-Test"
	})
	if g.p.Wifi != "Kario-Test" || g.p.SSID != "" || g.p.PSK != "" {
		t.Fatalf("pairing %+v", g.p)
	}
	c := g.phone(nil)
	if stored, _, err := c.Items("contacts", []any{vcard("Zhang San", "13800138000")}); err != nil || stored != 1 {
		t.Fatalf("contacts: %d %v", stored, err)
	}
	if done, err := c.Done(); err != nil || done.Contacts != 1 {
		t.Fatalf("done %+v %v", done, err)
	}
	_, err := g.r.Open(switchrecv.Config{Host: "127.0.0.1", Port: 1, SSID: "Kario-TEST", PSK: "kario-pass-1", Wifi: "Kario-Test",
		Sink: g.sink, Dir: t.TempDir()})
	if err == nil {
		t.Fatal("a session took a hotspot and a Wi-Fi network")
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// A batch sent again stores nothing twice; items the destination has
// are skipped, unreadable ones fail by their id.
func TestDuplicates(t *testing.T) {
	g := newRig(t, []string{"contacts", "call_log"}, nil)
	c := g.phone(nil)
	batch := []any{vcard("A", "1"), vcard("B", "2"), vcard("A", "1")}
	if stored, skipped, err := c.Items("contacts", batch); err != nil || stored != 2 || skipped != 1 {
		t.Fatalf("first: %d %d %v", stored, skipped, err)
	}
	if stored, skipped, err := c.Items("contacts", batch); err != nil || stored != 0 || skipped != 3 {
		t.Fatalf("again: %d %d %v", stored, skipped, err)
	}
	more := []any{vcard("DUP", "3"), map[string]string{"vcard": "BEGIN:VCARD\r\nFN:BAD\r\nEND:VCARD", "id": "c-9"},
		map[string]string{"vcard": "not one"}}
	if stored, skipped, err := c.Items("contacts", more); err != nil || stored != 0 || skipped != 1 {
		t.Fatalf("more: %d %d %v", stored, skipped, err)
	}
	if _, _, err := c.Items("call_log", []any{map[string]any{"number": "1", "type": "sideways", "at": 1, "id": "x1"}}); err != nil {
		t.Fatal(err)
	}
	done, err := c.Done()
	if err != nil || done.Contacts != 2 || len(done.Failed) != 3 {
		t.Fatalf("done %+v %v", done, err)
	}
	f := done.Failed
	if f[0].Kind != "contacts" || f[0].ID != "c-9" || f[0].Reason != "no name or number" || f[1].ID != "" || f[1].Reason != "not a vcard" || f[2].Kind != "call_log" || f[2].ID != "x1" || f[2].Reason != "bad type" {
		t.Fatalf("failed %+v", f)
	}
}

// A destination that is down answers 503 and keeps nothing: the batch,
// sent again later, goes in.
func TestSinkDown(t *testing.T) {
	g := newRig(t, []string{"contacts"}, nil)
	c := g.phone(nil)
	g.sink.down = true
	_, _, err := c.Items("contacts", []any{vcard("A", "1")})
	wantCode(t, err, http.StatusServiceUnavailable, "unavailable")
	g.sink.down = false
	if stored, _, err := c.Items("contacts", []any{vcard("A", "1")}); err != nil || stored != 1 {
		t.Fatalf("again: %d %v", stored, err)
	}
}

// A file broken off goes on where the receiver says, from an app that
// started again (a new key pair, a second hello under the same token).
func TestResumeAndSecondHello(t *testing.T) {
	g := newRig(t, []string{"photos", "videos"}, nil)
	first, _ := oldphone.New(g.p, nil)
	firstHello := first.Seal("POST", "/v1/hello", []byte(`{"app":"kario-switch","version":"1.0.0","device":{"model":"Old"},"counts":{"photos":1}}`))
	r, err := first.Send("POST", "/v1/hello", firstHello, true)
	if err != nil || r.Status != http.StatusOK || json.Unmarshal(r.Plain, &first.Hello) != nil {
		t.Fatalf("first hello: %+v %v", r, err)
	}
	data := bytes.Repeat([]byte{1, 2, 3, 4, 5, 6, 7}, 400000)
	info := fileInfo("VID-1", "video", "clip.mp4", data)
	if off, err := first.Offer(info); err != nil || off != 0 {
		t.Fatalf("offer %d %v", off, err)
	}
	if off, err := first.Chunk("VID-1", 0, data[:1<<20]); err != nil || off != 1<<20 {
		t.Fatalf("chunk %d %v", off, err)
	}
	second := g.phone(map[string]int{"photos": 1, "videos": 1})
	if p := g.s.Progress(); p.Kinds[1].Expected != 1 || p.Device.Model != "Redmi Note 12" {
		t.Fatalf("after the second hello: %+v", p)
	}
	// The first instance's hello sent again is a replay under its key:
	// refused, and the session stays with the second instance's.
	if r, err := first.Send("POST", "/v1/hello", firstHello, true); err != nil || r.Status != http.StatusConflict || !strings.Contains(string(r.Plain), `"replay"`) {
		t.Fatalf("replayed hello: %+v %v", r, err)
	}
	if p := g.s.Progress(); p.Device.Model != "Redmi Note 12" {
		t.Fatalf("the replayed hello changed the session: %+v", p.Device)
	}
	off, err := second.Offer(info)
	if err != nil || off != 1<<20 {
		t.Fatalf("resume at %d %v", off, err)
	}
	if _, err := second.Chunk("VID-1", 0, data[:10]); err == nil {
		t.Fatal("a chunk at the wrong offset went in")
	} else if e := wantCode(t, err, http.StatusBadRequest, "bad_offset"); e.Offset != 1<<20 {
		t.Fatalf("bad_offset gives %d", e.Offset)
	}
	if err := second.SendFile("VID-1", "video", "clip.mp4", "video/mp4", 0, data); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(g.sink.files["VID-1"], data) {
		t.Fatal("the resumed file differs")
	}
	if off, err := second.Offer(info); err != nil || off != int64(len(data)) {
		t.Fatalf("offer of a file in: %d %v", off, err)
	}
	_, err = second.Chunk("VID-1", int64(len(data)), []byte{1})
	if e := wantCode(t, err, http.StatusBadRequest, "bad_offset"); e.Offset != int64(len(data)) {
		t.Fatalf("chunk after the end: %+v", e)
	}
	if done, err := second.Done(); err != nil || done.Videos != 1 {
		t.Fatalf("done %+v %v", done, err)
	}
}

func fileInfo(id, kind, name string, data []byte) oldphone.FileInfo {
	sum := sha256Hex(data)
	return oldphone.FileInfo{ID: id, Kind: kind, Name: name, Size: int64(len(data)), Mime: "video/mp4", SHA256: sum}
}

// A file whose bytes do not hash to what was offered is dropped with 422
// and starts again from 0.
func TestCorruptedChunk(t *testing.T) {
	g := newRig(t, []string{"photos"}, nil)
	c := g.phone(nil)
	data := []byte("the photo's real bytes")
	if _, err := c.Offer(fileInfo("p1", "photo", "a.jpg", data)); err != nil {
		t.Fatal(err)
	}
	bad := append([]byte{}, data...)
	bad[3] ^= 0x20
	_, err := c.Chunk("p1", 0, bad)
	wantCode(t, err, http.StatusUnprocessableEntity, "bad_hash")
	if len(g.sink.files) != 0 {
		t.Fatal("the bad file was stored")
	}
	if off, err := c.Offer(fileInfo("p1", "photo", "a.jpg", data)); err != nil || off != 0 {
		t.Fatalf("offer again: %d %v", off, err)
	}
	if done, _ := c.Done(); len(done.Failed) != 1 || done.Failed[0].Reason != "sha256" || done.Failed[0].Kind != "photo" {
		t.Fatalf("failed %+v", done.Failed)
	}
}

// An envelope sent twice is refused the second time (409, sealed); a
// retry is a new envelope.
func TestReplay(t *testing.T) {
	g := newRig(t, []string{"sms"}, nil)
	c := g.phone(nil)
	env := c.Seal("POST", "/v1/items/sms", []byte(`{"items":[{"address":"1","body":"x","type":"in","at":1000,"read":false}],"last":true}`))
	if r, err := c.Send("POST", "/v1/items/sms", env, false); err != nil || r.Status != http.StatusOK {
		t.Fatalf("first %+v %v", r, err)
	}
	r, err := c.Send("POST", "/v1/items/sms", env, false)
	if err != nil || r.Status != http.StatusConflict || !r.Sealed || !strings.Contains(string(r.Plain), `"code":"replay"`) {
		t.Fatalf("replay %+v %s %v", r, r.Plain, err)
	}
	// An older counter than the last is a replay too.
	c.SetCounter(0)
	_, _, err = c.Items("sms", []any{map[string]any{"address": "2", "body": "y", "type": "in", "at": 1000}})
	wantCode(t, err, http.StatusConflict, "replay")
	if len(g.sink.msgs) != 1 {
		t.Fatalf("stored %d", len(g.sink.msgs))
	}
}

// What does not open under the session's keys gets 401 in plain JSON.
func TestWrongKey(t *testing.T) {
	g := newRig(t, []string{"contacts"}, nil)
	other := g.p
	other.Token = bytes.Repeat([]byte{9}, 32)
	c, _ := oldphone.New(other, nil)
	_, err := c.SayHello(oldphone.Device{}, nil)
	wantCode(t, err, http.StatusUnauthorized, "bad_key")
	r, err := c.Send("POST", "/v1/items/contacts", []byte("garbage"), false)
	if err != nil || r.Status != http.StatusUnauthorized || r.Sealed || string(r.Plain) != `{"error":"the envelope does not open","code":"bad_key"}` {
		t.Fatalf("garbage: %+v %s %v", r, r.Plain, err)
	}
	// A real app key, but no hello yet: the AAD differs, nothing opens.
	good, _ := oldphone.New(g.p, nil)
	r, err = good.Send("POST", "/v1/items/contacts", good.Seal("POST", "/v1/items/contacts", []byte(`{"items":[]}`)), false)
	if err != nil || r.Status != http.StatusUnauthorized {
		t.Fatalf("before hello: %+v %v", r, err)
	}
	// An envelope sealed for another path does not open on this one.
	good = g.phone(nil)
	r, _ = good.Send("POST", "/v1/items/contacts", good.Seal("POST", "/v1/items/sms", []byte(`{"items":[]}`)), false)
	if r.Status != http.StatusUnauthorized {
		t.Fatalf("other path: %+v", r)
	}
	if p := g.s.Progress(); p.State != switchrecv.StateReceiving {
		t.Fatalf("%+v", p)
	}
}

// Kinds the user did not tick are refused, as items and as files.
func TestNotAccepted(t *testing.T) {
	g := newRig(t, []string{"contacts", "photos"}, nil)
	c := g.phone(nil)
	if !slices.Equal(c.Hello.Accept, []string{"contacts", "photos"}) {
		t.Fatalf("accept %v", c.Hello.Accept)
	}
	_, _, err := c.Items("sms", []any{map[string]any{"address": "1", "body": "x", "type": "in", "at": 1}})
	wantCode(t, err, http.StatusForbidden, "not_accepted")
	_, err = c.Offer(fileInfo("v1", "video", "a.mp4", []byte("x")))
	wantCode(t, err, http.StatusForbidden, "not_accepted")
	_, _, err = c.Items("photos", []any{})
	wantCode(t, err, http.StatusNotFound, "not_found")
}

// A body over max_body is refused unread (413); the client sends the
// batch again in halves.
func TestBodyTooLarge(t *testing.T) {
	g := newRig(t, []string{"contacts"}, nil)
	c := g.phone(nil)
	r, err := c.Send("POST", "/v1/items/contacts", make([]byte, switchrecv.MaxBody+1), false)
	if err != nil || r.Status != http.StatusRequestEntityTooLarge || !r.Sealed || !strings.Contains(string(r.Plain), "too_large") {
		t.Fatalf("too large: %+v %s %v", r, r.Plain, err)
	}
	photo := strings.Repeat("A", 76) + "\r\n "
	var batch []any
	for i := range 500 {
		v := fmt.Sprintf("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Person %d\r\nTEL:1390000%04d\r\nPHOTO;ENCODING=b:%s\r\nEND:VCARD\r\n", i, i, strings.Repeat(photo, 120))
		batch = append(batch, map[string]string{"vcard": v})
	}
	stored, _, err := c.Items("contacts", batch)
	if err != nil || stored != 500 || len(g.sink.vcards) != 500 {
		t.Fatalf("split batch: %d %v", stored, err)
	}
	// Before any request opened there is no key to seal under.
	g2 := newRig(t, []string{"contacts"}, nil)
	c2, _ := oldphone.New(g2.p, nil)
	r, err = c2.Send("POST", "/v1/hello", make([]byte, switchrecv.MaxBody+1), true)
	if err != nil || r.Status != http.StatusRequestEntityTooLarge || r.Sealed {
		t.Fatalf("too large before hello: %+v %v", r, err)
	}
}

// Thirty minutes (here a moment) without a request end the session.
func TestIdleTimeout(t *testing.T) {
	changes := make(chan struct{}, 64)
	g := newRig(t, []string{"contacts"}, func(cfg *switchrecv.Config) {
		cfg.Idle = 150 * time.Millisecond
		cfg.OnChange = func() { changes <- struct{}{} }
	})
	c := g.phone(nil)
	for range 3 {
		time.Sleep(80 * time.Millisecond)
		if _, _, err := c.Items("contacts", []any{}); err != nil {
			t.Fatal("a request kept the session up, it ended:", err)
		}
	}
	select {
	case <-g.s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the session did not end")
	}
	if p := g.s.Progress(); p.State != switchrecv.StateIdle {
		t.Fatalf("%+v", p)
	}
	_, _, err := c.Items("contacts", []any{})
	wantCode(t, err, http.StatusUnauthorized, "bad_key")
	if len(changes) == 0 {
		t.Fatal("no change reported")
	}
}

// Files over the limits, a bad offer and bad chunks.
func TestFileLimits(t *testing.T) {
	g := newRig(t, []string{"photos"}, func(cfg *switchrecv.Config) { cfg.MaxFile, cfg.MaxTotal = 100, 150 })
	c := g.phone(nil)
	_, err := c.Offer(fileInfo("big", "photo", "big.jpg", make([]byte, 101)))
	wantCode(t, err, http.StatusInsufficientStorage, "too_big")
	if _, err := c.Offer(fileInfo("a", "photo", "a.jpg", make([]byte, 100))); err != nil {
		t.Fatal(err)
	}
	_, err = c.Offer(fileInfo("b", "photo", "b.jpg", make([]byte, 60)))
	wantCode(t, err, http.StatusInsufficientStorage, "no_space")
	for _, o := range []oldphone.FileInfo{
		{ID: "../x", Kind: "photo", Name: "x", Size: 1, SHA256: sha256Hex(nil)},
		{ID: "x", Kind: "photos", Name: "x", Size: 1, SHA256: sha256Hex(nil)},
		{ID: "x", Kind: "photo", Name: "/", Size: 1, SHA256: sha256Hex(nil)},
		{ID: "x", Kind: "photo", Name: "x", Size: 0, SHA256: sha256Hex(nil)},
		{ID: "x", Kind: "photo", Name: "x", Size: 1, SHA256: "abc"},
	} {
		_, err := c.Offer(o)
		wantCode(t, err, http.StatusBadRequest, "bad_offer")
	}
	_, err = c.Chunk("a", 0, make([]byte, 101))
	wantCode(t, err, http.StatusBadRequest, "bad_chunk")
	_, err = c.Chunk("nope", 0, []byte{1})
	wantCode(t, err, http.StatusNotFound, "not_found")
	_, err = c.Chunk("a", 5, []byte{1})
	if e := wantCode(t, err, http.StatusBadRequest, "bad_offset"); e.Offset != 0 {
		t.Fatalf("offset %d", e.Offset)
	}
}
