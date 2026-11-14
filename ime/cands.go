package ime

import (
	"sort"
	"strings"
)

// Candidate is one choice in the candidate bar. Consumed is how many input
// units (letters or digits) it covers; the rest keeps composing.
type Candidate struct {
	Text     string
	Pinyin   string
	Consumed int
}

type token struct {
	text string
	end  int // offset in the input just after this token
}

// segment splits input (letters, or digits when t9) into syllables; only the
// last token may be an unfinished syllable. Apostrophes force a boundary.
func (d *dicts) segment(s string, t9 bool) [][]token {
	full := func(t string) bool {
		if t9 {
			return d.codeFull[t]
		}
		return d.syllable[t]
	}
	prefix := func(t string) bool {
		if t9 {
			return len(d.codePre[t]) > 0
		}
		return len(d.sylPre[t]) > 0
	}
	memo := map[int][][]token{}
	var seg func(i int) [][]token
	seg = func(i int) [][]token {
		for i < len(s) && s[i] == '\'' {
			i++
		}
		if i == len(s) {
			return [][]token{nil}
		}
		if r, ok := memo[i]; ok {
			return r
		}
		var out [][]token
		end := i
		for end < len(s) && s[end] != '\'' {
			end++
		}
		for j := min(end, i+6); j > i; j-- {
			t := s[i:j]
			if j == len(s) && prefix(t) {
				out = append(out, []token{{t, j}})
				continue
			}
			if !full(t) {
				continue
			}
			for _, rest := range seg(j) {
				out = append(out, append([]token{{t, j}}, rest...))
				if len(out) >= 16 {
					break
				}
			}
		}
		sort.SliceStable(out, func(a, b int) bool { return len(out[a]) < len(out[b]) })
		memo[i] = out
		return out
	}
	var res [][]token
	for _, sg := range seg(0) {
		if sg != nil {
			res = append(res, sg)
		}
	}
	return res
}

// lookup finds words for complete tokens plus an expanded final token;
// fuzzy also tries the fuzzy-pinyin variants of the complete syllables.
func (d *dicts) lookup(toks []token, t9, lastPartial, fuzzy bool) []int32 {
	index, expand := secKey, d.sylPre
	if t9 {
		index, expand = secT9, d.codePre
	}
	head := make([]string, len(toks)-1)
	for i, t := range toks[:len(toks)-1] {
		head[i] = t.text
	}
	prefixes := []string{strings.Join(head, " ")}
	last := toks[len(toks)-1].text
	finals := []string{last}
	if lastPartial {
		finals = expand[last]
	}
	if fuzzy && !t9 {
		prefixes = d.fuzzyKeys(head, 16)
		finals = d.variants(last)
		if lastPartial {
			// An unfinished syllable: swap its initial, then expand.
			finals = nil
			for _, s := range append(initialSwaps(last), d.variants(last)...) {
				finals = append(finals, expand[s]...)
			}
		}
	}
	var out []int32
	seen := map[int32]bool{}
	for _, p := range prefixes {
		if p != "" {
			p += " "
		}
		for _, f := range finals {
			for _, id := range d.exact(index, p+f) {
				if !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}

func (d *dicts) zhCandidates(s string, t9 bool, spelling string, fuzzy bool) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	add := func(ids []int32, consumed, limit int) {
		n := 0
		for _, id := range ids {
			word := d.zhWord(id)
			py := strings.ReplaceAll(d.zhPinyin(id), " ", "'")
			if spelling != "" && !strings.HasPrefix(py, spelling) && !strings.HasPrefix(spelling, py) {
				continue
			}
			if seen[word] {
				continue
			}
			seen[word] = true
			out = append(out, Candidate{Text: word, Pinyin: py, Consumed: consumed})
			if n++; n >= limit {
				return
			}
		}
	}
	segs := d.segment(s, t9)
	for _, sg := range segs {
		add(d.lookup(sg, t9, true, fuzzy), len(s), 24)
	}
	if !t9 && !strings.Contains(s, "'") {
		if inits := splitInitials(s); len(inits) >= 2 {
			add(d.exact(secInit, strings.Join(inits, " ")), len(s), 8)
		}
	}
	if len(segs) > 0 {
		best := segs[0]
		for k := len(best) - 1; k >= 1; k-- {
			add(d.lookup(best[:k], t9, false, fuzzy), best[k-1].end, 12)
		}
		add(d.lookup(best[:1], t9, len(best) == 1, fuzzy), best[0].end, 12)
	}
	return out
}

// splitInitials reads "zgr" or "zhgr" as initials z g r (an abbreviation of
// zhongguoren); a vowel after the first letter means it is not an
// abbreviation.
func splitInitials(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if i+1 < len(s) && strings.Contains("zcs", s[i:i+1]) && s[i+1] == 'h' {
			out = append(out, s[i:i+1])
			i++
			continue
		}
		if strings.Contains("aoeiuv", s[i:i+1]) && len(out) > 0 {
			return nil
		}
		out = append(out, s[i:i+1])
	}
	return out
}

// spellings lists distinct pinyin readings of the best candidates (for the
// 9-key spelling row).
func spellings(cs []Candidate, n int) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range cs {
		if c.Pinyin == "" || seen[c.Pinyin] {
			continue
		}
		seen[c.Pinyin] = true
		out = append(out, c.Pinyin)
		if len(out) == n {
			break
		}
	}
	return out
}

