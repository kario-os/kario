package appstore

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kario-os/kario/pkg"
)

const token = "staff-token"

func newServer(t *testing.T) (*Server, *httptest.Server) {
	dir := t.TempDir()
	store, err := pkg.DevStore(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	store.Name = "store"
	s := New(dir, store, token)
	ts := httptest.NewServer(s.Routes())
	t.Cleanup(ts.Close)
	return s, ts
}

func call(t *testing.T, method, url, auth string, body any, out any) int {
	t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, r)
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

// developer registers and has the staff verify the identity.
func developer(t *testing.T, ts *httptest.Server, name, number string) pkg.Developer {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	reg := map[string]string{"name": name, "pub": b64.EncodeToString(key.Public().(ed25519.PublicKey)), "kind": "company", "number": number}
	var d Developer
	if code := call(t, "POST", ts.URL+"/v1/developers", "", reg, &d); code != 200 || d.Status != "pending" || d.Cert != nil {
		t.Fatalf("register: %d %+v", code, d)
	}
	if code := call(t, "POST", ts.URL+"/v1/admin/developers/"+name, "", nil, nil); code != 401 {
		t.Fatalf("verified without the staff token: %d", code)
	}
	if code := call(t, "POST", ts.URL+"/v1/admin/developers/"+name, token, nil, &d); code != 200 || d.Cert == nil {
		t.Fatalf("verify: %d %+v", code, d)
	}
	return pkg.Developer{Key: key, Cert: *d.Cert}
}

func build(t *testing.T, dev pkg.Developer, manifest map[string]any, extra ...string) []byte {
	dir := t.TempDir()
	raw, _ := json.Marshal(manifest)
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "phone-app-x"), []byte("#!/bin/sh\n"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "data.bin"), bytes.Repeat([]byte("same in every version "), 2000), 0o644)
	for _, e := range extra {
		_ = os.WriteFile(filepath.Join(dir, e), []byte(e), 0o644)
	}
	pk, err := pkg.Build(dir, dev)
	if err != nil {
		t.Fatal(err)
	}
	return pk
}

func submit(t *testing.T, ts *httptest.Server, raw []byte) (int, string) {
	var out struct{ Status string }
	code := call(t, "POST", ts.URL+"/v1/submissions", "", raw, &out)
	return code, out.Status
}

