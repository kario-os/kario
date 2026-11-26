package shell

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// fakeV1Form is a phone.ui/1 made up for the tests (there is no major 1):
// its toast is "notice" with the text in "body", its action names the page
// in "page" rather than "screen_id", and resync is "resend". fakeV1 is the
// Shell speaking it; an SDK is given the form itself (ui.EnvTestForms).
var fakeV1Form = ui.ProtoForm{Major: 1, Rules: []ui.ProtoRule{
	{T: ui.MsgToast, As: "notice", Fields: map[string]string{"text": "body"}},
	{T: ui.MsgResync, As: "resend"},
	{Fields: map[string]string{"screen_id": "page"}},
}}

var fakeV1 = protoVersion{
	major:   1,
	changes: []string{"toast is notice, its text in body", "action names the page in page", "resync is resend"},
	in:      fakeV1Form.FromMajor,
	out:     fakeV1Form.ToMajor,
}

// transition: the Shell's own major 0, and the made-up 1 beside it.
var transition = []protoVersion{{major: ui.ProtoMajor}, fakeV1}

// fakeV1App plays an app of the made-up phone.ui/1 (offering 0 as well
// with both): its page's title says what it saw.
func fakeV1App(f *fakeConn, both bool) int {
	hello := map[string]any{"t": "hello", "app": os.Getenv("PHONE_APP_ID"), "proto": "phone.ui/1"}
	if both {
		hello["proto"], hello["protos"] = ui.Proto, []string{"phone.ui/1", ui.Proto}
	}
	if f.send(hello) != nil {
		return 2
	}
	page := func(t, title string) {
		_ = f.send(map[string]any{"t": t, "screen": ui.Screen{ID: "main", Title: title, Body: []ui.Node{{Kind: ui.KindText, Text: title}}}})
	}
	for {
		line, err := f.rd.ReadBytes('\n')
		if err != nil {
			return 0
		}
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			return 3
		}
		switch m["t"] {
		case ui.MsgStart:
			page(ui.MsgReset, "start said "+m["proto"].(string))
			_ = f.send(map[string]any{"t": "toast", "text": "a toast phone.ui/1 does not have"})
		case ui.MsgPing:
			_ = f.send(map[string]any{"t": ui.MsgPong, "value": m["value"]})
		case ui.MsgAction:
			_, old := m["screen_id"]
			page(ui.MsgUpdate, "action on "+m["page"].(string)+" screen_id "+strconv.FormatBool(old))
			_ = f.send(map[string]any{"t": "notice", "body": "noticed " + m["page"].(string)})
		case "resend":
			page(ui.MsgReset, "resent")
		}
	}
}

