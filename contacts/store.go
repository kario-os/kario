// Package contacts is the phone's contacts store, which the Shell owns
// (shell/contacts.go), and the vCard files contacts come in and go out in.
package contacts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/ui"
)

// Limits of one contact and of the store. A longer name, note or address
// line is cut, numbers, e-mail and postal addresses past their limits are
// left out.
const (
	MaxContacts    = 5000
	MaxNumbers     = 8
	MaxName        = 64 // characters
	MaxNumber      = 40 // bytes
	MaxNotes       = 1000
	MaxEmails      = 8
	MaxEmail       = 254 // bytes
	MaxAddresses   = 4
	MaxAddressPart = 100 // characters, each of an address's
	MaxField       = 100 // characters: a nickname, an organization, a title
	MaxURLs        = 4
	MaxURL         = 1000 // bytes
	maxBook        = 64
)

var (
	ErrEmpty    = errors.New("a contact needs a name or a number")
	ErrFull     = fmt.Errorf("the contacts are full (%d)", MaxContacts)
	ErrNotFound = errors.New("no such contact")
)

// Store is the contacts, kept sorted by their pinyin, in one JSON file
// written whole on every change.
type Store struct {
	path   string
	pinyin func(string) string
	list   []ui.Contact
	byKey  map[string]int // NumberKey -> index in list
	seq    int
	// unreadable: the file is there but could not be read; nothing is
	// written over it.
	unreadable error
	// Created: there was no file (a new phone). Kept: the file did not
	// parse and was moved here, out of the way of the writes.
	Created bool
	Kept    string
	// batch: Batch runs, and writes once at its end if dirty (the
	// contacts) or gdirty (the groups).
	batch, dirty, gdirty bool
	// groups (groups.go) and the last group ID's number.
	groups []ui.ContactGroup
	gseq   int
	// NewBook is the address book a new contact goes to when it names
	// none (ui.Contact.Book), "" for the phone alone.
	NewBook string
	// gcAt: when photos no contact shows were last removed (photos.go).
	gcAt time.Time
}

// Open reads the store at path. pinyin spells names for search
// (ime.Pinyin). A missing file is an empty store; a damaged one is kept
// beside it (Kept) and the store starts empty.
func Open(path string, pinyin func(string) string) *Store {
	s := &Store{path: path, pinyin: pinyin}
	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		s.Created = true
	case err != nil:
		s.unreadable = err
	default:
		var list []ui.Contact
		if err := json.Unmarshal(raw, &list); err != nil {
			kept := path + ".damaged"
			for i := 2; fileExists(kept); i++ {
				kept = fmt.Sprintf("%s.damaged-%d", path, i)
			}
			if err := os.Rename(path, kept); err != nil {
				s.unreadable = err
				break
			}
			s.Kept = kept
			break
		}
		s.load(list)
	}
	s.index()
	s.loadGroups()
	s.gcPhotos(time.Now())
	return s
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func (s *Store) load(list []ui.Contact) {
	seen := map[string]bool{}
	for _, c := range list {
		if n, ok := strings.CutPrefix(c.ID, "c"); ok {
			if v, err := strconv.Atoi(n); err == nil && v > s.seq {
				s.seq = v
			}
		}
	}
	for _, x := range list {
		// The kept spelling saves opening the dictionary at every start.
		c, err := Clean(x, nil)
		if err != nil {
			continue
		}
		c.Pinyin = strings.ToLower(oneLine(x.Pinyin))
		if c.Pinyin == "" && s.pinyin != nil {
			c.Pinyin = s.pinyin(c.Name)
		}
		if c.ID == "" || seen[c.ID] {
			s.seq++
			c.ID = "c" + strconv.Itoa(s.seq)
		}
		seen[c.ID] = true
		s.list = append(s.list, c)
		if len(s.list) == MaxContacts {
			break
		}
	}
	s.sort()
}

// Seed fills a new store (one Open created) with contacts.
func (s *Store) Seed(list []ui.Contact) error {
	s.load(list)
	s.index()
	return s.save()
}

