package contacts

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	utf16 "golang.org/x/text/encoding/unicode"

	"github.com/kario-os/kario/ui"
)

// vCard files (RFC 2426 3.0, RFC 6350 4.0, and the 2.1 that feature phones
// and car kits still write) are read leniently, as phones write them in
// many ways, and within bounds, as they come from anywhere (Files, a
// Bluetooth push).
const (
	MaxVCardBytes = 4 << 20
	// maxLine bounds an unfolded property, maxPhotoLine a PHOTO (base64
	// of MaxPhotoBytes); longer ones are skipped whole.
	maxLine      = 64 << 10
	maxPhotoLine = MaxPhotoBytes/3*4 + 4<<10
)

var (
	ErrTooBig   = fmt.Errorf("a vCard file is at most %d MB", MaxVCardBytes>>20)
	ErrNotVCard = errors.New("not a vCard file")
)

// IsVCard reports whether a file is a vCard by its name or type.
func IsVCard(name, mime string) bool {
	ext := strings.ToLower(name)
	m := strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))
	return strings.HasSuffix(ext, ".vcf") || strings.HasSuffix(ext, ".vcard") || m == "text/vcard" || m == "text/x-vcard" || m == "text/directory"
}

// Parse reads the contacts of a vCard file: the name from FN (from N or
// ORG without it), the numbers from TEL with their types, e-mail addresses
// from EMAIL, postal addresses from ADR, the photo from PHOTO (inline
// only: PhotoData, a picture the store keeps), the notes from NOTE, the
// nickname, organization and title from NICKNAME, ORG and TITLE, the
// birthday from BDAY, the anniversary from ANNIVERSARY, X-ANNIVERSARY or
// iPhone's labelled X-ABDATE, web addresses from URL; everything else is
// left out (groups too: a file's CATEGORIES are not
// the phone's groups). Bad is how many cards had neither a name nor a
// number, or came past MaxContacts. The contacts are Clean but have no
// pinyin.
func Parse(data []byte) (list []ui.Contact, bad int, err error) {
	cards, err := readCards(data, 0)
	if err != nil {
		return nil, 0, err
	}
	for _, card := range cards {
		c, err := card.contact()
		switch {
		case err != nil || len(list) >= MaxContacts:
			bad++
		default:
			list = append(list, c)
		}
	}
	return list, bad, nil
}

// readCards reads the cards of a file, at most limit of them (0: all).
func readCards(data []byte, limit int) ([]*rawCard, error) {
	if len(data) > MaxVCardBytes {
		return nil, ErrTooBig
	}
	data = toUTF8(data)
	var cards []*rawCard
	var card *rawCard
	depth := 0
	found := false
	for _, line := range unfold(data) {
		p, ok := parseLine(line)
		if !ok {
			continue
		}
		switch {
		case p.name == "BEGIN" && strings.EqualFold(strings.TrimSpace(p.value), "VCARD"):
			found = true
			if depth++; depth == 1 {
				card = &rawCard{}
			}
			continue
		case p.name == "END" && strings.EqualFold(strings.TrimSpace(p.value), "VCARD"):
			if depth > 0 {
				depth--
			}
			if depth == 0 && card != nil {
				cards = append(cards, card)
				card = nil
				if len(cards) == limit {
					return cards, nil
				}
			}
			continue
		}
		if depth == 1 && card != nil {
			card.add(p)
		}
	}
	if !found {
		return nil, ErrNotVCard
	}
	return cards, nil
}

// toUTF8 decodes a file with a UTF-16 byte order mark, and drops UTF-8's.
func toUTF8(b []byte) []byte {
	switch {
	case bytes.HasPrefix(b, []byte{0xef, 0xbb, 0xbf}):
		return b[3:]
	case bytes.HasPrefix(b, []byte{0xff, 0xfe}), bytes.HasPrefix(b, []byte{0xfe, 0xff}):
		if out, err := utf16.UTF16(utf16.LittleEndian, utf16.UseBOM).NewDecoder().Bytes(b); err == nil {
			return out
		}
	}
	return b
}

