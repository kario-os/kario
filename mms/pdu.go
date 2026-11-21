package mms

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
)

// X-Mms-Message-Type values (OMA-MMS-ENC §7.3.30).
const (
	SendReq         = 128
	SendConf        = 129
	NotificationInd = 130
	NotifyRespInd   = 131
	RetrieveConf    = 132
	AcknowledgeInd  = 133
	DeliveryInd     = 134
	ReadRecInd      = 135
	ReadOrigInd     = 136
)

// X-Mms-Status (notify response, delivery report), X-Mms-Response-Status
// and X-Mms-Retrieve-Status values this package names.
const (
	StatusExpired      = 128
	StatusRetrieved    = 129
	StatusRejected     = 130
	StatusDeferred     = 131
	StatusUnrecognised = 132
	StatusUnreachable  = 135
	ResponseOK         = 128
	RetrieveOK         = 128
	// Version13 is OMA MMS 1.3, the version this package writes.
	Version13 = 0x13
)

// Limits on what Decode accepts.
const (
	maxHeaders    = 256
	maxRecipients = 64
	MaxParts      = 64
)

// PDU is an MMS message or one of the exchanges about it. Times are Unix
// seconds; ExpiryAfter is a relative expiry (seconds after arrival).
// Addresses come without their /TYPE= suffix.
type PDU struct {
	Type            int
	TransactionID   string
	Version         int
	MessageID       string
	From            string
	To, Cc, Bcc     []string
	Subject         string
	Date            int64
	ContentLocation string
	Expiry          int64
	ExpiryAfter     int64
	Size            int64
	Class           string
	Status          int
	ResponseStatus  int
	ResponseText    string
	RetrieveStatus  int
	RetrieveText    string
	DeliveryReport  bool
	ReadReport      bool
	// ContentType is the body's media type, Start and Related its start
	// part and type (multipart/related); Parts its parts, Body the body
	// when it is not multipart.
	ContentType string
	Start       string
	Related     string
	Parts       []Part
	Body        []byte
}

// Part is a body part: its media type, character set (a MIME name), name
// (the name or filename parameter, or the Content-Disposition's),
// Content-ID (without the angle brackets) and Content-Location.
type Part struct {
	ContentType string
	Charset     string
	Name        string
	ContentID   string
	Location    string
	Data        []byte
}

// Text is a text part's content in UTF-8.
func (p Part) Text() string { return decodeText(p.Data, p.Charset) }

func decodeText(raw []byte, charset string) string {
	var enc encoding.Encoding
	switch strings.ToLower(charset) {
	case "", "utf-8", "us-ascii":
		if utf8.Valid(raw) {
			return string(raw)
		}
		enc = charmap.ISO8859_1
	case "iso-8859-1":
		enc = charmap.ISO8859_1
	case "gbk", "gb2312":
		enc = simplifiedchinese.GBK
	case "gb18030":
		enc = simplifiedchinese.GB18030
	case "big5":
		enc = traditionalchinese.Big5
	case "shift_jis":
		enc = japanese.ShiftJIS
	case "euc-kr":
		enc = korean.EUCKR
	case "iso-10646-ucs-2", "utf-16be":
		enc = unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM)
	case "utf-16le":
		enc = unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)
	case "utf-16":
		enc = unicode.UTF16(unicode.BigEndian, unicode.UseBOM)
	default:
		return strings.ToValidUTF8(string(raw), "\uFFFD")
	}
	out, err := enc.NewDecoder().Bytes(raw)
	if err != nil {
		return strings.ToValidUTF8(string(raw), "\uFFFD")
	}
	return strings.ToValidUTF8(string(out), "\uFFFD")
}

// encodedString: a text string, or a value length, a character set and
// the text (which may hold NULs in UCS-2: it runs to the value's end).
func (r *reader) encodedString() string {
	if c := r.peek(); c > 31 || c == 0 {
		return decodeText([]byte(r.text()), "")
	}
	sub := reader{b: r.take(r.valueLength())}
	if r.err != nil {
		return ""
	}
	cs := charsets[sub.integer()]
	raw := sub.b[sub.i:]
	if len(raw) > 0 && raw[0] == 127 {
		raw = raw[1:]
	}
	if strings.HasPrefix(cs, "utf-16") || cs == "iso-10646-ucs-2" {
		if len(raw)%2 == 1 && raw[len(raw)-1] == 0 {
			raw = raw[:len(raw)-1]
		}
	} else if i := bytes.IndexByte(raw, 0); i >= 0 {
		raw = raw[:i]
	}
	if sub.err != nil {
		r.fail(sub.err)
	}
	return decodeText(raw, cs)
}

