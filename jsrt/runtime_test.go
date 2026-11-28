package jsrt

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

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

// zoneApp shows noon UTC on the clock of the zone in force, at the start
// and at each event.
const zoneApp = `import * as kario from "kario";

const noon = () => "noon UTC " + new Date(Date.UTC(2026, 0, 15, 12)).getHours();
const show = (text) => kario.reset({ id: "main", title: "Zone", body: [{ kind: "text", text }] });
kario.on("start", () => show("at the start " + noon()));
kario.on("event", (name, value) => show(name + " " + value + " " + noon()));
`

// The phone's zone changes while the app runs: the runtime puts the new
// one in force (TZ) before the app hears of it (time.zone), as the SDKs
// do, so Date's local time is the new zone's.
func TestTimeZoneEvent(t *testing.T) { testTimeZoneEvent(t, []string{binary(t)}) }

func testTimeZoneEvent(t *testing.T, argv []string) {
	main := filepath.Join(t.TempDir(), "main.js")
	if err := os.WriteFile(main, []byte(zoneApp), 0o644); err != nil {
		t.Fatal(err)
	}
	s := launchWith(t, t.TempDir(), "en", nil, []string{"TZ=Asia/Shanghai"}, argv[0], append(argv[1:], main)...)
	s.hello("", nil)
	s.screenText("at the start noon UTC 20")
	s.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventTimeZone, Value: "America/New_York"})
	s.screenText("time.zone America/New_York noon UTC 7")
	s.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventTimeZone, Value: "UTC"})
	s.screenText("time.zone UTC noon UTC 12")
}

// clockApp shows the time Date reads three ways and a date built from its
// parts, at the start and at each event.
const clockApp = `import * as kario from "kario";

const show = (what) => kario.reset({ id: "main", title: "Clock", body: [{ kind: "text",
  text: what + " now=" + Date.now() + " new=" + new Date().getTime() + " call=" + Date() + " parts=" + new Date(2026, 0, 15).getDate() }] });
kario.on("start", () => show("start"));
kario.on("event", (name, value) => show(name + "=" + value));
`

// A selftest moves the system clock (timeshift, clock-at): Date reads it,
// from the offset at the start (PHONE_CLOCK_OFFSET_MS) and the new one
// before the app hears of it (time.clock); a date given its parts stays.
func TestClockEvent(t *testing.T) { testClockEvent(t, []string{binary(t)}) }

func testClockEvent(t *testing.T, argv []string) {
	main := filepath.Join(t.TempDir(), "main.js")
	if err := os.WriteFile(main, []byte(clockApp), 0o644); err != nil {
		t.Fatal(err)
	}
	s := launchWith(t, t.TempDir(), "en", nil, []string{"TZ=UTC", ui.EnvClockOffset + "=7200000"}, argv[0], append(argv[1:], main)...)
	read := regexp.MustCompile(`now=(\d+) new=(\d+) call=(\w{3} \w{3} \d{2} \d{4} \d{2}:\d{2}:\d{2}).* parts=15`)
	check := func(what, text string, before time.Time, ahead time.Duration) {
		t.Helper()
		after := time.Now()
		m := read.FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("%s: %q", what, text)
		}
		now, _ := strconv.ParseInt(m[1], 10, 64)
		made, _ := strconv.ParseInt(m[2], 10, 64)
		called, err := time.Parse("Mon Jan 02 2006 15:04:05", m[3])
		from, to := before.Add(ahead).Add(-time.Second), after.Add(ahead).Add(time.Second)
		for i, at := range []time.Time{time.UnixMilli(now), time.UnixMilli(made), called} {
			if err != nil || at.Before(from) || at.After(to) {
				t.Errorf("%s: Date read %d is %v, want %v ahead of the machine's clock (%v)", what, i, at, ahead, err)
			}
		}
	}
	before := time.Now()
	s.hello("", nil)
	check("at the start", s.screenText("start now="), before, 2*time.Hour)
	for _, c := range []struct {
		value string
		ahead time.Duration
	}{{"-1800000", -30 * time.Minute}, {"0", 0}} {
		before = time.Now()
		s.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventClock, Value: c.value})
		check("time.clock "+c.value, s.screenText("time.clock="+c.value+" now="), before, c.ahead)
	}
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
	waitStored(t, data, "count", "1")

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
	if !s.logHas("boom", `started {"count":0} com.example.test`) {
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

// Storage errors are ordinary errors: QuickJS-ng lets no script catch an
// InternalError, so a storage failure thrown as one would end the app.
func TestStorageErrorsCatchable(t *testing.T) {
	s := launch(t, "", "en", binary(t), filepath.Join(appCopy(t, "nodata"), "main.js"))
	s.hello("", nil)
	s.screenText("caught storage: the app has no data directory")
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
	if out, err := jsrtCommand(t, bin, "--compile", main, main+"c").CombinedOutput(); err != nil {
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
	if out, err := jsrtCommand(t, bin, "--compile", main, main+"c").CombinedOutput(); err != nil {
		t.Fatalf("compile: %v %s", err, out)
	}
	raw, _ := os.ReadFile(main + "c")
	raw[5] ^= 1
	_ = os.WriteFile(main+"c", raw, 0o644)
	s = launch(t, t.TempDir(), "zh-Hans", bin, main)
	s.hello("", nil)
	s.screenText("Jishu 0")
	if !s.logHas("running the source") {
		t.Fatalf("log:\n%s", s.log())
	}
}

func TestNotUnderShell(t *testing.T) {
	bin := binary(t)
	out, err := jsrtCommand(t, bin, filepath.Join(appCopy(t, "app"), "main.js")).CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 || !strings.Contains(string(out), "not started by phone-shell") {
		t.Fatalf("%v %s", err, out)
	}
}
