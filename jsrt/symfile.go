package jsrt

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// A symbol file (app.phonesym) is what kario package writes beside a
// package and never into it, so phones do not get it: the source map of
// the package's module, the module itself (minified, as in the package: a
// frame's function gets its name in the source by it) and, optionally, its
// native modules unstripped. The store keeps it for the app's developer and
// the reviewers, checked against the package it came with, and symbolizes
// the crash reports it receives with it. It is a zip of SymbolsManifest
// (symbols.json), the map (main.js.map), the module (main.js) and
// lib/<arch>/<name>.so.
const (
	SymbolsExt      = ".phonesym"
	SymbolsManifest = "symbols.json"
	// MaxSymbols bounds a symbol file.
	MaxSymbols = 128 << 20
)

// SymbolFile is a symbol file's content.
type SymbolFile struct {
	App     string `json:"app"`
	Version string `json:"version"`
	// PackageSHA256 is the package's digest, MainSHA256 its module's.
	PackageSHA256 string `json:"package_sha256"`
	Main          string `json:"main"`
	MainSHA256    string `json:"main_sha256"`
	// Map is the module's source map, Module the module (nil in symbol
	// files written before it was); Libs the unstripped native modules
	// by their path in the package (lib/<arch>/<name>.so).
	Map    []byte            `json:"-"`
	Module []byte            `json:"-"`
	Libs   map[string][]byte `json:"-"`
}

// SHA256 is the hex digest symbol files and packages are matched by.
func SHA256(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])
}

// Marshal writes the symbol file.
func (s *SymbolFile) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	head, _ := json.MarshalIndent(s, "", "  ")
	put := func(name string, raw []byte) error {
		w, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		_, err = w.Write(raw)
		return err
	}
	if err := put(SymbolsManifest, head); err != nil {
		return nil, err
	}
	if s.Map != nil {
		if err := put(Main(s.Main)+".map", s.Map); err != nil {
			return nil, err
		}
	}
	if s.Module != nil {
		if err := put(Main(s.Main), s.Module); err != nil {
			return nil, err
		}
	}
	libs := make([]string, 0, len(s.Libs))
	for p := range s.Libs {
		libs = append(libs, p)
	}
	slices.Sort(libs)
	for _, p := range libs {
		if err := put(p, s.Libs[p]); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadSymbolFile reads a symbol file and checks its parts: a map that
// parses, libraries of native modules where packages have them.
func ReadSymbolFile(raw []byte) (*SymbolFile, error) {
	if len(raw) > MaxSymbols {
		return nil, fmt.Errorf("a symbol file larger than %d MB", MaxSymbols>>20)
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("not a symbol file: %w", err)
	}
	var s SymbolFile
	files := map[string][]byte{}
	var total int64
	for _, f := range z.File {
		if _, dup := files[f.Name]; dup {
			return nil, fmt.Errorf("%s twice in the symbol file", f.Name)
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(r, MaxSymbols-total+1))
		r.Close()
		if err != nil {
			return nil, err
		}
		if total += int64(len(body)); total > MaxSymbols {
			return nil, fmt.Errorf("a symbol file larger than %d MB unpacked", MaxSymbols>>20)
		}
		files[f.Name] = body
	}
	head, ok := files[SymbolsManifest]
	if !ok {
		return nil, errors.New("no " + SymbolsManifest + " in the symbol file")
	}
	if err := json.Unmarshal(head, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", SymbolsManifest, err)
	}
	if s.App == "" || s.Version == "" || len(s.PackageSHA256) != 64 || !validMain(Main(s.Main)) {
		return nil, errors.New(SymbolsManifest + ": needs app, version, package_sha256 and main")
	}
	for name, body := range files {
		switch {
		case name == SymbolsManifest:
		case name == Main(s.Main)+".map":
			if _, err := ParseSourceMap(body); err != nil {
				return nil, err
			}
			s.Map = body
		case name == Main(s.Main):
			if SHA256(body) != s.MainSHA256 {
				return nil, fmt.Errorf("%s is not the module %s names", name, SymbolsManifest)
			}
			s.Module = body
		case strings.HasPrefix(name, NativeDir+"/"):
			arch, _, isModule := nativeFile(name)
			if !isModule || !slices.Contains(Arches, arch) || !isELF(body) {
				return nil, fmt.Errorf("%s: lib/ holds native modules, lib/<arch>/<name>%s", name, NativeExt)
			}
			if s.Libs == nil {
				s.Libs = map[string][]byte{}
			}
			s.Libs[name] = body
		default:
			return nil, fmt.Errorf("%s: not a part of a symbol file", name)
		}
	}
	return &s, nil
}

// SourceMap is the module's map, nil without one.
func (s *SymbolFile) SourceMap() (*SourceMap, error) {
	if s.Map == nil {
		return nil, nil
	}
	return ParseSourceMap(s.Map)
}

// Symbolize maps a crash report's lines to the sources: the JavaScript
// frames in its log through the map, the native backtrace through the
// libraries (those of the symbol file, then those of more: the package's
// own, when they carry symbols). It returns the log and the backtrace as
// they read then, nil for what it could not map.
func (s *SymbolFile) Symbolize(log, backtrace []string, more map[string][]byte) (stack, native []string) {
	main := Main(s.Main)
	if m, err := s.SourceMap(); err == nil && m != nil && HasJSFrames(log, main) {
		stack = m.SymbolizeStackNamed(log, main, s.Module)
	}
	frames := ParseBacktrace(backtrace)
	if len(frames) == 0 {
		return stack, nil
	}
	var syms []*Symbols
	for _, src := range []map[string][]byte{s.Libs, more} {
		paths := make([]string, 0, len(src))
		for p := range src {
			paths = append(paths, p)
		}
		slices.Sort(paths)
		for _, p := range paths {
			if sy, err := LoadSymbols(ModuleName(p), src[p]); err == nil {
				syms = append(syms, sy)
			}
		}
	}
	if len(syms) == 0 {
		return stack, nil
	}
	return stack, Symbolize(frames, syms)
}
