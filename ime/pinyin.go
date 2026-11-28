package ime

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// surnames.json: how a surname reads where its character reads otherwise
// in words (a polyphone: U+5355 is dan in words, shan as a surname), and
// the compound surnames.
//
//go:embed data/surnames.json
var surnameData []byte

var surnames = sync.OnceValue(func() map[string]string {
	m := map[string]string{}
	_ = json.Unmarshal(surnameData, &m)
	return m
})

// maxWord is the longest dictionary word a name is matched against.
const maxWord = 4

// Pinyin spells a name for search on a keypad: lower-case words separated
// by spaces, a Chinese character's pinyin syllable each, a word in other
// letters (accents dropped) and digits as itself. The first characters read
// as a surname reads, the rest as the dictionary's longest words, then
// each character's most frequent reading; what it cannot spell is left out.
func Pinyin(name string) string {
	var out []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			out = append(out, word.String())
			word.Reset()
		}
	}
	rs := []rune(name)
	start := true
	for i := 0; i < len(rs); {
		r := rs[i]
		if !unicode.Is(unicode.Han, r) {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				for _, f := range norm.NFD.String(string(unicode.ToLower(r))) {
					if f >= 'a' && f <= 'z' || f >= '0' && f <= '9' {
						word.WriteRune(f)
					}
				}
			} else {
				flush()
			}
			i++
			continue
		}
		flush()
		n, syl := 0, ""
		if start {
			n, syl = surname(rs[i:])
		}
		if n == 0 {
			n, syl = longestWord(rs[i:])
		}
		start = false
		if syl != "" {
			out = append(out, syl)
		}
		i += max(n, 1)
	}
	flush()
	return strings.Join(out, " ")
}

func surname(rs []rune) (int, string) {
	for n := min(2, len(rs)); n > 0; n-- {
		if syl, ok := surnames()[string(rs[:n])]; ok {
			return n, syl
		}
	}
	return 0, ""
}

// longestWord reads the longest run of Chinese characters at the start of
// rs the dictionary has, by its most frequent reading.
func longestWord(rs []rune) (int, string) {
	d := load()
	n := 0
	for n < len(rs) && n < maxWord && unicode.Is(unicode.Han, rs[n]) {
		n++
	}
	for ; n > 0; n-- {
		for _, id := range d.exact(secWord, string(rs[:n])) {
			if p := d.zhPinyin(id); len(strings.Fields(p)) == n {
				return n, p
			}
		}
	}
	return 1, ""
}
