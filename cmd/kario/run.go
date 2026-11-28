package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	sdkts "github.com/kario-os/kario/sdk/ts"
	"github.com/kario-os/kario/subproc"
)

type runOptions struct {
	engine, inspect, device, preview, gui, shell, node, lang, native string
	brk, watch                                                       bool
	// sku is the SKU to simulate; skuFile the sku.json written for it.
	sku, skuFile string
	// profile is where -profile writes the timeline (profile.go).
	profile string
}

// snapshot is what -watch compares: the project's sources, catalogs,
// manifest and native modules, by name, size and time.
func snapshot(dir string) string {
	var b strings.Builder
	for _, top := range []string{"src", "locale", "manifest.json", "native"} {
		_ = filepath.WalkDir(filepath.Join(dir, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if st, err := d.Info(); err == nil {
				fmt.Fprintf(&b, "%s %d %d\n", p, st.Size(), st.ModTime().UnixNano())
			}
			return nil
		})
	}
	return b.String()
}

// watch builds the app again (as bo says) whenever its sources change,
// then calls reload; a build that fails is reported and the running app
// left as it is. Editors write in bursts: a change is built once it has
// settled.
func watch(dir string, bo buildOptions, every time.Duration, reload func(), stop <-chan struct{}) {
	last := snapshot(dir)
	manifest, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		now := snapshot(dir)
		if now == last {
			continue
		}
		for settled := false; !settled; {
			time.Sleep(every)
			next := snapshot(dir)
			settled, now = next == now, next
		}
		last = now
		r, err := build(dir, bo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kario run: the build failed, the app runs on unchanged:\n%v\n", err)
			continue
		}
		fmt.Fprintf(os.Stderr, "kario run: built again (main.js %s), restarting the app\n", kb(r.main))
		if m, _ := os.ReadFile(filepath.Join(dir, "manifest.json")); string(m) != string(manifest) {
			manifest = m
			fmt.Fprintln(os.Stderr, "kario run: manifest.json changed: the Shell keeps the one it started with until kario run starts again")
		}
		reload()
	}
}

// findShell is phone-shell: the flag's, $KARIO_SHELL, make build's layout
// (build/bin/kario, build/usr/bin/phone-shell), beside this program, or on
// PATH.
func findShell(flagPath string) (string, error) {
	exe, _ := os.Executable()
	return lookShell(flagPath, exe)
}

// lookShell is findShell for kario at exe. The staged Shell comes before
// the one beside kario: go build leaves a phone-shell in build/bin too,
// whose prefix (build/) has neither the system's apps nor a renderer.
func lookShell(flagPath, exe string) (string, error) {
	cands := []string{flagPath, os.Getenv("KARIO_SHELL")}
	if exe != "" {
		d := filepath.Dir(exe)
		cands = append(cands, filepath.Join(d, "..", "usr", "bin", "phone-shell"), filepath.Join(d, "phone-shell"))
	}
	for i, c := range cands {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
		if i == 0 {
			return "", fmt.Errorf("-shell %s: no such program", c)
		}
	}
	if p, err := exec.LookPath("phone-shell"); err == nil {
		return p, nil
	}
	return "", errors.New("no phone-shell (make build in kario, then -shell build/usr/bin/phone-shell or $KARIO_SHELL)")
}

