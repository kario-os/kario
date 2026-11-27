package shell

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// keep-server <host:port> apart is the line server in a process of its
// own, which outlives a shell-restart as a server out on the network
// would: this program again (KeepServerApart), with its control, a line
// in and a line out, a socket pair on its descriptor 3. It starts from the
// process thread with a parent-death signal, so it dies with phone-shell
// however that ends, while an in-place restart, which execs from that
// thread, keeps it. The restart carries the runner's end of the control
// over to the next Shell (Resume.KeepServer) as it carries an app's
// connection; any other exec closes it, and the helper ends with its
// control.
//
// keep-server <host:port> outside outlives a shell-crash too, which a
// process of the Shell's service does not: systemd ends what is left in
// the service's group before it starts the next Shell. The helper goes
// into a systemd scope of its own, as an app does (scopes.go), without the
// parent-death signal; its control is a socket in the run directory, which
// it listens on (descriptor 3) and each Shell connects to. When a control
// connection ends it waits keepOutsideWait for the next one, then ends.

const (
	keepApartEnv = "PHONE_KEEP_SERVER"
	// keepWaitEnv: outside's helper, and how long it waits for the next
	// control connection.
	keepWaitEnv     = "PHONE_KEEP_SERVER_WAIT"
	keepOutsideWait = time.Minute
)

// KeepServerApart serves as keep-server's helper when this process was
// started as one, and then reports true: the program has nothing else to
// do.
func KeepServerApart() bool {
	addr, ok := os.LookupEnv(keepApartEnv)
	if !ok {
		return false
	}
	ctl := os.NewFile(3, "keep-server control")
	var err error
	if wait, outside := os.LookupEnv(keepWaitEnv); outside {
		err = serveKeepOutside(addr, ctl, wait)
	} else {
		err = serveKeepApart(addr, ctl)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "keep-server:", err)
		os.Exit(1)
	}
	return true
}

func serveKeepApart(addr string, ctl io.ReadWriteCloser) error {
	defer ctl.Close()
	s, err := keepListen(addr, ctl)
	if err != nil {
		return err
	}
	defer s.close()
	_, err = s.control(ctl)
	return err
}

// serveKeepOutside answers one control connection after another on the
// listener f, until one asks it to end or none comes within wait.
func serveKeepOutside(addr string, f *os.File, wait string) error {
	d, err := time.ParseDuration(wait)
	if err != nil {
		return err
	}
	l, err := net.FileListener(f)
	f.Close()
	if err != nil {
		return err
	}
	ln, ok := l.(*net.UnixListener)
	if !ok {
		l.Close()
		return errors.New("the control is not a unix socket")
	}
	defer os.Remove(ln.Addr().String())
	defer ln.Close()
	ctl, err := ln.Accept()
	if err != nil {
		return err
	}
	s, err := keepListen(addr, ctl)
	if err != nil {
		ctl.Close()
		return err
	}
	defer s.close()
	for {
		quit, _ := s.control(ctl)
		ctl.Close()
		if quit {
			return nil
		}
		_ = ln.SetDeadline(time.Now().Add(d))
		if ctl, err = ln.Accept(); err != nil {
			return nil
		}
	}
}

// keepListen starts the line server at addr, or says on the control why
// it cannot.
func keepListen(addr string, ctl io.Writer) (*keepServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(ctl, "error %v\n", err)
		return nil, err
	}
	s := &keepServer{ln: ln}
	go s.serve()
	return s, nil
}

// control says where the server is, then answers the runner on ctl until
// the control ends; quit: the runner asked the helper to end.
func (s *keepServer) control(ctl io.ReadWriter) (bool, error) {
	if _, err := fmt.Fprintf(ctl, "addr %s\n", s.ln.Addr()); err != nil {
		return false, err
	}
	sc := bufio.NewScanner(ctl)
	for sc.Scan() {
		cmd, arg, _ := strings.Cut(sc.Text(), " ")
		reply := "ok"
		switch cmd {
		case "quit":
			return true, nil
		case "send":
			if err := s.send(arg); err != nil {
				reply = "error " + err.Error()
			}
		case "count":
			n, _ := s.count(arg)
			reply = strconv.Itoa(n)
		case "accepted":
			n, open, _ := s.accepted()
			reply = fmt.Sprintf("%d %d", n, open)
		case "reset":
			if err := s.reset(); err != nil {
				reply = "error " + err.Error()
			}
		default:
			reply = "error no such command: " + cmd
		}
		if _, err := fmt.Fprintln(ctl, reply); err != nil {
			return false, err
		}
	}
	return false, nil
}

