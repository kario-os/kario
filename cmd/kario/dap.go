package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/kario-os/kario/devctl"
	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
)

// kario dap is the debug adapter Kario Studio starts for a "kario" debug
// session, speaking the Debug Adapter Protocol on standard input and
// output: it debugs a TypeScript app on a phone in developer mode as VS
// Code's JavaScript debugger does one under Node.js. A launch builds the
// project unminified with its source map, installs it with the development
// store (kario device install) and restarts it under kario-jsrt's debugger
// (phone-dev debug, over the trusted cable, 29-app-development.md §3); an
// attach installs nothing and joins the app as it runs (phone-dev debug
// -attach: the same process, its state kept; one not running starts under
// the debugger), and says so when the phone's main.js is not this build.
// Breakpoints (with conditions, hit counts and log messages), function
// breakpoints, stacks and steps go through the source map; the debugger on
// the phone (jsrt/src/debug.c) sees only main.js. What the editor gives to
// evaluate (expressions, conditions, logpoints' parts, new values) is
// TypeScript in the source's names: esbuild strips the types, and a name it
// renamed in the bundle (count2 for a clashing count) is given its new name
// (Define, which touches only names the expression does not bind itself);
// a frame's variables show the names as written.

// dapArgs are a launch's or an attach's arguments (the debug
// configuration's fields).
type dapArgs struct {
	Project     string `json:"project"`
	SSH         string `json:"ssh"`
	To          string `json:"to"`
	DevStore    string `json:"devstore"`
	Developer   string `json:"developer"`
	Native      string `json:"native"`
	StopOnEntry bool   `json:"stopOnEntry"`
}

type dapMessage struct {
	Seq       int             `json:"seq"`
	Type      string          `json:"type"`
	Command   string          `json:"command"`
	Arguments json.RawMessage `json:"arguments"`
}

// Variable references: the phone's object references are below scopeRef;
// a frame's scopes are scopeRef + 3*frame + kind.
const scopeRef = 1 << 20

var scopeKinds = []string{"local", "closure", "global"}

type dapBreakpoint struct {
	gens                                []int
	condition, hitCondition, logMessage string
	// shown is its place in the editor's last setBreakpoints answer.
	shown int
}

type dap struct {
	in    *bufio.Reader
	out   io.Writer
	logs  io.Writer
	outMu sync.Mutex
	seq   int

	project, main, app string
	srcMap             *jsrt.SourceMap
	phone              *jsrtConn
	ssh                *exec.Cmd
	stopOnEntry        bool
	mu                 sync.Mutex
	bps                map[string][]dapBreakpoint
	stopText           string
	hello              chan map[string]any
	// renames: by source of the map, the names esbuild renamed (original
	// to main.js's, SourceMap.Renames); frames: the source each frame of
	// the last stack is in.
	renames map[string]map[string]string
	frames  []string
}

func serveDAP(in io.Reader, out, logs io.Writer) error {
	d := &dap{in: bufio.NewReader(in), out: out, logs: logs, bps: map[string][]dapBreakpoint{}, hello: make(chan map[string]any, 1)}
	defer d.stop()
	for {
		m, err := readDAP(d.in)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if m.Type != "request" {
			continue
		}
		if done := d.handle(m); done {
			return nil
		}
	}
}

// readDAP reads one message: headers, a blank line, Content-Length bytes.
func readDAP(r *bufio.Reader) (dapMessage, error) {
	var m dapMessage
	n := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return m, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			n, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	if n < 0 || n > 64<<20 {
		return m, errors.New("dap: no Content-Length")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return m, err
	}
	return m, json.Unmarshal(body, &m)
}

func (d *dap) send(v map[string]any) {
	d.outMu.Lock()
	defer d.outMu.Unlock()
	d.seq++
	v["seq"] = d.seq
	raw, _ := json.Marshal(v)
	fmt.Fprintf(d.out, "Content-Length: %d\r\n\r\n%s", len(raw), raw)
}

