package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// The generated files match the Go sources: a change to package ui or to
// the languages fails here until go run ./tools/tsgen writes them again.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	files, err := stale(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) > 0 {
		t.Fatalf("stale: %s; run go run ./tools/tsgen", strings.Join(files, ", "))
	}
}

func TestTypes(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	ts, err := typescript(filepath.Join(root, "ui"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`export type Language = "zh-Hans" | "zh-Hant" | "en" | "es";`,
		"export interface Screen {",
		"  kind: NodeKind;",
		"  actions?: Action[];",
		"  canvas?: Canvas;",
		"  mic?: boolean;",
		"  fields?: Record<string, string>;",
		`  "clipboard.set": ClipArgs;`,
		`  "location.get": Location;`,
		`  | "media.pick"`,
		`  | "cancelled"`,
	} {
		if !strings.Contains(string(ts), want) {
			t.Errorf("ui.d.ts lacks %q", want)
		}
	}
	if strings.Contains(string(ts), `"media:"`) {
		t.Error("MediaPrefix taken for a media kind")
	}
}
