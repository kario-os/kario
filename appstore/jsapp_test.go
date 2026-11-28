package appstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/jsrt/nativetest"
	"github.com/kario-os/kario/pkg"
)

// Review takes a TypeScript app with its module and no program of its own,
// and refuses one that brings native code or bytecode.
func TestReviewJSApp(t *testing.T) {
	_, ts := newServer(t)
	dev := developer(t, ts, "Alice", "91110108MA01ABCD2H")
	pack := func(id string, files map[string]os.FileMode) []byte {
		t.Helper()
		dir := t.TempDir()
		raw, _ := json.Marshal(map[string]any{"id": id, "name": "Notes", "version": "1.0", "runtime": "js", "main": "main.js"})
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644)
		for name, mode := range files {
			_ = os.WriteFile(filepath.Join(dir, name), []byte(`import * as kario from "kario";`), mode)
		}
		pk, err := pkg.Build(dir, dev)
		if err != nil {
			t.Fatal(err)
		}
		return pk
	}
	if code, st := submit(t, ts, pack("org.example.tsnotes", map[string]os.FileMode{"main.js": 0o644})); code != 200 || st != "pending_review" {
		t.Fatalf("a TypeScript app: %d %s", code, st)
	}
	for _, c := range []struct {
		files map[string]os.FileMode
		want  string
	}{
		{map[string]os.FileMode{"main.js": 0o644, "helper": 0o755}, "no executable file"},
		{map[string]os.FileMode{"main.js": 0o644, "main.jsc": 0o644}, "not bytecode"},
		{map[string]os.FileMode{"app.js": 0o644}, "not in the package"},
	} {
		var out struct{ Error string }
		code := call(t, "POST", ts.URL+"/v1/submissions", "", pack("org.example.tsbad", c.files), &out)
		if code == 200 || !strings.Contains(out.Error, c.want) {
			t.Errorf("%v: %d %q, want %q", c.files, code, out.Error, c.want)
		}
	}
}

// An app with native modules needs them built for the phone; every version
// of it waits for the staff, and the catalogue says it brings native code.
func TestReviewNativeModules(t *testing.T) {
	probe := nativetest.Build(t)
	srv, ts := newServer(t)
	dev := developer(t, ts, "Alice", "91110108MA01ABCD2H")
	pack := func(version string, arches ...string) []byte {
		t.Helper()
		dir := t.TempDir()
		raw, _ := json.Marshal(map[string]any{"id": "org.example.imgproc", "name": "Filters", "version": version, "runtime": "js", "native": []string{"probe"}})
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "main.js"), []byte(`import * as kario from "kario";`), 0o644)
		for _, a := range arches {
			p := filepath.Join(dir, filepath.FromSlash(jsrt.NativePath(a, "probe")))
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, nativetest.Retarget(probe, a), 0o644)
		}
		pk, err := pkg.Build(dir, dev)
		if err != nil {
			t.Fatal(err)
		}
		return pk
	}
	var out struct{ Error string }
	if code := call(t, "POST", ts.URL+"/v1/submissions", "", pack("1.0", jsrt.ArchX86_64), &out); code == 200 || !strings.Contains(out.Error, "is not built for arm64") {
		t.Fatalf("no arm64 library: %d %q", code, out.Error)
	}
	if code, st := submit(t, ts, pack("1.0", jsrt.ArchARM64, jsrt.ArchX86_64)); code != 200 || st != "pending_review" {
		t.Fatalf("1.0: %d %s", code, st)
	}
	call(t, "POST", ts.URL+"/v1/admin/reviews/org.example.imgproc/1.0", token, map[string]string{}, nil)
	if e := srv.catalog["org.example.imgproc"]; !e.Native {
		t.Fatalf("catalogue entry %+v", e)
	}
	if code, st := submit(t, ts, pack("1.1", jsrt.ArchARM64)); code != 200 || st != "pending_review" || srv.pending["org.example.imgproc@1.1"].Why != "native code: probe" {
		t.Fatalf("an update with native code: %d %s %q", code, st, srv.pending["org.example.imgproc@1.1"].Why)
	}
}

