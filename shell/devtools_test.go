package shell

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/devctl"
	"github.com/kario-os/kario/netfw"
)

// devOpen is a Shell in developer mode with a trusted computer on the
// cable, its link's firewall as the Shell sets it.
func devOpen(t *testing.T) *Core {
	t.Helper()
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		c.settings.DeveloperMode, c.usb.trusted = true, true
		c.usb.simOn, c.usb.state = true, "configured"
		c.usbFirewall(true)
		return nil
	})
	return c
}

// devFetch is a request whose reply is followed by bytes: them and what
// the Shell said beside them.
func devFetch(c *Core, req devctl.Request) ([]byte, string, error) {
	var out, notes bytes.Buffer
	err := devctl.Call(c.DevLinkPath(), req, nil, &out, &notes)
	return out.Bytes(), strings.TrimSpace(notes.String()), err
}

// Every tool of the link meets the same gate as installing does.
func TestDevToolsGate(t *testing.T) {
	c := loopCore(t, t.TempDir())
	for _, req := range []devctl.Request{
		{Op: devctl.OpPush, Path: "x", Size: 1},
		{Op: devctl.OpPull, Path: "x"},
		{Op: devctl.OpScreenshot},
		{Op: devctl.OpKey, Keys: []string{"enter"}},
		{Op: devctl.OpType, Text: "x"},
		{Op: devctl.OpSyslog},
		{Op: devctl.OpForward, Port: 8080},
		{Op: devctl.OpReboot},
		{Op: devctl.OpUpdate, Size: 1},
		{Op: devctl.OpBugreport},
	} {
		var body io.Reader
		if req.Size > 0 {
			body = strings.NewReader("x")
		}
		err := devctl.Call(c.DevLinkPath(), req, body, io.Discard, nil)
		wantRefused(t, req.Op+" with developer mode off", err, errDevModeOff)
	}
	_ = c.onLoop(func() error { c.settings.DeveloperMode = true; return nil })
	_, _, err := devFetch(c, devctl.Request{Op: devctl.OpBugreport})
	wantRefused(t, "a bug report without a trusted computer", err, errDevNoCable)
}

