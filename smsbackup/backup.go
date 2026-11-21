// Package smsbackup reads and writes the XML of "SMS Backup & Restore"
// (SyncTech), the Android app most people move their messages with: a root
// <smses> holding <sms> elements and <mms> elements with their <parts> and
// <addrs>, everything a message has in attributes (32-messaging.md §4.1).
// A backup file is untrusted input; the reader is a small scanner of its
// own rather than encoding/xml, so that a picture's base64 (an MMS part's
// data, megabytes) is skipped without being held, the app's emoji written
// as two numeric references (a UTF-16 surrogate pair each) come out whole,
// and a stray control character does not end the file.
package smsbackup

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/ui"
)

// Limits of what is read: messages in a file, characters of a message's
// text and subject, bytes of an address, of a name; attributes of an
// element, parts and addresses of a multimedia message. A longer text is
// cut; a message whose address is longer is left out.
const (
	MaxMessages = 20000
	MaxText     = 5000
	MaxSubject  = 200
	MaxAddress  = 64
	maxName     = 64
	maxAttrs    = 64
	maxParts    = 64
	maxAddrs    = 64
	// maxValue: bytes of an attribute value kept while reading (UTF-8, so
	// MaxText characters fit); the rest of a longer one is read past.
	maxValue = 4 * MaxText
)

// ErrNotBackup: the data is not such a file (no <smses> element first).
var ErrNotBackup = errors.New("not an SMS Backup & Restore file")

// Messages before 2000 or after 3000 are taken for unreadable dates.
const (
	minAt = 946684800
	maxAt = 32503680000
)

// The message boxes and types of Android's telephony provider that the
// app writes: sms type and mms msg_box (1 received, 2 sent, 5 failed; the
// others are drafts and messages waiting to go), mms m_type and addr type.
const (
	boxInbox  = 1
	boxSent   = 2
	boxFailed = 5

	mTypeSendReq      = 128
	mTypeRetrieveConf = 132

	addrFrom = 137
	addrTo   = 151
	addrCc   = 130
	addrBcc  = 129
)

// ownToken is what Android writes for this phone's own number in a sent
// multimedia message.
const ownToken = "insert-address-token"

// Stats: messages read, and those left out (a draft, a picture alone, no
// address or an unreadable date); Truncated: reading stopped at
// MaxMessages or MaxTotalText.
type Stats struct {
	Messages  int
	Skipped   int
	Truncated bool
}

// MaxTotalText bounds the text of all the messages read (bytes): what a
// phone holds (10000 messages) many times over.
const MaxTotalText = 32 << 20

// Parse reads a backup: each message as phone-smsd imports it (Peer, Dir,
// Text, At, Read; a multimedia message's Kind, Subject and Members; State
// failed for one that was not sent). own is this phone's number, "" when
// unknown: it is left out of a group's members (unknown, a received
// message to one recipient is to this phone). Numbers are compared as
// ui.NumberKey does.
func Parse(r io.Reader, own string) ([]ui.SMS, Stats, error) {
	p := &parser{r: bufio.NewReaderSize(r, 64<<10), own: ui.NumberKey(own)}
	if own == "" {
		p.own = ""
	}
	err := p.run()
	return p.out, p.st, err
}

type parser struct {
	r    *bufio.Reader
	own  string
	out  []ui.SMS
	st   Stats
	text int
	root bool
	mms  *mmsMsg
	name []byte
	attr map[string]string
}

type mmsMsg struct {
	attrs map[string]string
	texts []string
	addrs []addr
	parts int
}

type addr struct {
	number string
	typ    int
}

func (p *parser) run() error {
	for {
		if err := p.skipTo('<'); err != nil {
			if err == io.EOF && p.root {
				p.flushMMS()
				return nil
			}
			if err == io.EOF {
				return ErrNotBackup
			}
			return err
		}
		c, err := p.r.ReadByte()
		if err != nil {
			return p.end(err)
		}
		switch c {
		case '?':
			err = p.skipPast("?>")
		case '!':
			err = p.special()
		case '/':
			var name string
			if name, err = p.readName(); err == nil {
				err = p.skipTo('>')
				p.endTag(name)
			}
		default:
			_ = p.r.UnreadByte()
			err = p.startTag()
		}
		if err != nil {
			return p.end(err)
		}
		if len(p.out) >= MaxMessages || p.st.Truncated {
			p.st.Truncated = true
			return nil
		}
	}
}

