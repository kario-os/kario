package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/devctl"
	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/jsrt/nativetest"
	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// An app still writing its storage when the Shell stops (the runtime's
// writer thread waits for fsync, which takes seconds on slow flash) gets to
// finish before it is killed.
func TestShutdownWaitsForAnAppFinishingAWrite(t *testing.T) {
	done := filepath.Join(t.TempDir(), "written")
	cmd := exec.Command("sh", "-c", `trap 'sleep 1.5; echo ok > "$0"; exit 0' TERM; while :; do sleep 0.05; done`, done)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	c := &Core{apps: []*App{{running: true, proc: cmd.Process}}}
	t0 := time.Now()
	c.Shutdown()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the app was killed %v after SIGTERM, before it finished: %v", time.Since(t0).Round(10*time.Millisecond), err)
	}
	if _, err := os.Stat(done); err != nil {
		t.Fatalf("the app's write did not happen: %v", err)
	}
}

// On the device an app is the first process of a pid namespace of its own
// (sandbox.go), which SIGTERM's default action cannot end. A TypeScript app
// in the middle of a storage write when the Shell stops still ends by
// itself once the write is on disk, long before the grace is up, and how
// it ends is no crash. (The kernel lets an unprivileged user make such a
// namespace in a user namespace of its own; where it does not, the test is
// skipped.)
func TestShutdownTypeScriptAppAsFirstProcess(t *testing.T) {
	bin := jsrtBin(t)
	main := filepath.Join(t.TempDir(), "main.js")
	if err := os.WriteFile(main, []byte(storageMain(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	parent, child := os.NewFile(uintptr(fds[0]), "shell"), os.NewFile(uintptr(fds[1]), "app")
	defer parent.Close()
	cmd := exec.Command("/usr/bin/env", "LD_PRELOAD="+nativetest.SlowFS(t), "SLOWFS_DELAY_MS=500", bin, main)
	cmd.Env = []string{"PHONE_UI_FD=3", "PHONE_APP_ID=" + storeID, "PHONE_DATA_DIR=" + data, "PATH=" + os.Getenv("PATH")}
	cmd.ExtraFiles = []*os.File{child}
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWPID,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}}
	if err := cmd.Start(); err != nil {
		t.Skipf("no pid namespace of its own for the app: %v", err)
	}
	child.Close()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	conn := wire.New(parent)
	msgs := make(chan ui.Msg, 64)
	go func() {
		defer close(msgs)
		for {
			var m ui.Msg
			if conn.Recv(&m) != nil {
				return
			}
			msgs <- m
		}
	}()
	until := func(ok func(ui.Msg) bool) {
		t.Helper()
		for timeout := time.After(10 * time.Second); ; {
			select {
			case m, open := <-msgs:
				if !open {
					t.Fatalf("the app exited: %v", <-exited)
				}
				if ok(m) {
					return
				}
			case <-timeout:
				t.Fatal("no answer from the app")
			}
		}
	}
	until(func(m ui.Msg) bool { return m.T == ui.MsgHello })
	_ = conn.Send(ui.Msg{T: ui.MsgStart})
	_ = conn.Send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateForeground})
	until(func(m ui.Msg) bool { return m.Screen != nil })
	_ = conn.Send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "save", Value: "x"})
	until(func(m ui.Msg) bool { return m.T == ui.MsgToast && m.Text == "saved x" })
	stored := func() string {
		raw, err := os.ReadFile(filepath.Join(data, jsrt.StorageFile))
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		var m map[string]string
		if err != nil || json.Unmarshal(raw, &m) != nil {
			t.Fatalf("storage file: %v %q", err, raw)
		}
		return m["v"]
	}
	if got := stored(); got != "" {
		t.Fatalf("the write is done already (storage %q): fsync is not slow", got)
	}

	c := &Core{apps: []*App{{running: true, proc: cmd.Process}}}
	t0 := time.Now()
	c.Shutdown()
	took := time.Since(t0)
	err = <-exited
	t.Logf("ended %v after SIGTERM: %v", took.Round(time.Millisecond), err)
	if took >= stopGrace {
		t.Fatalf("still running when the grace (%v) was up: killed (%v)", stopGrace, err)
	}
	if r := exitReason(err); r != "" {
		t.Fatalf("its end reads as a crash: %s", r)
	}
	if got := stored(); got != `"x"` {
		t.Fatalf("ended before the write: storage %q", got)
	}
}

