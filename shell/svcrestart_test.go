package shell

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/wire"
)

// answeringService answers every call on path with {"pid": pid} until stop.
func answeringService(t *testing.T, path string, pid int) (stop func()) {
	t.Helper()
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var conns []net.Conn
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			conns = append(conns, nc)
			go func(c *wire.Conn) {
				for {
					var req wire.RPC
					if c.Recv(&req) != nil {
						return
					}
					res, _ := json.Marshal(map[string]int{"pid": pid})
					_ = c.Send(wire.RPC{ID: req.ID, OK: true, Result: res})
				}
			}(wire.New(nc))
		}
	}()
	return func() {
		ln.Close()
		for _, c := range conns {
			c.Close()
		}
	}
}

// A service that restarted (telephonyd after a crash) answers the first
// call made after it: the connection to the old process is not used, so an
// emergency call placed right then goes out.
func TestServiceRestartFirstCall(t *testing.T) {
	c := loopCore(t, t.TempDir())
	path := filepath.Join(c.cfg.RunDir, "fakesvc.sock")
	stop := answeringService(t, path, 1)
	var got struct{ PID int }
	if err := c.broker.rpc("fakesvc", "status", nil, &got); err != nil || got.PID != 1 {
		t.Fatalf("first process: %v %+v", err, got)
	}
	stop()
	defer answeringService(t, path, 2)()
	// The old connection's reader sees the close.
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cl := c.broker.clients["fakesvc"]; cl == nil || cl.Lost() {
			break
		}
	}
	if err := c.broker.rpc("fakesvc", "status", nil, &got); err != nil || got.PID != 2 {
		t.Fatalf("first call to the restarted service: %v %+v", err, got)
	}
}
