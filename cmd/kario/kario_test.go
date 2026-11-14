package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
)

func newTestProject(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "My App")
	if err := newProject(dir, "", ""); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestNewBuildPackage(t *testing.T) {
	dir := newTestProject(t)
	for _, f := range []string{"manifest.json", "src/main.ts", "src/locale/zh-Hans.json", "test/main.test.ts", "tsconfig.json", ".gitignore",
		"types/kario/index.d.ts", "types/kario/ui.d.ts", "types/karioui/index.ts", "types/kariotest/index.ts"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("kario new wrote no %s", f)
		}
	}
	m, _, err := readManifest(dir)
	if err != nil || m.ID != "com.example.my_app" || m.Name != "My App" {
		t.Fatalf("manifest %+v %v", m, err)
	}
	if err := newProject(dir, "", ""); err == nil {
		t.Fatal("kario new wrote over a project")
	}

	r, err := build(dir, buildOptions{minify: true})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "build")
	main, _ := os.ReadFile(filepath.Join(out, "main.js"))
	if r.dir != out || !strings.HasPrefix(string(main), "import") || !strings.Contains(string(main), `from"kario"`) ||
		strings.Contains(string(main), "@kario/ui") || strings.Contains(string(main), ": Screen") || strings.Join(r.locales, " ") != "es zh-Hans zh-Hant" {
		t.Fatalf("build %+v:\n%s", r, main)
	}
	if _, err := build(dir, buildOptions{}); err != nil {
		t.Fatalf("a second build over the first: %v", err)
	}

	store := t.TempDir()
	file := filepath.Join(t.TempDir(), "app.phoneapp")
	if _, err := packageApp(dir, packageOptions{out: file, devStore: store, name: "Dev"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(file)
	p, err := pkg.Open(raw, pkg.LoadRoots(filepath.Join(store, "trust")))
	if err != nil {
		t.Fatal(err)
	}
	if err := jsrt.Check(jsrt.Runtime, "main.js", "", p.Files); err != nil || p.ID != "com.example.my_app" || p.Data["locale/zh-Hans.json"] == nil {
		t.Fatalf("package %s: %v %v", p.ID, err, p.Files)
	}
	if _, err := packageApp(out, packageOptions{out: file, devStore: store, name: "Dev"}); err != nil {
		t.Fatalf("packaging a built directory: %v", err)
	}
	_ = os.WriteFile(filepath.Join(out, "main.jsc"), []byte("KJSC"), 0o644)
	if _, err := packageApp(out, packageOptions{out: file, devStore: store, name: "Dev"}); err == nil || !strings.Contains(err.Error(), "bytecode") {
		t.Fatalf("bytecode packaged: %v", err)
	}
}

func TestBuildRefuses(t *testing.T) {
	for _, c := range []struct {
		name, want string
		fix        func(dir string)
	}{
		{"exec", `no "exec"`, func(dir string) {
			replace(t, dir, "manifest.json", `"main": "main.js",`, `"main": "main.js", "exec": "app",`)
		}},
		{"runtime", `"runtime": "js"`, func(dir string) { replace(t, dir, "manifest.json", `"runtime": "js",`, "") }},
		{"id", "reverse domain", func(dir string) { replace(t, dir, "manifest.json", `"com.example.my_app"`, `"notes"`) }},
		{"permission", "needs a purpose", func(dir string) { replace(t, dir, "manifest.json", `"permissions": []`, `"permissions": ["camera"]`) }},
		{"system permission", "not one an app can declare", func(dir string) {
			replace(t, dir, "manifest.json", `"permissions": []`, `"permissions": ["system"]`)
		}},
		{"privacy label", `"location" permission opens location`, func(dir string) {
			replace(t, dir, "manifest.json", `"permissions": []`,
				`"permissions": ["net", "location"], "purposes": {"location": "Nearby notes"}, "privacy": {}`)
		}},
		{"node module", `Could not resolve "fs"`, func(dir string) {
			_ = os.WriteFile(filepath.Join(dir, "src", "main.ts"), []byte(`import * as fs from "fs"; fs.readFileSync("x");`), 0o644)
		}},
		{"language", "not a language the system offers", func(dir string) {
			_ = os.WriteFile(filepath.Join(dir, "src", "locale", "fr.json"), []byte(`{}`), 0o644)
		}},
		{"two catalogs", "keep one", func(dir string) { _ = os.MkdirAll(filepath.Join(dir, "locale"), 0o755) }},
	} {
		dir := newTestProject(t)
		c.fix(dir)
		if _, err := build(dir, buildOptions{}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	dir := newTestProject(t)
	other := t.TempDir()
	_ = os.WriteFile(filepath.Join(other, "notes.txt"), []byte("mine"), 0o644)
	if _, err := build(dir, buildOptions{out: other}); err == nil {
		t.Fatal("a build overwrote a directory that is not a build")
	}
}

func replace(t *testing.T, dir, file, old, new string) {
	t.Helper()
	p := filepath.Join(dir, file)
	raw, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(raw), old) {
		t.Fatalf("%s lacks %q", file, old)
	}
	_ = os.WriteFile(p, []byte(strings.Replace(string(raw), old, new, 1)), 0o644)
}

// With kario-jsrt at hand, -bytecode writes main.jsc beside main.js.
func TestBuildBytecode(t *testing.T) {
	bin := os.Getenv("KARIO_JSRT")
	if bin == "" {
		bin, _ = filepath.Abs(filepath.Join("..", "..", "build", "jsrt", jsrt.Program))
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("no kario-jsrt (make jsrt)")
	}
	dir := newTestProject(t)
	r, err := build(dir, buildOptions{bytecode: true, jsrt: bin})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "build", "main.jsc"))
	if err != nil || r.bytecode != len(raw) || !strings.HasPrefix(string(raw), "KJSC") {
		t.Fatalf("bytecode %d: %v", r.bytecode, err)
	}
}

