package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
	if s := nextSteps("apps/My App", "My App", "com.example.my_app"); !strings.HasPrefix(s, "apps/My App: My App (com.example.my_app). Next:\n") ||
		!strings.Contains(s, "kario run -preview 127.0.0.1:8790 'apps/My App'") || !strings.Contains(s, "kario test 'apps/My App'\n") {
		t.Fatalf("next steps:\n%s", s)
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
	if err := jsrt.Check(jsrt.App{Runtime: jsrt.Runtime, Main: "main.js"}, p.Files, jsrt.FromData(p.Data)); err != nil || p.ID != "com.example.my_app" || p.Data["locale/zh-Hans.json"] == nil {
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

// netApp is an app of the network: a WebSocket on start, and an upload
// of a FormData through a Request.
const netApp = `import * as kario from "kario";

const log: string[] = [];
const show = () =>
  kario.reset({ id: "main", title: "Chat", body: log.map((text) => ({ kind: "text" as const, text })), actions: [{ id: "upload", label: "Upload" }] });

kario.on("start", () => {
  const ws = new kario.WebSocket("wss://chat.example.com/live?room=1", ["chat.v2", "chat.v1"], { headers: { "X-Token": "t1" } });
  ws.onopen = () => {
    log.push("open " + ws.protocol);
    ws.send("hello");
    show();
  };
  ws.onmessage = (e) => {
    log.push(typeof e.data === "string" ? "got " + e.data : "bytes " + e.data.byteLength);
    if (e.data === "bye?") ws.close(1000, "done");
    show();
  };
  ws.onerror = (e) => {
    log.push("error " + e.message);
    show();
  };
  ws.onclose = (e) => {
    log.push("closed " + e.code + " " + e.wasClean);
    show();
  };
  show();
});

kario.on("action", async (e) => {
  if (e.action !== "upload") return;
  const form = new kario.FormData();
  form.append("note", "hi");
  form.append("photo", new kario.Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }), "p.png");
  const res = await kario.fetch(new kario.Request("https://api.example.com/upload", { method: "POST", body: form }));
  const body = (await res.json()) as { ok: boolean };
  log.push("upload " + res.status + " " + res.headers.get("x-id") + " " + res.headers["x-id"] + " " + body.ok + " " + (res instanceof kario.Response));
  show();
});
`

const netTests = `import { expectEqual, launch, test } from "@kario/test";

test("the app's WebSocket talks to the test's server", async () => {
  const app = await launch({ websocket: (req) => ({ protocol: req.protocols.includes("chat.v2") ? "chat.v2" : undefined }) });
  const ws = await app.websocket();
  expectEqual([ws.url, ws.protocols, ws.headers["x-token"]], ["wss://chat.example.com/live?room=1", ["chat.v2", "chat.v1"], "t1"], "the handshake");
  expectEqual(await ws.next(), "hello", "the app's first message");
  await app.wait("open chat.v2");
  ws.send("welcome");
  ws.send(new Uint8Array([1, 2, 3, 4]));
  ws.ping("p");
  await app.wait("bytes 4");
  app.expect("got welcome");
  ws.send("bye?");
  expectEqual(await ws.closed, { code: 1000, reason: "done", clean: true }, "the app's close");
  await app.wait("closed 1000 true");
  expectEqual(app.websockets.length, 1, "connections");
});

test("a refused handshake, a dropped connection, the server's close", async () => {
  const refused = await launch({ websocket: () => ({ status: 403 }) });
  await refused.wait("closed 1006 false");
  refused.expect("error ");
  const dropped = await launch();
  (await dropped.websocket()).drop();
  await dropped.wait("closed 1006 false");
  const app = await launch();
  const ws = await app.websocket();
  await ws.next();
  await ws.close(4001, "go away");
  await app.wait("closed 4001 true");
});

test("a FormData through a Request, and fetch's Response", async () => {
  const app = await launch({ fetch: () => ({ status: 201, headers: { "X-Id": "7", "Content-Type": "application/json" }, body: '{"ok":true}' }) });
  await app.choose("Upload");
  await app.wait("upload 201 7 7 true true");
  const req = app.fetches[0];
  const type = Object.entries(req.headers).find(([k]) => k.toLowerCase() === "content-type")?.[1] ?? "";
  const body = new TextDecoder().decode(req.body);
  if (req.method !== "POST" || !type.startsWith("multipart/form-data; boundary=") || !body.includes('name="photo"; filename="p.png"') || !body.includes("Content-Type: image/png"))
    throw new Error(req.method + " " + type + "\n" + body);
});
`

// kario test plays the server for an app's WebSocket (the handshake's
// URL, protocols and headers; messages both ways, a ping, closes from
// either side, a refused handshake, a dropped connection) and hands a
// FormData body over as multipart, with fetch's answer a Response.
func TestKarioTestNetwork(t *testing.T) {
	n := node(t)
	dir := newTestProject(t)
	replace(t, dir, "manifest.json", `"permissions": []`, `"permissions": ["net"]`)
	if err := os.WriteFile(filepath.Join(dir, "src", "main.ts"), []byte(netApp), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "test", "main.test.ts"))
	if err := os.WriteFile(filepath.Join(dir, "test", "net.test.ts"), []byte(netTests), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := testApp(dir, testOptions{node: n, lang: "en"}); err != nil {
		t.Fatal(err)
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

// In make build's layout kario finds the staged Shell (build/usr/bin), not
// the program go build also leaves beside it.
func TestLookShell(t *testing.T) {
	t.Setenv("KARIO_SHELL", "")
	root := t.TempDir()
	exe := filepath.Join(root, "build", "bin", "kario")
	staged := filepath.Join(root, "build", "usr", "bin", "phone-shell")
	beside := filepath.Join(root, "build", "bin", "phone-shell")
	for _, p := range []string{exe, beside, staged} {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, nil, 0o755)
	}
	if got, err := lookShell("", exe); err != nil || filepath.Clean(got) != staged {
		t.Fatalf("make build's layout: %s %v", got, err)
	}
	_ = os.Remove(staged)
	if got, err := lookShell("", exe); err != nil || got != beside {
		t.Fatalf("beside kario: %s %v", got, err)
	}
	if got, err := lookShell(staged, exe); err == nil {
		t.Fatalf("a -shell that is not there: %s", got)
	}
}

// Node.js older than 22.15 is refused with the version it is, not left to
// fail on the runtime's flags.
func TestFindNodeVersion(t *testing.T) {
	t.Setenv("KARIO_NODE", "")
	dir := t.TempDir()
	fake := func(name, version string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte("#!/bin/sh\necho "+version+"\n"), 0o755)
		return p
	}
	if _, err := findNode(fake("node18", "v18.0.0")); err == nil || !strings.Contains(err.Error(), "is Node.js v18.0.0: apps run on Node.js 22.15 or newer") {
		t.Fatalf("Node.js 18: %v", err)
	}
	for _, v := range []string{"v22.15.0", "v24.14.0"} {
		if p, err := findNode(fake("node-"+v, v)); err != nil || p == "" {
			t.Fatalf("Node.js %s: %v", v, err)
		}
	}
	t.Setenv("KARIO_NODE", fake("node22", "v22.14.1"))
	if _, err := findNode(""); err == nil {
		t.Fatal("$KARIO_NODE 22.14")
	}
	for v, want := range map[string]bool{"v22.15.0": true, "v23.0.0": true, "v22.9.0": false, "v8.0.0": false, "22.15.0": false} {
		if nodeAtLeast(v, 22, 15) != want {
			t.Errorf("%s: %v", v, !want)
		}
	}
}

// kario run takes an app written in C, C++, Rust or Go as it is: a
// manifest with "exec" and the program built beside it.
func TestNativeProgram(t *testing.T) {
	if p, err := nativeProgram(newTestProject(t)); err != nil || p != "" {
		t.Fatalf("a TypeScript project: %q %v", p, err)
	}
	dir := t.TempDir()
	app := func(manifest string) {
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644)
	}
	app(`{"id": "com.example.hello", "name": "Hello", "exec": "hello"}`)
	if _, err := nativeProgram(dir); err == nil || !strings.Contains(err.Error(), "build the app's program first") {
		t.Fatalf("not built: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "hello"), []byte("source"), 0o644)
	if _, err := nativeProgram(dir); err == nil || !strings.Contains(err.Error(), "not an executable program") {
		t.Fatalf("not executable: %v", err)
	}
	_ = os.Chmod(filepath.Join(dir, "hello"), 0o755)
	if p, err := nativeProgram(dir); err != nil || p != filepath.Join(dir, "hello") {
		t.Fatalf("built: %q %v", p, err)
	}
	for _, c := range []struct{ manifest, want string }{
		{`{"id": "com.example.hello", "name": "Hello", "exec": "bin/hello"}`, "a program beside manifest.json"},
		{`{"id": "hello", "name": "Hello", "exec": "hello"}`, "reverse domain"},
		{`{"id": "com.example.hello", "exec": "hello"}`, "needs a name"},
	} {
		app(c.manifest)
		if _, err := nativeProgram(dir); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.manifest, err, c.want)
		}
	}
	app(`{"id": "com.example.hello", "name": "Hello", "exec": "hello"}`)
	for _, c := range []struct {
		o    runOptions
		want string
	}{
		{runOptions{engine: "jsrt"}, "-engine"},
		{runOptions{engine: "node", inspect: "127.0.0.1:9229"}, "gdb -p"},
		{runOptions{engine: "node", preview: "127.0.0.1:0", watch: true}, "-watch"},
		{runOptions{engine: "node", native: dir}, "-native"},
	} {
		if err := runApp(dir, c.o); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.o, err, c.want)
		}
	}
}

