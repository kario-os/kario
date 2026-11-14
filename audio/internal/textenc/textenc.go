// Package textenc turns the legacy text of music files into UTF-8: cue
// sheets and ID3 tags written by Chinese software are often GBK, and old
// Windows rippers write UTF-16 with a byte order mark.
package textenc

import (
	"bytes"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

// Decode reads b as UTF-8 or UTF-16 when a byte order mark says so, UTF-8
// when it is valid UTF-8, and GBK (GB 18030) otherwise; Latin-1 is the
// last resort for bytes that are not GBK either.
func Decode(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xef, 0xbb, 0xbf}):
		return string(b[3:])
	case bytes.HasPrefix(b, []byte{0xff, 0xfe}):
		return UTF16(b[2:], false)
	case bytes.HasPrefix(b, []byte{0xfe, 0xff}):
		return UTF16(b[2:], true)
	case utf8.Valid(b):
		return string(b)
	}
	if s, ok := GBK(b); ok {
		return s
	}
	return Latin1(b)
}

// Legacy reads text a format declares as Latin-1 (ID3's encoding 0),
// which Chinese taggers fill with GBK or UTF-8 anyway: pure ASCII and true
// Latin-1 stay as they are.
func Legacy(b []byte) string {
	ascii := true
	for _, c := range b {
		if c >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return string(b)
	}
	if utf8.Valid(b) {
		return string(b)
	}
	if s, ok := GBK(b); ok && cjk(s) {
		return s
	}
	return Latin1(b)
}

// GBK decodes GB 18030 (a superset of GBK and GB 2312); ok is false when
// b is not that.
func GBK(b []byte) (string, bool) {
	out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b)
	if err != nil || bytes.ContainsRune(out, utf8.RuneError) {
		return "", false
	}
	return string(out), true
}

// cjk: most of the non-ASCII letters are Han, kana or Hangul (what a GBK
// reading of real text gives; Latin-1 read as GBK gives rare Han mixed
// with symbols).
func cjk(s string) bool {
	han, other := 0, 0
	for _, r := range s {
		switch {
		case r < 0x80:
		case unicode.Is(unicode.Han, r) || unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Hangul) || r >= 0x3000 && r <= 0x303f || r >= 0xff00 && r <= 0xffef:
			han++
		default:
			other++
		}
	}
	return han > 0 && other*4 <= han
}

func Latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

// The character sets SACD text declares (Scarlet Book codes).
const (
	ISO646    = 1
	ISO8859_1 = 2
	ShiftJIS  = 3
	KSC5601   = 4
	GB2312    = 5
	Big5      = 6
)

// Charset decodes text in one of the SACD character sets; an unknown
// code is read as Decode would.
func Charset(b []byte, code int) string {
	var dec *encoding.Decoder
	switch code {
	case ISO646:
		return string(b)
	case ISO8859_1, 7:
		return Latin1(b)
	case ShiftJIS:
		dec = japanese.ShiftJIS.NewDecoder()
	case KSC5601:
		dec = korean.EUCKR.NewDecoder()
	case GB2312:
		dec = simplifiedchinese.GBK.NewDecoder()
	case Big5:
		dec = traditionalchinese.Big5.NewDecoder()
	default:
		return Decode(b)
	}
	out, err := dec.Bytes(b)
	if err != nil {
		return Decode(b)
	}
	return string(out)
}

// UTF16 decodes UTF-16 without its byte order mark.
func UTF16(b []byte, bigEndian bool) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		if bigEndian {
			u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			u[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	return string(utf16.Decode(u))
}