// end is how reading stops at err: a file cut short keeps what it gave.
func (p *parser) end(err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		if !p.root {
			return ErrNotBackup
		}
		p.flushMMS()
		return nil
	}
	return err
}

func (p *parser) skipTo(b byte) error {
	for {
		c, err := p.r.ReadByte()
		if err != nil {
			return err
		}
		if c == b {
			return nil
		}
	}
}

func (p *parser) skipPast(s string) error {
	matched := 0
	for matched < len(s) {
		c, err := p.r.ReadByte()
		if err != nil {
			return err
		}
		switch {
		case c == s[matched]:
			matched++
		case c == s[0]:
			matched = 1
		default:
			matched = 0
		}
	}
	return nil
}

// special skips a comment, a CDATA section or a declaration (a DOCTYPE's
// internal subset in brackets included).
func (p *parser) special() error {
	head, _ := p.r.Peek(7)
	switch {
	case bytes.HasPrefix(head, []byte("--")):
		_, _ = p.r.Discard(2)
		return p.skipPast("-->")
	case bytes.HasPrefix(head, []byte("[CDATA[")):
		_, _ = p.r.Discard(7)
		return p.skipPast("]]>")
	}
	depth := 0
	for {
		c, err := p.r.ReadByte()
		if err != nil {
			return err
		}
		switch {
		case c == '[':
			depth++
		case c == ']' && depth > 0:
			depth--
		case c == '>' && depth == 0:
			return nil
		}
	}
}

func nameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == ':'
}

// readName reads an element or attribute name (cut at maxName bytes).
func (p *parser) readName() (string, error) {
	p.name = p.name[:0]
	for {
		c, err := p.r.ReadByte()
		if err != nil {
			return "", err
		}
		if !nameByte(c) {
			_ = p.r.UnreadByte()
			return strings.ToLower(string(p.name)), nil
		}
		if len(p.name) < maxName {
			p.name = append(p.name, c)
		}
	}
}

func (p *parser) skipSpace() (byte, error) {
	for {
		c, err := p.r.ReadByte()
		if err != nil {
			return 0, err
		}
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			return c, nil
		}
	}
}

// startTag reads an element's name and attributes up to its '>'.
func (p *parser) startTag() error {
	name, err := p.readName()
	if err != nil {
		return err
	}
	clear(p.attr)
	if p.attr == nil {
		p.attr = map[string]string{}
	}
	empty := false
	for {
		c, err := p.skipSpace()
		if err != nil {
			return err
		}
		if c == '>' {
			break
		}
		if c == '/' {
			empty = true
			continue
		}
		empty = false
		if !nameByte(c) {
			continue
		}
		_ = p.r.UnreadByte()
		an, err := p.readName()
		if err != nil {
			return err
		}
		if c, err = p.skipSpace(); err != nil {
			return err
		}
		if c != '=' {
			_ = p.r.UnreadByte()
			continue
		}
		if c, err = p.skipSpace(); err != nil {
			return err
		}
		if c != '"' && c != '\'' {
			_ = p.r.UnreadByte()
			continue
		}
		v, err := p.readValue(c)
		if err != nil {
			return err
		}
		if len(p.attr) < maxAttrs {
			p.attr[an] = v
		}
	}
	p.element(name, empty)
	return nil
}

