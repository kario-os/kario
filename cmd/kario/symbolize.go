package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kario-os/kario/jsrt"
)

// symbolize maps the native backtrace of a crash (what kario-jsrt writes
// to the app's log, which the Shell keeps in the crash record and the
// store hands its developer) to functions and source lines, with the
// unstripped libraries in files: ELF files, or directories searched for
// them (a project's native/, a build tree). Input is the log text, a crash
// record, or the store's list of them; the last backtrace counts, or every
// one of a list.
func symbolize(in io.Reader, out io.Writer, files []string) error {
	return symbolizeWith(in, out, files, nil, "", nil)
}

// symbolizeWith is symbolize with the module's source map (from a map
// file or a symbol file): the JavaScript stacks in the input go to the
// TypeScript, their functions by their names there when the module is
// given too, and a symbol file's libraries join files.
func symbolizeWith(in io.Reader, out io.Writer, files []string, m *jsrt.SourceMap, main string, module []byte) error {
	raw, err := io.ReadAll(io.LimitReader(in, 16<<20))
	if err != nil {
		return err
	}
	if m != nil {
		lines := jsLines(raw)
		if jsrt.HasJSFrames(lines, main) {
			for _, l := range m.SymbolizeStackNamed(lines, main, module) {
				fmt.Fprintln(out, l)
			}
			if len(backtraces(raw)) == 0 {
				return nil
			}
			fmt.Fprintln(out)
		}
	}
	traces := backtraces(raw)
	if len(traces) == 0 {
		if m != nil {
			return errors.New("no JavaScript stack of " + main + " and no native backtrace in the input")
		}
		return errors.New("no native backtrace in the input (lines \"" + jsrt.BacktraceFrame + "<n> …\"; a JavaScript stack needs -map or -symbols)")
	}
	syms, err := loadSymbols(files)
	if err != nil {
		return err
	}
	for i, bt := range traces {
		if i > 0 {
			fmt.Fprintln(out)
		}
		if strings.HasPrefix(bt[0], jsrt.BacktraceHead) {
			fmt.Fprintln(out, bt[0])
		}
		frames := jsrt.ParseBacktrace(bt)
		for _, l := range jsrt.Symbolize(frames, syms) {
			fmt.Fprintln(out, l)
		}
		for _, f := range missing(frames, syms) {
			fmt.Fprintf(out, "(no symbols for %s: give -so the unstripped library it was built as)\n", f)
		}
	}
	return nil
}

// crashText is what a crash record or the store's crash list holds.
type crashText struct {
	Backtrace []string `json:"backtrace"`
	Log       []string `json:"log"`
}

// jsLines are the input's log lines: every crash's log of JSON, or the
// text's lines.
func jsLines(raw []byte) []string {
	var records []crashText
	var one crashText
	switch {
	case json.Unmarshal(raw, &records) == nil:
	case json.Unmarshal(raw, &one) == nil:
		records = []crashText{one}
	default:
		return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	}
	var out []string
	for i, r := range records {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, r.Log...)
	}
	return out
}

// backtraces finds the backtraces in raw: of each crash record of JSON
// (its backtrace, else the one at its log's end), or the last in plain
// text.
func backtraces(raw []byte) [][]string {
	var records []crashText
	var one crashText
	switch {
	case json.Unmarshal(raw, &records) == nil:
	case json.Unmarshal(raw, &one) == nil:
		records = []crashText{one}
	default:
		if bt := jsrt.LastBacktrace(strings.Split(string(raw), "\n")); len(jsrt.ParseBacktrace(bt)) > 0 {
			return [][]string{bt}
		}
		// Frames without the head (pasted on their own) still count.
		if lines := strings.Split(string(raw), "\n"); len(jsrt.ParseBacktrace(lines)) > 0 {
			return [][]string{lines}
		}
		return nil
	}
	var out [][]string
	for _, r := range records {
		bt := r.Backtrace
		if len(bt) == 0 {
			bt = jsrt.LastBacktrace(r.Log)
		}
		if len(jsrt.ParseBacktrace(bt)) > 0 {
			out = append(out, bt)
		}
	}
	return out
}

// loadSymbols reads every ELF file among files and in the directories
// among them.
func loadSymbols(files []string) ([]*jsrt.Symbols, error) {
	var out []*jsrt.Symbols
	add := func(p string) error {
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(raw), "\x7fELF") {
			return nil
		}
		s, err := jsrt.LoadSymbols(jsrt.ModuleName(p), raw)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, s)
		return nil
	}
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			if err := add(f); err != nil {
				return nil, err
			}
			continue
		}
		err = filepath.WalkDir(f, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() || !strings.HasSuffix(p, jsrt.NativeExt) {
				return err
			}
			return add(p)
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// missing are the modules of frames no file was given for, with their
// build-ids.
func missing(frames []jsrt.Frame, syms []*jsrt.Symbols) []string {
	var out []string
	for _, f := range frames {
		name := f.Object
		if f.BuildID != "" {
			name += " (build-id " + f.BuildID + ")"
		}
		if !strings.HasPrefix(f.Object, jsrt.NativeScheme) || slices.Contains(out, name) {
			continue
		}
		found := false
		for _, s := range syms {
			found = found || f.BuildID != "" && s.BuildID == f.BuildID || s.Name == f.Object && (f.BuildID == "" || s.BuildID == "")
		}
		if !found {
			out = append(out, name)
		}
	}
	return out
}

// libraries is -so, given once a file or directory.
type libraries []string

func (l *libraries) String() string { return strings.Join(*l, ",") }

func (l *libraries) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// symbolizeFile is kario symbolize: the crash in args (a file, none:
// standard input) with the libraries of so, and the source map of mapFile
// (the module beside it, as kario build writes them, when it is there) or
// of the symbol file symFile (whose module and libraries count too).
func symbolizeFile(args []string, so []string, mapFile, symFile string) error {
	var m *jsrt.SourceMap
	var module []byte
	main := jsrt.DefaultMain
	switch {
	case mapFile != "" && symFile != "":
		return errors.New("symbolize: -map or -symbols, not both")
	case mapFile != "":
		raw, err := os.ReadFile(mapFile)
		if err != nil {
			return err
		}
		if m, err = jsrt.ParseSourceMap(raw); err != nil {
			return err
		}
		module, _ = os.ReadFile(strings.TrimSuffix(mapFile, ".map"))
	case symFile != "":
		raw, err := os.ReadFile(symFile)
		if err != nil {
			return err
		}
		s, err := jsrt.ReadSymbolFile(raw)
		if err != nil {
			return err
		}
		if m, err = s.SourceMap(); err != nil {
			return err
		}
		main, module = jsrt.Main(s.Main), s.Module
		if len(s.Libs) > 0 {
			dir, err := os.MkdirTemp("", "kario-symbols-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(dir)
			for p, lib := range s.Libs {
				dst := filepath.Join(dir, filepath.FromSlash(p))
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(dst, lib, 0o644); err != nil {
					return err
				}
			}
			so = append(so, dir)
		}
	}
	in := io.Reader(os.Stdin)
	switch len(args) {
	case 0:
	case 1:
		f, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	default:
		return errors.New("symbolize: one crash at a time")
	}
	return symbolizeWith(in, os.Stdout, so, m, main, module)
}
