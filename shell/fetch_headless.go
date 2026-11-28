package shell

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	be "encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/ui"
)

// fetchSink is the selftest's server for an app's own fetch and WebSocket:
// on the device's loopback, which an app with the "net" permission
// reaches, it keeps "METHOD PATH BODY" of each request and answers "ok",
// except on the paths examples/netcheck checks:
//
//	/stream       128 KB, a second's pause, 128 KB more, in chunks of 32 KB
//	/hold         no answer; "METHOD /hold gone" is kept once the client goes away
//	/cookie/set   sets sid=netcheck (for the session) and keep=1 (for an hour)
//	/cookie/get   the request's cookies, sorted by name ("" for none)
//	/ws           a WebSocket that echoes each unfragmented message, keeping "WS /ws TEXT"
type fetchSink struct {
	url string
	mu  sync.Mutex
	got []string
}

func (s *fetchSink) keep(line string) {
	s.mu.Lock()
	s.got = append(s.got, line)
	s.mu.Unlock()
}

func (s *fetchSink) seen(prefix, text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.got {
		if strings.HasPrefix(g, prefix) && strings.Contains(g, text) {
			return true
		}
	}
	return false
}

// fetchServe is the step fetch-server <host:port> (port 0: a free one, as
// selftests running at once on one machine need; fill's {fetch} is its
// URL); a second one on the same run keeps the first server.
func (r *Runner) fetchServe(arg string) error {
	if r.fetch != nil {
		return nil
	}
	ln, err := net.Listen("tcp", strings.TrimSpace(arg))
	if err != nil {
		return err
	}
	sink := &fetchSink{url: "http://" + ln.Addr().String()}
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(sink.serve)}
	go func() { _ = srv.Serve(ln) }()
	r.fetch = sink
	return nil
}

func (s *fetchSink) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	s.keep(req.Method + " " + req.URL.Path + " " + string(body))
	switch req.URL.Path {
	case "/stream":
		rc := http.NewResponseController(w)
		chunk := bytes.Repeat([]byte("s"), 32<<10)
		for i := 0; i < 8; i++ {
			if i == 4 {
				time.Sleep(time.Second)
			}
			if _, err := w.Write(chunk); err != nil || rc.Flush() != nil {
				return
			}
		}
	case "/hold":
		select {
		case <-req.Context().Done():
			s.keep(req.Method + " /hold gone")
		case <-time.After(time.Minute):
		}
	case "/cookie/set":
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "netcheck", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "keep", Value: "1", Path: "/", MaxAge: 3600})
		_, _ = io.WriteString(w, "set")
	case "/cookie/get":
		var sent []string
		for _, c := range req.Cookies() {
			sent = append(sent, c.Name+"="+c.Value)
		}
		sort.Strings(sent)
		_, _ = io.WriteString(w, strings.Join(sent, "; "))
	case "/ws":
		s.echo(w, req)
	default:
		_, _ = io.WriteString(w, "ok")
	}
}

// echo is a WebSocket server (RFC 6455) as far as the check needs one: it
// echoes text and binary messages, answers a ping with a pong and a close
// with a close.
func (s *fetchSink) echo(w http.ResponseWriter, req *http.Request) {
	key := req.Header.Get("Sec-WebSocket-Key")
	if !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") || key == "" {
		http.Error(w, "not a WebSocket handshake", http.StatusBadRequest)
		return
	}
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Minute))
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + wsAccept(key) + "\r\n\r\n")
	if rw.Flush() != nil {
		return
	}
	for {
		op, p, err := readWSFrame(rw.Reader)
		if err != nil {
			return
		}
		switch op {
		case 1, 2:
			s.keep("WS " + req.URL.Path + " " + string(p))
			err = writeWSFrame(rw.Writer, op, p)
		case 8:
			_ = writeWSFrame(rw.Writer, 8, p)
			return
		case 9:
			err = writeWSFrame(rw.Writer, 10, p)
		}
		if err != nil {
			return
		}
	}
}

// wsAccept is the server's Sec-WebSocket-Accept for the client's key.
func wsAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// readWSFrame reads one frame from the client, which masks every frame,
// and unmasks it; at most 1 MB.
func readWSFrame(r *bufio.Reader) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	if h[1]&0x80 == 0 {
		return 0, nil, errors.New("an unmasked frame from the client")
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
	if n > 1<<20 {
		return 0, nil, fmt.Errorf("a frame of %d bytes", n)
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

// writeWSFrame writes one whole frame, unmasked, as a server does.
func writeWSFrame(w *bufio.Writer, op byte, p []byte) error {
	b := []byte{0x80 | op}
	switch n := len(p); {
	case n < 126:
		b = append(b, byte(n))
	case n < 1<<16:
		b = be.BigEndian.AppendUint16(append(b, 126), uint16(n))
	default:
		b = be.BigEndian.AppendUint64(append(b, 127), uint64(n))
	}
	if _, err := w.Write(append(b, p...)); err != nil {
		return err
	}
	return w.Flush()
}

// fillText is fill's text with {chat} and {fetch} put in.
func (r *Runner) fillText(arg string) string {
	arg = strings.ReplaceAll(arg, "{chat}", strings.TrimSuffix(r.C.appEnvValue(ui.EnvChatServer), "/"))
	if r.fetch != nil {
		arg = strings.ReplaceAll(arg, "{fetch}", r.fetch.url)
	}
	if r.keep != nil {
		arg = strings.ReplaceAll(arg, "{keepalive}", r.keep.addr())
	}
	return arg
}

// fetchGot is the step fetch-got <METHOD> <path> <text> [ms]: the server got
// such a request with text in its body.
func (r *Runner) fetchGot(arg string) error {
	if r.fetch == nil {
		return errors.New("fetch-got before fetch-server")
	}
	rest, ms := lastNum(arg, 8000)
	f := strings.Fields(rest)
	if len(f) < 3 {
		return errors.New("usage: fetch-got <METHOD> <path> <text> [ms]")
	}
	prefix, text := f[0]+" "+f[1]+" ", strings.Join(f[2:], " ")
	if _, ok := r.waitFor(func(string) bool { return r.fetch.seen(prefix, text) }, ms); !ok {
		r.fetch.mu.Lock()
		defer r.fetch.mu.Unlock()
		return fmt.Errorf("no %s%s… on the server; it got %q", prefix, text, r.fetch.got)
	}
	return nil
}
