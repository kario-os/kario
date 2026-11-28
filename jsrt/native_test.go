package jsrt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/kario-os/kario/jsrt/nativetest"
	"github.com/kario-os/kario/ui"
)

// nativeApp is testdata/native with so as lib/<this machine>/probe.so and
// declare as the manifest's "native".
func nativeApp(t *testing.T, so []byte, declare ...string) string {
	t.Helper()
	arch := HostArch()
	if arch == "" {
		t.Skip("modules are built for arm64 and x86_64 only")
	}
	dir := appCopy(t, "native")
	if so != nil {
		writeModule(t, dir, so)
	}
	m := map[string]any{"id": "com.example.test", "name": "Native", "runtime": Runtime, "main": DefaultMain, "native": declare}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeModule(t *testing.T, dir string, so []byte) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(NativePath(HostArch(), "probe")))
	_ = os.Remove(p)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, so, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func load(spec string) ui.Msg {
	return ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "load", Value: spec, Fields: map[string]string{}}
}

// A native module from JavaScript: numbers, strings, bytes changed in
// place and returned, JSON, errors; asynchronous functions on worker
// threads at once; events from the module's own thread, in order.
func TestNative(t *testing.T) { testNative(t, jsrtEngine(t)) }

func testNative(t *testing.T, start engine) { testNativeModule(t, start, nativetest.Build(t)) }

// The same module written with kario_native.hpp and with the Rust crate
// behaves as the C one, on both engines, and what the wrappers add holds:
// typed arguments refuse what they are not, an exception or a panic (in an
// asynchronous function too) becomes the Error JavaScript gets, and the app
// runs on.
func TestNativeCpp(t *testing.T)  { testWrapped(t, jsrtEngine(t), nativetest.BuildCpp(t), cppExtra) }
func TestNativeRust(t *testing.T) { testWrapped(t, jsrtEngine(t), nativetest.BuildRust(t), rustExtra) }
func TestNodeNativeCpp(t *testing.T) {
	testWrapped(t, nativeNodeEngine(t), nativetest.BuildCpp(t), cppExtra)
}
func TestNodeNativeRust(t *testing.T) {
	testWrapped(t, nativeNodeEngine(t), nativetest.BuildRust(t), rustExtra)
}

const (
	typedExtra = "extra 42 | Error:typed: argument 1 is a string, not a number | Error:typed: argument 1 is not an integer in range | "
	cppExtra   = typedExtra + "Error:thrown: from C++ | Error:unknown: threw something that is not a std::exception | rejected:thrown: from C++"
	rustExtra  = typedExtra + "Error:thrown panicked: thrown: from Rust | Error:invalid digit found in string | rejected:thrownAsync panicked: thrown: from Rust"
)

func testWrapped(t *testing.T, start engine, so []byte, extra string) {
	testNativeModule(t, start, so)
	dir := nativeApp(t, so, "probe")
	s := start(t, t.TempDir(), "en", filepath.Join(dir, DefaultMain))
	s.hello("", nil)
	s.screenText("ready")
	s.send(action("extra"))
	if got := s.screenText("extra "); !strings.Contains(got, extra) {
		t.Fatalf("got  %s\nwant %s", got, extra)
	}
	s.send(action("sync"))
	s.screenText("sync 5 ABC")
}

func testNativeModule(t *testing.T, start engine, so []byte) {
	dir := nativeApp(t, so, "probe")
	s := start(t, t.TempDir(), "en", filepath.Join(dir, DefaultMain))
	s.hello("", nil)
	s.screenText("ready")
	s.send(action("sync"))
	s.screenText(`sync 5 ABC 2 1,253,5 3,2,1 {"a":[1,"x\n"],"b":null} u0bnsyoo true 100`)
	s.send(action("throw"))
	s.screenText("threw Error:nope SyntaxError Error:upper:")
	s.send(action("async"))
	s.screenText(`async true true 6,5,4 4,5,6 "q\"\t" later`)
	s.send(action("events"))
	var want []string
	for i := 0; i < 50; i++ {
		want = append(want, string(rune('0'+i/10))+string(rune('0'+i%10)))
	}
	text := s.screenText("events ")
	got := strings.Fields(strings.SplitN(text, "events ", 2)[1])
	if len(got) < 2 || got[1] != "true" || strings.Count(got[0], ",") != 49 || !strings.HasPrefix(got[0], "0,1,2,") || !strings.HasSuffix(got[0], ",48,49") {
		t.Fatalf("events: %s", text)
	}
}

