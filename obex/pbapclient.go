package obex

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"time"
)

// The client side of Phone Book Access (PCE): reading an old phone's
// phone book and call histories when moving from it (docs/36). Only the
// phone's own phone book (telecom/), not a SIM card's.

// pceFeatures are the PCE features this client has (PbapSupportedFeatures):
// download and browsing.
const pceFeatures = 0x00000003

// PBAP12 is the server's profile version (from its service record) from
// which the client says its features in CONNECT; an older server gets no
// application parameters there.
const PBAP12 = 0x0102

// PullProps is the property selector of a pull: what a move keeps (names,
// numbers, notes) and the call time; no photo, which would be most of it.
const PullProps = PropVersion | PropFN | PropN | PropTel | PropOrg | PropNote | PropCallDateTime

// maxPull bounds one object a client reads (a page of a phone book, a
// listing, a message).
const maxPull = 16 << 20

// U32 and U64 are four- and eight-byte parameter values.
func U32(n uint32) []byte { return binary.BigEndian.AppendUint32(nil, n) }
func U64(n uint64) []byte { return binary.BigEndian.AppendUint64(nil, n) }

// PhoneBook is a session with a phone's phone book server. After an error
// other than the server's answer (ResponseError) the session is out of
// step: close the link.
type PhoneBook struct{ cl *Client }

// DialPhoneBook connects to a phone's phone book server; version is its
// profile version (0: unknown). cn has the waits (the phone may ask its
// user first) and the authentication; the target and the parameters are
// the profile's.
func DialPhoneBook(rw io.ReadWriter, mtu int, version uint16, cn Connect) (*PhoneBook, error) {
	cn.Target, cn.Params = PBAPTarget, nil
	if version >= PBAP12 {
		cn.Params = Params{tagSupportedFeature: U32(pceFeatures)}.Encode()
	}
	cl, err := DialWith(rw, mtu, cn)
	if err != nil {
		return nil, err
	}
	cl.MaxObject = maxPull
	return &PhoneBook{cl: cl}, nil
}

// Close ends the session.
func (pb *PhoneBook) Close() error { return pb.cl.Close() }

// Size is how many entries an object has (telecom/pb.vcf, telecom/ich.vcf
// and so on), -1 when the phone does not say.
func (pb *PhoneBook) Size(object string) (int, error) {
	body, ans, err := pb.cl.Get("x-bt/phonebook", object, Params{tagMaxListCount: U16(0)}.Encode())
	if err != nil {
		return -1, err
	}
	if n := ParseParams(ans).uint16(tagPhonebookSize, -1); n >= 0 {
		return n, nil
	}
	// A phone that took the count of 0 for no count sent the object.
	if len(body) > 0 {
		return len(SplitVCards(body)), nil
	}
	return -1, nil
}

// Pull reads count entries of the object from offset as vCards with the
// properties PullProps selects, in vCard 2.1: car kits ask so, and phones
// write it best. A phone may answer 3.0 all the same.
func (pb *PhoneBook) Pull(object string, offset, count int) ([]string, error) {
	params := Params{tagPropertySelector: U64(PullProps), tagFormat: {0}, tagMaxListCount: U16(count), tagListStartOffset: U16(offset)}
	body, _, err := pb.cl.Get("x-bt/phonebook", object, params.Encode())
	if err != nil {
		return nil, err
	}
	return SplitVCards(body), nil
}

// PullAll reads the whole object a page at a time and gives each page's
// cards to each, stopping at the first error each returns; size is what
// Size said (-1: unknown). A phone that ignores the offset (it sends the
// whole object, or the first page, again) ends it with what it gave.
func (pb *PhoneBook) PullAll(object string, size, page int, each func([]string) error) error {
	var first string
	for off := 0; off <= 0xffff; off += page {
		cards, err := pb.Pull(object, off, page)
		if err != nil {
			return err
		}
		if len(cards) == 0 || off > 0 && cards[0] == first {
			return nil
		}
		if off == 0 {
			first = cards[0]
		}
		if err := each(cards); err != nil {
			return err
		}
		if len(cards) != page || size >= 0 && off+page >= size {
			return nil
		}
	}
	return nil
}

// OwnerFirst reports whether the phone book starts with the phone's own
// card, handle 0 (PBAP 1.2 keeps it for the owner), which a move leaves
// out: the first card of the listing in handle order. A phone that does
// not list (download only) is taken to have it, as the profile says.
func (pb *PhoneBook) OwnerFirst() (bool, error) {
	body, _, err := pb.cl.Get("x-bt/vcard-listing", "telecom/pb", Params{tagOrder: {0}, tagMaxListCount: U16(1)}.Encode())
	var re ResponseError
	if errors.As(err, &re) && (re == BadRequest || re == NotImplemented || re == NotAcceptable) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	hs := listingAttrs(body, "card", "handle")
	return len(hs) > 0 && strings.EqualFold(strings.TrimSpace(hs[0]["handle"]), "0.vcf"), nil
}

