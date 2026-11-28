package shell

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The selftest's server for a TypeScript app's keepalive WebSocket
// (keep-server ... ws): the handshake, text messages counted as lines,
// keep-send as a text message, a ping and a close answered.
func TestKeepServerWebSocket(t *testing.T) {
	r := &Runner{C: snapCore(t), Out: io.Discard}
	if err := r.step("keep-server", "127.0.0.1:0 tls"); err == nil {
		t.Fatal("keep-server with a mode that is not ws")
	}
	if err := r.step("keep-server", "127.0.0.1:0 ws"); err != nil {
		t.Fatal(err)
	}
	defer r.step("keep-server", "off")
	conn, br := keepWSDial(t, r.fillText("{keepalive}"))
	if _, err := conn.Write(append(wsMasked(1, "HELLO"), wsMasked(1, "PING")...)); err != nil {
		t.Fatal(err)
	}
	for _, s := range [][2]string{{"keep-got", "1 HELLO"}, {"keep-got", "1 PING"}, {"keep-send", "\u4f60\u597d"}} {
		if err := r.step(s[0], s[1]); err != nil {
			t.Fatalf("%s %s: %v", s[0], s[1], err)
		}
	}
	if got := wsFrame(t, br); got != fmt.Sprintf("1 %q", "\u4f60\u597d") {
		t.Errorf("keep-send gave %s", got)
	}
	var got []string
	if _, err := conn.Write(append(wsMasked(9, "hi"), wsMasked(8, "\x03\xe8")...)); err != nil {
		t.Fatal(err)
	}
	got = append(got, wsFrame(t, br), wsFrame(t, br))
	if want := []string{`10 "hi"`, `8 "\x03\xe8"`}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("frames %q, want %q", got, want)
	}
}

// One line server for a C app's line connection and a TypeScript app's
// WebSocket at once: a connection that opens with a GET speaks
// WebSocket, and keep-send reaches each in its own way. A first line
// shorter than a GET is a line at once.
func TestKeepServerTellsWebSocket(t *testing.T) {
	r := &Runner{C: snapCore(t), Out: io.Discard}
	if err := r.step("keep-server", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer r.step("keep-server", "off")
	line, err := net.Dial("tcp", r.fillText("{keepalive}"))
	if err != nil {
		t.Fatal(err)
	}
	defer line.Close()
	if _, err := line.Write([]byte("GO\n")); err != nil {
		t.Fatal(err)
	}
	ws, br := keepWSDial(t, r.fillText("{keepalive}"))
	if _, err := ws.Write(wsMasked(1, "HELLO")); err != nil {
		t.Fatal(err)
	}
	for _, s := range [][2]string{{"keep-got", "1 GO"}, {"keep-got", "1 HELLO"}, {"keep-conns", "2"}, {"keep-send", "both"}} {
		if err := r.step(s[0], s[1]); err != nil {
			t.Fatalf("%s %s: %v", s[0], s[1], err)
		}
	}
	keepRead(t, line, "both")
	if got := wsFrame(t, br); got != fmt.Sprintf("1 %q", "both") {
		t.Errorf("keep-send gave the WebSocket %s", got)
	}
}

// keepWSDial opens a WebSocket to addr.
func keepWSDial(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	fmt.Fprintf(conn, "GET /live HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", key)
	br := bufio.NewReader(conn)
	hs, err := http.ReadResponse(br, nil)
	if err != nil || hs.StatusCode != http.StatusSwitchingProtocols || hs.Header.Get("Sec-WebSocket-Accept") != wsAccept(key) {
		t.Fatalf("handshake: %v %+v", err, hs)
	}
	return conn, br
}

// wsMasked is a client's frame.
func wsMasked(op byte, p string) []byte {
	mask := []byte{1, 2, 3, 4}
	b := append([]byte{0x80 | op, 0x80 | byte(len(p))}, mask...)
	for i := range len(p) {
		b = append(b, p[i]^mask[i&3])
	}
	return b
}

// wsFrame reads a short frame from the server: its opcode and payload.
func wsFrame(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		t.Fatal(err)
	}
	p := make([]byte, h[1]&0x7f)
	if _, err := io.ReadFull(br, p); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%d %q", h[0]&0x0f, p)
}

// keep-server on a host name: {keepalive} is that name with the port it
// took, which the app asks the system to dial (an address stays one).
func TestKeepServerByName(t *testing.T) {
	r := &Runner{C: snapCore(t), Out: io.Discard}
	for _, at := range []string{"localhost", "127.0.0.1"} {
		if err := r.step("keep-server", at+":0 ws"); err != nil {
			t.Fatal(err)
		}
		addr := r.fillText("{keepalive}")
		host, port, _ := net.SplitHostPort(addr)
		if host != at || port == "0" || port == "" {
			t.Errorf("keep-server %s:0 gave {keepalive} %s", at, addr)
		}
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if err := r.step("keep-server", "off"); err != nil {
			t.Fatal(err)
		}
	}
}
