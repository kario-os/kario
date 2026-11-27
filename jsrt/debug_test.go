package jsrt

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// debugConn is the computer's end of kario-jsrt's debugger socket
// (src/debug.c): requests and their replies, and the events in order.
type debugConn struct {
	t      *testing.T
	f      *os.File
	mu     sync.Mutex
	next   int
	wait   map[int]chan map[string]any
	events chan map[string]any
}

func newDebugConn(t *testing.T, f *os.File) *debugConn {
	d := &debugConn{t: t, f: f, wait: map[int]chan map[string]any{}, events: make(chan map[string]any, 64)}
	go func() {
		r := bufio.NewReader(f)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				close(d.events)
				return
			}
			var m map[string]any
			if err := json.Unmarshal(line, &m); err != nil {
				t.Errorf("debugger line %q: %v", line, err)
				continue
			}
			if _, ok := m["event"]; ok {
				d.events <- m
				continue
			}
			id, _ := m["id"].(float64)
			d.mu.Lock()
			ch := d.wait[int(id)]
			delete(d.wait, int(id))
			d.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}()
	t.Cleanup(func() { f.Close() })
	return d
}

// call sends a request and waits for its reply; an error reply fails the
// test unless failing.
func (d *debugConn) call(cmd string, args map[string]any) map[string]any {
	d.t.Helper()
	m, err := d.try(cmd, args)
	if err != nil {
		d.t.Fatalf("%s: %v", cmd, err)
	}
	return m
}

func (d *debugConn) try(cmd string, args map[string]any) (map[string]any, error) {
	d.mu.Lock()
	d.next++
	id := d.next
	ch := make(chan map[string]any, 1)
	d.wait[id] = ch
	d.mu.Unlock()
	req := map[string]any{"id": id, "cmd": cmd}
	for k, v := range args {
		req[k] = v
	}
	raw, _ := json.Marshal(req)
	if _, err := d.f.Write(append(raw, '\n')); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if e, ok := m["error"].(string); ok {
			return m, fmt.Errorf("%s", e)
		}
		return m, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("no reply to %s", cmd)
	}
}

// event waits for the next event of the kind, skipping output.
func (d *debugConn) event(kind string) map[string]any {
	d.t.Helper()
	for {
		select {
		case m, ok := <-d.events:
			if !ok {
				d.t.Fatalf("the debugger socket closed waiting for %s", kind)
			}
			if m["event"] == kind {
				return m
			}
			if m["event"] != "output" {
				d.t.Fatalf("event %v, want %s", m, kind)
			}
		case <-time.After(5 * time.Second):
			d.t.Fatalf("no %s event", kind)
		}
	}
}

// hangUp is the computer going: shut down at once, as Close alone waits
// for the reader's blocked read.
func (d *debugConn) hangUp() {
	_ = syscall.Shutdown(int(d.f.Fd()), syscall.SHUT_RDWR)
	d.f.Close()
}

// noStop checks that no stopped event came.
func (d *debugConn) noStop() {
	d.t.Helper()
	for {
		select {
		case m := <-d.events:
			if m["event"] == "stopped" {
				d.t.Fatalf("stopped: %v", m)
			}
		default:
			return
		}
	}
}

// stopped waits for a stop and returns its reason and the top frame.
func (d *debugConn) stopped(reason string) map[string]any {
	d.t.Helper()
	ev := d.event("stopped")
	if ev["reason"] != reason {
		d.t.Fatalf("stopped for %v (%v), want %s", ev["reason"], ev["text"], reason)
	}
	return ev
}

func (d *debugConn) frames() []map[string]any {
	d.t.Helper()
	var out []map[string]any
	for _, f := range d.call("stack", nil)["frames"].([]any) {
		out = append(out, f.(map[string]any))
	}
	return out
}

// at checks the innermost frame is fn at line.
func (d *debugConn) at(fn string, line int) {
	d.t.Helper()
	f := d.frames()[0]
	if f["name"] != fn || f["line"] != float64(line) || f["file"] != "main.js" {
		d.t.Fatalf("at %v, want %s main.js:%d", f, fn, line)
	}
}

// vars are a scope's variables by name: their values as shown.
func (d *debugConn) vars(args map[string]any) map[string]map[string]any {
	d.t.Helper()
	out := map[string]map[string]any{}
	for _, v := range d.call("variables", args)["variables"].([]any) {
		m := v.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

func lineOf(t *testing.T, src, marker string) int {
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, "// "+marker) {
			return i + 1
		}
	}
	t.Fatalf("no line marked %s", marker)
	return 0
}

