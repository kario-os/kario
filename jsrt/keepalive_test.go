package jsrt

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	be "encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// A TypeScript app's keepalive WebSocket (21-push.md §8.3): the runtime
// asks the Shell for the socket (net.keepalive, one call at a time), speaks
// TLS and WebSocket on it, gives the server the app's heartbeat on "beat",
// keeps what arrived while the app was frozen, closes the one before when
// another gets its socket, and has the Shell let go of the socket when it
// ends. The same cases hold on Node.js under kario run, the socket coming
// through kario-native.node.
func TestKeepaliveWebSocket(t *testing.T)     { testKeepalive(t, jsrtEngine(t)) }
func TestNodeKeepaliveWebSocket(t *testing.T) { testKeepalive(t, nativeNodeEngine(t)) }

// keepServer is the WebSocket server the keepalive WebSockets reach: each
// connection, once its handshake is answered, is a keepPeer on peers.
type keepServer struct{ peers chan *keepPeer }

// keepPeer is the server's end of one connection: got has the client's
// text messages and its close ("close <code> <reason>"), and is closed
// with the connection.
type keepPeer struct {
	host    string
	rw      *bufio.ReadWriter
	mu      sync.Mutex
	closing bool
	got     chan string
}

func (ks *keepServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if rw.Flush() != nil {
		return
	}
	p := &keepPeer{host: r.Host, rw: rw, got: make(chan string, 16)}
	defer close(p.got)
	ks.peers <- p
	for {
		op, b, err := readClientFrame(rw.Reader)
		if err != nil {
			return
		}
		switch op {
		case 1:
			p.got <- string(b)
		case 8:
			code, reason := 1005, ""
			if len(b) >= 2 {
				code, reason = int(be.BigEndian.Uint16(b)), string(b[2:])
			}
			p.got <- fmt.Sprintf("close %d %s", code, reason)
			p.mu.Lock()
			answered := p.closing
			p.mu.Unlock()
			if !answered {
				p.send(8, b)
			}
			return
		case 9:
			p.send(10, b)
		}
	}
}

func (p *keepPeer) send(op byte, b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closing = p.closing || op == 8
	h := []byte{0x80 | op}
	switch n := len(b); {
	case n < 126:
		h = append(h, byte(n))
	case n < 65536:
		h = append(h, 126, byte(n>>8), byte(n))
	default:
		h = append(h, 127)
		h = be.BigEndian.AppendUint64(h, uint64(n))
	}
	if _, err := p.rw.Write(append(h, b...)); err != nil {
		return err
	}
	return p.rw.Flush()
}

// next is what the server got next from the client.
func (p *keepPeer) next(t *testing.T) string {
	t.Helper()
	select {
	case m, ok := <-p.got:
		if !ok {
			t.Fatal("the connection ended")
		}
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("nothing from the client")
	}
	return ""
}

func (ks *keepServer) peer(t *testing.T) *keepPeer {
	t.Helper()
	select {
	case p := <-ks.peers:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no WebSocket handshake reached the server")
	}
	return nil
}

// keepShell is the fake Shell of these cases: it keeps the requests that
// come among the screens, and the copy of the socket it handed over.
type keepShell struct {
	*shell
	reqs   []ui.Msg
	screen string
	held   *net.TCPConn
}

func (k *keepShell) read() {
	k.t.Helper()
	select {
	case m, ok := <-k.msgs:
		if !ok {
			k.t.Fatalf("app exited; log:\n%s", k.log())
		}
		switch {
		case m.T == ui.MsgReq:
			k.reqs = append(k.reqs, m)
		case m.Screen != nil:
			k.screen = screenString(m.Screen)
		}
	case <-time.After(5 * time.Second):
		k.t.Fatalf("nothing from the app (the screen: %q); log:\n%s", k.screen, k.log())
	}
}

// until waits for the screen to show want.
func (k *keepShell) until(want string) {
	k.t.Helper()
	for !strings.Contains(k.screen, want) {
		k.read()
	}
}