// keepCtl is the runner's end of the helper's control.
type keepCtl interface {
	io.ReadWriteCloser
	SetReadDeadline(time.Time) error
}

// keepApart is the runner's end of the helper.
type keepApart struct {
	ctl   keepCtl
	rd    *bufio.Reader
	proc  *os.Process
	at    string
	scale int
	// path: outside's control socket; none: apart's socket pair.
	path string
}

// keepCarried is the helper as one Shell leaves it to the next: apart's
// control stays open across the exec of an in-place restart (FD); the
// next Shell connects to outside's again (Path).
type keepCarried struct {
	FD   int    `json:"fd"`
	Pid  int    `json:"pid"`
	Addr string `json:"addr"`
	Path string `json:"path,omitempty"`
}

func startKeepApart(addr string, scale int) (*keepApart, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	ours, theirs := os.NewFile(uintptr(fds[0]), "keep-server"), os.NewFile(uintptr(fds[1]), "keep-server helper")
	defer theirs.Close()
	cmd := exec.Command("/proc/self/exe")
	// kario-system runs the program it is called by.
	cmd.Args = []string{os.Args[0]}
	cmd.Env = append(os.Environ(), keepApartEnv+"="+addr)
	cmd.ExtraFiles = []*os.File{theirs}
	// A file, not a pipe: the goroutine copying a pipe would end at the
	// restart's exec, and the helper with it.
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := StartProcess(cmd); err != nil {
		ours.Close()
		return nil, err
	}
	k := &keepApart{ctl: ours, rd: bufio.NewReader(ours), proc: cmd.Process, scale: scale}
	if err := k.hello(); err != nil {
		_ = k.close()
		return nil, err
	}
	return k, nil
}

// startKeepOutside starts outside's helper with its control at a socket
// in the run directory, and moves it into a scope of its own.
func (r *Runner) startKeepOutside(addr string) (*keepApart, error) {
	g := r.C.cg
	if g == nil || !g.scopes {
		return nil, errors.New("keep-server outside needs app scopes (PHONE_APP_SCOPES=1, as root)")
	}
	return startKeepOutside(addr, filepath.Join(r.C.cfg.RunDir, "keep-server.sock"), r.Scale, func(pid int) error {
		_, err := g.scope(fmt.Sprintf("phone-keep-server-%d.scope", pid), pid,
			dbusProp{"Description", dbus.MakeVariant("the selftest's server for apps' own connections")})
		return err
	})
}

// startKeepOutside: aside moves the helper out of the Shell's group (nil:
// it stays there).
func startKeepOutside(addr, path string, scale int, aside func(pid int) error) (*keepApart, error) {
	_ = os.Remove(path)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	// The helper listens on it from now on and removes it as it ends.
	ln.SetUnlinkOnClose(false)
	f, err := ln.File()
	ln.Close()
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	defer f.Close()
	cmd := exec.Command("/proc/self/exe")
	cmd.Args = []string{os.Args[0]}
	cmd.Env = append(os.Environ(), keepApartEnv+"="+addr, keepWaitEnv+"="+(keepOutsideWait*time.Duration(max(1, scale))).String())
	cmd.ExtraFiles = []*os.File{f}
	cmd.Stderr = os.Stderr
	if err := StartProcess(cmd); err != nil {
		os.Remove(path)
		return nil, err
	}
	k := &keepApart{proc: cmd.Process, scale: scale, path: path}
	if aside != nil {
		if err := aside(cmd.Process.Pid); err != nil {
			_ = cmd.Process.Kill()
			_ = k.close()
			return nil, fmt.Errorf("keep-server helper: %w", err)
		}
	}
	if err := k.dial(); err != nil {
		_ = k.close()
		return nil, err
	}
	return k, nil
}

// dial connects to outside's control.
func (k *keepApart) dial() error {
	conn, err := net.Dial("unix", k.path)
	if err != nil {
		return fmt.Errorf("keep-server helper: %w", err)
	}
	k.ctl, k.rd = conn, bufio.NewReader(conn)
	return k.hello()
}

