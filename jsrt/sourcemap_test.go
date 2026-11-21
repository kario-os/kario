package jsrt

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evanw/esbuild/pkg/api"
)

// vlq encodes source map segments: each line's segments as fields
// [column, source, line, column(, name)], absolute, 0-based.
func vlq(lines [][][]int) string {
	enc := func(v int) string {
		u := v << 1
		if v < 0 {
			u = (-v << 1) | 1
		}
		var s string
		for {
			d := u & 31
			u >>= 5
			if u > 0 {
				d |= 32
			}
			s += string(b64digits[d])
			if u == 0 {
				return s
			}
		}
	}
	var out []string
	prev := make([]int, 5)
	for _, segs := range lines {
		prev[0] = 0
		var parts []string
		for _, s := range segs {
			var p string
			for i, v := range s {
				p += enc(v - prev[i])
				prev[i] = v
			}
			parts = append(parts, p)
		}
		out = append(out, strings.Join(parts, ","))
	}
	return strings.Join(out, ";")
}

func testMap(t *testing.T) []byte {
	raw, _ := json.Marshal(map[string]any{
		"version": 3, "file": "main.js",
		"sources":        []string{"src/main.ts", "kario-sdk:@kario/ui"},
		"sourcesContent": []any{"let x = 1;\n", nil},
		"names":          []string{"press"},
		"mappings": vlq([][][]int{
			{{0, 1, 0, 0}},                  // 1: the SDK
			{{0, 0, 2, 0}, {6, 0, 2, 6, 0}}, // 2: main.ts:3
			{{2, 0, 3, 2}},                  // 3: main.ts:4
			{},                              // 4: nothing
			{{0, 0, 6, 0}, {10, 0, 7, 4}},   // 5: main.ts:7, then 8
			{{0}, {4, 0, 9, 0}},             // 6: unmapped, then main.ts:10
		}),
	})
	return raw
}