func sortKey(c ui.Contact) string {
	p := strings.ToLower(c.Pinyin)
	if p == "" || p[0] < 'a' || p[0] > 'z' {
		// Names that do not start with a letter go last, as phones list
		// them under "#".
		return "{" + p + "\x00" + c.Name
	}
	return p + "\x00" + c.Name
}

func (s *Store) sort() {
	sort.SliceStable(s.list, func(i, j int) bool { return sortKey(s.list[i]) < sortKey(s.list[j]) })
}

// Reindex compares numbers again (the region they are read in changed).
func (s *Store) Reindex() { s.index() }

func (s *Store) index() {
	s.byKey = map[string]int{}
	for i, c := range s.list {
		for _, n := range c.Numbers {
			if k := ui.NumberKey(n.Number); k != "" {
				if _, ok := s.byKey[k]; !ok {
					s.byKey[k] = i
				}
			}
		}
	}
}

// List is every contact, by pinyin, with its groups (a copy).
func (s *Store) List() []ui.Contact {
	in := s.groupsOf()
	out := make([]ui.Contact, len(s.list))
	for i, c := range s.list {
		c = copyOf(c)
		c.Groups = in[c.ID]
		out[i] = c
	}
	return out
}

func copyOf(c ui.Contact) ui.Contact {
	c.Numbers = slices.Clone(c.Numbers)
	c.Emails = slices.Clone(c.Emails)
	c.Addresses = slices.Clone(c.Addresses)
	c.URLs = slices.Clone(c.URLs)
	c.Groups = slices.Clone(c.Groups)
	return c
}

func (s *Store) Len() int { return len(s.list) }

func (s *Store) at(id string) int {
	for i := range s.list {
		if s.list[i].ID == id {
			return i
		}
	}
	return -1
}

// Get is the contact with that ID, with its groups.
func (s *Store) Get(id string) (ui.Contact, bool) {
	if i := s.at(id); i >= 0 {
		c := copyOf(s.list[i])
		c.Groups = s.groupsOf()[id]
		return c, true
	}
	return ui.Contact{}, false
}

// ByNumber is the contact a number belongs to, compared as NumberKey does.
func (s *Store) ByNumber(number string) (ui.Contact, bool) {
	if i, ok := s.byKey[ui.NumberKey(number)]; ok && number != "" {
		return s.list[i], true
	}
	return ui.Contact{}, false
}

// Clean is a contact as the store keeps it: control characters out,
// the limits applied, labels known, numbers once each, dates that are
// none dropped, the name the first number's when it has none, Number and
// Pinyin filled in.
func Clean(c ui.Contact, pinyin func(string) string) (ui.Contact, error) {
	out := ui.Contact{ID: c.ID, Name: cut(oneLine(c.Name), MaxName), Notes: cut(strings.TrimSpace(text(c.Notes)), MaxNotes),
		Nickname: cut(oneLine(c.Nickname), MaxField), Org: cut(oneLine(c.Org), MaxField), Title: cut(oneLine(c.Title), MaxField),
		Birthday: cleanDate(c.Birthday), Anniversary: cleanDate(c.Anniversary), Book: cut(oneLine(c.Book), maxBook), PhotoData: c.PhotoData}
	if photoKeyOK(c.Photo) {
		out.Photo = c.Photo
	}
	for _, u := range c.URLs {
		if u = cleanURL(u); u != "" && !slices.Contains(out.URLs, u) && len(out.URLs) < MaxURLs {
			out.URLs = append(out.URLs, u)
		}
	}
	seenEmail := map[string]bool{}
	for _, e := range c.Emails {
		a := cleanEmail(e.Address)
		if k := EmailKey(a); a != "" && !seenEmail[k] && len(out.Emails) < MaxEmails {
			seenEmail[k] = true
			out.Emails = append(out.Emails, ui.ContactEmail{Address: a, Label: cleanPlace(e.Label)})
		}
	}
	seenAddr := map[string]bool{}
	for _, a := range c.Addresses {
		a = cleanAddress(a)
		if k := AddressKey(a); k != "" && !seenAddr[k] && len(out.Addresses) < MaxAddresses {
			seenAddr[k] = true
			out.Addresses = append(out.Addresses, a)
		}
	}
	seen := map[string]bool{}
	for _, n := range ui.NumbersOf(c) {
		num := cleanNumber(n.Number)
		k := ui.NumberKey(num)
		if num == "" || seen[k] || len(out.Numbers) == MaxNumbers {
			continue
		}
		seen[k] = true
		out.Numbers = append(out.Numbers, ui.ContactNumber{Number: num, Label: cleanLabel(n.Label)})
	}
	if len(out.Numbers) > 0 {
		out.Number = out.Numbers[0].Number
	}
	if out.Name == "" {
		out.Name = out.Number
	}
	if out.Name == "" {
		return ui.Contact{}, ErrEmpty
	}
	if pinyin != nil {
		out.Pinyin = pinyin(out.Name)
	}
	return out, nil
}

