package contacts

import (
	"bytes"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kario-os/kario/ui"
)

// Cards are vCards one at a time, as a CardDAV server keeps them (package
// carddav). The store shows a card's name, numbers, e-mail and postal
// addresses, photo, notes, nickname, organization, title, birthday,
// anniversary and web addresses, and a sync that keeps groups in them its
// CATEGORIES; the rest of it (what other programs write) stays the
// server's: a change made on the phone goes back as the server's own card
// with just what changed rewritten (PatchCard), everything else byte for
// byte as it came, a photo included.

// CardInfo is what a sync needs of a card besides the contact: its UID,
// its VERSION, whether it is a group (vCard 4.0's KIND:group, Apple's
// X-ADDRESSBOOKSERVER-KIND), which is no contact: the contact's name is
// the group's, Members its members (UIDKey); and its photo's URL when the
// photo is one (PhotoURI, as written: it may be relative).
type CardInfo struct {
	UID      string
	Version  string
	Group    bool
	Members  []string
	PhotoURI string
}

// ParseCard reads one card (the first, if the data holds more) as Parse
// reads a file's, its CATEGORIES as the contact's Groups (names): Clean,
// without pinyin; ErrEmpty when it has neither a name nor a number, and
// then the contact has only the notes.
func ParseCard(data []byte) (ui.Contact, CardInfo, error) {
	cards, err := readCards(data, 1)
	if err != nil {
		return ui.Contact{}, CardInfo{}, err
	}
	if len(cards) == 0 {
		return ui.Contact{}, CardInfo{}, ErrNotVCard
	}
	r := cards[0]
	info := CardInfo{UID: r.uid, Version: r.version, Group: r.kind == "group", Members: r.members, PhotoURI: r.photoURI}
	c, err := r.contact()
	if err == ErrEmpty {
		c.Notes = cut(strings.TrimSpace(text(strings.Join(r.notes, "\n"))), MaxNotes)
	}
	c.Groups = r.categories
	return c, info, err
}

// NewCard is a contact as a new card: version "4.0", or 3.0 for anything
// else (what iPhone Contacts and the Kario account server read), with the
// photo PhotoData holds and its Groups (names) as CATEGORIES. at is
// written as its REV.
func NewCard(c ui.Contact, uid, version string, at time.Time) []byte {
	v4 := version == "4.0"
	var b bytes.Buffer
	b.WriteString("BEGIN:VCARD\r\n")
	if v4 {
		b.WriteString("VERSION:4.0\r\n")
	} else {
		b.WriteString("VERSION:3.0\r\n")
	}
	fold(&b, "UID:"+escape(uid))
	fold(&b, "FN:"+escape(c.Name))
	fold(&b, nLine("", c.Name))
	for _, n := range ui.NumbersOf(c) {
		fold(&b, telLine("", n, v4))
	}
	for _, e := range c.Emails {
		fold(&b, emailLine(e, v4))
	}
	for _, a := range c.Addresses {
		fold(&b, adrLine(a, v4))
	}
	if c.Notes != "" {
		fold(&b, "NOTE:"+escape(c.Notes))
	}
	for _, l := range otherLines(c, v4) {
		fold(&b, l)
	}
	if len(c.Groups) > 0 {
		fold(&b, categoriesLine(c.Groups))
	}
	if c.PhotoData != nil {
		fold(&b, photoLine(c.PhotoData, v4))
	}
	fold(&b, "REV:"+rev(at, v4))
	b.WriteString("END:VCARD\r\n")
	return b.Bytes()
}

// multi is a property a card may have several of (TEL, EMAIL, ADR) as
// PatchCard rewrites it: each value by its key, with its label as the
// store shows raw (shown) and as now has it (want).
type multi struct {
	shown, want map[string]string
	have        map[string]bool
	// last: the index in the card's lines after its last one, -1 none.
	last int
}

func newMulti() *multi {
	return &multi{shown: map[string]string{}, want: map[string]string{}, have: map[string]bool{}, last: -1}
}

func (m *multi) changed() bool {
	if len(m.shown) != len(m.want) {
		return true
	}
	for k, l := range m.want {
		if m.shown[k] != l {
			return true
		}
	}
	return false
}

