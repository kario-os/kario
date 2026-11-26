package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/devctl"
	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// dapClient drives kario dap as an editor does: requests by seq, their
// responses, and the events in order.
type dapClient struct {
	t      *testing.T
	w      io.Writer
	mu     sync.Mutex
	seq    int
	resp   map[int]chan map[string]any
	events chan map[string]any
}

func newDAPClient(t *testing.T, w io.Writer, r io.Reader) *dapClient {
	c := &dapClient{t: t, w: w, resp: map[int]chan map[string]any{}, events: make(chan map[string]any, 256)}
	go func() {
		br := bufio.NewReader(r)
		for {
			n := 0
			for {
				line, err := br.ReadString('\n')
				if err != nil {
					close(c.events)
					return
				}
				line = strings.TrimSpace(line)
				if line == "" {
					break
				}
				fmt.Sscanf(line, "Content-Length: %d", &n)
			}
			body := make([]byte, n)
			if _, err := io.ReadFull(br, body); err != nil {
				close(c.events)
				return
			}
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			if m["type"] == "event" {
				c.events <- m
				continue
			}
			rs, _ := m["request_seq"].(float64)
			c.mu.Lock()
			ch := c.resp[int(rs)]
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}()
	return c
}

func (c *dapClient) request(cmd string, args any) map[string]any {
	c.t.Helper()
	r := c.try(cmd, args)
	if r["success"] != true {
		c.t.Fatalf("%s: %v", cmd, r["message"])
	}
	body, _ := r["body"].(map[string]any)
	return body
}

func (c *dapClient) try(cmd string, args any) map[string]any {
	c.t.Helper()
	c.mu.Lock()
	c.seq++
	seq := c.seq
	ch := make(chan map[string]any, 1)
	c.resp[seq] = ch
	c.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"seq": seq, "type": "request", "command": cmd, "arguments": args})
	fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n%s", len(raw), raw)
	select {
	case r := <-ch:
		return r
	case <-time.After(30 * time.Second):
		c.t.Fatalf("no response to %s", cmd)
		return nil
	}
}

// event waits for an event of the kind; output events are collected.
func (c *dapClient) event(kind string, output *[]string) map[string]any {
	c.t.Helper()
	for {
		select {
		case m, ok := <-c.events:
			if !ok {
				c.t.Fatalf("kario dap ended waiting for %s", kind)
			}
			body, _ := m["body"].(map[string]any)
			if m["event"] == "output" && output != nil {
				*output = append(*output, fmt.Sprint(body["output"]))
			}
			if m["event"] == kind {
				return body
			}
		case <-time.After(30 * time.Second):
			c.t.Fatalf("no %s event", kind)
		}
	}
}

const dapApp = `import * as kario from "kario";

let count = 0;

function bump(by: number): number {
  const next = count + by; // @next

  count = next; // @count
  return count;
}

const page = (n: number) => ({ id: "main", title: "t", body: [{ kind: "text", text: "count " + n }] });

kario.on("start", () => kario.reset(page(count)));

kario.on("action", () => {
  const n = bump(2);
  kario.update(page(n));
});
`

// phoneApp is the fake phone's side of a debug session: the app the
// computer installed last runs in kario-jsrt under the debugger, its
// pages and actions on a fake Shell.
type phoneApp struct {
	ui    *wire.Conn
	pages chan string
}

