package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kario-os/kario/apps/chat/bot"
	"github.com/kario-os/kario/push"
)

type gateway struct {
	mu     sync.Mutex
	pushed []push.Send
	status int
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var p push.Send
	_ = json.NewDecoder(r.Body).Decode(&p)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status != 0 {
		w.WriteHeader(g.status)
		return
	}
	g.pushed = append(g.pushed, p)
}

func newServer(t *testing.T) (*server, *gateway) {
	gw := &gateway{}
	ts := httptest.NewServer(gw)
	t.Cleanup(ts.Close)
	return &server{gwFlag: ts.URL, tokens: map[string]string{}, keys: map[string]string{}, http: ts.Client()}, gw
}

func call(h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/", strings.NewReader(body)))
	return rec
}

func TestDeliverPushesAMessageFromAPeer(t *testing.T) {
	s, gw := newServer(t)
	if rec := call(s.login, `{"user":"me","push_token":"tok"}`); rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	if rec := call(s.deliver, `{"from":"lisi","to":"me","text":"msg 7"}`); rec.Code != http.StatusOK {
		t.Fatalf("deliver: %d %s", rec.Code, rec.Body)
	}
	if len(gw.pushed) != 1 {
		t.Fatalf("pushed %d, want 1", len(gw.pushed))
	}
	p := gw.pushed[0]
	if p.Token != "tok" || p.Title != bot.Peers["lisi"] || p.Body != "msg 7" || p.Thread != "lisi" {
		t.Errorf("push %+v", p)
	}
	var data map[string]string
	if err := json.Unmarshal([]byte(p.Data), &data); err != nil || data["type"] != "msg" || data["peer"] != "lisi" || data["text"] != "msg 7" {
		t.Errorf("data %q (%v)", p.Data, err)
	}
}

func TestDeliverSealsToThePushKey(t *testing.T) {
	s, gw := newServer(t)
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	call(s.login, `{"user":"me","push_token":"tok","push_key":"`+pub+`"}`)
	if rec := call(s.deliver, `{"from":"zhangsan","to":"me","text":"hi"}`); rec.Code != http.StatusOK {
		t.Fatalf("deliver: %d %s", rec.Code, rec.Body)
	}
	if len(gw.pushed) != 1 || !push.IsSealed(gw.pushed[0].Data) {
		t.Fatalf("pushed %+v", gw.pushed)
	}
	plain, err := push.Open(key, gw.pushed[0].Data)
	if err != nil || !strings.Contains(string(plain), `"text":"hi"`) {
		t.Errorf("opened %q (%v)", plain, err)
	}
}

func TestDeliverRefuses(t *testing.T) {
	s, gw := newServer(t)
	call(s.login, `{"user":"me","push_token":"tok"}`)
	for _, c := range []struct {
		body string
		code int
	}{
		{`{"from":"lisi","to":"nobody","text":"x"}`, http.StatusNotFound},
		{`{"from":"stranger","to":"me","text":"x"}`, http.StatusBadRequest},
		{`{"from":"lisi","to":"me","text":""}`, http.StatusBadRequest},
		{`not json`, http.StatusBadRequest},
	} {
		if rec := call(s.deliver, c.body); rec.Code != c.code {
			t.Errorf("%s: %d, want %d", c.body, rec.Code, c.code)
		}
	}
	if len(gw.pushed) != 0 {
		t.Errorf("pushed %+v", gw.pushed)
	}
	gw.status = http.StatusGone
	if rec := call(s.deliver, `{"from":"lisi","to":"me","text":"x"}`); rec.Code != http.StatusBadGateway {
		t.Errorf("gateway refused: %d, want %d", rec.Code, http.StatusBadGateway)
	}
}