// Keys the computer injects work the phone as the person's do, but a
// system dialog that unlocks, grants or trusts takes none of them except
// the one that cancels, and the lock takes none at all; the person's own
// keys still answer. Meanwhile the status bar says keys come from the
// computer.
func TestDevInjectedKeys(t *testing.T) {
	if !alone(t) {
		return
	}
	c := devOpen(t)
	key := func(names ...string) error {
		_, err := devCall(c, devctl.Request{Op: devctl.OpKey, Keys: names}, nil)
		return err
	}
	if _, err := devCall(c, devctl.Request{Op: devctl.OpKey, Keys: []string{"bogus"}}, nil); err == nil || !strings.Contains(err.Error(), "not a key") {
		t.Fatalf("an unknown key: %v", err)
	}

	unlocked := false
	_ = c.onLoop(func() error {
		c.pushOverlay(&confirmOverlay{text: "Unlock the device?", trust: true, onYes: func() { unlocked = true }})
		return nil
	})
	err := key("enter")
	wantRefused(t, "an injected enter on the unlock dialog", err, "takes only the keys of the person holding the phone")
	if err := key("right"); err == nil {
		t.Fatal("an injected arrow moved a trust dialog's choice")
	}
	_ = c.onLoop(func() error {
		if len(c.overlays) != 1 || unlocked {
			t.Errorf("the dialog answered: overlays %d, unlocked %v", len(c.overlays), unlocked)
		}
		if c.toast == nil || c.toast.body != tr.T("Keys from the computer cannot answer this; use the phone's own keys") {
			t.Errorf("nothing on the phone says why: %+v", c.toast)
		}
		if mid, kind := c.statusMid(); kind != "input" || mid != tr.T("● Keys from the computer") {
			t.Errorf("status bar while injecting: %q %q", mid, kind)
		}
		return nil
	})
	if err := key("esc"); err != nil {
		t.Fatalf("an injected esc cancels: %v", err)
	}
	_ = c.onLoop(func() error {
		if len(c.overlays) != 0 {
			t.Error("esc did not cancel the dialog")
		}
		c.pushOverlay(&confirmOverlay{text: "Unlock the device?", trust: true, onYes: func() { unlocked = true }})
		c.overlayShown[c.overlays[0]] = time.Now().Add(-time.Second)
		c.Key(Named(KeyEnter))
		if !unlocked {
			t.Error("the person's own enter did not answer")
		}
		return nil
	})

	saved := false
	_ = c.onLoop(func() error {
		c.pushOverlay(&confirmOverlay{text: "Save “a.txt” to Files?", onYes: func() { saved = true }})
		return nil
	})
	if err := key("enter"); err != nil || !saved {
		t.Fatalf("an injected enter on an ordinary dialog: %v, saved %v", err, saved)
	}

	for _, o := range []overlay{&permOverlay{}, &authOverlay{}, &lockPadOverlay{}, &simPinOverlay{}, &menuOverlay{trust: true}, &confirmOverlay{secret: true, trust: true}} {
		if !refusesInjected(o) {
			t.Errorf("%T takes injected keys", o)
		}
	}
	for _, o := range []overlay{&menuOverlay{}, &confirmOverlay{}, &incomingOverlay{}, &scanOverlay{}} {
		if refusesInjected(o) {
			t.Errorf("%T refuses injected keys", o)
		}
	}

	_ = c.onLoop(func() error { c.keylock = true; return nil })
	err = key("tab")
	wantRefused(t, "an injected key on the lock", err, "the lock takes only")
	_, err = devCall(c, devctl.Request{Op: devctl.OpType, Text: "*"}, nil)
	wantRefused(t, "an injected * on the lock", err, "the lock")
	_ = c.onLoop(func() error {
		if !c.keylock {
			t.Error("injected keys unlocked the keypad")
		}
		c.keylock = false
		return nil
	})
	time.Sleep(devInputShown + 100*time.Millisecond)
	_ = c.onLoop(func() error {
		if _, kind := c.statusMid(); kind == "input" {
			t.Error("the status bar still says keys come from the computer")
		}
		return nil
	})
}

// Typed text reaches the focused input as the person's typing does.
func TestDevTypedText(t *testing.T) {
	if !alone(t) {
		return
	}
	c := devOpen(t)
	if out, err := devCall(c, devctl.Request{Op: devctl.OpType, Text: "/\u8ba1\u7b97\u5668"}, nil); err != nil || !strings.Contains(out, "typed 4 characters") {
		t.Fatalf("type: %q %v", out, err)
	}
	_ = c.onLoop(func() error {
		if n := len(c.overlays); n == 0 {
			t.Error("typing / opened no palette")
		} else if m, ok := c.overlays[n-1].(*menuOverlay); !ok || m.query != "\u8ba1\u7b97\u5668" {
			t.Errorf("the palette has %+v", c.overlays[n-1])
		}
		return nil
	})
}

