package shell

import (
	"bufio"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kario-os/kario/devctl"
	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
)

// The developer link (package devctl, 29-app-development.md §2): phone-dev,
// run in an SSH session over the trusted cable, reaches the Shell through
// RunDir/dev.sock. Every request is refused unless developer mode is on,
// the computer on the cable is trusted for network sharing and, on a
// device, the session came in over the cable; the socket answers the
// Shell's own user only (root on the device, which an app never is).
// Installing goes through the installer's checks and unpack, uninstalling
// through removeApp, reloading as kario run -watch reloads; only installed
// apps are the computer's to touch, never the system's. The tools beside
// apps (files, injected keys, the system's log, port forwarding, restarts:
// devtools.go; screenshots: devshot.go; system updates: devupdate.go; bug
// reports: devreport.go) meet the same gate.

const (
	errDevModeOff  = "developer mode is off on the phone (Settings › Developer mode)"
	errDevNoCable  = "no trusted computer on the cable: plug it in and trust it on the phone (network sharing, not file transfer)"
	errDevNotCable = "the request did not come over the trusted cable (SSH to " + devctl.USBAddr + ")"
	errDevNotYours = "is not an installed app (system apps are not the computer's to change)"
	// devLogTail: how much of an app's log a logs request starts with.
	devLogTail = 64 << 10
	// devGateEvery: how often a followed log checks the gate again.
	devGateEvery   = time.Second
	devRestartWait = 5 * time.Second
)

type devLink struct {
	ln net.Listener
	// injected: when the computer last injected a key (the status bar says
	// so for devInputShown); refused: what turned the last one away.
	injected time.Time
	refused  string
	// shooting: a screenshot is being drawn (one at a time).
	shooting bool
	// reboot and journal stand in for systemd's (tests, the selftest):
	// nil and "" for the real ones.
	reboot  func(target string) error
	journal string
}

func (c *Core) startDevLink() {
	path := devctl.SockPath(c.cfg.RunDir)
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		c.log.Printf("dev link: %v", err)
		return
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		c.log.Printf("dev link: %v", err)
		return
	}
	c.dev.ln = ln
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go c.devConn(conn)
		}
	}()
}

// devGate is the rule every request meets, on the loop.
func (c *Core) devGate(via string) error {
	switch {
	case !c.settings.DeveloperMode:
		return errors.New(errDevModeOff)
	case !c.usb.trusted || c.usb.mtp != nil:
		return errors.New(errDevNoCable)
	case !c.usb.sim && devctl.ViaAddr(via) != usbAddr:
		return errors.New(errDevNotCable)
	}
	return nil
}

// onLoop runs f on the event loop and waits for it.
func (c *Core) onLoop(f func() error) error {
	done := make(chan error, 1)
	c.Post(func() { done <- f() })
	return <-done
}

func peerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) { cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return 0, err
	}
	if cerr != nil {
		return 0, cerr
	}
	return cred.Uid, nil
}

