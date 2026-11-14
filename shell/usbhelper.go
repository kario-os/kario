package shell

import (
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kario-os/kario/sandbox"
	"github.com/kario-os/kario/usbshare"
)

// USB file sharing's server runs in phone-usbshared, a process of its own
// (27-usb-typec.md §3.2), the way Android runs MTP in the MediaProvider
// process and not in system_server: a parsing bug reachable from any
// computer plugged in must not reach the Shell, which holds everything.
// The Shell keeps what needs a privilege or the library: the listening
// socket on port 80 (made in shareUSB, kept while the helper restarts)
// and the stores, lent through usbshare.Helper on a socket pair. The
// helper starts through phone-sandbox with the helper policy: its own uid
// and fresh namespaces with an empty network one (as root), Landlock
// allowing it nothing but its program, seccomp, then its own
// sandbox.DescriptorsOnly; and in a cgroup of its own with a memory limit
// (usbHelperLimits). It ends with sharing, with the Shell (parent
// death signal, and its end of the pair closes), and is started again if
// it crashes while sharing is on, at most usbHelperRestarts times.

const (
	usbHelperProgram = "phone-usbshared"
	// usbHelperID is the helper's key in uids.json: no app id has a slash.
	usbHelperID       = "system/usbshared"
	usbHelperRestarts = 3
	// usbHelperGroup is its cgroup, beside the apps' ("system." is no
	// app's id). Without delegated cgroups (a developer machine) it stays
	// in the shell's group, as apps do.
	usbHelperGroup = "system.usbshared"
)

// usbHelperLimits: what a computer can make the helper take, Go's soft
// limit (GOMEMLIMIT) a quarter below the hard one. Files stream through
// pipes; the helper itself needs a few MB.
var (
	usbHelperLimits   = limits{mem: 64 << 20, pids: 64}
	usbHelperMemLimit = strconv.FormatInt(usbHelperLimits.mem/4*3>>20, 10) + "MiB"
)

type usbHelper struct {
	cmd  *exec.Cmd
	link *usbshare.Helper
	done chan struct{} // closed once the process is gone
}

func (c *Core) startUSBHelper() error {
	h, err := c.launchUSBHelper()
	if err != nil {
		return fmt.Errorf("the sharing helper: %w", err)
	}
	c.usb.helper, c.usb.last = h, h
	go func() {
		<-h.done
		c.Post(func() { c.usbHelperEnded(h) })
	}()
	return nil
}