// Files go to and come from the developer directory and the data of an
// app installed in developer mode, nothing else; a push cut short leaves
// nothing behind.
func TestDevPushPull(t *testing.T) {
	if !alone(t) {
		return
	}
	c := devOpen(t)
	push := func(path, body string, mode uint32) (string, error) {
		var out bytes.Buffer
		err := devctl.Call(c.DevLinkPath(), devctl.Request{Op: devctl.OpPush, Path: path, Size: int64(len(body)), Mode: mode}, strings.NewReader(body), &out, nil)
		return out.String(), err
	}
	pull := func(path string) (string, error) {
		raw, _, err := devFetch(c, devctl.Request{Op: devctl.OpPull, Path: path})
		return string(raw), err
	}
	dev := c.devDir()
	if out, err := push("fixtures/a.txt", "hello", 0o755); err != nil || !strings.Contains(out, "pushed "+filepath.Join(dev, "fixtures", "a.txt")+" (5 bytes)") {
		t.Fatalf("push: %q %v", out, err)
	}
	if st, err := os.Stat(filepath.Join(dev, "fixtures", "a.txt")); err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("pushed file: %v %v", st, err)
	}
	if got, err := pull(filepath.Join(dev, "fixtures", "a.txt")); err != nil || got != "hello" {
		t.Fatalf("pull by its absolute path: %q %v", got, err)
	}
	if got, err := push("empty", "", 0); err != nil || !strings.Contains(got, "(0 bytes)") {
		t.Fatalf("an empty file: %q %v", got, err)
	}
	if got, err := pull("empty"); err != nil || got != "" {
		t.Fatalf("pull an empty file: %q %v", got, err)
	}
	for _, p := range []string{"/etc/passwd", filepath.Join(c.cfg.StateDir, "settings.json"), "../settings.json", "fixtures/../../x", "com.test.sys:x"} {
		if _, err := push(p, "x", 0); err == nil {
			t.Errorf("pushed %s", p)
		}
		if _, err := pull(p); err == nil {
			t.Errorf("pulled %s", p)
		}
	}
	if _, err := pull("fixtures"); err == nil || !strings.Contains(err.Error(), "not a file") {
		t.Fatalf("pull a directory: %v", err)
	}

	cut := devctl.Request{Op: devctl.OpPush, Path: "cut.bin", Size: 1 << 20}
	if err := devctl.Call(c.DevLinkPath(), cut, strings.NewReader("only this"), io.Discard, nil); err == nil {
		t.Fatal("a push cut short succeeded")
	}
	time.Sleep(100 * time.Millisecond)
	if left, _ := filepath.Glob(filepath.Join(dev, "cut.bin*")); len(left) != 0 {
		t.Fatalf("a push cut short left %v", left)
	}

	raw, key := devPackage(t, filepath.Join(t.TempDir(), "devstore"), map[string]string{
		"manifest.json": `{"id": "com.example.devfiles", "name": "Dev files", "exec": "app", "version": "1.0"}`,
		"app":           "#!/bin/sh\nexec sleep 30\n",
	}, "app")
	if _, err := devCall(c, devctl.Request{Op: devctl.OpTrust, Key: key}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := devCall(c, devctl.Request{Op: devctl.OpInstall}, raw); err != nil {
		t.Fatal(err)
	}
	if out, err := push("com.example.devfiles:files/seed.json", `{"n": 1}`, 0); err != nil || !strings.Contains(out, filepath.Join(c.cfg.StateDir, "apps", "com.example.devfiles", "files", "seed.json")) {
		t.Fatalf("push into the app's data: %q %v", out, err)
	}
	if got, err := pull("com.example.devfiles:/files/seed.json"); err != nil || got != `{"n": 1}` {
		t.Fatalf("pull from the app's data: %q %v", got, err)
	}
	if _, err := push("com.example.devfiles:../com.test.sys/x", "x", 0); err == nil {
		t.Fatal("pushed out of the app's data")
	}
	_ = c.onLoop(func() error {
		a := c.appByID("com.example.devfiles")
		rec := readInstallRecord(a.M.dir)
		rec.Store = "kario"
		raw, _ := json.Marshal(rec)
		return os.WriteFile(filepath.Join(a.M.dir, installRecordFile), raw, 0o644)
	})
	_, err := pull("com.example.devfiles:files/seed.json")
	wantRefused(t, "a store app's data", err, "not installed in developer mode")
}

