package shell

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/vpn"
)

// keepLines is keep-server's server: in the Shell's process (keepServer)
// or in a helper process of its own (keepApart, keepalive_apart.go).
type keepLines interface {
	addr() string
	send(line string) error
	count(line string) (int, error)
	// accepted: the connections accepted in all, and how many of them
	// are still open.
	accepted() (int, int, error)
	// reset ends every connection open with a TCP reset.
	reset() error
	close() error
}

// keepServer is the selftest's server for apps' own connections: lines
// in, lines out (ws: WebSocket text messages, for a TypeScript app's
// keepalive WebSocket; without ws, a connection that opens with an HTTP
// GET), on the phone's loopback or in a network namespace of its own (ns)
// behind a veth pair, where the per-app firewall sees the traffic (it
// leaves loopback alone).
type keepServer struct {
	ln    net.Listener
	host  string // the name it listens on, for addr; "" for an address
	ns    *os.File
	ws    bool
	mu    sync.Mutex
	conns []net.Conn
	// Per connection: it ended; it speaks WebSocket.
	done, wsc []bool
	ended     int
	got       []string
}

// The veth pair to the namespace's server: the phone's end and address,
// the server's.
const keepTestVeth, keepTestPeer = "vethkeep0", "vethkeep1"

var keepTestHost, keepTestAddr = netip.MustParsePrefix("192.168.252.1/24"), netip.MustParsePrefix("192.168.252.2/24")

// listenNetns listens in a network namespace of its own, reached over a
// veth pair.
func listenNetns() (net.Listener, *os.File, error) {
	ns, err := vpn.Netns()
	if err != nil {
		return nil, nil, fmt.Errorf("network namespace: %w", err)
	}
	var ln net.Listener
	steps := []struct {
		what string
		do   func() error
	}{
		{"veth pair", func() error { _ = vpn.DeleteLink(keepTestVeth, nil); return vpn.Veth(keepTestVeth, keepTestPeer, ns) }},
		{"address of " + keepTestVeth, func() error { return vpn.AddAddr(keepTestVeth, keepTestHost, nil) }},
		{keepTestVeth + " up", func() error { return vpn.LinkUp(keepTestVeth, nil) }},
		{"address of " + keepTestPeer, func() error { return vpn.AddAddr(keepTestPeer, keepTestAddr, ns) }},
		{keepTestPeer + " up", func() error { return vpn.LinkUp(keepTestPeer, ns) }},
		{"listener", func() error {
			return vpn.InNetns(ns, func() error {
				var err error
				ln, err = net.Listen("tcp4", netip.AddrPortFrom(keepTestAddr.Addr(), 0).String())
				return err
			})
		}},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			_ = vpn.DeleteLink(keepTestVeth, nil)
			ns.Close()
			return nil, nil, fmt.Errorf("keep-server netns: %s: %w", s.what, err)
		}
	}
	return ln, ns, nil
}

func (s *keepServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		i := len(s.conns)
		s.conns, s.done, s.wsc = append(s.conns, conn), append(s.done, false), append(s.wsc, s.ws)
		s.mu.Unlock()
		go func() {
			br := bufio.NewReader(conn)
			if s.ws || keepOpensGET(br) {
				s.mu.Lock()
				s.wsc[i] = true
				s.mu.Unlock()
				s.serveWS(conn, br)
			} else {
				sc := bufio.NewScanner(br)
				for sc.Scan() {
					s.mu.Lock()
					s.got = append(s.got, sc.Text())
					s.mu.Unlock()
				}
			}
			s.mu.Lock()
			s.ended++
			s.done[i] = true
			s.mu.Unlock()
		}()
	}
}

// keepOpensGET: the connection opens with an HTTP GET (a WebSocket's
// opening handshake) rather than a line.
func keepOpensGET(br *bufio.Reader) bool {
	// A byte at a time: a short first line must not wait for more.
	for n := 1; n <= 4; n++ {
		b, err := br.Peek(n)
		if err != nil || !strings.HasPrefix("GET ", string(b)) {
			return false
		}
	}
	return true
}

func (s *keepServer) addr() string {
	if s.host == "" {
		return s.ln.Addr().String()
	}
	return net.JoinHostPort(s.host, strconv.Itoa(s.ln.Addr().(*net.TCPAddr).Port))
}