func (f *fakePhone) debugInstalled(t *testing.T, bin, store string) *phoneApp {
	app := &phoneApp{pages: make(chan string, 64)}
	f.debug = func(conn net.Conn, r *bufio.Reader, req devctl.Request) {
		f.mu.Lock()
		raw := f.pkgs[len(f.pkgs)-1]
		f.mu.Unlock()
		p, err := pkg.Open(raw, pkg.LoadRoots(filepath.Join(store, "trust")))
		if err != nil {
			_ = devctl.WriteReply(conn, devctl.Reply{Error: err.Error()})
			return
		}
		// Not t.TempDir: this runs on the fake phone's goroutine.
		scratch, err := os.MkdirTemp("", "kario-dap-app-")
		if err != nil {
			t.Error(err)
			return
		}
		defer os.RemoveAll(scratch)
		dir, data := filepath.Join(scratch, "app"), filepath.Join(scratch, "data")
		if err := p.Extract(dir); err != nil {
			t.Error(err)
			return
		}
		sum, _ := packagedSum(raw, jsrt.DefaultMain)
		pair := func() (*os.File, *os.File) {
			fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			return os.NewFile(uintptr(fds[0]), "parent"), os.NewFile(uintptr(fds[1]), "child")
		}
		uiOurs, uiTheirs := pair()
		dbgOurs, dbgTheirs := pair()
		cmd := exec.Command(bin, filepath.Join(dir, jsrt.DefaultMain))
		cmd.ExtraFiles = []*os.File{uiTheirs, dbgTheirs}
		cmd.Env = []string{"PHONE_UI_FD=3", "PHONE_APP_ID=" + req.App, "PHONE_DATA_DIR=" + data, "PHONE_LANG=en", jsrt.DebugFDEnv + "=4"}
		cmd.Stderr = io.Discard
		if err := cmd.Start(); err != nil {
			t.Error(err)
			return
		}
		uiTheirs.Close()
		dbgTheirs.Close()
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
		app.ui = wire.New(uiOurs)
		go func() {
			for {
				var m ui.Msg
				if err := app.ui.Recv(&m); err != nil {
					return
				}
				switch {
				case m.T == ui.MsgHello:
					_ = app.ui.Send(ui.Msg{T: ui.MsgStart})
				case m.Screen != nil && len(m.Screen.Body) > 0:
					app.pages <- m.Screen.Body[0].Text
				}
			}
		}()
		_ = devctl.WriteReply(conn, devctl.Reply{OK: true, Text: fmt.Sprintf("debugging %s 1.0 (main.js sha256 %s)", req.App, sum)})
		go func() { _, _ = io.Copy(dbgOurs, r); dbgOurs.Close() }()
		_, _ = io.Copy(conn, dbgOurs)
	}
	return app
}