// SameNames reports whether two lists of group names hold the same names,
// in any case and order.
func SameNames(a, b []string) bool {
	set := func(l []string) []string {
		out := make([]string, 0, len(l))
		for _, n := range l {
			if n = strings.ToLower(n); !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
		sort.Strings(out)
		return out
	}
	return slices.Equal(set(a), set(b))
}

// PatchCard is raw, a card as the server has it, changed to show now: the
// name (FN and N), the numbers (TEL), e-mail and postal addresses (EMAIL,
// ADR), the photo (PHOTO: now's PhotoData when its Photo changed), the
// notes (NOTE), nickname (NICKNAME), organization (ORG's first part, the
// units after it kept), title (TITLE), birthday (BDAY), anniversary
// (ANNIVERSARY, X-ANNIVERSARY or iPhone's labelled X-ABDATE, as the card
// has it) and web addresses (URL), and with categories its Groups (names)
// as CATEGORIES, are written again where now differs from what the store
// makes of raw, and REV, if the card has one, becomes at. A value the
// store does not show (past its limit, a number a phone does not dial, a
// photo it does not keep) stays as it is, as does every other property and
// line. Unchanged, raw comes back as it is.
func PatchCard(raw []byte, now ui.Contact, categories bool, at time.Time) ([]byte, error) {
	return PatchCardFetched(raw, now, categories, at, "")
}

// PatchCardFetched is PatchCard for a card whose PHOTO is a URL
// (CardInfo.PhotoURI) that a sync fetched as the picture of key photo: the
// PHOTO line stays as long as now's Photo is that picture.
func PatchCardFetched(raw []byte, now ui.Contact, categories bool, at time.Time, photo string) ([]byte, error) {
	base, info, err := ParseCard(raw)
	if err != nil && err != ErrEmpty {
		return nil, err
	}
	if photo != "" && info.PhotoURI != "" && base.Photo == "" {
		base.Photo = photo
	}
	groups := now.Groups
	now, err = Clean(now, nil)
	if err != nil {
		return nil, err
	}
	nameChanged := now.Name != base.Name
	notesChanged := now.Notes != base.Notes
	photoChanged := now.Photo != base.Photo
	catsChanged := categories && !SameNames(groups, base.Groups)
	nickChanged, orgChanged, titleChanged := now.Nickname != base.Nickname, now.Org != base.Org, now.Title != base.Title
	bdayChanged, annChanged := now.Birthday != base.Birthday, now.Anniversary != base.Anniversary
	tel, email, adr, web := newMulti(), newMulti(), newMulti(), newMulti()
	for _, u := range base.URLs {
		web.shown[u] = "url"
	}
	for _, u := range now.URLs {
		web.want[u] = "url"
	}
	for _, n := range base.Numbers {
		tel.shown[ui.NumberKey(n.Number)] = n.Label
	}
	for _, n := range now.Numbers {
		tel.want[ui.NumberKey(n.Number)] = n.Label
	}
	for _, e := range base.Emails {
		email.shown[EmailKey(e.Address)] = e.Label
	}
	for _, e := range now.Emails {
		email.want[EmailKey(e.Address)] = e.Label
	}
	for _, a := range base.Addresses {
		adr.shown[AddressKey(a)] = a.Label
	}
	for _, a := range now.Addresses {
		adr.want[AddressKey(a)] = a.Label
	}
	others := nickChanged || orgChanged || titleChanged || bdayChanged || annChanged || web.changed()
	if !nameChanged && !notesChanged && !photoChanged && !catsChanged && !tel.changed() && !email.changed() && !adr.changed() && !others {
		return raw, nil
	}
	if photoChanged && now.Photo != "" && (now.PhotoData == nil || PhotoKey(now.PhotoData) != now.Photo) {
		return nil, ErrNoPhoto
	}
	v4 := info.Version == "4.0"
	text := string(toUTF8(raw))
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	lines := cardLines(text)
	begin, end := cardBounds(lines)
	if begin < 0 || end < 0 {
		return nil, ErrNotVCard
	}
	newLine := func(s string) string {
		var b bytes.Buffer
		foldEOL(&b, s, eol)
		return b.String()
	}
	// iPhone Contacts' anniversary is the X-ABDATE of a group labelled so.
	annGroups := map[string]bool{}
	for _, l := range lines[begin+1 : end] {
		if p, ok := parseLine(l.text); ok && p.name == "X-ABLABEL" && p.group != "" && strings.ToLower(strings.TrimSpace(unescape(p.text()))) == appleAnniversary {
			annGroups[p.group] = true
		}
	}
	var out []string
	out = append(out, rawOf(lines[:begin+1])...)
	hasFN, hasN, wroteNote := false, false, false
	hadNick, hadOrg, hadTitle, hadBday, hadAnn := false, false, false, false, false
	afterVersion := -1
	dropGroups := map[string]bool{}
	keepGroups := map[string]bool{}
	type grouped struct {
		at    int
		group string
	}
	var labels []grouped
	nested := 0
	// each is one of the values of m on line l: dropped when now does not
	// have it, retyped when its label changed, kept otherwise.
	each := func(m *multi, k, group string, l cardLine, retyped func() string) {
		label, isShown := m.shown[k]
		switch {
		case isShown && m.want[k] == "":
			dropGroups[group] = true
			return
		case isShown && !m.have[k] && m.want[k] != label:
			// Retyped outside its group: the group's label would
			// still say the old type.
			dropGroups[group] = true
			out = append(out, newLine(retyped()))
		default:
			keepGroups[group] = true
			out = append(out, l.raw)
		}
		m.have[k] = m.have[k] || isShown
		m.last = len(out)
	}
	for _, l := range lines[begin+1 : end] {
		p, ok := parseLine(l.text)
		if ok && strings.EqualFold(strings.TrimSpace(p.value), "VCARD") {
			switch p.name {
			case "BEGIN":
				nested++
			case "END":
				nested = max(0, nested-1)
			}
		}
		if !ok || nested > 0 || p.name == "END" {
			// A card inside the card (2.1's AGENT) is not the contact's.
			out = append(out, l.raw)
			continue
		}
		group := groupOf(l.text)
		switch p.name {
		case "VERSION":
			out = append(out, l.raw)
			afterVersion = len(out)
			continue
		case "FN":
			first := !hasFN
			hasFN = true
			if !nameChanged {
				break
			}
			if first {
				out = append(out, newLine(prefix(group)+"FN:"+escape(now.Name)))
			}
			continue
		case "N":
			first := !hasN
			hasN = true
			if !nameChanged {
				break
			}
			if first {
				out = append(out, newLine(nLine(group, now.Name)))
			}
			continue
		case "NOTE":
			if !notesChanged {
				break
			}
			if !wroteNote && now.Notes != "" {
				out = append(out, newLine(prefix(group)+"NOTE:"+escape(now.Notes)))
			}
			wroteNote = true
			continue
		case "NICKNAME", "ORG", "TITLE":
			// The first is the one shown, and the one rewritten.
			had, changed, value := &hadNick, nickChanged, now.Nickname
			switch p.name {
			case "ORG":
				had, changed, value = &hadOrg, orgChanged, now.Org
			case "TITLE":
				had, changed, value = &hadTitle, titleChanged, now.Title
			}
			first := !*had
			*had = true
			if !changed || !first {
				break
			}
			switch {
			case value == "":
				dropGroups[group] = true
			case p.name == "NICKNAME":
				out = append(out, newLine(nicknameLine(group, value)))
			case p.name == "ORG":
				out = append(out, newLine(prefix(group)+"ORG:"+escape(value)+orgUnits(p)))
			default:
				out = append(out, newLine(prefix(group)+"TITLE:"+escape(value)))
			}
			if value != "" {
				keepGroups[group] = true
			}
			continue
		case "BDAY":
			if !bdayChanged {
				break
			}
			if !hadBday && now.Birthday != "" {
				out = append(out, newLine(prefix(group)+dateLine("BDAY", now.Birthday, v4)))
				keepGroups[group] = true
			} else {
				dropGroups[group] = true
			}
			hadBday = true
			continue
		case "ANNIVERSARY", "X-ANNIVERSARY", "X-ABDATE":
			if !annChanged || p.name == "X-ABDATE" && !annGroups[p.group] {
				break
			}
			if !hadAnn && now.Anniversary != "" {
				out = append(out, newLine(prefix(group)+dateLine(p.name, now.Anniversary, v4 && p.name == "ANNIVERSARY")))
				keepGroups[group] = true
			} else {
				dropGroups[group] = true
			}
			hadAnn = true
			continue
		case "URL":
			k := cleanURL(unescape(p.text()))
			each(web, k, group, l, func() string { return l.text })
			continue
		case "REV":
			out = append(out, newLine(prefix(group)+"REV:"+rev(at, v4)))
			continue
		case "TEL":
			k := ui.NumberKey(cleanNumber(telValue(p)))
			each(tel, k, group, l, func() string { return retype(l.text, tel.want[k], v4) })
			continue
		case "EMAIL":
			k := EmailKey(cleanEmail(emailValue(p)))
			each(email, k, group, l, func() string { return retypeAs(l.text, "EMAIL", emailType(email.want[k], v4)) })
			continue
		case "ADR":
			k := AddressKey(cleanAddress(addressOf(p)))
			each(adr, k, group, l, func() string { return retypeAs(l.text, "ADR", placeParam(adr.want[k], v4)) })
			continue
		case "PHOTO":
			if photoChanged {
				dropGroups[group] = true
				continue
			}
		case "CATEGORIES":
			if catsChanged {
				continue
			}
		case "X-ABLABEL":
			labels = append(labels, grouped{len(out), group})
		default:
			if group != "" {
				keepGroups[group] = true
			}
		}
		out = append(out, l.raw)
	}
	// Apple names a value's label in a property of its group
	// (item1.X-ABLabel); it goes with the last value of its group.
	for i := len(labels) - 1; i >= 0; i-- {
		if g := labels[i]; g.group != "" && dropGroups[g.group] && !keepGroups[g.group] {
			out[g.at] = ""
		}
	}
	// insert[i] are the lines that go before out[i].
	insert := map[int][]string{}
	telAt := tel.last
	if telAt < 0 {
		telAt = len(out)
	}
	for _, n := range now.Numbers {
		if k := ui.NumberKey(n.Number); !tel.have[k] {
			tel.have[k] = true
			insert[telAt] = append(insert[telAt], newLine(telLine("", n, v4)))
		}
	}
	emailAt := email.last
	if emailAt < 0 {
		emailAt = telAt
	}
	for _, e := range now.Emails {
		if k := EmailKey(e.Address); !email.have[k] {
			email.have[k] = true
			insert[emailAt] = append(insert[emailAt], newLine(emailLine(e, v4)))
		}
	}
	adrAt := adr.last
	if adrAt < 0 {
		adrAt = emailAt
	}
	for _, a := range now.Addresses {
		if k := AddressKey(a); !adr.have[k] {
			adr.have[k] = true
			insert[adrAt] = append(insert[adrAt], newLine(adrLine(a, v4)))
		}
	}
	if notesChanged && !wroteNote && now.Notes != "" {
		insert[telAt] = append(insert[telAt], newLine("NOTE:"+escape(now.Notes)))
	}
	for _, x := range []struct {
		add  bool
		line string
	}{
		{nickChanged && !hadNick && now.Nickname != "", nicknameLine("", now.Nickname)},
		{orgChanged && !hadOrg && now.Org != "", "ORG:" + escape(now.Org)},
		{titleChanged && !hadTitle && now.Title != "", "TITLE:" + escape(now.Title)},
		{bdayChanged && !hadBday && now.Birthday != "", dateLine("BDAY", now.Birthday, v4)},
		{annChanged && !hadAnn && now.Anniversary != "", dateLine(anniversaryName(v4), now.Anniversary, v4)},
	} {
		if x.add {
			insert[telAt] = append(insert[telAt], newLine(x.line))
		}
	}
	webAt := web.last
	if webAt < 0 {
		webAt = adrAt
	}
	for _, u := range now.URLs {
		if !web.have[u] {
			web.have[u] = true
			insert[webAt] = append(insert[webAt], newLine("URL:"+u))
		}
	}
	if catsChanged && len(groups) > 0 {
		insert[len(out)] = append(insert[len(out)], newLine(categoriesLine(groups)))
	}
	if photoChanged && now.Photo != "" {
		insert[len(out)] = append(insert[len(out)], newLine(photoLine(now.PhotoData, v4)))
	}
	if afterVersion < 0 {
		afterVersion = begin + 1
	}
	// A card without FN shows a name made of N, ORG or its first number,
	// which the change may have changed: it gets the name for its own (a
	// server wants an FN anyway, and 3.0 an N).
	var head []string
	if !hasFN {
		head = append(head, newLine("FN:"+escape(now.Name)))
	}
	if !hasN && !v4 {
		head = append(head, newLine(nLine("", now.Name)))
	}
	insert[afterVersion] = append(head, insert[afterVersion]...)
	var b strings.Builder
	for i := 0; i <= len(out); i++ {
		for _, s := range insert[i] {
			b.WriteString(s)
		}
		if i < len(out) {
			b.WriteString(out[i])
		}
	}
	for _, s := range rawOf(lines[end:]) {
		b.WriteString(s)
	}
	return []byte(b.String()), nil
}

// cardBounds are the indexes of the first card's BEGIN and END, -1 when
// it has none.
func cardBounds(lines []cardLine) (begin, end int) {
	begin, end = -1, -1
	depth := 0
	for i, l := range lines {
		p, ok := parseLine(l.text)
		if !ok {
			continue
		}
		isCard := strings.EqualFold(strings.TrimSpace(p.value), "VCARD")
		switch {
		case p.name == "BEGIN" && isCard:
			if depth++; depth == 1 && begin < 0 {
				begin = i
			}
		case p.name == "END" && isCard && depth > 0:
			if depth--; depth == 0 && begin >= 0 && end < 0 {
				end = i
			}
		}
	}
	return begin, end
}

// NewGroupCard is a group as a new card: vCard 4.0's KIND:group and
// MEMBER, or for 3.0 Apple's X-ADDRESSBOOKSERVER-KIND and -MEMBER (what
// iPhone Contacts and iCloud read). members are the UIDs of its cards.
func NewGroupCard(name, uid string, members []string, version string, at time.Time) []byte {
	v4 := version == "4.0"
	var b bytes.Buffer
	b.WriteString("BEGIN:VCARD\r\n")
	if v4 {
		b.WriteString("VERSION:4.0\r\nKIND:group\r\n")
	} else {
		b.WriteString("VERSION:3.0\r\nX-ADDRESSBOOKSERVER-KIND:group\r\n")
	}
	fold(&b, "UID:"+escape(uid))
	fold(&b, "FN:"+escape(name))
	if !v4 {
		fold(&b, "N:"+escape(name)+";;;;")
	}
	for _, m := range members {
		if v4 {
			fold(&b, "MEMBER:"+memberRef(m))
		} else {
			fold(&b, "X-ADDRESSBOOKSERVER-MEMBER:"+memberRef(m))
		}
	}
	fold(&b, "REV:"+rev(at, v4))
	b.WriteString("END:VCARD\r\n")
	return b.Bytes()
}

// PatchGroupCard is a group's card with the name and members (the UIDs of
// its cards) now: FN (and N) written again when the name changed, the
// member lines of UIDs no longer in it dropped and the new ones added the
// way the card writes its members, REV moved; the rest as it was.
// Unchanged, raw comes back as it is.
func PatchGroupCard(raw []byte, name string, members []string, at time.Time) ([]byte, error) {
	base, info, err := ParseCard(raw)
	if err != nil && err != ErrEmpty {
		return nil, err
	}
	name = cut(oneLine(name), MaxGroupName)
	want := map[string]bool{}
	for _, m := range members {
		want[UIDKey(m)] = true
	}
	had := map[string]bool{}
	for _, m := range info.Members {
		had[m] = true
	}
	sameMembers := len(had) == len(want)
	for k := range want {
		sameMembers = sameMembers && had[k]
	}
	nameChanged := name != base.Name
	if !nameChanged && sameMembers {
		return raw, nil
	}
	v4 := info.Version == "4.0"
	text := string(toUTF8(raw))
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	lines := cardLines(text)
	begin, end := cardBounds(lines)
	if begin < 0 || end < 0 {
		return nil, ErrNotVCard
	}
	newLine := func(s string) string {
		var b bytes.Buffer
		foldEOL(&b, s, eol)
		return b.String()
	}
	apple := !v4
	for _, l := range lines[begin+1 : end] {
		if p, ok := parseLine(l.text); ok && (p.name == "X-ADDRESSBOOKSERVER-MEMBER" || p.name == "X-ADDRESSBOOKSERVER-KIND") {
			apple = true
		} else if ok && p.name == "MEMBER" {
			apple = false
			break
		}
	}
	out := rawOf(lines[:begin+1])
	hasFN, last := false, -1
	have := map[string]bool{}
	for _, l := range lines[begin+1 : end] {
		p, ok := parseLine(l.text)
		if !ok {
			out = append(out, l.raw)
			continue
		}
		group := groupOf(l.text)
		switch p.name {
		case "FN":
			if nameChanged {
				if !hasFN {
					out = append(out, newLine(prefix(group)+"FN:"+escape(name)))
				}
				hasFN = true
				continue
			}
			hasFN = true
		case "N":
			if nameChanged {
				out = append(out, newLine(prefix(group)+"N:"+escape(name)+";;;;"))
				continue
			}
		case "REV":
			out = append(out, newLine(prefix(group)+"REV:"+rev(at, v4)))
			continue
		case "MEMBER", "X-ADDRESSBOOKSERVER-MEMBER":
			k := MemberUID(unescape(p.value))
			if k != "" && !want[k] {
				continue
			}
			have[k] = true
			out = append(out, l.raw)
			last = len(out)
			continue
		}
		out = append(out, l.raw)
	}
	if last < 0 {
		last = len(out)
	}
	var add []string
	if !hasFN {
		add = append(add, newLine("FN:"+escape(name)))
	}
	for _, m := range members {
		if k := UIDKey(m); !have[k] {
			have[k] = true
			if apple {
				add = append(add, newLine("X-ADDRESSBOOKSERVER-MEMBER:"+memberRef(m)))
			} else {
				add = append(add, newLine("MEMBER:"+memberRef(m)))
			}
		}
	}
	out = append(out[:last], append(add, out[last:]...)...)
	out = append(out, rawOf(lines[end:])...)
	return []byte(strings.Join(out, "")), nil
}

// orgUnits are an ORG's parts after the organization's name, as written
// (";Sales;East"), "" for none or for a value in a transfer encoding.
func orgUnits(p property) string {
	if len(p.params["ENCODING"]) > 0 || len(p.params["CHARSET"]) > 0 || p.has("TYPE", "QUOTED-PRINTABLE") {
		return ""
	}
	v := p.value
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '\\':
			i++
		case ';':
			if strings.Trim(v[i:], "; ") == "" {
				return ""
			}
			return v[i:]
		}
	}
	return ""
}

