package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/pkg"
)

type packageOptions struct {
	out, key, cert string
	devStore, name string
	native         string
	// arches: the architectures whose native modules go in, each needed
	// (comma-separated); "" every one they are built for. The store sends
	// each phone its own only.
	arches string
	// symbols is where the symbol file goes (jsrt.SymbolFile; "" beside
	// the package, "-" nowhere); nativeSymbols the unstripped native
	// modules, <arch>/<name>.so, it carries for those packaged.
	symbols, nativeSymbols string
	// debug builds main.js unminified, for the device's debugger, and
	// mapFile keeps its source map there.
	debug   bool
	mapFile string
}

// symbolsPath is the symbol file beside a package (app.phonesym).
func symbolsPath(out string) string {
	return strings.TrimSuffix(out, ".phoneapp") + jsrt.SymbolsExt
}

// packageArches is -arch as a list (nil for all), with the phone's in it.
func packageArches(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	var out []string
	for _, a := range strings.Split(s, ",") {
		if a = strings.TrimSpace(a); !slices.Contains(jsrt.Arches, a) {
			return nil, fmt.Errorf("-arch: %q is not an architecture modules are built for (%s)", a, strings.Join(jsrt.Arches, ", "))
		}
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	if !slices.Contains(out, jsrt.DeviceArch) {
		return nil, fmt.Errorf("-arch: the package needs %s, the phone's", jsrt.DeviceArch)
	}
	return out, nil
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
// first, without bytecode; an app directory kario build wrote goes as it is,
// but for its source map. Its native modules have to be built for the phone
// (arm64) and every architecture of -arch; a package may carry several. The
// module's source map goes into the symbol file beside the package, never
// into the package.
func packageApp(dir string, o packageOptions) (int, error) {
	arches, err := packageArches(o.arches)
	if err != nil {
		return 0, err
	}
	tmp, err := os.MkdirTemp("", "kario-package-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)
	app, mapFile := dir, filepath.Join(tmp, jsrt.DefaultMain+".map")
	if _, err := os.Stat(filepath.Join(dir, "src", "main.ts")); err == nil {
		app = filepath.Join(tmp, "app")
		if _, err := build(dir, buildOptions{out: app, minify: !o.debug, quiet: true, native: o.native, mapFile: mapFile}); err != nil {
			return 0, err
		}
	} else if _, err := os.Stat(filepath.Join(dir, jsrt.DefaultMain+".map")); err == nil {
		app = filepath.Join(tmp, "app")
		if err := copyApp(dir, app, mapFile); err != nil {
			return 0, err
		}
	}
	if _, err := os.Stat(filepath.Join(app, jsrt.Bytecode(jsrt.DefaultMain))); err == nil {
		return 0, fmt.Errorf("%s has bytecode, which is for the system image: build it without -bytecode", app)
	}
	need := arches
	if need == nil {
		need = []string{jsrt.DeviceArch}
	}
	if err := checkFiles(app, false, need...); err != nil {
		return 0, err
	}
	dev, err := signer(o)
	if err != nil {
		return 0, err
	}
	raw, err := pkg.BuildArches(app, dev, arches)
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(o.out, raw, 0o644); err != nil {
		return 0, err
	}
	srcMap, err := os.ReadFile(mapFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	if o.mapFile != "" && srcMap != nil {
		if err := os.WriteFile(o.mapFile, srcMap, 0o644); err != nil {
			return 0, err
		}
	}
	if o.symbols == "-" {
		return len(raw), nil
	}
	if o.symbols == "" {
		o.symbols = symbolsPath(o.out)
	}
	sym, err := symbolFile(app, raw, srcMap, o.nativeSymbols)
	if err != nil || sym == nil {
		return len(raw), err
	}
	out, err := sym.Marshal()
	if err != nil {
		return 0, err
	}
	return len(raw), os.WriteFile(o.symbols, out, 0o644)
}

// symbolFile is the package raw's symbol file: the map of its module and
// the unstripped native modules in nativeSymbols ("" none), each the
// build of the one packaged (the same build-id); nil for an app with
// neither (a program, or a module without a map).
func symbolFile(app string, raw, srcMap []byte, nativeSymbols string) (*jsrt.SymbolFile, error) {
	var m struct {
		ID      string `json:"id"`
		Version string `json:"version"`
		Runtime string `json:"runtime"`
		Main    string `json:"main"`
	}
	head, err := os.ReadFile(filepath.Join(app, "manifest.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(head, &m); err != nil {
		return nil, err
	}
	if m.Runtime != jsrt.Runtime {
		return nil, nil
	}
	main := jsrt.Main(m.Main)
	code, err := os.ReadFile(filepath.Join(app, main))
	if err != nil {
		return nil, err
	}
	s := &jsrt.SymbolFile{App: m.ID, Version: m.Version, PackageSHA256: jsrt.SHA256(raw), Main: main, MainSHA256: jsrt.SHA256(code), Map: srcMap, Module: code}
	if nativeSymbols != "" {
		libs, _ := filepath.Glob(filepath.Join(app, jsrt.NativeDir, "*", "*"+jsrt.NativeExt))
		for _, lib := range libs {
			rel, _ := filepath.Rel(app, lib)
			arch, name := filepath.Base(filepath.Dir(lib)), filepath.Base(lib)
			full, err := os.ReadFile(filepath.Join(nativeSymbols, arch, name))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			packaged, _ := os.ReadFile(lib)
			a, err1 := jsrt.LoadSymbols(name, packaged)
			b, err2 := jsrt.LoadSymbols(name, full)
			if err := errors.Join(err1, err2); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if a.BuildID == "" || a.BuildID != b.BuildID {
				return nil, fmt.Errorf("-native-symbols %s: not the build packaged as %s (build-id %q, packaged %q)", filepath.Join(nativeSymbols, arch, name), filepath.ToSlash(rel), b.BuildID, a.BuildID)
			}
			if s.Libs == nil {
				s.Libs = map[string][]byte{}
			}
			s.Libs[filepath.ToSlash(rel)] = full
		}
	}
	if s.Map == nil && s.Libs == nil {
		return nil, nil
	}
	return s, nil
}

// copyApp copies an app directory but for its source map, which goes to
// mapFile.
func copyApp(src, dst, mapFile string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if rel == jsrt.DefaultMain+".map" {
			return os.WriteFile(mapFile, raw, 0o644)
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), raw, st.Mode().Perm())
	})
}