// readValue reads a quoted attribute value, its references resolved: the
// five named ones, decimal and hexadecimal character references (a
// surrogate pair's two references make one character); control
// characters other than tab and line breaks are dropped. Past maxValue
// bytes the value is read but not kept.
func (p *parser) readValue(quote byte) (string, error) {
	var b []byte
	var high rune
	put := func(r rune) {
		if high != 0 {
			if r >= 0xDC00 && r <= 0xDFFF {
				r = 0x10000 + (high-0xD800)<<10 + (r - 0xDC00)
			} else if len(b) < maxValue {
				b = utf8.AppendRune(b, utf8.RuneError)
			}
			high = 0
		}
		switch {
		case r >= 0xD800 && r <= 0xDBFF:
			high = r
			return
		case r >= 0xDC00 && r <= 0xDFFF:
			r = utf8.RuneError
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r':
			return
		}
		if len(b) < maxValue {
			b = utf8.AppendRune(b, r)
		}
	}
	for {
		c, err := p.r.ReadByte()
		if err != nil {
			return "", err
		}
		switch {
		case c == quote:
			if high != 0 && len(b) < maxValue {
				b = utf8.AppendRune(b, utf8.RuneError)
			}
			return strings.ToValidUTF8(string(b), "\uFFFD"), nil
		case c == '&':
			ref, err := p.readRef()
			if err != nil {
				return "", err
			}
			if r, ok := refRune(ref); ok {
				put(r)
			} else {
				for _, r := range "&" + ref {
					put(r)
				}
			}
		case c < 0x80:
			put(rune(c))
		default:
			if high != 0 {
				put(utf8.RuneError)
				high = 0
			}
			if len(b) < maxValue {
				b = append(b, c)
			}
		}
	}
}

// readRef reads what follows '&' up to ';' (at most 10 bytes; without a
// ';' by then it is no reference and is kept as text).
func (p *parser) readRef() (string, error) {
	var ref []byte
	for len(ref) < 10 {
		c, err := p.r.ReadByte()
		if err != nil {
			return "", err
		}
		if c == ';' {
			return string(ref) + ";", nil
		}
		if c == '"' || c == '\'' || c == '&' || c == '<' {
			_ = p.r.UnreadByte()
			break
		}
		ref = append(ref, c)
	}
	return string(ref), nil
}

func refRune(ref string) (rune, bool) {
	name, ok := strings.CutSuffix(ref, ";")
	if !ok {
		return 0, false
	}
	switch name {
	case "lt":
		return '<', true
	case "gt":
		return '>', true
	case "amp":
		return '&', true
	case "quot":
		return '"', true
	case "apos":
		return '\'', true
	}
	num, ok := strings.CutPrefix(name, "#")
	if !ok || num == "" {
		return 0, false
	}
	base := 10
	if h, ok := strings.CutPrefix(strings.ToLower(num), "x"); ok {
		num, base = h, 16
	}
	n, err := strconv.ParseUint(num, base, 32)
	if err != nil || n > utf8.MaxRune {
		return 0, false
	}
	return rune(n), true
}

func (p *parser) element(name string, empty bool) {
	switch {
	case name == "smses":
		p.root = true
	case !p.root:
	case name == "sms":
		p.flushMMS()
		p.sms()
	case name == "mms":
		p.flushMMS()
		p.mms = &mmsMsg{attrs: clone(p.attr)}
		if empty {
			p.flushMMS()
		}
	case p.mms == nil:
	case name == "part" && p.mms.parts < maxParts:
		p.mms.parts++
		if strings.EqualFold(value(p.attr, "ct"), "text/plain") {
			if t := value(p.attr, "text"); t != "" {
				p.mms.texts = append(p.mms.texts, t)
			}
		}
	case name == "addr" && len(p.mms.addrs) < maxAddrs:
		typ, _ := strconv.Atoi(value(p.attr, "type"))
		if a := strings.TrimSpace(value(p.attr, "address")); address(a) {
			p.mms.addrs = append(p.mms.addrs, addr{a, typ})
		}
	}
}

// address: a's a multimedia message's address, not empty nor holding
// what separates a list of them.
func address(a string) bool { return a != "" && !strings.ContainsAny(a, ",;~") }

func (p *parser) endTag(name string) {
	if name == "mms" {
		p.flushMMS()
	}
}

