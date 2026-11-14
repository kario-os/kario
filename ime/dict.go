// Package ime is the system input method: Chinese pinyin and English for both
// full (QWERTY) and 9-key (T9) keypads, with candidates, abbreviations and
// association. It runs in phone-imed (or inside the shell as a fallback), so
// apps only ever receive committed text, never raw keystrokes.
//
// Dictionaries come from AOSP (Apache-2.0): PinyinIME's raw dictionary and
// LatinIME's English word list; see data/NOTICE.* and tools/imedict.
package ime

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"
)

//go:embed data/zh.tsv.gz
var zhData []byte

//go:embed data/en.tsv.gz
var enData []byte

// strokes.tsv.gz: character and its stroke code in GB 13000.1's five
// classes (1 horizontal, 2 vertical, 3 left-falling, 4 dot, 5 turning), from
// cnchar's stroke-order data (MIT, NOTICE.cnchar;
// tools/strokes-from-cnchar.py).
//
//go:embed data/strokes.tsv.gz
var strokeData []byte

var t9Letters = map[rune]byte{
	'a': '2', 'b': '2', 'c': '2', 'd': '3', 'e': '3', 'f': '3', 'g': '4', 'h': '4', 'i': '4',
	'j': '5', 'k': '5', 'l': '5', 'm': '6', 'n': '6', 'o': '6', 'p': '7', 'q': '7', 'r': '7', 's': '7',
	't': '8', 'u': '8', 'v': '8', 'w': '9', 'x': '9', 'y': '9', 'z': '9',
}

// T9 returns the 9-key digit code of a word ("" if it has other characters).
func T9(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		d, ok := t9Letters[r]
		if !ok {
			if r == '\'' {
				continue
			}
			return ""
		}
		b.WriteByte(d)
	}
	return b.String()
}

// initial is the first letter: abbreviations match z for both z and zh, as
// users type them.
func initial(syl string) string { return syl[:1] }

// The dictionary is one read-only blob, built from the embedded word lists
// at build time (phone-imedict) and mapped into memory at run time, so only
// the pages a lookup touches are resident and they stay shareable, clean
// page cache. Every index is a sorted array of fixed-size records searched
// in place; nothing is parsed into Go maps except the ~400 syllables.
//
//	header   magic "PIMEDIC1", crc32 of the source lists, then per section
//	         (offset, count) as little-endian uint32
//	pool     strings, each a length byte and the bytes, deduplicated
//	zh       per word, in frequency order: word, pinyin ("zhong guo")
//	en       per word: word, lower case, digit code, frequency
//	tables   (key, id) sorted by key then id; ids of equal keys are
//	         therefore in frequency order

const dictMagic = "PIMEDIC2"

const (
	secPool = iota
	secZh
	secEn
	secSyl     // syllables; id unused
	secKey     // "zhong guo"
	secT9      // "94664 486"
	secInit    // "zh g", 2-4 syllable words, at most 40 per key
	secWord    // by word, for association
	secEnLower // en by lower-case spelling
	secEnT9    // en by digit code
	secStroke  // "25112141" (stroke code) -> zh id of the character
	nSec
)

const headerSize = 8 + 4 + 8*nSec

var recSize = [nSec]int{secPool: 1, secZh: 8, secEn: 16, secSyl: 8, secKey: 8, secT9: 8, secInit: 8, secWord: 8, secEnLower: 8, secEnT9: 8, secStroke: 8}

type section struct{ off, n int }

type dicts struct {
	b        []byte
	sec      [nSec]section
	syllable map[string]bool
	sylPre   map[string][]string // letter prefix -> syllables
	codeFull map[string]bool     // syllable digit codes
	codePre  map[string][]string // digit prefix -> full codes
}

var (
	loadOnce sync.Once
	loaded   *dicts
	dictPath string
	// DictSource says where the dictionary came from, for diagnostics.
	DictSource string
)

// SetDictPath points the engine at a prebuilt dictionary file; without one
// (or if it is stale or damaged) the dictionary is built in memory.
func SetDictPath(path string) { dictPath = path }

func load() *dicts {
	loadOnce.Do(func() {
		if dictPath != "" {
			if b, err := mapFile(dictPath); err == nil {
				if d, err := openDict(b); err == nil {
					loaded, DictSource = d, "mapped "+dictPath
					return
				}
			}
		}
		d, err := openDict(BuildDict())
		if err != nil {
			panic(err)
		}
		loaded, DictSource = d, "built in memory"
		debug.FreeOSMemory()
	})
	return loaded
}

// Preload opens the dictionary in the background so the first keystroke
// does not stall.
func Preload() { go load() }