func (d *dap) event(name string, body map[string]any) {
	m := map[string]any{"type": "event", "event": name}
	if body != nil {
		m["body"] = body
	}
	d.send(m)
}

func (d *dap) respond(req dapMessage, body map[string]any, err error) {
	m := map[string]any{"type": "response", "request_seq": req.Seq, "command": req.Command, "success": err == nil}
	if err != nil {
		m["message"] = err.Error()
	}
	if body != nil {
		m["body"] = body
	}
	d.send(m)
}

// output says text in the debug console.
func (d *dap) output(category, text string) {
	d.event("output", map[string]any{"category": category, "output": text})
}

// consoleWriter shows what is written in the debug console.
type consoleWriter struct {
	d        *dap
	category string
}

func (w consoleWriter) Write(p []byte) (int, error) {
	w.d.output(w.category, string(p))
	return len(p), nil
}

func (d *dap) handle(req dapMessage) (done bool) {
	var body map[string]any
	var err error
	args := map[string]any{}
	_ = json.Unmarshal(req.Arguments, &args)
	switch req.Command {
	case "initialize":
		body = map[string]any{
			"supportsConfigurationDoneRequest":  true,
			"supportsConditionalBreakpoints":    true,
			"supportsHitConditionalBreakpoints": true,
			"supportsLogPoints":                 true,
			"supportsFunctionBreakpoints":       true,
			"supportsEvaluateForHovers":         true,
			"supportsSetVariable":               true,
			"supportsExceptionInfoRequest":      true,
			"supportsTerminateRequest":          true,
			"exceptionBreakpointFilters": []any{
				map[string]any{"filter": "uncaught", "label": "Uncaught Exceptions", "default": true},
				map[string]any{"filter": "all", "label": "All Exceptions", "default": false},
			},
		}
	case "launch", "attach":
		var a dapArgs
		if err = json.Unmarshal(req.Arguments, &a); err == nil {
			err = d.start(a, req.Command == "launch")
		}
		if err == nil {
			d.respond(req, nil, nil)
			d.event("initialized", nil)
			return false
		}
	case "setBreakpoints":
		body, err = d.setBreakpoints(req.Arguments)
	case "setFunctionBreakpoints":
		body, err = d.setFunctionBreakpoints(req.Arguments)
	case "setExceptionBreakpoints":
		var a struct {
			Filters []string `json:"filters"`
		}
		_ = json.Unmarshal(req.Arguments, &a)
		_, err = d.call("setExceptions", map[string]any{"uncaught": slices.Contains(a.Filters, "uncaught"), "all": slices.Contains(a.Filters, "all")})
	case "configurationDone":
		_, err = d.call("run", map[string]any{"stopOnEntry": d.stopOnEntry})
	case "threads":
		body = map[string]any{"threads": []any{map[string]any{"id": 1, "name": d.app}}}
	case "stackTrace":
		body, err = d.stackTrace()
	case "scopes":
		frame := intArg(args, "frameId") - 1
		var scopes []any
		for k, name := range []string{"Local", "Closure", "Global"} {
			scopes = append(scopes, map[string]any{"name": name, "variablesReference": scopeRef + 3*frame + k, "expensive": k == 2})
		}
		body = map[string]any{"scopes": scopes}
	case "variables":
		body, err = d.variables(intArg(args, "variablesReference"), args)
	case "setVariable":
		ref := intArg(args, "variablesReference")
		name, _ := args["name"].(string)
		value, _ := args["value"].(string)
		q := map[string]any{"name": name, "value": d.js(value, "")}
		if ref >= scopeRef {
			frame := (ref - scopeRef) / 3
			src := d.frameSource(frame)
			q["frame"], q["scope"], q["value"] = frame, scopeKinds[(ref-scopeRef)%3], d.js(value, src)
			if gen, ok := d.renames[src][name]; ok {
				q["name"] = gen
			}
		} else {
			q["ref"] = ref
		}
		var r map[string]any
		if r, err = d.call("setVariable", q); err == nil {
			body = map[string]any{"value": r["value"], "type": r["type"], "variablesReference": r["ref"]}
		}
	case "evaluate":
		expr, _ := args["expression"].(string)
		q := map[string]any{"expression": d.js(expr, "")}
		if f := intArg(args, "frameId"); f > 0 {
			q["frame"], q["expression"] = f-1, d.js(expr, d.frameSource(f-1))
		}
		var r map[string]any
		if r, err = d.call("evaluate", q); err == nil {
			body = map[string]any{"result": r["value"], "type": r["type"], "variablesReference": r["ref"]}
		}
	case "continue", "next", "stepIn", "stepOut", "pause":
		_, err = d.call(req.Command, nil)
		if req.Command == "continue" {
			body = map[string]any{"allThreadsContinued": true}
		}
	case "exceptionInfo":
		body = map[string]any{"exceptionId": "exception", "description": d.stopText, "breakMode": "unhandled"}
	case "source":
		body, err = d.source(intArg(args, "sourceReference"))
	case "disconnect", "terminate":
		d.stop()
		d.respond(req, nil, nil)
		if req.Command == "terminate" {
			d.event("terminated", nil)
		}
		return req.Command == "disconnect"
	default:
		err = fmt.Errorf("%s is not supported", req.Command)
	}
	d.respond(req, body, err)
	return false
}