// protoCore is a Shell with the fake apps test.v1 and test.v1both, its
// event loop on a goroutine of its own.
func protoCore(t *testing.T) *Core {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("the fake apps run without phone-sandbox, which root refuses")
	}
	root := t.TempDir()
	apps, bin := filepath.Join(root, "apps"), filepath.Join(root, "bin")
	run, err := os.MkdirTemp("", "protover-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	script := "#!/bin/sh\nexec " + strconv.Quote(os.Args[0]) + " -test.run='^TestFakeApp$'\n"
	_ = os.MkdirAll(bin, 0o755)
	if err := os.WriteFile(filepath.Join(bin, fakeAppExec), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"test.v1", "test.v1both"} {
		dir := filepath.Join(apps, id)
		_ = os.MkdirAll(dir, 0o755)
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id":"`+id+`","name":"`+id+`","exec":"`+fakeAppExec+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := New(Config{W: 40, H: 24, AppsDir: apps, AppBinDir: bin, RunDir: run, StateDir: filepath.Join(root, "state"),
		Log: io.Discard, FreezeAfter: -1, AppEnv: []string{fakeAppEnv + "=1"}})
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

// until polls the Shell's state on its loop.
func until(t *testing.T, c *Core, what string, f func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var ok bool
		_ = c.onLoop(func() error { ok = f(); return nil })
		if ok {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("no %s", what)
		}
	}
}

func title(a *App) string {
	if v := a.top(); v != nil {
		return v.S.Title
	}
	return ""
}

// During a transition the Shell speaks two majors: an app of the older
// one (here a made-up phone.ui/1, there being none) is told so in start,
// its messages turn into the Shell's own form and the Shell's into its
// (an action's page, a notice for a toast), a message of the Shell's own
// form it should not send is dropped as malformed; an app offering both
// gets the newest. With one major only, the same app is refused.
func TestProtoTransition(t *testing.T) {
	if !alone(t) {
		return
	}
	c := protoCore(t)
	c.protos = transition
	for _, id := range []string{"test.v1", "test.v1both"} {
		var a *App
		_ = c.onLoop(func() error { a = c.appByID(id); c.Launch(a, ""); return nil })
		until(t, c, id+"'s page", func() bool { return title(a) == "start said phone.ui/1" })
		_ = c.onLoop(func() error {
			if a.proto == nil || a.proto.major != 1 || c.spoken(a) != "phone.ui/1" {
				t.Errorf("%s speaks %s", id, c.spoken(a))
			}
			a.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "open"})
			return nil
		})
		until(t, c, id+"'s answer to an action", func() bool { return title(a) == "action on main screen_id false" })
		until(t, c, id+"'s notice as a toast", func() bool { return c.toast != nil && strings.Contains(c.toast.body, "noticed main") })
		until(t, c, id+"'s toast dropped as malformed", func() bool { return a.guard.bad == 1 })
		_ = c.onLoop(func() error { c.kill(a); return nil })
		until(t, c, id+" ended", func() bool { return !a.running })
	}

	c.protos = nil
	var a *App
	_ = c.onLoop(func() error { a = c.appByID("test.v1"); c.Launch(a, ""); return nil })
	until(t, c, "the app refused", func() bool {
		return !a.running && strings.Contains(a.guard.violation, `speaks "phone.ui/1", the system phone.ui/0`)
	})
}

// A Shell taking over an app's connection keeps the older major it speaks
// (the connection's name says it): resync goes in its form and its
// messages turn; a major the new Shell does not speak closes it.
func TestProtoAdopted(t *testing.T) {
	c := protoCore(t)
	c.protos = transition
	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	pair := func() (*os.File, *os.File) {
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		return os.NewFile(uintptr(fds[0]), "app"), os.NewFile(uintptr(fds[1]), "shell")
	}
	app, shell := pair()
	defer app.Close()
	_ = c.onLoop(func() error {
		c.Adopt([]Inherited{{ID: "test.v1", Pid: sleep.Process.Pid, File: shell, Major: 1}})
		return nil
	})
	line := make([]byte, 64)
	n, err := app.Read(line)
	if err != nil || string(line[:n]) != `{"t":"resend"}`+"\n" {
		t.Fatalf("the adopted app got %q %v", line[:n], err)
	}
	if _, err := app.WriteString(`{"t":"reset","screen":{"id":"main","title":"again","body":[]}}` + "\n" + `{"t":"notice","body":"adopted"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	var a *App
	_ = c.onLoop(func() error { a = c.appByID("test.v1"); c.setForeground(a); return nil })
	until(t, c, "the adopted app's page", func() bool { return title(a) == "again" })
	_ = c.onLoop(func() error {
		if appConnName(a, sleep.Process.Pid) != connName("test.v1", sleep.Process.Pid)+".v1" {
			t.Errorf("its connection is %s", appConnName(a, sleep.Process.Pid))
		}
		return nil
	})

	other, shell2 := pair()
	defer other.Close()
	_ = c.onLoop(func() error {
		c.Adopt([]Inherited{{ID: "test.v1both", Pid: sleep.Process.Pid, File: shell2, Major: 2}})
		return nil
	})
	_ = other.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := other.Read(line); n != 0 || err != io.EOF {
		t.Fatalf("a connection of phone.ui/2: read %q %v, want it closed", line[:n], err)
	}
}