// -sourcemap writes main.js.map beside main.js, its sources relative to it,
// so a debugger finds src/main.ts; packages carry none.
func TestBuildSourceMap(t *testing.T) {
	dir := newTestProject(t)
	if _, err := build(dir, buildOptions{sourcemap: true}); err != nil {
		t.Fatal(err)
	}
	main, _ := os.ReadFile(filepath.Join(dir, "build", "main.js"))
	m, err := os.ReadFile(filepath.Join(dir, "build", "main.js.map"))
	if err != nil || !strings.Contains(string(m), `"../src/main.ts"`) || !strings.Contains(string(m), `kario-sdk:@kario/ui`) ||
		!strings.HasSuffix(strings.TrimSpace(string(main)), "//# sourceMappingURL=main.js.map") {
		t.Fatalf("source map %v:\n%.300s", err, m)
	}
	if _, err := build(dir, buildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "build", "main.js.map")); err == nil {
		t.Fatal("a build without -sourcemap kept the map")
	}
}

// node is Node.js with synchronous module hooks, for kario run and test.
func node(t *testing.T) string {
	p, err := findNode("")
	if err != nil {
		t.Skip(err)
	}
	if out, err := exec.Command(p, "-e", `process.exit(typeof require("node:module").registerHooks === "function" ? 0 : 1)`).CombinedOutput(); err != nil {
		t.Skipf("node without module.registerHooks: %v %s", err, out)
	}
	return p
}

// kario test runs the template's tests against the fake Shell, and fails
// when one fails.
func TestKarioTest(t *testing.T) {
	n := node(t)
	dir := newTestProject(t)
	if err := testApp(dir, testOptions{node: n, lang: "en"}); err != nil {
		t.Fatal(err)
	}
	replace(t, dir, "test/main.test.ts", `app.expect("Tapped 2 times")`, `app.expect("Tapped 3 times")`)
	if err := testApp(dir, testOptions{node: n, lang: "en"}); err == nil || err.Error() != "tests failed" {
		t.Fatalf("a failing test: %v", err)
	}
	if err := testApp(dir, testOptions{node: n, lang: "en", run: "outlives"}); err != nil {
		t.Fatalf("-run: %v", err)
	}
	_ = os.RemoveAll(filepath.Join(dir, "test"))
	if err := testApp(dir, testOptions{node: n}); err == nil || !strings.Contains(err.Error(), "no tests") {
		t.Fatalf("no tests: %v", err)
	}
}

