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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	if q.Has("badaccept") {
		accept = "x" + accept[1:]
	}
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
		if _, err := rw.Write(append(b, p...)); err != nil {
			return err
		}
		ws.sent.Add(int64(len(p)))
		return rw.Flush()
	}
	switch {
	case q.Has("hello"):
		// In the same write as the handshake's answer.
		send(1, []byte("hello"))
	case q.Has("huge"):
		b := be.BigEndian.AppendUint64([]byte{0x82, 127}, 5<<20)
		rw.Write(append(b, make([]byte, 1024)...))
		rw.Flush()
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
	if err := rw.Flush(); err != nil {
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

	// Cookies: only with credentials "include"; the jar outlives the
	// process, a session cookie does not.
	run(map[string]any{"url": secure.URL + "/cookie/get", "init": map[string]any{"credentials": "include"}}, "ok 200 true false - -")
	run(map[string]any{"url": secure.URL + "/cookie/set", "init": map[string]any{"credentials": "include"}}, "ok 200 true false - set")
	run(map[string]any{"url": secure.URL + "/cookie/get", "init": map[string]any{"credentials": "include"}}, "ok 200 true false - keep=1; sid=abc")
	run(map[string]any{"url": secure.URL + "/cookie/get"}, "ok 200 true false - -")
	run(map[string]any{"url": secure.URL + "/cookie/get", "init": map[string]any{"credentials": "always"}}, `err TypeError: fetch: credentials is "omit", "same-origin" or "include"`)

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