// emailValue is an EMAIL's address, without a mailto:.
func emailValue(p property) string {
	v := strings.TrimSpace(unescape(p.text()))
	v, _ = cutPrefixFold(v, "mailto:")
	return v
}

// addressOf is an ADR's parts.
func addressOf(p property) ui.ContactAddress {
	parts := components(p.text())
	get := func(i int) string {
		if i < len(parts) {
			return strings.TrimSpace(parts[i])
		}
		return ""
	}
	return ui.ContactAddress{Label: placeLabel(p), POBox: get(0), Extended: get(1), Street: get(2), City: get(3), Region: get(4),
		Postcode: get(5), Country: get(6)}
}

// placeParam is the TYPE an EMAIL or ADR gets for a label ("" none).
func placeParam(label string, v4 bool) string {
	t := placeType[cleanPlace(label)]
	if v4 {
		return strings.ToLower(t)
	}
	return t
}

// emailType is the TYPE an EMAIL gets for a label, as emailLine writes it.
func emailType(label string, v4 bool) string {
	t := placeParam(label, v4)
	switch {
	case v4:
		return t
	case t == "":
		return "INTERNET"
	}
	return "INTERNET," + t
}

// retypeAs is a property with another TYPE (none for "") and its other
// parameters and value as they were.
func retypeAs(text, name, typ string) string {
	i := valueStart(text)
	head := name
	for _, p := range splitParams(text[:i])[1:] {
		k, _, hasValue := strings.Cut(p, "=")
		k = strings.ToUpper(strings.TrimSpace(k))
		switch {
		case hasValue && k != "TYPE":
			head += ";" + p
		case !hasValue && (k == "QUOTED-PRINTABLE" || k == "BASE64" || k == "8BIT"):
			head += ";" + p
		}
	}
	if typ != "" {
		head += ";TYPE=" + typ
	}
	return head + ":" + text[i+1:]
}

