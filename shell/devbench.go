package shell

import (
	"archive/zip"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kario-os/kario/devctl"
	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

// The selftest's computer on the developer link (devlink.go), the store
// apps' bytecode (jscache.go) and start times (29-app-development.md §5.1).

// devProgram is phone-dev among the system's programs (prefix/bin).
func (c *Core) devProgram() string {
	return filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(c.cfg.AppBinDir))), "bin", devctl.Program)
}

// devCmd is the steps dev and dev-ssh: <op> [<arg>...] [-> text]. The
// computer runs phone-dev <op>: dev on the phone itself, standing in for
// the SSH session (on a device as one over the cable would:
// SSH_CONNECTION), and dev-ssh through dropbear's own client in the
// emulator's computer, over the cable. install's arg is a sample
// (share/phone/examples), signed with the development store; trust's, the
// development store's root; push's, the file on the phone and then its
// text; type's, the text; update's, signed or unsigned (a bundle the
// simulated RAUC of ota sim takes or refuses). A screenshot or a bug
// report reads as "PNG <w>x<h>" or the zip's file names, and what the
// phone says beside it; a screenshot goes to the snapshot dir as
// <arg>.png. reboot never restarts the machine the selftest runs on. A
// leading ! wants a refusal. The output has to contain text.
func (r *Runner) devCmd(arg string, overSSH bool) error {
	spec, want, _ := strings.Cut(arg, " -> ")
	f := strings.Fields(spec)
	if len(f) == 0 {
		return errors.New("usage: dev [!]<op> [<arg>...] [-> text]")
	}
	op, refused := strings.CutPrefix(f[0], "!")
	args := append([]string{op}, f[1:]...)
	var stdin []byte
	switch op {
	case devctl.OpInstall, devctl.OpTrust:
		if len(f) != 2 {
			return fmt.Errorf("usage: dev %s <arg>", op)
		}
		if op == devctl.OpTrust {
			key, err := r.C.devStoreKey()
			if err != nil {
				return err
			}
			args[1] = key
			break
		}
		raw, err := r.C.signedExample(f[1])
		if err != nil {
			return err
		}
		stdin, args[1] = raw, "-"
	case devctl.OpPush:
		if len(f) < 3 {
			return errors.New("usage: dev push <path> <text>")
		}
		stdin = []byte(strings.ReplaceAll(strings.Join(f[2:], " "), `\n`, "\n"))
		args = []string{op, f[1], strconv.Itoa(len(stdin))}
	case devctl.OpType:
		args = []string{op, strings.Join(f[1:], " ")}
	case devctl.OpUpdate:
		if len(f) != 2 {
			return errors.New("usage: dev update signed|unsigned")
		}
		stdin = bytes.Repeat([]byte{'x'}, 1<<20)
		if f[1] == "signed" {
			stdin = append([]byte(simBundleMagic), make([]byte, 1<<20)...)
		}
		args = []string{op, strconv.Itoa(len(stdin))}
	case devctl.OpReboot:
		r.C.dev.reboot = func(target string) error {
			r.C.Post(func() { r.C.Trace("dev reboot %q: not on the selftest's machine", target) })
			return nil
		}
	case devctl.OpScreenshot:
		if len(f) != 2 {
			return errors.New("usage: dev screenshot <name>")
		}
		args = args[:1]
	}
	var out, notes bytes.Buffer
	var err error
	if overSSH {
		err = r.devSSH(args, stdin, &out, &notes)
	} else {
		err = r.devLocal(args, stdin, &out, &notes)
	}
	if devctl.Binary(op) && err == nil {
		summary, serr := r.devSummary(op, f[1:], out.Bytes())
		if serr != nil {
			return serr
		}
		out.Reset()
		out.WriteString(summary)
	}
	got := strings.TrimSpace(out.String() + "\n" + notes.String())
	r.C.Trace("dev %v: %v: %s", args, err, got)
	switch {
	case refused && err == nil:
		return fmt.Errorf("phone-dev %s was not refused: %s", op, got)
	case !refused && err != nil:
		return fmt.Errorf("phone-dev %s: %v: %s", op, err, got)
	case !strings.Contains(got, strings.TrimSpace(want)):
		return fmt.Errorf("phone-dev %s said %q, not %q", op, got, want)
	}
	return nil
}