func (c *Core) devConn(conn net.Conn) {
	defer conn.Close()
	refuse := func(err error) { _ = devctl.WriteReply(conn, devctl.Reply{Error: err.Error()}) }
	if uid, err := peerUID(conn); err != nil || uid != uint32(os.Geteuid()) {
		refuse(fmt.Errorf("only the Shell's own user may use %s", devctl.Sock))
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Minute))
	r := bufio.NewReader(conn)
	req, err := devctl.ReadRequest(r)
	if err != nil {
		refuse(err)
		return
	}
	if req.Op == devctl.OpInstall && req.Size > pkg.MaxSize {
		refuse(fmt.Errorf("a package is at most %d MB", pkg.MaxSize>>20))
		return
	}
	if err := c.onLoop(func() error { return c.devGate(req.Via) }); err != nil {
		c.Post(func() { c.log.Printf("dev link: %s refused: %v", req.Op, err) })
		refuse(err)
		return
	}
	var text string
	switch req.Op {
	case devctl.OpInstall:
		if devctl.WriteReply(conn, devctl.Reply{OK: true}) != nil {
			return
		}
		raw := make([]byte, req.Size)
		if _, err = io.ReadFull(r, raw); err == nil {
			err = c.onLoop(func() error {
				var e error
				if e = c.devGate(req.Via); e == nil {
					text, e = c.devInstall(raw)
				}
				return e
			})
		}
	case devctl.OpTrust:
		err = c.onLoop(func() error {
			var e error
			if e = c.devGate(req.Via); e == nil {
				text, e = c.devTrust(req.Key)
			}
			return e
		})
	case devctl.OpLogs:
		_ = conn.SetReadDeadline(time.Time{})
		c.devLogs(conn, r, req)
		return
	case devctl.OpDebug:
		_ = conn.SetReadDeadline(time.Time{})
		c.devDebug(conn, r, req)
		return
	case devctl.OpRestart:
		text, err = c.devRestart(req)
	case devctl.OpPush:
		_ = conn.SetReadDeadline(time.Time{})
		text, err = c.devPush(conn, r, req)
	case devctl.OpPull:
		c.devPull(conn, req)
		return
	case devctl.OpScreenshot:
		c.devScreenshot(conn, req)
		return
	case devctl.OpKey, devctl.OpType:
		text, err = c.devInject(req)
	case devctl.OpSyslog:
		_ = conn.SetReadDeadline(time.Time{})
		c.devSyslog(conn, r, req)
		return
	case devctl.OpForward:
		_ = conn.SetReadDeadline(time.Time{})
		c.devForward(conn, r, req)
		return
	case devctl.OpReboot:
		text, err = c.devReboot(req)
	case devctl.OpUpdate:
		_ = conn.SetReadDeadline(time.Time{})
		c.devUpdate(conn, r, req)
		return
	case devctl.OpBugreport:
		c.devBugreport(conn, req)
		return
	default:
		err = c.onLoop(func() error {
			a, e := c.devApp(req)
			if e != nil {
				return e
			}
			if req.Op == devctl.OpUninstall {
				c.removeApp(a)
				c.log.Printf("dev link: uninstalled %s", a.M.ID)
				c.showToast(tr.T("Uninstalled “%s”", a.M.Name))
				text = "uninstalled " + a.M.ID
				return nil
			}
			c.log.Printf("dev link: reloading %s", a.M.ID)
			c.reloadApp(a)
			text = "reloading " + a.M.ID
			return nil
		})
	}
	if err != nil {
		c.Post(func() { c.log.Printf("dev link: %s: %v", req.Op, err) })
		refuse(err)
		return
	}
	_ = devctl.WriteReply(conn, devctl.Reply{OK: true, Text: text})
}

// devApp is the installed app a request names, its gate checked; on the loop.
func (c *Core) devApp(req devctl.Request) (*App, error) {
	if err := c.devGate(req.Via); err != nil {
		return nil, err
	}
	a := c.appByID(req.App)
	if a == nil || !a.M.installed {
		return nil, fmt.Errorf("%s %s", req.App, errDevNotYours)
	}
	return a, nil
}

// devInstall installs a package the computer sent, through the installer's
// checks, without the dialog: the user turned developer mode on and
// trusted this computer.
func (c *Core) devInstall(raw []byte) (string, error) {
	p, err := pkg.OpenFor(raw, c.storeRoots(), pkg.HostArch())
	if errors.Is(err, pkg.ErrUntrustedStore) {
		return "", fmt.Errorf("%w: the phone does not trust the package's store (kario device install -devstore sends a development store's root first)", err)
	}
	if err != nil {
		return "", err
	}
	m, err := c.checkPackage(p)
	if err != nil {
		return "", err
	}
	if err := c.unpack(p, m, c.appByID(m.ID)); err != nil {
		return "", err
	}
	c.log.Printf("dev link: installed %s %s from the computer (developer %s, store %s)", m.ID, p.Version, p.Developer.Name, p.Developer.Store)
	c.showToast(tr.T("Installed “%s” from the computer", m.Name))
	return fmt.Sprintf("installed %s %s (developer %s, store %s)", m.ID, p.Version, p.Developer.Name, p.Developer.Store), nil
}

// devTrust makes the computer's development store the one developer mode
// trusts (StateDir/trust/dev.pub; there is one development store).
func (c *Core) devTrust(key string) (string, error) {
	pub, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", errors.New("trust: not a base64 Ed25519 public key")
	}
	dir := filepath.Join(c.cfg.StateDir, "trust")
	path := filepath.Join(dir, devStoreName+".pub")
	line := base64.StdEncoding.EncodeToString(pub) + "\n"
	if old, err := os.ReadFile(path); err == nil && string(old) == line {
		return "the development store is trusted already", nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(line), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	// Apps the previous development store signed are checked again.
	for _, a := range c.apps {
		a.verified = false
	}
	c.log.Printf("dev link: the development store is now the computer's (%s…)", key[:min(len(key), 12)])
	c.showToast(tr.T("Trusting the computer's development store (developer mode)"))
	return "trusting the development store", nil
}

// devStoreName is the development store's name (pkg.DevStore).
const devStoreName = "dev"

