package shell

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kario-os/kario/devctl"
)

// The developer link's tools beside apps (27-usb-typec.md §4.1, adb's
// counterparts): files to and from the phone, injected keys, the system's
// log, port forwarding, restarts. Each request meets devGate first; what a
// tool may touch is decided here, not by phone-dev.

const (
	errDevPath = "only the developer directory (%s) and the data of apps installed in developer mode (APP-ID:PATH) are the computer's to read and write"
	// devInputShown: how long the status bar says keys came from the
	// computer after the last one.
	devInputShown = 3 * time.Second
	devKeyGap     = 40 * time.Millisecond
	devTypeGap    = 10 * time.Millisecond
	// devSpareSpace stays free on the phone after a push or an update.
	devSpareSpace = 32 << 20
	// devSyslogTail: how much of a log file syslog starts with when no
	// number of lines is asked for.
	devSyslogTail = 256 << 10
	devRebootWait = 500 * time.Millisecond
)

// devDir is the developer directory (Android's /data/local/tmp): the
// computer's to push to and pull from.
func (c *Core) devDir() string { return filepath.Join(c.cfg.StateDir, "dev") }

// injecting: keys from the computer came within devInputShown.
func (c *Core) injecting() bool {
	return !c.dev.injected.IsZero() && time.Since(c.dev.injected) < devInputShown
}

// devKeys are key's names as keys, marked injected: the selftest key
// step's names (enter, esc, up, f4, call, power, ...), space, and shift+
// before an arrow, first or last (selecting text).
func devKeys(names []string) ([]Key, error) {
	var out []Key
	for _, n := range names {
		name, shift := strings.CutPrefix(n, "shift+")
		kn, ok := keyNames[name]
		var k Key
		switch {
		case name == "space":
			k = Runes(" ")
		case ok:
			k = Key{Name: kn, Shift: shift}
		default:
			return nil, fmt.Errorf("key: %q is not a key (one of space %s)", n, strings.Join(slices.Sorted(maps.Keys(keyNames)), " "))
		}
		k.Injected = true
		out = append(out, k)
	}
	return out, nil
}