func intArg(args map[string]any, k string) int {
	f, _ := args[k].(float64)
	return int(f)
}

// call asks the phone's debugger.
func (d *dap) call(cmd string, args map[string]any) (map[string]any, error) {
	if d.phone == nil {
		return nil, errors.New("not connected to the phone")
	}
	return d.phone.call(cmd, args)
}

// start builds, installs (launch) and connects: the app restarts on the
// phone under the debugger, waiting for its breakpoints.
func (d *dap) start(a dapArgs, install bool) error {
	if a.Project == "" {
		return errors.New(`the debug configuration needs "project" (the app's directory)`)
	}
	project, err := filepath.Abs(a.Project)
	if err != nil {
		return err
	}
	m, _, err := readManifest(project)
	if err != nil {
		return err
	}
	d.project, d.app, d.stopOnEntry = project, m.ID, a.StopOnEntry
	tmp, err := os.MkdirTemp("", "kario-dap-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	store := cmp.Or(a.DevStore, defaultDevStore())
	o := packageOptions{out: filepath.Join(tmp, "app.phoneapp"), devStore: store, name: cmp.Or(a.Developer, "Example Developer"),
		native: a.Native, debug: true, mapFile: filepath.Join(tmp, "main.js.map"), symbols: "-"}
	d.output("console", fmt.Sprintf("Building %s for the debugger…\n", project))
	if _, err := packageApp(project, o); err != nil {
		return err
	}
	raw, err := os.ReadFile(o.out)
	if err != nil {
		return err
	}
	srcMap, err := os.ReadFile(o.mapFile)
	if err != nil {
		return fmt.Errorf("no source map: %w", err)
	}
	if d.srcMap, err = jsrt.ParseSourceMap(srcMap); err != nil {
		return err
	}
	d.main = jsrt.Main(m.Main)
	sum, err := packagedSum(raw, d.main)
	if err != nil {
		return err
	}
	if js, err := packagedFile(raw, d.main); err == nil {
		d.renames = d.srcMap.Renames(js)
	}
	dev := device{ssh: strings.Fields(cmp.Or(a.SSH, envOr("KARIO_SSH", "ssh"))), to: cmp.Or(a.To, envOr("KARIO_DEVICE", "root@"+devctl.USBAddr)),
		stdout: consoleWriter{d, "console"}, stderr: consoleWriter{d, "stderr"}}
	if len(dev.ssh) == 0 {
		return errors.New("no ssh command")
	}
	if install {
		if _, err := dev.send(raw, store); err != nil {
			return err
		}
	}
	argv := append(append([]string{}, dev.ssh[1:]...), "-T", dev.to, devctl.Program, devctl.OpDebug)
	if !install {
		argv = append(argv, "-attach")
	}
	argv = append(argv, d.app)
	cmd := exec.Command(dev.ssh[0], argv...)
	w, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	r, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	lines := bufio.NewReader(r)
	first, err := lines.ReadString('\n')
	attached := strings.HasPrefix(first, "attached to ")
	if !attached && !strings.HasPrefix(first, "debugging ") {
		w.Close()
		werr := cmd.Wait()
		if _, why, refused := strings.Cut(stderr.String(), devctl.Program+": "); refused {
			return fmt.Errorf("the phone: %s", strings.TrimSpace(why))
		}
		return errors.Join(err, sshErr(werr, dev.to), errors.New(strings.TrimSpace(stderr.String()+first)))
	}
	d.ssh = cmd
	if attached {
		d.output("console", "The phone "+first)
	} else {
		d.output("console", "The phone is "+first)
		if !install {
			d.output("console", "The app was not running as one a debugger can join: the phone started it under the debugger.\n")
		}
	}
	if f := strings.Fields(strings.TrimSuffix(strings.TrimSpace(first), ")")); len(f) == 0 || f[len(f)-1] != sum {
		d.output("stderr", "The app on the phone is not this build of "+project+": breakpoints and stacks may be off (launch installs it).\n")
	}
	d.phone = newJSRTConn(w)
	go d.phone.read(lines, d.onPhone)
	select {
	case h, ok := <-d.hello:
		if !ok {
			return errors.New("the phone's debugger went away")
		}
		how := "Debugging"
		if h["attached"] == true {
			how = "Attached to"
		}
		d.output("console", fmt.Sprintf("%s %s on the phone (%v).\n", how, d.app, h["engine"]))
	case <-time.After(30 * time.Second):
		return errors.New("the app's debugger did not answer")
	}
	return nil
}

// packagedFile is a file of the package.
func packagedFile(raw []byte, name string) ([]byte, error) {
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("%s is not in the package", name)
		}
		if h.Name == name {
			return io.ReadAll(io.LimitReader(tr, jsrt.MaxSourceMap))
		}
	}
}