// devRestart ends an installed app and opens it afresh.
func (c *Core) devRestart(req devctl.Request) (string, error) {
	var id string
	err := c.onLoop(func() error {
		a, err := c.devApp(req)
		if err != nil {
			return err
		}
		id = a.M.ID
		c.log.Printf("dev link: restarting %s", id)
		c.endApp(a, endGrace)
		return nil
	})
	if err != nil {
		return "", err
	}
	for end := time.Now().Add(devRestartWait); ; time.Sleep(20 * time.Millisecond) {
		stopped := false
		err := c.onLoop(func() error {
			a, err := c.devApp(req)
			if err != nil {
				return err
			}
			if stopped = !a.running; stopped {
				c.ForgetTombstone(id)
				c.Launch(a, "")
			}
			return nil
		})
		switch {
		case err != nil:
			return "", err
		case stopped:
			return "restarted " + id, nil
		case time.Now().After(end):
			return "", fmt.Errorf("%s did not end within %s", id, devRestartWait)
		}
	}
}

// debuggable: only a TypeScript app the development store signed, that
// is one the computer installed in developer mode, gets a debugger; never
// a store app or a system app.
func debuggable(a *App) error {
	if a.M.Runtime != jsrt.Runtime {
		return fmt.Errorf("%s is not a TypeScript app (only kario-jsrt's apps have the debugger)", a.M.ID)
	}
	if readInstallRecord(a.M.dir).Store != devStoreName {
		return fmt.Errorf("%s was not installed in developer mode (signed by the development store): only those are debugged", a.M.ID)
	}
	return nil
}

// attachable: the app may have a debugger attach while it runs (its
// process gets attachPort's other end and code with every statement's
// position).
func attachable(a *App) bool {
	return a.M.installed && a.M.Runtime == jsrt.Runtime && debuggable(a) == nil
}

// debugFiles gives an app's new process its debugger sockets: the one a
// debugger restarting it waits on (debugFile), and, for an app the
// computer may debug, the socket an attaching debugger's comes through
// later (attachPort, which the Shell only writes). It returns the child's
// ends, to close once the process started.
func (c *Core) debugFiles(a *App, cmd *exec.Cmd) []*os.File {
	var child []*os.File
	pass := func(env string, f *os.File) {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", env, 3+len(cmd.ExtraFiles)))
		cmd.ExtraFiles = append(cmd.ExtraFiles, f)
		child = append(child, f)
	}
	if a.debugFile != nil {
		pass(jsrt.DebugFDEnv, a.debugFile)
		a.debugFile = nil
	}
	if a.attachPort != nil {
		a.attachPort.Close()
		a.attachPort = nil
	}
	if attachable(a) {
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
		if err != nil {
			c.log.Printf("%s: no socket for a debugger to attach: %v", a.M.ID, err)
			return child
		}
		_ = syscall.Shutdown(fds[0], syscall.SHUT_RD)
		a.attachPort = os.NewFile(uintptr(fds[0]), "debug-attach")
		pass(jsrt.DebugAttachFDEnv, os.NewFile(uintptr(fds[1]), "debug-attach-child"))
	}
	return child
}

// debugPair is a debugger socket: ours, and the app's end. Ours is
// non-blocking, so closing it while it is read reaches the app at once
// (the app then drops its breakpoints and runs on).
func debugPair() (*os.File, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := syscall.SetNonblock(fds[0], true); err != nil {
		syscall.Close(fds[0])
		syscall.Close(fds[1])
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "debug-parent"), os.NewFile(uintptr(fds[1]), "debug-child"), nil
}

// attach hands the running app one end of a new debugger socket (the
// other is ours); false when it cannot take one. A frozen app is thawed to
// take it, its time in the background kept: it freezes again once the
// debugger goes.
func (c *Core) attach(a *App) (*os.File, bool) {
	if !a.running || a.attachPort == nil {
		return nil, false
	}
	ours, theirs, err := debugPair()
	if err != nil {
		return nil, false
	}
	defer theirs.Close()
	if err := unix.Sendmsg(int(a.attachPort.Fd()), []byte{0}, unix.UnixRights(int(theirs.Fd())), nil, unix.MSG_DONTWAIT); err != nil {
		c.log.Printf("dev link: %s takes no debugger while it runs (%v); restarting it under one", a.M.ID, err)
		ours.Close()
		return nil, false
	}
	if a.frozen && a.proc != nil {
		_ = c.freezeApp(a, false)
		c.log.Printf("thawed %s for its debugger", a.M.ID)
	}
	a.frozen = false
	return ours, true
}