// findRenderer is the pixel renderer for -preview: the flag's,
// $KARIO_RENDERER, the shell's renderer slot (lib/phone/renderer), or
// phone-paint built in the kario tree the shell was built in.
func findRenderer(flagPath, shell string) (string, error) {
	prefix := filepath.Dir(filepath.Dir(shell))
	for _, c := range []string{flagPath, os.Getenv("KARIO_RENDERER"), filepath.Join(prefix, "lib", "phone", "renderer"),
		filepath.Join(prefix, "..", "..", "paint", "target", "release", "phone-paint")} {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("-preview needs a pixel renderer (make paint in kario, or -gui PATH)")
}

// nodeRunner is how the Shell starts the app under Node.js: the runtime's
// run.mjs, with the inspector when asked and V8's CPU profiler when
// profiling (the Shell adds main.js).
func nodeRunner(node, rt string, o runOptions) []string {
	argv := slices.Concat([]string{node}, sdkts.NodeFlags, []string{"--enable-source-maps"})
	switch {
	case o.inspect != "" && o.brk:
		argv = append(argv, "--inspect-brk="+o.inspect)
	case o.inspect != "":
		argv = append(argv, "--inspect="+o.inspect)
	}
	if o.profile != "" {
		argv = append(argv, "--cpu-prof", "--cpu-prof-dir="+cpuProfileDir(o.profile))
	}
	return append(argv, filepath.Join(rt, "run.mjs"))
}

// shellArgs are phone-shell's arguments: the app as the developer's
// (--dev-app, opened at start), under the runner when there is one, and
// the browser preview when asked.
func shellArgs(app string, runner []string, renderer string, o runOptions) []string {
	args := []string{"--dev-app", app}
	if runner != nil {
		raw, _ := json.Marshal(runner)
		args = append(args, "--dev-runner", string(raw))
	}
	if o.skuFile != "" {
		args = append(args, "--sku", o.skuFile)
	}
	if o.device != "" {
		args = append(args, "--device", o.device)
	}
	if o.preview != "" {
		args = append(args, "--gui", renderer, "--preview", o.preview)
	}
	if o.profile != "" {
		args = append(args, "--profile", rawTrace(o.profile))
	}
	return args
}

// nativeProgram is the program of an app written in C, C++, Rust or Go
// (22-developer-api.md §9): a manifest with "exec" and no "runtime". Its
// developer builds it with their own tools, beside manifest.json, where the
// installer wants it too; kario run starts it as it is. A TypeScript
// project has none.
func nativeProgram(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return "", err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("manifest.json: %w", err)
	}
	if m.Runtime != "" || m.Exec == "" {
		return "", nil
	}
	switch {
	case !appID.MatchString(m.ID) || strings.HasPrefix(m.ID, "system."):
		return "", fmt.Errorf("manifest.json: id %q: a reverse domain name like com.example.notes", m.ID)
	case strings.TrimSpace(m.Name) == "":
		return "", errors.New("manifest.json: the app needs a name")
	case filepath.Base(m.Exec) != m.Exec || m.Exec == "." || m.Exec == "..":
		return "", fmt.Errorf("manifest.json: exec %q: a program beside manifest.json", m.Exec)
	}
	p, err := filepath.Abs(filepath.Join(dir, m.Exec))
	if err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	switch {
	case err != nil:
		return "", fmt.Errorf("no %s: build the app's program first (exec %q in manifest.json)", p, m.Exec)
	case !st.Mode().IsRegular() || st.Mode()&0o111 == 0:
		return "", fmt.Errorf("%s is not an executable program (exec %q in manifest.json)", p, m.Exec)
	}
	return p, nil
}