// sourceCRC identifies the embedded word lists from their lengths and ends:
// enough to notice a dictionary built from other lists, without paging in
// the whole embedded data.
func sourceCRC() uint32 {
	h := crc32.NewIEEE()
	for _, b := range [][]byte{zhData, enData, strokeData} {
		_ = binary.Write(h, binary.LittleEndian, uint64(len(b)))
		h.Write(b[:min(len(b), 4096)])
		h.Write(b[max(0, len(b)-4096):])
	}
	return h.Sum32()
}

func openDict(b []byte) (*dicts, error) {
	if len(b) < headerSize || string(b[:8]) != dictMagic {
		return nil, errors.New("not an IME dictionary")
	}
	if binary.LittleEndian.Uint32(b[8:]) != sourceCRC() {
		return nil, errors.New("IME dictionary is from other word lists")
	}
	d := &dicts{b: b, syllable: map[string]bool{}, sylPre: map[string][]string{},
		codeFull: map[string]bool{}, codePre: map[string][]string{}}
	for i := range d.sec {
		off := int(binary.LittleEndian.Uint32(b[12+8*i:]))
		n := int(binary.LittleEndian.Uint32(b[16+8*i:]))
		if off < headerSize || n < 0 || off+n*recSize[i] > len(b) {
			return nil, errors.New("IME dictionary is damaged")
		}
		d.sec[i] = section{off, n}
	}
	for k := 0; k < d.sec[secSyl].n; k++ {
		s := d.tkey(secSyl, k)
		d.syllable[s] = true
		for j := 1; j <= len(s); j++ {
			d.sylPre[s[:j]] = append(d.sylPre[s[:j]], s)
		}
		code := T9(s)
		if !d.codeFull[code] {
			d.codeFull[code] = true
			for j := 1; j <= len(code); j++ {
				d.codePre[code[:j]] = append(d.codePre[code[:j]], code)
			}
		}
	}
	return d, nil
}

func (d *dicts) u32(at int) uint32 { return binary.LittleEndian.Uint32(d.b[at:]) }

// str reads a pool string without copying; the blob lives as long as the
// process.
func (d *dicts) str(off uint32) string {
	p := d.sec[secPool]
	at := p.off + int(off)
	if int(off) >= p.n {
		return ""
	}
	n := int(d.b[at])
	if int(off)+1+n > p.n || n == 0 {
		return ""
	}
	return unsafe.String(&d.b[at+1], n)
}

func (d *dicts) rec(sec, i int) int { return d.sec[sec].off + i*recSize[sec] }

func (d *dicts) zhCount() int           { return d.sec[secZh].n }
func (d *dicts) zhWord(id int32) string { return d.str(d.u32(d.rec(secZh, int(id)))) }
func (d *dicts) zhPinyin(id int32) string {
	return d.str(d.u32(d.rec(secZh, int(id)) + 4))
}

type enEntry struct {
	word, lower, t9 string
	freq            int
}

func (d *dicts) enCount() int { return d.sec[secEn].n }

func (d *dicts) en(id int32) enEntry {
	at := d.rec(secEn, int(id))
	return enEntry{word: d.str(d.u32(at)), lower: d.str(d.u32(at + 4)), t9: d.str(d.u32(at + 8)), freq: int(d.u32(at + 12))}
}

func (d *dicts) tkey(sec, k int) string { return d.str(d.u32(d.rec(sec, k))) }
func (d *dicts) tid(sec, k int) int32   { return int32(d.u32(d.rec(sec, k) + 4)) }

// exact returns the ids stored under key, in frequency order.
func (d *dicts) exact(sec int, key string) []int32 {
	n := d.sec[sec].n
	lo := sort.Search(n, func(k int) bool { return d.tkey(sec, k) >= key })
	var out []int32
	for k := lo; k < n && d.tkey(sec, k) == key; k++ {
		out = append(out, d.tid(sec, k))
	}
	return out
}

// prefix calls f for every record whose key starts with p, in key order,
// until f returns false.
func (d *dicts) prefix(sec int, p string, f func(id int32, key string) bool) {
	n := d.sec[sec].n
	lo := sort.Search(n, func(k int) bool { return d.tkey(sec, k) >= p })
	for k := lo; k < n; k++ {
		key := d.tkey(sec, k)
		if !strings.HasPrefix(key, p) || !f(d.tid(sec, k), key) {
			return
		}
	}
}

// prefixRange returns English ids whose key starts with p, by frequency.
func (d *dicts) prefixRange(sec int, p string, limit int, keep func(enEntry) bool) []int32 {
	var hits []int32
	d.prefix(sec, p, func(id int32, _ string) bool {
		if keep == nil || keep(d.en(id)) {
			hits = append(hits, id)
		}
		return true
	})
	sort.SliceStable(hits, func(a, b int) bool { return d.en(hits[a]).freq > d.en(hits[b]).freq })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func lines(gz []byte, f func(string)) {
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return
	}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f(sc.Text())
	}
}

type dictBuilder struct {
	pool   []byte
	at     map[string]uint32
	tables [nSec][][2]uint32 // (key offset, id); key strings kept for sorting
	keys   [nSec][]string
}

