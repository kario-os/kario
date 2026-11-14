package jsrt

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/ui"
)

// appCopy copies testdata/<name> into a fresh directory (bytecode written
// next to it stays out of the tree).
func appCopy(t *testing.T, name string) string {
	t.Helper()
	dst := t.TempDir()
	src := filepath.Join("testdata", name)
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), raw, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func action(id string) ui.Msg {
	return ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: id, Fields: map[string]string{}}
}

// The app's side of the protocol from JavaScript: pages, actions, live
// inputs and canvas keys, capability calls as promises (errors with the
// platform's text), timers, state for a tombstone, popped pages, a Shell
// taking over, and storage that outlives the process.
func TestRuntime(t *testing.T) { testRuntime(t, jsrtEngine(t)) }

// engine starts an app's module as the Shell does, on one of the engines.
type engine func(t *testing.T, dataDir, lang, main string) *shell

func jsrtEngine(t *testing.T) engine {
	bin := binary(t)
	return func(t *testing.T, dataDir, lang, main string) *shell { return launch(t, dataDir, lang, bin, main) }
}

func testRuntime(t *testing.T, start engine) {
	dir, data := appCopy(t, "app"), t.TempDir()
	s := start(t, data, "zh-Hans", filepath.Join(dir, "main.js"))
	s.hello("", nil)
	s.screenText("Jishu 0")

	s.send(action("inc"))
	s.screenText("Jishu 1")
	if raw, err := os.ReadFile(filepath.Join(data, "kario-storage.json")); err != nil || !strings.Contains(string(raw), `"count":"1"`) {
		t.Fatalf("storage file: %s %v", raw, err)
	}

	s.send(action("copy"))
	req := s.expect(ui.MsgReq)
	if req.Cap != ui.CapClipboardSet || string(req.Args) != `{"text":"x"}` {
		t.Fatalf("request %+v", req)
	}
	s.send(ui.Msg{T: ui.MsgRes, Req: req.Req, OK: true, Data: json.RawMessage(`{"done":true}`)})
	s.screenText(`ok {"done":true}`)

	s.send(action("copy"))
	req = s.expect(ui.MsgReq)
	s.send(ui.Msg{T: ui.MsgRes, Req: req.Req, Err: ui.ErrCancelled})
	s.screenText("err cancelled clipboard.set true")

	s.send(action("timer"))
	s.screenText("timer fired")
	s.send(action("cancel"))
	if text := s.screenText("cancel"); strings.Contains(text, "cancelled timer ran") {
		t.Fatalf("a cleared timer ran: %s", text)
	}
	s.send(action("interval"))
	s.screenText("interval 3")

	s.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Node: "q", Action: "changed", Value: "ab"})
	s.screenText("input q=ab")
	s.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Node: "board", Action: ui.ActionKey, Value: "up"})
	s.screenText("key up")
	s.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "fields", Fields: map[string]string{"q": "typed"}})
	s.screenText(`fields {"q":"typed"} main`)

	s.send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateBackground})
	st := s.expect(ui.MsgState)
	var saved struct {
		Count int    `json:"count"`
		Note  string `json:"note"`
	}
	if json.Unmarshal(st.Data, &saved) != nil || saved.Count != 1 {
		t.Fatalf("state %s", st.Data)
	}

	s.send(action("push"))
	if p := s.expect(ui.MsgPush); p.Screen == nil || p.Screen.ID != "second" {
		t.Fatalf("push %+v", p)
	}
	s.send(ui.Msg{T: ui.MsgPopped, ScreenID: "second"})
	s.screenText("popped second")

	s.send(action("push"))
	s.expect(ui.MsgPush)
	s.send(ui.Msg{T: ui.MsgResync})
	if r := s.expect(ui.MsgReset); r.Screen.ID != "main" {
		t.Fatalf("resync reset %+v", r.Screen)
	}
	if p := s.expect(ui.MsgPush); p.Screen.ID != "second" {
		t.Fatalf("resync push %+v", p.Screen)
	}
	s.screenText("resynced")

	s.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventOpen, Value: "deep"})
	s.screenText("event open deep")
	s.send(ui.Msg{T: ui.MsgNotifAction, Value: "n1", Action: "reply", Text: "hi"})
	s.screenText("notif n1 reply hi")

	s.send(action("throw"))
	s.send(action("lang"))
	s.screenText("zh-Hans Nihao 3 of x a and %!s(MISSING)")
	if !strings.Contains(s.log(), "boom") || !strings.Contains(s.log(), `started {"count":0} com.example.test`) {
		t.Fatalf("log:\n%s", s.log())
	}

	s.send(action("big"))
	s.screenText("refused storage: over 1024 KB count")

	s.conn.Close()
	if code := s.exit(); code != 0 {
		t.Fatalf("exit %d when the shell closed; log:\n%s", code, s.log())
	}

	// A new process: storage kept, the state handed back, the deep link.
	s = start(t, data, "zh-Hans", filepath.Join(dir, "main.js"))
	s.hello("", map[string]any{"count": 1, "note": "x"})
	s.screenText(`restored {"count":1,"note":"x"}`)
	s = start(t, data, "zh-Hans", filepath.Join(dir, "main.js"))
	s.hello("open:3", nil)
	s.screenText("arg open:3")
}

