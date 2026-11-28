package jsrt

import (
	"bytes"
	"debug/dwarf"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The lines kario-jsrt writes to the app's log when an app with native
// modules crashes (src/jsrt.h's JSRT_BACKTRACE_*): the head says what
// crashed, then one line a native frame, innermost first, then the end.
//
//	kario-jsrt: crashed in native:probe.crash: SIGSEGV at 0x0
//	kario-jsrt: #0 native:probe +0x1139 build-id 5e1d…
//	kario-jsrt: #1 kario-jsrt +0x2a1b0 build-id 77ac…
//	kario-jsrt: end of backtrace
const (
	BacktraceHead  = "kario-jsrt: crashed"
	BacktraceFrame = "kario-jsrt: #"
	BacktraceEnd   = "kario-jsrt: end of backtrace"
	// MaxBacktrace bounds the lines of one (kario-jsrt writes at most 48
	// frames).
	MaxBacktrace = 52
)

// Frame is one line of a backtrace: the object the address is in (a
// module as "native:<name>", else the file's name, "" when unknown), the
// offset into it (an address of its ELF file), its build-id.
type Frame struct {
	N       int
	Object  string
	Offset  uint64
	BuildID string
	// Addr is the address itself when no object holds it.
	Addr uint64
}

var frameLine = regexp.MustCompile(`#(\d+) (?:(\S+) \+0x([0-9a-f]+)(?: build-id ([0-9a-f]+))?|0x([0-9a-f]+))\s*$`)

// LastBacktrace is the last backtrace in a log's lines, head to end, or
// nil; one cut short (the log ends inside it) counts up to its last line.
func LastBacktrace(lines []string) []string {
	start := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], BacktraceHead) {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	var out []string
	for _, l := range lines[start:] {
		if len(out) == MaxBacktrace {
			break
		}
		if len(out) > 0 && !strings.HasPrefix(l, BacktraceFrame) && l != BacktraceEnd {
			break
		}
		out = append(out, l)
		if l == BacktraceEnd {
			break
		}
	}
	return out
}

// ParseBacktrace reads the frames out of lines (a backtrace, a log, or
// text pasted from either: the "kario-jsrt: " in front is optional).
func ParseBacktrace(lines []string) []Frame {
	var out []Frame
	for _, l := range lines {
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), strings.TrimSuffix(BacktraceFrame, "#")))
		if !strings.HasPrefix(l, "#") {
			continue
		}
		m := frameLine.FindStringSubmatch(l)
		if m == nil || m[0] != l {
			continue
		}
		f := Frame{Object: m[2], BuildID: m[4]}
		f.N, _ = strconv.Atoi(m[1])
		if m[5] != "" {
			f.Addr, _ = strconv.ParseUint(m[5], 16, 64)
		} else {
			f.Offset, _ = strconv.ParseUint(m[3], 16, 64)
		}
		out = append(out, f)
	}
	return out
}

// Symbols maps the offsets of one ELF file (a module or any other library,
// unstripped: its symbol table and DWARF) to functions and source lines.
type Symbols struct {
	Name    string
	BuildID string
	dwarf   *dwarf.Data
	funcs   []elfFunc
}

type elfFunc struct {
	name   string
	lo, hi uint64
}

// Location is where an offset is: the function, and the source line when
// the file has DWARF.
type Location struct {
	Function string
	File     string
	Line     int
}

func (l Location) String() string {
	s := l.Function
	if s == "" {
		s = "?"
	}
	if l.File != "" {
		s += fmt.Sprintf(" (%s:%d)", l.File, l.Line)
	}
	return s
}

// LoadSymbols reads an ELF file for Symbolize; name is how backtraces
// name it ("native:imgproc" for lib/<arch>/imgproc.so, else its file name).
func LoadSymbols(name string, raw []byte) (*Symbols, error) {
	f, err := elf.NewFile(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("not an ELF file")
	}
	defer f.Close()
	s := &Symbols{Name: name, BuildID: BuildID(f)}
	syms, _ := f.Symbols()
	if len(syms) == 0 {
		syms, _ = f.DynamicSymbols()
	}
	for _, y := range syms {
		if elf.ST_TYPE(y.Info) == elf.STT_FUNC && y.Value != 0 {
			s.funcs = append(s.funcs, elfFunc{y.Name, y.Value, y.Value + max(y.Size, 1)})
		}
	}
	sort.Slice(s.funcs, func(i, j int) bool { return s.funcs[i].lo < s.funcs[j].lo })
	if d, err := f.DWARF(); err == nil {
		s.dwarf = d
	}
	return s, nil
}

// ModuleName is how a backtrace names a file of a package or a build:
// lib/<arch>/<name>.so and <arch>/<name>.so (a project's native/) are the
// module <name>, anything else goes by its file name.
func ModuleName(file string) string {
	file = strings.ReplaceAll(file, "\\", "/")
	if name, ok := strings.CutSuffix(path.Base(file), NativeExt); ok && ValidNative(name) {
		arch := path.Base(path.Dir(file))
		for _, a := range Arches {
			if a == arch {
				return NativeScheme + name
			}
		}
	}
	return path.Base(file)
}

