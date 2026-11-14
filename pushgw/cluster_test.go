package pushgw

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/push"
)

type node struct {
	g   *Gateway
	srv *httptest.Server
}

// start runs an instance whose URL goes in its own addr file, peered with
// the others' files.
func start(t *testing.T, dir, name string, peers ...string) *node {
	t.Helper()
	g := &Gateway{devices: map[string]*device{}, heartbeat: time.Second, node: name}
	var files []string
	for _, p := range peers {
		files = append(files, filepath.Join(dir, p+".addr"))
	}
	n := &node{g: g}
	n.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { g.Routes().ServeHTTP(w, r) }))
	t.Cleanup(n.srv.Close)
	if err := os.WriteFile(filepath.Join(dir, name+".addr"), []byte(n.srv.URL+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.Join([]byte("0123456789abcdef0123456789abcdef"), &http.Client{Timeout: 5 * time.Second}, files)
	return n
}

func post(t *testing.T, url string, body, out any) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("%s: not within 5 s", what)
}

func queued(g *Gateway, dev string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if d := g.devices[dev]; d != nil {
		return len(d.Queue)
	}
	return -1
}

func TestCluster(t *testing.T) {
	dir := t.TempDir()
	a, b := start(t, dir, "a", "b"), start(t, dir, "b", "a")
	dev := map[string]string{"device": "dev-1", "secret": "0123456789abcdef", "app": "com.example.chat"}
	var tok struct{ Token string }
	if code := post(t, a.srv.URL+"/v1/device/token", dev, &tok); code != http.StatusOK {
		t.Fatalf("token at a: %d", code)
	}
	eventually(t, "b learns the device", func() bool { return queued(b.g, "dev-1") == 0 })

	// The device holds its connection to a; the app server posts to b.
	raw, _ := json.Marshal(map[string]any{"device": "dev-1", "secret": "0123456789abcdef"})
	resp, err := http.Post(a.srv.URL+"/v1/device/connect", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := make(chan line, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			var l line
			if json.Unmarshal(sc.Bytes(), &l) == nil && l.T == "msg" {
				lines <- l
			}
		}
	}()
	var sent struct{ ID string }
	if code := post(t, b.srv.URL+"/v1/push", push.Send{Token: tok.Token, Title: "sent through b"}, &sent); code != http.StatusOK {
		t.Fatalf("a's token at b: %d", code)
	}
	select {
	case l := <-lines:
		if l.Msg.ID != sent.ID || l.Msg.Title != "sent through b" {
			t.Fatalf("delivered %+v", l.Msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the push posted to b never reached the device on a")
	}
	ack := map[string]string{"device": "dev-1", "secret": "0123456789abcdef", "id": sent.ID}
	if code := post(t, a.srv.URL+"/v1/device/ack", ack, nil); code != http.StatusOK {
		t.Fatalf("ack at a: %d", code)
	}
	eventually(t, "the ack reaches b", func() bool { return queued(b.g, "dev-1") == 0 && queued(a.g, "dev-1") == 0 })
	if b.g.syncFromPeers(); queued(b.g, "dev-1") != 0 {
		t.Fatal("merging a's snapshot brought the acknowledged message back")
	}

	// a dies; a message waits at b; a new instance in a's place catches up
	// from b before the device comes back to it.
	a.srv.CloseClientConnections()
	a.srv.Close()
	if code := post(t, b.srv.URL+"/v1/push", push.Send{Token: tok.Token, Title: "while a is away"}, nil); code != http.StatusOK {
		t.Fatalf("push while a is down: %d", code)
	}
	revived := start(t, dir, "a", "b")
	if n := queued(revived.g, "dev-1"); n != 1 {
		t.Fatalf("the restarted instance has %d queued, want 1", n)
	}
	if code := post(t, revived.srv.URL+"/v1/push", push.Send{Token: tok.Token, Title: "the token still works"}, nil); code != http.StatusOK {
		t.Fatalf("the old token at the restarted instance: %d", code)
	}
	eventually(t, "b gets the restarted instance's message", func() bool { return queued(b.g, "dev-1") == 2 })

	// A request not signed with the cluster key changes nothing.
	body, _ := json.Marshal([]op{{Kind: "installs", Device: "dev-1", App: "com.example.chat", N: 9}})
	if r, err := http.Post(b.srv.URL+"/v1/peer/ops", "application/json", bytes.NewReader(body)); err != nil || r.StatusCode != http.StatusForbidden {
		t.Fatalf("unsigned peer ops: %v %v", err, r.StatusCode)
	}
	if code := post(t, b.srv.URL+"/v1/push", push.Send{Token: tok.Token, Title: "x"}, nil); code != http.StatusOK {
		t.Fatalf("token revoked by an unsigned request: %d", code)
	}
}

// A message on the connection keeps the NAT mapping alive as a ping would:
// the next ping comes a full interval after it, so the radio wakes once.
func TestTrafficDefersPing(t *testing.T) {
	g := &Gateway{key: []byte("0123456789abcdef0123456789abcdef"), devices: map[string]*device{}, heartbeat: 400 * time.Millisecond, node: "n"}
	srv := httptest.NewServer(g.Routes())
	defer srv.Close()
	dev := map[string]string{"device": "dev-1", "secret": "0123456789abcdef", "app": "com.example.chat"}
	var tok struct{ Token string }
	post(t, srv.URL+"/v1/device/token", dev, &tok)
	raw, _ := json.Marshal(map[string]any{"device": "dev-1", "secret": "0123456789abcdef"})
	resp, err := http.Post(srv.URL+"/v1/device/connect", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	start := time.Now()
	type ev struct {
		t  string
		at time.Duration
	}
	evs := make(chan ev, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			var l line
			if json.Unmarshal(sc.Bytes(), &l) == nil {
				evs <- ev{l.T, time.Since(start)}
			}
		}
	}()
	time.Sleep(300 * time.Millisecond)
	post(t, srv.URL+"/v1/push", push.Send{Token: tok.Token, Title: "x"}, nil)
	var msgAt time.Duration
	for e := range evs {
		switch e.t {
		case "msg":
			msgAt = e.at
		case "ping":
			if msgAt == 0 {
				t.Fatalf("ping at %v before the message", e.at)
			}
			if gap := e.at - msgAt; gap < 350*time.Millisecond {
				t.Fatalf("ping %v after the message, want a full interval", gap)
			}
			return
		}
	}
}