// js is TypeScript src as the debugger on the phone evaluates it in a
// frame of source: types stripped, renamed names renamed; as it is when
// esbuild cannot read it (the phone says why).
func (d *dap) js(src, source string) string {
	r := api.Transform(src, api.TransformOptions{Loader: api.LoaderTS, Define: d.renames[source], LogLevel: api.LogLevelSilent})
	if len(r.Errors) > 0 {
		return src
	}
	return strings.TrimSuffix(strings.TrimRight(string(r.Code), "\n"), ";")
}

// logMessage is a logpoint's message with each {expression} made js.
func (d *dap) logMessage(msg, source string) string {
	var out strings.Builder
	for {
		open := strings.IndexByte(msg, '{')
		if open < 0 {
			out.WriteString(msg)
			return out.String()
		}
		depth, end := 0, -1
		for i := open; i < len(msg) && end < 0; i++ {
			switch msg[i] {
			case '{':
				depth++
			case '}':
				if depth--; depth == 0 {
					end = i
				}
			}
		}
		if end < 0 {
			out.WriteString(msg)
			return out.String()
		}
		out.WriteString(msg[:open] + "{" + d.js(msg[open+1:end], source) + "}")
		msg = msg[end+1:]
	}
}

// frameSource is the source frame (from 0) of the last stack is in.
func (d *dap) frameSource(frame int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if frame < 0 || frame >= len(d.frames) {
		return ""
	}
	return d.frames[frame]
}

// packagedSum is a file's SHA-256 as the package's signature lists it.
func packagedSum(raw []byte, name string) (string, error) {
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if err != nil {
			return "", errors.New("not a package (no signature)")
		}
		if h.Name != pkg.SigPath {
			continue
		}
		var sig pkg.Signature
		if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&sig); err != nil {
			return "", err
		}
		for _, f := range sig.Files {
			if f.Path == name {
				return f.SHA256, nil
			}
		}
		return "", fmt.Errorf("%s is not in the package", name)
	}
}

