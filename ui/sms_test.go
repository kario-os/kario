package ui

import (
	"strings"
	"testing"
)

func TestSMSSegments(t *testing.T) {
	for _, c := range []struct {
		text        string
		parts, left int
		ucs2        bool
	}{
		{"", 0, 160, false},
		{"hello", 1, 155, false},
		{strings.Repeat("a", 160), 1, 0, false},
		{strings.Repeat("a", 161), 2, 145, false},
		{strings.Repeat("a", 306), 2, 0, false},
		{"{}", 1, 156, false},
		{"\u4f60\u597d", 1, 68, true},
		{strings.Repeat("\u4f60", 70), 1, 0, true},
		{strings.Repeat("\u4f60", 71), 2, 63, true},
		{"caf\u00e9 \U0001F600", 1, 63, true},
	} {
		parts, left, ucs2 := SMSSegments(c.text)
		if parts != c.parts || left != c.left || ucs2 != c.ucs2 {
			t.Errorf("SMSSegments(%q) = %d, %d, %v; want %d, %d, %v", c.text, parts, left, ucs2, c.parts, c.left, c.ucs2)
		}
	}
}
