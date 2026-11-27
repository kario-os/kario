package pushgw

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/push"
)

func TestHooks(t *testing.T) {
	g := &Gateway{key: []byte("0123456789abcdef0123456789abcdef"), devices: map[string]*device{}}
	var tok struct{ Token string }
	_ = json.Unmarshal(do(t, g.token, map[string]string{"device": "dev-1", "secret": "0123456789abcdef", "app": "com.example.chat"}).Body.Bytes(), &tok)
	authorized, limited := 0, 0
	g.Authorize = func(r *http.Request, device, app string) error {
		authorized++
		switch {
		case device != "dev-1" || app != "com.example.chat":
			t.Errorf("authorize %s %s", device, app)
		case r.Header.Get("X-Key") == "":
			return &StatusError{Code: http.StatusUnauthorized, Err: errors.New("no key")}
		case r.Header.Get("X-Key") != "chat":
			return errors.New("key of another app")
		}
		return nil
	}
	g.Limit = func(r *http.Request, device, app, kind string) error {
		if limited++; limited > 2 {
			return &StatusError{Code: http.StatusTooManyRequests, RetryAfter: 1500 * time.Millisecond}
		}
		return nil
	}
	send := func(key string, s push.Send) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(s)
		req := httptest.NewRequest(http.MethodPost, "/v1/push", bytes.NewReader(raw))
		if key != "" {
			req.Header.Set("X-Key", key)
		}
		rec := httptest.NewRecorder()
		g.push(rec, req)
		return rec
	}
	if code := send("chat", push.Send{Token: "forged", Title: "x"}).Code; code != http.StatusForbidden || authorized != 0 {
		t.Fatalf("forged token: %d, authorize asked %d times", code, authorized)
	}
	if code := send("", push.Send{Token: tok.Token, Title: "x"}).Code; code != http.StatusUnauthorized {
		t.Fatalf("no key: %d, want 401", code)
	}
	if code := send("wallet", push.Send{Token: tok.Token, Title: "x"}).Code; code != http.StatusForbidden {
		t.Fatalf("another app's key: %d, want 403", code)
	}
	if code := send("chat", push.Send{Token: tok.Token}).Code; code != http.StatusBadRequest || limited != 0 {
		t.Fatalf("empty push: %d, limit asked %d times", code, limited)
	}
	for i := range 2 {
		if code := send("chat", push.Send{Token: tok.Token, Title: "x"}).Code; code != http.StatusOK {
			t.Fatalf("push %d: %d", i, code)
		}
	}
	rec := send("chat", push.Send{Token: tok.Token, Title: "x"})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("over the limit: %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if n := len(g.devices["dev-1"].Queue); n != 2 {
		t.Fatalf("%d queued, want 2", n)
	}
}

// An instance a device does not belong on sends its requests on (307) and
// forwards pushes for it; the instance it belongs on takes those without
// asking its own hooks again.
func TestOwner(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	client := &http.Client{Timeout: 5 * time.Second}
	serve := func(g *Gateway) *httptest.Server {
		g.Join(key, client, nil)
		srv := httptest.NewServer(g.Routes())
		t.Cleanup(srv.Close)
		return srv
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	a := &Gateway{devices: map[string]*device{}, heartbeat: time.Second, node: "a"}
	b := &Gateway{devices: map[string]*device{}, heartbeat: time.Second, node: "b"}
	as, bs := serve(a), serve(b)
	a.Owner = func(device string) []string {
		if device == "dev-b" {
			return []string{bs.URL}
		}
		return nil
	}
	dev := map[string]string{"device": "dev-b", "secret": "0123456789abcdef", "app": "com.example.chat"}
	raw, _ := json.Marshal(dev)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Post(as.URL+"/v1/device/token", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || resp.Header.Get("Location") != bs.URL+"/v1/device/token" {
		t.Fatalf("token at the wrong instance: %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if a.devices["dev-b"] != nil {
		t.Fatal("the wrong instance trusted the device on first use")
	}
	var tok struct{ Token string }
	if code := post(t, as.URL+"/v1/device/token", dev, &tok); code != http.StatusOK || tok.Token == "" {
		t.Fatalf("token through the redirect: %d", code)
	}
	raw, _ = json.Marshal(map[string]any{"device": "dev-b", "secret": "0123456789abcdef"})
	stream, err := http.Post(as.URL+"/v1/device/connect", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if !strings.HasPrefix(bs.URL, "http://"+stream.Request.URL.Host) {
		t.Fatalf("connected to %s, want %s", stream.Request.URL.Host, bs.URL)
	}
	msgs := make(chan push.Message, 4)
	go func() {
		sc := bufio.NewScanner(stream.Body)
		for sc.Scan() {
			var l line
			if json.Unmarshal(sc.Bytes(), &l) == nil && l.T == "msg" {
				msgs <- *l.Msg
			}
		}
	}()

	b.Authorize = func(*http.Request, string, string) error { return errors.New("not at b") }
	a.Owner = func(device string) []string { return []string{dead.URL, bs.URL} }
	var sent struct {
		ID     string
		Online bool
	}
	if code := post(t, as.URL+"/v1/push", push.Send{Token: tok.Token, Title: "forwarded by a"}, &sent); code != http.StatusOK || !sent.Online {
		t.Fatalf("push forwarded past an unreachable instance: %d %+v", code, sent)
	}
	select {
	case m := <-msgs:
		if m.ID != sent.ID || m.Title != "forwarded by a" {
			t.Fatalf("delivered %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the forwarded push never reached the device")
	}
	if code := post(t, bs.URL+"/v1/push", push.Send{Token: tok.Token, Title: "x"}, nil); code != http.StatusForbidden {
		t.Fatalf("b's own Authorize: %d", code)
	}

	b.Owner = func(string) []string { return []string{as.URL} }
	if code := post(t, as.URL+"/v1/push", push.Send{Token: tok.Token, Title: "x"}, nil); code != http.StatusMisdirectedRequest {
		t.Fatalf("instances that disagree: %d, want 421", code)
	}
	body, _ := json.Marshal(push.Send{Token: tok.Token, Title: "x"})
	if r, err := http.Post(bs.URL+"/v1/peer/push", "application/json", bytes.NewReader(body)); err != nil || r.StatusCode != http.StatusForbidden {
		t.Fatalf("unsigned forwarded push: %v %v", err, r.StatusCode)
	}
}

func TestListenTLS(t *testing.T) {
	dir := t.TempDir()
	ln, url, err := Listen("127.0.0.1:0", dir, false)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{key: []byte("0123456789abcdef0123456789abcdef"), devices: map[string]*device{}}
	srv := &http.Server{Handler: g.Routes()}
	go srv.Serve(ln)
	defer srv.Close()
	if !strings.HasPrefix(url, "https://127.0.0.1:") {
		t.Fatalf("url %s", url)
	}
	client, _ := push.Client(push.CertPath(dir), 5*time.Second)
	raw, _ := json.Marshal(map[string]string{"device": "dev-1", "secret": "0123456789abcdef", "app": "com.example.chat"})
	resp, err := client.Post(url+"/v1/device/token", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token over the pinned TLS: %d", resp.StatusCode)
	}
	if _, err := http.Post(url+"/v1/device/token", "application/json", bytes.NewReader(raw)); err == nil {
		t.Fatal("a client without the pin got through")
	}
}