func (s *Store) clean(c ui.Contact) (ui.Contact, error) { return Clean(c, s.pinyin) }

// oneLine is text without line breaks or other control characters, its
// spaces collapsed.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, ""))), " ")
}

// text keeps line breaks and drops the other control characters.
func text(s string) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || r == '\r' {
			return -1
		}
		return r
	}, s)
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n]))
}

// cleanNumber keeps what a phone dials (digits, +, *, #, the pause and
// wait marks p and w, the separators people write) from a number, a
// tel: URI's included.
func cleanNumber(n string) string {
	n = strings.TrimSpace(n)
	if rest, ok := strings.CutPrefix(strings.ToLower(n), "tel:"); ok {
		n = rest
	}
	var b strings.Builder
	for _, r := range n {
		switch {
		case r >= '0' && r <= '9', strings.ContainsRune("+*#,;()-. ", r):
			b.WriteRune(r)
		case r == 'p' || r == 'w' || r == 'P' || r == 'W':
			b.WriteRune(r | 0x20)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if len(out) > MaxNumber {
		out = strings.TrimSpace(out[:MaxNumber])
	}
	if !strings.ContainsAny(out, "0123456789") {
		return ""
	}
	return out
}

func cleanLabel(l string) string {
	switch l {
	case ui.NumberMobile, ui.NumberHome, ui.NumberWork, ui.NumberOther:
		return l
	case "":
		return ui.NumberMobile
	}
	return ui.NumberOther
}

// cleanEmail is an e-mail address as the store keeps it: one word, ""
// when longer than MaxEmail.
func cleanEmail(a string) string {
	a = strings.Join(strings.Fields(oneLine(a)), "")
	if len(a) > MaxEmail {
		return ""
	}
	return a
}

// cleanURL is a web address as the store keeps it: one word, "" when
// longer than MaxURL.
func cleanURL(u string) string {
	u = strings.Join(strings.Fields(oneLine(u)), "")
	if len(u) > MaxURL {
		return ""
	}
	return u
}

// cleanDate is a birthday or an anniversary as the store keeps it, ""
// for one that is not ui.ContactDateParts's.
func cleanDate(s string) string {
	s = strings.TrimSpace(s)
	if _, _, _, ok := ui.ContactDateParts(s); !ok {
		return ""
	}
	return s
}

// cleanAddress is a postal address as the store keeps it: each part one
// line, cut at MaxAddressPart.
func cleanAddress(a ui.ContactAddress) ui.ContactAddress {
	part := func(s string) string { return cut(oneLine(s), MaxAddressPart) }
	return ui.ContactAddress{Label: cleanPlace(a.Label), POBox: part(a.POBox), Extended: part(a.Extended), Street: part(a.Street),
		City: part(a.City), Region: part(a.Region), Postcode: part(a.Postcode), Country: part(a.Country)}
}

// cleanPlace is an e-mail or postal address's label: home, work or other.
func cleanPlace(l string) string {
	switch l {
	case ui.NumberHome, ui.NumberWork:
		return l
	}
	return ui.NumberOther
}

// EmailKey is how e-mail addresses are compared: in any case.
func EmailKey(a string) string { return strings.ToLower(a) }

// AddressKey is how postal addresses are compared: their parts in any
// case, "" for an address with none.
func AddressKey(a ui.ContactAddress) string {
	parts := []string{a.POBox, a.Extended, a.Street, a.City, a.Region, a.Postcode, a.Country}
	k := strings.ToLower(strings.Join(parts, "\x00"))
	if strings.Trim(k, "\x00 ") == "" {
		return ""
	}
	return k
}

// Save adds a contact (no ID) or changes the one with its ID, and
// returns it as kept. A new one goes to its Book (NewBook when it names
// none, the phone alone for ui.BookPhone); a changed one stays in its
// book unless it names another. PhotoData becomes its photo; a Photo the
// store does not have is ErrNoPhoto.
func (s *Store) Save(c ui.Contact) (ui.Contact, error) {
	c, err := s.clean(c)
	if err != nil {
		return ui.Contact{}, err
	}
	if err := s.takePhoto(&c); err != nil {
		return ui.Contact{}, err
	}
	old := s.list
	if !s.batch {
		s.list = append([]ui.Contact(nil), s.list...)
	}
	if c.ID == "" {
		if len(s.list) >= MaxContacts {
			s.list = old
			return ui.Contact{}, ErrFull
		}
		s.seq++
		c.ID = "c" + strconv.Itoa(s.seq)
		c.Book = s.bookOf(c.Book)
		s.list = append(s.list, c)
	} else if i := s.at(c.ID); i >= 0 {
		switch c.Book {
		case "":
			c.Book = s.list[i].Book
		case ui.BookPhone:
			c.Book = ""
		}
		s.list[i] = c
	} else {
		s.list = old
		return ui.Contact{}, ErrNotFound
	}
	if err := s.commit(old); err != nil {
		return ui.Contact{}, err
	}
	return c, nil
}

// bookOf is the book a new contact naming book goes to.
func (s *Store) bookOf(book string) string {
	switch book {
	case "":
		return s.NewBook
	case ui.BookPhone:
		return ""
	}
	return book
}

// takePhoto stores a contact's PhotoData as its Photo, and checks a Photo
// it names.
func (s *Store) takePhoto(c *ui.Contact) error {
	if c.PhotoData != nil {
		key, err := s.PutPhoto(c.PhotoData)
		if err != nil {
			return err
		}
		c.Photo = key
	} else if c.Photo != "" && !s.HasPhoto(c.Photo) {
		return ErrNoPhoto
	}
	c.PhotoData = nil
	return nil
}

// MoveBook puts the contacts of one book in another ("" the phone alone),
// in one write, and says how many there were.
func (s *Store) MoveBook(from, to string) (int, error) {
	old := s.list
	s.list = append([]ui.Contact(nil), s.list...)
	n := 0
	for i := range s.list {
		if s.list[i].Book == from {
			s.list[i].Book = to
			n++
		}
	}
	if n == 0 {
		s.list = old
		return 0, nil
	}
	if err := s.commit(old); err != nil {
		return 0, err
	}
	return n, nil
}

// Delete removes the contact with that ID.
func (s *Store) Delete(id string) error {
	i := s.at(id)
	if i < 0 {
		return ErrNotFound
	}
	old := s.list
	if s.batch {
		s.list = append(s.list[:i], s.list[i+1:]...)
	} else {
		s.list = append(append([]ui.Contact(nil), s.list[:i]...), s.list[i+1:]...)
	}
	return s.commit(old)
}

// Duplicate reports whether the store has c already: a contact of the
// same name (any case) with every number of c's, or c's name alone.
func (s *Store) Duplicate(c ui.Contact) bool {
	for _, x := range s.list {
		if !strings.EqualFold(x.Name, c.Name) {
			continue
		}
		have := map[string]bool{}
		for _, n := range x.Numbers {
			have[ui.NumberKey(n.Number)] = true
		}
		all := true
		for _, n := range c.Numbers {
			all = all && have[ui.NumberKey(n.Number)]
		}
		if all {
			return true
		}
	}
	return false
}

// AddAll adds the contacts the store does not have yet (Duplicate), in one
// write: how many it added and how many it left out, already there or past
// MaxContacts.
func (s *Store) AddAll(list []ui.Contact) (added, skipped int, err error) {
	ids, err := s.Import(list)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		if id != "" {
			added++
		}
	}
	return added, len(ids) - added, nil
}

// Import is AddAll giving each contact's new ID, "" for one left out, so
// what it added can be taken out again (DeleteIDs).
func (s *Store) Import(list []ui.Contact) ([]string, error) {
	old := s.list
	s.list = append([]ui.Contact(nil), s.list...)
	seq := s.seq
	ids := make([]string, len(list))
	added := 0
	for i, c := range list {
		c, err := s.clean(c)
		c.ID = ""
		if err != nil || s.Duplicate(c) || len(s.list) >= MaxContacts {
			continue
		}
		if s.takePhoto(&c) != nil {
			c.Photo = ""
		}
		s.seq++
		c.ID = "c" + strconv.Itoa(s.seq)
		c.Book = s.bookOf(c.Book)
		s.list = append(s.list, c)
		ids[i] = c.ID
		added++
	}
	if added == 0 {
		s.list = old
		return ids, nil
	}
	if err := s.commit(old); err != nil {
		s.seq = seq
		return nil, err
	}
	return ids, nil
}

// DeleteIDs removes the contacts with these IDs in one write, and says how
// many there were; IDs it does not have are left alone.
func (s *Store) DeleteIDs(ids []string) (int, error) {
	gone := map[string]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	old := s.list
	var keep []ui.Contact
	for _, c := range s.list {
		if !gone[c.ID] {
			keep = append(keep, c)
		}
	}
	n := len(old) - len(keep)
	if n == 0 {
		return 0, nil
	}
	s.list = keep
	if err := s.commit(old); err != nil {
		return 0, err
	}
	return n, nil
}

// commit writes the changed list; when that fails the list is old again.
func (s *Store) commit(old []ui.Contact) error {
	if s.batch {
		s.dirty = true
		return nil
	}
	s.sort()
	if err := s.save(); err != nil {
		s.list = old
		return err
	}
	s.index()
	s.gcPhotos(time.Now())
	return nil
}

// Batch runs f, whose changes to the store and its groups are made in
// memory, and writes them once after it: a sync's hundreds of changes cost
// one write of each file, one sort. Meanwhile the list is in no order and
// ByNumber does not know the changes. When a write fails, every change f
// made that is not written is undone.
func (s *Store) Batch(f func()) error {
	if s.batch {
		f()
		return nil
	}
	old, seq := s.list, s.seq
	oldGroups, gseq := s.cloneGroups(), s.gseq
	s.list = append([]ui.Contact(nil), old...)
	s.batch, s.dirty, s.gdirty = true, false, false
	f()
	s.batch = false
	if !s.dirty {
		s.list = old
	} else {
		s.sort()
		if err := s.save(); err != nil {
			s.list, s.seq = old, seq
			s.groups, s.gseq = oldGroups, gseq
			s.index()
			return err
		}
		s.index()
	}
	if s.gdirty {
		if err := s.saveGroups(s.groups); err != nil {
			s.groups, s.gseq = oldGroups, gseq
			return err
		}
	}
	if s.dirty {
		s.gcPhotos(time.Now())
	}
	return nil
}

func (s *Store) save() error {
	if s.unreadable != nil {
		return fmt.Errorf("the contacts file could not be read, so it is not written: %w", s.unreadable)
	}
	list := s.list
	if list == nil {
		list = []ui.Contact{}
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(s.path, raw)
}

// WriteFile replaces path with data as one step, readable by its owner
// only: a temporary file beside it, synced, renamed over it, the directory
// synced, so a crash or power loss leaves the old file or the new one.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
