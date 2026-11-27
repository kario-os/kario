package shell

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/devctl"
	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

// loopCore is a Shell whose event loop runs on its own goroutine, as
// phone-shell's does; the test reaches its state through onLoop.
func loopCore(t *testing.T, binDir string) *Core {
	t.Helper()
	apps := t.TempDir()
	_ = os.MkdirAll(filepath.Join(apps, "sys"), 0o755)
	_ = os.WriteFile(filepath.Join(apps, "sys", "manifest.json"), []byte(`{"id": "com.test.sys", "name": "Sys", "exec": "true"}`), 0o644)
	run, err := os.MkdirTemp("", "devlink-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	c, err := New(Config{W: 40, H: 24, AppsDir: apps, AppBinDir: binDir, RunDir: run, StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case f := <-c.Events():
				f()
			case <-stop:
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = c.onLoop(func() error { c.Shutdown(); return nil })
		close(stop)
	})
	return c
}

// devPackage signs an app directory's files with a development store kept
// in storeDir, as kario package -devstore does; it returns the package and
// the store's root as kario device sends it.
func devPackage(t *testing.T, storeDir string, files map[string]string, exec ...string) ([]byte, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		mode := os.FileMode(0o644)
		for _, e := range exec {
			if e == name {
				mode = 0o755
			}
		}
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	store, err := pkg.DevStore(storeDir, filepath.Join(storeDir, "trust"))
	if err != nil {
		t.Fatal(err)
	}
	dev, err := pkg.DevDeveloper(filepath.Join(storeDir, "developers"), "Test Developer", store)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := pkg.Build(dir, dev)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(storeDir, "trust", "dev.pub"))
	if err != nil {
		t.Fatal(err)
	}
	return raw, strings.TrimSpace(string(key))
}

func devCall(c *Core, req devctl.Request, body []byte) (string, error) {
	var out bytes.Buffer
	if body != nil {
		req.Size = int64(len(body))
	}
	err := devctl.Call(c.DevLinkPath(), req, bytes.NewReader(body), &out, &out)
	return out.String(), err
}

func wantRefused(t *testing.T, what string, err error, why string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), why) {
		t.Fatalf("%s: %v, want a refusal: %s", what, err, why)
	}
}

// A refused install says why even when the package is far bigger than the
// socket's buffer: the refusal comes before the package is sent, not as a
// broken pipe halfway through it.
func TestDevLinkRefusesBeforeThePackage(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_, err := devCall(c, devctl.Request{Op: devctl.OpInstall}, make([]byte, 8<<20))
	wantRefused(t, "an 8 MB package with developer mode off", err, errDevModeOff)
}

