package ime

import (
	_ "embed"
	"encoding/json"
	"slices"
	"unicode"
	"unicode/utf8"
)

// marks.json: the punctuation the Chinese modes type, the glyphs that show
// a stroke code and what a key types with Chr held.
//
//go:embed data/marks.json
var marksData []byte

var marks = loadMarks()

type markTables struct {
	punct       []string        // the 9-key pad's 1 key lists these
	fullWidth   map[rune]string // a keyboard's marks typed in pinyin mode
	sentenceEnd string          // marks after which Abc capitalises
	strokes     map[rune]string // by stroke key: 1-5 the classes, 6 any stroke
	// variants: a key's accented letters or related marks (Chr held);
	// variantsZh: the Chinese marks that come first in pinyin mode.
	variants, variantsZh map[rune][]string
}

func loadMarks() markTables {
	var raw struct {
		Punctuation []string            `json:"punctuation"`
		FullWidth   map[string]string   `json:"full_width"`
		SentenceEnd string              `json:"sentence_end"`
		Strokes     map[string]string   `json:"strokes"`
		Variants    map[string][]string `json:"variants"`
		VariantsZh  map[string][]string `json:"variants_zh"`
	}
	if err := json.Unmarshal(marksData, &raw); err != nil {
		panic("ime: data/marks.json: " + err.Error())
	}
	byRune := func(m map[string]string) map[rune]string {
		out := make(map[rune]string, len(m))
		for k, v := range m {
			r, _ := utf8.DecodeRuneInString(k)
			out[r] = v
		}
		return out
	}
	lists := func(m map[string][]string) map[rune][]string {
		out := make(map[rune][]string, len(m))
		for k, v := range m {
			r, _ := utf8.DecodeRuneInString(k)
			out[r] = v
		}
		return out
	}
	return markTables{punct: raw.Punctuation, fullWidth: byRune(raw.FullWidth), sentenceEnd: raw.SentenceEnd, strokes: byRune(raw.Strokes),
		variants: lists(raw.Variants), variantsZh: lists(raw.VariantsZh)}
}

// Variants are what key r types with Chr held, in turn, as on Nokia's
// QWERTY phones (e: é è ê ë ...); zh (pinyin mode) puts a mark's Chinese
// forms first. A capital's variants are capitals; nil for a key without.
func Variants(r rune, zh bool) []string {
	lower := unicode.ToLower(r)
	var out []string
	if zh {
		out = append(out, marks.variantsZh[lower]...)
	}
	for _, v := range marks.variants[lower] {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	if r == lower {
		return out
	}
	upper := make([]string, len(out))
	for i, v := range out {
		rs := []rune(v)
		for j := range rs {
			rs[j] = unicode.ToUpper(rs[j])
		}
		upper[i] = string(rs)
	}
	return upper
}