// runApp builds the app (with a source map) and runs it in the developer
// machine's Shell: under Node.js (the debugger can attach) or kario-jsrt,
// as on the device. An app written in C, C++, Rust or Go is not built: its
// program runs as the Shell starts its own apps.
func runApp(dir string, o runOptions) error {
	if o.engine != "node" && o.engine != "jsrt" {
		return fmt.Errorf("-engine %q: node or jsrt", o.engine)
	}
	if o.watch && o.preview == "" {
		return errors.New("-watch needs -preview: without it the Shell's text UI owns the terminal")
	}
	prog, err := nativeProgram(dir)
	if err != nil {
		return err
	}
	if prog != "" {
		switch {
		case o.engine != "node":
			return fmt.Errorf("-engine: %s is a program of its own, not a TypeScript app", filepath.Base(prog))
		case o.inspect != "":
			return fmt.Errorf("-inspect: attach your own debugger to %s (gdb -p, dlv attach)", filepath.Base(prog))
		case o.watch:
			return fmt.Errorf("-watch: build %s with its own tools, then kario run again", filepath.Base(prog))
		case o.native != "":
			return errors.New("-native: native modules are a TypeScript app's")
		}
		o.engine = "native"
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	if o.sku != "" {
		s, err := skuByID(o.sku)
		if err != nil {
			return err
		}
		if o.device != "" && o.device != s.info.Device {
			return fmt.Errorf("-device %s and -sku %s (a %s): give one", o.device, s.ID, s.info.Device)
		}
		if o.skuFile, err = writeSKU(s, filepath.Join(cache, "kario", "sku")); err != nil {
			return err
		}
	}
	shell, err := findShell(o.shell)
	if err != nil {
		return err
	}
	renderer := ""
	if o.preview != "" {
		if renderer, err = findRenderer(o.gui, shell); err != nil {
			return err
		}
	}
	out, err := filepath.Abs(filepath.Join(dir, "build"))
	if prog != "" {
		out, err = filepath.Abs(dir)
	}
	if err != nil {
		return err
	}
	if o.profile != "" {
		if o.profile, err = filepath.Abs(o.profile); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(o.profile), 0o755); err != nil {
			return err
		}
	}
	bo := buildOptions{out: out, sourcemap: true, quiet: true, native: o.native}
	if prog == "" {
		r, err := build(dir, bo)
		if err != nil {
			return err
		}
		for _, n := range r.notes {
			fmt.Fprintln(os.Stderr, "kario run:", n)
		}
	}
	var runner []string
	if o.engine == "node" {
		node, err := findNode(o.node)
		if err != nil {
			return err
		}
		rt := filepath.Join(cache, "kario", "node")
		if err := writeNode(rt); err != nil {
			return err
		}
		if err := nativeAddon(out, rt); err != nil {
			return err
		}
		runner = nodeRunner(node, rt, o)
	}
	cmd := exec.Command(shell, shellArgs(out, runner, renderer, o)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if o.lang != "" {
		cmd.Env = append(cmd.Env, "PHONE_LANG="+o.lang)
	}
	if o.inspect != "" && o.engine == "node" {
		fmt.Fprintf(os.Stderr, "kario run: %s under Node.js, inspector on %s (the app's log: the Shell's run directory)\n", out, o.inspect)
	}
	// Stopping kario stops the Shell, which ends the app. Profiling, the
	// Shell is asked to stop (SIGTERM) and ends the app in turn, which then
	// writes its CPU profile: with the preview it is kept out of the
	// terminal's process group, where Ctrl-C would kill it at once.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	// The Shell ends with kario however kario ends: killed (by hand, or a
	// test killed while it ran), kario left the Shell running, the
	// preview's port taken.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM, Setpgid: o.profile != "" && o.preview != ""}
	start := time.Now()
	if err := subproc.Start(cmd); err != nil {
		return err
	}
	var stopped atomic.Bool
	go func() {
		for s := range sig {
			if o.profile != "" {
				s = syscall.SIGTERM
			}
			stopped.Store(true)
			_ = cmd.Process.Signal(s)
		}
	}()
	if o.watch {
		stop := make(chan struct{})
		defer close(stop)
		go watch(dir, bo, 300*time.Millisecond, func() { _ = cmd.Process.Signal(syscall.SIGUSR2) }, stop)
	}
	err = cmd.Wait()
	if o.profile != "" {
		var m manifest
		if raw, err := os.ReadFile(filepath.Join(out, "manifest.json")); err == nil {
			_ = json.Unmarshal(raw, &m)
		}
		if perr := reportProfile(o, start, m.ID); perr != nil {
			return perr
		}
	}
	if stopped.Load() {
		return nil
	}
	return err
}