// A Shell that has shut down posts no more housekeeping to its loop, which
// no one runs any more: otherwise the goroutine, and the whole Shell with
// it, stays for as long as the process (FuzzAppMsg starts a Shell for each
// input).
func TestShutdownStopsHousekeeping(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "apps", "test.idle")
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id":"test.idle","name":"Idle","exec":"./none"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run, err := os.MkdirTemp("", "shutdown-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(run)
	c, err := New(Config{W: 40, H: 24, AppsDir: filepath.Join(root, "apps"), AppBinDir: filepath.Join(root, "bin"), RunDir: run,
		StateDir: filepath.Join(root, "state"), Log: discardLog{}, FreezeAfter: -1})
	if err != nil {
		t.Fatal(err)
	}
	c.screen.tick.Store(int64(10 * time.Millisecond))
	c.Shutdown()
	count := func(d time.Duration) (n int) {
		for end := time.After(d); ; {
			select {
			case <-c.ev:
				n++
			case <-end:
				return n
			}
		}
	}
	count(time.Second)
	if n := count(time.Second); n > 10 {
		t.Fatalf("%d events in a second after Shutdown", n)
	}
}

// Shutdown ends every helper process the Shell started, each the way its
// own stop does, and waits for it. A Shell that shut down and went on in
// the same process (the end-to-end tests, kario test's runs on a real
// Shell, a restart in place) kept them until the process ended: the USB
// sharing helper while the cable was in, a compile, the web processes,
// the selftest's access point and device, a computer's followed log.
// Checked by pid: no child of the test process (zombies too, a helper not
// waited for) is left when Shutdown returns. Alone: the other tests'
// Shells have children of their own.
func TestShutdownEndsHelpers(t *testing.T) {
	if !alone(t) {
		return
	}
	cat, sleep := lookPath(t, "cat"), lookPath(t, "sleep")
	usb := func(t *testing.T, mode, helper string, ends bool) {
		if os.Geteuid() == 0 {
			t.Skip("the helper runs without phone-sandbox, which root refuses")
		}
		bin := t.TempDir()
		pid := standIn(t, filepath.Join(bin, usbHelperProgram), "", helper)
		before := children()
		c := loopCore(t, bin)
		if err := c.onLoop(func() error { return startSharing(c, mode) }); err != nil {
			t.Fatal(err)
		}
		if took := checkShutdown(t, loopShutdown(c), before, pid()); ends && took >= stopGrace {
			t.Errorf("Shutdown took %v: the helper did not end when its pair closed", took)
		}
		time.Sleep(100 * time.Millisecond)
		_ = c.onLoop(func() error { return nil })
		for pid, what := range newChildren(before) {
			t.Errorf("pid %d (%s) started after Shutdown", pid, what)
		}
	}
	// The helper (no PATH) ends when the Shell's end of the pair on fd 4
	// closes, as phone-usbshared does, or holds on to it.
	t.Run("usb sharing", func(t *testing.T) { usb(t, "", "exec "+cat+" <&4 >/dev/null\n", true) })
	t.Run("usb file transfer", func(t *testing.T) { usb(t, ui.USBModeMTP, "exec "+cat+" <&4 >/dev/null\n", true) })
	t.Run("usb helper holding on", func(t *testing.T) { usb(t, "", "exec "+sleep+" 600\n", false) })

	t.Run("compile", func(t *testing.T) {
		bin := t.TempDir()
		pid := standIn(t, filepath.Join(bin, jsrt.Program), "case \"$1\" in --version) echo 'kario-jsrt (stand-in)'; exit 0;; esac\n", "exec sleep 600\n")
		before := children()
		c := loopCore(t, bin)
		src, err := os.ReadFile(filepath.Join("..", "jsrt", "testdata", "app", "main.js"))
		if err != nil {
			t.Fatal(err)
		}
		raw, key := devPackage(t, filepath.Join(t.TempDir(), "devstore"), map[string]string{
			"manifest.json": `{"id": "com.example.jsc", "name": "JSC", "runtime": "js", "version": "1.0"}`,
			"main.js":       string(src),
		})
		if err := c.onLoop(func() error {
			c.settings.DeveloperMode = true
			if _, err := c.devTrust(key); err != nil {
				return err
			}
			p, err := pkg.Open(raw, c.storeRoots())
			if err != nil {
				return err
			}
			m, err := c.checkPackage(p)
			if err != nil {
				return err
			}
			return c.unpack(p, m, c.appByID(m.ID))
		}); err != nil {
			t.Fatal(err)
		}
		checkShutdown(t, loopShutdown(c), before, pid())
	})

	t.Run("web process", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("PHONE_WEBVIEW is not taken as root")
		}
		webview := filepath.Join(t.TempDir(), "phone-webview")
		pid := standIn(t, webview, "", "exec sleep 600\n")
		t.Setenv("PHONE_WEBVIEW", webview)
		before := children()
		c := loopCore(t, t.TempDir())
		if err := c.onLoop(func() error {
			_, err := c.webProcFor(c.appByID("com.test.sys"))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		checkShutdown(t, loopShutdown(c), before, pid())
	})

	t.Run("enterprise access point", func(t *testing.T) {
		dir := t.TempDir()
		hostapd, stopped := filepath.Join(dir, "hostapd"), filepath.Join(dir, "stopped")
		pid := standIn(t, hostapd, "", "trap 'echo > "+stopped+"; exit 0' TERM\necho AP-ENABLED\nwhile :; do sleep 0.05; done\n")
		log, err := os.Create(filepath.Join(dir, "hostapd.log"))
		if err != nil {
			t.Fatal(err)
		}
		before := children()
		w := &iwdRadio{}
		if _, err := w.runEAP(log, hostapd, filepath.Join(dir, "hostapd.conf")); err != nil {
			t.Fatal(err)
		}
		c := &Core{wifi: w}
		checkShutdown(t, c.Shutdown, before, pid())
		if _, err := os.Stat(stopped); err != nil {
			t.Errorf("hostapd was not stopped as its parent's death stops it (SIGTERM): %v", err)
		}
	})

	t.Run("hotspot device", func(t *testing.T) {
		h := newHotspotClient("device", "wlan2", t.TempDir(), "Kario-test")
		before := children()
		go ownThread(func() {
			defer close(h.done)
			h.serve()
		})
		if _, err := h.start(h.path("sleep.log"), "sleep", "600"); err != nil {
			t.Fatal(err)
		}
		var pids []int
		for pid := range newChildren(before) {
			pids = append(pids, pid)
		}
		c := &Core{}
		c.helpers.add(h.helper())
		checkShutdown(t, c.Shutdown, before, pids...)
	})

	t.Run("followed log", func(t *testing.T) {
		journal := filepath.Join(t.TempDir(), "journalctl")
		pid := standIn(t, journal, "", "echo journal\nexec sleep 600\n")
		before := children()
		c := devOpen(t)
		_ = c.onLoop(func() error { c.dev.journal = journal; return nil })
		followed := make(chan error, 1)
		go func() {
			followed <- devctl.Call(c.DevLinkPath(), devctl.Request{Op: devctl.OpSyslog, Unit: "iwd", Follow: true}, nil, io.Discard, nil)
		}()
		checkShutdown(t, loopShutdown(c), before, pid())
		select {
		case <-followed:
		case <-time.After(5 * time.Second):
			t.Error("the computer's syslog -f went on after Shutdown")
		}
	})

	// phone-shell's SIGTERM handler shuts down off the loop, which goes on
	// meanwhile: the helpers' ends reach it while Shutdown waits.
	t.Run("off the loop", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("the helpers run without phone-sandbox, which root refuses")
		}
		bin := t.TempDir()
		usbPid := standIn(t, filepath.Join(bin, usbHelperProgram), "", "exec "+cat+" <&4 >/dev/null\n")
		webview := filepath.Join(t.TempDir(), "phone-webview")
		webPid := standIn(t, webview, "", "exec sleep 600\n")
		t.Setenv("PHONE_WEBVIEW", webview)
		before := children()
		c := loopCore(t, bin)
		if err := c.onLoop(func() error {
			if err := startSharing(c, ""); err != nil {
				return err
			}
			_, err := c.webProcFor(c.appByID("com.test.sys"))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		checkShutdown(t, c.Shutdown, before, usbPid(), webPid())
	})
}

