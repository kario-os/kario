package wire

import (
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A line that is not a request is answered with an error, and the requests
// before and after it are answered too: the service does not hang up on
// them (found by the services' RPC fuzzing, wire/rpcfuzz).
func TestMalformedRequestIsAnswered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	ln := make(chan struct{})
	go func() {
		close(ln)
		_ = Serve(path, func(method string, _ json.RawMessage) (any, error) {
			if method == "slow" {
				time.Sleep(50 * time.Millisecond)
			}
			return map[string]string{"method": method}, nil
		})
	}()
	<-ln
	var nc net.Conn
	var err error
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if nc, err = net.Dial("unix", path); err == nil || time.Now().After(end) {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	c := New(nc)
	defer c.Close()
	if _, err := nc.Write([]byte(`{"id":1,"method":"slow"}` + "\n" + `{"id":2,"method":7}` + "\nnot json\n" + `{"id":3,"method":"fast"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	got := map[int]RPC{}
	nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	for len(got) < 4 {
		var r RPC
		if err := c.Recv(&r); err != nil {
			t.Fatalf("after %d answers: %v (%v)", len(got), err, got)
		}
		got[r.ID] = r
	}
	if !got[1].OK || !got[3].OK || got[2].OK || !strings.Contains(got[2].Err, "malformed") || got[0].OK || got[0].Err == "" {
		t.Fatalf("answers %+v", got)
	}
}