// unfold joins a property's lines: a line starting with a space or tab
// goes on the one before (folding, all versions), and so does the line
// after a quoted-printable value's soft break, a trailing "=" (2.1).
func unfold(data []byte) []string {
	var out []string
	var cur strings.Builder
	long := false
	limit := maxLine
	flush := func() {
		if cur.Len() > 0 && !long {
			out = append(out, cur.String())
		}
		cur.Reset()
		long, limit = false, maxLine
	}
	add := func(s string) {
		if long || cur.Len()+len(s) > limit {
			long = true
			return
		}
		cur.WriteString(s)
	}
	for _, l := range splitLines(data) {
		if !long && softBreak(cur.String()) {
			s := cur.String()
			cur.Reset()
			cur.WriteString(s[:len(s)-1])
			add(l)
			continue
		}
		if (cur.Len() > 0 || long) && l != "" && (l[0] == ' ' || l[0] == '\t') {
			add(l[1:])
			continue
		}
		flush()
		if isPhotoLine(l) {
			limit = maxPhotoLine
		}
		add(l)
	}
	flush()
	return out
}

// isPhotoLine: a line starts a PHOTO property (of a group, too).
func isPhotoLine(l string) bool {
	end := strings.IndexAny(l, ";:")
	if end < 0 {
		return false
	}
	name := l[:end]
	if j := strings.LastIndexByte(name, '.'); j >= 0 {
		name = name[j+1:]
	}
	return strings.EqualFold(strings.TrimSpace(name), "PHOTO")
}

func splitLines(data []byte) []string {
	s := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
	return strings.Split(s, "\n")
}

// softBreak: a quoted-printable property whose line ends in "=".
func softBreak(line string) bool {
	if !strings.HasSuffix(line, "=") {
		return false
	}
	i := valueStart(line)
	return i > 0 && strings.Contains(strings.ToUpper(line[:i]), "QUOTED-PRINTABLE")
}

// valueStart is the index of the colon before the value (not one inside a
// quoted parameter), -1 without one.
func valueStart(line string) int {
	quoted := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			quoted = !quoted
		case ':':
			if !quoted {
				return i
			}
		}
	}
	return -1
}

type property struct {
	name   string              // upper case, without its group
	group  string              // lower case ("item1" of item1.TEL), "" without one
	params map[string][]string // upper-case names; 2.1's bare ones under TYPE
	value  string
}

func parseLine(line string) (property, bool) {
	i := valueStart(line)
	if i <= 0 {
		return property{}, false
	}
	head, value := line[:i], line[i+1:]
	parts := splitParams(head)
	name := strings.ToUpper(strings.TrimSpace(parts[0]))
	group := ""
	if j := strings.LastIndexByte(name, '.'); j >= 0 {
		name, group = name[j+1:], strings.ToLower(name[:j])
	}
	p := property{name: name, group: group, params: map[string][]string{}, value: value}
	for _, param := range parts[1:] {
		k, v, ok := strings.Cut(param, "=")
		if !ok {
			k, v = "TYPE", param
		}
		k = strings.ToUpper(strings.TrimSpace(k))
		for _, x := range strings.Split(strings.Trim(strings.TrimSpace(v), `"`), ",") {
			if x = strings.TrimSpace(x); x != "" {
				p.params[k] = append(p.params[k], strings.ToUpper(x))
			}
		}
	}
	return p, true
}

// splitParams splits at semicolons outside quotes.
func splitParams(head string) []string {
	var out []string
	quoted, start := false, 0
	for i := 0; i < len(head); i++ {
		switch head[i] {
		case '"':
			quoted = !quoted
		case ';':
			if !quoted {
				out = append(out, head[start:i])
				start = i + 1
			}
		}
	}
	return append(out, head[start:])
}