// startSharing plugs the simulated cable in and trusts the computer, with
// USB mode mode; on the loop.
func startSharing(c *Core, mode string) error {
	c.settings.USBMode = mode
	if err := c.PlugUSB(true); err != nil {
		return err
	}
	if c.usb.prompt == nil {
		return errors.New("no question whether to trust the computer")
	}
	c.usb.prompt.onYes()
	if c.usb.helper == nil {
		return errors.New("sharing started no helper")
	}
	return nil
}

// checkShutdown runs shutdown and fails t for each child of the test
// process not in before that is left when it returns; started must be
// children before it. It returns how long shutdown took.
func checkShutdown(t *testing.T, shutdown func(), before map[int]string, started ...int) time.Duration {
	t.Helper()
	now := children()
	for _, pid := range started {
		if _, ok := now[pid]; !ok {
			t.Fatalf("pid %d is no child of the test (children %v)", pid, now)
		}
	}
	if len(started) == 0 {
		t.Fatal("no helper started")
	}
	t0 := time.Now()
	shutdown()
	took := time.Since(t0)
	t.Logf("Shutdown took %v", took.Round(time.Millisecond))
	for pid, what := range newChildren(before) {
		t.Errorf("pid %d (%s) is left when Shutdown returns", pid, what)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return took
}

// loopShutdown shuts c down on its loop (loopCore).
func loopShutdown(c *Core) func() {
	return func() { _ = c.onLoop(func() error { c.Shutdown(); return nil }) }
}

// children are the test process's children, zombies too, by pid: their
// name and state.
func children() map[int]string {
	out := map[int]string{}
	self := strconv.Itoa(os.Getpid())
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		s := string(raw)
		open, end := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if err != nil || open < 0 || end < open {
			continue
		}
		if f := strings.Fields(s[end+1:]); len(f) > 1 && f[1] == self {
			out[pid] = fmt.Sprintf("%s, state %s", s[open+1:end], f[0])
		}
	}
	return out
}

// newChildren are the children not in before.
func newChildren(before map[int]string) map[int]string {
	out := children()
	for pid := range before {
		delete(out, pid)
	}
	return out
}

// standIn writes a helper's stand-in at path: a shell script that runs pre,
// writes its pid where the returned function waits for it, then runs body.
func standIn(t *testing.T, path, pre, body string) (pid func() int) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "pid")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+pre+"echo $$ > '"+file+"'\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return func() int {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			raw, _ := os.ReadFile(file)
			if n, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				return n
			}
			if time.Now().After(end) {
				t.Fatalf("%s did not start", filepath.Base(path))
			}
		}
	}
}

// lookPath is prog's path, for a helper started without PATH.
func lookPath(t *testing.T, prog string) string {
	t.Helper()
	p, err := exec.LookPath(prog)
	if err != nil {
		t.Skip(err)
	}
	p, err = filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