// A map from the bundle back to the TypeScript and forth: positions,
// names, where a line's code starts (a line without code moves on), and
// a stack's frames rewritten.
func TestSourceMap(t *testing.T) {
	m, err := ParseSourceMap(testMap(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		line, col int
		want      Position
		ok        bool
	}{
		{2, 1, Position{"src/main.ts", 3, 1, ""}, true},
		{2, 8, Position{"src/main.ts", 3, 7, "press"}, true},
		{3, 1, Position{"src/main.ts", 4, 3, ""}, true},
		{5, 12, Position{"src/main.ts", 8, 5, ""}, true},
		{6, 2, Position{"src/main.ts", 10, 1, ""}, true},
		{1, 1, Position{"kario-sdk:@kario/ui", 1, 1, ""}, true},
		{4, 1, Position{}, false},
		{9, 1, Position{}, false},
	} {
		p, ok := m.Original(c.line, c.col)
		if p != c.want || ok != c.ok {
			t.Errorf("Original(%d, %d) = %+v %v, want %+v", c.line, c.col, p, ok, c.want)
		}
	}
	if g, l := m.Generated("src/main.ts", 3, 0); len(g) != 1 || g[0] != 2 || l != 3 {
		t.Errorf("Generated(3) = %v %d", g, l)
	}
	if g, l := m.Generated("src/main.ts", 5, 5); len(g) != 1 || g[0] != 5 || l != 7 {
		t.Errorf("Generated(5) moved to %v %d, want line 7 at 5", g, l)
	}
	if g, _ := m.Generated("src/main.ts", 5, 1); g != nil {
		t.Errorf("Generated(5) within one line: %v", g)
	}
	if g, _ := m.Generated("src/other.ts", 1, 9); g != nil {
		t.Errorf("an unknown source: %v", g)
	}
	stack := []string{"Error: boom", "    at press (main.js:2:7)", "    at <anonymous> (main.js:5:11)", "    at forEach (native)", "    at f (other.js:2:1)"}
	got := m.SymbolizeStack(stack, "main.js")
	want := []string{"Error: boom", "    at press (src/main.ts:3:7)", "    at <anonymous> (src/main.ts:8:5)", "    at forEach (native)", "    at f (other.js:2:1)"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("SymbolizeStack:\n%s", strings.Join(got, "\n"))
	}
	if !HasJSFrames(stack, "main.js") || HasJSFrames(stack[3:], "main.js") {
		t.Error("HasJSFrames")
	}
	for _, bad := range []string{
		`{"version": 2, "sources": [], "mappings": ""}`,
		`{"version": 3, "sources": ["a"], "mappings": "A!"}`,
		`{"version": 3, "sources": ["a"], "mappings": "ACAA"}`,
		`{"version": 3, "sources": ["a"], "mappings": "AAAg"}`,
		`not json`,
	} {
		if _, err := ParseSourceMap([]byte(bad)); err == nil {
			t.Errorf("parsed %s", bad)
		}
	}
}

// A symbol file round-trips; one with parts a symbol file has not, or
// without its manifest, is refused; it symbolizes a crash's JavaScript
// stack and its native backtrace.
func TestSymbolFile(t *testing.T) {
	probe := nativeProbe(t)
	s := &SymbolFile{App: "com.example.app", Version: "1.2", PackageSHA256: strings.Repeat("ab", 32), Main: "main.js",
		MainSHA256: SHA256([]byte("x")), Map: testMap(t), Libs: map[string][]byte{"lib/x86_64/probe.so": probe}}
	raw, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadSymbolFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.App != s.App || got.Version != s.Version || got.PackageSHA256 != s.PackageSHA256 || !bytes.Equal(got.Map, s.Map) || !bytes.Equal(got.Libs["lib/x86_64/probe.so"], probe) {
		t.Fatalf("round trip: %+v", got)
	}
	log := []string{"kario-jsrt: the app failed to start: Error: boom", "    at press (main.js:2:7)"}
	stack, native := got.Symbolize(log, nil, nil)
	if len(stack) != 2 || stack[1] != "    at press (src/main.ts:3:7)" || native != nil {
		t.Fatalf("symbolized %q %q", stack, native)
	}
	if stack, _ := got.Symbolize([]string{"no frames here"}, nil, nil); stack != nil {
		t.Fatalf("a log without frames: %q", stack)
	}
	if sy, err := LoadSymbols("native:probe", probe); err == nil && len(sy.funcs) > 0 {
		f := sy.funcs[len(sy.funcs)/2]
		bt := []string{BacktraceHead + " in native:probe.f: SIGSEGV at 0x0", fmt.Sprintf("%s0 native:probe +0x%x build-id %s", BacktraceFrame, f.lo, sy.BuildID), BacktraceEnd}
		if _, native := got.Symbolize(nil, bt, nil); len(native) != 1 || !strings.Contains(native[0], f.name) {
			t.Fatalf("native backtrace %q, want %s", native, f.name)
		}
	}

	zipOf := func(files map[string]string) []byte {
		var buf bytes.Buffer
		z := zip.NewWriter(&buf)
		for n, b := range files {
			w, _ := z.Create(n)
			_, _ = w.Write([]byte(b))
		}
		_ = z.Close()
		return buf.Bytes()
	}
	head, _ := json.Marshal(s)
	for name, bad := range map[string][]byte{
		"not a zip":           []byte("PK nonsense"),
		"no manifest":         zipOf(map[string]string{"main.js.map": string(s.Map)}),
		"a stray file":        zipOf(map[string]string{SymbolsManifest: string(head), "notes.txt": "x"}),
		"a bad map":           zipOf(map[string]string{SymbolsManifest: string(head), "main.js.map": "{}"}),
		"a library not ELF":   zipOf(map[string]string{SymbolsManifest: string(head), "lib/arm64/x.so": "text"}),
		"a library elsewhere": zipOf(map[string]string{SymbolsManifest: string(head), "lib/mips/x.so": "\x7fELF"}),
		"no package digest":   zipOf(map[string]string{SymbolsManifest: `{"app": "a.b", "version": "1", "main": "main.js"}`}),
	} {
		if _, err := ReadSymbolFile(bad); err == nil {
			t.Errorf("%s: read", name)
		}
	}
}

// nativeProbe is an unstripped ELF library of this machine (the native
// tests' probe when built, else kario-jsrt itself stands in).
func nativeProbe(t *testing.T) []byte {
	for _, p := range []string{filepath.Join("..", "build", "native-test", "x86_64", "probe.so"), filepath.Join("..", "build", "jsrt", Program)} {
		if raw, err := os.ReadFile(p); err == nil {
			return raw
		}
	}
	return []byte("\x7fELF stand-in")
}

// Renames are the names esbuild changed in the bundle (a clash between
// modules), by source, from the map's names and the bundle itself; a
// column counts UTF-16 code units, as source maps do.
func TestRenames(t *testing.T) {
	dir := t.TempDir()
	for name, src := range map[string]string{
		"a.ts":    "export let count = 1;\nexport function bump(by: number): number {\n  count = count + by;\n  return count;\n}\n",
		"main.ts": "import { bump } from \"./a\";\nlet count = 10;\nlet label = \"\u4f60\u597d\\u{1f600}\"; label = label + count;\nconsole.log(\"\u4f60\u597d\U0001F600\", count, bump(count));\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := api.Build(api.BuildOptions{EntryPoints: []string{filepath.Join(dir, "main.ts")}, Bundle: true, Format: api.FormatESModule,
		Outfile: filepath.Join(dir, "out", "main.js"), Sourcemap: api.SourceMapExternal, AbsWorkingDir: dir, Charset: api.CharsetUTF8})
	if len(r.Errors) > 0 {
		t.Fatal(r.Errors)
	}
	var js, raw []byte
	for _, f := range r.OutputFiles {
		if strings.HasSuffix(f.Path, ".map") {
			raw = f.Contents
		} else {
			js = f.Contents
		}
	}
	m, err := ParseSourceMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), "count2") {
		t.Fatalf("esbuild renamed nothing:\n%s", js)
	}
	got := m.Renames(js)
	if len(got) != 1 || len(got["../main.ts"]) != 1 || got["../main.ts"]["count"] != "count2" {
		t.Fatalf("renames %v (sources %v)", got, m.Sources)
	}
	for _, tc := range []struct {
		line string
		col  int
		want string
	}{
		{"x(\"\u4f60\u597d\", count2)", 8, "count2"},
		{"x(\"\U0001F600\", count2)", 8, "count2"},
		{"x(\"\U0001F600\", count2)", 7, ""},
		{"x(count2)", 1, ""},
		{"x(count2)", 40, ""},
		{"f(a$1_b, 2)", 2, "a$1_b"},
	} {
		if got := identAt(tc.line, tc.col); got != tc.want {
			t.Errorf("identAt(%q, %d) = %q, want %q", tc.line, tc.col, got, tc.want)
		}
	}
}