// The system's log: the journal of the phone's services where systemd
// keeps one, the services' logs in the run directory where it does not,
// and the Shell's own; never an app's log. Followed, it goes on until
// the gate closes.
func TestDevSyslog(t *testing.T) {
	if !alone(t) {
		return
	}
	c := devOpen(t)
	run := c.cfg.RunDir
	_ = os.WriteFile(filepath.Join(run, "mediad.log"), []byte("mediad up\nmediad camera vivid\n"), 0o600)
	_ = os.WriteFile(filepath.Join(run, "com.example.chat.log"), []byte("app: the user wrote a secret\n"), 0o600)
	shellLog, _ := os.OpenFile(filepath.Join(run, "shell.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	defer shellLog.Close()
	fmt.Fprintln(shellLog, "shell: 10:00:00 first")
	fmt.Fprintln(shellLog, "shell: 10:00:01 second")
	syslog := func(req devctl.Request) (string, error) {
		req.Op = devctl.OpSyslog
		var out bytes.Buffer
		err := devctl.Call(c.DevLinkPath(), req, nil, &out, nil)
		return out.String(), err
	}
	out, err := syslog(devctl.Request{})
	if err != nil || !strings.Contains(out, "mediad: mediad camera vivid\n") || !strings.Contains(out, "shell: 10:00:01 second\n") || strings.Contains(out, "secret") {
		t.Fatalf("the developer machine's syslog: %v\n%s", err, out)
	}
	if out, err = syslog(devctl.Request{Unit: "shell", Lines: 1}); err != nil || out != "shell: 10:00:01 second\n" {
		t.Fatalf("-u shell -n 1: %v %q", err, out)
	}
	if out, err = syslog(devctl.Request{Unit: "phone-mediad.service", Lines: 1}); err != nil || out != "mediad: mediad camera vivid\n" {
		t.Fatalf("-u phone-mediad.service -n 1: %v %q", err, out)
	}
	if _, err = syslog(devctl.Request{Unit: "com.example.chat"}); err == nil {
		t.Fatal("syslog handed out an app's log")
	}

	journal := filepath.Join(t.TempDir(), "journalctl")
	_ = os.WriteFile(journal, []byte("#!/bin/sh\necho \"journal $*\"\n"), 0o755)
	_ = c.onLoop(func() error { c.dev.journal = journal; return nil })
	if out, err = syslog(devctl.Request{Lines: 5}); err != nil || !strings.HasPrefix(out, "journal --no-pager -o short-iso -b -u phone-* -n 5\n") || strings.Contains(out, "mediad up") || !strings.Contains(out, "second") {
		t.Fatalf("the device's syslog: %v\n%s", err, out)
	}
	if out, err = syslog(devctl.Request{Unit: "iwd"}); err != nil || out != "journal --no-pager -o short-iso -b -u iwd\n" {
		t.Fatalf("-u iwd: %v %q", err, out)
	}

	pr, pw := io.Pipe()
	lines := bufio.NewReader(pr)
	done := make(chan error, 1)
	go func() {
		done <- devctl.Call(c.DevLinkPath(), devctl.Request{Op: devctl.OpSyslog, Unit: "shell", Follow: true, Lines: 1}, nil, pw, nil)
		pw.Close()
	}()
	next := func() string {
		got := make(chan string, 1)
		go func() { l, _ := lines.ReadString('\n'); got <- l }()
		select {
		case l := <-got:
			return l
		case <-time.After(5 * time.Second):
			t.Fatal("nothing more from syslog -f")
			return ""
		}
	}
	if l := next(); l != "shell: 10:00:01 second\n" {
		t.Fatalf("followed: %q", l)
	}
	fmt.Fprintln(shellLog, "shell: 10:00:02 third")
	if l := next(); l != "shell: 10:00:02 third\n" {
		t.Fatalf("followed on: %q", l)
	}
	_ = c.onLoop(func() error { c.settings.DeveloperMode = false; return nil })
	next()
	if l := next(); !strings.Contains(l, errDevModeOff) {
		t.Fatalf("followed past the gate: %q", l)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("syslog -f went on with developer mode off")
	}
}

// Forwarding rides SSH on the cable, which its firewall lets through in
// developer mode only; it is held while the gate is open and ends with it.
func TestDevForward(t *testing.T) {
	if !alone(t) {
		return
	}
	c := devOpen(t)
	if out, err := devCall(c, devctl.Request{Op: devctl.OpForward, Port: 80, Reverse: true}, nil); err == nil {
		t.Fatalf("a reverse forward on a system port: %q", out)
	}
	_ = c.onLoop(func() error { c.usb.link = netfw.Link{Name: c.usb.link.Name, TCP: []uint16{80}}; return nil })
	_, err := devCall(c, devctl.Request{Op: devctl.OpForward, Port: 8080}, nil)
	wantRefused(t, "a forward the cable's firewall does not let through", err, "does not let SSH")
	_ = c.onLoop(func() error { c.usbFirewall(true); return nil })

	pr, pw := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- devctl.Call(c.DevLinkPath(), devctl.Request{Op: devctl.OpForward, Port: 8080, Reverse: true}, pr, &out, nil)
	}()
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("the forward ended at once: %v %q", err, out.String())
	default:
	}
	_ = c.onLoop(func() error {
		err := c.settingsSet("developer_mode", 0.0)
		if slices.Contains(c.usb.link.TCP, 22) {
			t.Error("port 22 open with developer mode off")
		}
		return err
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the forward went on with developer mode off")
	}
	pw.Close()
	if !strings.Contains(out.String(), "forwarding 127.0.0.1:8080 on the phone to the computer") || !strings.Contains(out.String(), errDevModeOff) {
		t.Fatalf("forward said %q", out.String())
	}

	_ = c.onLoop(func() error { c.settings.DeveloperMode = true; c.usbFirewall(true); return nil })
	pr, pw = io.Pipe()
	go func() {
		done <- devctl.Call(c.DevLinkPath(), devctl.Request{Op: devctl.OpForward, Port: 8080}, pr, io.Discard, nil)
	}()
	time.Sleep(200 * time.Millisecond)
	pw.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the forward went on after the computer went")
	}
}

