package wire

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestActivatedHelper is the service side: it serves on the socket it was
// handed as fd 3, as systemd hands it.
func TestActivatedHelper(t *testing.T) {
	if os.Getenv("WIRE_ACTIVATED") == "" {
		t.Skip("helper process")
	}
	os.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	os.Setenv("LISTEN_FDS", "1")
	_ = Serve("activated", func(method string, _ json.RawMessage) (any, error) {
		return map[string]string{"method": method, "listen_fds": os.Getenv("LISTEN_FDS")}, nil
	})
}

func TestSocketActivation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svc.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ul := ln.(*net.UnixListener)
	ul.SetUnlinkOnClose(false) // systemd keeps the socket file
	f, _ := ul.File()
	cmd := exec.Command(os.Args[0], "-test.run", "TestActivatedHelper")
	cmd.Env = append(os.Environ(), "WIRE_ACTIVATED=1")
	cmd.ExtraFiles = []*os.File{f}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	ln.Close()
	var cl *Client
	for i := 0; i < 50 && cl == nil; i++ {
		cl, _ = Dial(path)
		time.Sleep(20 * time.Millisecond)
	}
	if cl == nil {
		t.Fatal("no service on the activated socket")
	}
	defer cl.Close()
	var out map[string]string
	if err := cl.Call("hello", nil, &out); err != nil {
		t.Fatal(err)
	}
	if out["method"] != "hello" || out["listen_fds"] != "" {
		t.Fatalf("reply %v (the activation variables must not leak to children)", out)
	}
}