// cardLine is one property: the lines it was written on, as they were, and
// unfolded.
type cardLine struct {
	raw, text string
}

// cardLines splits as Parse does: at CRLF, LF and a CR alone.
func cardLines(text string) []cardLine {
	var out []cardLine
	var phys []string
	for len(text) > 0 {
		i := strings.IndexAny(text, "\r\n")
		switch {
		case i < 0:
			phys, text = append(phys, text), ""
		case text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n':
			phys, text = append(phys, text[:i+2]), text[i+2:]
		default:
			phys, text = append(phys, text[:i+1]), text[i+1:]
		}
	}
	for _, p := range phys {
		t := strings.TrimRight(p, "\r\n")
		if n := len(out); n > 0 {
			last := &out[n-1]
			// In unfold's order: a soft break first, then folding.
			switch {
			case softBreak(last.text):
				last.raw += p
				last.text = last.text[:len(last.text)-1] + t
				continue
			case t != "" && (t[0] == ' ' || t[0] == '\t'):
				last.raw += p
				last.text += t[1:]
				continue
			}
		}
		out = append(out, cardLine{raw: p, text: t})
	}
	return out
}

func rawOf(lines []cardLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.raw
	}
	return out
}

// groupOf is a property's group ("item1" of item1.TEL), "" without one.
func groupOf(text string) string {
	i := valueStart(text)
	if i <= 0 {
		return ""
	}
	name := strings.TrimSpace(splitParams(text[:i])[0])
	if j := strings.LastIndexByte(name, '.'); j >= 0 {
		return name[:j]
	}
	return ""
}