func (p property) has(param, value string) bool {
	for _, v := range p.params[param] {
		if v == value {
			return true
		}
	}
	return false
}

// text is the value decoded: its transfer encoding, its charset (a value
// that is not UTF-8 without one is taken for GB 18030, which Chinese
// phones write), never invalid UTF-8.
func (p property) text() string {
	raw := []byte(p.value)
	switch {
	case p.has("ENCODING", "QUOTED-PRINTABLE") || p.has("TYPE", "QUOTED-PRINTABLE"):
		raw = quotedPrintable(raw)
	case p.has("ENCODING", "B") || p.has("ENCODING", "BASE64") || p.has("TYPE", "BASE64"):
		if b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(p.value), "")); err == nil {
			raw = b
		}
	}
	var enc encoding.Encoding
	if cs := p.params["CHARSET"]; len(cs) > 0 {
		switch cs[0] {
		case "GB2312", "GBK", "GB18030", "CP936", "EUC-CN":
			enc = simplifiedchinese.GB18030
		case "BIG5", "BIG-5", "CP950":
			enc = traditionalchinese.Big5
		case "ISO-8859-1", "LATIN1", "WINDOWS-1252", "CP1252":
			enc = charmap.Windows1252
		}
	}
	if enc == nil && !utf8.Valid(raw) {
		enc = simplifiedchinese.GB18030
	}
	if enc != nil {
		if out, err := enc.NewDecoder().Bytes(raw); err == nil {
			raw = out
		}
	}
	return strings.ToValidUTF8(string(raw), "")
}

// quotedPrintable decodes leniently: an "=" not followed by two hex digits
// stays as it is.
func quotedPrintable(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == '=' && i+2 < len(b) {
			if h, ok := unhex(b[i+1]); ok {
				if l, ok := unhex(b[i+2]); ok {
					out = append(out, h<<4|l)
					i += 2
					continue
				}
			}
		}
		out = append(out, b[i])
	}
	return out
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// components splits a structured value at unescaped semicolons and
// unescapes each part.
func components(v string) []string { return split(v, true) }

func unescape(v string) string { return split(v, false)[0] }

func split(v string, structured bool) []string {
	var out []string
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		switch {
		case v[i] == '\\' && i+1 < len(v):
			i++
			switch v[i] {
			case 'n', 'N':
				b.WriteByte('\n')
			default:
				b.WriteByte(v[i])
			}
		case v[i] == ';' && structured:
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(v[i])
		}
	}
	return append(out, b.String())
}

type rawCard struct {
	fn, n, org string
	numbers    []ui.ContactNumber
	emails     []ui.ContactEmail
	addresses  []ui.ContactAddress
	photo      []byte
	notes      []string
	// The other properties the store shows. abDates are Apple's dates
	// (itemN.X-ABDATE) by group, abLabels the groups' X-ABLabel, which
	// says which date is the anniversary.
	nickname, title, bday, anniversary string
	urls                               []string
	abDates                            []abDate
	abLabels                           map[string]string
	// uid, version, kind, members (a group's, by UID), categories and a
	// photo at a URL are what a CardDAV card needs besides (ParseCard).
	uid, version, kind string
	members            []string
	categories         []string
	photoURI           string
}

// abDate is an X-ABDATE's group and date.
type abDate struct{ group, value string }

// appleAnniversary is the X-ABLabel of iPhone Contacts' anniversary date.
const appleAnniversary = "_$!<anniversary>!$_"