// onPhone turns the phone's debugger's events into the editor's.
func (d *dap) onPhone(ev map[string]any) {
	switch ev["event"] {
	case "hello":
		select {
		case d.hello <- ev:
		default:
		}
	case "stopped":
		reason, _ := ev["reason"].(string)
		text, _ := ev["text"].(string)
		d.mu.Lock()
		d.stopText = text
		d.mu.Unlock()
		desc := map[string]string{"debugger": "Paused on a debugger statement", "exception": "Paused on an exception", "entry": "Paused on entry",
			"function breakpoint": "Paused on a function breakpoint"}[reason]
		body := map[string]any{"reason": reason, "threadId": 1, "allThreadsStopped": true}
		if desc != "" {
			body["description"] = desc
		}
		if text != "" {
			body["text"] = text
		}
		d.event("stopped", body)
	case "output":
		cat, _ := ev["category"].(string)
		text, _ := ev["text"].(string)
		if file, _ := ev["file"].(string); file != "" {
			d.logpoint(text, file, intArg(ev, "line"), intArg(ev, "column"))
			return
		}
		if cat != "stderr" {
			cat = "stdout"
		}
		d.output(cat, text)
	case "closed":
		close(d.hello)
		d.event("terminated", nil)
	}
}

func (d *dap) stop() {
	if d.phone != nil {
		d.phone.close()
	}
	if d.ssh != nil {
		done := make(chan struct{})
		go func() { _ = d.ssh.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = d.ssh.Process.Kill()
			<-done
		}
		d.ssh = nil
	}
}