// hello reads where the helper's server is, the first line on a control.
func (k *keepApart) hello() error {
	hello, err := k.read(10 * time.Second)
	at, ok := strings.CutPrefix(hello, "addr ")
	if err == nil && ok {
		k.at = at
		return nil
	}
	if msg, bad := strings.CutPrefix(hello, "error "); bad {
		return errors.New(msg)
	}
	return fmt.Errorf("keep-server helper: %q, %v", hello, err)
}

func (k *keepApart) read(d time.Duration) (string, error) {
	_ = k.ctl.SetReadDeadline(time.Now().Add(d * time.Duration(max(1, k.scale))))
	line, err := k.rd.ReadString('\n')
	return strings.TrimSuffix(line, "\n"), err
}

func (k *keepApart) ask(cmd string) (string, error) {
	if _, err := k.ctl.Write([]byte(cmd + "\n")); err != nil {
		return "", fmt.Errorf("keep-server helper: %w", err)
	}
	reply, err := k.read(5 * time.Second)
	if err != nil {
		return "", fmt.Errorf("keep-server helper: %w", err)
	}
	if msg, bad := strings.CutPrefix(reply, "error "); bad {
		return "", errors.New(msg)
	}
	return reply, nil
}

func (k *keepApart) addr() string { return k.at }

func (k *keepApart) send(line string) error {
	_, err := k.ask("send " + line)
	return err
}

func (k *keepApart) count(line string) (int, error) {
	reply, err := k.ask("count " + line)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(reply)
}

func (k *keepApart) accepted() (int, int, error) {
	reply, err := k.ask("accepted")
	if err != nil {
		return 0, 0, err
	}
	var n, open int
	if _, err := fmt.Sscanf(reply, "%d %d", &n, &open); err != nil {
		return 0, 0, fmt.Errorf("keep-server helper: %q: %w", reply, err)
	}
	return n, open, nil
}

func (k *keepApart) reset() error {
	_, err := k.ask("reset")
	return err
}

// close ends the helper and waits for it; one that does not end is
// killed.
func (k *keepApart) close() error {
	if k.ctl != nil {
		_, _ = k.ctl.Write([]byte("quit\n"))
		k.ctl.Close()
	}
	if k.path != "" {
		defer os.Remove(k.path)
	}
	done := make(chan struct{})
	go func() {
		// A Shell before a crash started it: not this one's child, and
		// gone once its process is.
		if _, err := k.proc.Wait(); err != nil {
			for processAlive(k.proc.Pid) {
				time.Sleep(20 * time.Millisecond)
			}
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second * time.Duration(max(1, k.scale))):
		_ = k.proc.Kill()
		<-done
		return fmt.Errorf("keep-server helper pid %d did not end: killed", k.proc.Pid)
	}
}

// carry leaves the helper to the next Shell: apart's control stays open
// across the exec of an in-place restart.
func (k *keepApart) carry() (*keepCarried, error) {
	c := &keepCarried{Pid: k.proc.Pid, Addr: k.at, Path: k.path}
	f, ok := k.ctl.(*os.File)
	if k.path != "" || !ok {
		return c, nil
	}
	rc, err := f.SyscallConn()
	if err != nil {
		return nil, err
	}
	if cerr := rc.Control(func(fd uintptr) {
		c.FD = int(fd)
		_, err = unix.FcntlInt(fd, unix.F_SETFD, 0)
	}); cerr != nil || err != nil {
		return nil, errors.Join(cerr, err)
	}
	return c, nil
}

// take is the helper a previous Shell left (LoadResume has made apart's
// control close-on-exec again).
func (c *keepCarried) take(scale int) (*keepApart, error) {
	proc, _ := os.FindProcess(c.Pid)
	k := &keepApart{proc: proc, at: c.Addr, scale: scale, path: c.Path}
	if c.Path != "" {
		if err := k.dial(); err != nil {
			return nil, err
		}
		return k, nil
	}
	f := os.NewFile(uintptr(c.FD), "keep-server")
	k.ctl, k.rd = f, bufio.NewReader(f)
	return k, nil
}

// keepCarry is keep-server's helper for the Shell that goes on after this
// one: an in-place restart's, or (crash) the one systemd starts after a
// crash, which only outside's helper outlives. Nil when there is none, or
// the server is in this process and ends with it.
func (r *Runner) keepCarry(crash bool) (*keepCarried, error) {
	k, ok := r.keep.(*keepApart)
	if !ok || crash && k.path == "" {
		return nil, nil
	}
	return k.carry()
}
