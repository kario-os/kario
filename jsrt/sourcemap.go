package jsrt

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SourceMap is a source map (version 3, as esbuild writes them for main.js):
// from positions in the module kario-jsrt runs to the TypeScript, and
// back. It serves the debugger (breakpoints in .ts files, stacks in them)
// and the symbolication of JavaScript stacks in crash reports.
type SourceMap struct {
	File           string
	Sources        []string
	SourcesContent []string
	Names          []string
	// lines holds each generated line's segments, by column.
	lines [][]segment
}

type segment struct {
	col, src, line, srcCol, name int
}

// Position is a place in a source: line and column from 1, as kario-jsrt
// and editors count; Name the original identifier there, if the map says.
type Position struct {
	Source       string
	Line, Column int
	Name         string
}

// MaxSourceMap bounds a map read.
const MaxSourceMap = 64 << 20

// ParseSourceMap reads a map's JSON.
func ParseSourceMap(raw []byte) (*SourceMap, error) {
	if len(raw) > MaxSourceMap {
		return nil, fmt.Errorf("a source map larger than %d MB", MaxSourceMap>>20)
	}
	var j struct {
		Version        int       `json:"version"`
		File           string    `json:"file"`
		SourceRoot     string    `json:"sourceRoot"`
		Sources        []string  `json:"sources"`
		SourcesContent []*string `json:"sourcesContent"`
		Names          []string  `json:"names"`
		Mappings       string    `json:"mappings"`
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, fmt.Errorf("source map: %w", err)
	}
	if j.Version != 3 {
		return nil, fmt.Errorf("source map version %d (3 is read)", j.Version)
	}
	m := &SourceMap{File: j.File, Names: j.Names}
	for _, s := range j.Sources {
		if j.SourceRoot != "" && !strings.Contains(s, ":") {
			s = path.Join(j.SourceRoot, s)
		}
		m.Sources = append(m.Sources, s)
	}
	for _, c := range j.SourcesContent {
		if c == nil {
			m.SourcesContent = append(m.SourcesContent, "")
		} else {
			m.SourcesContent = append(m.SourcesContent, *c)
		}
	}
	if err := m.decode(j.Mappings); err != nil {
		return nil, err
	}
	return m, nil
}

const b64digits = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

var b64value = func() (t [256]int8) {
	for i := range t {
		t[i] = -1
	}
	for i, c := range b64digits {
		t[c] = int8(i)
	}
	return
}()

func (m *SourceMap) decode(mappings string) error {
	var src, line, srcCol, name int
	cur := []segment{}
	i := 0
	vlq := func() (int, error) {
		v, shift := 0, 0
		for {
			if i >= len(mappings) {
				return 0, errors.New("source map: mappings cut short")
			}
			d := b64value[mappings[i]]
			i++
			if d < 0 || shift > 30 {
				return 0, errors.New("source map: bad mappings")
			}
			v |= int(d&31) << shift
			shift += 5
			if d&32 == 0 {
				break
			}
		}
		if v&1 != 0 {
			return -(v >> 1), nil
		}
		return v >> 1, nil
	}
	col := 0
	for i <= len(mappings) {
		if i == len(mappings) || mappings[i] == ';' {
			sort.SliceStable(cur, func(a, b int) bool { return cur[a].col < cur[b].col })
			m.lines = append(m.lines, cur)
			cur, col = []segment{}, 0
			i++
			continue
		}
		if mappings[i] == ',' {
			i++
			continue
		}
		var fields [5]int
		n := 0
		for n < 5 && i < len(mappings) && mappings[i] != ',' && mappings[i] != ';' {
			v, err := vlq()
			if err != nil {
				return err
			}
			fields[n] = v
			n++
		}
		col += fields[0]
		s := segment{col: col, src: -1, name: -1}
		if n >= 4 {
			src += fields[1]
			line += fields[2]
			srcCol += fields[3]
			if src < 0 || src >= len(m.Sources) {
				return errors.New("source map: a mapping to a source it does not list")
			}
			s.src, s.line, s.srcCol = src, line, srcCol
			if n == 5 {
				name += fields[4]
				if name >= 0 && name < len(m.Names) {
					s.name = name
				}
			}
		}
		cur = append(cur, s)
	}
	return nil
}

