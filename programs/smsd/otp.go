package smsd

import (
	_ "embed"
	"encoding/json"
	"strings"
	"unicode"
)

// Verification codes: a run of 4-8 digits (or two runs split by one space
// or dash, "123 456") near a word that announces a code, the nearest one
// winning. The words, and the marks that make digits an amount, a date or
// a quantity rather than a code, are in the languages messages arrive in
// (Chinese among them): data, in otp.json.
//
//go:embed otp.json
var otpJSON []byte

type otpWords struct {
	Keywords []string `json:"keywords"`
	Before   []string `json:"before"` // just before the digits: a currency, a number sign
	After    []string `json:"after"`  // just after: a unit (money, time, data), a date
}

var otp = func() otpWords {
	var w otpWords
	if err := json.Unmarshal(otpJSON, &w); err != nil {
		panic("otp.json: " + err.Error())
	}
	return w
}()

// maxCodeDistance: characters between the code and its word, at most.
const maxCodeDistance = 40

func lowerRunes(s string) []rune {
	rs := []rune(s)
	for i, r := range rs {
		rs[i] = unicode.ToLower(r)
	}
	return rs
}

func isASCIILetter(r rune) bool { return r < 128 && unicode.IsLetter(r) }

// at reports whether w (lower case) is at i in rs; a word of ASCII letters
// must stand alone ("code" not in "barcode").
func at(rs []rune, i int, w []rune) bool {
	if i < 0 || i+len(w) > len(rs) {
		return false
	}
	for k, r := range w {
		if rs[i+k] != r {
			return false
		}
	}
	if isASCIILetter(w[0]) && i > 0 && isASCIILetter(rs[i-1]) {
		return false
	}
	if isASCIILetter(w[len(w)-1]) && i+len(w) < len(rs) && isASCIILetter(rs[i+len(w)]) {
		return false
	}
	return true
}

func endsWith(rs []rune, end int, w []rune) bool { return at(rs, end-len(w), w) }

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// findCode is the verification code in a message, "" for none.
func findCode(text string) string {
	rs := lowerRunes(text)
	type span struct{ from, to int }
	var words []span
	for _, k := range otp.Keywords {
		w := []rune(strings.ToLower(k))
		for i := range rs {
			if at(rs, i, w) {
				words = append(words, span{i, i + len(w)})
			}
		}
	}
	if len(words) == 0 {
		return ""
	}
	best, bestDist := "", maxCodeDistance+1
	for i := 0; i < len(rs); {
		if !isDigit(rs[i]) {
			i++
			continue
		}
		from := i
		code := ""
		for i < len(rs) {
			j := i
			for j < len(rs) && isDigit(rs[j]) {
				j++
			}
			code += string(rs[i:j])
			i = j
			if i+1 < len(rs) && (rs[i] == ' ' || rs[i] == '-') && isDigit(rs[i+1]) {
				i++
				continue
			}
			break
		}
		to := i
		if !plausible(rs, from, to, len(code)) {
			continue
		}
		for _, w := range words {
			d := from - w.to
			if w.from >= to {
				d = w.from - to + 1 // a word after its code counts one further
			}
			if d >= 0 && d < bestDist {
				best, bestDist = code, d
			}
		}
	}
	return best
}

// plausible: rs[from:to] (n digits) is not a number of another kind.
func plausible(rs []rune, from, to, n int) bool {
	if n < 4 || n > 8 {
		return false
	}
	if from > 0 && (rs[from-1] == '.' || rs[from-1] == ',' || rs[from-1] == ':' || isASCIILetter(rs[from-1])) {
		return false
	}
	if to < len(rs) && (rs[to] == ':' || isASCIILetter(rs[to]) && !isUnitStart(rs, to)) {
		return false
	}
	if to+1 < len(rs) && (rs[to] == '.' || rs[to] == ',') && isDigit(rs[to+1]) {
		return false
	}
	for _, b := range otp.Before {
		if endsWith(rs, skipSpace(rs, from), []rune(b)) {
			return false
		}
	}
	for _, a := range otp.After {
		if at(rs, skipSpaceAfter(rs, to), []rune(a)) {
			return false
		}
	}
	return true
}

// isUnitStart: letters right after digits are a unit ("10gb"), which the
// After list rules out; any other letters make the digits part of a word.
func isUnitStart(rs []rune, i int) bool {
	for _, a := range otp.After {
		if w := []rune(a); isASCIILetter(w[0]) && at(rs, i, w) {
			return true
		}
	}
	return false
}

func skipSpace(rs []rune, from int) int {
	for from > 0 && rs[from-1] == ' ' {
		from--
	}
	return from
}

func skipSpaceAfter(rs []rune, to int) int {
	for to < len(rs) && rs[to] == ' ' {
		to++
	}
	return to
}