func (c *rawCard) add(p property) {
	switch p.name {
	case "NICKNAME":
		if c.nickname == "" {
			var names []string
			for _, n := range splitList(p.text()) {
				if n = strings.TrimSpace(n); n != "" {
					names = append(names, n)
				}
			}
			c.nickname = strings.Join(names, ", ")
		}
	case "TITLE":
		if c.title == "" {
			c.title = strings.TrimSpace(unescape(p.text()))
		}
	case "BDAY":
		if c.bday == "" {
			c.bday = dateOf(p)
		}
	case "ANNIVERSARY", "X-ANNIVERSARY":
		if c.anniversary == "" {
			c.anniversary = dateOf(p)
		}
	case "X-ABDATE":
		if d := dateOf(p); d != "" && p.group != "" && len(c.abDates) < 16 {
			c.abDates = append(c.abDates, abDate{p.group, d})
		}
	case "X-ABLABEL":
		if p.group != "" {
			if c.abLabels == nil {
				c.abLabels = map[string]string{}
			}
			c.abLabels[p.group] = strings.ToLower(strings.TrimSpace(unescape(p.text())))
		}
	case "URL":
		if u := strings.TrimSpace(unescape(p.text())); u != "" && len(c.urls) < MaxURLs*2 {
			c.urls = append(c.urls, u)
		}
	case "EMAIL":
		if a := emailValue(p); a != "" && len(c.emails) < MaxEmails*2 {
			c.emails = append(c.emails, ui.ContactEmail{Address: a, Label: placeLabel(p)})
		}
	case "ADR":
		if len(c.addresses) < MaxAddresses*2 {
			c.addresses = append(c.addresses, addressOf(p))
		}
	case "PHOTO":
		if c.photo == nil && c.photoURI == "" {
			if b := p.picture(); PhotoOK(b) {
				c.photo = b
			} else if b == nil {
				c.photoURI = photoURI(p)
			}
		}
	case "CATEGORIES":
		for _, n := range splitList(p.text()) {
			if n = cut(oneLine(n), MaxGroupName); n != "" && !slices.ContainsFunc(c.categories, func(x string) bool { return strings.EqualFold(x, n) }) {
				c.categories = append(c.categories, n)
			}
		}
	case "MEMBER", "X-ADDRESSBOOKSERVER-MEMBER":
		if m := MemberUID(strings.TrimSpace(unescape(p.value))); m != "" {
			c.members = append(c.members, m)
		}
	case "UID":
		if c.uid == "" {
			c.uid = strings.TrimSpace(unescape(p.text()))
		}
	case "VERSION":
		c.version = strings.TrimSpace(p.value)
	case "KIND", "X-ADDRESSBOOKSERVER-KIND":
		c.kind = strings.ToLower(strings.TrimSpace(p.value))
	case "FN":
		if c.fn == "" {
			c.fn = strings.TrimSpace(unescape(p.text()))
		}
	case "N":
		if c.n == "" {
			c.n = structuredName(components(p.text()))
		}
	case "ORG":
		if c.org == "" {
			c.org = strings.TrimSpace(components(p.text())[0])
		}
	case "TEL":
		v := unescape(p.text())
		// A tel: URI (4.0) may have parameters (;ext=12).
		if uri, ok := cutPrefixFold(strings.TrimSpace(v), "tel:"); ok {
			v, _, _ = strings.Cut(uri, ";")
		}
		if len(c.numbers) < MaxNumbers*2 {
			c.numbers = append(c.numbers, ui.ContactNumber{Number: v, Label: telLabel(p)})
		}
	case "NOTE":
		if n := strings.TrimSpace(unescape(p.text())); n != "" && len(c.notes) < 16 {
			c.notes = append(c.notes, n)
		}
	}
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// structuredName writes N's family and given names the way the name is
// said: together when they are Chinese, given name first otherwise.
func structuredName(parts []string) string {
	get := func(i int) string {
		if i < len(parts) {
			return strings.TrimSpace(parts[i])
		}
		return ""
	}
	family, given, middle := get(0), get(1), get(2)
	if hasHan(family) || hasHan(given) {
		return family + middle + given
	}
	return strings.Join(strings.Fields(given+" "+middle+" "+family), " ")
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// placeLabel maps EMAIL's and ADR's types to the store's labels.
func placeLabel(p property) string {
	switch {
	case p.has("TYPE", "WORK"):
		return ui.NumberWork
	case p.has("TYPE", "HOME"):
		return ui.NumberHome
	}
	return ui.NumberOther
}

// picture is a PHOTO's bytes: base64 (ENCODING=b, 3.0's and 2.1's) or a
// data: URI (4.0's); nil for a URL (photoURI), which only a sync may
// fetch, from its book's own server (package carddav).
func (p property) picture() []byte {
	v := strings.TrimSpace(p.value)
	b64 := p.has("ENCODING", "B") || p.has("ENCODING", "BASE64") || p.has("TYPE", "BASE64")
	if rest, ok := cutPrefixFold(v, "data:"); ok && !b64 {
		head, data, ok := strings.Cut(rest, ",")
		if !ok || !strings.HasSuffix(strings.ToLower(head), ";base64") {
			return nil
		}
		v, b64 = data, true
	}
	if !b64 {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(v), ""))
	if err != nil {
		return nil
	}
	return raw
}

// splitList splits a list value (CATEGORIES) at unescaped commas.
func splitList(v string) []string {
	var out []string
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		switch {
		case v[i] == '\\' && i+1 < len(v):
			i++
			b.WriteByte(v[i])
		case v[i] == ',':
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(v[i])
		}
	}
	return append(out, b.String())
}