// devSummary reads a screenshot or a bug report for the step's text, and
// keeps the screenshot in the snapshot dir; a pulled file reads as itself.
func (r *Runner) devSummary(op string, args []string, raw []byte) (string, error) {
	switch op {
	case devctl.OpScreenshot:
		cfg, err := png.DecodeConfig(bytes.NewReader(raw))
		if err != nil {
			return "", fmt.Errorf("the screenshot is no PNG: %w", err)
		}
		if r.SnapDir != "" {
			_ = os.MkdirAll(r.SnapDir, 0o755)
			if err := os.WriteFile(filepath.Join(r.SnapDir, args[0]+".png"), raw, 0o644); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("PNG %dx%d", cfg.Width, cfg.Height), nil
	case devctl.OpBugreport:
		z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			return "", fmt.Errorf("the bug report is no zip: %w", err)
		}
		var names []string
		for _, f := range z.File {
			names = append(names, f.Name)
		}
		return strings.Join(names, " "), nil
	}
	return string(raw), nil
}

func (r *Runner) devLocal(args []string, stdin []byte, out, notes *bytes.Buffer) error {
	cmd := r.devLocalCmd(args)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), out, notes
	return r.waitCmd(cmd)
}

// devLocalCmd is phone-dev run on the phone itself as in an SSH session
// over the cable.
func (r *Runner) devLocalCmd(args []string) *exec.Cmd {
	cmd := exec.Command(r.C.devProgram(), args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), devctl.RunDirEnv + "=" + r.C.cfg.RunDir}
	if !r.C.usb.sim {
		cmd.Env = append(cmd.Env, devctl.SSHEnv+"=10.71.0.10 50022 "+usbAddr+" 22")
	}
	return cmd
}