// Original is where a generated position (line and column from 1) comes
// from: the last mapping at or before it on its line.
func (m *SourceMap) Original(line, column int) (Position, bool) {
	if line < 1 || line > len(m.lines) {
		return Position{}, false
	}
	segs := m.lines[line-1]
	k := sort.Search(len(segs), func(i int) bool { return segs[i].col > column-1 }) - 1
	for ; k >= 0 && segs[k].src < 0; k-- {
	}
	if k < 0 {
		for k = 0; k < len(segs) && segs[k].src < 0; k++ {
		}
		if k == len(segs) {
			return Position{}, false
		}
	}
	s := segs[k]
	p := Position{Source: m.Sources[s.src], Line: s.line + 1, Column: s.srcCol + 1}
	if s.name >= 0 {
		p.Name = m.Names[s.name]
	}
	return p, true
}

// Renames are the identifiers the bundler renamed (esbuild gives a name
// that clashes with another module's a suffix: count2 for count), by
// source: the original name to the name in generated, the module the map
// is of. The map names the original only where the two differ; a name
// renamed two ways in one source is left out.
func (m *SourceMap) Renames(generated []byte) map[string]map[string]string {
	lines := strings.Split(string(generated), "\n")
	out := map[string]map[string]string{}
	twice := map[string]bool{}
	for g, segs := range m.lines {
		if g >= len(lines) {
			break
		}
		for _, s := range segs {
			if s.name < 0 || s.src < 0 {
				continue
			}
			orig, src := m.Names[s.name], m.Sources[s.src]
			gen := identAt(lines[g], s.col)
			if gen == "" || gen == orig || twice[src+"\x00"+orig] {
				continue
			}
			if out[src] == nil {
				out[src] = map[string]string{}
			}
			if had, ok := out[src][orig]; ok && had != gen {
				delete(out[src], orig)
				twice[src+"\x00"+orig] = true
				continue
			}
			out[src][orig] = gen
		}
	}
	return out
}

// byteAt is the byte offset in line of its UTF-16 column col (as source
// maps count), -1 past its end.
func byteAt(line string, col int) int {
	units := 0
	for i, r := range line {
		if units == col {
			return i
		}
		units++
		if r > 0xffff {
			units++
		}
	}
	return -1
}

// identAt is the identifier at col (UTF-16 code units) of line, "" when
// there is none.
func identAt(line string, col int) string {
	at := byteAt(line, col)
	if at < 0 {
		return ""
	}
	end := at
	for end < len(line) {
		c := line[end]
		if c >= 0x80 || c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || end > at && c >= '0' && c <= '9' {
			end++
			continue
		}
		break
	}
	return line[at:end]
}

// SourceIndex is source's index in Sources, or -1.
func (m *SourceMap) SourceIndex(source string) int {
	for i, s := range m.Sources {
		if s == source {
			return i
		}
	}
	return -1
}

// Generated are the generated lines where the code of source's line
// starts (the first mapping of each is on it), and that line; a line
// without code moves to the next one that has some, at most ahead lines
// on.
func (m *SourceMap) Generated(source string, line, ahead int) ([]int, int) {
	src := m.SourceIndex(source)
	if src < 0 {
		return nil, 0
	}
	starts := map[int][]int{}
	for g, segs := range m.lines {
		for _, s := range segs {
			if s.src >= 0 {
				if s.src == src {
					starts[s.line+1] = append(starts[s.line+1], g+1)
				}
				break
			}
		}
	}
	for l := line; l <= line+ahead; l++ {
		if gs := starts[l]; len(gs) > 0 {
			return gs, l
		}
	}
	return nil, 0
}