func (d *dicts) enCandidates(s string, t9 bool) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	push := func(w string) {
		if !seen[strings.ToLower(w)] {
			seen[strings.ToLower(w)] = true
			out = append(out, Candidate{Text: w, Consumed: len(s)})
		}
	}
	if t9 {
		exact := d.prefixRange(secEnT9, s, 6, func(e enEntry) bool { return len(e.t9) == len(s) })
		longer := d.prefixRange(secEnT9, s, 6, func(e enEntry) bool { return len(e.t9) > len(s) })
		for _, i := range append(exact, longer...) {
			push(d.en(i).word)
		}
		if len(out) == 0 {
			push(s)
		}
		return out
	}
	push(s)
	for _, i := range d.prefixRange(secEnLower, strings.ToLower(s), 8, nil) {
		push(d.en(i).word)
	}
	return out
}

// association suggests continuations after committing text: words that
// start with the committed text, else with its last character.
func (d *dicts) association(committed string) []Candidate {
	rs := []rune(committed)
	if len(rs) == 0 {
		return nil
	}
	for _, try := range []string{committed, string(rs[len(rs)-1:])} {
		var ids []int32
		d.prefix(secWord, try, func(id int32, word string) bool {
			if word != try {
				ids = append(ids, id)
			}
			return true
		})
		sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
		var out []Candidate
		seen := map[string]bool{}
		for _, id := range ids {
			rest := strings.TrimPrefix(d.zhWord(id), try)
			if !seen[rest] {
				seen[rest] = true
				out = append(out, Candidate{Text: rest})
			}
			if len(out) == 10 {
				break
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// Stroke input: 1 horizontal, 2 vertical, 3 left-falling, 4 dot, 5 turning,
// in writing order, 6 any one stroke. Characters whose stroke code starts
// with the input, most frequent first.
const strokeAny = '6'

// StrokeGlyphs shows a stroke code as the strokes themselves.
func StrokeGlyphs(code string) string {
	var b strings.Builder
	for _, r := range code {
		b.WriteString(marks.strokes[r])
	}
	return b.String()
}

func strokeMatch(key, code string) bool {
	if len(key) < len(code) {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] != strokeAny && code[i] != key[i] {
			return false
		}
	}
	return true
}

func (d *dicts) strokeCandidates(code string, limit int) []Candidate {
	type hit struct {
		id    int32
		whole bool
	}
	var hits []hit
	// Only the part before the first wildcard narrows the search.
	lead := code
	if wild := strings.IndexByte(code, strokeAny); wild >= 0 {
		lead = code[:wild]
	}
	d.prefix(secStroke, lead, func(id int32, key string) bool {
		if strokeMatch(key, code) {
			hits = append(hits, hit{id, len(key) == len(code)})
		}
		return true
	})
	// A character whose strokes are all typed comes first, then by
	// frequency (zh ids are frequency ranks).
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].whole != hits[b].whole {
			return hits[a].whole
		}
		return hits[a].id < hits[b].id
	})
	out := make([]Candidate, 0, min(limit, len(hits)))
	seen := map[int32]bool{}
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		if seen[h.id] {
			continue
		}
		seen[h.id] = true
		out = append(out, Candidate{Text: d.zhWord(h.id), Consumed: len(code)})
	}
	return out
}