// devInject presses or types what the computer sends, one key at a time
// on the loop as the person's keys come (after a dialog's first moment,
// as the selftest's do), each marked injected; meanwhile the status bar
// says keys come from the computer. A key the lock or a trust dialog
// refuses ends it.
func (c *Core) devInject(req devctl.Request) (string, error) {
	var keys []Key
	gap := devKeyGap
	if req.Op == devctl.OpKey {
		var err error
		if keys, err = devKeys(req.Keys); err != nil {
			return "", err
		}
	} else {
		gap = devTypeGap
		for _, r := range req.Text {
			k := Runes(string(r))
			if r == '\n' {
				k = Named(KeyEnter)
			}
			k.Injected = true
			keys = append(keys, k)
		}
	}
	defer time.AfterFunc(devInputShown, func() { c.Post(c.postFrame) })
	for i, k := range keys {
		var arm time.Duration
		if err := c.onLoop(func() error { arm = c.overlayArmLeft(); return c.devGate(req.Via) }); err != nil {
			return "", err
		}
		time.Sleep(arm)
		err := c.onLoop(func() error {
			if err := c.devGate(req.Via); err != nil {
				return err
			}
			c.dev.injected, c.dev.refused = time.Now(), ""
			c.Press(k)
			if c.dev.refused != "" {
				return fmt.Errorf("key %d of %d refused: %s takes only the keys of the person holding the phone", i+1, len(keys), c.dev.refused)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		time.Sleep(gap)
	}
	c.Post(func() { c.log.Printf("dev link: %s: %d keys injected", req.Op, len(keys)) })
	if req.Op == devctl.OpKey {
		return fmt.Sprintf("pressed %d keys", len(keys)), nil
	}
	return fmt.Sprintf("typed %d characters", len(keys)), nil
}

// devFile is a file a push or pull may touch: name inside dir (an os.Root
// keeps it there), owned by uid on a device (-1: the Shell's own).
type devFile struct {
	dir, name string
	uid       int
}

// devPath is the file a push or pull names: in the developer directory
// (a relative path, or an absolute one inside it), or APP-ID:PATH inside
// the data of an app the computer installed in developer mode, as
// Android's run-as reaches a debuggable app's. Nothing else on the phone,
// whatever the session's user could reach. On the loop.
func (c *Core) devPath(p string) (devFile, error) {
	if id, rel, ok := strings.Cut(p, ":"); ok && devctl.ValidApp(id) {
		a := c.appByID(id)
		if a == nil || !a.M.installed {
			return devFile{}, fmt.Errorf("%s %s", id, errDevNotYours)
		}
		if readInstallRecord(a.M.dir).Store != devStoreName {
			return devFile{}, fmt.Errorf("%s was not installed in developer mode (signed by the development store): its data is the user's", id)
		}
		name, err := devRel(p, rel)
		if err != nil {
			return devFile{}, err
		}
		f := devFile{dir: filepath.Join(c.cfg.StateDir, "apps", id), name: name, uid: -1}
		if err := os.MkdirAll(f.dir, 0o700); err != nil {
			return devFile{}, err
		}
		if c.Sandboxed() {
			uid, err := c.appUID(id)
			if err != nil {
				return devFile{}, err
			}
			if err := c.ownDataDir(f.dir, uid); err != nil {
				return devFile{}, err
			}
			f.uid = int(uid)
		}
		return f, nil
	}
	dir, rel := c.devDir(), p
	if filepath.IsAbs(p) {
		inside, ok := strings.CutPrefix(filepath.Clean(p), dir+"/")
		if !ok {
			return devFile{}, fmt.Errorf(errDevPath, dir)
		}
		rel = inside
	}
	name, err := devRel(p, rel)
	if err != nil {
		return devFile{}, err
	}
	return devFile{dir: dir, name: name, uid: -1}, os.MkdirAll(dir, 0o700)
}

// devRel is a path inside a directory, or why p names none.
func devRel(p, rel string) (string, error) {
	name := filepath.Clean(strings.TrimLeft(rel, "/"))
	if name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("%q names no file inside", p)
	}
	return name, nil
}

// devSpace: size bytes fit on dir's file system with devSpareSpace left.
func devSpace(dir string, size int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return err
	}
	if free := int64(st.Bavail) * st.Bsize; free-size < devSpareSpace {
		return fmt.Errorf("not enough space on the phone: %d MB free, %d MB needed and %d MB kept", free>>20, size>>20, devSpareSpace>>20)
	}
	return nil
}

// devMkdirs makes dir's missing directories inside root, owned by uid.
func devMkdirs(root *os.Root, dir string, uid int) error {
	if dir == "." {
		return nil
	}
	path := ""
	for _, part := range strings.Split(dir, "/") {
		path = filepath.Join(path, part)
		err := root.Mkdir(path, 0o755)
		switch {
		case errors.Is(err, os.ErrExist):
		case err != nil:
			return err
		case uid >= 0:
			if err := root.Chown(path, uid, uid); err != nil {
				return err
			}
		}
	}
	return nil
}

// devPush writes the bytes the computer sends into a file devPath allows,
// through a temporary file renamed into place: a push cut short leaves
// nothing behind.
func (c *Core) devPush(conn net.Conn, r *bufio.Reader, req devctl.Request) (string, error) {
	var f devFile
	err := c.onLoop(func() error {
		if err := c.devGate(req.Via); err != nil {
			return err
		}
		var e error
		f, e = c.devPath(req.Path)
		return e
	})
	if err == nil {
		err = devSpace(f.dir, req.Size)
	}
	if err != nil {
		return "", err
	}
	if err := devctl.WriteReply(conn, devctl.Reply{OK: true}); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(f.dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := devMkdirs(root, filepath.Dir(f.name), f.uid); err != nil {
		return "", err
	}
	tmp := fmt.Sprintf("%s.push-%d", f.name, time.Now().UnixNano())
	w, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	n, err := io.CopyN(w, r, req.Size)
	if err == nil {
		err = w.Sync()
	}
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	mode := os.FileMode(req.Mode & 0o777)
	if mode == 0 {
		mode = 0o644
	}
	if err == nil {
		err = root.Chmod(tmp, mode)
	}
	if err == nil && f.uid >= 0 {
		err = root.Chown(tmp, f.uid, f.uid)
	}
	if err == nil {
		err = root.Rename(tmp, f.name)
	}
	if err != nil {
		_ = root.Remove(tmp)
		return "", fmt.Errorf("%s: %d of %d bytes: %w", req.Path, n, req.Size, err)
	}
	where := filepath.Join(f.dir, f.name)
	c.Post(func() { c.log.Printf("dev link: the computer pushed %s (%d bytes)", where, n) })
	return fmt.Sprintf("pushed %s (%d bytes)", where, n), nil
}

// devPull sends a file devPath allows.
func (c *Core) devPull(conn net.Conn, req devctl.Request) {
	var f devFile
	err := c.onLoop(func() error {
		if err := c.devGate(req.Via); err != nil {
			return err
		}
		var e error
		f, e = c.devPath(req.Path)
		return e
	})
	var file *os.File
	var size int64
	if err == nil {
		file, size, err = openDevFile(f)
	}
	if err != nil {
		c.Post(func() { c.log.Printf("dev link: pull refused: %v", err) })
		_ = devctl.WriteReply(conn, devctl.Reply{Error: err.Error()})
		return
	}
	defer file.Close()
	if devctl.WriteReply(conn, devctl.Reply{OK: true, Size: size}) != nil {
		return
	}
	_, _ = io.CopyN(conn, file, size)
}

func openDevFile(f devFile) (*os.File, int64, error) {
	root, err := os.OpenRoot(f.dir)
	if err != nil {
		return nil, 0, err
	}
	defer root.Close()
	file, err := root.Open(f.name)
	if err != nil {
		return nil, 0, err
	}
	st, err := file.Stat()
	if err == nil && !st.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a file (pull takes files one by one)", filepath.Join(f.dir, f.name))
	}
	if err != nil {
		file.Close()
		return nil, 0, err
	}
	return file, st.Size(), nil
}

// devForwardOpen: the gate, and the cable's firewall (netfw.Link) lets
// SSH through, which every forwarded connection rides; on the loop.
func (c *Core) devForwardOpen(via string) error {
	if err := c.devGate(via); err != nil {
		return err
	}
	if !slices.Contains(c.usb.link.TCP, 22) {
		return errors.New("the cable's firewall does not let SSH (port 22) through")
	}
	return nil
}

// devForward holds a port forwarding the computer set up on its SSH
// session: ssh -L to the port on the phone's loopback, or -R from it.
// Forwarded connections ride port 22 of the cable, which the link's
// firewall opens only in developer mode, and end on the loopback, which the
// firewall's per-app rules let through before any app's (an app without
// the network permission has no loopback of the phone's to reach a reverse
// port on). It lasts while the gate is open: when it closes the request
// ends, and with it the session's command and its forwarding.
func (c *Core) devForward(conn net.Conn, r *bufio.Reader, req devctl.Request) {
	if err := c.onLoop(func() error { return c.devForwardOpen(req.Via) }); err != nil {
		c.Post(func() { c.log.Printf("dev link: forward refused: %v", err) })
		_ = devctl.WriteReply(conn, devctl.Reply{Error: err.Error()})
		return
	}
	text := fmt.Sprintf("forwarding to 127.0.0.1:%d on the phone", req.Port)
	if req.Reverse {
		text = fmt.Sprintf("forwarding 127.0.0.1:%d on the phone to the computer", req.Port)
	}
	c.Post(func() { c.log.Printf("dev link: %s", text) })
	if devctl.WriteReply(conn, devctl.Reply{OK: true, Text: text}) != nil {
		return
	}
	gone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(gone)
	}()
	for {
		select {
		case <-gone:
			c.Post(func() { c.log.Printf("dev link: %s ended", text) })
			return
		case <-time.After(devGateEvery):
			if err := c.onLoop(func() error { return c.devForwardOpen(req.Via) }); err != nil {
				c.Post(func() { c.log.Printf("dev link: %s ended: %v", text, err) })
				fmt.Fprintf(conn, "%s: %v\n", devctl.Program, err)
				return
			}
		}
	}
}