// What kario-jsrt refuses to load, each with the reason: a module the
// manifest does not declare, a name that is not one, a library elsewhere
// or for the other architecture, one that is not a module of this
// interface, one whose init fails.
func TestNativeRefused(t *testing.T) { testNativeRefused(t, jsrtEngine(t)) }

func testNativeRefused(t *testing.T, start engine) {
	probe := nativetest.Build(t)
	arch, other := HostArch(), nativetest.Other()
	outside := filepath.Join(t.TempDir(), "probe.so")
	if err := os.WriteFile(outside, probe, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, spec, want string
		declare          []string
		so               []byte
		fix              func(dir string)
	}{
		{"not declared", "native:probe", "the manifest does not declare it", nil, probe, nil},
		{"capital letters", "native:Probe", "a native module's name", []string{"probe"}, probe, nil},
		{"a path", "native:../probe", "a native module's name", []string{"probe"}, probe, nil},
		{"missing", "native:probe", "no lib/" + arch + "/probe.so in the app's package", []string{"probe"}, nil, nil},
		{"outside the package", "native:probe", "is not a file of the app's package", []string{"probe"}, nil, func(dir string) {
			p := filepath.Join(dir, filepath.FromSlash(NativePath(arch, "probe")))
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.Symlink(outside, p); err != nil {
				t.Fatal(err)
			}
		}},
		{"other architecture", "native:probe", "a library for " + other + ", not " + arch, []string{"probe"}, nativetest.Retarget(probe, other), nil},
		{"not a library", "native:probe", "not an ELF library", []string{"probe"}, []byte("not a library at all, just text"), nil},
		{"no entry points", "native:probe", "is not a native module", []string{"probe"}, nativetest.Plain(t), nil},
		{"another interface", "native:probe", "built for the native interface 2.0", []string{"probe"}, nativetest.Build(t, "PROBE_ABI=0x20000"), nil},
		{"a newer minor version", "native:probe", "built for the native interface 1.7", []string{"probe"}, nativetest.Build(t, "PROBE_ABI=0x10007"), nil},
		{"init fails", "native:probe", "kario_native_init returned 7", []string{"probe"}, nativetest.Build(t, "PROBE_INIT_FAILS"), nil},
	} {
		dir := nativeApp(t, c.so, c.declare...)
		if c.fix != nil {
			c.fix(dir)
		}
		s := start(t, t.TempDir(), "en", filepath.Join(dir, DefaultMain))
		s.hello("", nil)
		s.screenText("ready")
		s.send(load(c.spec))
		if text := s.screenText("refused"); !strings.Contains(text, c.want) {
			t.Errorf("%s: %s, want %q", c.name, text, c.want)
		}
	}

	// A static import of a module that is refused keeps the app from
	// starting at all.
	dir := nativeApp(t, probe)
	if err := os.WriteFile(filepath.Join(dir, DefaultMain), []byte(`import { add } from "native:probe";
import * as kario from "kario";
kario.on("start", () => kario.reset({ id: "main", title: String(add(1, 2)), body: [] }));
`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := start(t, t.TempDir(), "en", filepath.Join(dir, DefaultMain))
	if code := s.exit(); code != 1 || !strings.Contains(s.log(), "the manifest does not declare it") {
		t.Fatalf("static import of an undeclared module: exit %d; log:\n%s", code, s.log())
	}
}

// A crash inside a module ends the app, on the engine's thread or a
// worker's; its log says in which function and has the native frames,
// which the module's debug information maps to its source lines: the
// faulting function, its caller in the module, then kario-jsrt (the
// unwinder got past the signal).
func TestNativeCrash(t *testing.T) {
	bin := binary(t)
	probe := nativetest.Build(t)
	dir := nativeApp(t, probe, "probe")
	syms, err := LoadSymbols(NativeScheme+"probe", probe)
	if err != nil || syms.BuildID == "" {
		t.Fatalf("probe: %v, build-id %q", err, syms.BuildID)
	}
	src, _ := os.ReadFile(filepath.Join(nativetest.Dir(), "probe.c"))
	lineOf := func(text string) string {
		for i, l := range strings.Split(string(src), "\n") {
			if strings.Contains(l, text) {
				return "probe.c:" + strconv.Itoa(i+1)
			}
		}
		t.Fatalf("no %q in probe.c", text)
		return ""
	}
	for _, fn := range []string{"crash", "crashAsync"} {
		s := launch(t, t.TempDir(), "en", bin, filepath.Join(dir, DefaultMain))
		s.hello("", nil)
		s.screenText("ready")
		s.send(action(fn))
		code := s.exit()
		lines := strings.Split(strings.TrimSpace(s.log()), "\n")
		bt := LastBacktrace(lines)
		if code == 0 || len(bt) < 4 || !strings.HasPrefix(bt[0], "kario-jsrt: crashed in native:probe."+fn+": SIGSEGV at 0x0") || bt[len(bt)-1] != BacktraceEnd {
			t.Fatalf("%s: exit %d; log:\n%s", fn, code, s.log())
		}
		// The Shell keeps it only when it ends the log.
		if tail := lines[len(lines)-len(bt):]; strings.Join(tail, "\n") != strings.Join(bt, "\n") {
			t.Fatalf("%s: the backtrace does not end the log:\n%s", fn, s.log())
		}
		frames := ParseBacktrace(bt)
		if len(frames) < 3 || frames[0].Object != NativeScheme+"probe" || frames[0].BuildID != syms.BuildID ||
			frames[1].Object != NativeScheme+"probe" || frames[2].Object != Program {
			t.Fatalf("%s: frames %+v; log:\n%s", fn, frames, s.log())
		}
		out := Symbolize(frames, []*Symbols{syms})
		if want := "fault (" + lineOf("static void fault(") + ")"; !strings.HasSuffix(out[0], want) {
			t.Errorf("%s: #0 %q, want %q", fn, out[0], want)
		}
		if want := "crash (" + lineOf("	fault(NULL);") + ")"; !strings.HasSuffix(out[1], want) {
			t.Errorf("%s: #1 %q, want %q", fn, out[1], want)
		}
	}
}

// A crash still ends the app, and as a crash, where it is the first
// process of a pid namespace (on the device), which the kernel delivers no
// signal it raises with the default action: a fault comes again once the
// handler returns and kills it as elsewhere; abort() (an assert, an
// uncaught C++ exception) cannot end it with SIGABRT, so it exits with
// 128 + 6 instead of going on into abort's fallbacks (on x86-64 a fault
// with a second backtrace). Either way the backtrace naming the signal
// ends the log, which the Shell keeps in its crash record.
func TestNativeCrashFirstProcess(t *testing.T) {
	bin := binary(t)
	dir := nativeApp(t, nativetest.Build(t), "probe")
	run := func(t *testing.T, attr *syscall.SysProcAttr, fn, sig string, want func(syscall.WaitStatus) bool) {
		s := launchIn(t, attr, t.TempDir(), "en", nil, nil, bin, filepath.Join(dir, DefaultMain))
		s.hello("", nil)
		s.screenText("ready")
		s.send(action(fn))
		ws := s.ended()
		lines := strings.Split(strings.TrimSpace(s.log()), "\n")
		bt := LastBacktrace(lines)
		if !want(ws) || len(bt) < 3 || !strings.HasPrefix(bt[0], "kario-jsrt: crashed in native:probe."+fn+": "+sig) ||
			strings.Join(lines[len(lines)-len(bt):], "\n") != strings.Join(bt, "\n") {
			t.Fatalf("%s: ended %v; log:\n%s", fn, ws, s.log())
		}
		if n := strings.Count(s.log(), BacktraceHead); n != 1 {
			t.Fatalf("%s: %d backtraces; log:\n%s", fn, n, s.log())
		}
	}
	signaled := func(sig syscall.Signal) func(syscall.WaitStatus) bool {
		return func(ws syscall.WaitStatus) bool { return ws.Signaled() && ws.Signal() == sig }
	}
	t.Run("abort", func(t *testing.T) { run(t, nil, "abort", "SIGABRT", signaled(syscall.SIGABRT)) })
	t.Run("first process", func(t *testing.T) {
		attr := firstProcess(t)
		run(t, attr, "crash", "SIGSEGV", signaled(syscall.SIGSEGV))
		run(t, attr, "abort", "SIGABRT", func(ws syscall.WaitStatus) bool { return ws.Exited() && ws.ExitStatus() == 128+int(syscall.SIGABRT) })
	})
}

// seccompMode is the Seccomp line of a process's status (0 none, 2 a
// filter).
func seccompMode(t *testing.T, pid int) string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(l, "Seccomp:"); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Skip("no Seccomp line in /proc/<pid>/status")
	return ""
}

// Once its modules are loaded, an app is sealed: a module that maps
// executable memory, makes memory executable or runs a program gets
// EPERM, and loads no library; threads still start and end, and the app
// runs on. An app without native modules is not sealed at all.
func TestNativeSealed(t *testing.T) {
	bin := binary(t)
	dir := nativeApp(t, nativetest.Build(t), "probe")
	plain := filepath.Join(t.TempDir(), "plain.so")
	if err := os.WriteFile(plain, nativetest.Plain(t), 0o644); err != nil {
		t.Fatal(err)
	}
	s := launch(t, t.TempDir(), "en", bin, filepath.Join(dir, DefaultMain))
	s.hello("", nil)
	s.screenText("ready")
	s.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "exec", Value: plain, Fields: map[string]string{}})
	s.screenText("exec mmap EPERM, rw ok, mprotect EPERM, personality EPERM, exec EPERM, dlopen failed thread 7")
	s.send(action("sync"))
	s.screenText("sync 5 ABC")
	s.send(action("async"))
	s.screenText("async true true")
	if seccompMode(t, os.Getpid()) != "0" {
		t.Skip("this test runs under a seccomp filter of its own: cannot tell a sealed app from another")
	}
	if m := seccompMode(t, s.cmd.Process.Pid); m != "2" {
		t.Errorf("an app with native modules: Seccomp %s", m)
	}
	js := launch(t, t.TempDir(), "en", bin, filepath.Join(appCopy(t, "app"), DefaultMain))
	js.hello("", nil)
	js.expect(ui.MsgReset)
	if m := seccompMode(t, js.cmd.Process.Pid); m != "0" {
		t.Errorf("an app without native modules: Seccomp %s", m)
	}
}

