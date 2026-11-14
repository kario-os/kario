package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
)

type packageOptions struct {
	out, key, cert string
	devStore, name string
}

// signer is the developer who signs: a key and its store certificate, the
// files phone-pkg keygen and register write, or a development store's
// (pkg.DevStore, trusted by phones in developer mode).
func signer(o packageOptions) (pkg.Developer, error) {
	if o.devStore != "" {
		store, err := pkg.DevStore(o.devStore, filepath.Join(o.devStore, "trust"))
		if err != nil {
			return pkg.Developer{}, err
		}
		return pkg.DevDeveloper(filepath.Join(o.devStore, "developers"), o.name, store)
	}
	raw, err := os.ReadFile(o.key)
	if err != nil {
		return pkg.Developer{}, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return pkg.Developer{}, fmt.Errorf("%s is not a phone-pkg key", o.key)
	}
	var c pkg.DevCert
	if raw, err = os.ReadFile(o.cert); err == nil {
		err = json.Unmarshal(raw, &c)
	}
	if err != nil {
		return pkg.Developer{}, fmt.Errorf("%s: %w (phone-pkg register writes it)", o.cert, err)
	}
	return pkg.Developer{Key: ed25519.NewKeyFromSeed(seed), Cert: c}, nil
}

// packageApp signs an app as a .phoneapp: a project (src/main.ts) is built
// first, without bytecode; an app directory kario build wrote goes as it is.
func packageApp(dir string, o packageOptions) (int, error) {
	app := dir
	if _, err := os.Stat(filepath.Join(dir, "src", "main.ts")); err == nil {
		tmp, err := os.MkdirTemp("", "kario-package-")
		if err != nil {
			return 0, err
		}
		defer os.RemoveAll(tmp)
		app = filepath.Join(tmp, "app")
		if _, err := build(dir, buildOptions{out: app, minify: true, quiet: true}); err != nil {
			return 0, err
		}
	}
	if _, err := os.Stat(filepath.Join(app, jsrt.Bytecode(jsrt.DefaultMain))); err == nil {
		return 0, fmt.Errorf("%s has bytecode, which is for the system image: build it without -bytecode", app)
	}
	if err := checkFiles(app, false); err != nil {
		return 0, err
	}
	dev, err := signer(o)
	if err != nil {
		return 0, err
	}
	raw, err := pkg.Build(app, dev)
	if err != nil {
		return 0, err
	}
	return len(raw), os.WriteFile(o.out, raw, 0o644)
}