// startDebug runs testdata/debug under the debugger, as the Shell starts
// an app installed in developer mode that the computer debugs.
func startDebug(t *testing.T) (*shell, *debugConn, string) {
	bin := binary(t)
	dir := appCopy(t, "debug")
	src, _ := os.ReadFile(filepath.Join(dir, "main.js"))
	ours, theirs := socketPair(t)
	s := launchWith(t, t.TempDir(), "en", []*os.File{theirs}, []string{"KARIO_JSRT_DEBUG_FD=4"}, bin, filepath.Join(dir, "main.js"))
	d := newDebugConn(t, ours)
	hello := d.event("hello")
	if hello["main"] != "main.js" || hello["app"] != "com.example.test" || !strings.Contains(hello["engine"].(string), "+kario") {
		t.Fatalf("hello %v", hello)
	}
	return s, d, string(src)
}

// The debugger on the device: line breakpoints (with conditions), the
// stack with positions, arguments, locals and closure variables, objects
// opened by reference, evaluating in a frame (and writing back what it
// assigns), step over, in and out, debugger statements, uncaught
// exceptions (caught ones run on), pausing, console output, and the app
// running on once the computer goes.
func TestDebugger(t *testing.T) {
	s, d, src := startDebug(t)
	line := func(m string) int { return lineOf(t, src, m) }

	// Nothing runs before "run": breakpoints are set first.
	if bps := d.call("setBreakpoints", map[string]any{"file": "main.js", "breakpoints": []any{map[string]any{"line": line("@sum")}}})["breakpoints"].([]any); len(bps) != 1 || bps[0].(map[string]any)["verified"] != true {
		t.Fatalf("breakpoints %v", bps)
	}
	d.call("setExceptions", map[string]any{"uncaught": true})
	if _, err := d.try("stack", nil); err == nil {
		t.Fatal("a stack before any pause")
	}
	d.call("run", nil)
	s.hello("", nil)
	s.screenText("n 0")

	s.send(action("press"))
	d.stopped("breakpoint")
	fr := d.frames()
	if len(fr) < 3 || fr[1]["name"] != "press" || fr[1]["line"] != float64(line("@call")) || fr[2]["line"] != float64(line("@dispatch")) {
		t.Fatalf("frames %v", fr)
	}
	d.at("total", line("@sum"))
	loc := d.vars(map[string]any{"frame": 0, "scope": "local"})
	if loc["a"]["value"] != "1" || loc["b"]["value"] != "10" || loc["a"]["type"] != "number" {
		t.Fatalf("locals %v", loc)
	}
	if _, ok := loc["sum"]; ok {
		t.Fatalf("sum shown before it is initialized: %v", loc)
	}
	cl := d.vars(map[string]any{"frame": 1, "scope": "closure"})
	if cl["presses"]["value"] != "1" || cl["total"]["type"] != "function" || cl["greeting"]["value"] != `"hi"` {
		t.Fatalf("closure of press %v", cl)
	}
	if r := d.call("evaluate", map[string]any{"frame": 0, "expression": "a + b"}); r["value"] != "11" {
		t.Fatalf("evaluate %v", r)
	}
	if r := d.call("evaluate", map[string]any{"frame": 1, "expression": "presses * 100"}); r["value"] != "100" {
		t.Fatalf("evaluate in press %v", r)
	}
	if _, err := d.try("evaluate", map[string]any{"frame": 0, "expression": "nosuch.x"}); err == nil || !strings.Contains(err.Error(), "nosuch") {
		t.Fatalf("evaluate of an unknown name: %v", err)
	}
	// An assignment in the frame changes the program's variable.
	d.call("evaluate", map[string]any{"frame": 0, "expression": "a = 5"})

	d.call("next", nil)
	d.stopped("step")
	d.at("total", line("@return"))
	if loc = d.vars(map[string]any{"frame": 0, "scope": "local"}); loc["sum"]["value"] != "15" {
		t.Fatalf("after a = 5, sum %v", loc["sum"])
	}
	if _, err := d.try("setVariable", map[string]any{"frame": 0, "scope": "local", "name": "sum", "value": "20"}); err == nil || !strings.Contains(err.Error(), "constant") {
		t.Fatalf("set a const: %v", err)
	}
	if r := d.call("setVariable", map[string]any{"frame": 0, "scope": "local", "name": "a", "value": "a * 20"}); r["value"] != "100" {
		t.Fatalf("setVariable %v", r)
	}
	if loc = d.vars(map[string]any{"frame": 0, "scope": "local"}); loc["a"]["value"] != "100" {
		t.Fatalf("a after setVariable %v", loc["a"])
	}

	// Stepping over the return comes out in the caller.
	d.call("next", nil)
	d.stopped("step")
	d.at("press", line("@call"))
	d.call("next", nil)
	d.stopped("step")
	d.at("press", line("@info"))
	if loc = d.vars(map[string]any{"frame": 0, "scope": "local"}); loc["t"]["value"] != "30" || loc["e"]["type"] != "object" {
		t.Fatalf("locals of press %v", loc)
	}
	d.call("next", nil)
	d.stopped("step")
	d.at("press", line("@update"))
	info := d.vars(map[string]any{"frame": 0, "scope": "local"})["info"]
	props := d.vars(map[string]any{"ref": info["ref"]})
	if props["t"]["value"] != "30" || props["list"]["value"] != "Array(3)" || props["greeting"]["value"] != `"hi"` {
		t.Fatalf("info's properties %v", props)
	}
	if list := d.vars(map[string]any{"ref": props["list"]["ref"]}); list["2"]["value"] != "3" || list["length"]["value"] != "3" {
		t.Fatalf("list %v", list)
	}

	// In and out of screen().
	d.call("stepIn", nil)
	d.stopped("step")
	d.at("screen", line("@screen"))
	d.call("stepOut", nil)
	d.stopped("step")
	d.at("press", line("@update"))
	d.call("continue", nil)
	s.screenText("n 30")

	// A debugger statement stops; a condition that does not hold does not.
	s.send(action("stop"))
	d.stopped("debugger")
	d.at("", line("@debugger"))
	d.call("continue", nil)
	d.call("setBreakpoints", map[string]any{"file": "main.js", "breakpoints": []any{}})
	d.call("setBreakpoints", map[string]any{"file": "main.js", "breakpoints": []any{map[string]any{"line": line("@inc"), "condition": "presses > 5"}}})
	s.send(action("press"))
	s.screenText("n 24")
	d.noStop()
	d.call("setBreakpoints", map[string]any{"file": "main.js", "breakpoints": []any{map[string]any{"line": line("@inc"), "condition": "presses === 2"}}})
	s.send(action("press"))
	d.stopped("breakpoint")
	d.at("press", line("@inc"))
	d.call("setBreakpoints", map[string]any{"file": "main.js", "breakpoints": []any{}})
	d.call("continue", nil)
	s.screenText("n 26")

	// A caught exception runs on; an uncaught one stops where it is thrown.
	s.send(action("caught"))
	s.screenText("n caught")
	d.noStop()
	s.send(action("throw"))
	ev := d.stopped("exception")
	if !strings.Contains(ev["text"].(string), "boom 3") {
		t.Fatalf("exception %v", ev)
	}
	d.at("", line("@throw"))
	d.call("continue", nil)
	if !s.logHas("boom 3") {
		t.Fatalf("the uncaught exception is not in the log:\n%s", s.log())
	}

	// Console output goes to the debugger too, as do error reports.
	s.send(action("log"))
	for seen := []string{}; ; {
		ev := d.event("output")
		seen = append(seen, ev["category"].(string)+": "+ev["text"].(string))
		if ev["category"] == "console" {
			if ev["text"] != "presses 3\n" || !strings.Contains(strings.Join(seen, ""), "stderr: uncaught exception") && !strings.Contains(strings.Join(seen, ""), "boom 3") {
				t.Fatalf("output %q", seen)
			}
			break
		}
	}

	// Pausing an idle app stops in the next code it runs.
	d.call("pause", nil)
	s.send(action("press"))
	d.stopped("pause")
	if g := d.vars(map[string]any{"scope": "global"}); g["globalThis"]["ref"] == float64(0) {
		t.Fatalf("global %v", g)
	}
	d.call("continue", nil)
	s.screenText("n 28")

	// Running, the debugger evaluates in the global scope only.
	if r := d.call("evaluate", map[string]any{"expression": "typeof console.log"}); r["value"] != `"function"` {
		t.Fatalf("global evaluate %v", r)
	}

	// The computer goes: the app runs on, breakpoints gone.
	d.call("setBreakpoints", map[string]any{"file": "main.js", "breakpoints": []any{map[string]any{"line": line("@sum")}}})
	d.f.Close()
	time.Sleep(50 * time.Millisecond)
	s.send(action("press"))
	s.screenText("n 30")
}

