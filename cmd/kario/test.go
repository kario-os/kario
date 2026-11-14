package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	sdkts "github.com/kario-os/kario/sdk/ts"
)

type testOptions struct {
	run, node, lang string
	inspect         bool
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
		External:      []string{"kario", "node:*"},
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

// findNode is Node.js: the flag's, $KARIO_NODE, or node on PATH.
func findNode(flagPath string) (string, error) {
	for _, c := range []string{flagPath, os.Getenv("KARIO_NODE")} {
		if c != "" {
			return c, nil
		}
	}
	if p, err := exec.LookPath("node"); err == nil {
		return p, nil
	}
	return "", errors.New("no node on PATH (Node.js 22.15 or newer runs apps on the developer machine; -node PATH)")
}

// testArgs are node's arguments for the tests: the runner first, every
// file in one process (the runner's hooks hold for all of them).
func testArgs(rt string, files []string, o testOptions) []string {
	args := []string{"--enable-source-maps", "--import", filepath.Join(rt, "test.mjs"), "--test", "--test-isolation=none", "--test-reporter=spec"}
	if o.run != "" {
		args = append(args, "--test-name-pattern="+o.run)
	}
	if o.inspect {
		args = append(args, "--inspect-brk")
	}
	return append(args, files...)
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
	if _, err := build(dir, buildOptions{out: app, inline: true, quiet: true}); err != nil {
		return err
	}
	var bundles []string
	for _, f := range files {
		out := filepath.Join(tmp, "test", strings.TrimSuffix(strings.TrimSuffix(filepath.Base(f), ".ts"), ".js")+".mjs")
		if err := bundleTest(dir, f, out); err != nil {
			return err
		}
		bundles = append(bundles, out)
	}
	rt := filepath.Join(tmp, "node")
	if err := writeNode(rt); err != nil {
		return err
	}
	cmd := exec.Command(node, testArgs(rt, bundles, o)...)
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