func (s *keepServer) send(line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.conns) == s.ended {
		return errors.New("nothing connected")
	}
	for i, c := range s.conns {
		if s.done[i] {
			continue
		}
		var err error
		if s.wsc[i] {
			err = writeWSFrame(bufio.NewWriter(c), 1, []byte(line))
		} else {
			_, err = c.Write([]byte(line + "\n"))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// serveWS answers the opening handshake and takes text messages as lines;
// a ping and a close are answered.
func (s *keepServer) serveWS(conn net.Conn, br *bufio.Reader) {
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	key := req.Header.Get("Sec-WebSocket-Key")
	if !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") || key == "" {
		_, _ = io.WriteString(conn, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n")
		return
	}
	if _, err := io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+wsAccept(key)+"\r\n\r\n"); err != nil {
		return
	}
	for {
		op, p, err := readWSFrame(br)
		if err != nil {
			return
		}
		s.mu.Lock()
		switch op {
		case 1:
			s.got = append(s.got, string(p))
		case 8:
			err = writeWSFrame(bufio.NewWriter(conn), 8, p)
		case 9:
			err = writeWSFrame(bufio.NewWriter(conn), 10, p)
		}
		s.mu.Unlock()
		if op == 8 || err != nil {
			return
		}
	}
}

func (s *keepServer) count(line string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, l := range s.got {
		if l == line {
			n++
		}
	}
	return n, nil
}

func (s *keepServer) accepted() (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns), len(s.conns) - s.ended, nil
}

// reset: no linger on close, so the kernel sends a reset instead of a FIN;
// the app reads an error, and the socket, its copies too, hangs up.
func (s *keepServer) reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for i, c := range s.conns {
		if s.done[i] {
			continue
		}
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
		c.Close()
		n++
	}
	if n == 0 {
		return errors.New("nothing connected")
	}
	return nil
}

func (s *keepServer) close() error {
	s.ln.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
	if s.ns == nil {
		return nil
	}
	err := vpn.DeleteLink(keepTestVeth, nil)
	return errors.Join(err, s.ns.Close())
}

// keepStep runs the steps for apps' own connections:
//
//	keep-server <host:port> [apart|outside|ws]|netns [ws]|off   start the line server ({keepalive} is its address: a host
//	                              name stays the name, with the port it took), apart: in a process of its own that a
//	                              shell-restart keeps (keepalive_apart.go), outside: that process in a systemd scope of
//	                              its own, which a shell-crash keeps too (app scopes), netns: in a network namespace of
//	                              its own behind a veth pair (root), ws: speaking WebSocket, its text messages the lines
//	                              (without ws, a connection opening with an HTTP GET speaks it); or end it
//	keep-send <text>              send a line (a text message) to every connection it has open
//	keep-got <count> <text>       it got that line at least count times in all
//	keep-conns <n>                it accepted n connections in all, and every one is still open (no reconnect)
//	keep-reset                    it ends every connection it has open with a TCP reset (no FIN): the socket hangs up
//	keepalive <app-id> own|shared|data <n>   the app's own connection had n such wakes today
//	keepalive-due <app-id>        the app's heartbeat is past its deadline and the slack: the system
//	                              wakes for it now (the push heartbeats of the run come in real time)
//	keepalive-net <app-id> <down> <up>   the system's copy of the app's connection is a socket of the app's uid,
//	                              the app's process holds the same socket, and data usage counts at least down
//	                              and up bytes to the app today (the per-app firewall's counters); prints
//	                              PHONE-KEEPALIVE-NET
func (r *Runner) keepStep(cmd, arg string) error {
	arg = strings.TrimSpace(arg)
	switch cmd {
	case "keep-server":
		if arg == "off" {
			var err error
			if r.keep != nil {
				err = r.keep.close()
				r.keep = nil
			}
			return err
		}
		if r.keep != nil {
			return errors.New("keep-server is running")
		}
		if addr, apart := strings.CutSuffix(arg, " apart"); apart {
			k, err := startKeepApart(strings.TrimSpace(addr), r.Scale)
			if err != nil {
				return err
			}
			r.keep = k
			return nil
		}
		if addr, outside := strings.CutSuffix(arg, " outside"); outside {
			k, err := r.startKeepOutside(strings.TrimSpace(addr))
			if err != nil {
				return err
			}
			r.keep = k
			return nil
		}
		where, mode, _ := strings.Cut(arg, " ")
		if mode = strings.TrimSpace(mode); mode != "" && mode != "ws" {
			return errors.New("usage: keep-server <host:port> [apart|outside|ws]|netns [ws]|off")
		}
		var ns *os.File
		var ln net.Listener
		var err error
		if where == "netns" {
			ln, ns, err = listenNetns()
		} else {
			ln, err = net.Listen("tcp", where)
		}
		if err != nil {
			return err
		}
		s := &keepServer{ln: ln, ns: ns, ws: mode == "ws"}
		if h, _, err := net.SplitHostPort(where); err == nil && h != "" && net.ParseIP(h) == nil {
			s.host = h
		}
		go s.serve()
		r.keep = s
	case "keep-send":
		if r.keep == nil {
			return errors.New("keep-send before keep-server")
		}
		return r.keep.send(arg)
	case "keep-got":
		if r.keep == nil {
			return errors.New("keep-got before keep-server")
		}
		count, line, _ := strings.Cut(arg, " ")
		n, err := strconv.Atoi(count)
		if err != nil || strings.TrimSpace(line) == "" {
			return errors.New("usage: keep-got <count> <text>")
		}
		line = strings.TrimSpace(line)
		got := 0
		r.waitFor(func(string) bool { got, err = r.keep.count(line); return err != nil || got >= n }, 8000)
		if err != nil {
			return err
		}
		if got < n {
			return fmt.Errorf("the server got %q %d times, want %d", line, got, n)
		}
	case "keep-conns":
		if r.keep == nil {
			return errors.New("keep-conns before keep-server")
		}
		want, err := strconv.Atoi(arg)
		if err != nil {
			return errors.New("usage: keep-conns <n>")
		}
		n, open, err := r.keep.accepted()
		if err != nil {
			return err
		}
		if n != want || open != want {
			return fmt.Errorf("the server accepted %d connections, %d of them still open; want %d, all open", n, open, want)
		}
	case "keep-reset":
		if r.keep == nil {
			return errors.New("keep-reset before keep-server")
		}
		return r.keep.reset()
	case "keepalive":
		f := strings.Fields(arg)
		if len(f) != 3 {
			return errors.New("usage: keepalive <app-id> own|shared|data <n>")
		}
		want, _ := strconv.Atoi(f[2])
		got := -1
		if _, ok := r.waitFor(func(string) bool {
			if u := r.C.KeepaliveUse(f[0]); u != nil {
				got = map[string]int{keepOwn: u.Own, keepShared: u.Shared, keepData: u.Data}[f[1]]
			}
			return got == want
		}, 4000); !ok {
			return fmt.Errorf("%s: %d %s wakes, want %d", f[0], got, f[1], want)
		}
	case "keepalive-due":
		k := r.C.keep.conns[arg]
		if k == nil {
			return fmt.Errorf("%s has no connection of its own", arg)
		}
		k.beat.Last = r.C.now().Add(-k.beat.Every - push.KeepaliveSlack - time.Second)
		r.C.keepaliveTick()
	case "keepalive-net":
		return r.keepaliveNet(arg)
	}
	return nil
}