// stopOnEntry stops before the module's first line.
func TestDebuggerEntry(t *testing.T) {
	s, d, _ := startDebug(t)
	d.call("run", map[string]any{"stopOnEntry": true})
	d.stopped("entry")
	f := d.frames()
	if len(f) != 1 || f[0]["file"] != "main.js" {
		t.Fatalf("entry frames %v", f)
	}
	d.call("continue", nil)
	s.hello("", nil)
	s.screenText("n 0")
}

// Without the variable, nothing changes for an app: no debugger, no wait.
func TestNoDebugger(t *testing.T) {
	s := launch(t, t.TempDir(), "en", binary(t), filepath.Join(appCopy(t, "debug"), "main.js"))
	s.hello("", nil)
	s.screenText("n 0")
	s.send(action("stop"))
	s.send(action("press"))
	s.screenText("n 22")
}

// Hit counts, logpoints and function breakpoints: a breakpoint stops from
// its Nth hit, only on the Nth or every Nth; the breakpoints of one group
// count together; a logpoint writes its message with the values in it and
// runs on; a function breakpoint stops as the function is entered (with
// its arguments there), by name or Class.name, under its condition.
func TestDebuggerBreakpointKinds(t *testing.T) {
	s, d, src := startDebug(t)
	line := func(m string) int { return lineOf(t, src, m) }
	bps := func(list ...map[string]any) []any {
		t.Helper()
		all := []any{}
		for _, b := range list {
			all = append(all, b)
		}
		return d.call("setBreakpoints", map[string]any{"file": "main.js", "breakpoints": all})["breakpoints"].([]any)
	}
	if got := bps(map[string]any{"line": line("@inc"), "hitCondition": "every 2"}); got[0].(map[string]any)["verified"] != false || got[0].(map[string]any)["message"] == nil {
		t.Fatalf("a hit count that is none: %v", got)
	}
	bps(map[string]any{"line": line("@inc"), "hitCondition": "3"})
	d.call("run", nil)
	s.hello("", nil)
	s.screenText("n 0")
	press := func(want string) {
		t.Helper()
		s.send(action("press"))
		if want != "" {
			s.screenText(want)
		}
	}
	press("n 22")
	press("n 24")
	d.noStop()
	press("")
	d.stopped("breakpoint")
	if v := d.vars(map[string]any{"frame": 1, "scope": "closure"})["presses"]["value"]; v != "2" {
		t.Fatalf("stopped with presses %v, want the third press (2 before it)", v)
	}
	d.call("continue", nil)
	s.screenText("n 26")
	press("")
	d.stopped("breakpoint")
	d.call("continue", nil)
	s.screenText("n 28")

	// "=N": only the Nth; "%N": every Nth.
	bps(map[string]any{"line": line("@inc"), "hitCondition": "==2"})
	press("n 30")
	press("")
	d.stopped("breakpoint")
	d.call("continue", nil)
	s.screenText("n 32")
	press("n 34")
	d.noStop()
	bps(map[string]any{"line": line("@inc"), "hitCondition": "%2"})
	press("n 36")
	press("")
	d.stopped("breakpoint")
	d.call("continue", nil)
	s.screenText("n 38")

	// Two lines of one group share their count: the second hit is the
	// second line of the first press.
	bps(map[string]any{"line": line("@inc"), "group": 0, "hitCondition": "=2"}, map[string]any{"line": line("@call"), "group": 0, "hitCondition": "=2"})
	press("")
	d.stopped("breakpoint")
	d.at("press", line("@call"))
	d.call("continue", nil)
	s.screenText("n 40")

	// A logpoint says what its expressions are and runs on; one that throws
	// says so in its place.
	bps(map[string]any{"line": line("@call"), "logMessage": "press {presses} twice {presses * 2} {nosuch} {({a: 1}).a}", "condition": "presses > 11"})
	press("n 42")
	press("n 44")
	var logged map[string]any
	for logged == nil {
		ev := d.event("output")
		if ev["category"] == "console" {
			logged = ev
		}
	}
	if text := logged["text"].(string); !strings.HasPrefix(text, "press 12 twice 24 ReferenceError: ") || !strings.HasSuffix(text, " 1\n") || strings.Count(text, "\n") != 1 ||
		logged["file"] != "main.js" || logged["line"] != float64(line("@call")) {
		t.Fatalf("logpoint %v", logged)
	}
	d.noStop()
	bps()

	// Function breakpoints.
	if got := d.call("setFunctionBreakpoints", map[string]any{"breakpoints": []any{map[string]any{"name": "total", "condition": "a === 14"}}})["breakpoints"].([]any); got[0].(map[string]any)["verified"] != true {
		t.Fatalf("function breakpoints %v", got)
	}
	press("n 46")
	d.noStop()
	press("")
	d.stopped("function breakpoint")
	f := d.frames()
	if f[0]["name"] != "total" || f[1]["name"] != "press" {
		t.Fatalf("stopped in %v", f)
	}
	if loc := d.vars(map[string]any{"frame": 0, "scope": "local"}); loc["a"]["value"] != "14" || loc["b"]["value"] != "10" {
		t.Fatalf("arguments on entry %v", loc)
	}
	d.call("next", nil)
	d.stopped("step")
	d.at("total", line("@sum"))
	d.call("continue", nil)
	s.screenText("n 48")
	d.call("setFunctionBreakpoints", map[string]any{"breakpoints": []any{map[string]any{"name": "Counter.screen", "hitCondition": "2"}}})
	press("n 50")
	d.noStop()
	press("")
	d.stopped("function breakpoint")
	d.at("screen", line("@screen"))
	d.call("setFunctionBreakpoints", map[string]any{"breakpoints": []any{}})
	d.call("continue", nil)
	s.screenText("n 52")
	press("n 54")
	d.noStop()
}