// Translations follow the platform's fallback chain: zh-Hant takes what
// its catalog lacks from zh-Hans, every language ends in English.
func TestTranslations(t *testing.T) { testTranslations(t, jsrtEngine(t)) }

func testTranslations(t *testing.T, start engine) {
	dir := appCopy(t, "app")
	for _, c := range []struct{ lang, title, hello string }{
		{"zh-Hant", "Jishuqi (Hant)", "zh-Hant Nihao"},
		{"zh-TW", "Jishuqi (Hant)", "zh-Hant Nihao"},
		{"es", "Counter", "es Hello"},
		{"fr", "Counter", "en Hello"},
		{"", "Jishuqi", "zh-Hans Nihao"},
	} {
		s := start(t, t.TempDir(), c.lang, filepath.Join(dir, "main.js"))
		s.hello("", nil)
		s.screenText(c.title)
		s.send(action("lang"))
		s.screenText(c.hello)
	}
}

// App code cannot reach QuickJS's std and os modules, or any module but
// "kario", or a host global; a static import of either keeps the app from
// starting at all.
func TestNoSystemModules(t *testing.T) {
	bin := binary(t)
	s := launch(t, t.TempDir(), "en", bin, filepath.Join(appCopy(t, "escape"), "main.js"))
	s.hello("", nil)
	s.screenText("nothing reachable")
	testStaticImports(t, jsrtEngine(t))
}

func testStaticImports(t *testing.T, start engine) {
	for _, name := range []string{"static-os", "static-std"} {
		s := start(t, t.TempDir(), "en", filepath.Join(appCopy(t, name), "main.js"))
		if code := s.exit(); code != 1 || !strings.Contains(s.log(), "could not load module") {
			t.Fatalf("%s: exit %d; log:\n%s", name, code, s.log())
		}
	}
}

// Precompiled bytecode runs as the source does; bytecode of another engine
// version falls back to the source beside it.
func TestBytecode(t *testing.T) {
	bin := binary(t)
	dir := appCopy(t, "app")
	main := filepath.Join(dir, "main.js")
	if out, err := exec.Command(bin, "--compile", main, main+"c").CombinedOutput(); err != nil {
		t.Fatalf("compile: %v %s", err, out)
	}
	if err := os.Remove(main); err != nil {
		t.Fatal(err)
	}
	s := launch(t, t.TempDir(), "zh-Hans", bin, main+"c")
	s.hello("", nil)
	s.screenText("Jishu 0")
	s.send(action("timer"))
	s.screenText("timer fired")

	dir = appCopy(t, "app")
	main = filepath.Join(dir, "main.js")
	if out, err := exec.Command(bin, "--compile", main, main+"c").CombinedOutput(); err != nil {
		t.Fatalf("compile: %v %s", err, out)
	}
	raw, _ := os.ReadFile(main + "c")
	raw[5] ^= 1
	_ = os.WriteFile(main+"c", raw, 0o644)
	s = launch(t, t.TempDir(), "zh-Hans", bin, main)
	s.hello("", nil)
	s.screenText("Jishu 0")
	if !strings.Contains(s.log(), "running the source") {
		t.Fatalf("log:\n%s", s.log())
	}
}

func TestNotUnderShell(t *testing.T) {
	bin := binary(t)
	out, err := exec.Command(bin, filepath.Join(appCopy(t, "app"), "main.js")).CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 || !strings.Contains(string(out), "not started by phone-shell") {
		t.Fatalf("%v %s", err, out)
	}
}