const namedFrames = `import { on } from "kario";
function computeTotal(items: number[]): number {
  if (items.length > 2) throw new Error("too many");
  return items.length;
}
const handleStart = (label: string) => computeTotal([1, 2, label.length]);
class Cart {
  checkout() { return handleStart("\u4f60\u597d"); }
}
on("start", () => { new Cart().checkout(); });
`

// A stack of the minified module, as kario-jsrt writes it, gets each
// frame's function back by its name in the source: a function declared, an
// arrow function in a const; a method keeps its name (minifying leaves it)
// and an anonymous one stays anonymous.
func TestFunctionNames(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.ts"), []byte(namedFrames), 0o644); err != nil {
		t.Fatal(err)
	}
	r := api.Build(api.BuildOptions{EntryPoints: []string{filepath.Join(dir, "main.ts")}, Bundle: true, Format: api.FormatESModule, External: []string{"kario"},
		MinifyIdentifiers: true, MinifySyntax: true, MinifyWhitespace: true, Outfile: filepath.Join(dir, "out", DefaultMain), Sourcemap: api.SourceMapExternal,
		AbsWorkingDir: dir, Charset: api.CharsetUTF8, Write: true})
	if len(r.Errors) > 0 {
		t.Fatal(r.Errors)
	}
	js, _ := os.ReadFile(filepath.Join(dir, "out", DefaultMain))
	raw, _ := os.ReadFile(filepath.Join(dir, "out", DefaultMain+".map"))
	m, err := ParseSourceMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	s := launch(t, t.TempDir(), "en", binary(t), filepath.Join(dir, "out", DefaultMain))
	s.hello("", nil)
	if !s.logHas("too many", "(main.js:") {
		t.Fatalf("log:\n%s", s.log())
	}
	var lines []string
	for _, l := range strings.Split(s.log(), "\n") {
		if strings.Contains(l, "(main.js:") {
			lines = append(lines, l)
		}
	}
	minified := strings.Join(m.SymbolizeStack(lines, DefaultMain), "\n")
	named := strings.Join(m.SymbolizeStackNamed(lines, DefaultMain, js), "\n")
	for _, want := range []string{"at computeTotal (../main.ts:3:", "at handleStart (../main.ts:6:", "at checkout (../main.ts:8:", "at <anonymous> (../main.ts:10:"} {
		if !strings.Contains(named, want) {
			t.Errorf("no %q in\n%s", want, named)
		}
	}
	if strings.Contains(minified, "computeTotal") || strings.Contains(minified, "handleStart") {
		t.Errorf("the minified module kept the names:\n%s", minified)
	}
}
