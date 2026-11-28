package shell

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/devctl"
)

// dapWait bounds each wait for kario dap, as Kario Studio's requests are.
const dapWait = 30 * time.Second

// dapEditor drives kario dap as Kario Studio does, from the goroutine that
// runs the Shell's loop (the Runner's): while it waits for kario dap the
// loop runs, and the developer link's requests with it.
type dapEditor struct {
	t      *testing.T
	r      *Runner
	w      io.WriteCloser
	mu     sync.Mutex
	seq    int
	resp   map[int]chan map[string]any
	ended  bool
	events chan map[string]any
	exited chan error
	// console is everything the debug console showed, for failures;
	// output what it showed up to the last event waited for.
	console, stderr lockedBuffer
	output          []string
}

// startDAP starts kario dap with env added to this process's environment.
func startDAP(t *testing.T, r *Runner, kario string, env ...string) *dapEditor {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	e := &dapEditor{t: t, r: r, w: inW, resp: map[int]chan map[string]any{}, events: make(chan map[string]any, 1024), exited: make(chan error, 1)}
	cmd := exec.Command(kario, "dap")
	cmd.Env = append(os.Environ(), env...)
	// Files, not pipes Wait copies: what kario dap writes before it ends
	// is all read.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, &e.stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	err = StartProcess(cmd)
	inR.Close()
	outW.Close()
	if err != nil {
		t.Fatal(err)
	}
	go e.read(outR)
	go func() { e.exited <- cmd.Wait() }()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-e.exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	return e
}

// read hands kario dap's responses to their requests and its events to
// events, until it ends.
func (e *dapEditor) read(r io.ReadCloser) {
	defer r.Close()
	defer func() {
		e.mu.Lock()
		e.ended = true
		for seq, ch := range e.resp {
			close(ch)
			delete(e.resp, seq)
		}
		e.mu.Unlock()
		close(e.events)
	}()
	br := bufio.NewReader(r)
	for {
		n := -1
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			fmt.Sscanf(line, "Content-Length: %d", &n)
		}
		body := make([]byte, max(n, 0))
		if _, err := io.ReadFull(br, body); err != nil {
			return
		}
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if m["type"] == "event" {
			if b, _ := m["body"].(map[string]any); m["event"] == "output" {
				fmt.Fprint(&e.console, b["output"])
			}
			e.events <- m
			continue
		}
		rs, _ := m["request_seq"].(float64)
		e.mu.Lock()
		ch := e.resp[int(rs)]
		delete(e.resp, int(rs))
		e.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
}

func (e *dapEditor) says() string {
	return fmt.Sprintf("debug console:\n%s\nkario dap's standard error:\n%s\nthe screen:\n%s", e.console.String(), e.stderr.String(), e.r.screen())
}

// wait runs the Shell's loop until ch has a message.
func (e *dapEditor) wait(ch <-chan map[string]any, what string) map[string]any {
	e.t.Helper()
	for end := time.Now().Add(dapWait); ; {
		select {
		case m, ok := <-ch:
			if !ok {
				e.t.Fatalf("kario dap ended waiting for %s\n%s", what, e.says())
			}
			return m
		default:
		}
		if time.Now().After(end) {
			e.t.Fatalf("no %s in %s\n%s", what, dapWait, e.says())
		}
		e.r.pump(10 * time.Millisecond)
	}
}

func (e *dapEditor) request(cmd string, args any) map[string]any {
	e.t.Helper()
	e.mu.Lock()
	if e.ended {
		e.mu.Unlock()
		e.t.Fatalf("%s: kario dap has ended\n%s", cmd, e.says())
	}
	e.seq++
	seq := e.seq
	ch := make(chan map[string]any, 1)
	e.resp[seq] = ch
	e.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"seq": seq, "type": "request", "command": cmd, "arguments": args})
	if _, err := fmt.Fprintf(e.w, "Content-Length: %d\r\n\r\n%s", len(raw), raw); err != nil {
		e.t.Fatalf("%s: %v", cmd, err)
	}
	r := e.wait(ch, "answer to "+cmd)
	if r["success"] != true {
		e.t.Fatalf("%s: %v\n%s", cmd, r["message"], e.says())
	}
	body, _ := r["body"].(map[string]any)
	return body
}

// event runs the loop until kario dap sends an event of the kind; the debug
// console's output on the way goes to output.
func (e *dapEditor) event(kind string) map[string]any {
	e.t.Helper()
	for {
		m := e.wait(e.events, kind+" event")
		body, _ := m["body"].(map[string]any)
		if m["event"] == "output" {
			e.output = append(e.output, fmt.Sprint(body["output"]))
		}
		if m["event"] == kind {
			return body
		}
	}
}

