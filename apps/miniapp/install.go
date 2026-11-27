package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/pkg"
)

// Signed mini-programs: besides the built-in ones, the host runs programs
// its vendor's mini-program store signed (the .phoneapp format of fw/pkg:
// the developer's signature over every file, the developer certificate
// issued by the store). A program is verified when it is added and again
// every time the host starts; one whose files changed is not loaded.
//
// Trusted stores: trust/ in the host's code (the vendor's root, shipped with
// the host) and trust/ in its data directory, where only the host and the
// system can write (the system puts development roots there).

func (h *host) roots() pkg.Roots {
	code, _ := os.Getwd()
	return pkg.LoadRoots(filepath.Join(code, "trust"), filepath.Join(h.app.DataDir, "trust"))
}

func (h *host) installedDir() string { return filepath.Join(h.app.DataDir, "mp") }

// loadInstalled adds the installed programs whose files still verify.
func (h *host) loadInstalled() {
	h.rejected = nil
	entries, _ := os.ReadDir(h.installedDir())
	roots := h.roots()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(h.installedDir(), e.Name())
		if err := pkg.VerifyDir(dir, roots); err != nil {
			log.Printf("mini-program %s not loaded: %v", e.Name(), err)
			h.rejected = append(h.rejected, e.Name())
			continue
		}
		p, err := readProgram(dir, e.Name())
		if err != nil {
			log.Printf("mini-program %s: %v", e.Name(), err)
			h.rejected = append(h.rejected, e.Name())
			continue
		}
		h.progs = append(h.progs, p)
	}
}

func readProgram(dir, id string) (*program, error) {
	p, err := readProgramFS(os.DirFS(dir), id)
	if err != nil {
		return nil, err
	}
	p.dir = dir
	return p, nil
}

// readProgramFS reads a program's app.json, code and catalog.
func readProgramFS(files fs.FS, id string) (*program, error) {
	raw, err := fs.ReadFile(files, "app.json")
	if err != nil {
		return nil, err
	}
	var m meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m.ID = id
	if err := m.check(); err != nil {
		return nil, err
	}
	src, err := loadSourceFS(files, id)
	if err != nil {
		return nil, err
	}
	msgs, err := readCatalog(files, i18n.Lang())
	if err != nil {
		return nil, err
	}
	return &program{meta: m, source: src, msgs: msgs}, nil
}

// addDownloaded installs every package in downloads/*.mpk that verifies and
// says, one line each, what happened.
func (h *host) addDownloaded() []string {
	paths, _ := filepath.Glob(filepath.Join(h.app.DataDir, "downloads", "*.mpk"))
	if len(paths) == 0 {
		return []string{tr.T("No mini-programs to add (prototype: mini-program packages go in downloads/)")}
	}
	var out []string
	added := map[string]bool{}
	for _, path := range paths {
		id, name, err := h.install(path)
		if err != nil {
			log.Printf("%s: %v", filepath.Base(path), err)
			out = append(out, tr.T("%s failed verification", filepath.Base(path)))
			continue
		}
		added[id] = true
		out = append(out, tr.T("Added \"%s\"", name))
	}
	old := h.progs
	h.progs = nil
	if err := h.load(); err != nil {
		log.Print(err)
	}
	h.keepRunning(old, added)
	return out
}

// keepRunning puts the programs of before a reload whose runtimes run on
// (warm) back into h.progs, where their timers, trimWarm and stopAll find
// them; one added again, or no longer loaded, ends.
func (h *host) keepRunning(old []*program, added map[string]bool) {
	for _, o := range old {
		if o.run == nil {
			continue
		}
		i := slices.IndexFunc(h.progs, func(p *program) bool { return p.meta.ID == o.meta.ID })
		if i < 0 || added[o.meta.ID] {
			h.end(o)
			continue
		}
		h.progs[i] = o
	}
}

func (h *host) install(path string) (id, name string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	p, err := pkg.Open(raw, h.roots())
	if err != nil {
		return "", "", err
	}
	if _, err := programsFS.Open("programs/" + p.ID); err == nil {
		return "", "", fmt.Errorf("%s is a built-in mini-program", p.ID)
	}
	var m meta
	if err := json.Unmarshal(p.Data["app.json"], &m); err != nil || m.Name == "" {
		return "", "", errors.New("app.json with a name is required")
	}
	if err := m.check(); err != nil {
		return "", "", fmt.Errorf("app.json: %w", err)
	}
	final := filepath.Join(h.installedDir(), p.ID)
	if err := os.MkdirAll(h.installedDir(), 0o700); err != nil {
		return "", "", err
	}
	tmp := final + ".new"
	_ = os.RemoveAll(tmp)
	if err := p.Extract(tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return "", "", err
	}
	_ = os.RemoveAll(final)
	name, _ = m.text()
	return p.ID, name, os.Rename(tmp, final)
}
