package jsrt

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	be "encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// The network beyond plain fetch: response bodies read as streams (with
// their limits and timeouts), AbortSignal on fetch and WebSocket, the
// per-app cookie jar, TextEncoder and TextDecoder, and WebSocket against a
// server that also misbehaves (huge frames, a flood, bad UTF-8, a wrong
// handshake). The same cases hold on Node.js.
func TestNet(t *testing.T)     { testNet(t, jsrtEngine(t), true) }
func TestNodeNet(t *testing.T) { testNet(t, nodeEngine(t), false) }

// wsServer is a WebSocket server for the tests: RFC 6455 as far as they
// need it, the query picking what it does.
type wsServer struct {
	sent atomic.Int64
}

func (ws *wsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Has("status") {
		http.Error(w, "no", http.StatusForbidden)
		return
	}
	if q.Has("stall") {
		<-r.Context().Done()
		return
	}
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if q.Has("badaccept") {
		sum[0] ^= 0xff
	}
	accept := base64.StdEncoding.EncodeToString(sum[:])
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	head := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n"
	if p := q.Get("proto"); p != "" {
		head += "Sec-WebSocket-Protocol: " + p + "\r\n"
	}
	rw.WriteString(head + "\r\n")
	// The flood's goroutine sends while the handler answers the client
	// (its close): one writer at a time on the connection's buffer.
	var wmu sync.Mutex
	flush := func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		if _, err := rw.Write(b); err != nil {
			return err
		}
		return rw.Flush()
	}
	send := func(op byte, p []byte) error {
		b := []byte{0x80 | op}
		switch n := len(p); {
		case n < 126:
			b = append(b, byte(n))
		case n < 65536:
			b = append(b, 126, byte(n>>8), byte(n))
		default:
			b = append(b, 127)
			b = be.BigEndian.AppendUint64(b, uint64(n))
		}
		if err := flush(append(b, p...)); err != nil {
			return err
		}
		ws.sent.Add(int64(len(p)))
		return nil
	}
	switch {
	case q.Has("hello"):
		// In the same write as the handshake's answer.
		send(1, []byte("hello"))
	case q.Has("huge"):
		b := be.BigEndian.AppendUint64([]byte{0x82, 127}, 5<<20)
		flush(append(b, make([]byte, 1024)...))
	case q.Has("bye"):
		send(8, append([]byte{0x0f, 0xa1}, "bye"...))
	case q.Has("badutf8"):
		send(1, []byte{0xc3, 0x28})
	case q.Has("ping"):
		send(9, []byte("hi"))
	case q.Has("auth"):
		send(1, []byte(r.Header.Get("Authorization")))
	case q.Has("flood"):
		go func() {
			chunk := make([]byte, 64<<10)
			for ws.sent.Load() < 256<<20 && send(2, chunk) == nil {
			}
		}()
	}
	if err := flush(nil); err != nil {
		return
	}
	for {
		op, p, err := readClientFrame(rw.Reader)
		if err != nil {
			return
		}
		switch op {
		case 1, 2:
			if !q.Has("flood") {
				send(op, p)
			}
		case 8:
			send(8, p)
			return
		case 9:
			send(10, p)
		case 10:
			if q.Has("ping") && string(p) == "hi" {
				send(1, []byte("pong ok"))
			}
		}
	}
}

// readClientFrame reads one frame a client sent (masked) and unmasks it.
func readClientFrame(r *bufio.Reader) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	if h[1]&0x80 == 0 {
		return 0, nil, fmt.Errorf("an unmasked frame from the client")
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(be.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = be.BigEndian.Uint64(b[:])
	}
	var mask [4]byte
	if _, err := io.ReadFull(r, mask[:]); err != nil {
		return 0, nil, err
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return 0, nil, err
	}
	for i := range p {
		p[i] ^= mask[i&3]
	}
	return h[0] & 0x0f, p, nil
}

// blackHole takes connections and never answers (not even TLS's
// handshake), counting those the client has not closed.
type blackHole struct {
	ln             net.Listener
	accepted, open atomic.Int64
}

func newBlackHole(t *testing.T) *blackHole {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := &blackHole{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			h.accepted.Add(1)
			h.open.Add(1)
			go func() {
				io.Copy(io.Discard, c)
				c.Close()
				h.open.Add(-1)
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return h
}

// dropped waits until the client has closed every connection it made.
func (h *blackHole) dropped(t *testing.T, what string) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); h.accepted.Load() == 0 || h.open.Load() > 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%s: %d of %d connections still open", what, h.open.Load(), h.accepted.Load())
		}
	}
}