// devReboot restarts the phone a moment after answering, into the target
// the bootloader reads from the kernel's reboot reason (bootloader: ABL's
// fastboot on the OnePlus 6T); with none, into a system update installed
// and waiting, as Settings would. A developer machine is not the Shell's
// to restart.
func (c *Core) devReboot(req devctl.Request) (string, error) {
	var text string
	err := c.onLoop(func() error {
		if err := c.devGate(req.Via); err != nil {
			return err
		}
		reboot := c.dev.reboot
		if reboot == nil {
			if c.cfg.SKU.ID == "" {
				return errors.New("not a phone: the developer machine is not the Shell's to restart")
			}
			reboot = systemdReboot
		}
		c.showToast(tr.T("Restarting: the computer asked"))
		into := req.Target
		switch {
		case into != "":
			text = "restarting into " + into
		case c.update.state == "installed":
			text = "restarting into the system update installed"
			reboot = func(string) error { c.Post(func() { c.restartIntoUpdate("the computer") }); return nil }
		default:
			text = "restarting"
		}
		c.log.Printf("dev link: %s, as the computer asked", text)
		time.AfterFunc(devRebootWait, func() {
			if err := reboot(into); err != nil {
				c.Post(func() { c.log.Printf("dev link: restart: %v", err) })
			}
		})
		return nil
	})
	return text, err
}