// ntGNUBuildID is the note type of a build-id (elf.h's NT_GNU_BUILD_ID).
const ntGNUBuildID = 3

// BuildID is the file's NT_GNU_BUILD_ID in hex, "" without one.
func BuildID(f *elf.File) string {
	for _, sec := range f.Sections {
		if sec.Type != elf.SHT_NOTE {
			continue
		}
		raw, err := sec.Data()
		if err != nil {
			continue
		}
		for len(raw) >= 12 {
			namesz, descsz, typ := f.ByteOrder.Uint32(raw), f.ByteOrder.Uint32(raw[4:]), f.ByteOrder.Uint32(raw[8:])
			name, desc := (uint64(namesz)+3)&^3, (uint64(descsz)+3)&^3
			if 12+name+desc > uint64(len(raw)) {
				break
			}
			if typ == ntGNUBuildID && namesz == 4 && string(raw[12:16]) == "GNU\x00" {
				return hex.EncodeToString(raw[12+name : 12+name+uint64(descsz)])
			}
			raw = raw[12+name+desc:]
		}
	}
	return ""
}

// Lookup is the location of an offset; caller is true for a frame past
// the first, whose address is where the call returns to, so the call
// itself is the byte before.
func (s *Symbols) Lookup(off uint64, caller bool) (Location, bool) {
	pc := off
	if caller && pc > 0 {
		pc--
	}
	var loc Location
	if s.dwarf != nil {
		r := s.dwarf.Reader()
		if cu, err := r.SeekPC(pc); err == nil && cu != nil {
			loc.Function = dwarfFunction(s.dwarf, r, pc)
			if lr, err := s.dwarf.LineReader(cu); err == nil && lr != nil {
				var le dwarf.LineEntry
				if lr.SeekPC(pc, &le) == nil && le.File != nil {
					loc.File, loc.Line = path.Base(le.File.Name), le.Line
				}
			}
		}
	}
	if loc.Function == "" {
		i := sort.Search(len(s.funcs), func(i int) bool { return s.funcs[i].lo > pc }) - 1
		if i >= 0 && pc < s.funcs[i].hi {
			loc.Function = s.funcs[i].name
		}
	}
	return loc, loc.Function != "" || loc.File != ""
}

// dwarfFunction is the innermost function (inlined or not) holding pc in
// the compile unit whose children r is at.
func dwarfFunction(d *dwarf.Data, r *dwarf.Reader, pc uint64) string {
	name, depth := "", 0
	for {
		e, err := r.Next()
		if err != nil || e == nil {
			return name
		}
		if e.Tag == 0 {
			if depth--; depth < 0 {
				return name
			}
			continue
		}
		descend := e.Tag == dwarf.TagNamespace
		switch e.Tag {
		case dwarf.TagSubprogram, dwarf.TagInlinedSubroutine, dwarf.TagLexDwarfBlock:
			ranges, _ := d.Ranges(e)
			for _, rg := range ranges {
				descend = descend || pc >= rg[0] && pc < rg[1]
			}
			if n := entryName(d, e); descend && e.Tag != dwarf.TagLexDwarfBlock && n != "" {
				name = n
			}
		}
		switch {
		case e.Children && descend:
			depth++
		case e.Children:
			r.SkipChildren()
		}
	}
}

// entryName is a function's name, from its abstract origin or
// specification when it has none of its own (inlined and out-of-line
// copies, C++ methods).
func entryName(d *dwarf.Data, e *dwarf.Entry) string {
	for i := 0; i < 4 && e != nil; i++ {
		if n, ok := e.Val(dwarf.AttrName).(string); ok {
			return n
		}
		off, ok := e.Val(dwarf.AttrAbstractOrigin).(dwarf.Offset)
		if !ok {
			off, ok = e.Val(dwarf.AttrSpecification).(dwarf.Offset)
		}
		if !ok {
			return ""
		}
		r := d.Reader()
		r.Seek(off)
		e, _ = r.Next()
	}
	return ""
}

// Symbolize writes a backtrace's frames with what files says of them: a
// frame's file is the one with its build-id, or, when either has none, the
// one of its name. Frames no file holds stay as they are.
func Symbolize(frames []Frame, files []*Symbols) []string {
	var out []string
	for i, f := range frames {
		if f.Object == "" {
			out = append(out, fmt.Sprintf("#%d 0x%x", f.N, f.Addr))
			continue
		}
		line := fmt.Sprintf("#%d %s +0x%x", f.N, f.Object, f.Offset)
		var s *Symbols
		for _, c := range files {
			if f.BuildID != "" && c.BuildID == f.BuildID {
				s = c
				break
			}
		}
		if s == nil {
			for _, c := range files {
				if c.Name == f.Object && (f.BuildID == "" || c.BuildID == "") {
					s = c
					break
				}
			}
		}
		switch loc, ok := lookup(s, f.Offset, i > 0); {
		case s == nil:
		case ok:
			line += " " + loc.String()
		default:
			line += " ?"
		}
		out = append(out, line)
	}
	return out
}

func lookup(s *Symbols, off uint64, caller bool) (Location, bool) {
	if s == nil {
		return Location{}, false
	}
	return s.Lookup(off, caller)
}
