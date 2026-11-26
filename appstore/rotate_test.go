package appstore

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kario-os/kario/keyset"
	"github.com/kario-os/kario/pkg"
)

// The store root rotated: the next key given to devices while the old one
// signs, then the store restarted with the next key signing and the old one
// still trusted. Each verified developer is certified again under the new
// key (registering again hands the certificate out); a package under the
// old certificate is taken while the old key is trusted and refused once
// it is not; the catalog comes signed with the new key, which an image
// with both keys takes and an image with the old one alone does not.
func TestRootRotation(t *testing.T) {
	dir := t.TempDir()
	_, oldKey, _ := ed25519.GenerateKey(rand.Reader)
	_, nextKey, _ := ed25519.GenerateKey(rand.Reader)
	oldPub, nextPub := oldKey.Public().(ed25519.PublicKey), nextKey.Public().(ed25519.PublicKey)
	start := func(st *pkg.Store) *httptest.Server {
		ts := httptest.NewServer(New(dir, st, token).Routes())
		t.Cleanup(ts.Close)
		return ts
	}
	app := func(version string) map[string]any {
		return map[string]any{"id": "org.example.todo", "name": "To-do", "version": version, "exec": "phone-app-x"}
	}

	ts := start(&pkg.Store{Name: "store", Key: oldKey, Trusted: keyset.Set{nextPub}})
	alice := developer(t, ts, "Alice", "91110108MA01ABCD2H")
	if alice.Cert.Key != keyset.ID(oldPub) {
		t.Fatalf("certified by key %q, not the old one", alice.Cert.Key)
	}
	if code, _ := submit(t, ts, build(t, alice, app("1.0"))); code != 200 {
		t.Fatalf("submit 1.0: %d", code)
	}
	ts.Close()

	ts = start(&pkg.Store{Name: "store", Key: nextKey, Trusted: keyset.Set{oldPub}})
	reg := map[string]string{"name": "Alice", "pub": alice.Cert.Pub, "kind": "company", "number": "91110108MA01ABCD2H"}
	var d Developer
	if code := call(t, "POST", ts.URL+"/v1/developers", "", reg, &d); code != 200 || d.Cert == nil || d.Cert.Key != keyset.ID(nextPub) {
		t.Fatalf("registered again: %d %+v", code, d.Cert)
	}
	if code, status := submit(t, ts, build(t, alice, app("1.1"))); code != 200 {
		t.Fatalf("a package under the old certificate while the old key is trusted: %d %s", code, status)
	}
	res, err := http.Get(ts.URL + "/v1/catalog")
	if err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	_ = json.NewDecoder(res.Body).Decode(&raw)
	res.Body.Close()
	if _, err := pkg.VerifyCatalog(raw, pkg.Roots{"store": {oldPub, nextPub}}, time.Now()); err != nil {
		t.Fatalf("an image with both keys: %v", err)
	}
	if _, err := pkg.VerifyCatalog(raw, pkg.Roots{"store": {oldPub}}, time.Now()); err == nil {
		t.Fatal("an image with the old key alone took the new key's catalog")
	}
	ts.Close()

	ts = start(&pkg.Store{Name: "store", Key: nextKey})
	if code, _ := submit(t, ts, build(t, alice, app("1.2"))); code != 422 {
		t.Fatalf("a package under the retired key's certificate: %d", code)
	}
	if code, _ := submit(t, ts, build(t, pkg.Developer{Key: alice.Key, Cert: *d.Cert}, app("1.2"))); code != 200 {
		t.Fatalf("a package under the new certificate: %d", code)
	}
}