// The developer link answers only with developer mode on and a trusted
// computer on the cable (on a device: a session that came in over it);
// then it installs through the installer, streams an installed app's log,
// reloads, restarts and uninstalls it, and leaves the system's apps alone.
func TestDevLink(t *testing.T) {
	if !alone(t) {
		return
	}
	c := loopCore(t, t.TempDir())
	app := map[string]string{
		"manifest.json": `{"id": "com.example.devtest", "name": "Dev test", "exec": "app", "version": "1.0"}`,
		"app":           "#!/bin/sh\necho hello from the app\nexec cat <&3 >/dev/null\n",
	}
	raw, key := devPackage(t, filepath.Join(t.TempDir(), "devstore"), app, "app")
	// A trusted computer is on the simulated cable: otherwise the
	// housekeeping's cable check ends the sharing.
	set := func(dev, trusted bool) {
		_ = c.onLoop(func() error {
			c.settings.DeveloperMode, c.usb.trusted, c.usb.simOn = dev, trusted, trusted
			return nil
		})
	}
	install := devctl.Request{Op: devctl.OpInstall}

	_, err := devCall(c, install, raw)
	wantRefused(t, "developer mode off, no computer", err, errDevModeOff)
	set(true, false)
	_, err = devCall(c, install, raw)
	wantRefused(t, "no trusted computer", err, errDevNoCable)
	_, err = devCall(c, devctl.Request{Op: devctl.OpTrust, Key: key}, nil)
	wantRefused(t, "trust without a trusted computer", err, errDevNoCable)
	set(false, true)
	_, err = devCall(c, devctl.Request{Op: devctl.OpLogs, App: "com.test.sys"}, nil)
	wantRefused(t, "developer mode off", err, errDevModeOff)
	_ = c.onLoop(func() error {
		c.settings.DeveloperMode = true
		c.usb.mtp = &usbMTP{}
		return nil
	})
	_, err = devCall(c, install, raw)
	wantRefused(t, "file transfer (MTP)", err, errDevNoCable)
	_ = c.onLoop(func() error {
		c.usb.mtp = nil
		c.usb.sim = false
		defer func() { c.usb.sim = true }()
		if err := c.devGate(""); err == nil || err.Error() != errDevNotCable {
			t.Errorf("a device took a request from no SSH session: %v", err)
		}
		if err := c.devGate("192.168.1.20 40000 192.168.1.5 22"); err == nil {
			t.Error("a device took a request from SSH over Wi-Fi")
		}
		if err := c.devGate("10.71.0.10 40000 " + devctl.USBAddr + " 22"); err != nil {
			t.Errorf("a session over the cable: %v", err)
		}
		if err := c.devGate("::ffff:10.71.0.10 40000 ::ffff:" + devctl.USBAddr + " 22"); err != nil {
			t.Errorf("a session over the cable through systemd's dual-stack socket: %v", err)
		}
		if err := c.devGate("::ffff:192.168.1.20 40000 ::ffff:192.168.1.5 22"); err == nil {
			t.Error("a device took a request from SSH over Wi-Fi through the dual-stack socket")
		}
		return nil
	})
	if _, err := os.Stat(filepath.Join(c.packagesDir(), "com.example.devtest")); err == nil {
		t.Fatal("installed while refused")
	}

	_, err = devCall(c, install, raw)
	wantRefused(t, "a store the phone does not trust", err, "does not trust")
	if out, err := devCall(c, devctl.Request{Op: devctl.OpTrust, Key: key}, nil); err != nil || !strings.Contains(out, "trusting") {
		t.Fatalf("trust: %q %v", out, err)
	}
	if out, err := devCall(c, install, raw); err != nil || !strings.Contains(out, "installed com.example.devtest 1.0") {
		t.Fatalf("install: %q %v", out, err)
	}
	_ = c.onLoop(func() error {
		if a := c.appByID("com.example.devtest"); a == nil || !a.M.installed {
			t.Error("not among the apps")
		}
		if c.toast == nil || !strings.Contains(c.toast.title+c.toast.body, "Dev test") {
			t.Errorf("no toast on the phone: %+v", c.toast)
		}
		return nil
	})

	if _, err := devCall(c, devctl.Request{Op: devctl.OpLogs, App: "com.test.sys"}, nil); err == nil || !strings.Contains(err.Error(), errDevNotYours) {
		t.Fatalf("a system app's log: %v", err)
	}
	_, err = devCall(c, devctl.Request{Op: devctl.OpRestart, App: "com.test.sys"}, nil)
	wantRefused(t, "restarting a system app", err, errDevNotYours)
	if out, err := devCall(c, devctl.Request{Op: devctl.OpRestart, App: "com.example.devtest"}, nil); err != nil || !strings.Contains(out, "restarted") {
		t.Fatalf("restart: %q %v", out, err)
	}
	var pid int
	_ = c.onLoop(func() error { pid = c.AppPid("com.example.devtest"); return nil })
	if pid == 0 {
		t.Fatal("not running after restart")
	}
	var log string
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && !strings.Contains(log, "hello from the app"); time.Sleep(50 * time.Millisecond) {
		log, err = devCall(c, devctl.Request{Op: devctl.OpLogs, App: "com.example.devtest"}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(log, "hello from the app") {
		t.Fatalf("logs: %q", log)
	}
	if out, err := devCall(c, devctl.Request{Op: devctl.OpRestart, App: "com.example.devtest"}, nil); err != nil || !strings.Contains(out, "restarted") {
		t.Fatalf("restart: %q %v", out, err)
	}
	_ = c.onLoop(func() error {
		if now := c.AppPid("com.example.devtest"); now == 0 || now == pid {
			t.Errorf("restart: pid %d, was %d", now, pid)
		}
		return nil
	})
	if out, err := devCall(c, devctl.Request{Op: devctl.OpReload, App: "com.example.devtest"}, nil); err != nil || !strings.Contains(out, "reloading") {
		t.Fatalf("reload: %q %v", out, err)
	}

	followed := make(chan error, 1)
	var stream bytes.Buffer
	go func() {
		followed <- devctl.Call(c.DevLinkPath(), devctl.Request{Op: devctl.OpLogs, App: "com.example.devtest", Follow: true}, nil, &stream, nil)
	}()
	time.Sleep(300 * time.Millisecond)
	set(true, false)
	select {
	case <-followed:
	case <-time.After(5 * time.Second):
		t.Fatal("a followed log went on after the computer was no longer trusted")
	}
	if !strings.Contains(stream.String(), errDevNoCable) {
		t.Fatalf("followed log: %q", stream.String())
	}
	_, err = devCall(c, devctl.Request{Op: devctl.OpUninstall, App: "com.example.devtest"}, nil)
	wantRefused(t, "uninstall after unplugging", err, errDevNoCable)

	set(true, true)
	if out, err := devCall(c, devctl.Request{Op: devctl.OpUninstall, App: "com.example.devtest"}, nil); err != nil || !strings.Contains(out, "uninstalled") {
		t.Fatalf("uninstall: %q %v", out, err)
	}
	_ = c.onLoop(func() error {
		if c.appByID("com.example.devtest") != nil {
			t.Error("still among the apps")
		}
		return nil
	})
	if _, err := os.Stat(filepath.Join(c.packagesDir(), "com.example.devtest")); !os.IsNotExist(err) {
		t.Fatalf("package left behind: %v", err)
	}
}

// The computer debugs an installed TypeScript app over the developer
// link: the app restarts under kario-jsrt's debugger, whose socket the
// connection carries both ways; it may sit paused without an ANR; the
// session ends when the computer goes. A program, an app installed some
// other way and a second debugger at once are refused, as is everything
// with developer mode off.
func TestDevLinkDebug(t *testing.T) {
	bin := t.TempDir()
	if err := os.Symlink(jsrtBin(t), filepath.Join(bin, jsrt.Program)); err != nil {
		t.Fatal(err)
	}
	c := loopCore(t, bin)
	store := filepath.Join(t.TempDir(), "devstore")
	js, key := devPackage(t, store, map[string]string{
		"manifest.json": `{"id": "com.example.dbg", "name": "Debug me", "runtime": "js", "version": "1.0"}`,
		"main.js": `import * as kario from "kario";
kario.on("start", () => {
  const greeting = "hello";
  debugger;
  kario.reset({ id: "main", title: greeting, body: [] });
});
`,
	})
	native, _ := devPackage(t, store, map[string]string{
		"manifest.json": `{"id": "com.example.prog", "name": "Program", "exec": "app", "version": "1.0"}`,
		"app":           "#!/bin/sh\nexec sleep 30\n",
	}, "app")
	debug := devctl.Request{Op: devctl.OpDebug, App: "com.example.dbg"}

	_ = c.onLoop(func() error {
		c.settings.DeveloperMode, c.usb.trusted = true, true
		c.usb.simOn, c.usb.state = true, "configured"
		return nil
	})
	if _, err := devCall(c, devctl.Request{Op: devctl.OpTrust, Key: key}, nil); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{js, native} {
		if _, err := devCall(c, devctl.Request{Op: devctl.OpInstall}, raw); err != nil {
			t.Fatal(err)
		}
	}
	_, err := devCall(c, devctl.Request{Op: devctl.OpDebug, App: "com.example.prog"}, nil)
	wantRefused(t, "debugging a program", err, "not a TypeScript app")
	_, err = devCall(c, devctl.Request{Op: devctl.OpDebug, App: "com.test.sys"}, nil)
	wantRefused(t, "debugging a system app", err, errDevNotYours)
	_ = c.onLoop(func() error { c.settings.DeveloperMode = false; return nil })
	_, err = devCall(c, debug, nil)
	wantRefused(t, "debugging with developer mode off", err, errDevModeOff)
	_ = c.onLoop(func() error { c.settings.DeveloperMode = true; return nil })

	toApp, fromComputer := io.Pipe()
	fromApp, toComputer := io.Pipe()
	session := make(chan error, 1)
	go func() { session <- devctl.Call(c.DevLinkPath(), debug, toApp, toComputer, nil); toComputer.Close() }()
	lines := bufio.NewReader(fromApp)
	next := func() string {
		t.Helper()
		got := make(chan string, 1)
		go func() { l, _ := lines.ReadString('\n'); got <- l }()
		select {
		case l := <-got:
			return l
		case <-time.After(10 * time.Second):
			t.Fatal("nothing from the debugger")
			return ""
		}
	}
	if l := next(); !strings.HasPrefix(l, "debugging com.example.dbg 1.0 (main.js sha256 ") {
		t.Fatalf("reply %q", l)
	}
	if l := next(); !strings.Contains(l, `"event":"hello"`) || !strings.Contains(l, `"app":"com.example.dbg"`) {
		t.Fatalf("hello %q", l)
	}
	_, err = devCall(c, debug, nil)
	wantRefused(t, "a second debugger", err, "being debugged already")
	_, _ = io.WriteString(fromComputer, `{"id": 1, "cmd": "run"}`+"\n")
	if l := next(); !strings.Contains(l, `"id":1`) || !strings.Contains(l, `"ok":true`) {
		t.Fatalf("run %q", l)
	}
	if l := next(); !strings.Contains(l, `"reason":"debugger"`) {
		t.Fatalf("stop %q", l)
	}
	_ = c.onLoop(func() error {
		a := c.appByID("com.example.dbg")
		a.ping.sent = c.now().Add(-time.Minute)
		c.fg = a
		c.checkANR()
		for _, o := range c.overlays {
			if _, ok := o.(*anrOverlay); ok {
				t.Error("an ANR for an app paused in the debugger")
			}
		}
		a.ping.sent = time.Time{}
		return nil
	})
	_, _ = io.WriteString(fromComputer, `{"id": 2, "cmd": "evaluate", "frame": 0, "expression": "greeting + 1"}`+"\n")
	if l := next(); !strings.Contains(l, `"value":"\"hello1\""`) {
		t.Fatalf("evaluate %q", l)
	}
	_, _ = io.WriteString(fromComputer, `{"id": 3, "cmd": "continue"}`+"\n")
	next()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var title string
		_ = c.onLoop(func() error {
			if a := c.appByID("com.example.dbg"); len(a.stack) > 0 {
				title = a.stack[len(a.stack)-1].S.Title
			}
			return nil
		})
		if title == "hello" {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the app did not go on after continue")
		}
	}
	fromComputer.Close()
	select {
	case <-session:
	case <-time.After(5 * time.Second):
		t.Fatal("the session went on after the computer went")
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var debugging bool
		_ = c.onLoop(func() error { debugging = c.appByID("com.example.dbg").debugging; return nil })
		if !debugging {
			break
		}
		if time.Now().After(end) {
			t.Fatal("still debugging after the session")
		}
	}

	// Installed some other way (the record says so): no debugger.
	_ = c.onLoop(func() error {
		a := c.appByID("com.example.dbg")
		rec := readInstallRecord(a.M.dir)
		rec.Store = "kario"
		raw, _ := json.Marshal(rec)
		return os.WriteFile(filepath.Join(a.M.dir, installRecordFile), raw, 0o644)
	})
	_, err = devCall(c, debug, nil)
	wantRefused(t, "debugging a store app", err, "not installed in developer mode")
}

// A debugger attaches to an installed TypeScript app as it runs: the same
// process (no restart, its state kept), which started from bytecode
// compiled with every statement's position, so a breakpoint on a line
// with only a literal stops; once the computer goes the app runs on, and
// the app takes a debugger again. An app not running is started under
// one instead.
func TestDevLinkAttach(t *testing.T) {
	bin := t.TempDir()
	if err := os.Symlink(jsrtBin(t), filepath.Join(bin, jsrt.Program)); err != nil {
		t.Fatal(err)
	}
	c := loopCore(t, bin)
	store := filepath.Join(t.TempDir(), "devstore")
	main := `import * as kario from "kario";
let n = 0;
function bump() {
  let step = 1; // @step
  n += step;
  kario.update({ id: "main", title: "n " + n, body: [] });
}
kario.on("start", () => kario.reset({ id: "main", title: "n " + n, body: [] }));
kario.on("action", () => bump());
`
	raw, key := devPackage(t, store, map[string]string{
		"manifest.json": `{"id": "com.example.att", "name": "Attach me", "runtime": "js", "version": "1.0"}`,
		"main.js":       main,
	})
	_ = c.onLoop(func() error {
		c.settings.DeveloperMode, c.usb.trusted = true, true
		c.usb.simOn, c.usb.state = true, "configured"
		return nil
	})
	if _, err := devCall(c, devctl.Request{Op: devctl.OpTrust, Key: key}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := devCall(c, devctl.Request{Op: devctl.OpInstall}, raw); err != nil {
		t.Fatal(err)
	}
	waitBuilt(t, c, 1)
	title := func() string {
		var s string
		_ = c.onLoop(func() error {
			if a := c.appByID("com.example.att"); len(a.stack) > 0 {
				s = a.stack[len(a.stack)-1].S.Title
			}
			return nil
		})
		return s
	}
	waitTitle := func(want string) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); title() != want; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("title %q, want %q", title(), want)
			}
		}
	}
	press := func() {
		_ = c.onLoop(func() error {
			c.appByID("com.example.att").send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "x"})
			return nil
		})
	}
	var args []string
	var pid int
	_ = c.onLoop(func() error {
		a := c.appByID("com.example.att")
		c.Launch(a, "")
		args = appCmdline(a)
		return nil
	})
	waitTitle("n 0")
	press()
	press()
	waitTitle("n 2")
	_ = c.onLoop(func() error { pid = c.AppPid("com.example.att"); return nil })
	if !slices.Contains(args, jsrt.BytecodeFlag) {
		t.Fatalf("not started from its bytecode: %v", args)
	}

	session := func() (io.Writer, func(), func() string, chan error) {
		return debugSession(t, c, devctl.Request{Op: devctl.OpDebug, App: "com.example.att", Attach: true})
	}
	w, hangUp, next, done := session()
	if l := next(); !strings.HasPrefix(l, "attached to com.example.att 1.0 (main.js sha256 ") {
		t.Fatalf("reply %q", l)
	}
	if l := next(); !strings.Contains(l, `"event":"hello"`) || !strings.Contains(l, `"attached":true`) {
		t.Fatalf("hello %q", l)
	}
	step := 0
	for i, l := range strings.Split(main, "\n") {
		if strings.Contains(l, "@step") {
			step = i + 1
		}
	}
	fmt.Fprintf(w, `{"id": 1, "cmd": "setBreakpoints", "file": "main.js", "breakpoints": [{"line": %d}]}`+"\n", step)
	next()
	_, _ = io.WriteString(w, `{"id": 2, "cmd": "run"}`+"\n")
	next()
	press()
	if l := next(); !strings.Contains(l, `"reason":"breakpoint"`) {
		t.Fatalf("stop %q", l)
	}
	_, _ = io.WriteString(w, `{"id": 3, "cmd": "evaluate", "frame": 0, "expression": "n"}`+"\n")
	if l := next(); !strings.Contains(l, `"value":"2"`) {
		t.Fatalf("the app's state after attaching: %q", l)
	}
	_ = c.onLoop(func() error {
		if now := c.AppPid("com.example.att"); now != pid {
			t.Errorf("attaching restarted the app: pid %d, was %d", now, pid)
		}
		if !c.appByID("com.example.att").debugging {
			t.Error("not debugging while attached")
		}
		return nil
	})
	_, _ = io.WriteString(w, `{"id": 4, "cmd": "continue"}`+"\n")
	next()
	waitTitle("n 3")
	hangUp()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the session went on after the computer went")
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var debugging bool
		_ = c.onLoop(func() error { debugging = c.appByID("com.example.att").debugging; return nil })
		if !debugging {
			break
		}
		if time.Now().After(end) {
			t.Fatal("still debugging after the session")
		}
	}
	press()
	waitTitle("n 4")

	// An app that sat idle while attached lets the debugger go too: the
	// next one attaches.
	for i := range 2 {
		w, hangUp, next, done = session()
		if l := next(); !strings.HasPrefix(l, "attached to ") {
			t.Fatalf("attach %d: %q", i+2, l)
		}
		if l := next(); !strings.Contains(l, `"attached":true`) {
			t.Fatalf("attach %d: hello %q", i+2, l)
		}
		_, _ = io.WriteString(w, `{"id": 1, "cmd": "run"}`+"\n")
		next()
		hangUp()
		<-done
	}
	_ = c.onLoop(func() error { c.kill(c.appByID("com.example.att")); return nil })
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var running, debugging bool
		_ = c.onLoop(func() error {
			a := c.appByID("com.example.att")
			running, debugging = a.running, a.debugging
			return nil
		})
		if !running && !debugging {
			break
		}
		if time.Now().After(end) {
			t.Fatal("still running")
		}
	}
	_, hangUp, next, done = session()
	if l := next(); !strings.HasPrefix(l, "debugging com.example.att 1.0 ") {
		t.Fatalf("attaching to an app not running: %q", l)
	}
	if l := next(); !strings.Contains(l, `"attached":false`) {
		t.Fatalf("hello %q", l)
	}
	hangUp()
	<-done
}