// Node.js seals nothing (V8 compiles to machine code), so the same probe
// does there what the seal stops: the control for TestNativeSealed.
func TestNodeNativeUnsealed(t *testing.T) {
	start := nativeNodeEngine(t)
	dir := nativeApp(t, nativetest.Build(t), "probe")
	plain := filepath.Join(t.TempDir(), "plain.so")
	if err := os.WriteFile(plain, nativetest.Plain(t), 0o644); err != nil {
		t.Fatal(err)
	}
	s := start(t, t.TempDir(), "en", filepath.Join(dir, DefaultMain))
	s.hello("", nil)
	s.screenText("ready")
	s.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "exec", Value: plain, Fields: map[string]string{}})
	s.screenText("exec mmap ok, rw ok, mprotect ok, personality ok, exec No such file or directory, dlopen ok thread 7")
}

// A module whose init leaves memory it could still turn into other code
// (here writable and executable) keeps the app from starting; so does one
// that asks for an executable stack, which sealing would leave no thread.
func TestNativeUnsealable(t *testing.T) {
	bin := binary(t)
	for _, c := range []struct {
		name, def, want string
	}{
		{"writable code", "PROBE_RWX_INIT", "a native module left code that could still change"},
		{"executable stack", "-Wl,-z,execstack", "asks for an executable stack"},
	} {
		dir := nativeApp(t, nativetest.Build(t, c.def), "probe")
		if err := os.WriteFile(filepath.Join(dir, DefaultMain), []byte(`import { add } from "native:probe";
import * as kario from "kario";
kario.on("start", () => kario.reset({ id: "main", title: String(add(1, 2)), body: [] }));
`), 0o644); err != nil {
			t.Fatal(err)
		}
		s := launch(t, t.TempDir(), "en", bin, filepath.Join(dir, DefaultMain))
		if code := s.exit(); code != 1 || !strings.Contains(s.log(), c.want) {
			t.Errorf("%s: exit %d; log:\n%s", c.name, code, s.log())
		}
	}
}