// systemdReboot restarts through systemd with the reason for the
// bootloader (LINUX_REBOOT_CMD_RESTART2).
func systemdReboot(target string) error {
	return exec.Command("systemctl", systemdArgs(target)...).Run()
}

func systemdArgs(target string) []string {
	if target == "" {
		return []string{"reboot"}
	}
	return []string{"reboot", "--reboot-argument=" + target}
}

// syslogFile is a log file syslog sends, each line after prefix.
type syslogFile struct {
	path, prefix string
}

// syslogSources: the journal command (nil for none) and the files of a
// syslog request for unit ("" all, "shell" the Shell's own log). The
// device's journal is systemd's, read as root; on the developer machine
// the services phone-shell starts log into the run directory, apps' logs
// (named by their dotted ids) beside them being the apps' (kario device
// logs).
func (c *Core) syslogSources(unit string, lines int, follow bool) ([]string, []syslogFile, error) {
	shellLog := syslogFile{path: filepath.Join(c.cfg.RunDir, "shell.log")}
	if unit == "shell" {
		return nil, []syslogFile{shellLog}, nil
	}
	journal := c.dev.journal
	if journal == "" && c.Sandboxed() {
		journal, _ = exec.LookPath("journalctl")
	}
	if journal != "" {
		args := []string{journal, "--no-pager", "-o", "short-iso", "-b", "-u", cmp.Or(unit, "phone-*")}
		switch {
		case lines > 0:
			args = append(args, "-n", fmt.Sprint(lines))
		case follow:
			args = append(args, "--no-tail")
		}
		if follow {
			args = append(args, "-f")
		}
		var files []syslogFile
		if unit == "" {
			files = []syslogFile{shellLog}
		}
		return args, files, nil
	}
	paths, _ := filepath.Glob(filepath.Join(c.cfg.RunDir, "*.log"))
	sort.Strings(paths)
	want := strings.TrimSuffix(strings.TrimPrefix(unit, "phone-"), ".service")
	var files []syslogFile
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".log")
		if name == "shell" || strings.Contains(name, ".") || unit != "" && name != want {
			continue
		}
		files = append(files, syslogFile{path: p, prefix: name + ": "})
	}
	if unit == "" {
		files = append(files, shellLog)
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("syslog: no log of %q on this phone", unit)
	}
	return nil, files, nil
}