func (c *Core) launchUSBHelper() (*usbHelper, error) {
	exe := filepath.Join(c.cfg.AppBinDir, usbHelperProgram)
	if _, err := os.Stat(exe); err != nil {
		return nil, err
	}
	cmd, err := c.usbHelperCommand(exe)
	if err != nil {
		return nil, err
	}
	files, err := c.usbHelperFiles()
	if err != nil {
		return nil, err
	}
	defer closeFiles(files)
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	mine, theirs := os.NewFile(uintptr(fds[0]), "usbshared"), os.NewFile(uintptr(fds[1]), "usbshared-child")
	defer theirs.Close()
	nc, err := net.FileConn(mine)
	mine.Close()
	if err != nil {
		return nil, err
	}
	cmd.ExtraFiles = append([]*os.File{files[0], theirs}, files[1:]...)
	cmd.Env = append([]string{"PHONE_LANG=" + tr.Lang(), "GOMEMLIMIT=" + usbHelperMemLimit}, c.usbHelperEnv()...)
	if g := os.Getenv("GODEBUG"); g != "" {
		cmd.Env = append(cmd.Env, "GODEBUG="+g)
	}
	if lg, err := os.OpenFile(filepath.Join(c.cfg.RunDir, "usbshared.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stdout, cmd.Stderr = lg, lg
		defer lg.Close()
	}
	var cgfd *os.File
	if c.cg != nil && !c.cg.scopes {
		if cgfd, err = c.cg.prepareLimits(usbHelperGroup, usbHelperLimits); err == nil {
			cmd.SysProcAttr.UseCgroupFD, cmd.SysProcAttr.CgroupFD = true, int(cgfd.Fd())
		} else {
			c.log.Printf("usb sharing: the helper's cgroup: %v", err)
		}
	}
	err = StartProcess(cmd)
	if cgfd != nil {
		cgfd.Close()
	}
	if err != nil {
		nc.Close()
		return nil, err
	}
	h := &usbHelper{cmd: cmd, done: make(chan struct{})}
	pid := cmd.Process.Pid
	if c.cg != nil && c.cg.scopes {
		if err := c.cg.attachLimits(usbHelperGroup, pid, usbHelperLimits); err != nil {
			c.log.Printf("usb sharing: the helper's scope: %v", err)
		}
	}
	h.link = usbshare.ServeHelper(nc.(*net.UnixConn), usbshare.HelperConfig{
		Lib: usbLibrary{c}, Password: c.usb.pass, Lang: tr.Lang,
		Hello: func(hi usbshare.Hello) {
			c.Post(func() {
				c.log.Printf("usb sharing: helper pid %d, uid %d, capabilities %#x, sandbox %s", pid, hi.UID, hi.Caps, strings.Join(hi.Sandbox, ","))
			})
		},
		Log:   func(line string) { c.Post(func() { c.log.Printf("%s", line) }) },
		Added: func(string, string) { c.Post(func() { c.usb.added++ }) },
	})
	go func() {
		_ = cmd.Wait()
		h.link.Close()
		close(h.done)
	}()
	return h, nil
}

// usbHelperCommand runs the helper through the sandbox launcher; without
// root only the layers that need none apply, as for apps.
func (c *Core) usbHelperCommand(exe string) (*exec.Cmd, error) {
	attr := &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	launcher := filepath.Join(c.cfg.AppBinDir, "phone-sandbox")
	if _, err := os.Stat(launcher); err != nil {
		if c.Sandboxed() {
			return nil, fmt.Errorf("refusing to share files unconfined: %w", err)
		}
		c.log.Printf("usb sharing: no phone-sandbox, the helper confines only itself")
		cmd := exec.Command(exe)
		cmd.SysProcAttr = attr
		return cmd, nil
	}
	p := sandbox.Policy{Code: usbHelperCode(exe), Helper: true}
	if c.Sandboxed() {
		uid, err := c.appUID(usbHelperID)
		if err != nil {
			return nil, err
		}
		p.UID, p.Namespaces = uid, true
		attr.Cloneflags = syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS | syscall.CLONE_NEWNET
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(launcher, string(raw), exe)
	cmd.SysProcAttr = attr
	return cmd, nil
}

// usbHelperCode is what Landlock lets the helper read and run: its
// program, and the system's libraries when the program is linked against
// them (a developer build; the image's is static).
func usbHelperCode(exe string) []string {
	code := []string{exe}
	f, err := elf.Open(exe)
	if err != nil {
		return code
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return append(code, "/lib", "/lib64", "/usr/lib", "/usr/lib64")
		}
	}
	return code
}

// stopUSBHelper ends the helper: its end of the pair closes, and a helper
// still there a second later is killed.
func (c *Core) stopUSBHelper() {
	h := c.usb.helper
	c.usb.helper = nil
	if h == nil {
		return
	}
	h.link.Close()
	go func() {
		select {
		case <-h.done:
		case <-time.After(time.Second):
			_ = h.cmd.Process.Kill()
		}
	}()
}

func (c *Core) usbHelperEnded(h *usbHelper) {
	if c.cg != nil && (c.usb.helper == nil || c.usb.helper == h) {
		if n := c.cg.oomKills(usbHelperGroup); n > 0 {
			c.log.Printf("usb sharing: the helper went over its memory limit (%d MB)", usbHelperLimits.mem>>20)
		}
		c.cg.release(usbHelperGroup)
	}
	if c.usb.helper != h {
		return
	}
	c.usb.helper = nil
	if !c.usb.trusted || c.usb.ln == nil && c.usb.mtp == nil {
		return
	}
	if c.usb.restarts >= usbHelperRestarts {
		c.log.Printf("usb sharing: the helper ended (%v) after %d restarts", h.cmd.ProcessState, c.usb.restarts)
		c.endSharing("the helper keeps ending")
		return
	}
	c.usb.restarts++
	c.log.Printf("usb sharing: the helper ended (%v); starting it again", h.cmd.ProcessState)
	if err := c.startUSBHelper(); err != nil {
		c.log.Printf("usb sharing: %v", err)
		c.endSharing("the helper does not start")
	}
}

// --- Selftest ---

// usbHelperCmd is the selftest's usb-helper:
//
//	confined  the helper runs in its sandbox and cannot open or make a
//	          file by path, make a socket, run a program or debug one
//	limited   it is in a cgroup of its own with its memory and process
//	          limits (where the shell has cgroups)
//	refuses   the Shell refuses what a misbehaving helper asks
//	crash     the helper is killed; another starts and serves
//	gone      after sharing ends no helper is left
func (r *Runner) usbHelperCmd(arg string) error {
	c := r.C
	switch strings.TrimSpace(arg) {
	case "confined":
		h := c.usb.helper
		if h == nil {
			return errors.New("no sharing helper")
		}
		hi, ok := r.helperHello(h)
		if !ok {
			return errors.New("the helper did not report its sandbox")
		}
		layers := sandbox.ParseApplied(strings.Join(hi.Sandbox, ","))
		need := []string{"rlimit", "seccomp", sandbox.FDOnly}
		if c.Sandboxed() {
			need = append(need, "uid", "ns", "nonet", "landlock")
			if hi.UID == 0 || hi.Caps != 0 {
				return fmt.Errorf("the helper runs as uid %d with capabilities %#x", hi.UID, hi.Caps)
			}
		}
		for _, l := range need {
			if !layers[l] {
				return fmt.Errorf("the helper's sandbox lacks %s: %v", l, hi.Sandbox)
			}
		}
		got, err := h.link.Probe()
		if err != nil {
			return fmt.Errorf("probe: %w", err)
		}
		for _, k := range []string{"open", "create", "socket-inet", "socket-unix", "exec", "ptrace"} {
			if v := got[k]; v != "EPERM" && v != "EACCES" {
				return fmt.Errorf("the helper's %s was not refused: %q", k, v)
			}
		}
		c.log.Printf("usb sharing: helper pid %d confined (%s): %v", h.cmd.Process.Pid, strings.Join(hi.Sandbox, ","), got)
	case "limited":
		h := c.usb.helper
		if h == nil {
			return errors.New("no sharing helper")
		}
		if c.cg == nil {
			c.log.Printf("usb sharing: no cgroups (%s): the helper shares the shell's group", c.cgOff)
			return nil
		}
		for file, want := range map[string]string{"memory.max": strconv.FormatInt(usbHelperLimits.mem, 10), "memory.swap.max": "0",
			"pids.max": strconv.Itoa(usbHelperLimits.pids)} {
			if got := c.cg.value(usbHelperGroup, file); got != want {
				return fmt.Errorf("the helper's %s is %q, want %q", file, got, want)
			}
		}
		if !slices.Contains(strings.Fields(c.cg.value(usbHelperGroup, "cgroup.procs")), strconv.Itoa(h.cmd.Process.Pid)) {
			return fmt.Errorf("the helper (pid %d) is not in its group", h.cmd.Process.Pid)
		}
		c.log.Printf("usb sharing: helper pid %d in its group, memory %d MB, %d processes", h.cmd.Process.Pid, usbHelperLimits.mem>>20, usbHelperLimits.pids)
	case "refuses":
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
		if err != nil {
			return err
		}
		ends := [2]*net.UnixConn{}
		for i, fd := range fds {
			f := os.NewFile(uintptr(fd), "misbehaving")
			nc, err := net.FileConn(f)
			f.Close()
			if err != nil {
				return err
			}
			ends[i] = nc.(*net.UnixConn)
		}
		link := usbshare.ServeHelper(ends[0], usbshare.HelperConfig{Lib: usbLibrary{c}, Password: c.usb.pass, Lang: tr.Lang,
			Log: func(line string) { c.Post(func() { c.log.Printf("%s", line) }) }})
		defer link.Close()
		return r.offLoop(func() error { return usbshare.Misbehave(ends[1], "files") })
	case "crash":
		h := c.usb.helper
		if h == nil {
			return errors.New("no sharing helper")
		}
		old := h.cmd.Process.Pid
		_ = h.cmd.Process.Kill()
		end := time.Now().Add(r.scaled(10 * time.Second))
		for time.Now().Before(end) {
			if n := c.usb.helper; n != nil && n.cmd.Process.Pid != old {
				if _, ok := r.helperHello(n); ok {
					return nil
				}
			}
			r.pump(50 * time.Millisecond)
		}
		return fmt.Errorf("no new helper after pid %d was killed", old)
	case "gone":
		h := c.usb.last
		if h == nil {
			return errors.New("sharing never started a helper")
		}
		select {
		case <-h.done:
			if c.usb.helper != nil {
				return errors.New("a helper is running")
			}
		case <-time.After(r.scaled(3 * time.Second)):
			return fmt.Errorf("the helper (pid %d) is still running", h.cmd.Process.Pid)
		}
	default:
		return errors.New("usb-helper confined|limited|refuses|crash|gone")
	}
	return nil
}

// helperHello waits (keeping the loop going) for the helper's report.
func (r *Runner) helperHello(h *usbHelper) (usbshare.Hello, bool) {
	end := time.Now().Add(r.scaled(5 * time.Second))
	for {
		if hi, ok := h.link.Reported(); ok || time.Now().After(end) {
			return hi, ok
		}
		r.pump(20 * time.Millisecond)
	}
}

// scaled is a selftest wait under --timeout-scale (slow emulation).
func (r *Runner) scaled(d time.Duration) time.Duration { return d * time.Duration(max(1, r.Scale)) }

// offLoop runs f, which needs the shell's loop, while the loop goes on.
func (r *Runner) offLoop(f func() error) error {
	done := make(chan error, 1)
	go func() { done <- f() }()
	for {
		select {
		case err := <-done:
			return err
		default:
			r.pump(20 * time.Millisecond)
		}
	}
}

// usbPutBig is the selftest's usb-put-big <path> <bytes>: the computer
// copies that many bytes there without saying the length beforehand
// (chunked), is refused with 413, and the file is not in the folder.
func (r *Runner) usbPutBig(arg string) error {
	f := strings.Fields(arg)
	if len(f) != 2 {
		return errors.New("usb-put-big <path> <bytes>")
	}
	var n int64
	if _, err := fmt.Sscan(f[1], &n); err != nil {
		return err
	}
	dir, name := f[0][:strings.LastIndex(f[0], "/")+1], f[0][strings.LastIndex(f[0], "/")+1:]
	addr, pass := r.C.USBSharing()
	return r.computer(addr, func(do httpDo, _ dialPort, base string) error {
		req, err := http.NewRequest("PUT", base+f[0], io.LimitReader(zeroReader{}, n))
		if err != nil {
			return err
		}
		req.SetBasicAuth("kario", pass)
		resp, err := do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			return fmt.Errorf("PUT %s of %d bytes: %s", f[0], n, resp.Status)
		}
		req, _ = http.NewRequest("GET", base+dir, nil)
		req.SetBasicAuth("kario", pass)
		if resp, err = do(req); err != nil {
			return err
		}
		defer resp.Body.Close()
		page, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if strings.Contains(string(page), strings.TrimSuffix(name, filepath.Ext(name))) {
			return fmt.Errorf("%s is in %s after the 413", name, dir)
		}
		return nil
	})
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
