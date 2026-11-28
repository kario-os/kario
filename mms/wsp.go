// Package mms reads and writes multimedia messages: the WAP push that
// announces one in an SMS (WAP-230 WSP, WAP-251 Push), the MMS PDUs of
// OMA-MMS-ENC 1.2 / 1.3 (headers, multipart bodies) exchanged with the
// operator's MMSC over HTTP, the SMIL that lays a message out, and a WAP
// Push service indication (WBXML). Everything read here comes from anyone
// who can send an SMS or answer an HTTP request: lengths are checked
// against the input, counts are bounded, and a malformed PDU is an error.
// FakeMMSC stands in for the operator in tests and the simulated baseband.
package mms

import (
	"errors"
	"strconv"
	"strings"
)

var errShort = errors.New("mms: truncated")

// reader walks WSP-encoded bytes; after the first error every read gives
// zero values and err keeps that error.
type reader struct {
	b   []byte
	i   int
	err error
}

func (r *reader) fail(err error) {
	if r.err == nil {
		r.err = err
	}
	r.i = len(r.b)
}

func (r *reader) left() int { return len(r.b) - r.i }

func (r *reader) peek() byte {
	if r.i >= len(r.b) {
		r.fail(errShort)
		return 0
	}
	return r.b[r.i]
}

func (r *reader) byte() byte {
	c := r.peek()
	if r.err == nil {
		r.i++
	}
	return c
}

func (r *reader) take(n int) []byte {
	if n < 0 || n > r.left() {
		r.fail(errShort)
		return nil
	}
	out := r.b[r.i : r.i+n]
	r.i += n
	return out
}

// uintvar: 7 bits an octet, high bit set on all but the last; at most 5
// octets (32 bits).
func (r *reader) uintvar() int {
	var v uint64
	for n := 0; n < 5; n++ {
		c := r.byte()
		v = v<<7 | uint64(c&0x7f)
		if c&0x80 == 0 {
			if v > 1<<31-1 {
				r.fail(errors.New("mms: uintvar too large"))
				return 0
			}
			return int(v)
		}
	}
	r.fail(errors.New("mms: uintvar too long"))
	return 0
}

// valueLength: a short length (0-30) or 31 and a uintvar.
func (r *reader) valueLength() int {
	c := r.byte()
	switch {
	case c <= 30:
		return int(c)
	case c == 31:
		return r.uintvar()
	}
	r.fail(errors.New("mms: not a value length"))
	return 0
}

// text: a text string, its leading quote (127, before octets above 127)
// dropped, up to and without the end-of-string NUL.
func (r *reader) text() string {
	if r.peek() == 127 {
		r.i++
	}
	if r.err != nil {
		return ""
	}
	end := r.i
	for end < len(r.b) && r.b[end] != 0 {
		end++
	}
	if end == len(r.b) {
		r.fail(errShort)
		return ""
	}
	s := string(r.b[r.i:end])
	r.i = end + 1
	return s
}

// integer: a short integer (one octet, high bit set) or a long one (a
// short length and up to 8 octets).
func (r *reader) integer() uint64 {
	c := r.peek()
	if c&0x80 != 0 {
		r.i++
		return uint64(c & 0x7f)
	}
	n := int(r.byte())
	if n > 8 || n == 0 {
		r.fail(errors.New("mms: bad long integer"))
		return 0
	}
	var v uint64
	for _, b := range r.take(n) {
		v = v<<8 | uint64(b)
	}
	return v
}

// skipValue passes any header value by the WSP rule: one octet above
// 127, a value length and that many octets, or a text string.
func (r *reader) skipValue() {
	c := r.peek()
	switch {
	case c >= 0x80:
		r.i++
	case c <= 31:
		r.take(r.valueLength())
	default:
		r.text()
	}
}

