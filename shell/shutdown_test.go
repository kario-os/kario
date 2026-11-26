package shell

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/jsrt/nativetest"
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