// sourcePath is the map's name for an editor's file, "" outside the project.
func (d *dap) sourcePath(path string) string {
	rel, err := filepath.Rel(d.project, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

// logpoint shows a logpoint's line in the debug console at its place in
// the TypeScript.
func (d *dap) logpoint(text, file string, line, col int) {
	body := map[string]any{"category": "console", "output": text}
	if file == d.main && d.srcMap != nil {
		if p, ok := d.srcMap.Original(line, col); ok {
			body["source"], body["line"], body["column"] = d.source1(p.Source), p.Line, p.Column
		}
	}
	d.event("output", body)
}

func (d *dap) setBreakpoints(raw json.RawMessage) (map[string]any, error) {
	var a struct {
		Source struct {
			Path string `json:"path"`
		} `json:"source"`
		Breakpoints []struct {
			Line         int    `json:"line"`
			Condition    string `json:"condition"`
			HitCondition string `json:"hitCondition"`
			LogMessage   string `json:"logMessage"`
		} `json:"breakpoints"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	src := d.sourcePath(a.Source.Path)
	var out []any
	var set []dapBreakpoint
	for _, b := range a.Breakpoints {
		var gens []int
		line := 0
		if src != "" && d.srcMap != nil {
			gens, line = d.srcMap.Generated(src, b.Line, 20)
		}
		if gens == nil {
			out = append(out, map[string]any{"verified": false, "line": b.Line, "message": "no code of the app on this line or the next ones"})
			continue
		}
		out = append(out, map[string]any{"verified": true, "line": line})
		cond, log := b.Condition, b.LogMessage
		if cond != "" {
			cond = d.js(cond, src)
		}
		if log != "" {
			log = d.logMessage(log, src)
		}
		set = append(set, dapBreakpoint{gens: gens, condition: cond, hitCondition: b.HitCondition, logMessage: log, shown: len(out) - 1})
	}
	d.mu.Lock()
	d.bps[a.Source.Path] = set
	// A breakpoint on several lines of main.js is one group: its hits
	// count together. shownAt is each line's breakpoint in out (-1: another
	// file's).
	all := []any{}
	var shownAt []int
	group := 0
	for _, path := range slices.Sorted(maps.Keys(d.bps)) {
		for _, b := range d.bps[path] {
			for _, g := range b.gens {
				all = append(all, map[string]any{"line": g, "condition": b.condition, "hitCondition": b.hitCondition, "logMessage": b.logMessage, "group": group})
				at := -1
				if path == a.Source.Path {
					at = b.shown
				}
				shownAt = append(shownAt, at)
			}
			group++
		}
	}
	d.mu.Unlock()
	r, err := d.call("setBreakpoints", map[string]any{"file": d.main, "breakpoints": all})
	if err != nil {
		return nil, err
	}
	// The phone refuses a hit count it cannot read: the editor says why.
	got, _ := r["breakpoints"].([]any)
	for k, raw := range got {
		if b, _ := raw.(map[string]any); k < len(shownAt) && shownAt[k] >= 0 && b["verified"] == false {
			out[shownAt[k]] = map[string]any{"verified": false, "line": out[shownAt[k]].(map[string]any)["line"], "message": b["message"]}
		}
	}
	if out == nil {
		out = []any{}
	}
	return map[string]any{"breakpoints": out}, nil
}

// setFunctionBreakpoints: by the function's name in the TypeScript, which
// an unminified build keeps (esbuild renames only a clash of names).
func (d *dap) setFunctionBreakpoints(raw json.RawMessage) (map[string]any, error) {
	var a struct {
		Breakpoints []struct {
			Name         string `json:"name"`
			Condition    string `json:"condition"`
			HitCondition string `json:"hitCondition"`
		} `json:"breakpoints"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	list := []any{}
	for _, b := range a.Breakpoints {
		cond := b.Condition
		if cond != "" {
			cond = d.js(cond, "")
		}
		list = append(list, map[string]any{"name": b.Name, "condition": cond, "hitCondition": b.HitCondition})
	}
	r, err := d.call("setFunctionBreakpoints", map[string]any{"breakpoints": list})
	if err != nil {
		return nil, err
	}
	out, _ := r["breakpoints"].([]any)
	if out == nil {
		out = []any{}
	}
	return map[string]any{"breakpoints": out}, nil
}

func (d *dap) stackTrace() (map[string]any, error) {
	r, err := d.call("stack", nil)
	if err != nil {
		return nil, err
	}
	frames, _ := r["frames"].([]any)
	var out []any
	sources := make([]string, len(frames))
	defer func() {
		d.mu.Lock()
		d.frames = sources
		d.mu.Unlock()
	}()
	for i, raw := range frames {
		f, _ := raw.(map[string]any)
		name, _ := f["name"].(string)
		if name == "" {
			name = "(anonymous)"
		}
		sf := map[string]any{"id": i + 1, "name": name, "line": 0, "column": 0}
		file, _ := f["file"].(string)
		line, col := intArg(f, "line"), intArg(f, "column")
		switch {
		case f["native"] == true:
			sf["name"], sf["presentationHint"] = name+" (native)", "subtle"
		case file == d.main && d.srcMap != nil:
			if p, ok := d.srcMap.Original(line, col); ok {
				sf["source"], sf["line"], sf["column"] = d.source1(p.Source), p.Line, p.Column
				sources[i] = p.Source
				break
			}
			fallthrough
		default:
			sf["source"], sf["line"], sf["column"] = map[string]any{"name": file, "presentationHint": "deemphasize"}, line, col
		}
		out = append(out, sf)
	}
	if out == nil {
		out = []any{}
	}
	return map[string]any{"stackFrames": out, "totalFrames": len(out)}, nil
}

// source1 is how the editor finds a source of the map: a file of the
// project by its path, the SDK's by reference to the map's copy.
func (d *dap) source1(src string) map[string]any {
	if !strings.Contains(src, ":") {
		p := filepath.Join(d.project, filepath.FromSlash(src))
		return map[string]any{"name": filepath.Base(p), "path": p}
	}
	i := d.srcMap.SourceIndex(src)
	return map[string]any{"name": src, "sourceReference": i + 1, "presentationHint": "deemphasize"}
}

func (d *dap) source(ref int) (map[string]any, error) {
	if d.srcMap == nil || ref < 1 || ref > len(d.srcMap.SourcesContent) {
		return nil, errors.New("no such source")
	}
	return map[string]any{"content": d.srcMap.SourcesContent[ref-1], "mimeType": "text/typescript"}, nil
}

// variables are an object's properties (paged as the editor asks: filter,
// start, count) or a frame's variables, a renamed one under the name the
// source has unless that name is taken; a getter's value is fetched when
// the editor shows it (lazy).
func (d *dap) variables(ref int, args map[string]any) (map[string]any, error) {
	q := map[string]any{"ref": ref}
	for _, k := range []string{"filter", "start", "count"} {
		if v, ok := args[k]; ok {
			q[k] = v
		}
	}
	original := map[string]string{}
	if ref >= scopeRef {
		frame := (ref - scopeRef) / 3
		q = map[string]any{"frame": frame, "scope": scopeKinds[(ref-scopeRef)%3]}
		for orig, gen := range d.renames[d.frameSource(frame)] {
			original[gen] = orig
		}
	}
	r, err := d.call("variables", q)
	if err != nil {
		return nil, err
	}
	vars, _ := r["variables"].([]any)
	taken := map[any]bool{}
	for _, raw := range vars {
		v, _ := raw.(map[string]any)
		taken[v["name"]] = true
	}
	out := []any{}
	for _, raw := range vars {
		v, _ := raw.(map[string]any)
		name := v["name"]
		if gen, _ := name.(string); original[gen] != "" && !taken[original[gen]] {
			name = original[gen]
		}
		e := map[string]any{"name": name, "value": v["value"], "type": v["type"], "variablesReference": v["ref"]}
		if n, ok := v["indexed"]; ok {
			e["indexedVariables"] = n
		}
		if v["lazy"] == true {
			e["presentationHint"] = map[string]any{"lazy": true}
		}
		out = append(out, e)
	}
	return map[string]any{"variables": out}, nil
}

// jsrtConn is the computer's end of kario-jsrt's debugger socket: requests
// answered by id, events to a function.
type jsrtConn struct {
	w    io.WriteCloser
	mu   sync.Mutex
	next int
	wait map[int]chan map[string]any
	gone bool
}

func newJSRTConn(w io.WriteCloser) *jsrtConn {
	return &jsrtConn{w: w, wait: map[int]chan map[string]any{}}
}

func (c *jsrtConn) read(r *bufio.Reader, onEvent func(map[string]any)) {
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		if _, ok := m["event"]; ok {
			onEvent(m)
			continue
		}
		id, _ := m["id"].(float64)
		c.mu.Lock()
		ch := c.wait[int(id)]
		delete(c.wait, int(id))
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
	c.mu.Lock()
	c.gone = true
	for id, ch := range c.wait {
		close(ch)
		delete(c.wait, id)
	}
	c.mu.Unlock()
	onEvent(map[string]any{"event": "closed"})
}

func (c *jsrtConn) call(cmd string, args map[string]any) (map[string]any, error) {
	c.mu.Lock()
	if c.gone {
		c.mu.Unlock()
		return nil, errors.New("the app's debugger is gone")
	}
	c.next++
	id := c.next
	ch := make(chan map[string]any, 1)
	c.wait[id] = ch
	c.mu.Unlock()
	req := map[string]any{"id": id, "cmd": cmd}
	for k, v := range args {
		req[k] = v
	}
	raw, _ := json.Marshal(req)
	if _, err := c.w.Write(append(raw, '\n')); err != nil {
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, errors.New("the app's debugger is gone")
		}
		if e, ok := m["error"].(string); ok {
			return m, errors.New(e)
		}
		return m, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("the phone did not answer %s", cmd)
	}
}

func (c *jsrtConn) close() { _ = c.w.Close() }