// debugSession is a debugger's connection over the developer link: what
// it writes to the app, its next line, and hangUp, the computer going
// (whatever the app still says is read and dropped).
func debugSession(t *testing.T, c *Core, req devctl.Request) (io.Writer, func(), func() string, chan error) {
	toApp, fromComputer := io.Pipe()
	fromApp, toComputer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- devctl.Call(c.DevLinkPath(), req, toApp, toComputer, nil)
		toComputer.Close()
	}()
	lines := bufio.NewReader(fromApp)
	next := func() string {
		t.Helper()
		got := make(chan string, 1)
		go func() { l, _ := lines.ReadString('\n'); got <- l }()
		select {
		case l := <-got:
			return l
		case <-time.After(10 * time.Second):
			t.Fatal("nothing from the debugger")
			return ""
		}
	}
	hangUp := func() {
		fromComputer.Close()
		go func() { _, _ = io.Copy(io.Discard, lines) }()
	}
	return fromComputer, hangUp, next, done
}

// A debugger attaches to an app frozen in the background: the app is
// thawed to take it, without a restart, and stays awake while debugged
// however long it has been in the background (a frozen app would not
// answer); once the debugger goes it freezes again.
func TestDevLinkAttachFrozen(t *testing.T) {
	bin := t.TempDir()
	if err := os.Symlink(jsrtBin(t), filepath.Join(bin, jsrt.Program)); err != nil {
		t.Fatal(err)
	}
	c := loopCore(t, bin)
	const id = "com.example.frz"
	raw, key := devPackage(t, filepath.Join(t.TempDir(), "devstore"), map[string]string{
		"manifest.json": `{"id": "` + id + `", "name": "Freeze me", "runtime": "js", "version": "1.0"}`,
		"main.js": `import * as kario from "kario";
globalThis.answer = 41;
kario.on("start", () => kario.reset({ id: "main", title: "Freeze me", body: [] }));
`,
	})
	_ = c.onLoop(func() error {
		c.settings.DeveloperMode, c.usb.trusted = true, true
		c.usb.simOn, c.usb.state = true, "configured"
		return nil
	})
	if _, err := devCall(c, devctl.Request{Op: devctl.OpTrust, Key: key}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := devCall(c, devctl.Request{Op: devctl.OpInstall}, raw); err != nil {
		t.Fatal(err)
	}
	waitBuilt(t, c, 1)
	_ = c.onLoop(func() error { c.Launch(c.appByID(id), ""); return nil })
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var ready bool
		_ = c.onLoop(func() error { ready = c.appByID(id).ready; return nil })
		if ready {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the app did not start")
		}
	}
	var pid int
	// freeze leaves the app a minute in the background and lets the
	// Shell freeze what it would; it reports what the kernel has, given
	// the time a stop or a continue takes to reach the process.
	freeze := func(want string) (bool, string) {
		var frozen bool
		var state string
		_ = c.onLoop(func() error {
			a := c.appByID(id)
			if c.fg == a {
				c.setForeground(nil)
			}
			a.bgSince = time.Now().Add(-time.Minute)
			c.freezeIdle()
			frozen, pid = a.frozen, c.AppPid(id)
			return nil
		})
		for end := time.Now().Add(2 * time.Second); state != want && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			_ = c.onLoop(func() error { state = c.ProcState(id); return nil })
		}
		return frozen, state
	}
	if frozen, state := freeze("frozen"); !frozen || state != "frozen" {
		t.Fatalf("in the background: frozen %v, %s", frozen, state)
	}
	was := pid

	w, hangUp, next, done := debugSession(t, c, devctl.Request{Op: devctl.OpDebug, App: id, Attach: true})
	if l := next(); !strings.HasPrefix(l, "attached to "+id+" 1.0 ") {
		t.Fatalf("reply %q", l)
	}
	if l := next(); !strings.Contains(l, `"event":"hello"`) || !strings.Contains(l, `"attached":true`) {
		t.Fatalf("hello %q", l)
	}
	if frozen, state := freeze("running"); frozen || state != "running" {
		t.Fatalf("frozen while debugged: %v, %s", frozen, state)
	}
	_, _ = io.WriteString(w, `{"id": 1, "cmd": "run"}`+"\n")
	next()
	_, _ = io.WriteString(w, `{"id": 2, "cmd": "evaluate", "expression": "answer + 1"}`+"\n")
	if l := next(); !strings.Contains(l, `"id":2`) || !strings.Contains(l, `"value":"42"`) {
		t.Fatalf("evaluate %q", l)
	}
	if pid != was {
		t.Fatalf("attaching restarted the app: pid %d, was %d", pid, was)
	}
	hangUp()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the session went on after the computer went")
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var debugging bool
		_ = c.onLoop(func() error { debugging = c.appByID(id).debugging; return nil })
		if !debugging {
			break
		}
		if time.Now().After(end) {
			t.Fatal("still debugging after the session")
		}
	}
	if frozen, state := freeze("frozen"); !frozen || state != "frozen" || pid != was {
		t.Fatalf("after the debugger went: frozen %v, %s, pid %d (was %d)", frozen, state, pid, was)
	}
}