// MemberUID is the UID a group's MEMBER names (urn:uuid:<UID>, or the UID
// alone) as UIDKey has it; "" for another kind of member (a mailto:).
func MemberUID(ref string) string {
	ref = strings.TrimSpace(ref)
	if _, ok := cutPrefixFold(ref, "urn:uuid:"); !ok && strings.Contains(ref, ":") {
		return ""
	}
	return UIDKey(ref)
}

// UIDKey is how a card's UID is compared with a group's members: in any
// case, without urn:uuid:.
func UIDKey(uid string) string {
	uid, _ = cutPrefixFold(strings.TrimSpace(uid), "urn:uuid:")
	return strings.ToLower(uid)
}

// memberRef is how a group names the card with that UID.
func memberRef(uid string) string {
	if _, ok := cutPrefixFold(uid, "urn:uuid:"); ok {
		return uid
	}
	return "urn:uuid:" + uid
}

// telLabel maps TEL's types to the store's labels: a mobile first, then
// work, then home.
func telLabel(p property) string {
	switch {
	case p.has("TYPE", "CELL") || p.has("TYPE", "MOBILE") || p.has("TYPE", "IPHONE"):
		return ui.NumberMobile
	case p.has("TYPE", "WORK"):
		return ui.NumberWork
	case p.has("TYPE", "HOME"):
		return ui.NumberHome
	}
	return ui.NumberOther
}

func (c *rawCard) contact() (ui.Contact, error) {
	name := c.fn
	if name == "" {
		name = c.n
	}
	if name == "" {
		name = c.org
	}
	out := ui.Contact{Name: name, Numbers: c.numbers, Emails: c.emails, Addresses: c.addresses, Notes: strings.Join(c.notes, "\n"),
		Nickname: c.nickname, Org: c.org, Title: c.title, Birthday: c.bday, Anniversary: c.anniversary, URLs: c.urls}
	if out.Anniversary == "" {
		for _, d := range c.abDates {
			if c.abLabels[d.group] == appleAnniversary {
				out.Anniversary = d.value
				break
			}
		}
	}
	if c.photo != nil {
		out.Photo, out.PhotoData = PhotoKey(c.photo), c.photo
	}
	return Clean(out, nil)
}

