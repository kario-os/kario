package ime

import (
	"slices"
	"strings"
)

// Fuzzy pinyin for the full keyboard: speakers who merge z/zh,
// c/ch, s/sh, n/l or an/ang, en/eng, in/ing find their words whichever
// they type. The 9-key pad has no such variants: there the digit codes of
// z and zh differ in length.

var fuzzyFinals = [][2]string{{"ang", "an"}, {"eng", "en"}, {"ing", "in"}}

// variants are the valid syllables a syllable may stand for, itself first.
func (d *dicts) variants(syl string) []string {
	out := []string{syl}
	add := func(s string) {
		if d.syllable[s] && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	heads := []string{syl}
	switch {
	case strings.HasPrefix(syl, "zh"), strings.HasPrefix(syl, "ch"), strings.HasPrefix(syl, "sh"):
		heads = append(heads, syl[:1]+syl[2:])
	case syl[0] == 'z', syl[0] == 'c', syl[0] == 's':
		heads = append(heads, syl[:1]+"h"+syl[1:])
	}
	switch syl[0] {
	case 'n':
		heads = append(heads, "l"+syl[1:])
	case 'l':
		heads = append(heads, "n"+syl[1:])
	}
	for _, h := range heads {
		add(h)
		for _, f := range fuzzyFinals {
			switch {
			case strings.HasSuffix(h, f[0]):
				add(strings.TrimSuffix(h, f[0]) + f[1])
			case strings.HasSuffix(h, f[1]):
				add(strings.TrimSuffix(h, f[1]) + f[0])
			}
		}
	}
	return out
}

// initialSwaps is a (possibly unfinished) syllable with its initial swapped
// for the fuzzy partner, itself first.
func initialSwaps(s string) []string {
	out := []string{s}
	switch {
	case strings.HasPrefix(s, "zh"), strings.HasPrefix(s, "ch"), strings.HasPrefix(s, "sh"):
		out = append(out, s[:1]+s[2:])
	case s != "" && strings.ContainsRune("zcs", rune(s[0])):
		out = append(out, s[:1]+"h"+s[1:])
	}
	if s != "" && s[0] == 'n' {
		out = append(out, "l"+s[1:])
	} else if s != "" && s[0] == 'l' {
		out = append(out, "n"+s[1:])
	}
	return out
}

// fuzzyKeys are the index keys a run of full syllables may stand for, at
// most limit of them.
func (d *dicts) fuzzyKeys(syls []string, limit int) []string {
	keys := []string{""}
	for _, s := range syls {
		var next []string
		for _, k := range keys {
			for _, v := range d.variants(s) {
				if len(next) >= limit {
					break
				}
				if k == "" {
					next = append(next, v)
				} else {
					next = append(next, k+" "+v)
				}
			}
		}
		keys = next
	}
	return keys
}