// peak is the app's peak resident memory (VmHWM), in KB.
func (s *shell) peak() int {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(s.cmd.Process.Pid), "status"))
	if err != nil {
		s.t.Fatal(err)
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "VmHWM:" {
			n, _ := strconv.Atoi(f[1])
			return n
		}
	}
	return 0
}

func testNet(t *testing.T, start engine, device bool) {
	ws := &wsServer{}
	endless := make(chan int64, 4)
	mux := http.NewServeMux()
	mux.Handle("/", fetchServer())
	mux.Handle("/ws", ws)
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("s", 16<<10)
		for i := 0; i < 64; i++ {
			io.WriteString(w, chunk)
			http.NewResponseController(w).Flush()
		}
	})
	mux.HandleFunc("/endless", func(w http.ResponseWriter, r *http.Request) {
		chunk := []byte(strings.Repeat("e", 64<<10))
		var n int64
		for {
			if _, err := w.Write(chunk); err != nil || http.NewResponseController(w).Flush() != nil {
				endless <- n
				return
			}
			n += int64(len(chunk))
		}
	})
	mux.HandleFunc("/hold", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "a")
		http.NewResponseController(w).Flush()
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
		io.WriteString(w, "b")
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "%s|%s|%d|%d", r.Method, strings.Join(r.TransferEncoding, ","), r.ContentLength, len(raw))
	})
	// trickle answers at once, with a dot for each piece of the body it
	// reads, and then a few more.
	mux.HandleFunc("/trickle", func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		rc.EnableFullDuplex()
		io.WriteString(w, "<")
		rc.Flush()
		n, buf := 0, make([]byte, 4096)
		for {
			k, err := r.Body.Read(buf)
			n += k
			if k > 0 {
				io.WriteString(w, ".")
				rc.Flush()
			}
			if err != nil {
				break
			}
		}
		for i := 0; i < 4; i++ {
			time.Sleep(200 * time.Millisecond)
			io.WriteString(w, ".")
			rc.Flush()
		}
		fmt.Fprintf(w, "%d>", n)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/hello", http.StatusFound)
	})
	mux.HandleFunc("/sink", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/cookie/rules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "p=1; Path=/cookie/deep")
		w.Header().Add("Set-Cookie", "x=1; Path=/")
		io.WriteString(w, "rules")
	})
	mux.HandleFunc("/cookie/deep/get", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Header.Get("Cookie"))
	})
	mux.HandleFunc("/cookie/prefix", func(w http.ResponseWriter, r *http.Request) {
		for _, c := range []string{"__Host-a=1; Secure; Path=/", "__Host-c=1; Path=/", "__Host-d=1; Secure; Path=/x", "__Secure-ok=1; Secure; Path=/", "__Secure-no=1; Path=/"} {
			w.Header().Add("Set-Cookie", c)
		}
		io.WriteString(w, "prefix")
	})
	mux.HandleFunc("/cookie/drop", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "x=gone; Max-Age=0; Path=/")
		io.WriteString(w, "drop")
	})
	silent := make(chan time.Duration, 4)
	mux.HandleFunc("/silent", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
		silent <- time.Since(start)
	})
	mux.HandleFunc("/cookie/set", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "sid=abc; Path=/")
		w.Header().Add("Set-Cookie", "keep=1; Max-Age=3600; Path=/")
		io.WriteString(w, "set")
	})
	mux.HandleFunc("/cookie/get", func(w http.ResponseWriter, r *http.Request) {
		if c := r.Header.Get("Cookie"); c != "" {
			io.WriteString(w, c)
		} else {
			io.WriteString(w, "-")
		}
	})
	secure := httptest.NewTLSServer(mux)
	defer secure.Close()
	plain := httptest.NewServer(ws)
	defer plain.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fetchCAEnv, ca)
	t.Setenv("NODE_EXTRA_CA_CERTS", ca)
	wss := "wss" + strings.TrimPrefix(secure.URL, "https") + "/ws"

	dir, data := appCopy(t, "fetch"), t.TempDir()
	s := start(t, data, "en", filepath.Join(dir, "main.js"))
	s.hello("", nil)
	s.screenText("ready")
	run := func(plan map[string]any, want string) string {
		t.Helper()
		raw, _ := json.Marshal(plan)
		s.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "fetch", Value: string(raw)})
		return s.screenText(want)
	}
	drained := func(what string) int64 {
		t.Helper()
		select {
		case n := <-endless:
			return n
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the endless response was not cancelled", what)
		}
		return 0
	}

	run(map[string]any{"coding": true}, "coding 14 true true a true TypeError RangeError")
	run(map[string]any{"signals": true}, "signals false true on abort,listener AbortError: This operation was aborted true why")

	// Streams: any length in chunks of at most 64 KB; cancelled, the
	// transfer stops; read whole is still at most 8 MB.
	run(map[string]any{"url": secure.URL + "/big", "read": "stream"}, "ok 200 true false - 9437184 true true true")
	run(map[string]any{"url": secure.URL + "/stream", "read": "iterate"}, "ok 200 true false - 1048576 true true true")
	run(map[string]any{"url": secure.URL + "/endless", "read": "stream", "stop": 20}, "ok 200 true false - ")
	if n := drained("cancel"); n > 64<<20 {
		t.Errorf("the server wrote %d bytes before the cancel took", n)
	}
	run(map[string]any{"url": secure.URL + "/hold", "read": "stream", "init": map[string]any{"timeout": 300}},
		"err TypeError: fetch: no data for the timeout while the body was read after 200")
	run(map[string]any{"url": secure.URL + "/hold", "init": map[string]any{"timeout": 300}}, "err TypeError: fetch: timed out after 200")
	run(map[string]any{"url": secure.URL + "/hello", "read": "mixed"}, "ok 200 true false yes hi TypeError: fetch: the body was read whole (text(), json(), arrayBuffer()) hi")
	run(map[string]any{"url": secure.URL + "/stream", "read": "mixed2"}, "ok 200 true false - TypeError: fetch: the body was read as a stream")

	// AbortSignal: before, while waiting, with a reason, by a timeout,
	// while the body streams.
	run(map[string]any{"url": secure.URL + "/hello", "abort": "before"}, "err AbortError: This operation was aborted")
	run(map[string]any{"url": secure.URL + "/slow", "abort": 100}, "err AbortError: This operation was aborted")
	run(map[string]any{"url": secure.URL + "/slow", "abort": 50, "abortReason": "mine"}, "err Error: mine")
	run(map[string]any{"url": secure.URL + "/slow", "timeoutSignal": 100}, "err TimeoutError: The operation was aborted due to timeout")
	run(map[string]any{"url": secure.URL + "/endless", "read": "stream", "abortAfter": 3}, "err AbortError: This operation was aborted after 200")
	drained("abort")
	run(map[string]any{"url": secure.URL + "/hello", "init": map[string]any{"signal": "x"}}, "err TypeError: fetch: signal is an AbortSignal")
	// Aborted while it connects (a server that never answers TLS) or while
	// the server is silent, a request lets go of its place among the 8 and,
	// on the device, of its connection at once (Node.js's fetch keeps a
	// connection that is still being made).
	hole := newBlackHole(t)
	run(map[string]any{"burst": true, "url": "https://" + hole.ln.Addr().String() + "/", "next": secure.URL + "/hello"}, "burst AbortError next 200 hi")
	if device {
		hole.dropped(t, "aborted requests")
	}
	run(map[string]any{"url": secure.URL + "/silent", "abort": 100}, "err AbortError: This operation was aborted")
	select {
	case d := <-silent:
		if d > 600*time.Millisecond {
			t.Errorf("the server held the aborted request for %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("the server did not see the aborted request")
	}
	run(map[string]any{"any": true}, "any true TimeoutError true x y TypeError")

	// A body from a stream goes chunked as it comes, and its 3xx is the
	// app's; the stream failing, giving text, locked, without duplex: "half"
	// or on a GET fails the request; a stream too slow times out, an abort
	// cancels it; one that never ends against a server that never reads is
	// read only as far as the network takes it.
	up := secure.URL + "/upload"
	run(map[string]any{"streams": true}, "streams a,b1,b2 TypeError false from 1,2,3 cancel stop error bad desired 3 2 response hello break cancelled false blob true [object ReadableStream]")
	run(map[string]any{"upload": true, "url": up, "chunks": 20, "size": 100000}, "ok 200 POST|chunked|-1|2000000 | cancelled - |")
	run(map[string]any{"upload": true, "url": up, "request": true, "method": "PUT", "chunks": 3, "size": 10}, "ok 200 PUT|chunked|-1|30 | cancelled - |")
	run(map[string]any{"upload": true, "url": up, "pipe": secure.URL + "/stream"}, "ok 200 POST|chunked|-1|1048576 | cancelled - |")
	run(map[string]any{"upload": true, "url": secure.URL + "/redirect", "chunks": 2}, "ok 302 ")
	run(map[string]any{"upload": true, "url": up, "chunks": 5, "fail": 2}, "err TypeError: fetch: the body's stream failed: boom (cause boom) | cancelled - |")
	run(map[string]any{"upload": true, "url": up, "chunks": 5, "text": 1},
		"err TypeError: fetch: the body's stream gave a chunk that is not bytes (a Uint8Array; text goes through TextEncoder) | cancelled TypeError |")
	run(map[string]any{"upload": true, "url": up, "noDuplex": true}, `err TypeError: fetch: a body that is a stream needs duplex: "half" | cancelled - |`)
	run(map[string]any{"upload": true, "url": up, "request": true, "noDuplex": true}, `err TypeError: Request: a body that is a stream needs duplex: "half" | cancelled - |`)
	run(map[string]any{"upload": true, "url": up, "lock": true}, "err TypeError: fetch: the body's stream is locked (read already, or by another reader) | cancelled - |")
	run(map[string]any{"upload": true, "url": up, "method": "GET"}, "err TypeError: fetch: a GET or HEAD request has no body | cancelled - |")
	run(map[string]any{"upload": true, "url": up, "chunks": 3, "delay": 600, "init": map[string]any{"timeout": 300}},
		"err TypeError: fetch: no data from the body's stream for the timeout | cancelled TypeError |")
	run(map[string]any{"upload": true, "url": up, "chunks": 10, "delay": 200, "abort": 100}, "err AbortError: This operation was aborted | cancelled AbortError |")
	// The answer comes while the body still goes, longer in all than the
	// timeout but never quiet that long: neither times out.
	run(map[string]any{"upload": true, "url": secure.URL + "/trickle", "chunks": 4, "delay": 150, "stream": true, "init": map[string]any{"timeout": 600}},
		"ok 200 <.4000> | cancelled - |")
	run(map[string]any{"upload": true, "url": secure.URL + "/sink", "size": 65536, "abort": 1000}, "err AbortError: This operation was aborted | cancelled AbortError bounded true |")

	// Cookies: only with credentials "include"; the jar outlives the
	// process, a session cookie does not.
	run(map[string]any{"url": secure.URL + "/cookie/get", "init": map[string]any{"credentials": "include"}}, "ok 200 true false - -")
	run(map[string]any{"url": secure.URL + "/cookie/set", "init": map[string]any{"credentials": "include"}}, "ok 200 true false - set")
	run(map[string]any{"url": secure.URL + "/cookie/get", "init": map[string]any{"credentials": "include"}}, "ok 200 true false - keep=1; sid=abc")
	run(map[string]any{"url": secure.URL + "/cookie/get"}, "ok 200 true false - -")
	run(map[string]any{"url": secure.URL + "/cookie/get", "init": map[string]any{"credentials": "always"}}, `err TypeError: fetch: credentials is "omit", "same-origin" or "include"`)
	// RFC 6265's paths (the longer first) and Max-Age=0 taking a cookie away.
	include := map[string]any{"credentials": "include"}
	run(map[string]any{"url": secure.URL + "/cookie/rules", "init": include}, "ok 200 true false - rules |")
	run(map[string]any{"url": secure.URL + "/cookie/get", "init": include}, "ok 200 true false - keep=1; sid=abc; x=1 |")
	run(map[string]any{"url": secure.URL + "/cookie/deep/get", "init": include}, "ok 200 true false - p=1; keep=1; sid=abc; x=1 |")
	run(map[string]any{"url": secure.URL + "/cookie/drop", "init": include}, "ok 200 true false - drop |")
	run(map[string]any{"url": secure.URL + "/cookie/get", "init": include}, "ok 200 true false - keep=1; sid=abc |")
	// The prefixes: __Host- only Secure, on Path=/ and for the host alone;
	// __Secure- only Secure. The image's libcurl (8.19) keeps to them, as
	// Node.js's jar does; an older one (this machine's 7.61) knows none.
	run(map[string]any{"url": secure.URL + "/cookie/prefix", "init": include}, "ok 200 true false - prefix |")
	got := run(map[string]any{"url": secure.URL + "/cookie/get", "init": include}, "ok 200 true false - ")
	if device && strings.Contains(got, "__Host-c") && strings.Contains(got, "__Secure-no") {
		t.Logf("cookie prefixes not checked: this libcurl does not know them (%q)", got)
	} else {
		for _, c := range []string{"__Host-a=1", "__Secure-ok=1", "__Host-c", "__Host-d", "__Secure-no"} {
			if strings.Contains(got, c) != strings.HasSuffix(c, "=1") {
				t.Errorf("cookie prefixes: %q in %q", c, got)
			}
		}
	}

	// WebSocket: echo, protocols, headers, a server that closes or pings,
	// handshakes that fail, frames that break the rules.
	run(map[string]any{"ws": wss, "send": []any{"hi", map[string]any{"bin": "ab"}}, "count": 2},
		"state 0 arraybuffer | open  1 | text hi | binary 2 | got 2 | close 1000 enough true 3")
	run(map[string]any{"ws": wss + "?proto=chat", "protocols": []string{"chat", "json"}, "close": []any{1000}}, "open chat 1 | close 1000  true 3")
	run(map[string]any{"ws": wss + "?hello", "count": 1}, "open  1 | text hello | got 1 | close 1000 enough true 3")
	run(map[string]any{"ws": wss + "?auth", "options": map[string]any{"headers": map[string]any{"Authorization": "Bearer t"}}, "count": 1},
		"text Bearer t | got 1")
	run(map[string]any{"ws": wss + "?ping", "count": 1}, "text pong ok | got 1 | close 1000 enough true 3")
	run(map[string]any{"ws": wss + "?bye"}, "open  1 | close 4001 bye true 3")
	run(map[string]any{"ws": wss + "?proto=other", "protocols": "chat"},
		"error the server chose a protocol the client did not offer | close 1006 the server chose a protocol the client did not offer false 3")
	run(map[string]any{"ws": wss + "?badaccept"}, "error the server's Sec-WebSocket-Accept is wrong | close 1006")
	run(map[string]any{"ws": wss + "?status"}, "error the server did not switch to WebSocket (no 101 answer) | close 1006")
	run(map[string]any{"ws": wss + "?huge"}, "open  1 | error a message over the limit | close 1009 a message over the limit false 3")
	run(map[string]any{"ws": wss + "?badutf8"}, "open  1 | error a text message that is not UTF-8 | close 1007")
	run(map[string]any{"ws": wss, "abortOpen": true}, "open  1 | close 1006 aborted false 3")
	run(map[string]any{"ws": "wss://" + hole.ln.Addr().String() + "/", "abort": 200}, "state 0 arraybuffer | close 1006 aborted false 3")
	if device {
		hole.dropped(t, "a WebSocket aborted while connecting")
	}
	run(map[string]any{"ws": wss + "?stall", "abort": 200}, "state 0 arraybuffer | close 1006 aborted false 3")
	run(map[string]any{"ws": "ws" + strings.TrimPrefix(plain.URL, "http") + "/ws", "send": []any{"plain"}, "count": 1}, "open  1 | text plain | got 1")
	run(map[string]any{"ws": "ws://example.com/"}, "throw SyntaxError: WebSocket: ws:// only to the device's own loopback")
	run(map[string]any{"ws": wss + "#x"}, "throw SyntaxError: WebSocket: a WebSocket URL has no fragment")
	run(map[string]any{"ws": wss, "protocols": []string{"a b"}}, "throw TypeError: WebSocket: a protocol that is not a token")
	run(map[string]any{"ws": wss, "options": map[string]any{"headers": map[string]any{"Sec-WebSocket-Key": "x"}}},
		"throw TypeError: WebSocket: a header of the handshake itself")

	// A flood while the app is busy: the runtime stops reading at 8 MB
	// waiting for the app, so the app's memory stays bounded.
	ws.sent.Store(0)
	run(map[string]any{"ws": wss + "?flood", "busy": 1500, "count": 50, "quiet": true}, "got 50 | close 1000 enough")
	if device {
		if kb := s.peak(); kb > 96<<10 {
			t.Errorf("peak memory %d KB after a flood", kb)
		}
	}

	// A new process: the jar's lasting cookie is there, the session one
	// is not.
	s = start(t, data, "en", filepath.Join(dir, "main.js"))
	s.hello("", nil)
	s.screenText("ready")
	run(map[string]any{"url": secure.URL + "/cookie/get", "init": map[string]any{"credentials": "include"}}, "ok 200 true false - keep=1")
	if raw, err := os.ReadFile(filepath.Join(data, "kario-cookies.txt")); err != nil || !strings.Contains(string(raw), "keep") {
		t.Errorf("cookie jar: %q %v", raw, err)
	}

	// Without "net" in the manifest there is no WebSocket either.
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id": "com.example.test", "name": "Fetch test", "version": "1.0", "runtime": "js", "main": "main.js"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s = start(t, t.TempDir(), "en", filepath.Join(dir, "main.js"))
	s.hello("", nil)
	s.screenText("ready")
	run(map[string]any{"ws": wss}, `throw TypeError: WebSocket: the app's manifest does not ask for the "net" permission`)
}