// address drops an address's /TYPE=PLMN (or =IPv4 ...) suffix.
func address(s string) string {
	if i := strings.Index(strings.ToUpper(s), "/TYPE="); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// timeValue: a value length, then 128 and an absolute date or 129 and
// seconds from now.
func (r *reader) timeValue() (abs, rel int64) {
	sub := reader{b: r.take(r.valueLength())}
	switch sub.byte() {
	case 128:
		abs = int64(sub.integer())
	case 129:
		rel = int64(sub.integer())
	}
	if sub.err != nil {
		r.fail(sub.err)
	}
	return abs, rel
}

// Decode reads an MMS PDU.
func Decode(b []byte) (*PDU, error) {
	r := &reader{b: b}
	p := &PDU{}
	for n := 0; r.err == nil && r.left() > 0; n++ {
		if n == maxHeaders {
			return nil, errors.New("mms: too many headers")
		}
		c := r.byte()
		if c < 0x80 {
			// An application header: a token and a text value.
			r.i--
			r.text()
			r.text()
			continue
		}
		switch c & 0x7f {
		case 0x01, 0x02, 0x17:
			a := address(r.encodedString())
			list := map[byte]*[]string{0x01: &p.Bcc, 0x02: &p.Cc, 0x17: &p.To}[c&0x7f]
			if len(*list) == maxRecipients {
				return nil, errors.New("mms: too many recipients")
			}
			*list = append(*list, a)
		case 0x03:
			p.ContentLocation = r.text()
		case 0x04:
			ct := r.contentType()
			p.ContentType, p.Start, p.Related = ct.Media, ct.Params["start"], ct.Params["type"]
			body := r.b[r.i:]
			r.i = len(r.b)
			if r.err != nil {
				break
			}
			if multipart(p.ContentType) {
				parts, err := decodeMultipart(body)
				if err != nil {
					return nil, err
				}
				p.Parts = parts
			} else {
				p.Body = body
			}
		case 0x05:
			p.Date = int64(r.integer())
		case 0x06:
			p.DeliveryReport = r.byte() == 128
		case 0x08:
			p.Expiry, p.ExpiryAfter = r.timeValue()
		case 0x09:
			sub := reader{b: r.take(r.valueLength())}
			if r.err == nil && sub.byte() == 128 {
				p.From = address(sub.encodedString())
			}
		case 0x0A:
			if c := r.peek(); c >= 0x80 {
				p.Class = map[byte]string{128: "personal", 129: "advertisement", 130: "informational", 131: "auto"}[r.byte()]
			} else {
				p.Class = r.text()
			}
		case 0x0B:
			p.MessageID = r.text()
		case 0x0C:
			p.Type = int(r.byte())
		case 0x0D:
			p.Version = int(r.integer())
		case 0x0E:
			p.Size = int64(r.integer())
		case 0x10:
			p.ReadReport = r.byte() == 128
		case 0x12:
			p.ResponseStatus = int(r.byte())
		case 0x13:
			p.ResponseText = r.encodedString()
		case 0x15:
			p.Status = int(r.byte())
		case 0x16:
			p.Subject = r.encodedString()
		case 0x18:
			p.TransactionID = r.text()
		case 0x19:
			p.RetrieveStatus = int(r.byte())
		case 0x1A:
			p.RetrieveText = r.encodedString()
		default:
			r.skipValue()
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	if p.Type < SendReq || p.Type > ReadOrigInd {
		return nil, fmt.Errorf("mms: message type %d", p.Type)
	}
	return p, nil
}

func multipart(mediaType string) bool {
	return strings.HasPrefix(strings.ToLower(mediaType), "application/vnd.wap.multipart.")
}

func decodeMultipart(b []byte) ([]Part, error) {
	r := &reader{b: b}
	n := r.uintvar()
	if n > MaxParts {
		return nil, errors.New("mms: too many parts")
	}
	parts := make([]Part, 0, n)
	for i := 0; i < n && r.err == nil; i++ {
		hl := r.uintvar()
		dl := r.uintvar()
		h := reader{b: r.take(hl)}
		data := r.take(dl)
		if r.err != nil {
			break
		}
		ct := h.contentType()
		p := Part{ContentType: ct.Media, Charset: ct.Params["charset"], Name: firstOf(ct.Params["name"], ct.Params["filename"]), Data: data}
		for h.err == nil && h.left() > 0 {
			c := h.byte()
			if c < 0x80 {
				h.i--
				name := strings.ToLower(h.text())
				v := h.text()
				switch name {
				case "content-id":
					p.ContentID = strings.Trim(v, "<>\"")
				case "content-location":
					p.Location = v
				}
				continue
			}
			switch c & 0x7f {
			case 0x0E:
				p.Location = h.text()
			case 0x40:
				p.ContentID = strings.Trim(h.textValue(), "<>")
			case 0x2E, 0x45:
				d := reader{b: h.take(h.valueLength())}
				if h.err != nil {
					break
				}
				d.skipValue()
				params := map[string]string{}
				for d.err == nil && d.left() > 0 {
					d.parameter(params)
				}
				if p.Name == "" {
					p.Name = firstOf(params["filename"], params["name"])
				}
			default:
				h.skipValue()
			}
		}
		if h.err != nil {
			return nil, h.err
		}
		parts = append(parts, p)
	}
	if r.err != nil {
		return nil, r.err
	}
	return parts, nil
}

func firstOf(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// encodedString writes text as a text string, or with its character set
// (UTF-8) when it is not ASCII or starts with a control character (which
// would read as a value length).
func (w *writer) encodedString(s string) {
	ascii := s == "" || s[0] >= 0x20
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		w.text(s)
		return
	}
	var inner writer
	inner.integer(106)
	inner.text(s)
	w.valueLength(len(inner.b))
	w.b = append(w.b, inner.b...)
}

// plmn is an address as MMS writes a phone number.
func plmn(a string) string {
	if strings.Contains(a, "@") || strings.Contains(strings.ToUpper(a), "/TYPE=") {
		return a
	}
	return a + "/TYPE=PLMN"
}

// Encode writes a PDU: the message type, transaction and version first, the
// content type and body last, as OMA-MMS-ENC orders them.
func Encode(p *PDU) ([]byte, error) {
	if p.Type < SendReq || p.Type > ReadOrigInd {
		return nil, fmt.Errorf("mms: message type %d", p.Type)
	}
	w := &writer{}
	w.byte(0x8C)
	w.byte(byte(p.Type))
	if p.TransactionID != "" {
		w.byte(0x98)
		w.text(p.TransactionID)
	}
	v := p.Version
	if v == 0 {
		v = Version13
	}
	w.byte(0x8D)
	w.integer(uint64(v))
	if p.MessageID != "" {
		w.byte(0x8B)
		w.text(p.MessageID)
	}
	if p.Date != 0 {
		w.byte(0x85)
		w.longInt(uint64(p.Date))
	}
	if p.Type == SendReq || p.From != "" {
		var inner writer
		if p.From == "" {
			inner.byte(129)
		} else {
			inner.byte(128)
			inner.encodedString(plmn(p.From))
		}
		w.byte(0x89)
		w.valueLength(len(inner.b))
		w.b = append(w.b, inner.b...)
	}
	for _, x := range []struct {
		code byte
		list []string
	}{{0x97, p.To}, {0x82, p.Cc}, {0x81, p.Bcc}} {
		for _, a := range x.list {
			w.byte(x.code)
			w.encodedString(plmn(a))
		}
	}
	if p.Subject != "" {
		w.byte(0x96)
		w.encodedString(p.Subject)
	}
	if p.Class != "" {
		w.byte(0x8A)
		if c, ok := map[string]byte{"personal": 128, "advertisement": 129, "informational": 130, "auto": 131}[p.Class]; ok {
			w.byte(c)
		} else {
			w.text(p.Class)
		}
	}
	if p.Size != 0 {
		w.byte(0x8E)
		w.longInt(uint64(p.Size))
	}
	if p.Expiry != 0 || p.ExpiryAfter != 0 {
		var inner writer
		if p.Expiry != 0 {
			inner.byte(128)
			inner.longInt(uint64(p.Expiry))
		} else {
			inner.byte(129)
			inner.longInt(uint64(p.ExpiryAfter))
		}
		w.byte(0x88)
		w.valueLength(len(inner.b))
		w.b = append(w.b, inner.b...)
	}
	if p.ContentLocation != "" {
		w.byte(0x83)
		w.text(p.ContentLocation)
	}
	if p.Type == SendReq {
		w.byte(0x86)
		w.byte(map[bool]byte{true: 128, false: 129}[p.DeliveryReport])
		w.byte(0x90)
		w.byte(map[bool]byte{true: 128, false: 129}[p.ReadReport])
	}
	for _, x := range []struct {
		code byte
		v    int
	}{{0x92, p.ResponseStatus}, {0x95, p.Status}, {0x99, p.RetrieveStatus}} {
		if x.v != 0 {
			w.byte(x.code)
			w.byte(byte(x.v))
		}
	}
	if p.ResponseText != "" {
		w.byte(0x93)
		w.encodedString(p.ResponseText)
	}
	if p.ContentType == "" {
		return w.b, nil
	}
	w.byte(0x84)
	params := map[string]string{}
	if p.Start != "" {
		params["start"] = p.Start
	}
	if p.Related != "" {
		params["type"] = p.Related
	}
	w.contentType(p.ContentType, params)
	if !multipart(p.ContentType) {
		w.b = append(w.b, p.Body...)
		return w.b, nil
	}
	w.uintvar(len(p.Parts))
	for _, part := range p.Parts {
		var h writer
		params := map[string]string{}
		if part.Charset != "" {
			params["charset"] = part.Charset
		}
		if part.Name != "" {
			params["name"] = part.Name
		}
		h.contentType(part.ContentType, params)
		if part.ContentID != "" {
			h.byte(0xC0)
			h.quoted("<" + part.ContentID + ">")
		}
		if part.Location != "" {
			h.byte(0x8E)
			h.text(part.Location)
		}
		w.uintvar(len(h.b))
		w.uintvar(len(part.Data))
		w.b = append(w.b, h.b...)
		w.b = append(w.b, part.Data...)
	}
	return w.b, nil
}