// jsFrame is a frame of a JavaScript stack as QuickJS writes it.
var jsFrame = regexp.MustCompile(`^(\s*at )(.*) \(([^()]+):(\d+):(\d+)\)\s*$`)

// SymbolizeStack maps the frames in main (the module's file name) of a
// JavaScript stack's lines (an error's stack, a log) to the sources;
// other lines stay as they are.
func (m *SourceMap) SymbolizeStack(lines []string, main string) []string {
	return m.SymbolizeStackNamed(lines, main, nil)
}

// SymbolizeStackNamed is SymbolizeStack that, given the module itself
// (minified), also gives each frame its function's name in the source
// (FunctionName).
func (m *SourceMap) SymbolizeStackNamed(lines []string, main string, module []byte) []string {
	var gen []string
	if module != nil {
		gen = strings.Split(string(module), "\n")
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l
		f := jsFrame.FindStringSubmatch(l)
		if f == nil || path.Base(f[3]) != main {
			continue
		}
		line, _ := strconv.Atoi(f[4])
		col, _ := strconv.Atoi(f[5])
		if p, ok := m.Original(line, col); ok {
			name := f[2]
			if orig := m.functionName(gen, line, col, name); orig != "" {
				name = orig
			}
			out[i] = fmt.Sprintf("%s%s (%s:%d:%d)", f[1], name, p.Source, p.Line, p.Column)
		}
	}
	return out
}

// FunctionName is the name in the source of the function named fn in
// the minified module generated whose code is at line and column (from 1,
// as kario-jsrt counts): the name the map gives the nearest declaration of
// fn before it (function fn, class fn, let, const or var fn =). A nested
// scope never takes a name of the scopes around it when esbuild minifies,
// so no other declaration of fn lies between the function's and its body.
// "" when the map does not say (fn was not renamed, or is not declared so:
// a method, a property).
func (m *SourceMap) FunctionName(generated []byte, line, column int, fn string) string {
	return m.functionName(strings.Split(string(generated), "\n"), line, column, fn)
}

func (m *SourceMap) functionName(gen []string, line, column int, fn string) string {
	if len(gen) == 0 || fn == "" || identAt(fn, 0) != fn {
		return ""
	}
	for g := min(line, len(m.lines), len(gen)) - 1; g >= 0; g-- {
		segs := m.lines[g]
		for k := len(segs) - 1; k >= 0; k-- {
			s := segs[k]
			if s.name < 0 || g == line-1 && s.col > column-1 || identAt(gen[g], s.col) != fn {
				continue
			}
			if declares(gen[g], s.col, len(fn)) {
				return m.Names[s.name]
			}
		}
	}
	return ""
}

// declares: the identifier of n bytes at line's UTF-16 column col is a
// function's or a variable's declaration.
func declares(line string, col, n int) bool {
	at := byteAt(line, col)
	if at < 0 {
		return false
	}
	before := strings.TrimRight(line[:at], " ")
	after := strings.TrimLeft(line[min(at+n, len(line)):], " ")
	for _, kw := range []string{"function", "function*", "class"} {
		if strings.HasSuffix(before, kw) && (len(before) == len(kw) || identAt(before[len(before)-len(kw)-1:], 0) == "") {
			return true
		}
	}
	if !strings.HasPrefix(after, "=") || strings.HasPrefix(after, "==") || strings.HasPrefix(after, "=>") {
		return false
	}
	for _, kw := range []string{"let", "const", "var"} {
		if strings.HasSuffix(before, kw) && (len(before) == len(kw) || identAt(before[len(before)-len(kw)-1:], 0) == "") {
			return true
		}
	}
	return strings.HasSuffix(before, ",")
}

// HasJSFrames says whether lines hold a frame of main's.
func HasJSFrames(lines []string, main string) bool {
	for _, l := range lines {
		if f := jsFrame.FindStringSubmatch(l); f != nil && path.Base(f[3]) == main {
			return true
		}
	}
	return false
}
