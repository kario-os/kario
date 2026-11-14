// tsgen writes the files generated from the platform's Go sources, so the
// TypeScript side has one source: the types of @kario/sdk (sdk/ts/ui.d.ts)
// from package ui, and kario-jsrt's language table (jsrt/src/languages.h)
// from package i18n. make test fails when they are stale (tsgen_test.go).
//
//	go run ./tools/tsgen          rewrite them
//	go run ./tools/tsgen -check   only report the stale ones
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	check := flag.Bool("check", false, "report stale files instead of writing them")
	flag.Parse()
	root, err := repoRoot()
	if err != nil {
		fail(err)
	}
	files, err := generate(root)
	if err != nil {
		fail(err)
	}
	stale := 0
	for _, f := range files {
		path := filepath.Join(root, f.path)
		old, _ := os.ReadFile(path)
		if bytes.Equal(old, f.data) {
			continue
		}
		if *check {
			fmt.Fprintf(os.Stderr, "tsgen: %s is stale\n", f.path)
			stale++
			continue
		}
		if err := os.WriteFile(path, f.data, 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("tsgen: wrote %s\n", f.path)
	}
	if stale > 0 {
		os.Exit(1)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "tsgen:", err)
	os.Exit(1)
}

// repoRoot is the directory of kario's go.mod at or above the working one.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if raw, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && bytes.HasPrefix(raw, []byte("module github.com/kario-os/kario\n")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not inside the kario repository")
		}
		dir = parent
	}
}