// A restart goes to systemd with the reason the bootloader reads; with an
// update waiting, into it; never on a developer machine.
func TestDevReboot(t *testing.T) {
	if !alone(t) {
		return
	}
	c := devOpen(t)
	_, err := devCall(c, devctl.Request{Op: devctl.OpReboot}, nil)
	wantRefused(t, "restarting the developer machine", err, "not a phone")
	got := make(chan string, 2)
	_ = c.onLoop(func() error {
		c.dev.reboot = func(target string) error { got <- target; return nil }
		return nil
	})
	if out, err := devCall(c, devctl.Request{Op: devctl.OpReboot, Target: devctl.RebootBootloader}, nil); err != nil || !strings.Contains(out, "restarting into bootloader") {
		t.Fatalf("reboot bootloader: %q %v", out, err)
	}
	select {
	case target := <-got:
		if target != devctl.RebootBootloader {
			t.Fatalf("restarted into %q", target)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no restart")
	}
	restarted := make(chan bool, 1)
	_ = c.onLoop(func() error {
		c.update.init()
		c.update.state, c.update.restart = "installed", func() { restarted <- true }
		return nil
	})
	if out, err := devCall(c, devctl.Request{Op: devctl.OpReboot}, nil); err != nil || !strings.Contains(out, "system update installed") {
		t.Fatalf("reboot with an update waiting: %q %v", out, err)
	}
	select {
	case <-restarted:
	case <-time.After(3 * time.Second):
		t.Fatal("did not restart into the update")
	}
	if args := []string{"reboot", "--reboot-argument=recovery"}; !slices.Equal(systemdArgs(devctl.RebootRecovery), args) {
		t.Fatalf("systemctl %v", systemdArgs(devctl.RebootRecovery))
	}
}

// A sideloaded bundle goes through the update path: RAUC's progress, the
// slot it went into, and RAUC's refusal of a bundle it does not trust.
func TestDevUpdate(t *testing.T) {
	if !alone(t) {
		return
	}
	c := devOpen(t)
	slots := t.TempDir()
	update := func(body string) (string, error) {
		var out bytes.Buffer
		err := devctl.Call(c.DevLinkPath(), devctl.Request{Op: devctl.OpUpdate, Size: int64(len(body))}, strings.NewReader(body), &out, nil)
		return out.String(), err
	}
	_, err := update("bundle")
	wantRefused(t, "an update without A/B slots", err, "no A/B system slots")
	_ = os.WriteFile(filepath.Join(slots, "boot"), []byte("slot=a\ngood=a\ntries=0\nbooted=a\n"), 0o644)
	var installed []string
	_ = c.onLoop(func() error {
		u := &c.update
		u.init()
		u.slots = slots
		u.info = func(path string) string {
			raw, _ := os.ReadFile(path)
			return map[bool]string{true: "9.1.0"}[strings.HasPrefix(string(raw), "signed")]
		}
		u.install = func(path string, line func(string)) (string, error) {
			raw, err := os.ReadFile(path)
			installed = append(installed, string(raw))
			if err != nil || !strings.HasPrefix(string(raw), "signed") {
				return "Failed to verify bundle: signature verification failed", fmt.Errorf("exit status 1")
			}
			line(" 40% Copying image to rootfs.1")
			time.Sleep(devUpdateEvery + 200*time.Millisecond)
			_ = os.WriteFile(filepath.Join(slots, "boot"), []byte("slot=b\ngood=a\ntries=3\nbooted=a\n"), 0o644)
			return "Installing done.", nil
		}
		return nil
	})
	out, err := update("signed bundle")
	if err != nil || !strings.Contains(out, " 40% installing\n") || !strings.Contains(out, "installed 9.1.0 into slot b") || !strings.Contains(out, "back to slot a") {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if len(installed) != 1 || installed[0] != "signed bundle" {
		t.Fatalf("RAUC got %q", installed)
	}
	_ = c.onLoop(func() error {
		if c.update.state != "installed" || c.update.offer != nil {
			t.Errorf("after the update: %s, offer %+v", c.update.state, c.update.offer)
		}
		c.update.state = "idle"
		return nil
	})
	_, err = update("tampered bundle")
	wantRefused(t, "an unsigned bundle", err, "signature verification failed")
	if _, err := os.Stat(filepath.Join(c.cfg.StateDir, "shell", "sideload.raucb")); !os.IsNotExist(err) {
		t.Fatalf("the bundle stayed on the phone: %v", err)
	}
}

// A screenshot is drawn by a renderer of its own from the frame on the
// panel and comes back as a PNG; a secure screen goes to it masked, so
// neither a pairing code nor a payment's prompt reaches the picture.
func TestDevScreenshot(t *testing.T) {
	c := devOpen(t)
	_, _, err := devFetch(c, devctl.Request{Op: devctl.OpScreenshot})
	wantRefused(t, "a screenshot without a renderer", err, "draws no pixels")
	dir := t.TempDir()
	frames := filepath.Join(dir, "frames")
	renderer := filepath.Join(dir, "renderer")
	script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run='^TestFakeSnapRenderer$' -- \"$@\"\n", os.Args[0])
	if err := os.WriteFile(renderer, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PHONE_FAKE_FRAMES", frames)
	dev, _ := DeviceByID("bar-t9")
	if err := c.onLoop(func() error { _, err := StartGUI(c, renderer, dev); return err }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.onLoop(func() error { c.gui.Close(); return nil }) })
	lastFrame := func() Frame {
		t.Helper()
		raw, err := os.ReadFile(frames)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		var f Frame
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &f); err != nil {
			t.Fatal(err)
		}
		return f
	}

	shot, note, err := devFetch(c, devctl.Request{Op: devctl.OpScreenshot})
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := png.DecodeConfig(bytes.NewReader(shot)); err != nil || cfg.Width != 2 || note != "" {
		t.Fatalf("screenshot: %v %+v, note %q", err, cfg, note)
	}
	if f := lastFrame(); f.Status.Time == "" || f.Overlay != nil {
		t.Fatalf("the screenshot's frame: %+v", f)
	}

	for _, tc := range []struct {
		o      overlay
		why    string
		secret string
	}{
		{&confirmOverlay{text: "Bluetooth pairing with “Car”\nCode 482913\nIs it the same as on the other device?", trust: true, secret: true}, "a pairing code", "482913"},
		{&authOverlay{prompt: "Pay 30.00 to Morning Coffee"}, "a trusted confirmation", "Morning Coffee"},
		{&lockPadOverlay{digits: "12"}, "a PIN entry", ""},
	} {
		_ = c.onLoop(func() error {
			if a, ok := tc.o.(*authOverlay); ok {
				a.app = c.appByID("com.test.sys")
			}
			c.overlays = nil
			c.pushOverlay(tc.o)
			return nil
		})
		shot, note, err := devFetch(c, devctl.Request{Op: devctl.OpScreenshot})
		if err != nil || len(shot) == 0 || note != "masked: the screen shows "+tc.why {
			t.Fatalf("%T: %v, note %q", tc.o, err, note)
		}
		f := lastFrame()
		raw, _ := json.Marshal(f)
		if f.Overlay != nil || len(f.Body) != 1 || f.Body[0].Image == nil || !f.Body[0].Image.Secure || tc.secret != "" && strings.Contains(string(raw), tc.secret) {
			t.Fatalf("%T reached the screenshot's renderer: %s", tc.o, raw)
		}
	}
	_ = c.onLoop(func() error { c.overlays = nil; return nil })
}

