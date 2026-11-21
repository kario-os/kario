package mms

import (
	"errors"
	"strconv"
	"strings"
)

// Push is a WAP push (WAP-251) carried by an SMS: its content type, the
// application it is for (X-Wap-Application-Id: "4" is the MMS user agent)
// and the body (an MMS PDU, a service indication).
type Push struct {
	ContentType string
	AppID       string
	Body        []byte
}

// ParsePush reads a connectionless WSP push PDU: the transaction id, the
// PDU type (push or confirmed push), the headers' length, the content type
// and headers, and the body. A user data header still in front (the
// destination port 2948 the push came to) is passed over.
func ParsePush(b []byte) (*Push, error) {
	if len(b) > 2 && b[1] != wspPush && b[1] != wspConfirmedPush && int(b[0])+1 < len(b) && (b[1] == 0x05 || b[1] == 0x04 || b[1] == 0x00) {
		b = b[int(b[0])+1:]
	}
	r := &reader{b: b}
	r.byte()
	if t := r.byte(); r.err == nil && t != wspPush && t != wspConfirmedPush {
		return nil, errors.New("mms: not a WSP push")
	}
	hl := r.uintvar()
	h := reader{b: r.take(hl)}
	if r.err != nil {
		return nil, r.err
	}
	p := &Push{ContentType: h.contentType().Media, Body: r.b[r.i:]}
	for n := 0; h.err == nil && h.left() > 0; n++ {
		if n == maxHeaders {
			return nil, errors.New("mms: too many headers")
		}
		c := h.byte()
		if c < 0x80 {
			h.i--
			h.text()
			h.text()
			continue
		}
		if c&0x7f == 0x2F {
			if v := h.peek(); v >= 0x80 || v <= 30 {
				p.AppID = strconv.FormatUint(h.integer(), 10)
			} else {
				p.AppID = h.text()
			}
			continue
		}
		h.skipValue()
	}
	if h.err != nil {
		return nil, h.err
	}
	if p.ContentType == "" {
		return nil, errors.New("mms: push without a content type")
	}
	return p, nil
}

// IsMMS: the push carries an MMS PDU (a notification, a delivery report).
func (p *Push) IsMMS() bool {
	return strings.EqualFold(p.ContentType, TypeMMS) || p.AppID == "4" || strings.EqualFold(p.AppID, "x-wap-application:mms.ua")
}

// EncodePush wraps a body in a WSP push PDU (the fake MMSC and tests).
func EncodePush(tid byte, contentType string, body []byte) []byte {
	var h writer
	h.contentType(contentType, nil)
	if strings.EqualFold(contentType, TypeMMS) {
		h.byte(0xAF)
		h.integer(appIDMMS)
	}
	w := &writer{}
	w.byte(tid)
	w.byte(wspPush)
	w.uintvar(len(h.b))
	w.b = append(w.b, h.b...)
	w.b = append(w.b, body...)
	return w.b
}

// SI is a WAP Push service indication (WAP-167): a link and the text
// describing it. The system shows it as a message; nothing follows the
// link by itself.
type SI struct {
	Href, Text, ID, Action string
}

// SI tokens (WAP-167 §8): tags, attribute starts and attribute values.
var (
	siAttrStart = map[byte][2]string{
		0x05: {"action", "signal-none"}, 0x06: {"action", "signal-low"}, 0x07: {"action", "signal-medium"},
		0x08: {"action", "signal-high"}, 0x09: {"action", "delete"}, 0x0A: {"created", ""}, 0x0B: {"href", ""},
		0x0C: {"href", "http://"}, 0x0D: {"href", "http://www."}, 0x0E: {"href", "https://"}, 0x0F: {"href", "https://www."},
		0x10: {"si-expires", ""}, 0x11: {"si-id", ""}, 0x12: {"class", ""},
	}
	siAttrValue = map[byte]string{0x85: ".com/", 0x86: ".edu/", 0x87: ".net/", 0x88: ".org/"}
)

const maxSIText = 2 << 10

// ParseSI reads a binary (WBXML) service indication.
func ParseSI(b []byte) (*SI, error) {
	r := &reader{b: b}
	r.byte() // WBXML version
	if pub := r.uintvar(); pub == 0 {
		r.uintvar()
	}
	r.uintvar() // character set: the text is taken as UTF-8
	strtab := r.take(r.uintvar())
	if r.err != nil {
		return nil, r.err
	}
	str := func(off int) string {
		if off < 0 || off >= len(strtab) {
			r.fail(errors.New("mms: string table offset"))
			return ""
		}
		end := off
		for end < len(strtab) && strtab[end] != 0 {
			end++
		}
		return string(strtab[off:end])
	}
	si := &SI{}
	var tags []byte
	inIndication := false
	for steps := 0; r.err == nil && r.left() > 0; steps++ {
		if steps > 4096 || len(tags) > 16 {
			return nil, errors.New("mms: service indication too deep")
		}
		c := r.byte()
		switch c {
		case 0x00: // SWITCH_PAGE
			r.byte()
			continue
		case 0x01: // END
			if len(tags) == 0 {
				r.i = len(r.b)
				continue
			}
			if tags[len(tags)-1] == 0x06 {
				inIndication = false
			}
			tags = tags[:len(tags)-1]
			continue
		case 0x03: // STR_I
			if inIndication && len(si.Text) < maxSIText {
				si.Text += r.text()
			} else {
				r.text()
			}
			continue
		case 0x83: // STR_T
			s := str(r.uintvar())
			if inIndication && len(si.Text) < maxSIText {
				si.Text += s
			}
			continue
		case 0xC3: // OPAQUE
			r.take(r.uintvar())
			continue
		case 0x02: // ENTITY
			r.uintvar()
			continue
		}
		tag := c & 0x3f
		if c&0x80 != 0 {
			attrs, err := siAttrs(r, str)
			if err != nil {
				return nil, err
			}
			if tag == 0x06 {
				si.Href, si.ID, si.Action = attrs["href"], attrs["si-id"], attrs["action"]
			}
		}
		if c&0x40 != 0 {
			tags = append(tags, tag)
			if tag == 0x06 {
				inIndication = true
			}
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	si.Text = strings.TrimSpace(si.Text)
	if si.Href == "" && si.Text == "" {
		return nil, errors.New("mms: empty service indication")
	}
	return si, nil
}

// siAttrs reads a tag's attributes up to their END.
func siAttrs(r *reader, str func(int) string) (map[string]string, error) {
	attrs := map[string]string{}
	name := ""
	for n := 0; r.err == nil; n++ {
		if n > 256 {
			return nil, errors.New("mms: too many attributes")
		}
		c := r.byte()
		switch {
		case c == 0x01:
			return attrs, nil
		case c == 0x03:
			attrs[name] += r.text()
		case c == 0x83:
			attrs[name] += str(r.uintvar())
		case c == 0xC3:
			r.take(r.uintvar())
		case c >= 0x80:
			attrs[name] += siAttrValue[c]
		default:
			a, ok := siAttrStart[c]
			if !ok {
				return nil, errors.New("mms: unknown attribute")
			}
			name = a[0]
			attrs[name] = a[1]
		}
		if len(attrs[name]) > maxSIText {
			return nil, errors.New("mms: attribute too long")
		}
	}
	return nil, r.err
}
