package appstore

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/kario-os/kario/pkg"
)

// A staged update keeps the previous release for the devices outside it,
// both stay downloadable, the developer (only they) moves the percentage,
// and a version that crashes on many of the devices that took it halts.
func TestStagedRollout(t *testing.T) {
	srv, ts := newServer(t)
	alice := developer(t, ts, "Alice", "91110108MA01ABCD2H")
	app := func(version string) []byte {
		return build(t, alice, map[string]any{"id": "org.example.todo", "name": "To-do", "version": version, "exec": "phone-app-x"})
	}
	if code, _ := submit(t, ts, app("1.0")); code != 200 {
		t.Fatal(code)
	}
	call(t, "POST", ts.URL+"/v1/admin/reviews/org.example.todo/1.0", token, map[string]string{}, nil)
	var out struct{ Status string }
	if code := call(t, "POST", ts.URL+"/v1/submissions?rollout=10", "", app("1.1"), &out); code != 200 || out.Status != "published" {
		t.Fatalf("staged submission: %d %s", code, out.Status)
	}
	e := srv.catalog["org.example.todo"]
	if e.Version != "1.1" || e.Rollout != 10 || e.Previous == nil || e.Previous.Version != "1.0" {
		t.Fatalf("entry %+v", e)
	}
	for _, v := range []string{"1.0", "1.1"} {
		if res, err := http.Get(ts.URL + "/v1/packages/org.example.todo/" + v); err != nil || res.StatusCode != 200 {
			t.Fatalf("package %s: %v %v", v, err, res)
		}
	}

	move := func(key ed25519.PrivateKey, percent int) int {
		t.Helper()
		now := time.Now().Unix()
		body := map[string]any{"app": "org.example.todo", "version": "1.1", "percent": percent}
		req, _ := http.NewRequest("POST", ts.URL+"/v1/developer/rollout", jsonBody(body))
		req.Header.Set("X-Developer", "Alice")
		req.Header.Set("X-Time", strconv.FormatInt(now, 10))
		req.Header.Set("X-Signature", b64.EncodeToString(ed25519.Sign(key, []byte(pkg.RolloutMsg("Alice", now, "org.example.todo", "1.1", percent)))))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	_, other, _ := ed25519.GenerateKey(nil)
	if code := move(other, 100); code != 403 {
		t.Fatalf("someone else moved the rollout: %d", code)
	}
	if code := move(alice.Key, 50); code != 200 || srv.catalog["org.example.todo"].Rollout != 50 {
		t.Fatalf("raise: %d", code)
	}

	for range 2 {
		http.Get(ts.URL + "/v1/packages/org.example.todo/1.1?from=1.0")
	}
	for range haltCrashMin {
		call(t, "POST", ts.URL+"/v1/crashes", "", Crash{App: "org.example.todo", Version: "1.1", Reason: "SIGSEGV"}, nil)
	}
	if e := srv.catalog["org.example.todo"]; e.Rollout != 0 || e.Halted == "" || e.Previous == nil {
		t.Fatalf("not halted: %+v", e)
	}
	if code := move(alice.Key, 100); code != 200 {
		t.Fatalf("release to all: %d", code)
	}
	if e := srv.catalog["org.example.todo"]; e.Previous != nil || e.Rollout != 0 || e.Halted != "" {
		t.Fatalf("still staged: %+v", e)
	}
}

func jsonBody(v any) io.Reader {
	raw, _ := json.Marshal(v)
	return bytes.NewReader(raw)
}