// devDebug joins an installed app's debugger socket to the connection
// until either side goes, the app ends or the gate closes: attaching, the
// running app gets the socket (without a restart) when it can take one;
// otherwise the app restarts under the debugger, on a socket the new
// process gets.
func (c *Core) devDebug(conn net.Conn, r *bufio.Reader, req devctl.Request) {
	var ours *os.File
	var text string
	attached := false
	err := c.onLoop(func() error {
		a, err := c.devApp(req)
		if err != nil {
			return err
		}
		if err := debuggable(a); err != nil {
			return err
		}
		if a.debugging {
			return fmt.Errorf("%s is being debugged already", a.M.ID)
		}
		main := jsrt.Main(a.M.Main)
		sum, _ := signedSum(a.M.dir, main)
		if req.Attach {
			ours, attached = c.attach(a)
		}
		if attached {
			text = fmt.Sprintf("attached to %s %s (%s sha256 %s)", a.M.ID, a.M.Version, main, sum)
			c.log.Printf("dev link: a debugger attached to %s", a.M.ID)
		} else {
			if ours, a.debugFile, err = debugPair(); err != nil {
				return err
			}
			text = fmt.Sprintf("debugging %s %s (%s sha256 %s)", a.M.ID, a.M.Version, main, sum)
			c.log.Printf("dev link: debugging %s", a.M.ID)
		}
		a.debugging = true
		return nil
	})
	if err == nil && !attached {
		_, err = c.devRestart(req)
	}
	end := func() {
		c.Post(func() {
			if a := c.appByID(req.App); a != nil {
				a.debugging = false
				if a.debugFile != nil {
					a.debugFile.Close()
					a.debugFile = nil
				}
			}
		})
	}
	if err != nil {
		if ours != nil {
			ours.Close()
			end()
		}
		c.Post(func() { c.log.Printf("dev link: debug refused: %v", err) })
		_ = devctl.WriteReply(conn, devctl.Reply{Error: err.Error()})
		return
	}
	defer end()
	defer ours.Close()
	if devctl.WriteReply(conn, devctl.Reply{OK: true, Text: text}) != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(ours, r)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, ours)
		done <- struct{}{}
	}()
	for {
		select {
		case <-done:
			c.Post(func() { c.log.Printf("dev link: debugging %s ended", req.App) })
			return
		case <-time.After(devGateEvery):
			if err := c.onLoop(func() error { _, err := c.devApp(req); return err }); err != nil {
				c.Post(func() { c.log.Printf("dev link: debugging %s ended: %v", req.App, err) })
				return
			}
		}
	}
}

// devLogs sends the end of an installed app's log (its output, RunDir/<id>.log)
// and, followed, what it writes next, until the computer goes or the gate
// closes.
func (c *Core) devLogs(conn net.Conn, r *bufio.Reader, req devctl.Request) {
	var path string
	err := c.onLoop(func() error {
		a, err := c.devApp(req)
		if err == nil {
			path = filepath.Join(c.cfg.RunDir, a.M.ID+".log")
		}
		return err
	})
	if err != nil {
		_ = devctl.WriteReply(conn, devctl.Reply{Error: err.Error()})
		return
	}
	if devctl.WriteReply(conn, devctl.Reply{OK: true}) != nil {
		return
	}
	var off int64
	if st, err := os.Stat(path); err == nil && st.Size() > devLogTail {
		off = st.Size() - devLogTail
	}
	gone := make(chan struct{})
	if req.Follow {
		go func() {
			_, _ = io.Copy(io.Discard, r)
			close(gone)
		}()
	}
	first, checked := true, time.Now()
	for {
		n, err := sendLog(conn, path, off, first)
		if err != nil {
			return
		}
		off, first = off+n, false
		if !req.Follow {
			return
		}
		select {
		case <-gone:
			return
		case <-time.After(250 * time.Millisecond):
		}
		if time.Since(checked) >= devGateEvery {
			checked = time.Now()
			if err := c.onLoop(func() error { _, err := c.devApp(req); return err }); err != nil {
				fmt.Fprintf(conn, "\n%s: %v\n", devctl.Program, err)
				return
			}
		}
	}
}

// sendLog copies the log from off on; the first time from the start of a
// line.
func sendLog(w io.Writer, path string, off int64, first bool) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() <= off {
		return 0, nil
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	var skipped int64
	if first && off > 0 {
		head := bufio.NewReader(f)
		line, _ := head.ReadString('\n')
		skipped = int64(len(line))
		n, err := io.Copy(w, head)
		return skipped + n, err
	}
	return io.Copy(w, f)
}

// DevLinkPath is where phone-dev reaches this Shell (selftest).
func (c *Core) DevLinkPath() string { return devctl.SockPath(c.cfg.RunDir) }
