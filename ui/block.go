package ui

import (
	"strings"
	"unicode"

	"github.com/kario-os/kario/phonenum"
)

// BlockList is the system's block list for calls and messages (block.list,
// 32-messaging.md §6): numbers, and prefixes ending in "*" ("1065*": every
// number that starts so); Hidden blocks callers who withhold their number,
// Unknown every number that is not a contact's (short codes and senders
// with a name too). A blocked call is declined without ringing and logged
// as blocked; a blocked message goes to the blocked folder without a
// notification. Words block messages alone: one whose text holds a word
// (BlockedText) goes to the blocked folder whoever sent it.
type BlockList struct {
	Numbers []string `json:"numbers"`
	Hidden  bool     `json:"hidden,omitempty"`
	Unknown bool     `json:"unknown,omitempty"`
	Words   []string `json:"words,omitempty"`
}

// BlockSetArgs: block.set blocks (On) or unblocks Number (a prefix ends in
// "*") and Word, and sets Hidden or Unknown when given.
type BlockSetArgs struct {
	Number  string `json:"number,omitempty"`
	Word    string `json:"word,omitempty"`
	On      bool   `json:"on,omitempty"`
	Hidden  *bool  `json:"hidden,omitempty"`
	Unknown *bool  `json:"unknown,omitempty"`
}

// Why BlockList.Blocked blocks a number, and BlockedWord a message for
// its text.
const (
	BlockedNumber  = "number"
	BlockedPrefix  = "prefix"
	BlockedHidden  = "hidden"
	BlockedUnknown = "unknown"
	// BlockedMinor: minor mode allows only the contacts a parent chose.
	BlockedMinor = "minor"
	BlockedWord  = "word"
)

// MaxBlockWords: words on the list; MaxBlockWord: characters in one.
const (
	MaxBlockWords = 200
	MaxBlockWord  = 40
)

// WordKey is a word or a text as words are matched: letters and digits
// alone, lower-cased, full-width forms as their ASCII, so that "Free
// Gift", "free-gift" and the full-width "FREE gift" are one and spaces or
// marks put between the characters of a word do not hide it.
func WordKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0xFF01 && r <= 0xFF5E {
			r -= 0xFF01 - 0x21
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// BlockedText is the word of the list a message's text holds, "" for none.
func (b BlockList) BlockedText(text string) string {
	if len(b.Words) == 0 {
		return ""
	}
	t := WordKey(text)
	for _, w := range b.Words {
		if k := WordKey(w); k != "" && strings.Contains(t, k) {
			return w
		}
	}
	return ""
}

// hiddenWords are what networks put where a withheld number would be.
var hiddenWords = []string{"unknown", "private", "anonymous", "restricted", "withheld", "unavailable", "-1", "-2"}

// HiddenNumber: the caller withheld their number.
func HiddenNumber(n string) bool {
	n = strings.ToLower(strings.TrimSpace(n))
	if n == "" {
		return true
	}
	for _, w := range hiddenWords {
		if n == w {
			return true
		}
	}
	return false
}

// BlockEntry is how the list keeps a number (its phonenum.Key read in the
// region) or a prefix (its digits, a leading + kept, then "*"); "" for
// neither.
func BlockEntry(s, region string) string {
	s = strings.TrimSpace(s)
	if p, ok := strings.CutSuffix(s, "*"); ok {
		var b strings.Builder
		for i, r := range p {
			if (r >= '0' && r <= '9') || (r == '+' && i == 0) {
				b.WriteRune(r)
			}
		}
		if d := strings.TrimPrefix(b.String(), "+"); d == "" {
			return ""
		}
		return b.String() + "*"
	}
	return phonenum.Key(s, region)
}

// Blocked is why calls and messages from number (read in the region) are
// blocked, "" when they are not. known says whether a number is a
// contact's; with nil nothing counts as unknown (the contacts are not
// known yet).
func (b BlockList) Blocked(number, region string, known func(string) bool) string {
	if HiddenNumber(number) {
		if b.Hidden {
			return BlockedHidden
		}
		return ""
	}
	key := phonenum.Key(number, region)
	forms := numberForms(number, region)
	for _, e := range b.Numbers {
		if p, ok := strings.CutSuffix(e, "*"); ok {
			for _, f := range forms {
				if p != "" && strings.HasPrefix(f, p) {
					return BlockedPrefix
				}
			}
		} else if e == key {
			return BlockedNumber
		}
	}
	if b.Unknown && known != nil && !known(number) {
		return BlockedUnknown
	}
	return ""
}

// numberForms are the ways a prefix may match a number: E.164, the
// national number with and without its trunk prefix, the digits as
// written.
func numberForms(number, region string) []string {
	n := phonenum.Parse(number, region)
	if n.Alpha != "" {
		return nil
	}
	forms := []string{phonenum.Key(number, region), n.NSN}
	if r := phonenum.Lookup(region); r != nil && r.CC == n.CC && len(r.National) > 0 {
		forms = append(forms, r.National[0]+n.NSN)
	}
	var d strings.Builder
	for _, c := range number {
		if c >= '0' && c <= '9' || (c == '+' && d.Len() == 0) {
			d.WriteRune(c)
		}
	}
	return append(forms, d.String())
}
