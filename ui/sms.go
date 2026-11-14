package ui

import "unicode/utf16"

// gsm7 is the GSM 03.38 default alphabet; gsm7Ext its extension table,
// whose characters take two septets.
const (
	gsm7    = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"
	gsm7Ext = "^{}\\[~]|€\f"
)

// SMSSegments is how a text goes over the air: in how many parts, how many
// characters the last part still has room for, and whether it needs UCS-2
// (any character outside the GSM 7-bit alphabet: 70 a part, 67 when split)
// instead of GSM 7-bit (160, 153 when split).
func SMSSegments(text string) (parts, left int, ucs2 bool) {
	septets := 0
	for _, r := range text {
		switch {
		case containsRune(gsm7, r):
			septets++
		case containsRune(gsm7Ext, r):
			septets += 2
		default:
			ucs2 = true
		}
	}
	single, multi, n := 160, 153, septets
	if ucs2 {
		single, multi, n = 70, 67, len(utf16.Encode([]rune(text)))
	}
	switch {
	case n == 0:
		return 0, single, ucs2
	case n <= single:
		return 1, single - n, ucs2
	}
	parts = (n + multi - 1) / multi
	return parts, parts*multi - n, ucs2
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