func (a *phoneApp) page(t *testing.T, want string) {
	t.Helper()
	for {
		select {
		case p := <-a.pages:
			if p == want {
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no page %q", want)
		}
	}
}

func lineMarked(t *testing.T, src, marker string) int {
	for i, l := range strings.Split(src, "\n") {
		if strings.HasSuffix(l, "// "+marker) {
			return i + 1
		}
	}
	t.Fatalf("no line %s", marker)
	return 0
}

// kario dap debugs a project on a phone as an editor sees it: the launch
// builds and installs the project and restarts it under kario-jsrt's
// debugger over phone-dev; breakpoints are set in the .ts file (one on a
// blank line moves to the next line with code), the stack, scopes and
// evaluation are in TypeScript's terms through the source map, a step
// goes to the next line, and continue lets the app run on.
func TestDAP(t *testing.T) {
	bin := os.Getenv("KARIO_JSRT")
	if bin == "" {
		bin, _ = filepath.Abs(filepath.Join("..", "..", "build", "jsrt", jsrt.Program))
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("no kario-jsrt (make jsrt)")
	}
	phone, tmp, _ := startFakePhone(t)
	store := filepath.Join(tmp, "devstore")
	app := phone.debugInstalled(t, bin, store)
	dir := newTestProject(t)
	main := filepath.Join(dir, "src", "main.ts")
	if err := os.WriteFile(main, []byte(dapApp), 0o644); err != nil {
		t.Fatal(err)
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	served := make(chan error, 1)
	go func() { served <- serveDAP(inR, outW, io.Discard); outW.Close() }()
	c := newDAPClient(t, inW, outR)
	var output []string

	caps := c.request("initialize", map[string]any{"adapterID": "kario"})
	if caps["supportsConfigurationDoneRequest"] != true || caps["supportsConditionalBreakpoints"] != true {
		t.Fatalf("capabilities %v", caps)
	}
	c.request("launch", map[string]any{"project": dir, "devstore": store})
	c.event("initialized", &output)
	if got := strings.Join(phone.ops(), " "); got != "trust install debug" {
		t.Fatalf("the phone was asked: %s", got)
	}
	bps := c.request("setBreakpoints", map[string]any{
		"source":      map[string]any{"path": main},
		"breakpoints": []any{map[string]any{"line": lineMarked(t, dapApp, "@next")}, map[string]any{"line": lineMarked(t, dapApp, "@next") + 1}},
	})["breakpoints"].([]any)
	if b0, b1 := bps[0].(map[string]any), bps[1].(map[string]any); b0["verified"] != true || b0["line"] != float64(lineMarked(t, dapApp, "@next")) ||
		b1["verified"] != true || b1["line"] != float64(lineMarked(t, dapApp, "@count")) {
		t.Fatalf("breakpoints %v", bps)
	}
	c.request("setBreakpoints", map[string]any{"source": map[string]any{"path": main}, "breakpoints": []any{map[string]any{"line": lineMarked(t, dapApp, "@next")}}})
	c.request("setExceptionBreakpoints", map[string]any{"filters": []any{"uncaught"}})
	c.request("configurationDone", nil)
	app.page(t, "count 0")

	if err := app.ui.Send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "x"}); err != nil {
		t.Fatal(err)
	}
	if ev := c.event("stopped", &output); ev["reason"] != "breakpoint" || ev["threadId"] != float64(1) {
		t.Fatalf("stopped %v", ev)
	}
	if th := c.request("threads", nil)["threads"].([]any); len(th) != 1 {
		t.Fatalf("threads %v", th)
	}
	frames := c.request("stackTrace", map[string]any{"threadId": 1})["stackFrames"].([]any)
	top := frames[0].(map[string]any)
	if src := top["source"].(map[string]any); top["name"] != "bump" || src["path"] != main || top["line"] != float64(lineMarked(t, dapApp, "@next")) {
		t.Fatalf("top frame %v", top)
	}
	scopes := c.request("scopes", map[string]any{"frameId": top["id"]})["scopes"].([]any)
	local := scopes[0].(map[string]any)
	vars := map[string]string{}
	for _, v := range c.request("variables", map[string]any{"variablesReference": local["variablesReference"]})["variables"].([]any) {
		m := v.(map[string]any)
		vars[m["name"].(string)] = m["value"].(string)
	}
	if vars["by"] != "2" {
		t.Fatalf("locals %v", vars)
	}
	if r := c.request("evaluate", map[string]any{"expression": "by * 10 + count", "frameId": top["id"]}); r["result"] != "20" {
		t.Fatalf("evaluate %v", r)
	}

	c.request("next", map[string]any{"threadId": 1})
	c.event("stopped", &output)
	top = c.request("stackTrace", map[string]any{"threadId": 1})["stackFrames"].([]any)[0].(map[string]any)
	if top["line"] != float64(lineMarked(t, dapApp, "@count")) {
		t.Fatalf("after next %v", top)
	}
	c.request("continue", map[string]any{"threadId": 1})
	app.page(t, "count 2")

	c.request("disconnect", nil)
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("kario dap did not end on disconnect")
	}
	if all := strings.Join(output, ""); !strings.Contains(all, "installed com.example.my_app") || !strings.Contains(all, "Debugging com.example.my_app on the phone") {
		t.Fatalf("debug console:\n%s", all)
	}
}

// streamPair is a socket pair; ours (the first) is non-blocking, so closing
// it while it is read reaches the other end at once.
func streamPair(t *testing.T, kind int) (*os.File, *os.File) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, kind|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Error(err)
		return nil, nil
	}
	_ = syscall.SetNonblock(fds[0], true)
	return os.NewFile(uintptr(fds[0]), "parent"), os.NewFile(uintptr(fds[1]), "child")
}