// shWord quotes a word for the remote shell an SSH session's command line
// goes to.
func shWord(s string) string {
	if s != "" && strings.Trim(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789@%+=:,./_-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// devRemote is phone-dev's command line in an SSH session on the phone.
func (r *Runner) devRemote(args []string) string {
	words := []string{devctl.RunDirEnv + "=" + shWord(r.C.cfg.RunDir), devctl.Program}
	for _, a := range args {
		words = append(words, shWord(a))
	}
	return strings.Join(words, " ")
}

// devSSH runs phone-dev through dropbear's client (dbclient) in the
// emulator's computer: the development image lets root in without a
// password (allow-empty-password), as a phone on a developer's desk would
// with the developer's key.
func (r *Runner) devSSH(args []string, stdin []byte, out, notes *bytes.Buffer) error {
	dbclient, err := r.dbclient()
	if err != nil {
		return err
	}
	addr, _ := r.C.USBSharing()
	return r.computer(addr, func(_ httpDo, _ dialPort, _ string) error {
		cmd := exec.Command(dbclient, "-T", "-y", "-y", "root@"+usbAddr, r.devRemote(args))
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/tmp", "DROPBEAR_PASSWORD="}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), out, notes
		return cmd.Run()
	})
}

func (r *Runner) dbclient() (string, error) {
	if r.C.usb.sim {
		return "", errors.New("dev-ssh needs the emulator's cable (require-usb-host)")
	}
	p, err := exec.LookPath("dbclient")
	if err != nil {
		return "", fmt.Errorf("no dbclient (dropbear's client) in the image: %w", err)
	}
	return p, nil
}

// devRemove is the step dev-rm <path>: a file the computer pushed into the
// developer directory goes again (the selftest leaves the phone as it
// found it).
func (r *Runner) devRemove(arg string) error {
	f, err := r.C.devPath(strings.TrimSpace(arg))
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(f.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Remove(f.name)
}

// devForwardStep is the step dev-forward <phone-port> <computer-port>
// (the emulator): a TCP echo service on the phone's loopback at phone-port;
// the emulator's computer forwards its own computer-port to it over a real
// SSH session on the cable (dbclient -L, phone-dev forward holding it) and
// gets its bytes back through it. Then developer mode goes off while a
// forwarded connection carries bytes both ways: a new SSH connection is
// refused at once, the open session's bytes still come back, and once the
// computer closes the connection dbclient ends, told why; port 22 then
// closes to the open connections too. It prints PHONE-DEV-FORWARD.
func (r *Runner) devForwardStep(arg string) error {
	f := strings.Fields(arg)
	if len(f) != 2 {
		return errors.New("usage: dev-forward <phone-port> <computer-port>")
	}
	phonePort, err1 := strconv.Atoi(f[0])
	pcPort, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		return errors.New("usage: dev-forward <phone-port> <computer-port>")
	}
	dbclient, err := r.dbclient()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", f[0]))
	if err != nil {
		return err
	}
	defer ln.Close()
	var open atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			open.Add(1)
			go func() {
				defer open.Add(-1)
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	logFrom := r.shellLogEnd()
	saw := func() string {
		phone, pc := cableUnacked()
		return fmt.Sprintf("the echo service holds %d connections; SSH on the cable has %d bytes unacknowledged on the phone, %d on the computer; the Shell: %s",
			open.Load(), phone, pc, r.devLinkLog(logFrom))
	}
	limit := time.Duration(20*max(1, r.Scale)) * time.Second
	addr, _ := r.C.USBSharing()
	var summary string
	err = r.computer(addr, func(_ httpDo, _ dialPort, _ string) error {
		hold, holdW, err := os.Pipe()
		if err != nil {
			return err
		}
		defer holdW.Close()
		var said lockedBuffer
		cmd := exec.Command(dbclient, "-T", "-y", "-y", "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", pcPort, phonePort),
			"root@"+usbAddr, r.devRemote([]string{devctl.OpForward, f[0]}))
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/tmp", "DROPBEAR_PASSWORD="}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = hold, &said, &said
		if err := cmd.Start(); err != nil {
			return err
		}
		hold.Close()
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		until := func(cond func() bool) error {
			for end := time.Now().Add(limit); !cond(); time.Sleep(100 * time.Millisecond) {
				select {
				case err := <-exited:
					return fmt.Errorf("dbclient -L ended: %v", err)
				default:
				}
				if time.Now().After(end) {
					_ = cmd.Process.Kill()
					return errors.New("timed out")
				}
			}
			return nil
		}
		echo := func() error {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", f[1]), 2*time.Second)
			if err != nil {
				return err
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.WriteString(conn, "ping through ssh -L\n"); err != nil {
				return err
			}
			line, err := bufio.NewReader(conn).ReadString('\n')
			if err == nil && line != "ping through ssh -L\n" {
				err = fmt.Errorf("came back as %q", line)
			}
			return err
		}
		// dbclient forwards by itself as soon as it is logged in; the
		// Shell holds the forwarding once its answer is back through the
		// session.
		held := fmt.Sprintf("forwarding to 127.0.0.1:%d on the phone", phonePort)
		if err := until(func() bool { return strings.Contains(said.String(), held) }); err != nil {
			return fmt.Errorf("the Shell did not hold the forwarding: %v: %q (%s)", err, said.String(), saw())
		}
		var last error
		if err := until(func() bool { last = echo(); return last == nil }); err != nil {
			return fmt.Errorf("nothing came back through the forwarding: %v: %v: %q (%s)", err, last, said.String(), saw())
		}
		// A forwarded connection carries bytes both ways, unacknowledged
		// ones on the cable, when developer mode goes off; the computer
		// closes it only after. Had the firewall dropped what comes in on
		// port 22 at once, neither that close nor the session's end would
		// get through, and dbclient would wait for TCP to give up.
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", f[1]), 2*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		var rounds atomic.Int64
		go func() {
			chunk := bytes.Repeat([]byte("kario\n"), (16<<10)/6)
			back := make([]byte, len(chunk))
			for {
				if _, err := conn.Write(chunk); err != nil {
					return
				}
				if _, err := io.ReadFull(conn, back); err != nil {
					return
				}
				rounds.Add(1)
			}
		}()
		if err := until(func() bool { return rounds.Load() >= 3 }); err != nil {
			return fmt.Errorf("the held connection's bytes did not come back: %v: %q (%s)", err, said.String(), saw())
		}
		offAt := make(chan time.Time, 1)
		r.C.Post(func() { _ = r.C.settingsSet(ui.SettingDeveloperMode, 0.0); offAt <- time.Now() })
		off := <-offAt
		before := rounds.Load()
		if c, err := net.DialTimeout("tcp", net.JoinHostPort(usbAddr, "22"), 3*time.Second); err == nil {
			c.Close()
			return fmt.Errorf("a new SSH connection got in with developer mode off (%s)", saw())
		}
		echoed := rounds.Load() - before
		conn.Close()
		select {
		case <-exited:
		case <-time.After(limit):
			_ = cmd.Process.Kill()
			return fmt.Errorf("dbclient went on for %s after developer mode went off, %d echoes after that, the forwarded connection closed: %q (%s)",
				time.Since(off).Round(time.Second), echoed, said.String(), saw())
		}
		ended := time.Since(off)
		switch {
		case !strings.Contains(said.String(), errDevModeOff):
			return fmt.Errorf("the forwarding ended without saying why: %q (%s)", said.String(), saw())
		case echoed == 0:
			return fmt.Errorf("the open session carried nothing more once developer mode went off (%s)", saw())
		}
		draining := true
		for end := time.Now().Add(limit); draining; time.Sleep(100 * time.Millisecond) {
			_ = r.C.onLoop(func() error { draining = len(r.C.usb.link.Drain) > 0; return nil })
			if draining && time.Now().After(end) {
				return fmt.Errorf("port 22 still open to the connections %s after dbclient ended (%s)", limit, saw())
			}
		}
		summary = fmt.Sprintf("PHONE-DEV-FORWARD: dbclient ended %s after developer mode went off (%d echoes after it, a new SSH connection refused); port 22 closed to the open connections %s after\n",
			ended.Round(10*time.Millisecond), echoed, (time.Since(off) - ended).Round(10*time.Millisecond))
		return nil
	})
	if err == nil {
		fmt.Fprint(r.Out, summary)
	}
	return err
}

// lockedBuffer is a command's output, read while it runs.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// cableUnacked is what SSH connections on the cable have sent and not had
// acknowledged: in the process's network namespace, the phone's, and in
// this thread's, the emulator's computer when called there (usbComputer).
func cableUnacked() (phone, computer int) {
	count := func(proc string) int {
		n := 0
		for _, name := range []string{"tcp", "tcp6"} {
			if raw, err := os.ReadFile(filepath.Join(proc, "net", name)); err == nil {
				n += unackedSSH(string(raw))
			}
		}
		return n
	}
	return count("/proc/self"), count("/proc/thread-self")
}

// unackedSSH adds up the send queues of a /proc/net/tcp table's
// established connections to or from port 22.
func unackedSSH(table string) int {
	n := 0
	for _, l := range strings.Split(table, "\n") {
		fs := strings.Fields(l)
		if len(fs) < 5 || fs[3] != "01" || !strings.HasSuffix(fs[1], ":0016") && !strings.HasSuffix(fs[2], ":0016") {
			continue
		}
		tx, _, _ := strings.Cut(fs[4], ":")
		v, _ := strconv.ParseInt(tx, 16, 64)
		n += int(v)
	}
	return n
}

func (r *Runner) shellLog() string { return filepath.Join(r.C.cfg.RunDir, "shell.log") }

// shellLogEnd is where the Shell's log ends now, for devLinkLog.
func (r *Runner) shellLogEnd() int64 {
	st, err := os.Stat(r.shellLog())
	if err != nil {
		return 0
	}
	return st.Size()
}

// devLinkLog is what the Shell logged of the developer link, of
// developer mode and of the cable's firewall since off.
func (r *Runner) devLinkLog(off int64) string {
	raw, err := os.ReadFile(r.shellLog())
	if err != nil || int64(len(raw)) < off {
		return "no log"
	}
	var lines []string
	for _, l := range strings.Split(string(raw[off:]), "\n") {
		if strings.Contains(l, "dev link: ") || strings.Contains(l, "developer mode ") || strings.Contains(l, "usb: ") {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) == 0 {
		return "nothing of the link"
	}
	return strings.Join(lines, " | ")
}

// waitCmd runs a program while the loop goes on (the Shell answers it).
func (r *Runner) waitCmd(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	limit := time.After(time.Duration(30*max(1, r.Scale)) * time.Second)
	for {
		select {
		case err := <-done:
			return err
		case <-limit:
			_ = cmd.Process.Kill()
			return fmt.Errorf("%s did not finish", filepath.Base(cmd.Path))
		default:
			r.pump(10 * time.Millisecond)
		}
	}
}

// signedExample is a sample's package signed by the development store, as
// pkg-build makes it.
func (c *Core) signedExample(name string) ([]byte, error) {
	store, err := pkg.DevStore(filepath.Join(c.cfg.StateDir, "devstore"), filepath.Join(c.cfg.StateDir, "trust"))
	if err != nil {
		return nil, err
	}
	signer, err := pkg.DevDeveloper(filepath.Join(c.cfg.StateDir, "devstore", "developers"), exampleDeveloper, store)
	if err != nil {
		return nil, err
	}
	return pkg.Build(c.exampleDir(name), signer)
}

// devStoreKey is the development store's root, as kario device sends it.
func (c *Core) devStoreKey() (string, error) {
	if _, err := pkg.DevStore(filepath.Join(c.cfg.StateDir, "devstore"), filepath.Join(c.cfg.StateDir, "trust")); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(c.cfg.StateDir, "trust", devStoreName+".pub"))
	return strings.TrimSpace(string(raw)), err
}

// pkgInstall is the step pkg-install <example>: the sample signed by the
// development store and installed through the installer's checks without
// the dialog (developer mode has to be on), as the developer link does.
func (r *Runner) pkgInstall(name string) error {
	raw, err := r.C.signedExample(name)
	if err != nil {
		return err
	}
	_, err = r.C.devInstall(raw)
	return err
}

// pkgUninstall is the step pkg-uninstall <app-id>: removed as the
// developer link does, without the dialog.
func (r *Runner) pkgUninstall(id string) error {
	a := r.C.appByID(id)
	if a == nil || !a.M.installed {
		return fmt.Errorf("%s is not installed", id)
	}
	r.C.removeApp(a)
	return nil
}

// jsCacheCmd is the step jscache <app-id> ready|none|used|unused|tamper|source|cache [ms]:
// wait until the app's bytecode checks out; there is none; the running app
// started from it (or from its source); change a byte of it; the app
// starts from its source (for the bench) or from its bytecode again.
func (r *Runner) jsCacheCmd(arg string) error {
	rest, ms := lastNum(arg, 10000)
	f := strings.Fields(rest)
	if len(f) != 2 {
		return errors.New("usage: jscache <app-id> ready|none|used|unused|tamper|source|cache [ms]")
	}
	a := r.C.appByID(f[0])
	if !cachedApp(a) {
		if f[1] == "none" {
			if _, err := os.Stat(r.C.jsCachePath(f[0])); err == nil {
				return fmt.Errorf("%s is gone, its bytecode is not", f[0])
			}
			return nil
		}
		return fmt.Errorf("%s is not an installed TypeScript app", f[0])
	}
	path := filepath.Join(r.C.jsCachePath(f[0]), jsCacheFile)
	switch f[1] {
	case "ready":
		var err error
		if _, ok := r.waitFor(func(string) bool { _, err = r.C.checkJSCache(a); return err == nil }, ms); !ok {
			return fmt.Errorf("no bytecode for %s: %v", f[0], err)
		}
	case "none":
		if _, err := os.Stat(r.C.jsCachePath(f[0])); err == nil {
			return fmt.Errorf("%s has bytecode", f[0])
		}
	case "used", "unused":
		if used := slices.Contains(appCmdline(a), jsrt.BytecodeFlag); used != (f[1] == "used") || !a.running {
			return fmt.Errorf("%s runs as %q (bytecode wanted: %v)", f[0], appCmdline(a), f[1] == "used")
		}
	case "tamper":
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		raw[len(raw)/2] ^= 0x20
		return os.WriteFile(path, raw, 0o644)
	case "source", "cache":
		if r.C.jsc.source == nil {
			r.C.jsc.source = map[string]bool{}
		}
		r.C.jsc.source[f[0]] = f[1] == "source"
	default:
		return fmt.Errorf("jscache: unknown %q", f[1])
	}
	return nil
}

// appCmdline is the running app's command line (nil when it is not).
func appCmdline(a *App) []string {
	if !a.running || a.proc == nil {
		return nil
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", a.proc.Pid))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
}

// appBench is the step app-bench <app-id> <label> [runs]: the app opened
// afresh runs times, from the Shell's launch to the first page it hands
// over (the same for every runtime); then its memory. One line for the
// log: PHONE-APP-BENCH: <label> <app-id>: start <median> ms (<min>–<max>,
// <runs> runs) Rss <kB> Pss <kB> bytecode yes|no.
func (r *Runner) appBench(arg string) error {
	f := strings.Fields(arg)
	if len(f) < 2 || len(f) > 3 {
		return errors.New("usage: app-bench <app-id> <label> [runs]")
	}
	runs := 5
	if len(f) == 3 {
		runs, _ = strconv.Atoi(f[2])
	}
	a := r.C.appByID(f[0])
	if a == nil || runs < 1 {
		return fmt.Errorf("no app %s", f[0])
	}
	limit := time.Duration(10*max(1, r.Scale)) * time.Second
	var starts []time.Duration
	for i := 0; i < runs; i++ {
		if a.running {
			r.C.kill(a)
			if _, ok := r.waitFor(func(string) bool { return !a.running }, 5000); !ok {
				return fmt.Errorf("%s did not end", f[0])
			}
		}
		r.C.ForgetTombstone(f[0])
		t0 := time.Now()
		r.C.Launch(a, "")
		for !(a.ready && len(a.stack) > 0 && !a.stale) {
			if !a.running || time.Since(t0) > limit {
				return fmt.Errorf("%s showed no page (run %d)", f[0], i+1)
			}
			r.pump(time.Millisecond)
		}
		starts = append(starts, time.Since(t0))
	}
	r.pump(300 * time.Millisecond)
	var rss, pss int
	if a.running && a.proc != nil {
		if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", a.proc.Pid)); err == nil {
			for _, l := range strings.Split(string(raw), "\n") {
				if fs := strings.Fields(l); len(fs) >= 2 {
					switch fs[0] {
					case "Rss:":
						rss, _ = strconv.Atoi(fs[1])
					case "Pss:":
						pss, _ = strconv.Atoi(fs[1])
					}
				}
			}
		}
	}
	bc := "no"
	if slices.Contains(appCmdline(a), jsrt.BytecodeFlag) || a.M.Runtime == jsrt.Runtime && !a.M.installed && hasBeside(a) {
		bc = "yes"
	}
	sorted := slices.Clone(starts)
	slices.Sort(sorted)
	ms := func(d time.Duration) string {
		return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 1, 64)
	}
	fmt.Fprintf(r.Out, "PHONE-APP-BENCH: %s %s: start %s ms (%s–%s, %d runs) Rss %d kB Pss %d kB bytecode %s\n",
		f[1], f[0], ms(sorted[len(sorted)/2]), ms(sorted[0]), ms(sorted[len(sorted)-1]), runs, rss, pss, bc)
	return nil
}

// hasBeside: a system app's module has its bytecode beside it (main.jsc).
func hasBeside(a *App) bool {
	_, err := os.Stat(filepath.Join(a.M.dir, jsrt.Bytecode(jsrt.Main(a.M.Main))))
	return err == nil
}