// kario run hands the Shell the app and, under Node.js, the runner.
func TestRunArgs(t *testing.T) {
	o := runOptions{inspect: "127.0.0.1:9229", brk: true, device: "bar-t9", preview: "127.0.0.1:8790"}
	runner := nodeRunner("/n/node", "/rt", o)
	if strings.Join(runner, " ") != "/n/node --enable-source-maps --inspect-brk=127.0.0.1:9229 /rt/run.mjs" {
		t.Fatalf("runner %q", runner)
	}
	args := shellArgs("/w/a/build", runner, "/p/phone-paint", o)
	want := `--dev-app /w/a/build --dev-runner ["/n/node","--enable-source-maps","--inspect-brk=127.0.0.1:9229","/rt/run.mjs"] --device bar-t9 --gui /p/phone-paint --preview 127.0.0.1:8790`
	if strings.Join(args, " ") != want {
		t.Fatalf("args %q", args)
	}
	if args := shellArgs("/w/a/build", nil, "", runOptions{}); strings.Join(args, " ") != "--dev-app /w/a/build" {
		t.Fatalf("jsrt args %q", args)
	}
	if _, err := findShell(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Fatal("a -shell that is not there")
	}
	if err := runApp(newTestProject(t), runOptions{engine: "v8"}); err == nil || !strings.Contains(err.Error(), "node or jsrt") {
		t.Fatalf("engine: %v", err)
	}
}

// -watch builds the app again when a source changes and asks for a reload;
// a build that fails asks for none, the next good one does.
func TestWatch(t *testing.T) {
	if err := runApp(newTestProject(t), runOptions{engine: "node", watch: true}); err == nil || !strings.Contains(err.Error(), "-watch needs -preview") {
		t.Fatalf("-watch without -preview: %v", err)
	}
	dir := newTestProject(t)
	out := filepath.Join(t.TempDir(), "build")
	if _, err := build(dir, buildOptions{out: out}); err != nil {
		t.Fatal(err)
	}
	reloads, stop := make(chan bool, 4), make(chan struct{})
	defer close(stop)
	go watch(dir, out, 20*time.Millisecond, func() { reloads <- true }, stop)
	wait := func(want bool) {
		t.Helper()
		select {
		case <-reloads:
			if !want {
				t.Fatal("a reload after a failed build")
			}
		case <-time.After(time.Second):
			if want {
				t.Fatal("no reload")
			}
		}
	}
	time.Sleep(50 * time.Millisecond)
	replace(t, dir, "src/main.ts", "Tapped", "Pressed")
	wait(true)
	if main, _ := os.ReadFile(filepath.Join(out, "main.js")); !strings.Contains(string(main), "Pressed") {
		t.Fatal("main.js was not built again")
	}
	_ = os.WriteFile(filepath.Join(dir, "src", "broken.ts"), []byte("export const x = ;"), 0o644)
	replace(t, dir, "src/main.ts", `import {`, `import "./broken";`+"\nimport {")
	wait(false)
	_ = os.WriteFile(filepath.Join(dir, "src", "broken.ts"), []byte("export const x = 1;"), 0o644)
	wait(true)
}

// kario publish posts the package to the store's submissions and reports
// what the review said, or the store's error.
func TestPublish(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.phoneapp")
	_ = os.WriteFile(file, []byte("signed package"), 0o644)
	var got struct{ path, query, body string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.path, got.query, got.body = r.URL.Path, r.URL.RawQuery, string(raw)
		if string(raw) != "signed package" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"not signed by a developer this store certified"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"review","entry":{"id":"com.example.notes","version":"1.0"}}`))
	}))
	defer srv.Close()
	s, err := publish(file, publishOptions{store: srv.URL + "/", rollout: 10})
	if err != nil || s.Status != "review" || s.Entry.ID != "com.example.notes" || got.path != "/v1/submissions" || got.query != "rollout=10" {
		t.Fatalf("%+v %v %+v", s, err, got)
	}
	if _, err := publish(file, publishOptions{store: srv.URL, rollout: 100}); err != nil || got.query != "" {
		t.Fatalf("full rollout: %v %q", err, got.query)
	}
	_ = os.WriteFile(file, []byte("tampered"), 0o644)
	if _, err := publish(file, publishOptions{store: srv.URL, rollout: 100}); err == nil || err.Error() != "store: not signed by a developer this store certified" {
		t.Fatalf("refused: %v", err)
	}
	if _, err := publish(file, publishOptions{rollout: 100}); err == nil {
		t.Fatal("no store")
	}
}

// The TypeScript apps in this tree pass their own tests: the system's
// calculator and the tutorial's sample (relative paths, as make runs it).
func TestRepoApps(t *testing.T) {
	n := node(t)
	for _, dir := range []string{"../../apps/calc", "../../examples/karioui-notes"} {
		if err := testApp(dir, testOptions{node: n, lang: "en"}); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
		if _, err := build(dir, buildOptions{out: filepath.Join(t.TempDir(), "app"), minify: true}); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
	}
}
