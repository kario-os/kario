package obex

import (
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

// The client side of Message Access (MCE): reading an old phone's text
// messages when moving from it (docs/36). An Android phone serves them;
// an iPhone's message server is for a car kit's notifications and lists
// little or nothing.

// MASTarget is the Message Access service's target.
var MASTarget = []byte{0xbb, 0x58, 0x2b, 0x40, 0x42, 0x0c, 0x11, 0xdb, 0xb0, 0xde, 0x08, 0x00, 0x20, 0x0c, 0x9a, 0x66}

// MAP's application parameter tags (MAP 1.4 §6.3.1).
const (
	mapMaxListCount      = 0x01
	mapListStartOffset   = 0x02
	mapFilterMessageType = 0x03
	mapAttachment        = 0x0a
	mapListingSize       = 0x12
	mapCharset           = 0x14
)

// filterEmail leaves e-mail out of a listing (a set bit of
// FilterMessageType filters that type out); SMS and MMS stay.
const filterEmail = 0x04

// Messages is a session with a phone's message server, in its telecom/msg
// folder. After an error other than the server's answer (ResponseError)
// the session is out of step: close the link.
type Messages struct{ cl *Client }

// DialMessages connects to a phone's message server and goes to
// telecom/msg. The phone may ask its user first: wait bounds that,
// timeout each answer after it.
func DialMessages(rw io.ReadWriter, mtu int, wait, timeout time.Duration) (*Messages, error) {
	cl, err := DialWith(rw, mtu, Connect{Target: MASTarget, Wait: wait, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	cl.MaxObject = maxPull
	for _, f := range []string{"telecom", "msg"} {
		if err := cl.SetPath(f, false); err != nil {
			return nil, err
		}
	}
	return &Messages{cl: cl}, nil
}

// Close ends the session.
func (m *Messages) Close() error { return m.cl.Close() }

func listParams(offset, count int) Params {
	return Params{mapMaxListCount: U16(count), mapListStartOffset: U16(offset), mapFilterMessageType: {filterEmail}}
}

// Size is how many messages a folder of telecom/msg (inbox, sent) has, -1
// when the phone does not say.
func (m *Messages) Size(folder string) (int, error) {
	_, ans, err := m.cl.Get("x-bt/MAP-msg-listing", folder, listParams(0, 0).Encode())
	if err != nil {
		return -1, err
	}
	return ParseParams(ans).uint16(mapListingSize, -1), nil
}

// Entry is a message in a folder's listing: its handle, type (SMS_GSM,
// SMS_CDMA, MMS, EMAIL, IM), time, the sender's and recipient's
// addressing (numbers, "" when not said), whether it was read or sent,
// and its subject (the start of an SMS's text).
type Entry struct {
	Handle, Type      string
	At                time.Time
	Sender, Recipient string
	Read, Sent        bool
	Subject           string
}

// List reads count entries of a folder's listing from offset; times
// without a zone are in loc.
func (m *Messages) List(folder string, offset, count int, loc *time.Location) ([]Entry, error) {
	body, _, err := m.cl.Get("x-bt/MAP-msg-listing", folder, listParams(offset, count).Encode())
	if err != nil {
		return nil, err
	}
	return ParseListing(body, loc), nil
}

// ListAll reads a folder's whole listing a page at a time, giving each
// page to each, as PhoneBook.PullAll does.
func (m *Messages) ListAll(folder string, size, page int, loc *time.Location, each func([]Entry) error) error {
	var first string
	for off := 0; off <= 0xffff; off += page {
		es, err := m.List(folder, off, page, loc)
		if err != nil {
			return err
		}
		if len(es) == 0 || off > 0 && es[0].Handle == first {
			return nil
		}
		if off == 0 {
			first = es[0].Handle
		}
		if err := each(es); err != nil {
			return err
		}
		if len(es) != page || size >= 0 && off+page >= size {
			return nil
		}
	}
	return nil
}

// ParseListing reads a MAP-msg-listing; an entry without a handle is
// left out, one without a time that reads has a zero At.
func ParseListing(b []byte, loc *time.Location) []Entry {
	var out []Entry
	yes := func(s string) bool { return strings.EqualFold(strings.TrimSpace(s), "yes") }
	for _, a := range listingAttrs(b, "msg", "handle") {
		e := Entry{Handle: strings.TrimSpace(a["handle"]), Type: strings.ToUpper(strings.TrimSpace(a["type"])),
			Sender: strings.TrimSpace(a["sender_addressing"]), Recipient: strings.TrimSpace(a["recipient_addressing"]),
			Read: yes(a["read"]), Sent: yes(a["sent"]), Subject: a["subject"]}
		e.At, _ = ParseTime(a["datetime"], loc)
		out = append(out, e)
	}
	return out
}

// Message reads one message by its handle: its text in UTF-8, without
// attachments.
func (m *Messages) Message(handle string) (BMessage, error) {
	body, _, err := m.cl.Get("x-bt/message", handle, Params{mapAttachment: {0}, mapCharset: {1}}.Encode())
	if err != nil {
		return BMessage{}, err
	}
	return ParseBMessage(body)
}

// BMessage is a message as MAP sends it (bMessage, MAP 1.4 §3.1.3): read
// or not, its type and folder, the numbers of its originator and of its
// recipients (their vCards' TEL), and its text.
type BMessage struct {
	Read         bool
	Type, Folder string
	From, To     []string
	Text         string
}

// maxNesting bounds a bMessage's components inside one another (a
// forwarded message's envelopes): MAP nests three envelopes at most.
const maxNesting = 16

// Errors of a bMessage.
var (
	ErrNotBMessage = errors.New("not a bMessage")
	ErrNative      = errors.New("the text is in the phone's native encoding")
)

// ParseBMessage reads a bMessage leniently: the message text runs from the
// body's first BEGIN:MSG line to its last END:MSG line (LENGTH is not
// trusted: phones count it differently), a text in several MSG parts is
// joined; an MMS's or e-mail's text is its text/plain parts. Recipients
// are those of the outer envelope.
func ParseBMessage(b []byte) (BMessage, error) {
	var m BMessage
	lines := strings.Split(strings.ReplaceAll(strings.TrimLeft(string(b), "\ufeff \t\r\n"), "\r\n", "\n"), "\n")
	up := func(i int) string { return strings.ToUpper(strings.TrimSpace(lines[i])) }
	bodyEnd := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if up(i) == "END:BBODY" {
			bodyEnd = i
			break
		}
	}
	if len(lines) == 0 || up(0) != "BEGIN:BMSG" || bodyEnd < 0 {
		return m, ErrNotBMessage
	}
	var stack []string
	benvs := 0
	charset := ""
	msgStart, msgEnd := -1, -1
	for i := 1; i < bodyEnd && msgStart < 0; i++ {
		t := up(i)
		if what, ok := strings.CutPrefix(t, "BEGIN:"); ok {
			if what == "MSG" && len(stack) > 0 && stack[len(stack)-1] == "BBODY" {
				msgStart = i + 1
				break
			}
			if len(stack) == maxNesting {
				return m, ErrNotBMessage
			}
			stack = append(stack, what)
			if what == "BENV" {
				benvs++
			}
			continue
		}
		if what, ok := strings.CutPrefix(t, "END:"); ok {
			if len(stack) > 0 && stack[len(stack)-1] == what {
				stack = stack[:len(stack)-1]
				if what == "BENV" {
					benvs--
				}
			}
			continue
		}
		name, _, value, ok := cardLine(strings.TrimSpace(lines[i]))
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		inCard := len(stack) > 0 && stack[len(stack)-1] == "VCARD"
		switch {
		case len(stack) == 0 && name == "STATUS":
			m.Read = strings.EqualFold(value, "READ")
		case len(stack) == 0 && name == "TYPE":
			m.Type = strings.ToUpper(value)
		case len(stack) == 0 && name == "FOLDER":
			m.Folder = value
		case inCard && name == "TEL" && value != "" && benvs == 0:
			m.From = append(m.From, value)
		case inCard && name == "TEL" && value != "" && benvs == 1:
			m.To = append(m.To, value)
		case len(stack) > 0 && stack[len(stack)-1] == "BBODY" && name == "CHARSET":
			charset = strings.ToUpper(value)
		}
	}
	for i := bodyEnd - 1; i >= msgStart && msgStart >= 0; i-- {
		if up(i) == "END:MSG" {
			msgEnd = i
			break
		}
	}
	if msgStart < 0 || msgEnd < msgStart {
		return m, ErrNotBMessage
	}
	var text []string
	for i := msgStart; i < msgEnd; i++ {
		if up(i) == "END:MSG" && i+1 < msgEnd && up(i+1) == "BEGIN:MSG" {
			i++
			continue
		}
		text = append(text, strings.TrimSuffix(lines[i], "\r"))
	}
	m.Text = strings.Join(text, "\n")
	if charset == "NATIVE" && strings.HasPrefix(m.Type, "SMS") {
		return m, ErrNative
	}
	if m.Type == "MMS" || m.Type == "EMAIL" {
		m.Text = mimeText(m.Text)
	}
	m.Text = strings.ToValidUTF8(m.Text, "")
	return m, nil
}

// maxText bounds the text taken from one MMS part.
const maxText = 64 << 10

// mimeText is a MIME message's text/plain parts, one after another; a
// text without headers is itself.
func mimeText(s string) string {
	msg, err := mail.ReadMessage(strings.NewReader(s))
	if err != nil {
		return strings.TrimSpace(s)
	}
	ct := msg.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/plain"
	}
	return strings.TrimSpace(partText(ct, msg.Header.Get("Content-Transfer-Encoding"), msg.Body, 0))
}

func partText(ctype, cte string, r io.Reader, depth int) string {
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil {
		return ""
	}
	switch {
	case strings.HasPrefix(mt, "multipart/") && depth < 4:
		mr := multipart.NewReader(r, params["boundary"])
		var texts []string
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			pt := p.Header.Get("Content-Type")
			if pt == "" {
				pt = "text/plain"
			}
			if t := strings.TrimSpace(partText(pt, p.Header.Get("Content-Transfer-Encoding"), p, depth+1)); t != "" {
				texts = append(texts, t)
			}
		}
		return strings.Join(texts, "\n")
	case mt == "text/plain":
		switch strings.ToLower(strings.TrimSpace(cte)) {
		case "base64":
			r = base64.NewDecoder(base64.StdEncoding, &spaceless{r: r})
		case "quoted-printable":
			r = quotedprintable.NewReader(r)
		}
		b, _ := io.ReadAll(io.LimitReader(r, maxText))
		if cs := strings.ToLower(params["charset"]); cs != "" && cs != "utf-8" && cs != "us-ascii" && !utf8.Valid(b) {
			return ""
		}
		return string(b)
	}
	return ""
}

// spaceless drops the line breaks inside base64.
type spaceless struct{ r io.Reader }

func (s *spaceless) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	j := 0
	for _, c := range p[:n] {
		if c != '\r' && c != '\n' && c != ' ' && c != '\t' {
			p[j] = c
			j++
		}
	}
	if j == 0 && n > 0 && err == nil {
		return s.Read(p)
	}
	return j, err
}
