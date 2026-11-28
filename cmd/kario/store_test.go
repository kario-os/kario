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

const staff = "staff-token"

// testStore is the store backend's own code (appstore, which
// phone-storesrv runs) with a verified developer, whose key and
// certificate kario package signs with.
type testStore struct {
	dir  string
	ts   *httptest.Server
	root *pkg.Store
	o    packageOptions
}

func newTestStore(t *testing.T) *testStore {
	t.Helper()
	root, err := pkg.DevStore(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root.Name = "store"
	s := &testStore{dir: t.TempDir(), root: root}
	s.restart(t, nil)

	pub, key, _ := ed25519.GenerateKey(nil)
	reg := map[string]string{"name": "Example", "pub": base64.StdEncoding.EncodeToString(pub), "kind": "company", "number": "91110108MA01ABCD2H"}
	var dev appstore.Developer
	if code := s.post(t, "/v1/developers", "", reg, nil); code != 200 {
		t.Fatalf("register: %d", code)
	}
	if code := s.post(t, "/v1/admin/developers/Example", staff, map[string]string{}, &dev); code != 200 || dev.Cert == nil {
		t.Fatalf("verify: %d %+v", code, dev)
	}
	keys := t.TempDir()
	s.o = packageOptions{key: filepath.Join(keys, "dev.key"), cert: filepath.Join(keys, "dev.cert"), out: filepath.Join(keys, "app.phoneapp")}
	cert, _ := json.Marshal(dev.Cert)
	_ = os.WriteFile(s.o.key, []byte(base64.StdEncoding.EncodeToString(key.Seed())), 0o600)
	_ = os.WriteFile(s.o.cert, cert, 0o644)
	return s
}

// restart starts the store again over its state, set up by setup.
func (s *testStore) restart(t *testing.T, setup func(*appstore.Server)) {
	t.Helper()
	if s.ts != nil {
		s.ts.Close()
	}
	srv := appstore.New(s.dir, s.root, staff)
	if setup != nil {
		setup(srv)
	}
	s.ts = httptest.NewServer(srv.Routes())
	t.Cleanup(s.ts.Close)
}

func (s *testStore) post(t *testing.T, path, auth string, body any, out any) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", s.ts.URL+path, bytes.NewReader(raw))
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

// release packages the project and publishes it.
func (s *testStore) release(t *testing.T, dir string, rollout int) (*submission, error) {
	t.Helper()
	if _, err := packageApp(dir, s.o); err != nil {
		t.Fatal(err)
	}
	return publish(s.o.out, publishOptions{store: s.ts.URL, rollout: rollout, key: s.o.key})
}

// kario package and publish against the store backend: a new app waits
// for the staff, the catalogue carries its privacy label, an update
// collecting nothing new goes out staged at once, one collecting more
// data waits again.
func TestPublishToStore(t *testing.T) {
	st := newTestStore(t)
	dir := newTestProject(t)
	replace(t, dir, "manifest.json", `"permissions": []`,
		`"permissions": ["net"], "privacy": {"collects": [{"data": "diagnostics", "use": ["app"]}]}`)
	release := func(rollout int) *submission {
		t.Helper()
		s, err := st.release(t, dir, rollout)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(s.Symbols, "the store takes no symbol files") {
			t.Fatalf("symbols at a store without them: %q", s.Symbols)
		}
		return s
	}
	if s := release(100); s.Status != "pending_review" || s.Entry.ID != "com.example.my_app" || s.Entry.Version != "0.1" {
		t.Fatalf("new app: %+v", s)
	}
	if code := st.post(t, "/v1/admin/reviews/com.example.my_app/0.1", staff, map[string]string{}, nil); code != 200 {
		t.Fatalf("review: %d", code)
	}
	res, err := http.Get(st.ts.URL + "/v1/catalog")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	cat, err := pkg.VerifyCatalog(raw, st.root.Roots(), time.Now())
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

	if _, err := packageApp(dir, packageOptions{out: st.o.out, devStore: t.TempDir(), name: "Example"}); err != nil {
		t.Fatal(err)
	}
	if _, err := publish(st.o.out, publishOptions{store: st.ts.URL, rollout: 100}); err == nil || !strings.HasPrefix(err.Error(), "store: ") {
		t.Fatalf("a package the store did not certify: %v", err)
	}
}

// An app with network access and no privacy label: kario build says what
// to add; a store that requires labels refuses it with the same words, and
// takes an update of an app it published without one only in its grace,
// which kario publish passes on.
func TestPublishWithoutPrivacyLabel(t *testing.T) {
	st := newTestStore(t)
	dir := newTestProject(t)
	replace(t, dir, "manifest.json", `"permissions": []`, `"permissions": ["net", "camera"], "purposes": {"camera": "Scan a code"}`)
	r, err := build(dir, buildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if s := r.summary(); !strings.Contains(s, "no privacy label: a store that requires one") || !strings.Contains(s, "open photos: list each") {
		t.Fatalf("build summary:\n%s", s)
	}
	s, err := st.release(t, dir, 100)
	if err != nil || s.Status != "pending_review" || s.Warning != "" {
		t.Fatalf("a store without the rule: %+v %v", s, err)
	}
	st.post(t, "/v1/admin/reviews/com.example.my_app/0.1", staff, map[string]string{}, nil)

	st.restart(t, func(s *appstore.Server) { s.RequirePrivacy = true })
	replace(t, dir, "manifest.json", `"version": "0.1"`, `"version": "0.2"`)
	if _, err := st.release(t, dir, 100); err == nil || !strings.Contains(err.Error(), "store: privacy: no privacy label") ||
		!strings.Contains(err.Error(), "open photos: list each") {
		t.Fatalf("a store that requires labels: %v", err)
	}
	st.restart(t, func(s *appstore.Server) { s.RequirePrivacy, s.PrivacyGrace = true, time.Now().Add(48*time.Hour) })
	if s, err := st.release(t, dir, 100); err != nil || s.Status != "published" || !strings.Contains(s.Warning, "this store refuses updates without one") {
		t.Fatalf("an update in the grace: %+v %v", s, err)
	}
	replace(t, dir, "manifest.json", `"version": "0.2"`, `"version": "0.3"`)
	replace(t, dir, "manifest.json", `"purposes"`, `"privacy": {"local": ["photos"]}, "purposes"`)
	if r, err := build(dir, buildOptions{}); err != nil || strings.Contains(r.summary(), "privacy") {
		t.Fatalf("with a label: %v %+v", err, r)
	}
	if s, err := st.release(t, dir, 100); err != nil || s.Status != "published" || s.Warning != "" {
		t.Fatalf("an update with a label: %+v %v", s, err)
	}
}