// runInstalled starts the app the computer installed last as the phone
// starts one installed in developer mode, with the socket a debugger
// attaching comes through; the phone answers an attach as the Shell does,
// without a restart.
func (f *fakePhone) runInstalled(t *testing.T, bin, store string) *phoneApp {
	app := &phoneApp{pages: make(chan string, 64)}
	f.mu.Lock()
	raw := f.pkgs[len(f.pkgs)-1]
	f.mu.Unlock()
	p, err := pkg.Open(raw, pkg.LoadRoots(filepath.Join(store, "trust")))
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	dir := filepath.Join(scratch, "app")
	if err := p.Extract(dir); err != nil {
		t.Fatal(err)
	}
	uiOurs, uiTheirs := streamPair(t, syscall.SOCK_STREAM)
	port, portTheirs := streamPair(t, syscall.SOCK_SEQPACKET)
	cmd := exec.Command(bin, filepath.Join(dir, jsrt.DefaultMain))
	cmd.ExtraFiles = []*os.File{uiTheirs, portTheirs}
	cmd.Env = []string{"PHONE_UI_FD=3", "PHONE_APP_ID=com.example.my_app", "PHONE_DATA_DIR=" + filepath.Join(scratch, "data"), "PHONE_LANG=en", jsrt.DebugAttachFDEnv + "=4"}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	uiTheirs.Close()
	portTheirs.Close()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); port.Close() })
	app.ui = wire.New(uiOurs)
	go func() {
		for {
			var m ui.Msg
			if err := app.ui.Recv(&m); err != nil {
				return
			}
			switch {
			case m.T == ui.MsgHello:
				_ = app.ui.Send(ui.Msg{T: ui.MsgStart})
			case m.Screen != nil && len(m.Screen.Body) > 0:
				app.pages <- m.Screen.Body[0].Text
			}
		}
	}()
	sum, _ := packagedSum(raw, jsrt.DefaultMain)
	f.debug = func(conn net.Conn, r *bufio.Reader, req devctl.Request) {
		if !req.Attach {
			_ = devctl.WriteReply(conn, devctl.Reply{Error: "this phone only attaches"})
			return
		}
		ours, theirs := streamPair(t, syscall.SOCK_STREAM)
		err := syscall.Sendmsg(int(port.Fd()), []byte{0}, syscall.UnixRights(int(theirs.Fd())), nil, 0)
		theirs.Close()
		if err != nil {
			_ = devctl.WriteReply(conn, devctl.Reply{Error: err.Error()})
			return
		}
		_ = devctl.WriteReply(conn, devctl.Reply{OK: true, Text: fmt.Sprintf("attached to %s 1.0 (main.js sha256 %s)", req.App, sum)})
		go func() { _, _ = io.Copy(ours, r); ours.Close() }()
		_, _ = io.Copy(conn, ours)
	}
	return app
}

func (a *phoneApp) press(t *testing.T) {
	t.Helper()
	if err := a.ui.Send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "x"}); err != nil {
		t.Fatal(err)
	}
}