// dateOf is a BDAY's or an anniversary's date as the store keeps it, ""
// for a text or a value it does not read: 4.0's 19850412 and --0412,
// 3.0's 1985-04-12 and --04-12, a date-time's date, and a year Apple's
// X-APPLE-OMIT-YEAR says is none (iPhone Contacts writes 1604-04-12).
func dateOf(p property) string {
	if p.has("VALUE", "TEXT") {
		return ""
	}
	v := strings.TrimSpace(unescape(p.text()))
	if i := strings.IndexByte(v, 'T'); i > 0 {
		v = v[:i]
	}
	var y, md string
	switch {
	case len(v) == 10 && v[4] == '-' && v[7] == '-':
		y, md = v[:4], v[5:]
	case len(v) == 8 && !strings.Contains(v, "-"):
		y, md = v[:4], v[4:6]+"-"+v[6:]
	case len(v) == 7 && strings.HasPrefix(v, "--") && v[4] == '-':
		md = v[2:]
	case len(v) == 6 && strings.HasPrefix(v, "--"):
		md = v[2:4] + "-" + v[4:]
	default:
		return ""
	}
	if omit := p.params["X-APPLE-OMIT-YEAR"]; len(omit) > 0 && omit[0] == y {
		y = ""
	}
	s := "--" + md
	if y != "" {
		s = y + "-" + md
	}
	return cleanDate(s)
}

// dateLine is a date property as a card writes it: 4.0's basic format
// (--MMDD without the year); 3.0's extended one, which has no date without
// a year, so such a date goes as Apple's year 1604 with X-APPLE-OMIT-YEAR,
// which iPhone Contacts and DAVx⁵ read.
func dateLine(name, date string, v4 bool) string {
	y, m, d, ok := ui.ContactDateParts(date)
	switch {
	case !ok:
		return ""
	case v4 && y == 0:
		return fmt.Sprintf("%s:--%02d%02d", name, m, d)
	case v4:
		return fmt.Sprintf("%s:%04d%02d%02d", name, y, m, d)
	case y == 0:
		return fmt.Sprintf("%s;X-APPLE-OMIT-YEAR=1604:1604-%02d-%02d", name, m, d)
	}
	return fmt.Sprintf("%s:%04d-%02d-%02d", name, y, m, d)
}

// anniversaryName is the property an anniversary gets: 4.0's own, 3.0's
// X-ANNIVERSARY (what Android, Evolution and DAVx⁵ write).
func anniversaryName(v4 bool) string {
	if v4 {
		return "ANNIVERSARY"
	}
	return "X-ANNIVERSARY"
}

// nicknameLine is NICKNAME for the store's nickname, its ", " the list's
// commas.
func nicknameLine(group, nick string) string {
	var esc []string
	for _, n := range strings.Split(nick, ",") {
		if n = strings.TrimSpace(n); n != "" {
			esc = append(esc, escape(n))
		}
	}
	return prefix(group) + "NICKNAME:" + strings.Join(esc, ",")
}

// photoURI is a PHOTO's URL (4.0's value, 3.0's VALUE=uri; a path on the
// server too), "" for none.
func photoURI(p property) string {
	v := strings.TrimSpace(p.value)
	uri := p.has("VALUE", "URI") || p.has("VALUE", "URL")
	if _, ok := cutPrefixFold(v, "data:"); ok || v == "" || strings.ContainsAny(v, " \t") || !strings.Contains(v, ":") && !(uri && v[0] == '/') {
		return ""
	}
	return v
}

// otherLines are the lines of the other properties the store shows, for a
// new card or a file.
func otherLines(c ui.Contact, v4 bool) []string {
	var out []string
	if c.Nickname != "" {
		out = append(out, nicknameLine("", c.Nickname))
	}
	if c.Org != "" {
		out = append(out, "ORG:"+escape(c.Org))
	}
	if c.Title != "" {
		out = append(out, "TITLE:"+escape(c.Title))
	}
	if c.Birthday != "" {
		out = append(out, dateLine("BDAY", c.Birthday, v4))
	}
	if c.Anniversary != "" {
		out = append(out, dateLine(anniversaryName(v4), c.Anniversary, v4))
	}
	for _, u := range c.URLs {
		out = append(out, "URL:"+u)
	}
	return out
}