func (r *Runner) keepaliveNet(arg string) error {
	c, f := r.C, strings.Fields(arg)
	if len(f) != 3 {
		return errors.New("usage: keepalive-net <app-id> <down> <up>")
	}
	down, err1 := strconv.ParseUint(f[1], 10, 64)
	up, err2 := strconv.ParseUint(f[2], 10, 64)
	if err1 != nil || err2 != nil {
		return errors.New("keepalive-net: the bytes are whole numbers")
	}
	id := f[0]
	k := c.keep.conns[id]
	if k == nil {
		return fmt.Errorf("%s has no connection of its own", id)
	}
	uid, err := c.appUID(id)
	if err != nil {
		return err
	}
	var st unix.Stat_t
	rc, err := k.conn.SyscallConn()
	if err != nil {
		return err
	}
	if cerr := rc.Control(func(fd uintptr) { err = unix.Fstat(int(fd), &st) }); cerr != nil || err != nil {
		return errors.Join(cerr, err)
	}
	if st.Uid != uid {
		return fmt.Errorf("%s's connection is a socket of uid %d, not of the app's %d", id, st.Uid, uid)
	}
	pid := c.AppPid(id)
	if !holdsSocket(pid, st.Ino) {
		return fmt.Errorf("%s (pid %d) does not hold socket %d, the system's copy of its connection", id, pid, st.Ino)
	}
	var rx, tx uint64
	if _, ok := r.waitFor(func(string) bool {
		c.countData()
		if d := c.dataLedger().Days[c.now().Format(time.DateOnly)][id]; d != nil {
			rx, tx = d.WifiRx+d.MobileRx+d.RoamRx, d.WifiTx+d.MobileTx+d.RoamTx
		}
		return rx >= down && tx >= up
	}, 4000); !ok {
		return fmt.Errorf("data usage counts ↓%d B ↑%d B to %s today, want at least ↓%d B ↑%d B", rx, tx, id, down, up)
	}
	fmt.Fprintf(r.Out, "PHONE-KEEPALIVE-NET: %s: socket %d of uid %d, held by pid %d; counted today ↓%d B ↑%d B\n", id, st.Ino, uid, pid, rx, tx)
	return nil
}

// holdsSocket: the process has the socket of that inode open.
func holdsSocket(pid int, ino uint64) bool {
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	ents, _ := os.ReadDir(dir)
	want := "socket:[" + strconv.FormatUint(ino, 10) + "]"
	for _, e := range ents {
		if l, err := os.Readlink(dir + "/" + e.Name()); err == nil && l == want {
			return true
		}
	}
	return false
}