// The bug report has the system's records and none of the user's: an
// app's log or a crash record's log tail stays on the phone.
func TestDevBugreport(t *testing.T) {
	if !alone(t) {
		return
	}
	c := devOpen(t)
	run := c.cfg.RunDir
	_ = os.WriteFile(filepath.Join(run, "shell.log"), []byte("shell: 10:00:00 started\n"), 0o600)
	_ = os.WriteFile(filepath.Join(run, "mediad.log"), []byte("mediad up\n"), 0o600)
	_ = os.WriteFile(filepath.Join(run, "com.example.chat.log"), []byte("the user wrote a secret\n"), 0o600)
	_ = c.onLoop(func() error {
		c.loadCrashes()
		c.crashes = append(c.crashes, CrashRecord{Time: time.Now(), App: "com.example.chat", Reason: "SIGSEGV", Log: []string{"the user wrote a secret"}})
		return nil
	})
	raw, note, err := devFetch(c, devctl.Request{Op: devctl.OpBugreport})
	if err != nil || !strings.Contains(note, "private data left out") {
		t.Fatalf("bugreport: %v %q", err, note)
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range z.File {
		r, _ := f.Open()
		body, _ := io.ReadAll(r)
		r.Close()
		files[f.Name] = string(body)
		if strings.Contains(string(body), "secret") {
			t.Errorf("%s has the user's text", f.Name)
		}
	}
	for _, name := range []string{"README.txt", "about.json", "sku.json", "apps.txt", "crashes.json", "shell.log", "renderer.log", "dmesg.txt", "rauc.txt", "services/mediad.log"} {
		if _, ok := files[name]; !ok {
			t.Errorf("no %s in %v", name, slices.Sorted(maps.Keys(files)))
		}
	}
	if !strings.Contains(files["apps.txt"], "com.test.sys\t-\tsystem") || !strings.Contains(files["crashes.json"], "SIGSEGV") ||
		!strings.Contains(files["shell.log"], "started") || !strings.Contains(files["README.txt"], "Wi-Fi passphrases") {
		t.Errorf("bug report: apps %q crashes %q", files["apps.txt"], files["crashes.json"])
	}
}

// TestFakeSnapRenderer is not a test: run as a child process (through a
// script that passes the renderer's arguments after --) it plays a
// headless renderer that keeps the frames it gets in $PHONE_FAKE_FRAMES
// and snaps a 2×2 PNG.
func TestFakeSnapRenderer(t *testing.T) {
	frames := os.Getenv("PHONE_FAKE_FRAMES")
	if frames == "" {
		t.Skip("helper process")
	}
	dir := ""
	for i, a := range os.Args {
		if a == "--snap-dir" && i+1 < len(os.Args) {
			dir = os.Args[i+1]
		}
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	sc.Scan()
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(map[string]any{"t": "ready", "proto": RendererProto, "features": []string{"snap"}, "renderer": map[string]string{"name": "fake-snap"}})
	for sc.Scan() {
		var m struct{ T, Name string }
		_ = json.Unmarshal(sc.Bytes(), &m)
		switch m.T {
		case "frame":
			if dir != "" {
				f, _ := os.OpenFile(frames, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
				_, _ = f.Write(append(sc.Bytes(), '\n'))
				f.Close()
			}
		case "snap":
			img := image.NewRGBA(image.Rect(0, 0, 2, 2))
			img.Set(0, 0, color.White)
			var buf bytes.Buffer
			_ = png.Encode(&buf, img)
			msg := ""
			if err := os.WriteFile(filepath.Join(dir, m.Name+".png"), buf.Bytes(), 0o644); err != nil {
				msg = err.Error()
			}
			_ = enc.Encode(map[string]any{"t": "snapped", "name": m.Name, "err": msg})
		case "quit":
			os.Exit(0)
		}
	}
	os.Exit(0)
}