var vcardLabel = map[string]string{ui.NumberMobile: "CELL", ui.NumberHome: "HOME", ui.NumberWork: "WORK", ui.NumberOther: "VOICE"}

// Write is the contacts as a vCard 3.0 file, which phones and computers
// import: names, numbers, e-mail and postal addresses, notes, nicknames,
// organizations and titles, birthdays and anniversaries, web addresses,
// and the photos of those that come with their PhotoData.
func Write(list []ui.Contact) []byte {
	var b bytes.Buffer
	for _, c := range list {
		b.WriteString("BEGIN:VCARD\r\nVERSION:3.0\r\n")
		fold(&b, "FN:"+escape(c.Name))
		fold(&b, "N:"+escape(c.Name)+";;;;")
		for _, n := range ui.NumbersOf(c) {
			fold(&b, "TEL;TYPE="+vcardLabel[cleanLabel(n.Label)]+":"+escape(n.Number))
		}
		for _, e := range c.Emails {
			fold(&b, emailLine(e, false))
		}
		for _, a := range c.Addresses {
			fold(&b, adrLine(a, false))
		}
		if c.Notes != "" {
			fold(&b, "NOTE:"+escape(c.Notes))
		}
		for _, l := range otherLines(c, false) {
			fold(&b, l)
		}
		if c.PhotoData != nil {
			fold(&b, photoLine(c.PhotoData, false))
		}
		b.WriteString("END:VCARD\r\n")
	}
	return b.Bytes()
}

var placeType = map[string]string{ui.NumberHome: "HOME", ui.NumberWork: "WORK"}

func emailLine(e ui.ContactEmail, v4 bool) string {
	head := "EMAIL"
	if t := emailType(e.Label, v4); t != "" {
		head += ";TYPE=" + t
	}
	return head + ":" + escape(e.Address)
}

func adrLine(a ui.ContactAddress, v4 bool) string {
	head := "ADR"
	if t := placeParam(a.Label, v4); t != "" {
		head += ";TYPE=" + t
	}
	parts := []string{a.POBox, a.Extended, a.Street, a.City, a.Region, a.Postcode, a.Country}
	for i := range parts {
		parts[i] = escape(parts[i])
	}
	return head + ":" + strings.Join(parts, ";")
}

// photoLine is a picture inline: 3.0's ENCODING=b, 4.0's data: URI.
func photoLine(data []byte, v4 bool) string {
	mime, t := "image/jpeg", "JPEG"
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG")):
		mime, t = "image/png", "PNG"
	case bytes.HasPrefix(data, []byte("GIF8")):
		mime, t = "image/gif", "GIF"
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	if v4 {
		return "PHOTO:data:" + mime + ";base64," + b64
	}
	return "PHOTO;ENCODING=b;TYPE=" + t + ":" + b64
}

func categoriesLine(names []string) string {
	esc := make([]string, len(names))
	for i, n := range names {
		esc[i] = escape(n)
	}
	return "CATEGORIES:" + strings.Join(esc, ",")
}

func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", "").Replace(s)
}

// fold writes a line in pieces of at most 75 bytes, never inside a
// character, each after the first starting with a space.
func fold(b *bytes.Buffer, line string) { foldEOL(b, line, "\r\n") }

func foldEOL(b *bytes.Buffer, line, eol string) {
	limit := 75
	for len(line) > limit {
		i := limit
		for i > 0 && !utf8.RuneStart(line[i]) {
			i--
		}
		if i == 0 {
			// Not UTF-8 (a server's card may be anything): cut anywhere.
			i = limit
		}
		b.WriteString(line[:i])
		b.WriteString(eol + " ")
		line = line[i:]
		limit = 74
	}
	b.WriteString(line)
	b.WriteString(eol)
}