// req is the app's next request: net.keepalive with want.
func (k *keepShell) req(want ui.KeepaliveArgs) ui.Msg {
	k.t.Helper()
	for len(k.reqs) == 0 {
		k.read()
	}
	m := k.reqs[0]
	k.reqs = k.reqs[1:]
	var got ui.KeepaliveArgs
	if m.Cap != ui.CapNetKeepalive || json.Unmarshal(m.Args, &got) != nil || got != want {
		k.t.Fatalf("request %s %s, not net.keepalive %+v", m.Cap, m.Args, want)
	}
	return m
}

func (k *keepShell) act(id string, v any) {
	k.t.Helper()
	raw, _ := json.Marshal(v)
	k.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: id, Value: string(raw)})
}

func (k *keepShell) event(value string) {
	k.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventKeepalive, Value: value})
}

// grant answers req as the Shell does: it dials addr itself, keeps its
// copy (closing the one before, which the new connection replaces) and
// hands over the socket with the reply.
func (k *keepShell) grant(req ui.Msg, addr string) *net.TCPConn {
	k.t.Helper()
	k.letGo()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		k.t.Fatal(err)
	}
	k.held = c.(*net.TCPConn)
	f, err := k.held.File()
	if err != nil {
		k.t.Fatal(err)
	}
	var args ui.KeepaliveArgs
	_ = json.Unmarshal(req.Args, &args)
	data, _ := json.Marshal(ui.KeepaliveRes{EverySec: args.EverySec, Addr: addr})
	k.sendFiles(ui.Msg{T: ui.MsgRes, Req: req.Req, OK: true, Data: data}, f)
	return k.held
}

// letGo answers close: true as the Shell does: its copy is closed.
func (k *keepShell) letGo() {
	if k.held != nil {
		k.held.Close()
		k.held = nil
	}
}

func (k *keepShell) reply(req ui.Msg, err string) {
	k.send(ui.Msg{T: ui.MsgRes, Req: req.Req, OK: err == "", Err: err, Data: json.RawMessage(`{}`)})
}

// waiting is how much the socket has waiting to be read, seen on the fake
// Shell's copy as the Shell's watcher sees it.
func waiting(t *testing.T, c *net.TCPConn) int {
	t.Helper()
	raw, err := c.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	buf := make([]byte, 1<<20)
	raw.Control(func(fd uintptr) { n, _, _ = syscall.Recvfrom(int(fd), buf, syscall.MSG_PEEK|syscall.MSG_DONTWAIT) })
	return n
}

// holds says whether the app's process has c's socket open.
func (k *keepShell) holds(c *net.TCPConn) bool {
	k.t.Helper()
	raw, _ := c.SyscallConn()
	var st syscall.Stat_t
	raw.Control(func(fd uintptr) { syscall.Fstat(int(fd), &st) })
	dir := filepath.Join("/proc", strconv.Itoa(k.cmd.Process.Pid), "fd")
	es, err := os.ReadDir(dir)
	if err != nil {
		k.t.Fatal(err)
	}
	for _, e := range es {
		if l, _ := os.Readlink(filepath.Join(dir, e.Name())); l == fmt.Sprintf("socket:[%d]", st.Ino) {
			return true
		}
	}
	return false
}

func checksum(b []byte) uint32 {
	var h uint32
	for _, c := range b {
		h = h*31 + uint32(c)
	}
	return h
}

