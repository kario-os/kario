package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	sdkts "github.com/kario-os/kario/sdk/ts"
)

// The project kario new writes; gitignore becomes .gitignore (embed leaves
// dot files out).
//
//go:embed template
var template embed.FS

var notIDChars = regexp.MustCompile(`[^a-z0-9_]+`)

func newProject(dir, id, name string) error {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is not empty", dir)
	}
	base := filepath.Base(filepath.Clean(dir))
	if name == "" {
		name = base
	}
	if id == "" {
		seg := strings.Trim(notIDChars.ReplaceAllString(strings.ToLower(base), "_"), "_")
		if seg == "" || seg[0] < 'a' || seg[0] > 'z' {
			seg = "app" + seg
		}
		id = "com.example." + seg
	}
	if !appID.MatchString(id) {
		return fmt.Errorf("id %q: a reverse domain name like com.example.notes", id)
	}
	quoted, _ := json.Marshal(name)
	err := fs.WalkDir(template, "template", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := template.ReadFile(p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "template/")
		if rel == "gitignore" {
			rel = ".gitignore"
		}
		if rel == "manifest.json" {
			raw = []byte(strings.NewReplacer(`"APP_ID"`, `"`+id+`"`, `"APP_NAME"`, string(quoted)).Replace(string(raw)))
		}
		return write(filepath.Join(dir, filepath.FromSlash(rel)), raw)
	})
	if err != nil {
		return err
	}
	// The SDK's types and libraries, so editors check the code without a
	// package manager (kario build bundles its own copy of the libraries).
	for src, dst := range map[string]string{
		"index.d.ts": "kario/index.d.ts", "ui.d.ts": "kario/ui.d.ts", "package.json": "kario/package.json",
		"karioui/index.ts": "karioui/index.ts", "kariotest/index.ts": "kariotest/index.ts",
	} {
		raw, err := sdkts.Files.ReadFile(src)
		if err != nil {
			return err
		}
		if err := write(filepath.Join(dir, "types", filepath.FromSlash(dst)), raw); err != nil {
			return err
		}
	}
	fmt.Printf("%s: %s (%s); kario build %s\n", dir, name, id, dir)
	return nil
}

func write(p string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, raw, 0o644)
}