// lineWriter puts prefix before each line and keeps lines from several
// writers whole.
type lineWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
	mid    bool
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.prefix == "" {
		return l.w.Write(p)
	}
	var b strings.Builder
	for _, line := range strings.SplitAfter(string(p), "\n") {
		if line == "" {
			continue
		}
		if !l.mid {
			b.WriteString(l.prefix)
		}
		b.WriteString(line)
		l.mid = !strings.HasSuffix(line, "\n")
	}
	if _, err := io.WriteString(l.w, b.String()); err != nil {
		return 0, err
	}
	return len(p), nil
}

// tailStart is where a log file's last lines begin (lines 0: its last
// devSyslogTail bytes), and whether that is in the middle of a line.
func tailStart(path string, lines int) (int64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, false
	}
	start := max(0, st.Size()-devSyslogTail)
	if lines <= 0 {
		return start, start > 0
	}
	buf := make([]byte, st.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return start, start > 0
	}
	n := 0
	for i := len(buf) - 2; i >= 0; i-- {
		if buf[i] == '\n' {
			if n++; n == lines {
				return start + int64(i) + 1, false
			}
		}
	}
	return start, start > 0
}

// devSyslog sends the system's log, as logcat does: the journal of the
// phone's services (the run directory's service logs on the developer
// machine) and the Shell's own log; followed, what they write next, until
// the computer goes or the gate closes.
func (c *Core) devSyslog(conn net.Conn, r *bufio.Reader, req devctl.Request) {
	var journal []string
	var files []syslogFile
	err := c.onLoop(func() error { return c.devGate(req.Via) })
	if err == nil {
		journal, files, err = c.syslogSources(req.Unit, req.Lines, req.Follow)
	}
	if err != nil {
		_ = devctl.WriteReply(conn, devctl.Reply{Error: err.Error()})
		return
	}
	if devctl.WriteReply(conn, devctl.Reply{OK: true}) != nil {
		return
	}
	mu := &sync.Mutex{}
	if journal != nil {
		cmd := exec.Command(journal[0], journal[1:]...)
		cmd.Stdout = &lineWriter{mu: mu, w: conn}
		cmd.Stderr = cmd.Stdout
		if !req.Follow {
			_ = cmd.Run()
		} else if cmd.Start() == nil {
			gone := make(chan struct{})
			go func() {
				_ = cmd.Wait()
				close(gone)
			}()
			// Shutdown ends it as the computer going away does.
			c.helpers.add(helperProc{done: gone, stop: func() { _ = conn.Close() }, kill: cmd.Process.Kill})
			defer func() {
				_ = cmd.Process.Kill()
				<-gone
			}()
		}
	}
	type tail struct {
		syslogFile
		w     io.Writer
		off   int64
		first bool
	}
	var tails []*tail
	for _, f := range files {
		off, mid := tailStart(f.path, req.Lines)
		tails = append(tails, &tail{syslogFile: f, w: &lineWriter{mu: mu, w: conn, prefix: f.prefix}, off: off, first: mid})
	}
	send := func() error {
		for _, t := range tails {
			n, err := sendLog(t.w, t.path, t.off, t.first)
			if err != nil {
				return err
			}
			if n > 0 {
				t.off, t.first = t.off+n, false
			}
		}
		return nil
	}
	if err := send(); err != nil || !req.Follow {
		return
	}
	gone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(gone)
	}()
	checked := time.Now()
	for {
		select {
		case <-gone:
			return
		case <-time.After(250 * time.Millisecond):
		}
		if send() != nil {
			return
		}
		if time.Since(checked) >= devGateEvery {
			checked = time.Now()
			if err := c.onLoop(func() error { return c.devGate(req.Via) }); err != nil {
				mu.Lock()
				fmt.Fprintf(conn, "\n%s: %v\n", devctl.Program, err)
				mu.Unlock()
				return
			}
		}
	}
}