// seqpacketPair is the pair the Shell hands a debugger's socket over.
func seqpacketPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	return os.NewFile(uintptr(fds[0]), "attach-parent"), os.NewFile(uintptr(fds[1]), "attach-child")
}

// handOver attaches a debugger to the running app, as the Shell does: one
// end of a new socket pair over the app's attach socket.
func handOver(t *testing.T, port *os.File) *debugConn {
	t.Helper()
	ours, theirs := socketPair(t)
	defer theirs.Close()
	if err := syscall.Sendmsg(int(port.Fd()), []byte{0}, syscall.UnixRights(int(theirs.Fd())), nil, 0); err != nil {
		t.Fatal(err)
	}
	return newDebugConn(t, ours)
}

// A debugger attaches to an app that is running, without a restart: the
// app's state stays, breakpoints stop on a statement with no call or
// operator in it (the module was compiled with every statement's position
// for it), one debugger at a time, and once it goes another attaches.
func TestDebuggerAttach(t *testing.T) {
	bin := binary(t)
	dir := appCopy(t, "debug")
	src, _ := os.ReadFile(filepath.Join(dir, "main.js"))
	ours, theirs := seqpacketPair(t)
	defer ours.Close()
	s := launchWith(t, t.TempDir(), "en", []*os.File{theirs}, []string{DebugAttachFDEnv + "=4"}, bin, filepath.Join(dir, "main.js"))
	s.hello("", nil)
	s.screenText("n 0")
	s.send(action("press"))
	s.screenText("n 22")

	d := handOver(t, ours)
	if hello := d.event("hello"); hello["attached"] != true || hello["main"] != "main.js" {
		t.Fatalf("hello %v", hello)
	}
	d.call("setBreakpoints", map[string]any{"file": "main.js", "breakpoints": []any{map[string]any{"line": lineOf(t, string(src), "@assign")}}})
	d.call("run", nil)
	// A second debugger meanwhile is turned away.
	second := handOver(t, ours)
	select {
	case _, ok := <-second.events:
		if ok {
			t.Fatal("a second debugger got an event")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second debugger was not turned away")
	}
	s.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "assign", Value: "7"})
	d.stopped("breakpoint")
	d.at("assign", lineOf(t, string(src), "@assign"))
	if cl := d.vars(map[string]any{"frame": 1, "scope": "closure"}); len(cl) == 0 {
		t.Fatalf("no closure variables")
	}
	if g := d.call("evaluate", map[string]any{"frame": 0, "expression": "presses"}); g["value"] != "1" {
		t.Fatalf("the app restarted: presses %v", g)
	}
	d.call("continue", nil)
	s.screenText("n 7")
	d.hangUp()
	time.Sleep(50 * time.Millisecond)
	s.send(action("press"))
	s.screenText("n 36")

	d = handOver(t, ours)
	d.event("hello")
	d.call("run", map[string]any{"stopOnEntry": true})
	s.send(action("press"))
	d.stopped("pause")
	d.call("continue", nil)
	s.screenText("n 38")
}