// logged runs the loop until the debug console has shown the line.
func (e *dapEditor) logged(line string) {
	e.t.Helper()
	for !strings.Contains(strings.Join(e.output, ""), line) {
		e.event("output")
	}
}

// top is the stack's top frame.
func (e *dapEditor) top() map[string]any {
	e.t.Helper()
	frames, _ := e.request("stackTrace", map[string]any{"threadId": 1})["stackFrames"].([]any)
	if len(frames) == 0 {
		e.t.Fatalf("no stack\n%s", e.says())
	}
	return frames[0].(map[string]any)
}

// locals are the frame's local variables by name, as the editor shows them.
func (e *dapEditor) locals(frame any) map[string]string {
	e.t.Helper()
	scopes := e.request("scopes", map[string]any{"frameId": frame})["scopes"].([]any)
	ref := scopes[0].(map[string]any)["variablesReference"]
	vars := map[string]string{}
	for _, v := range e.request("variables", map[string]any{"variablesReference": ref})["variables"].([]any) {
		m := v.(map[string]any)
		vars[fmt.Sprint(m["name"])] = fmt.Sprint(m["value"])
	}
	return vars
}

func (e *dapEditor) evaluate(frame any, expr string) string {
	e.t.Helper()
	return fmt.Sprint(e.request("evaluate", map[string]any{"expression": expr, "frameId": frame, "context": "repl"})["result"])
}

// disconnect is the editor's stop: kario dap ends.
func (e *dapEditor) disconnect() {
	e.t.Helper()
	e.request("disconnect", nil)
	for end := time.Now().Add(dapWait); ; e.r.pump(10 * time.Millisecond) {
		select {
		case err := <-e.exited:
			if err != nil {
				e.t.Fatalf("kario dap: %v\n%s", err, e.says())
			}
			return
		default:
		}
		if time.Now().After(end) {
			e.t.Fatalf("kario dap did not end on disconnect\n%s", e.says())
		}
	}
}

// devDAPApp is the project's src/main.ts: a KarioUI page whose button bumps
// a count through the function the test breaks in.
const devDAPApp = `import { Button, Text, act, app, page, state } from "@kario/ui";

export const count = state(0);

function bump(by: number): number {
  const next = (count.value as number) + by; // @next
  count.set(next); // @set
  console.log("bumped to " + next);
  return next;
}

export const Main = page("main", () => ({
  title: "Debug me",
  body: [Button("Bump", act("bump", "Bump", () => bump(2))), Text("count " + count.value)],
}));

app(Main);
`

func markedLine(t *testing.T, src, marker string) int {
	t.Helper()
	for i, l := range strings.Split(src, "\n") {
		if strings.HasSuffix(l, "// "+marker) {
			return i + 1
		}
	}
	t.Fatalf("no line %s", marker)
	return 0
}

// devSSH stands in for ssh to the phone on the cable as the selftest's dev
// step does: the session's command runs here, with phone-dev among the
// system's programs and reaching this Shell. kario runs it through sh: a
// file this process wrote may still be open for writing in a child forked
// meanwhile, and exec then fails with ETXTBSY.
func devSSH(t *testing.T, c *Core, dir string) string {
	t.Helper()
	script := fmt.Sprintf(`while [ $# -gt 0 ] && [ "${1#-}" != "$1" ]; do shift; done
shift
export %s=%s %s='10.71.0.10 50022 %s 22' PATH=%s:"$PATH"
exec sh -c "$*"
`, devctl.RunDirEnv, shWord(c.cfg.RunDir), devctl.SSHEnv, usbAddr, shWord(filepath.Dir(c.devProgram())))
	path := filepath.Join(dir, "ssh")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return "sh " + path
}

// e2eAlone is alone for a test on the end-to-end stack: the child links
// the prefix staged once for this process, and what its stack leaves
// running is reaped.
func e2eAlone(t *testing.T) bool {
	if testing.Short() {
		t.Skip("end-to-end stack skipped in -short mode")
	}
	if os.Getenv(aloneEnv) == t.Name() {
		return true
	}
	go stagedPrefix()
	t.Parallel()
	prefix, err := stagedPrefix()
	if err != nil {
		t.Fatal(err)
	}
	aloneSlots <- struct{}{}
	defer func() { <-aloneSlots }()
	file := filepath.Join(filepath.Dir(prefix), "run-"+t.Name())
	r := runChild(t, nil, aloneEnv+"="+t.Name(), e2ePrefixEnv+"="+prefix, e2eRunFileEnv+"="+file)
	reapChild(file)
	r.report(t)
	return false
}

