package shell

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/push"
)

// keepServer is the selftest's server for apps' own connections: lines
// in, lines out, on the phone's loopback.
type keepServer struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
	got   []string
}

func (s *keepServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
		go func() {
			sc := bufio.NewScanner(conn)
			for sc.Scan() {
				s.mu.Lock()
				s.got = append(s.got, sc.Text())
				s.mu.Unlock()
			}
		}()
	}
}

func (s *keepServer) count(line string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, l := range s.got {
		if l == line {
			n++
		}
	}
	return n
}

func (s *keepServer) close() {
	s.ln.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
}

// keepStep runs the steps for apps' own connections:
//
//	keep-server <host:port>|off   start the line server ({keepalive} is its address) or end it
//	keep-send <text>              send a line to every connection it has
//	keep-got <count> <text>       it got that line at least count times in all
//	keepalive <app-id> own|shared|data <n>   the app's own connection had n such wakes today
//	keepalive-due <app-id>        the app's heartbeat is past its deadline and the slack: the system
//	                              wakes for it now (the push heartbeats of the run come in real time)
func (r *Runner) keepStep(cmd, arg string) error {
	arg = strings.TrimSpace(arg)
	switch cmd {
	case "keep-server":
		if arg == "off" {
			if r.keep != nil {
				r.keep.close()
				r.keep = nil
			}
			return nil
		}
		if r.keep != nil {
			return errors.New("keep-server is running")
		}
		ln, err := net.Listen("tcp", arg)
		if err != nil {
			return err
		}
		r.keep = &keepServer{ln: ln}
		go r.keep.serve()
	case "keep-send":
		if r.keep == nil {
			return errors.New("keep-send before keep-server")
		}
		r.keep.mu.Lock()
		defer r.keep.mu.Unlock()
		if len(r.keep.conns) == 0 {
			return errors.New("keep-send: nothing connected")
		}
		for _, c := range r.keep.conns {
			if _, err := c.Write([]byte(arg + "\n")); err != nil {
				return err
			}
		}
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
		if _, ok := r.waitFor(func(string) bool { return r.keep.count(line) >= n }, 8000); !ok {
			return fmt.Errorf("the server got %q %d times, want %d", line, r.keep.count(line), n)
		}
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
	}
	return nil
}
