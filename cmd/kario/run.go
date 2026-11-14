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
	"strings"
	"syscall"
	"time"
)

type runOptions struct {
	engine, inspect, device, preview, gui, shell, node, lang string
	brk, watch                                               bool
}

// snapshot is what -watch compares: the project's sources, catalogs and
// manifest, by name, size and time.
func snapshot(dir string) string {
	var b strings.Builder
	for _, top := range []string{"src", "locale", "manifest.json"} {
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

// watch builds the app again into out whenever its sources change, then
// calls reload; a build that fails is reported and the running app left as
// it is. Editors write in bursts: a change is built once it has settled.
func watch(dir, out string, every time.Duration, reload func(), stop <-chan struct{}) {
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
		r, err := build(dir, buildOptions{out: out, sourcemap: true, quiet: true})
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

// findShell is phone-shell: the flag's, $KARIO_SHELL, beside this program,
// make build's layout (build/bin/kario, build/usr/bin/phone-shell), or on
// PATH.
func findShell(flagPath string) (string, error) {
	cands := []string{flagPath, os.Getenv("KARIO_SHELL")}
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		cands = append(cands, filepath.Join(d, "phone-shell"), filepath.Join(d, "..", "usr", "bin", "phone-shell"))
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
// run.mjs, with the inspector when asked (the Shell adds main.js).
func nodeRunner(node, rt string, o runOptions) []string {
	argv := []string{node, "--enable-source-maps"}
	switch {
	case o.inspect != "" && o.brk:
		argv = append(argv, "--inspect-brk="+o.inspect)
	case o.inspect != "":
		argv = append(argv, "--inspect="+o.inspect)
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
	if o.device != "" {
		args = append(args, "--device", o.device)
	}
	if o.preview != "" {
		args = append(args, "--gui", renderer, "--preview", o.preview)
	}
	return args
}

// runApp builds the app (with a source map) and runs it in the developer
// machine's Shell: under Node.js (the debugger can attach) or kario-jsrt,
// as on the device.
func runApp(dir string, o runOptions) error {
	if o.engine != "node" && o.engine != "jsrt" {
		return fmt.Errorf("-engine %q: node or jsrt", o.engine)
	}
	if o.watch && o.preview == "" {
		return errors.New("-watch needs -preview: without it the Shell's text UI owns the terminal")
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
	out := filepath.Join(dir, "build")
	if _, err := build(dir, buildOptions{out: out, sourcemap: true, quiet: true}); err != nil {
		return err
	}
	if out, err = filepath.Abs(out); err != nil {
		return err
	}
	var runner []string
	if o.engine == "node" {
		node, err := findNode(o.node)
		if err != nil {
			return err
		}
		cache, err := os.UserCacheDir()
		if err != nil {
			cache = os.TempDir()
		}
		rt := filepath.Join(cache, "kario", "node")
		if err := writeNode(rt); err != nil {
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
	// Stopping kario stops the Shell, which ends the app.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		for s := range sig {
			_ = cmd.Process.Signal(s)
		}
	}()
	if o.watch {
		stop := make(chan struct{})
		defer close(stop)
		go watch(dir, out, 300*time.Millisecond, func() { _ = cmd.Process.Signal(syscall.SIGUSR2) }, stop)
	}
	return cmd.Wait()
}
