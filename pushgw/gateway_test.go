package pushgw

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/push"
)

func do(t *testing.T, h http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	return rec
}

func TestRevokeOnUninstall(t *testing.T) {
	g := &Gateway{key: []byte("0123456789abcdef0123456789abcdef"), devices: map[string]*device{}}
	dev := map[string]string{"device": "dev-1", "secret": "0123456789abcdef", "app": "com.example.chat"}
	token := func() string {
		var out struct{ Token string }
		if rec := do(t, g.token, dev); rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("token: %d %s", rec.Code, rec.Body)
		}
		return out.Token
	}
	send := func(tok string) int { return do(t, g.push, push.Send{Token: tok, Title: "hi"}).Code }
	first := token()
	if code := send(first); code != http.StatusOK {
		t.Fatalf("push with a fresh token: %d", code)
	}
	if rec := do(t, g.revoke, dev); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if code := send(first); code != http.StatusGone {
		t.Fatalf("push with a revoked token: %d, want 410", code)
	}
	second := token()
	if second == first {
		t.Fatal("the reinstalled app got the old token")
	}
	if code := send(second); code != http.StatusOK {
		t.Fatalf("push after reinstall: %d", code)
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	path := t.TempDir() + "/state.json"
	g := &Gateway{devices: map[string]*device{}, statePath: path}
	if err := g.load(); err != nil {
		t.Fatal(err)
	}
	dev := map[string]string{"device": "dev-1", "secret": "0123456789abcdef", "app": "com.example.chat"}
	var out struct{ Token string }
	rec := do(t, g.token, dev)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	do(t, g.push, push.Send{Token: out.Token, Title: "queued"})
	again := &Gateway{devices: map[string]*device{}, statePath: path}
	if err := again.load(); err != nil {
		t.Fatal(err)
	}
	if code := do(t, again.push, push.Send{Token: out.Token, Title: "after restart"}).Code; code != http.StatusOK {
		t.Fatalf("token after a restart: %d", code)
	}
	if n := len(again.devices["dev-1"].Queue); n != 2 {
		t.Fatalf("%d queued after a restart, want 2", n)
	}
}

func TestCollapse(t *testing.T) {
	g := &Gateway{key: []byte("0123456789abcdef0123456789abcdef"), devices: map[string]*device{}}
	dev := map[string]string{"device": "dev-1", "secret": "0123456789abcdef", "app": "com.example.chat"}
	var out struct{ Token string }
	_ = json.Unmarshal(do(t, g.token, dev).Body.Bytes(), &out)
	for _, s := range []push.Send{
		{Token: out.Token, Title: "unread 1", Collapse: "unread"},
		{Token: out.Token, Title: "other"},
		{Token: out.Token, Title: "unread 2", Collapse: "unread"},
	} {
		if code := do(t, g.push, s).Code; code != http.StatusOK {
			t.Fatalf("push: %d", code)
		}
	}
	var titles []string
	for _, m := range g.devices["dev-1"].Queue {
		titles = append(titles, m.Title)
	}
	if len(titles) != 2 || titles[0] != "other" || titles[1] != "unread 2" {
		t.Fatalf("queue after collapse: %v", titles)
	}
	long := push.Send{Token: out.Token, Title: "x", Collapse: string(make([]byte, push.MaxCollapse+1))}
	if code := do(t, g.push, long).Code; code != http.StatusBadRequest {
		t.Fatalf("over-long collapse_id: %d", code)
	}
}

func TestAttestedRegistration(t *testing.T) {
	dir := t.TempDir()
	ks, err := keystore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{key: []byte("0123456789abcdef0123456789abcdef"), devices: map[string]*device{}, rootPath: keystore.RootPubPath(dir)}
	dev := map[string]string{"device": "dev-1", "secret": "0123456789abcdef", "app": "com.example.chat"}
	if code := do(t, g.token, dev).Code; code != http.StatusForbidden {
		t.Fatalf("unregistered device got a token: %d", code)
	}
	att, _ := ks.Attest(push.RegisterApp, push.RegisterNonce("dev-1", "0123456789abcdef"), "")
	forged := *att
	forged.Nonce = push.RegisterNonce("dev-1", "another-secret-0000")
	if code := do(t, g.register, map[string]any{"device": "dev-1", "secret": "another-secret-0000", "attestation": &forged}).Code; code != http.StatusForbidden {
		t.Fatalf("reused attestation for another secret: %d", code)
	}
	if code := do(t, g.register, map[string]any{"device": "dev-1", "secret": "0123456789abcdef", "attestation": att}).Code; code != http.StatusOK {
		t.Fatalf("register: %d", code)
	}
	if code := do(t, g.token, dev).Code; code != http.StatusOK {
		t.Fatalf("registered device: %d", code)
	}
	other, _ := keystore.Open(t.TempDir())
	att2, _ := other.Attest(push.RegisterApp, push.RegisterNonce("dev-1", "fedcba9876543210"), "")
	if code := do(t, g.register, map[string]any{"device": "dev-1", "secret": "fedcba9876543210", "attestation": att2}).Code; code != http.StatusForbidden {
		t.Fatalf("a device of another root took over the id: %d", code)
	}
}