// SplitVCards cuts a phone book object into its vCards, each from its
// BEGIN:VCARD line to its END:VCARD line with CRLF line ends; a card
// inside a card (2.1's AGENT) stays in the outer one, and a card without
// its end is left out.
func SplitVCards(b []byte) []string {
	var out, cur []string
	depth := 0
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		t := strings.ToUpper(strings.TrimSpace(l))
		switch {
		case t == "BEGIN:VCARD":
			depth++
		case depth == 0:
			continue
		}
		cur = append(cur, strings.TrimRight(l, "\r"))
		if t == "END:VCARD" {
			if depth--; depth == 0 {
				out = append(out, strings.Join(cur, "\r\n")+"\r\n")
				cur = nil
			}
		}
	}
	return out
}

// Call is a call history entry: the number as the phone has it, the kind
// (RECEIVED, DIALED or MISSED) and when.
type Call struct {
	Number, Kind string
	At           time.Time
}

// historyKind is what the call history folders hold.
var historyKind = map[string]string{"ich": "RECEIVED", "och": "DIALED", "mch": "MISSED"}

// Errors of a call history entry.
var (
	ErrNoNumber = errors.New("no number")
	ErrNoTime   = errors.New("no call time")
	ErrNoKind   = errors.New("no call kind")
)

// ParseCall reads an entry of a call history folder (ich, och, mch or
// cch): its first TEL, and X-IRMC-CALL-DATETIME, whose parameter (MISSED,
// or TYPE=MISSED in 3.0) is the kind, or the folder's without one; a time
// without a zone is in loc.
func ParseCall(card, folder string, loc *time.Location) (Call, error) {
	c := Call{Kind: historyKind[folder]}
	var when string
	for _, l := range unfoldLines(card) {
		name, params, value, ok := cardLine(l)
		if !ok {
			continue
		}
		switch name {
		case "TEL":
			if c.Number == "" {
				v := strings.TrimSpace(value)
				if len(v) > 4 && strings.EqualFold(v[:4], "tel:") {
					v, _, _ = strings.Cut(v[4:], ";")
				}
				c.Number = v
			}
		case "X-IRMC-CALL-DATETIME":
			when = value
			for _, p := range params {
				k := strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(p), "TYPE=")))
				if k == "RECEIVED" || k == "DIALED" || k == "MISSED" {
					c.Kind = k
				}
			}
		}
	}
	switch {
	case c.Number == "":
		return c, ErrNoNumber
	case strings.TrimSpace(when) == "":
		return c, ErrNoTime
	case c.Kind == "":
		return c, ErrNoKind
	}
	at, err := ParseTime(when, loc)
	if err != nil {
		return c, ErrNoTime
	}
	c.At = at
	return c, nil
}

// unfoldLines splits a vCard (or a bMessage's headers) into its lines, a
// line starting with a space or tab joined to the one before.
func unfoldLines(s string) []string {
	var out []string
	var cur strings.Builder
	started := false
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l != "" && (l[0] == ' ' || l[0] == '\t') && started {
			cur.WriteString(l[1:])
			continue
		}
		if started {
			out = append(out, cur.String())
		}
		cur.Reset()
		cur.WriteString(l)
		started = true
	}
	if started {
		out = append(out, cur.String())
	}
	return out
}

// cardLine splits a property line: its name in upper case without a
// group, its parameters and its value.
func cardLine(l string) (name string, params []string, value string, ok bool) {
	head, value, ok := strings.Cut(l, ":")
	if !ok {
		return "", nil, "", false
	}
	parts := strings.Split(head, ";")
	name = strings.ToUpper(strings.TrimSpace(parts[0]))
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name, parts[1:], value, true
}

// ParseTime reads PBAP's and MAP's times: YYYYMMDDTHHMMSS in local time
// (loc), or followed by Z or a UTC offset (MAP 1.3 and later).
func ParseTime(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.ParseInLocation("20060102T150405", s, loc); err == nil {
		return t, nil
	}
	var err error
	for _, f := range []string{"20060102T150405Z0700", "20060102T150405Z07:00", "2006-01-02T15:04:05Z07:00"} {
		var t time.Time
		if t, err = time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	if t, err2 := time.ParseInLocation("2006-01-02T15:04:05", s, loc); err2 == nil {
		return t, nil
	}
	return time.Time{}, err
}

// listingAttrs reads the elements named elem of a listing (PBAP's
// vcard-listing, MAP's msg-listing) leniently, as phones write them: each
// one's attributes by lower-case name; one without attr is left out.
func listingAttrs(b []byte, elem, attr string) []map[string]string {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	var out []map[string]string
	for {
		t, err := d.Token()
		if err != nil {
			return out
		}
		se, ok := t.(xml.StartElement)
		if !ok || !strings.EqualFold(se.Name.Local, elem) {
			continue
		}
		m := map[string]string{}
		for _, a := range se.Attr {
			m[strings.ToLower(a.Name.Local)] = a.Value
		}
		if m[attr] != "" {
			out = append(out, m)
		}
	}
}