// kario dap attaches to the app as it runs on the phone: nothing is
// installed and the app keeps its state (no restart); a breakpoint with a
// hit count stops from its second hit on, a logpoint writes its values
// in the debug console at its place in the .ts and runs on, a function
// breakpoint stops as bump is entered, a hit count the phone cannot read
// is shown unverified, and once the editor goes the app runs on without
// stopping.
func TestDAPAttach(t *testing.T) {
	bin := os.Getenv("KARIO_JSRT")
	if bin == "" {
		bin, _ = filepath.Abs(filepath.Join("..", "..", "build", "jsrt", jsrt.Program))
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("no kario-jsrt (make jsrt)")
	}
	phone, tmp, _ := startFakePhone(t)
	store := filepath.Join(tmp, "devstore")
	dir := newTestProject(t)
	main := filepath.Join(dir, "src", "main.ts")
	if err := os.WriteFile(main, []byte(dapApp), 0o644); err != nil {
		t.Fatal(err)
	}
	built := t.TempDir()
	o := packageOptions{out: filepath.Join(built, "app.phoneapp"), devStore: store, name: "Example Developer", debug: true,
		mapFile: filepath.Join(built, "main.js.map"), symbols: "-"}
	if _, err := packageApp(dir, o); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(o.out)
	if err != nil {
		t.Fatal(err)
	}
	phone.pkgs = append(phone.pkgs, raw)
	app := phone.runInstalled(t, bin, store)
	app.page(t, "count 0")
	app.press(t)
	app.page(t, "count 2")

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	served := make(chan error, 1)
	go func() { served <- serveDAP(inR, outW, io.Discard); outW.Close() }()
	c := newDAPClient(t, inW, outR)
	var output []string
	caps := c.request("initialize", map[string]any{"adapterID": "kario"})
	if caps["supportsLogPoints"] != true || caps["supportsHitConditionalBreakpoints"] != true || caps["supportsFunctionBreakpoints"] != true {
		t.Fatalf("capabilities %v", caps)
	}
	c.request("attach", map[string]any{"project": dir, "devstore": store})
	c.event("initialized", &output)
	phone.mu.Lock()
	reqs := slices.Clone(phone.reqs)
	phone.mu.Unlock()
	if len(reqs) != 1 || reqs[0].Op != devctl.OpDebug || !reqs[0].Attach {
		t.Fatalf("the phone was asked: %+v", reqs)
	}
	next, count := lineMarked(t, dapApp, "@next"), lineMarked(t, dapApp, "@count")
	bps := c.request("setBreakpoints", map[string]any{
		"source": map[string]any{"path": main},
		"breakpoints": []any{
			map[string]any{"line": next, "hitCondition": "2"},
			map[string]any{"line": count, "logMessage": "count {count} next {next}"},
			map[string]any{"line": count + 1, "hitCondition": "often"},
		},
	})["breakpoints"].([]any)
	if b := bps[0].(map[string]any); b["verified"] != true || b["line"] != float64(next) {
		t.Fatalf("breakpoints %v", bps)
	}
	if b := bps[2].(map[string]any); b["verified"] != false || !strings.Contains(fmt.Sprint(b["message"]), "hit count") {
		t.Fatalf("a hit count that is none: %v", bps[2])
	}
	if fb := c.request("setFunctionBreakpoints", map[string]any{"breakpoints": []any{map[string]any{"name": "bump", "hitCondition": "=3"}}})["breakpoints"].([]any); fb[0].(map[string]any)["verified"] != true {
		t.Fatalf("function breakpoints %v", fb)
	}
	c.request("setExceptionBreakpoints", map[string]any{"filters": []any{"uncaught"}})
	c.request("configurationDone", nil)

	logged := func(want string) {
		t.Helper()
		for {
			select {
			case m, ok := <-c.events:
				if !ok {
					t.Fatal("kario dap ended")
				}
				body, _ := m["body"].(map[string]any)
				if m["event"] == "stopped" {
					t.Fatalf("stopped %v waiting for the logpoint", body)
				}
				if m["event"] != "output" || body["category"] != "console" || !strings.HasPrefix(fmt.Sprint(body["output"]), "count ") {
					continue
				}
				src, _ := body["source"].(map[string]any)
				if body["output"] != want || src["path"] != main || body["line"] != float64(count) {
					t.Fatalf("logpoint %v, want %q at line %d", body, want, count)
				}
				return
			case <-time.After(10 * time.Second):
				t.Fatalf("no logpoint %q", want)
			}
		}
	}
	app.press(t)
	logged("count 2 next 4\n")
	app.page(t, "count 4")
	app.press(t)
	if ev := c.event("stopped", &output); ev["reason"] != "breakpoint" {
		t.Fatalf("stopped %v", ev)
	}
	top := c.request("stackTrace", map[string]any{"threadId": 1})["stackFrames"].([]any)[0].(map[string]any)
	if top["name"] != "bump" || top["line"] != float64(next) {
		t.Fatalf("top frame %v", top)
	}
	if r := c.request("evaluate", map[string]any{"expression": "count", "frameId": top["id"]}); r["result"] != "4" {
		t.Fatalf("the app's state after attaching: %v", r)
	}
	c.request("continue", map[string]any{"threadId": 1})
	logged("count 4 next 6\n")
	app.page(t, "count 6")
	app.press(t)
	if ev := c.event("stopped", &output); ev["reason"] != "function breakpoint" {
		t.Fatalf("stopped %v, want the function breakpoint", ev)
	}
	if top := c.request("stackTrace", map[string]any{"threadId": 1})["stackFrames"].([]any)[0].(map[string]any); top["name"] != "bump" {
		t.Fatalf("function breakpoint in %v", top)
	}
	c.request("continue", map[string]any{"threadId": 1})
	if ev := c.event("stopped", &output); ev["reason"] != "breakpoint" {
		t.Fatalf("stopped %v", ev)
	}
	c.request("continue", map[string]any{"threadId": 1})
	app.page(t, "count 8")

	c.request("disconnect", nil)
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("kario dap did not end on disconnect")
	}
	app.press(t)
	app.page(t, "count 10")
	if all := strings.Join(output, ""); !strings.Contains(all, "Attached to com.example.my_app on the phone") || strings.Contains(all, "installed") || strings.Contains(all, "not this build") {
		t.Fatalf("debug console:\n%s", all)
	}
}

