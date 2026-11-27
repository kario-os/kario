package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/kario-os/kario/jsrt"
	sdkts "github.com/kario-os/kario/sdk/ts"
)

type testOptions struct {
	run, node, lang, native string
	inspect                 bool
}

// testFiles are a project's tests: test/*.test.ts (or .js).
func testFiles(dir string) ([]string, error) {
	var out []string
	for _, ext := range []string{".test.ts", ".test.js"} {
		m, err := filepath.Glob(filepath.Join(dir, "test", "*"+ext))
		if err != nil {
			return nil, err
		}
		out = append(out, m...)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("no tests in %s (test/*.test.ts)", filepath.Join(dir, "test"))
	}
	return out, nil
}

// bundleTest compiles one test file for Node.js: @kario/test and the app's
// own modules bundled in, "kario" left to the runner (sdk/ts/node/test.mjs).
func bundleTest(dir, file, out string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if file, err = filepath.Abs(file); err != nil {
		return err
	}
	res := api.Build(api.BuildOptions{
		AbsWorkingDir: abs,
		EntryPoints:   []string{file},
		Outfile:       out,
		Bundle:        true,
		Write:         true,
		Format:        api.FormatESModule,
		Target:        api.ES2023,
		Platform:      api.PlatformNode,
		External:      []string{"kario", "node:*", jsrt.NativeScheme + "*"},
		Plugins:       []api.Plugin{sdkPlugin},
		Sourcemap:     api.SourceMapInline,
		Charset:       api.CharsetUTF8,
		LogLevel:      api.LogLevelSilent,
	})
	if len(res.Errors) > 0 {
		return esbuildError(res.Errors)
	}
	return nil
}

// writeNode writes the runtime for Node.js (sdk/ts/node) into dir.
func writeNode(dir string) error {
	files, err := sdkts.Node()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, raw := range files {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// findNode is Node.js: the flag's, $KARIO_NODE, or node on PATH. An older
// one than 22.15, the first with the synchronous module hooks the runtime
// installs (module.registerHooks), is refused: it fails on their flags.
func findNode(flagPath string) (string, error) {
	p := flagPath
	if p == "" {
		p = os.Getenv("KARIO_NODE")
	}
	if p == "" {
		var err error
		if p, err = exec.LookPath("node"); err != nil {
			return "", errors.New("no node on PATH (Node.js 22.15 or newer runs apps on the developer machine; -node PATH)")
		}
	}
	out, err := exec.Command(p, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("%s --version: %v (Node.js 22.15 or newer runs apps on the developer machine; -node PATH or $KARIO_NODE)", p, err)
	}
	if v := strings.TrimSpace(string(out)); !nodeAtLeast(v, 22, 15) {
		return "", fmt.Errorf("%s is Node.js %s: apps run on Node.js 22.15 or newer (-node PATH or $KARIO_NODE)", p, v)
	}
	return p, nil
}

// nodeAtLeast says whether node --version's "v22.15.0" is major.minor or later.
func nodeAtLeast(v string, major, minor int) bool {
	var a, b int
	if _, err := fmt.Sscanf(v, "v%d.%d", &a, &b); err != nil {
		return false
	}
	return a > major || a == major && b >= minor
}

// nativeAddon builds kario-native.node beside the Node.js runtime in rt
// with this machine's C compiler ($CC, else cc): Node.js loads native
// modules through it, and the files a capability's reply brings (a picked
// photo) reach the app through its socket reader. An app that declares
// native modules needs it; for another one it is built when there is a
// compiler, and without it a reply's files do not reach the app.
func nativeAddon(appDir, rt string) error {
	m, _, err := readManifest(appDir)
	if err != nil {
		return err
	}
	cc := os.Getenv("CC")
	if cc == "" {
		cc = "cc"
	}
	p, err := exec.LookPath(cc)
	if err != nil {
		if len(m.Native) == 0 {
			return nil
		}
		return fmt.Errorf("native modules under Node.js load through %s, built with a C compiler, and there is no %s ($CC); kario run -engine jsrt runs the app on kario-jsrt instead", sdkts.Addon, cc)
	}
	if err := sdkts.BuildAddon(rt, p); err != nil && len(m.Native) > 0 {
		return err
	}
	return nil
}

// testArgs are node's arguments for the bundled test files: each file runs
// in its own process (the runner, test.mjs, set up in each), side by side,
// and node:test reports them in their names' order; a process ends with its
// file's tests, whatever its apps still have running. With -inspect one
// process runs them all, one after another (entry imports them in order),
// so that the debugger attaches once.
func testArgs(rt string, files []string, entry string, o testOptions) []string {
	args := []string{"--enable-source-maps", "--import", filepath.Join(rt, "test.mjs"), "--test", "--test-reporter=spec", "--test-force-exit"}
	if o.run != "" {
		args = append(args, "--test-name-pattern="+o.run)
	}
	if o.inspect {
		return append(args, "--inspect-brk", entry)
	}
	return append(args, files...)
}

// bundleNames name the test files' bundles: each test file's name, without
// the characters of a glob pattern (node --test reads every file it is given
// as one, and passes over a file the pattern does not match), and each
// different from the others'.
func bundleNames(files []string) []string {
	taken := map[string]bool{}
	var out []string
	for _, f := range files {
		base := strings.Map(func(r rune) rune {
			if strings.ContainsRune(`*?[]{}()!+@\`, r) {
				return '_'
			}
			return r
		}, strings.TrimSuffix(strings.TrimSuffix(filepath.Base(f), ".ts"), ".js"))
		name := base + ".mjs"
		for i := 2; taken[name]; i++ {
			name = fmt.Sprintf("%s-%d.mjs", base, i)
		}
		taken[name] = true
		out = append(out, name)
	}
	return out
}

// writeEntry writes the module that imports the test files, in order.
func writeEntry(path string, files []string) error {
	var b strings.Builder
	for _, f := range files {
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(f)}
		fmt.Fprintf(&b, "import %q;\n", u.String())
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// testApp builds the app and its tests into a temporary directory and runs
// them under Node.js against a fake Shell (@kario/test).
func testApp(dir string, o testOptions) error {
	files, err := testFiles(dir)
	if err != nil {
		return err
	}
	node, err := findNode(o.node)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "kario-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	app := filepath.Join(tmp, "app")
	if _, err := build(dir, buildOptions{out: app, inline: true, quiet: true, native: o.native}); err != nil {
		return err
	}
	var bundles []string
	for i, name := range bundleNames(files) {
		out := filepath.Join(tmp, "test", name)
		if err := bundleTest(dir, files[i], out); err != nil {
			return err
		}
		bundles = append(bundles, out)
	}
	rt := filepath.Join(tmp, "node")
	if err := writeNode(rt); err != nil {
		return err
	}
	if err := nativeAddon(app, rt); err != nil {
		return err
	}
	entry := ""
	if o.inspect {
		entry = filepath.Join(tmp, "all.test.mjs")
		if err := writeEntry(entry, bundles); err != nil {
			return err
		}
	}
	cmd := exec.Command(node, testArgs(rt, bundles, entry, o)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "KARIO_TEST_APP="+app, "KARIO_TEST_LANG="+o.lang)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return errors.New("tests failed")
		}
		return err
	}
	return nil
}