func TestReviewAndPublish(t *testing.T) {
	srv, ts := newServer(t)
	alice, bob := developer(t, ts, "Alice", "91110108MA01ABCD2H"), developer(t, ts, "Bob", "91310000MA1FL0000N")
	app := func(version string, extra map[string]any) map[string]any {
		m := map[string]any{"id": "org.example.todo", "name": "To-do", "version": version, "exec": "phone-app-x"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	review := func(version, decision string) {
		t.Helper()
		body := map[string]string{}
		if decision != "" {
			body["reject"] = decision
		}
		if code := call(t, "POST", ts.URL+"/v1/admin/reviews/org.example.todo/"+version, token, body, nil); code != 200 {
			t.Fatalf("review %s: %d", version, code)
		}
	}
	// The catalog comes signed by the store's root.
	catalog := func() []Entry {
		t.Helper()
		res, err := http.Get(ts.URL + "/v1/catalog")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		signed, err := pkg.VerifyCatalog(raw, map[string]ed25519.PublicKey{"store": srv.store.Public()}, time.Now())
		if err != nil {
			t.Fatalf("catalog: %v", err)
		}
		var cat []Entry
		if err := json.Unmarshal(signed.Apps, &cat); err != nil {
			t.Fatal(err)
		}
		return cat
	}
	if code, st := submit(t, ts, build(t, alice, app("1.0", nil))); code != 200 || st != "pending_review" {
		t.Fatalf("a new app: %d %s, want a human review", code, st)
	}
	if len(catalog()) != 0 {
		t.Fatal("published before the review")
	}
	review("1.0", "")
	forged := alice
	_, forged.Key, _ = ed25519.GenerateKey(rand.Reader)
	for name, raw := range map[string][]byte{
		"same version":        build(t, alice, app("1.0", nil)),
		"other developer":     build(t, bob, app("2.0", nil)),
		"reserved permission": build(t, alice, app("2.0", map[string]any{"permissions": []string{"install"}})),
		"pre-granted":         build(t, alice, app("2.0", map[string]any{"granted": []string{"camera"}})),
		"camera, no purpose":  build(t, alice, app("2.0", map[string]any{"permissions": []string{"camera"}})),
		"system id":           build(t, alice, map[string]any{"id": "com.phone.wallet", "name": "x", "version": "9", "exec": "phone-app-x"}),
		"key not certified":   build(t, forged, app("2.0", nil)),
	} {
		if code, _ := submit(t, ts, raw); code != 422 {
			t.Errorf("%s: got %d, want refusal", name, code)
		}
	}
	if code, st := submit(t, ts, build(t, alice, app("1.10", nil), "notes.txt")); code != 200 || st != "published" {
		t.Fatalf("an update with the same permissions: %d %s, want published at once", code, st)
	}
	if code, st := submit(t, ts, build(t, alice, app("1.11", map[string]any{"permissions": []string{"camera"}, "purposes": map[string]string{"camera": "Scan a list's barcode"}}))); code != 200 || st != "pending_review" {
		t.Fatalf("an update asking for the camera: %d %s, want a review", code, st)
	}
	review("1.11", "The camera's purpose is not explained")
	cat := catalog()
	if len(cat) != 1 || cat[0].Version != "1.10" || cat[0].Developer != "Alice" {
		t.Fatalf("catalog %+v", cat)
	}

	// Downloads: the whole package, or a delta from a version the store has.
	res, _ := http.Get(ts.URL + "/v1/packages/org.example.todo/1.10")
	full, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || len(full) != cat[0].Size || res.Header.Get("X-Phoneapp-Delta") != "" {
		t.Fatalf("download %d, %d bytes", res.StatusCode, len(full))
	}
	res, _ = http.Get(ts.URL + "/v1/packages/org.example.todo/1.10?from=1.0")
	delta, _ := io.ReadAll(res.Body)
	if res.Header.Get("X-Phoneapp-Delta") != "1.0" || !pkg.IsDelta(delta) || len(delta) >= len(full)/2 {
		t.Fatalf("delta: %q, %d bytes for %d", res.Header.Get("X-Phoneapp-Delta"), len(delta), len(full))
	}
	if res, _ := http.Get(ts.URL + "/v1/packages/org.example.todo/1.0"); res.StatusCode != 404 {
		t.Fatalf("superseded version still served: %d", res.StatusCode)
	}

	// Crash reports: any device sends them; only Alice reads them.
	body, _ := json.Marshal(Crash{App: "org.example.todo", Version: "1.10", Reason: "exit status 2", Log: []string{"panic: x"}})
	if code := call(t, "POST", ts.URL+"/v1/crashes", "", body, nil); code != 200 {
		t.Fatalf("crash report: %d", code)
	}
	if code := call(t, "POST", ts.URL+"/v1/crashes", "", Crash{App: "com.phone.wallet", Reason: "x"}, nil); code != 404 {
		t.Fatalf("crash of a non-store app accepted: %d", code)
	}
	read := func(dev pkg.Developer, name string, at time.Time) (int, []Crash) {
		req, _ := http.NewRequest("GET", ts.URL+"/v1/crashes?app=org.example.todo", nil)
		req.Header.Set("X-Developer", name)
		req.Header.Set("X-Time", strconv.FormatInt(at.Unix(), 10))
		req.Header.Set("X-Signature", b64.EncodeToString(ed25519.Sign(dev.Key, []byte(CrashReadMsg("org.example.todo", at.Unix())))))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out []Crash
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	if code, crashes := read(alice, "Alice", time.Now()); code != 200 || len(crashes) != 1 || crashes[0].Log[0] != "panic: x" {
		t.Fatalf("Alice reads: %d %+v", code, crashes)
	}
	for name, try := range map[string]func() (int, []Crash){
		"anonymous": func() (int, []Crash) {
			c := call(t, "GET", ts.URL+"/v1/crashes?app=org.example.todo", "", nil, nil)
			return c, nil
		},
		"other dev":     func() (int, []Crash) { return read(bob, "Bob", time.Now()) },
		"Alice's name":  func() (int, []Crash) { return read(bob, "Alice", time.Now()) },
		"replayed late": func() (int, []Crash) { return read(alice, "Alice", time.Now().Add(-time.Hour)) },
	} {
		if code, _ := try(); code != 403 {
			t.Errorf("crash reports read by %s: %d", name, code)
		}
	}

	// A takedown: out of the catalogue, onto the signed revocation list,
	// and no new version gets in.
	if code := call(t, "POST", ts.URL+"/v1/admin/takedowns/org.example.todo", token, map[string]string{"reason": "Collects personal data unlawfully"}, nil); code != 200 {
		t.Fatalf("takedown: %d", code)
	}
	if len(catalog()) != 0 {
		t.Fatal("still in the catalogue")
	}
	res, _ = http.Get(ts.URL + "/v1/revocations")
	raw, _ := io.ReadAll(res.Body)
	list, err := pkg.VerifyRevocations(raw, map[string]ed25519.PublicKey{"store": srv.store.Public()})
	if err != nil || len(list.Revoked) != 1 || list.Revoked[0].App != "org.example.todo" || list.Issued == 0 {
		t.Fatalf("revocations: %v %+v", err, list)
	}
	if code, _ := submit(t, ts, build(t, alice, app("2.0", nil))); code != 422 {
		t.Fatalf("a taken-down app came back: %d", code)
	}
}

func TestDeveloperIdentity(t *testing.T) {
	_, ts := newServer(t)
	pub := b64.EncodeToString(make([]byte, ed25519.PublicKeySize))
	for _, reg := range []map[string]string{
		{"name": "X", "pub": pub, "kind": "company", "number": "91110108MA01ABCD2J"},
		{"name": "X", "pub": pub, "kind": "person", "number": "110105194912310021"},
		{"name": "X", "pub": pub},
	} {
		if code := call(t, "POST", ts.URL+"/v1/developers", "", reg, nil); code != 400 {
			t.Errorf("%v: %d, want a refusal", reg, code)
		}
	}
	ok := map[string]string{"name": "Zhang San Studio", "pub": pub, "kind": "person", "number": "11010519491231002X"}
	var d Developer
	if code := call(t, "POST", ts.URL+"/v1/developers", "", ok, &d); code != 200 || d.Number != "**************002X" {
		t.Fatalf("register: %d %+v", code, d)
	}
	other := map[string]string{"name": "Zhang San Studio", "pub": b64.EncodeToString(bytes.Repeat([]byte{1}, 32)), "kind": "person", "number": "11010519491231002X"}
	if code := call(t, "POST", ts.URL+"/v1/developers", "", other, nil); code != 409 {
		t.Fatalf("another key took the name: %d", code)
	}
}

func TestDeveloperReadsOwnState(t *testing.T) {
	_, ts := newServer(t)
	alice, bob := developer(t, ts, "Alice", "91110108MA01ABCD2H"), developer(t, ts, "Bob", "91310000MA1FL0000N")
	read := func(name string, key ed25519.PrivateKey) (int, map[string]json.RawMessage) {
		t.Helper()
		now := time.Now().Unix()
		req, _ := http.NewRequest("GET", ts.URL+"/v1/developer", nil)
		req.Header.Set("X-Developer", name)
		req.Header.Set("X-Time", strconv.FormatInt(now, 10))
		req.Header.Set("X-Signature", b64.EncodeToString(ed25519.Sign(key, []byte(DevReadMsg(name, now)))))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]json.RawMessage
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	m := map[string]any{"id": "org.example.notes", "name": "Notes", "version": "1.0", "exec": "phone-app-x"}
	if code, st := submit(t, ts, build(t, alice, m)); code != 200 || st != "pending_review" {
		t.Fatalf("submit: %d %s", code, st)
	}
	code, out := read("Alice", alice.Key)
	if code != 200 || !bytes.Contains(out["pending"], []byte("org.example.notes")) || string(out["published"]) != "[]" {
		t.Fatalf("Alice's state: %d %s", code, out)
	}
	if code, out := read("Bob", bob.Key); code != 200 || bytes.Contains(out["pending"], []byte("org.example.notes")) {
		t.Fatalf("Bob sees Alice's submission: %d %s", code, out)
	}
	if code, _ := read("Alice", bob.Key); code != 403 {
		t.Fatalf("read as Alice with Bob's key: %d", code)
	}
	if code := call(t, "POST", ts.URL+"/v1/admin/reviews/org.example.notes/1.0", token, map[string]string{}, nil); code != 200 {
		t.Fatalf("review: %d", code)
	}
	if code, out := read("Alice", alice.Key); code != 200 || !bytes.Contains(out["published"], []byte("org.example.notes")) || string(out["pending"]) != "[]" {
		t.Fatalf("after the review: %d %s", code, out)
	}
}

// The state is replaced whole and reloads; no temporary file stays behind.
func TestStateSavedWhole(t *testing.T) {
	s, _ := newServer(t)
	s.developers["acme"] = &Developer{Name: "acme", Status: "pending"}
	s.save()
	s.developers["acme"].Status = "verified"
	s.save()
	again := New(s.dir, s.store, "")
	if d := again.developers["acme"]; d == nil || d.Status != "verified" {
		t.Fatalf("reloaded developer: %+v", d)
	}
	left, _ := filepath.Glob(filepath.Join(s.dir, ".state.json.*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

// A manifest's locales are checked at submission and travel in the
// catalogue entry, which names and describes the app in each language.
func TestLocalizedEntries(t *testing.T) {
	_, ts := newServer(t)
	alice := developer(t, ts, "Alice", "91110108MA01ABCD2H")
	app := func(id string, locales map[string]any) map[string]any {
		return map[string]any{"id": id, "name": "Notes", "lang": "en", "description": "Write things down", "version": "1.0",
			"exec": "phone-app-x", "permissions": []string{"camera"}, "purposes": map[string]string{"camera": "Scan a page"},
			"settings": []map[string]any{{"key": "sort", "title": "Sort", "type": "choice", "choices": []string{"date", "name"}, "default": "date"}},
			"locales":  locales}
	}
	good := map[string]any{
		"es":      map[string]any{"name": "Notas", "description": "Apunta cosas", "purposes": map[string]string{"camera": "Escanear una página"}},
		"zh-Hans": map[string]any{"name": "\u4fbf\u7b7e", "settings": map[string]any{"sort": map[string]any{"title": "\u6392\u5e8f", "choices": []string{"\u65e5\u671f", "\u540d\u79f0"}}}},
	}
	var out struct {
		Status string
		Entry  Entry
	}
	if code := call(t, "POST", ts.URL+"/v1/submissions", "", build(t, alice, app("org.example.notes", good)), &out); code != 200 {
		t.Fatalf("submission: %d", code)
	}
	e := out.Entry
	if e.Lang != "en" || e.Locales["es"].Name != "Notas" || e.Locales["es"].Purposes["camera"] != "Escanear una página" {
		t.Fatalf("entry %+v", e)
	}
	for lang, want := range map[string][2]string{"es": {"Notas", "Apunta cosas"}, "zh-Hant": {"\u4fbf\u7b7e", "Write things down"}, "en": {"Notes", "Write things down"}} {
		if n, s := e.Text(lang); n != want[0] || s != want[1] {
			t.Errorf("%s: %q %q", lang, n, s)
		}
	}
	for i, bad := range []map[string]any{
		{"fr": map[string]any{"name": "Notes"}},
		{"es": map[string]any{"purposes": map[string]string{"location": "x"}}},
		{"es": map[string]any{"settings": map[string]any{"sort": map[string]any{"choices": []string{"Fecha"}}}}},
		{"es": map[string]any{"name": "a very long name that no menu could ever show"}},
	} {
		if code, _ := submit(t, ts, build(t, alice, app("org.example.bad"+strconv.Itoa(i), bad))); code != 422 {
			t.Errorf("%v: %d, want refused", bad, code)
		}
	}
}