// Well-known media types (WSP Table 40), by their number.
var wellKnownMedia = []string{
	"*/*", "text/*", "text/html", "text/plain", "text/x-hdml", "text/x-ttml", "text/x-vCalendar", "text/x-vCard",
	"text/vnd.wap.wml", "text/vnd.wap.wmlscript", "text/vnd.wap.wta-event", "multipart/*", "multipart/mixed",
	"multipart/form-data", "multipart/byterantes", "multipart/alternative", "application/*", "application/java-vm",
	"application/x-www-form-urlencoded", "application/x-hdmlc", "application/vnd.wap.wmlc", "application/vnd.wap.wmlscriptc",
	"application/vnd.wap.wta-eventc", "application/vnd.wap.uaprof", "application/vnd.wap.wtls-ca-certificate",
	"application/vnd.wap.wtls-user-certificate", "application/x-x509-ca-cert", "application/x-x509-user-cert", "image/*",
	"image/gif", "image/jpeg", "image/tiff", "image/png", "image/vnd.wap.wbmp", "application/vnd.wap.multipart.*",
	"application/vnd.wap.multipart.mixed", "application/vnd.wap.multipart.form-data", "application/vnd.wap.multipart.byteranges",
	"application/vnd.wap.multipart.alternative", "application/xml", "text/xml", "application/vnd.wap.wbxml",
	"application/x-x968-cross-cert", "application/x-x968-ca-cert", "application/x-x968-user-cert", "text/vnd.wap.si",
	"application/vnd.wap.sic", "text/vnd.wap.sl", "application/vnd.wap.slc", "text/vnd.wap.co", "application/vnd.wap.coc",
	"application/vnd.wap.multipart.related", "application/vnd.wap.sia", "text/vnd.wap.connectivity-xml",
	"application/vnd.wap.connectivity-wbxml", "application/pkcs7-mime", "application/vnd.wap.hashed-certificate",
	"application/vnd.wap.signed-certificate", "application/vnd.wap.cert-response", "application/xhtml+xml",
	"application/wml+xml", "text/css", "application/vnd.wap.mms-message", "application/vnd.wap.rollover-certificate",
	"application/vnd.wap.locc+wbxml", "application/vnd.wap.loc+xml",
}

// Media types this package names.
const (
	TypeMMS          = "application/vnd.wap.mms-message"
	TypeSIC          = "application/vnd.wap.sic"
	TypeRelated      = "application/vnd.wap.multipart.related"
	TypeMixed        = "application/vnd.wap.multipart.mixed"
	TypeAlternative  = "application/vnd.wap.multipart.alternative"
	TypeSMIL         = "application/smil"
	TypeText         = "text/plain"
	TypeJPEG         = "image/jpeg"
	TypePNG          = "image/png"
	TypeGIF          = "image/gif"
	appIDMMS         = 4 // x-wap-application:mms.ua
	wspPush          = 0x06
	wspConfirmedPush = 0x07
)

func mediaName(n uint64) string {
	if n < uint64(len(wellKnownMedia)) {
		return wellKnownMedia[n]
	}
	return ""
}

func mediaNumber(t string) (int, bool) {
	for i, m := range wellKnownMedia {
		if strings.EqualFold(m, t) {
			return i, true
		}
	}
	return 0, false
}

// Character sets (IANA MIBenum) MMS text comes in.
var charsets = map[uint64]string{3: "us-ascii", 4: "iso-8859-1", 17: "shift_jis", 38: "euc-kr", 106: "utf-8", 113: "gbk",
	114: "gb18030", 1000: "iso-10646-ucs-2", 1013: "utf-16be", 1014: "utf-16le", 1015: "utf-16", 2025: "gb2312", 2026: "big5"}

// contentType is a Content-type-value: the media type and its parameters
// (charset by name, name, start, type), lower-case keys.
type contentType struct {
	Media  string
	Params map[string]string
}

func (r *reader) contentType() contentType {
	ct := contentType{Params: map[string]string{}}
	c := r.peek()
	switch {
	case c >= 0x80:
		ct.Media = mediaName(uint64(r.byte() & 0x7f))
		return ct
	case c > 31:
		ct.Media = r.text()
		return ct
	}
	n := r.valueLength()
	sub := reader{b: r.take(n)}
	if r.err != nil {
		return ct
	}
	if c := sub.peek(); c >= 0x80 || c <= 30 {
		ct.Media = mediaName(sub.integer())
	} else {
		ct.Media = sub.text()
	}
	for sub.err == nil && sub.left() > 0 {
		sub.parameter(ct.Params)
	}
	if sub.err != nil {
		r.fail(sub.err)
	}
	return ct
}

