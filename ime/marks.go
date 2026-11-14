package ime

import (
	_ "embed"
	"encoding/json"
	"unicode/utf8"
)

// marks.json: the punctuation the Chinese modes type and the glyphs that
// show a stroke code.
//
//go:embed data/marks.json
var marksData []byte

var marks = loadMarks()

type markTables struct {
	punct       []string        // the 9-key pad's 1 key lists these
	fullWidth   map[rune]string // a keyboard's marks typed in pinyin mode
	sentenceEnd string          // marks after which Abc capitalises
	strokes     map[rune]string // by stroke key: 1-5 the classes, 6 any stroke
}

func loadMarks() markTables {
	var raw struct {
		Punctuation []string          `json:"punctuation"`
		FullWidth   map[string]string `json:"full_width"`
		SentenceEnd string            `json:"sentence_end"`
		Strokes     map[string]string `json:"strokes"`
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
	return markTables{punct: raw.Punctuation, fullWidth: byRune(raw.FullWidth), sentenceEnd: raw.SentenceEnd, strokes: byRune(raw.Strokes)}
}