// The emulator's case that debugs an app over SSH on the cable runs here
// line for line on the real Shell, with phone-dev on the phone itself
// instead (dev and dev-debug without -ssh, as the developer link's other
// cases run off the emulator), so its steps and texts hold before QEMU
// runs it.
func TestDevDebugCase(t *testing.T) {
	if !e2eAlone(t) {
		return
	}
	var text []string
	in := false
	for _, l := range strings.Split(SelftestScript, "\n") {
		switch {
		case strings.HasPrefix(l, "== ") && strings.Contains(l, "phone-dev debug"):
			in = true
		case !in:
			continue
		case l == "":
			in = false
			continue
		case l == "require-usb-host" || l == "require-netfw" || strings.HasPrefix(l, "usb-port "):
			continue
		}
		if in {
			l = strings.Replace(l, "dev-debug -ssh ", "dev-debug ", 1)
			text = append(text, strings.Replace(l, "dev-ssh ", "dev ", 1))
		}
	}
	if len(text) < 10 {
		t.Fatal("no case debugging an app with phone-dev debug in the selftest script")
	}
	c, _ := e2eStack(t)
	var out strings.Builder
	r := &Runner{C: c, Out: &out}
	start := time.Now()
	if !r.Run("sleep 800\n== boot\nat sys.idle 5000\n" + strings.Join(text, "\n") + "\n") {
		t.Fatalf("%s\nthe screen:\n%s", out.String(), r.screen())
	}
	t.Logf("%s(%s)", out.String(), time.Since(start).Round(time.Millisecond))
}