// kario run of a Go app on the developer machine's Shell: not built, its
// own program started from its directory; the timeline has its pages.
func TestRunNativeApp(t *testing.T) {
	shell, paint := stagedShell(t)
	dir := filepath.Join(t.TempDir(), "notes")
	if msg, err := exec.Command("go", "build", "-o", filepath.Join(dir, "notes"), "../../examples/notes").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, msg)
	}
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id": "com.example.gonotes", "name": "Go notes", "exec": "notes"}`), 0o644)
	file := filepath.Join(t.TempDir(), "trace.json")
	addr := freeAddr(t)
	done := make(chan error, 1)
	go func() {
		done <- runApp(dir, runOptions{engine: "node", shell: shell, gui: paint, preview: addr, profile: file, device: "bar-t9"})
	}()
	up := false
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end) && !up; time.Sleep(100 * time.Millisecond) {
		if r, err := http.Get("http://" + addr + "/"); err == nil {
			r.Body.Close()
			up = true
		}
	}
	if !up {
		t.Fatal("the preview did not come up")
	}
	time.Sleep(3 * time.Second)
	_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("kario run did not stop")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var tf struct {
		TraceEvents []struct{ Name, Cat, Ph string } `json:"traceEvents"`
		OtherData   map[string]string                `json:"otherData"`
	}
	if err := json.Unmarshal(raw, &tf); err != nil {
		t.Fatal(err)
	}
	reset := false
	for _, e := range tf.TraceEvents {
		reset = reset || e.Ph == "X" && e.Cat == "page" && strings.HasPrefix(e.Name, "reset")
	}
	if !reset || tf.OtherData["app"] != "com.example.gonotes" || tf.OtherData["engine"] != "native" {
		t.Fatalf("the app's page in the trace: %v %v", reset, tf.OtherData)
	}
	if _, err := os.Stat(filepath.Join(dir, "build")); err == nil {
		t.Fatal("kario run built a native app")
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
	go watch(dir, buildOptions{out: out, quiet: true}, 20*time.Millisecond, func() { reloads <- true }, stop)
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
// calculator and calendar, the tutorial's sample and the native module's
// sample, its library built for this machine (relative paths, as make
// runs it).
func TestRepoApps(t *testing.T) {
	n := node(t)
	native := ""
	if jsrt.HostArch() != "" {
		native = t.TempDir()
		buildExampleModule(t, native)
	}
	for _, dir := range []string{"../../apps/calc", "../../apps/calendar", "../../examples/karioui-notes", "../../examples/imgproc"} {
		if err := testApp(dir, testOptions{node: n, lang: "en", native: native}); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
		if _, err := build(dir, buildOptions{out: filepath.Join(t.TempDir(), "app"), minify: true, native: native}); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
	}
}