// parameter reads one content-type parameter into p; the ones this package
// does not use are passed over.
func (r *reader) parameter(p map[string]string) {
	c := r.peek()
	if c < 0x80 && c > 31 {
		name := strings.ToLower(r.text())
		if c := r.peek(); c != 0 && (c >= 0x80 || c <= 30) {
			p[name] = strconv.FormatUint(r.integer(), 10)
			return
		}
		p[name] = r.textValue()
		return
	}
	switch r.integer() {
	case 0x01: // Charset
		if r.peek() == 0x80 {
			r.i++
			return
		}
		p["charset"] = charsets[r.integer()]
	case 0x03, 0x09: // Type (multipart/related)
		if c := r.peek(); c >= 0x80 || c <= 30 {
			p["type"] = mediaName(r.integer())
		} else {
			p["type"] = r.text()
		}
	case 0x05, 0x17: // Name
		p["name"] = r.textValue()
	case 0x06, 0x18: // Filename
		p["filename"] = r.textValue()
	case 0x0A, 0x19: // Start
		p["start"] = r.textValue()
	default:
		r.skipValue()
	}
}

// textValue: no value (NUL), a token or a quoted string.
func (r *reader) textValue() string {
	if r.peek() == 0 {
		r.i++
		return ""
	}
	if r.peek() == '"' {
		r.i++
	}
	return r.text()
}

// writer builds WSP-encoded bytes.
type writer struct{ b []byte }

func (w *writer) byte(c byte) { w.b = append(w.b, c) }

func (w *writer) uintvar(v int) {
	var tmp [5]byte
	i := len(tmp) - 1
	tmp[i] = byte(v & 0x7f)
	for v >>= 7; v > 0; v >>= 7 {
		i--
		tmp[i] = byte(v&0x7f) | 0x80
	}
	w.b = append(w.b, tmp[i:]...)
}

func (w *writer) valueLength(n int) {
	if n <= 30 {
		w.byte(byte(n))
		return
	}
	w.byte(31)
	w.uintvar(n)
}

// text quotes a first octet above 127 as WSP does, and 127 itself or a
// control character too (they would read as the quote, a value length).
func (w *writer) text(s string) {
	if s != "" && (s[0] >= 0x7f || s[0] < 0x20) {
		w.byte(127)
	}
	w.b = append(w.b, strings.ReplaceAll(s, "\x00", "")...)
	w.byte(0)
}

func (w *writer) quoted(s string) {
	w.byte('"')
	w.text(s)
}

func (w *writer) longInt(v uint64) {
	var tmp []byte
	for ; v > 0; v >>= 8 {
		tmp = append([]byte{byte(v)}, tmp...)
	}
	if len(tmp) == 0 {
		tmp = []byte{0}
	}
	w.byte(byte(len(tmp)))
	w.b = append(w.b, tmp...)
}

func (w *writer) integer(v uint64) {
	if v < 0x80 {
		w.byte(byte(v) | 0x80)
		return
	}
	w.longInt(v)
}

// contentType writes a media type with its parameters (charset utf-8,
// name, start, type).
func (w *writer) contentType(media string, params map[string]string) {
	var inner writer
	if n, ok := mediaNumber(media); ok {
		inner.integer(uint64(n))
	} else {
		inner.text(media)
	}
	if len(params) == 0 {
		w.b = append(w.b, inner.b...)
		return
	}
	if params["charset"] == "utf-8" {
		inner.byte(0x81)
		inner.integer(106)
	}
	if v := params["type"]; v != "" {
		inner.byte(0x89)
		if n, ok := mediaNumber(v); ok {
			inner.integer(uint64(n))
		} else {
			inner.text(v)
		}
	}
	if v := params["start"]; v != "" {
		inner.byte(0x8A)
		inner.text(v)
	}
	if v := params["name"]; v != "" {
		inner.byte(0x85)
		inner.text(v)
	}
	w.valueLength(len(inner.b))
	w.b = append(w.b, inner.b...)
}