// kario dap debugs a TypeScript app on the real Shell as Kario Studio does
// on a phone: developer mode on and the computer trusted on the cable, as
// the developer link's selftest cases have them; kario dap, phone-dev and
// the app's kario-jsrt are the staged programs, ssh a stand-in that runs
// the command on the phone. A launch builds the project, installs it and
// restarts it under the debugger; a breakpoint in the .ts stops when the
// button is pressed on the phone, with the stack, the locals and an
// evaluation in the frame in TypeScript's terms; a step goes to the next
// line, continue lets the page come, and what the app logs reaches the
// debug console and the app's log (kario device logs). Once the editor
// disconnects the app runs on, the same process, without stopping. Then,
// with the app frozen in the background, an attach joins it without a
// restart (nothing installed, its main.js this build), its breakpoint
// stops once the app is opened again and pressed, with its state kept,
// and after the detach it runs on.
func TestDevLinkDAP(t *testing.T) {
	if !e2eAlone(t) {
		return
	}
	var kario string
	c, _ := e2eStackWith(t, func(prefix string, _ *Config) {
		// In the stack's prefix, where noneLeft sees it.
		kario = filepath.Join(prefix, "bin", "kario")
		if err := os.Link(filepath.Join(filepath.Dir(os.Getenv(e2ePrefixEnv)), "bin", "kario"), kario); err != nil {
			t.Fatal(err)
		}
	})
	var out strings.Builder
	r := &Runner{C: c, Out: &out}
	if !r.Run("sleep 800\n== boot\nat sys.idle 5000\n") {
		t.Fatalf("boot:\n%s", out.String())
	}
	step := func(lines ...string) {
		t.Helper()
		for _, l := range lines {
			cmd, arg, _ := strings.Cut(l, " ")
			if err := r.step(cmd, arg); err != nil {
				t.Fatalf("%s: %v\nthe screen:\n%s", l, err, r.screen())
			}
		}
	}
	until := func(what string, cond func() bool) {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); !cond(); r.pump(20 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("%s: not so after 10s\nthe screen:\n%s", what, r.screen())
			}
		}
	}
	step("setting developer_mode 1", "usb-cable on")
	until("asked whether to trust the computer", func() bool { return c.usb.prompt != nil })
	r.press(Named(KeyEnter))
	until("trusting the computer", func() bool { return c.usb.trusted })
	step("at sys.idle")

	tmp := t.TempDir()
	project := filepath.Join(tmp, "debugme")
	if said, err := exec.Command(kario, "new", project).CombinedOutput(); err != nil {
		t.Fatalf("kario new: %v\n%s", err, said)
	}
	main := filepath.Join(project, "src", "main.ts")
	if err := os.WriteFile(main, []byte(devDAPApp), 0o644); err != nil {
		t.Fatal(err)
	}
	const id = "com.example.debugme"
	env := []string{"KARIO_SSH=" + devSSH(t, c, tmp), "KARIO_DEVICE="}
	args := map[string]any{"project": project, "devstore": filepath.Join(tmp, "devstore")}
	next, set := markedLine(t, devDAPApp, "@next"), markedLine(t, devDAPApp, "@set")
	breakAt := func(ed *dapEditor, line int) {
		t.Helper()
		bps := ed.request("setBreakpoints", map[string]any{"source": map[string]any{"path": main}, "breakpoints": []any{map[string]any{"line": line}}})["breakpoints"].([]any)
		if b := bps[0].(map[string]any); b["verified"] != true || b["line"] != float64(line) {
			t.Fatalf("breakpoint %v", bps)
		}
	}
	stoppedIn := func(ed *dapEditor, line int) map[string]any {
		t.Helper()
		top := ed.top()
		if src, _ := top["source"].(map[string]any); top["name"] != "bump" || src["path"] != main || top["line"] != float64(line) {
			t.Fatalf("stopped in %v, not bump at %s:%d", top, main, line)
		}
		return top
	}

	start := time.Now()
	ed := startDAP(t, r, kario, env...)
	ed.request("initialize", map[string]any{"adapterID": "kario"})
	ed.request("launch", args)
	ed.event("initialized")
	a := c.appByID(id)
	if a == nil || !a.M.installed || !a.running || !a.debugging {
		t.Fatalf("after the launch: %+v", a)
	}
	pid := a.proc.Pid
	breakAt(ed, next)
	ed.request("setExceptionBreakpoints", map[string]any{"filters": []any{"uncaught"}})
	ed.request("configurationDone", nil)
	step("at "+id+"/main 10000", "wait count 0", "key enter")
	if ev := ed.event("stopped"); ev["reason"] != "breakpoint" {
		t.Fatalf("stopped %v", ev)
	}
	top := stoppedIn(ed, next)
	if vars := ed.locals(top["id"]); vars["by"] != "2" {
		t.Fatalf("locals %v", vars)
	}
	if v := ed.evaluate(top["id"], "by * 10 + (count.value as number)"); v != "20" {
		t.Fatalf("evaluated %q, want 20", v)
	}
	ed.request("next", map[string]any{"threadId": 1})
	ed.event("stopped")
	stoppedIn(ed, set)
	ed.request("continue", map[string]any{"threadId": 1})
	step("wait count 2")
	ed.logged("bumped to 2\n")
	var logs lockedBuffer
	cmd := exec.Command(kario, "device", "logs", id)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := r.waitCmd(cmd); err != nil || !strings.Contains(logs.String(), "bumped to 2\n") {
		t.Fatalf("kario device logs: %v\n%s", err, logs.String())
	}
	ed.disconnect()
	if said := ed.console.String(); !strings.Contains(said, "installed "+id) || !strings.Contains(said, "Debugging "+id+" on the phone") {
		t.Fatalf("debug console:\n%s", said)
	}
	until("the debugger gone", func() bool { return !a.debugging })
	step("key enter", "wait count 4")
	if !a.running || a.proc.Pid != pid {
		t.Fatalf("the app did not run on after the disconnect: running %v, pid %d (was %d)", a.running, a.proc.Pid, pid)
	}
	launched := time.Since(start)

	start = time.Now()
	c.SetFreezeAfter(300 * time.Millisecond)
	step("key f4", "at sys.idle")
	until("frozen in the background", func() bool { return c.ProcState(id) == "frozen" })
	ed = startDAP(t, r, kario, env...)
	ed.request("initialize", map[string]any{"adapterID": "kario"})
	ed.request("attach", args)
	ed.event("initialized")
	breakAt(ed, next)
	ed.request("configurationDone", nil)
	c.Launch(a, "")
	step("at "+id+"/main 10000", "wait count 4", "key enter")
	if ev := ed.event("stopped"); ev["reason"] != "breakpoint" {
		t.Fatalf("stopped %v", ev)
	}
	top = stoppedIn(ed, next)
	if v := ed.evaluate(top["id"], "count.value"); v != "4" {
		t.Fatalf("the app's state after attaching: count %s, want 4", v)
	}
	if a.proc.Pid != pid {
		t.Fatalf("attaching restarted the app: pid %d, was %d", a.proc.Pid, pid)
	}
	ed.request("continue", map[string]any{"threadId": 1})
	step("wait count 6")
	ed.disconnect()
	if said := ed.console.String(); !strings.Contains(said, "Attached to "+id+" on the phone") || strings.Contains(said, "installed") || strings.Contains(said, "not this build") {
		t.Fatalf("debug console:\n%s", said)
	}
	until("the debugger gone", func() bool { return !a.debugging })
	step("key enter", "wait count 8")
	if a.proc.Pid != pid {
		t.Fatalf("the app did not run on after the detach: pid %d, was %d", a.proc.Pid, pid)
	}
	attached := time.Since(start)
	// Shutdown leaves file sharing's helper to end with the Shell's
	// process, and this process lives on after it.
	step("key f4", "usb-cable off")
	until("the cable out", func() bool { return !c.usb.trusted && c.usb.helper == nil })
	step("setting developer_mode 0")
	t.Logf("launch, stop, step, continue, disconnect: %s; attach to the frozen app, stop, detach: %s",
		launched.Round(time.Millisecond), attached.Round(time.Millisecond))
}