const lazyApp = `import { on, reset } from "kario";
class Cart {
  constructor() { this.items = new Map(); this.tags = new Set(); this.list = []; }
  get total() { let t = 0; for (const v of this.items.values()) t += v; return t; }
  get broken() { throw new Error("no total"); }
}
const cart = new Cart();
for (let i = 0; i < 1200; i++) { cart.items.set("k" + i, i); cart.tags.add(i); cart.list.push(i * 2); }
on("start", () => {
  debugger;
  reset({ id: "main", title: "after " + cart.list.length, body: [] });
});
`

// A getter (a class's, on the prototype) shows as lazy and is read, with
// the object as this, only when asked, its error as its value; a Map's, a
// Set's and an array's entries come in the pages the editor asks for, past
// the first 500 too, with their named properties apart.
func TestDebuggerLazyAndPages(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, DefaultMain)
	if err := os.WriteFile(main, []byte(lazyApp), 0o644); err != nil {
		t.Fatal(err)
	}
	ours, theirs := socketPair(t)
	s := launchWith(t, t.TempDir(), "en", []*os.File{theirs}, []string{"KARIO_JSRT_DEBUG_FD=4"}, binary(t), main)
	d := newDebugConn(t, ours)
	d.event("hello")
	d.call("run", nil)
	s.hello("", nil)
	d.stopped("debugger")
	cart := d.vars(map[string]any{"frame": 0, "scope": "closure"})["cart"]
	props := d.vars(map[string]any{"ref": cart["ref"]})
	for _, g := range []string{"total", "broken"} {
		if p := props[g]; p["lazy"] != true || p["value"] != "(...)" || p["ref"] == 0.0 {
			t.Fatalf("%s: %v", g, p)
		}
	}
	read := func(ref any) map[string]any {
		t.Helper()
		got := d.call("variables", map[string]any{"ref": ref})["variables"].([]any)
		if len(got) != 1 {
			t.Fatalf("a getter read as %v", got)
		}
		return got[0].(map[string]any)
	}
	if v := read(props["total"]["ref"]); v["value"] != "719400" || v["type"] != "number" {
		t.Fatalf("total %v", v)
	}
	if v := read(props["broken"]["ref"]); v["type"] != "error" || !strings.Contains(v["value"].(string), "no total") {
		t.Fatalf("broken %v", v)
	}

	page := func(ref any, filter string, start, count int) []map[string]any {
		t.Helper()
		args := map[string]any{"ref": ref, "filter": filter}
		if filter == "indexed" {
			args["start"], args["count"] = start, count
		}
		var out []map[string]any
		for _, v := range d.call("variables", args)["variables"].([]any) {
			out = append(out, v.(map[string]any))
		}
		return out
	}
	items, tags, list := props["items"], props["tags"], props["list"]
	if items["indexed"] != 1200.0 || tags["indexed"] != 1200.0 || list["indexed"] != 1200.0 || list["named"] != 1.0 {
		t.Fatalf("sizes: items %v, tags %v, list %v", items, tags, list)
	}
	if got := page(items["ref"], "indexed", 1000, 100); len(got) != 100 || got[0]["name"] != `["k1000"]` || got[0]["value"] != "1000" {
		t.Fatalf("items 1000–1099: %d, first %v", len(got), got[0])
	}
	if got := page(tags["ref"], "indexed", 1150, 100); len(got) != 50 || got[0]["name"] != "[1150]" || got[49]["value"] != "1199" {
		t.Fatalf("tags 1150–: %d %v", len(got), got)
	}
	if got := page(list["ref"], "indexed", 1100, 2); len(got) != 2 || got[1]["name"] != "1101" || got[1]["value"] != "2202" {
		t.Fatalf("list 1100–1101: %v", got)
	}
	if got := page(list["ref"], "named", 0, 0); len(got) != 1 || got[0]["name"] != "length" {
		t.Fatalf("list's named: %v", got)
	}
	if got := page(items["ref"], "", 0, 0); len(got) != 500 {
		t.Fatalf("items without a filter: %d", len(got))
	}
	d.call("continue", nil)
	s.screenText("after 1200")
}
