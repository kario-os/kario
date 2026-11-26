package pushd

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/wire"
)

// Every heartbeat and hello on the gateway connection is a wake window the
// Shell hears of, with the connection's interval; a message is not (the
// Shell hears of it as the push itself).
func TestHeartbeatsAreWindows(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/device/connect" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		for _, l := range []string{`{"t":"hello","heartbeat_ms":60000}`, `{"t":"ping"}`, `{"t":"msg","msg":{"id":"m1","app":"a"}}`,
			`{"t":"hello","heartbeat_ms":120000}`, `{"t":"ping"}`} {
			fmt.Fprintln(w, l)
			w.(http.Flusher).Flush()
		}
	}))
	defer gw.Close()
	dir := t.TempDir()
	sock := filepath.Join(dir, svc.PushdSock)
	d := &daemon{gwFlag: gw.URL, stateDir: dir, keystore: filepath.Join(dir, "none.sock"), tokens: map[string]string{},
		http: gw.Client(), stream: gw.Client(), hb: push.NewHeartbeat()}
	hub, err := wire.ServeHub(sock, d.handle)
	if err != nil {
		t.Fatal(err)
	}
	d.hub = hub
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	c := wire.New(nc)
	var ok json.RawMessage
	if err := c.Send(wire.RPC{ID: 1, Method: wire.Subscribe}); err != nil || c.Recv(&ok) != nil {
		t.Fatal("subscribe", err)
	}
	_ = d.session(gw.URL)
	var got []string
	for len(got) < 5 {
		var ev struct {
			Event       string `json:"event"`
			HeartbeatMS int64  `json:"heartbeat_ms"`
		}
		if err := c.Recv(&ev); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s %d", ev.Event, ev.HeartbeatMS))
	}
	want := []string{"window 60000", "window 60000", "push 0", "window 120000", "window 120000"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events %v, want %v", got, want)
	}
}
