package main

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
)

const throwingApp = `import * as kario from "kario";

function explode(n: number): number {
  if (n > 1) throw new Error("boom " + n); // @throw
  return n;
}

kario.on("start", () => {});
explode(2);
`

func packageFiles(t *testing.T, raw []byte) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
}

// kario package writes the module's source map into a symbol file beside
// the package and never into it, a build's map included; the symbol file
// names the package and its module by digest, and a crash's stack from
// the minified module, as kario-jsrt writes it, maps back to the
// TypeScript (kario symbolize -symbols).
func TestPackageSymbols(t *testing.T) {
	dir := newTestProject(t)
	if err := os.WriteFile(filepath.Join(dir, "src", "main.ts"), []byte(throwingApp), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "app.phoneapp")
	if _, err := packageApp(dir, packageOptions{out: out, devStore: t.TempDir(), name: "D"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	for _, f := range packageFiles(t, raw) {
		if strings.HasSuffix(f, ".map") {
			t.Fatalf("the package carries %s", f)
		}
	}
	symRaw, err := os.ReadFile(symbolsPath(out))
	if err != nil {
		t.Fatal(err)
	}
	sym, err := jsrt.ReadSymbolFile(symRaw)
	if err != nil {
		t.Fatal(err)
	}
	mainSum, _ := packagedSum(raw, jsrt.DefaultMain)
	if sym.App != "com.example.my_app" || sym.Version != "0.1" || sym.PackageSHA256 != jsrt.SHA256(raw) || sym.MainSHA256 != mainSum || jsrt.SHA256(sym.Module) != mainSum {
		t.Fatalf("symbol file %+v", sym)
	}
	m, err := sym.SourceMap()
	if err != nil || m.SourceIndex("src/main.ts") < 0 {
		t.Fatalf("map sources %v: %v", m.Sources, err)
	}

	// A build with -sourcemap keeps its map out of the package too.
	build1 := filepath.Join(t.TempDir(), "build")
	if _, err := build(dir, buildOptions{out: build1, sourcemap: true, minify: true}); err != nil {
		t.Fatal(err)
	}
	out2 := filepath.Join(t.TempDir(), "built.phoneapp")
	if _, err := packageApp(build1, packageOptions{out: out2, devStore: t.TempDir(), name: "D"}); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(out2)
	for _, f := range packageFiles(t, raw2) {
		if strings.HasSuffix(f, ".map") {
			t.Fatalf("the package of a build carries %s", f)
		}
	}
	if s2, err := os.ReadFile(symbolsPath(out2)); err != nil {
		t.Fatalf("no symbol file for a build: %v", err)
	} else if sym2, err := jsrt.ReadSymbolFile(s2); err != nil || sym2.Map == nil {
		t.Fatalf("a build's symbol file: %v", err)
	}
	if _, err := packageApp(dir, packageOptions{out: filepath.Join(t.TempDir(), "x.phoneapp"), devStore: t.TempDir(), name: "D", symbols: "-"}); err != nil {
		t.Fatal(err)
	}

	bin := os.Getenv("KARIO_JSRT")
	if bin == "" {
		bin, _ = filepath.Abs(filepath.Join("..", "..", "build", "jsrt", jsrt.Program))
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("no kario-jsrt (make jsrt): the stack's mapping not checked")
	}
	app := t.TempDir()
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Name == jsrt.DefaultMain {
			body, _ := io.ReadAll(tr)
			_ = os.WriteFile(filepath.Join(app, jsrt.DefaultMain), body, 0o644)
		}
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	ours, theirs := os.NewFile(uintptr(fds[0]), "ui"), os.NewFile(uintptr(fds[1]), "ui-child")
	defer ours.Close()
	cmd := exec.Command(bin, filepath.Join(app, jsrt.DefaultMain))
	cmd.ExtraFiles = []*os.File{theirs}
	cmd.Env = []string{"PHONE_UI_FD=3", "PHONE_APP_ID=com.example.my_app", "PHONE_DATA_DIR=" + t.TempDir()}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	theirs.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("kario-jsrt did not end")
	}
	if !strings.Contains(stderr.String(), "boom 2") || !strings.Contains(stderr.String(), "(main.js:") {
		t.Fatalf("stderr:\n%s", stderr.String())
	}
	var got bytes.Buffer
	if err := symbolizeWith(strings.NewReader(stderr.String()), &got, nil, m, jsrt.DefaultMain, nil); err != nil {
		t.Fatal(err)
	}
	want := "src/main.ts:" + strconv.Itoa(lineMarked(t, throwingApp, "@throw")) + ":"
	if !strings.Contains(got.String(), want) || strings.Contains(got.String(), "(main.js:") {
		t.Fatalf("symbolized:\n%s\nwant %s", got.String(), want)
	}
	// With the module (the symbol file's) the minified function is
	// explode again.
	if strings.Contains(got.String(), "explode (") {
		t.Fatalf("the minified build kept the name:\n%s", got.String())
	}
	got.Reset()
	if err := symbolizeWith(strings.NewReader(stderr.String()), &got, nil, m, jsrt.DefaultMain, sym.Module); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.String(), "at explode ("+want) {
		t.Fatalf("symbolized with the module:\n%s\nwant explode (%s", got.String(), want)
	}
}