func clone(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// value is an attribute's value; the app writes "null" for none.
func value(attrs map[string]string, name string) string {
	v := attrs[name]
	if strings.TrimSpace(v) == "null" {
		return ""
	}
	return v
}

// at reads a date: milliseconds, as the app writes both kinds of message,
// or seconds (the provider's own unit for a multimedia message).
func at(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	if n >= 1e11 {
		n /= 1000
	}
	if n < minAt || n > maxAt {
		return 0
	}
	return n
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func (p *parser) sms() {
	a := p.attr
	box, _ := strconv.Atoi(value(a, "type"))
	m := ui.SMS{Peer: strings.TrimSpace(value(a, "address")), Text: cut(value(a, "body"), MaxText), At: at(value(a, "date")), Read: value(a, "read") == "1"}
	switch box {
	case boxInbox:
		m.Dir = "in"
	case boxSent:
		m.Dir = "out"
	case boxFailed:
		m.Dir, m.State = "out", ui.SMSFailed
	}
	p.add(m)
}

func (p *parser) add(m ui.SMS) {
	if m.Dir == "out" {
		m.Read = true
	}
	if m.Dir == "" || m.Peer == "" || (m.Members == nil && len(m.Peer) > MaxAddress) || m.At == 0 || (strings.TrimSpace(m.Text) == "" && m.Subject == "") {
		p.st.Skipped++
		return
	}
	p.text += len(m.Text) + len(m.Subject)
	if p.text > MaxTotalText {
		p.st.Truncated = true
		return
	}
	p.out = append(p.out, m)
	p.st.Messages++
}

func (p *parser) mine(number string) bool {
	return number == ownToken || (p.own != "" && ui.NumberKey(number) == p.own)
}

// flushMMS turns the multimedia message read so far into a message: the
// sender and the other recipients of a received one (a group when there
// are others besides this phone), the recipients of a sent one, its text
// parts as its text.
func (p *parser) flushMMS() {
	x := p.mms
	if x == nil {
		return
	}
	p.mms = nil
	a := x.attrs
	box, _ := strconv.Atoi(value(a, "msg_box"))
	if box == 0 {
		switch mt, _ := strconv.Atoi(value(a, "m_type")); mt {
		case mTypeRetrieveConf:
			box = boxInbox
		case mTypeSendReq:
			box = boxSent
		}
	}
	m := ui.SMS{Kind: ui.SMSKindMMS, Subject: cut(strings.TrimSpace(value(a, "sub")), MaxSubject), At: at(value(a, "date")),
		Read: value(a, "read") == "1", Text: cut(strings.TrimSpace(strings.Join(x.texts, "\n")), MaxText)}
	// to: the recipients, this phone (the token) among them, each once.
	var from string
	var to []string
	seen := map[string]bool{}
	for _, ad := range x.addrs {
		k := ui.NumberKey(ad.number)
		switch {
		case ad.typ == addrFrom && from == "" && ad.number != ownToken:
			from = ad.number
		case ad.typ != addrTo && ad.typ != addrCc && ad.typ != addrBcc, seen[k]:
		default:
			seen[k] = true
			to = append(to, ad.number)
		}
	}
	if len(x.addrs) == 0 {
		for _, n := range strings.Split(value(a, "address"), "~") {
			if n = strings.TrimSpace(n); address(n) && !seen[ui.NumberKey(n)] {
				seen[ui.NumberKey(n)] = true
				to = append(to, n)
			}
		}
		if box == boxInbox && len(to) > 0 {
			from, to = to[0], to[1:]
		}
	}
	switch box {
	case boxInbox:
		m.Dir, m.Peer = "in", from
		if p.own != "" || len(to) > 1 {
			others := []string{from}
			fk := ui.NumberKey(from)
			for _, n := range to {
				if !p.mine(n) && ui.NumberKey(n) != fk && len(others) < ui.MaxGroup {
					others = append(others, n)
				}
			}
			if len(others) > 1 {
				m.Members = others
			}
		}
	case boxSent, boxFailed:
		m.Dir = "out"
		if box == boxFailed {
			m.State = ui.SMSFailed
		}
		var rcpt []string
		for _, n := range to {
			if !p.mine(n) && len(rcpt) < ui.MaxGroup {
				rcpt = append(rcpt, n)
			}
		}
		m.Peer = strings.Join(rcpt, ", ")
		if len(rcpt) > 1 {
			m.Members = rcpt
		}
	}
	for _, n := range m.Members {
		if len(n) > MaxAddress {
			m.Peer = ""
		}
	}
	p.add(m)
}

// Write writes messages as the app does, oldest first as given: a
// multimedia message with its subject and its text as one text part (its
// pictures stay in the media library), the recipients and the sender as
// addresses. name is a number's contact name ("" for none); own is this
// phone's number, "" when unknown. Scheduled messages and those still
// going out are left out; n is how many it wrote.
func Write(w io.Writer, msgs []ui.SMS, name func(string) string, own string, now time.Time) (n int, err error) {
	b := bufio.NewWriter(w)
	var keep []ui.SMS
	for _, m := range msgs {
		if m.State != ui.SMSScheduled && m.State != ui.SMSSending {
			keep = append(keep, m)
		}
	}
	if own == "" {
		own = ownToken
	}
	fmt.Fprintf(b, "<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>\n")
	fmt.Fprintf(b, "<smses count=\"%d\" backup_date=\"%d\" type=\"full\">\n", len(keep), now.UnixMilli())
	for _, m := range keep {
		box := boxInbox
		switch {
		case m.Dir == "out" && m.State == ui.SMSFailed:
			box = boxFailed
		case m.Dir == "out":
			box = boxSent
		}
		read := 0
		if m.Read || m.Dir == "out" {
			read = 1
		}
		contact := "(Unknown)"
		if n := name(m.Peer); n != "" {
			contact = n
		}
		date := time.Unix(m.At, 0)
		if m.Kind != ui.SMSKindMMS {
			fmt.Fprintf(b, "  <sms protocol=\"0\" address=%s date=\"%d\" type=\"%d\" subject=\"null\" body=%s toa=\"null\" sc_toa=\"null\" service_center=\"null\" read=\"%d\" status=\"-1\" locked=\"0\" date_sent=\"0\" readable_date=%s contact_name=%s />\n",
				attr(m.Peer), date.UnixMilli(), box, attr(m.Text), read, attr(date.Format("Jan 2, 2006 3:04:05 PM")), attr(contact))
			continue
		}
		peers := m.Members
		if len(peers) == 0 {
			peers = splitPeers(m.Peer)
		}
		mtype, sub := mTypeRetrieveConf, "null"
		if m.Dir == "out" {
			mtype = mTypeSendReq
		}
		if m.Subject != "" {
			sub = m.Subject
		}
		fmt.Fprintf(b, "  <mms date=\"%d\" ct_t=\"application/vnd.wap.multipart.related\" msg_box=\"%d\" address=%s sub=%s read=\"%d\" seen=\"1\" m_type=\"%d\" text_only=\"1\" date_sent=\"0\" locked=\"0\" readable_date=%s contact_name=%s>\n",
			date.UnixMilli(), box, attr(strings.Join(peers, "~")), attr(sub), read, mtype, attr(date.Format("Jan 2, 2006 3:04:05 PM")), attr(contact))
		b.WriteString("    <parts>\n")
		if m.Text != "" {
			fmt.Fprintf(b, "      <part seq=\"0\" ct=\"text/plain\" name=\"null\" chset=\"106\" cd=\"null\" fn=\"null\" cid=\"&lt;text0&gt;\" cl=\"text0.txt\" ctt_s=\"null\" ctt_t=\"null\" text=%s />\n", attr(m.Text))
		}
		b.WriteString("    </parts>\n    <addrs>\n")
		addrLine := func(n string, typ int) {
			fmt.Fprintf(b, "      <addr address=%s type=\"%d\" charset=\"106\" />\n", attr(n), typ)
		}
		if m.Dir == "out" {
			addrLine(own, addrFrom)
			for _, n := range peers {
				addrLine(n, addrTo)
			}
		} else {
			addrLine(m.Peer, addrFrom)
			for _, n := range peers {
				if ui.NumberKey(n) != ui.NumberKey(m.Peer) {
					addrLine(n, addrTo)
				}
			}
			addrLine(own, addrTo)
		}
		b.WriteString("    </addrs>\n  </mms>\n")
	}
	b.WriteString("</smses>\n")
	return len(keep), b.Flush()
}

func splitPeers(peer string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(peer, func(r rune) bool { return r == ',' || r == ';' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// attr is a quoted attribute value: markup characters, line breaks and
// tabs as references, so a reader that normalises attribute whitespace
// keeps them.
func attr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\n', '\r', '\t':
			fmt.Fprintf(&b, "&#%d;", r)
		default:
			if r >= 0x20 {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
