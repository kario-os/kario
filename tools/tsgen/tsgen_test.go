package main

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kario-os/kario/ui"
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
		"export type PlatformLevel = " + strconv.Itoa(ui.PlatformLevel) + ";",
		`  "tel.dial": ` + strconv.Itoa(ui.CapLevels[ui.CapTelDial]) + ";",
		`  | "media.list@2"`,
		`  "media.list@2": MediaListArgs | undefined;`,
		`  "media.list@2": MediaPage;`,
		`  "media.list@2": 2;`,
		"  kind?: MediaKind;",
		`  /** Superseded by "media.list@2" (platform level 2); served through level 4 at least (22-developer-api.md §2.3). */` + "\n" + `  "media.list": MediaItem[];`,
	} {
		if !strings.Contains(string(ts), want) {
			t.Errorf("ui.d.ts lacks %q", want)
		}
	}
	if strings.Contains(string(ts), `"media:"`) {
		t.Error("MediaPrefix taken for a media kind")
	}
	if strings.Contains(string(ts), `"`+ui.ErrNoService+`"`) {
		t.Error("ErrNoService, said in the user's language, taken for an error text")
	}
}