func testKeepalive(t *testing.T, start engine) {
	ks := &keepServer{peers: make(chan *keepPeer, 8)}
	plain := httptest.NewServer(ks)
	defer plain.Close()
	secure := httptest.NewTLSServer(ks)
	defer secure.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fetchCAEnv, ca)
	t.Setenv("NODE_EXTRA_CA_CERTS", ca)
	plainAddr, secureAddr := plain.Listener.Addr().String(), secure.Listener.Addr().String()
	plainPort, securePort := plain.Listener.Addr().(*net.TCPAddr).Port, secure.Listener.Addr().(*net.TCPAddr).Port
	local := func(name string) string { return "ws://127.0.0.1:" + strconv.Itoa(plainPort) + "/" + name }
	at := func(port, every int) ui.KeepaliveArgs {
		return ui.KeepaliveArgs{Host: "127.0.0.1", Port: port, EverySec: every}
	}
	release := ui.KeepaliveArgs{Close: true}

	dir := appCopy(t, "keepalive")
	k := &keepShell{shell: start(t, t.TempDir(), "en", filepath.Join(dir, "main.js"))}
	k.hello("", nil)
	k.until("ready")

	// The app's own call is refused, and never reaches the Shell; so is a
	// heartbeat out of range.
	k.act("call", nil)
	k.until("call net.keepalive: a TypeScript app keeps its own connection as a WebSocket (new WebSocket(url, protocols, { keepalive }))")
	for i, every := range []any{30, 4000, 600.5, "600"} {
		name := fmt.Sprintf("x%d", i)
		k.act("open", map[string]any{"name": name, "url": local(name), "every": every})
		k.until(name + " throw TypeError: WebSocket: keepalive is the heartbeat's interval in whole seconds, 60 to 3600")
	}

	// TLS on the socket the Shell dialled, to the name in the URL with no
	// lookup of it (the Shell dialled the local machine for example.com).
	k.act("open", map[string]any{"name": "a", "url": "wss://example.com:" + strconv.Itoa(securePort) + "/a", "every": 600, "hello": "HELLO"})
	shellA := k.grant(k.req(ui.KeepaliveArgs{Host: "example.com", Port: securePort, EverySec: 600}), secureAddr)
	a := ks.peer(t)
	if a.host != "example.com:"+strconv.Itoa(securePort) {
		t.Errorf("the handshake's Host: %q", a.host)
	}
	if m := a.next(t); m != "HELLO" {
		t.Fatalf("the server got %q", m)
	}
	k.until("a open")
	k.event(ui.KeepaliveBeat)
	k.until("a sent PING")
	if m := a.next(t); m != "PING" {
		t.Fatalf("the heartbeat: %q", m)
	}

	// Frozen, the data waits in the kernel, where the Shell sees it; thawed
	// with "data", the app has it whole.
	if err := k.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 100000)
	for i := range big {
		big[i] = byte(i*7 + i>>9)
	}
	text := "\u4fdd\u6d3b\u6d88\u606f"
	sent := make(chan error, 1)
	go func() {
		err := a.send(2, big)
		if err == nil {
			err = a.send(1, []byte(text))
		}
		sent <- err
	}()
	for end := time.Now().Add(5 * time.Second); waiting(t, shellA) == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			k.cmd.Process.Signal(syscall.SIGCONT)
			t.Fatal("no data waiting on the Shell's copy")
		}
	}
	if err := k.cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	k.event(ui.KeepaliveData)
	k.until(fmt.Sprintf("a binary 100000 %d", checksum(big)))
	k.until("a text " + text)
	k.until("event data")
	if err := <-sent; err != nil {
		t.Fatal(err)
	}

	// The server closes: the Shell is told to let go.
	a.send(8, append([]byte{0x0f, 0xa1}, "bye"...))
	k.until("a close 4001 bye true")
	k.reply(k.req(release), "")
	k.letGo()

	// Plain ws:// to a name, which resolves to where the Shell connected.
	k.act("open", map[string]any{"name": "b", "url": "ws://localhost:" + strconv.Itoa(plainPort) + "/b", "every": 900})
	k.grant(k.req(ui.KeepaliveArgs{Host: "localhost", Port: plainPort, EverySec: 900}), plainAddr)
	b := ks.peer(t)
	if b.host != "localhost:"+strconv.Itoa(plainPort) {
		t.Errorf("the handshake's Host: %q", b.host)
	}
	k.until("b open")

	// Two at once: the second's call waits for the first's reply. The first
	// getting its socket closes b; the second getting its socket drops the
	// first, still in its handshake (a server that never answers). Neither
	// tells the Shell to let go: it holds the latest.
	hole := newBlackHole(t)
	holePort := hole.ln.Addr().(*net.TCPAddr).Port
	k.act("open", map[string]any{"name": "c", "url": "ws://127.0.0.1:" + strconv.Itoa(holePort) + "/c", "every": 600})
	k.act("open", map[string]any{"name": "d", "url": local("d"), "every": 600})
	k.until("d state 0")
	if len(k.reqs) != 1 {
		t.Fatalf("%d calls out at once", len(k.reqs))
	}
	k.grant(k.req(at(holePort, 600)), hole.ln.Addr().String())
	k.until("b close 1001 replaced true")
	if m := b.next(t); m != "close 1001 replaced" {
		t.Errorf("the server got %q", m)
	}
	k.grant(k.req(at(plainPort, 600)), plainAddr)
	d := ks.peer(t)
	k.until("c close 1006 replaced false")
	k.until("d open")
	hole.dropped(t, "a keepalive WebSocket replaced in its handshake")
	k.event(ui.KeepaliveBeat)
	k.until("d sent PING")
	if m := d.next(t); m != "PING" {
		t.Fatalf("the heartbeat: %q", m)
	}

	// Cancelled before its socket came: the socket is closed when it comes,
	// d (whose connection the Shell replaced with it) is closed, and the
	// Shell told to let go.
	k.act("open", map[string]any{"name": "e", "url": local("e"), "every": 600, "abortable": true})
	reqE := k.req(at(plainPort, 600))
	k.act("abort", map[string]any{"name": "e"})
	k.until("e close 1006 aborted false")
	shellE := k.grant(reqE, plainAddr)
	k.until("d close 1001 replaced true")
	k.reply(k.req(release), "")
	if k.holds(shellE) {
		t.Error("the app holds the socket of the WebSocket it cancelled")
	}
	k.letGo()

	// After "lost" the Shell holds nothing: closing tells it nothing.
	k.act("open", map[string]any{"name": "f", "url": local("f"), "every": 600})
	k.grant(k.req(at(plainPort, 600)), plainAddr)
	ks.peer(t)
	k.until("f open")
	k.event(ui.KeepaliveLost)
	k.until("event lost")
	k.act("close", map[string]any{"name": "f", "args": []any{1000, "done"}})
	k.until("f close 1000 done true")

	// The Shell refusing, a reply without a socket: error and close, and
	// nothing to let go.
	k.act("open", map[string]any{"name": "g", "url": local("g"), "every": 600})
	k.reply(k.req(at(plainPort, 600)), "the network is off for this app")
	k.until("g error the network is off for this app")
	k.until("g close 1006 the network is off for this app false")
	k.act("open", map[string]any{"name": "h", "url": local("h"), "every": 600})
	k.reply(k.req(at(plainPort, 600)), "")
	k.until("h close 1006 net.keepalive: the reply has no socket false")
	k.act("open", map[string]any{"name": "i", "url": local("i"), "every": 600})
	m := k.req(at(plainPort, 600))
	k.sendFiles(ui.Msg{T: ui.MsgRes, Req: m.Req, OK: true, Data: json.RawMessage(`{}`)}, fileWith(t, "x"))
	k.until("i error net.keepalive: the reply has no socket")
	k.until("i close 1006 net.keepalive: the reply has no socket false")
	k.act("open", map[string]any{"name": "j", "url": local("j"), "every": 600})
	k.reply(k.req(at(plainPort, 600)), "the network is off for this app")
	k.until("j close 1006")
	if len(k.reqs) != 0 || len(ks.peers) != 0 {
		t.Errorf("left over: %d requests, %d connections", len(k.reqs), len(ks.peers))
	}

	// Without the keepalive permission it throws at once.
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id": "com.example.test", "name": "Keepalive test", "version": "1.0", "runtime": "js", "main": "main.js", "permissions": ["net"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	k = &keepShell{shell: start(t, t.TempDir(), "en", filepath.Join(dir, "main.js"))}
	k.hello("", nil)
	k.until("ready")
	k.act("open", map[string]any{"name": "k", "url": local("k"), "every": 600})
	k.until(`k throw TypeError: WebSocket: the app's manifest does not ask for the "keepalive" permission`)
}