// dapTSApp's count clashes with counter.ts's, so esbuild names it count2
// in main.js.
const dapTSApp = `import * as kario from "kario";
import { add } from "./counter";

let count = 0;
interface Box { n: number }

function bump(by: number): number {
  const box: Box = { n: count + by }; // @box
  count = box.n;
  return add(count); // @ret
}

const page = (n: number) => ({ id: "main", title: "t", body: [{ kind: "text", text: "count " + n }] });

kario.on("start", () => kario.reset(page(count)));
kario.on("action", () => kario.update(page(bump(2))));
`

const dapTSCounter = `export let count = 100;
export function add(n: number): number {
  count += n;
  return count;
}
`

// What the editor evaluates is TypeScript in the source's names: a
// condition and a logpoint with type assertions, an expression and a new
// value of a variable esbuild renamed in the bundle (count2) by its name
// in the source, and the frame's variables show that name too.
func TestDAPTypeScript(t *testing.T) {
	bin := os.Getenv("KARIO_JSRT")
	if bin == "" {
		bin, _ = filepath.Abs(filepath.Join("..", "..", "build", "jsrt", jsrt.Program))
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("no kario-jsrt (make jsrt)")
	}
	phone, tmp, _ := startFakePhone(t)
	store := filepath.Join(tmp, "devstore")
	app := phone.debugInstalled(t, bin, store)
	dir := newTestProject(t)
	main := filepath.Join(dir, "src", "main.ts")
	for name, src := range map[string]string{main: dapTSApp, filepath.Join(dir, "src", "counter.ts"): dapTSCounter} {
		if err := os.WriteFile(name, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = serveDAP(inR, outW, io.Discard); outW.Close() }()
	c := newDAPClient(t, inW, outR)
	var output []string
	c.request("initialize", map[string]any{"adapterID": "kario"})
	c.request("launch", map[string]any{"project": dir, "devstore": store})
	c.event("initialized", &output)
	c.request("setBreakpoints", map[string]any{"source": map[string]any{"path": main}, "breakpoints": []any{
		map[string]any{"line": lineMarked(t, dapTSApp, "@box"), "condition": "(by as number) > 1 && count === 0"},
		map[string]any{"line": lineMarked(t, dapTSApp, "@ret"), "logMessage": "count is {count as number}"},
	}})
	c.request("configurationDone", nil)
	app.page(t, "count 0")

	if err := app.ui.Send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "x"}); err != nil {
		t.Fatal(err)
	}
	if ev := c.event("stopped", &output); ev["reason"] != "breakpoint" {
		t.Fatalf("stopped %v; output %q", ev, output)
	}
	top := c.request("stackTrace", map[string]any{"threadId": 1})["stackFrames"].([]any)[0].(map[string]any)
	if top["line"] != float64(lineMarked(t, dapTSApp, "@box")) {
		t.Fatalf("top frame %v", top)
	}
	scopes := c.request("scopes", map[string]any{"frameId": top["id"]})["scopes"].([]any)
	closure := scopes[1].(map[string]any)["variablesReference"]
	vars := map[string]string{}
	for _, v := range c.request("variables", map[string]any{"variablesReference": closure})["variables"].([]any) {
		m := v.(map[string]any)
		vars[m["name"].(string)] = m["value"].(string)
	}
	if _, renamed := vars["count2"]; vars["count"] != "0" || renamed {
		t.Fatalf("closure %v", vars)
	}
	for expr, want := range map[string]string{"count as number": "0", "<number>by * 10 + count": "20", "(count satisfies number) + 1": "1"} {
		if r := c.request("evaluate", map[string]any{"expression": expr, "frameId": top["id"]}); r["result"] != want {
			t.Errorf("evaluate %q: %v, want %s", expr, r, want)
		}
	}
	if r := c.request("setVariable", map[string]any{"variablesReference": closure, "name": "count", "value": "40 as number"}); r["value"] != "40" {
		t.Fatalf("setVariable %v", r)
	}
	c.request("continue", map[string]any{"threadId": 1})
	app.page(t, "count 142")
	for end := time.Now().Add(10 * time.Second); !slices.Contains(output, "count is 42\n"); {
		if time.Now().After(end) {
			t.Fatalf("no logpoint; output %q", output)
		}
		select {
		case m := <-c.events:
			if body, _ := m["body"].(map[string]any); m["event"] == "output" {
				output = append(output, fmt.Sprint(body["output"]))
			}
		case <-time.After(time.Second):
		}
	}
	c.request("disconnect", nil)
}