func (w *dictBuilder) intern(s string) uint32 {
	if len(s) > 255 {
		s = s[:255]
	}
	if off, ok := w.at[s]; ok {
		return off
	}
	off := uint32(len(w.pool))
	w.pool = append(w.pool, byte(len(s)))
	w.pool = append(w.pool, s...)
	w.at[s] = off
	return off
}

func (w *dictBuilder) add(sec int, key string, id int) {
	w.tables[sec] = append(w.tables[sec], [2]uint32{w.intern(key), uint32(id)})
	w.keys[sec] = append(w.keys[sec], key)
}

// BuildDict compiles the embedded word lists into the dictionary blob.
func BuildDict() []byte {
	w := &dictBuilder{pool: []byte{0}, at: map[string]uint32{"": 0}}
	var zh [][2]uint32
	single := map[string]int{} // a character's first (most frequent) zh id
	syllables := map[string]bool{}
	perInit := map[string]int{}
	// zh.tsv.gz is sorted by frequency, so line order is frequency order.
	lines(zhData, func(l string) {
		p := strings.Split(l, "\t")
		if len(p) != 3 {
			return
		}
		id := len(zh)
		zh = append(zh, [2]uint32{w.intern(p[0]), w.intern(p[2])})
		if _, ok := single[p[0]]; !ok && utf8.RuneCountInString(p[0]) == 1 {
			single[p[0]] = id
		}
		syl := strings.Fields(p[2])
		codes := make([]string, len(syl))
		inits := make([]string, len(syl))
		for k, s := range syl {
			syllables[s] = true
			codes[k] = T9(s)
			inits[k] = initial(s)
		}
		w.add(secKey, p[2], id)
		w.add(secT9, strings.Join(codes, " "), id)
		w.add(secWord, p[0], id)
		if ik := strings.Join(inits, " "); len(syl) >= 2 && len(syl) <= 4 && perInit[ik] < 40 {
			perInit[ik]++
			w.add(secInit, ik, id)
		}
	})
	for s := range syllables {
		w.add(secSyl, s, 0)
	}
	// Stroke codes point at the character's zh entry, so ids are frequency
	// ranks; characters the word list lacks go last, without pinyin.
	lines(strokeData, func(l string) {
		ch, code, ok := strings.Cut(l, "\t")
		if !ok || code == "" {
			return
		}
		id, ok := single[ch]
		if !ok {
			id = len(zh)
			zh = append(zh, [2]uint32{w.intern(ch), 0})
			single[ch] = id
		}
		w.add(secStroke, code, id)
	})
	var en [][4]uint32
	lines(enData, func(l string) {
		p := strings.Split(l, "\t")
		if len(p) != 2 {
			return
		}
		f, _ := strconv.Atoi(p[1])
		id := len(en)
		lower := strings.ToLower(p[0])
		code := T9(p[0])
		en = append(en, [4]uint32{w.intern(p[0]), w.intern(lower), w.intern(code), uint32(f)})
		w.add(secEnLower, lower, id)
		w.add(secEnT9, code, id)
	})
	for sec := secSyl; sec < nSec; sec++ {
		t, keys := w.tables[sec], w.keys[sec]
		idx := make([]int, len(t))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return keys[idx[a]] < keys[idx[b]] })
		sorted := make([][2]uint32, len(t))
		for i, j := range idx {
			sorted[i] = t[j]
		}
		w.tables[sec] = sorted
	}
	var body bytes.Buffer
	var secs [nSec]section
	put := func(sec int, n int, write func()) {
		for (headerSize+body.Len())%4 != 0 {
			body.WriteByte(0)
		}
		secs[sec] = section{headerSize + body.Len(), n}
		write()
	}
	le := func(v uint32) { _ = binary.Write(&body, binary.LittleEndian, v) }
	put(secPool, len(w.pool), func() { body.Write(w.pool) })
	put(secZh, len(zh), func() {
		for _, e := range zh {
			le(e[0])
			le(e[1])
		}
	})
	put(secEn, len(en), func() {
		for _, e := range en {
			for _, v := range e {
				le(v)
			}
		}
	})
	for sec := secSyl; sec < nSec; sec++ {
		put(sec, len(w.tables[sec]), func() {
			for _, r := range w.tables[sec] {
				le(r[0])
				le(r[1])
			}
		})
	}
	out := make([]byte, headerSize, headerSize+body.Len())
	copy(out, dictMagic)
	binary.LittleEndian.PutUint32(out[8:], sourceCRC())
	for i, s := range secs {
		binary.LittleEndian.PutUint32(out[12+8*i:], uint32(s.off))
		binary.LittleEndian.PutUint32(out[16+8*i:], uint32(s.n))
	}
	return append(out, body.Bytes()...)
}

// WriteDict writes the compiled dictionary to path atomically.
func WriteDict(path string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, BuildDict(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
