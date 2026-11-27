package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/jsrt/nativetest"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// The Shell stops the host with SIGTERM (shutting down) or by closing its
// connection (ending it for low memory, an update, a restart): the
// runtimes it started finish their storage writes first, instead of dying
// with it (Pdeathsig) while the slow fsync is under way.
func TestTerminatedHostLetsRuntimesSave(t *testing.T) {
	real := jsrtBinary(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "phone-app-miniapp")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	slow := fmt.Sprintf("#!/bin/sh\nexec /usr/bin/env LD_PRELOAD=%s SLOWFS_DELAY_MS=1500 %s \"$@\"\n", nativetest.SlowFS(t), real)
	if err := os.WriteFile(filepath.Join(dir, jsrt.Program), []byte(slow), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Run("SIGTERM", func(t *testing.T) {
		hostEndsAfterSave(t, exe, func(cmd *exec.Cmd, _ *wire.Conn) error { return cmd.Process.Signal(syscall.SIGTERM) })
	})
	t.Run("connection closed", func(t *testing.T) {
		hostEndsAfterSave(t, exe, func(_ *exec.Cmd, conn *wire.Conn) error { return conn.CloseWrite() })
	})
}

// hostEndsAfterSave starts the host, adds a note in a mini-program, ends
// the host with end while the note's write is under way, and checks the
// note is on disk once the host is gone.
func hostEndsAfterSave(t *testing.T, exe string, end func(*exec.Cmd, *wire.Conn) error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	parent, child := os.NewFile(uintptr(fds[0]), "shell"), os.NewFile(uintptr(fds[1]), "app")
	data := t.TempDir()
	cmd := exec.Command(exe)
	cmd.ExtraFiles = []*os.File{child}
	cmd.Env = []string{"PHONE_UI_FD=3", "PHONE_APP_ID=" + testHostID, "PHONE_DATA_DIR=" + data, "PHONE_LANG=en",
		"HOME=" + data, "PATH=" + os.Getenv("PATH")}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child.Close()
	conn := wire.New(parent)
	defer func() {
		conn.Close()
		_ = cmd.Process.Kill()
	}()
	msgs := make(chan ui.Msg, 256)
	go func() {
		for {
			var m ui.Msg
			if conn.Recv(&m) != nil {
				close(msgs)
				return
			}
			msgs <- m
		}
	}()
	next := func(what string, match func(ui.Msg) bool) {
		t.Helper()
		timeout := time.After(10 * time.Second)
		for {
			select {
			case m, ok := <-msgs:
				if !ok {
					t.Fatalf("the host ended waiting for %s", what)
				}
				if match(m) {
					return
				}
			case <-timeout:
				t.Fatalf("no %s", what)
			}
		}
	}
	next("hello", func(m ui.Msg) bool { return m.T == ui.MsgHello })
	_ = conn.Send(ui.Msg{T: ui.MsgStart})
	next("the home page", func(m ui.Msg) bool { return m.T == ui.MsgReset })
	var page string
	_ = conn.Send(ui.Msg{T: ui.MsgAction, ScreenID: "home", Node: "p:notes", Action: ui.ActionOpen})
	next("the notes page", func(m ui.Msg) bool {
		if m.T == ui.MsgPush && strings.HasPrefix(m.Screen.ID, "mp:notes:index:") {
			page = m.Screen.ID
			return true
		}
		return false
	})
	_ = conn.Send(ui.Msg{T: ui.MsgAction, ScreenID: page, Node: "new", Action: "add", Value: "buy milk"})
	next("the note on the page", func(m ui.Msg) bool {
		raw, _ := json.Marshal(m)
		return strings.Contains(string(raw), "buy milk")
	})
	if err := end(cmd, conn); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the host did not end")
	}
	var stored []string
	_ = filepath.WalkDir(data, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == jsrt.StorageFile {
			raw, _ := os.ReadFile(p)
			stored = append(stored, string(raw))
		}
		return nil
	})
	if len(stored) != 1 || !strings.Contains(stored[0], "buy milk") {
		t.Fatalf("the note's storage after the host ended: %q", stored)
	}
}
