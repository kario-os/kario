package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/appstore"
	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

// kario package and publish against the store backend's own code
// (appstore, which phone-storesrv runs): a new app waits for the staff, the
// catalogue carries its privacy label, an update collecting nothing new
// goes out staged at once, one collecting more data waits again.
func TestPublishToStore(t *testing.T) {
	const staff = "staff-token"
	root, err := pkg.DevStore(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root.Name = "store"
	ts := httptest.NewServer(appstore.New(t.TempDir(), root, staff).Routes())
	defer ts.Close()
	post := func(path, auth string, body any, out any) int {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", ts.URL+path, bytes.NewReader(raw))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if out != nil {
			_ = json.NewDecoder(res.Body).Decode(out)
		}
		return res.StatusCode
	}

	pub, key, _ := ed25519.GenerateKey(nil)
	reg := map[string]string{"name": "Example", "pub": base64.StdEncoding.EncodeToString(pub), "kind": "company", "number": "91110108MA01ABCD2H"}
	var dev appstore.Developer
	if code := post("/v1/developers", "", reg, nil); code != 200 {
		t.Fatalf("register: %d", code)
	}
	if code := post("/v1/admin/developers/Example", staff, map[string]string{}, &dev); code != 200 || dev.Cert == nil {
		t.Fatalf("verify: %d %+v", code, dev)
	}
	keys := t.TempDir()
	o := packageOptions{key: filepath.Join(keys, "dev.key"), cert: filepath.Join(keys, "dev.cert"), out: filepath.Join(keys, "app.phoneapp")}
	cert, _ := json.Marshal(dev.Cert)
	_ = os.WriteFile(o.key, []byte(base64.StdEncoding.EncodeToString(key.Seed())), 0o600)
	_ = os.WriteFile(o.cert, cert, 0o644)

	dir := newTestProject(t)
	replace(t, dir, "manifest.json", `"permissions": []`,
		`"permissions": ["net"], "privacy": {"collects": [{"data": "diagnostics", "use": ["app"]}]}`)
	release := func(rollout int) *submission {
		t.Helper()
		if _, err := packageApp(dir, o); err != nil {
			t.Fatal(err)
		}
		s, err := publish(o.out, publishOptions{store: ts.URL, rollout: rollout})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := release(100); s.Status != "pending_review" || s.Entry.ID != "com.example.my_app" || s.Entry.Version != "0.1" {
		t.Fatalf("new app: %+v", s)
	}
	if code := post("/v1/admin/reviews/com.example.my_app/0.1", staff, map[string]string{}, nil); code != 200 {
		t.Fatalf("review: %d", code)
	}
	res, err := http.Get(ts.URL + "/v1/catalog")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	cat, err := pkg.VerifyCatalog(raw, map[string]ed25519.PublicKey{"store": root.Public()}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var apps []appstore.Entry
	if err := json.Unmarshal(cat.Apps, &apps); err != nil || len(apps) != 1 || !apps[0].Privacy.Sends(ui.DataDiagnostics) {
		t.Fatalf("catalogue %s: %v", cat.Apps, err)
	}

	replace(t, dir, "manifest.json", `"version": "0.1"`, `"version": "0.2"`)
	if s := release(10); s.Status != "published" {
		t.Fatalf("an update collecting nothing new: %+v", s)
	}
	replace(t, dir, "manifest.json", `"version": "0.2"`, `"version": "0.3"`)
	replace(t, dir, "manifest.json", `"use": ["app"]}`, `"use": ["app"]}, {"data": "usage", "use": ["analytics"]}`)
	if s := release(100); s.Status != "pending_review" {
		t.Fatalf("an update collecting usage data: %+v", s)
	}

	if _, err := packageApp(dir, packageOptions{out: o.out, devStore: t.TempDir(), name: "Example"}); err != nil {
		t.Fatal(err)
	}
	if _, err := publish(o.out, publishOptions{store: ts.URL, rollout: 100}); err == nil || !strings.HasPrefix(err.Error(), "store: ") {
		t.Fatalf("a package the store did not certify: %v", err)
	}
}