func prefix(group string) string {
	if group == "" {
		return ""
	}
	return group + "."
}

// telValue is a TEL's number: its text, or the number of a tel: URI.
func telValue(p property) string {
	v := strings.TrimSpace(unescape(p.text()))
	if uri, ok := cutPrefixFold(v, "tel:"); ok {
		v, _, _ = strings.Cut(uri, ";")
	}
	return v
}

var vcard4Label = map[string]string{ui.NumberMobile: "cell", ui.NumberHome: "home", ui.NumberWork: "work", ui.NumberOther: "voice"}

func telType(label string, v4 bool) string {
	if v4 {
		return vcard4Label[cleanLabel(label)]
	}
	return vcardLabel[cleanLabel(label)]
}

func telLine(group string, n ui.ContactNumber, v4 bool) string {
	return prefix(group) + "TEL;TYPE=" + telType(n.Label, v4) + ":" + escape(n.Number)
}

// retype is a TEL with another label and its value as it was.
func retype(text, label string, v4 bool) string {
	i := valueStart(text)
	params := ""
	for _, p := range splitParams(text[:i])[1:] {
		if strings.EqualFold(strings.TrimSpace(p), "VALUE=uri") {
			params = ";VALUE=uri"
		}
	}
	return "TEL" + params + ";TYPE=" + telType(label, v4) + ":" + text[i+1:]
}

// nLine is N for a name: family and given name as structuredName reads
// them back (the first character of a Chinese name is its family name,
// the last word of another).
func nLine(group, name string) string {
	family, given := "", name
	switch {
	case hasHan(name):
		r := []rune(name)
		family, given = string(r[:1]), string(r[1:])
	case strings.Contains(name, " "):
		i := strings.LastIndexByte(name, ' ')
		family, given = name[i+1:], name[:i]
	}
	return prefix(group) + "N:" + escape(family) + ";" + escape(given) + ";;;"
}

func rev(at time.Time, v4 bool) string {
	if v4 {
		return at.UTC().Format("20060102T150405Z")
	}
	return at.UTC().Format("2006-01-02T15:04:05Z")
}