// kario publish sends the symbol file after the package, signed with the
// developer's key over its digest; one of another package is refused
// before anything is sent.
func TestPublishSymbols(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(nil)
	keyFile := filepath.Join(t.TempDir(), "dev.key")
	_ = os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(key.Seed())), 0o600)
	var got []byte
	var gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/submissions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "pending_review", "entry": map[string]string{"id": "com.example.my_app", "version": "0.1"}})
	})
	mux.HandleFunc("POST /v1/symbols/{app}/{version}", func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		gotPath = r.URL.Path
		name, _ := url.PathUnescape(r.Header.Get("X-Developer"))
		ts, _ := strconv.ParseInt(r.Header.Get("X-Time"), 10, 64)
		sig, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-Signature"))
		msg := pkg.SymbolsMsg(name, ts, r.PathValue("app"), r.PathValue("version"), jsrt.SHA256(got))
		if name != "Ex Dev" || !ed25519.Verify(pub, []byte(msg), sig) {
			w.WriteHeader(403)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad signature"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "kept"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	dir := newTestProject(t)
	out := filepath.Join(t.TempDir(), "app.phoneapp")
	if _, err := packageApp(dir, packageOptions{out: out, devStore: t.TempDir(), name: "Ex Dev"}); err != nil {
		t.Fatal(err)
	}
	s, err := publish(out, publishOptions{store: ts.URL, rollout: 100, key: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	sym, _ := os.ReadFile(symbolsPath(out))
	if s.Symbols != "kept" || !bytes.Equal(got, sym) || gotPath != "/v1/symbols/com.example.my_app/0.1" {
		t.Fatalf("symbols %q, sent %d bytes to %s", s.Symbols, len(got), gotPath)
	}
	other, _ := os.ReadFile(symbolsPath(out))
	if _, err := packageApp(dir, packageOptions{out: out, devStore: t.TempDir(), name: "Ex Dev", symbols: "-"}); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(t.TempDir(), "old.phonesym")
	_ = os.WriteFile(stale, other, 0o644)
	got = nil
	if _, err := publish(out, publishOptions{store: ts.URL, rollout: 100, key: keyFile, symbols: stale}); err == nil || !strings.Contains(err.Error(), "another package") || got != nil {
		t.Fatalf("a stale symbol file: %v", err)
	}
	wrong := filepath.Join(t.TempDir(), "other.key")
	_, k2, _ := ed25519.GenerateKey(nil)
	_ = os.WriteFile(wrong, []byte(base64.StdEncoding.EncodeToString(k2.Seed())), 0o600)
	if _, err := packageApp(dir, packageOptions{out: out, devStore: t.TempDir(), name: "Ex Dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := publish(out, publishOptions{store: ts.URL, rollout: 100, key: wrong}); err == nil || !strings.Contains(err.Error(), "was submitted") || !strings.Contains(err.Error(), "bad signature") {
		t.Fatalf("a symbol file signed with another key: %v", err)
	}
}