// download is GET path's body, and whether it came as a delta.
func download(t *testing.T, url string) ([]byte, bool) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", url, res.StatusCode, raw)
	}
	return raw, res.Header.Get("X-Phoneapp-Delta") != ""
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// A package with libraries for both architectures is published whole; a
// device that says its architecture downloads it (and its updates, as
// deltas) without the other one's, which verifies against the developer's
// signature on that device and has the digest the catalogue lists for it.
func TestThinnedDownload(t *testing.T) {
	probe := nativetest.Build(t)
	srv, ts := newServer(t)
	dev := developer(t, ts, "Alice", "91110108MA01ABCD2H")
	roots := srv.store.Roots()
	publish := func(version string) Entry {
		t.Helper()
		dir := t.TempDir()
		raw, _ := json.Marshal(map[string]any{"id": "org.example.imgproc", "name": "Filters", "version": version, "runtime": "js", "native": []string{"probe"}})
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "main.js"), []byte(`import * as kario from "kario"; // `+version), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "photo.raw"), bytes.Repeat([]byte("unchanged "), 20000), 0o644)
		for _, a := range jsrt.Arches {
			p := filepath.Join(dir, filepath.FromSlash(jsrt.NativePath(a, "probe")))
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, append(nativetest.Retarget(probe, a), version...), 0o644)
		}
		pk, err := pkg.Build(dir, dev)
		if err != nil {
			t.Fatal(err)
		}
		if code, st := submit(t, ts, pk); code != 200 || st != "pending_review" {
			t.Fatalf("%s: %d %s", version, code, st)
		}
		call(t, "POST", ts.URL+"/v1/admin/reviews/org.example.imgproc/"+version, token, map[string]string{}, nil)
		e := srv.catalog["org.example.imgproc"]
		if e.Version != version || len(e.Thinned) != len(jsrt.Arches) {
			t.Fatalf("catalogue entry %+v", e)
		}
		return e
	}
	e := publish("1.0")
	base := ts.URL + "/v1/packages/org.example.imgproc/"
	whole, _ := download(t, base+"1.0")
	if digest(whole) != e.SHA256 {
		t.Fatal("the whole package is not the catalogue's")
	}
	arm, _ := download(t, base+"1.0?arch="+jsrt.ArchARM64)
	if len(arm) != e.Thinned[jsrt.ArchARM64].Size || digest(arm) != e.Thinned[jsrt.ArchARM64].SHA256 {
		t.Fatalf("thinned for arm64: %d bytes, catalogue %+v", len(arm), e.Thinned)
	}
	p, err := pkg.OpenFor(arm, roots, jsrt.ArchARM64)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsrt.Check(jsrt.App{Runtime: jsrt.Runtime, Native: []string{"probe"}}, p.Files, jsrt.FromData(p.Data), jsrt.ArchARM64); err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Files {
		if f.Path == jsrt.NativePath(jsrt.ArchX86_64, "probe") {
			t.Fatal("the other architecture's library came along")
		}
	}
	// What the other architecture's devices get lacks the phone's library.
	x86, _ := download(t, base+"1.0?arch="+jsrt.ArchX86_64)
	if _, err := pkg.OpenFor(x86, roots, jsrt.ArchARM64); err == nil || !strings.Contains(err.Error(), "lib/arm64/probe.so: differs from the signed list") {
		t.Fatalf("a package without the device's own library: %v", err)
	}
	if _, err := pkg.Open(arm, roots); err == nil {
		t.Fatal("a thinned package verified as whole")
	}
	installed := filepath.Join(t.TempDir(), "app")
	if err := p.Extract(installed); err != nil {
		t.Fatal(err)
	}
	e = publish("1.1")
	delta, isDelta := download(t, base+"1.1?from=1.0&arch="+jsrt.ArchARM64)
	if !isDelta {
		t.Fatal("no delta")
	}
	full, err := pkg.ApplyDeltaFor(delta, installed, jsrt.ArchARM64)
	if err != nil {
		t.Fatal(err)
	}
	if digest(full) != e.Thinned[jsrt.ArchARM64].SHA256 {
		t.Fatal("the rebuilt update is not the catalogue's thinned package")
	}
	if _, err := pkg.OpenFor(full, roots, jsrt.ArchARM64); err != nil {
		t.Fatal(err)
	}
	if err := pkg.VerifyDirFor(installed, jsrt.ArchARM64, roots); err != nil {
		t.Fatal(err)
	}
}
