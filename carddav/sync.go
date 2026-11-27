package carddav

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/ui"
)

// A sync, both ways, between the phone's contacts in one address book (those
// whose Book is the sync's Key) and the book on the server:
//
//  1. Pull: what changed on the server since the last sync, by its sync
//     token (sync-collection, RFC 6578) in pages of Limit, or, on a server
//     without one, by comparing every card's ETag with the last ones (a
//     CTag that did not move skips that); a token the server no longer
//     knows starts over from no token, when a card the phone has that the
//     whole listing lacks was deleted there. The changed cards are read in
//     batches of Batch (addressbook-multiget) and go into the phone's
//     contacts, a batch at a time in one write; the token moves on once
//     all of them are in.
//  2. Groups (groups.go): the group cards' names, then each contact's
//     groups, merged; the phone's side written at once.
//  3. Push: what changed on the phone since: deleted (DELETE with
//     If-Match), changed (the server's card read again and patched, PUT
//     with If-Match: contacts.PatchCard keeps what the phone does not
//     show) and new (PUT with If-None-Match: *); then the group cards.
//
// When both sides changed a contact (seen in the pull, or as a 412 to a
// write) it is merged against the copy both last agreed on (Item.Base),
// field by field: a field only one side changed takes that side's value;
// numbers, e-mail, postal and web addresses merge as sets, so one added on
// either side stays and one deleted on either side goes; a name, notes,
// photo or another field of one value (a birthday) both sides changed take
// the server's (the phone's clock and the server's say nothing reliable
// about which came later). An edit beats a
// delete, on either side: the contact comes back on the side that deleted
// it. A card new on the server that a contact on the phone not synced yet
// (in the book, or on the phone alone for a book's first sync) has the
// same name and numbers as (one side's numbers within the other's) is
// linked to it, not added twice.
//
// A photo goes both ways as the bytes it came as: the phone keeps a card's
// picture as it is (contacts.Store's photos) and a card whose photo did
// not change keeps its PHOTO line. A photo at a URL is fetched only from
// the book's own server (Client.Photo), once for each URL (Item.PhotoURL),
// and the card keeps its URL until the phone's photo changes.
//
// A contact the user moved to another book (or to the phone alone) is
// deleted here as one deleted on the phone; the other book's sync makes it
// a new card there, its photo and groups with it.
//
// A ReadOnly book is only read: the phone's contacts in it are the
// server's, whatever the phone did to them (the Shell lets it do nothing),
// and no group card is written.
//
// Cards the phone does not show (ones with neither a name nor a number)
// are remembered and left alone; ones that do not fit
// (contacts.MaxContacts) are tried again once there is room.

// Local is the phone's contacts store as a sync reaches it. List is every
// contact, its Groups the names of its groups (a sync knows groups by
// name); Groups are the groups, their members contact IDs; Photo a
// contact's picture. Apply makes a batch of changes in one write and says
// how each went: a change of a contact (Was set) or a delete happens only
// while the store's copy is still Was (ErrChanged otherwise: the user
// changed it meanwhile, and the next sync sees that); an add may find the
// store full (contacts.ErrFull).
type Local interface {
	List() ([]ui.Contact, error)
	Groups() ([]ui.ContactGroup, error)
	Photo(key string) ([]byte, error)
	Apply(ops []Op) ([]Done, error)
}

// Op is a change to the phone's contacts: Contact added (Was nil) or put
// in place of Was, or Was deleted; with Groups the contact's groups become
// Contact.Groups (names; a group missing is made), otherwise they stay.
// Group, instead, makes the group of its Name (when there is none) or
// renames the one of its ID.
type Op struct {
	Contact ui.Contact
	Was     *ui.Contact
	Delete  bool
	Groups  bool
	Group   *ui.ContactGroup
}

// Done is how an Op went: the contact (or group) as the store keeps it,
// or why not.
type Done struct {
	Contact ui.Contact
	Group   ui.ContactGroup
	Err     error
}

var ErrChanged = errors.New("the contact changed on the phone meanwhile")

// stateVersion: a State older than this is read from the server again
// whole, as it lacks what the phone came to show (2: e-mail and postal
// addresses, photos, groups; 3: birthdays, anniversaries, nicknames,
// organizations, titles, web addresses): without, a change on the phone
// would write a card with those taken out.
const stateVersion = 3

// State is what a sync keeps between runs, for one book.
type State struct {
	Book  string `json:"book"`
	Token string `json:"token,omitempty"`
	CTag  string `json:"ctag,omitempty"`
	// Synced: a sync has run to its end at least once.
	Synced  bool `json:"synced,omitempty"`
	Version int  `json:"version,omitempty"`
	// Groups is how the book keeps groups, as the last sync found
	// (GroupsVCard, GroupsCategories).
	Groups string  `json:"groups,omitempty"`
	Items  []*Item `json:"items"`
}

// Item is a card and the contact (or group) it is.
type Item struct {
	Href string `json:"href"`
	ETag string `json:"etag,omitempty"`
	UID  string `json:"uid,omitempty"`
	// ID is the phone's contact, "" for a card the phone leaves alone
	// (Skip says why).
	ID string `json:"id,omitempty"`
	// Base is the contact as both sides last agreed on it.
	Base ui.Contact `json:"base"`
	// Remote: the contact came from the server (signing out may take
	// those off the phone); New: the phone is creating the card.
	Remote bool   `json:"remote,omitempty"`
	New    bool   `json:"new,omitempty"`
	Skip   string `json:"skip,omitempty"`
	// A contact's: Groups are the names of its groups both sides last
	// agreed on, Cats its card's CATEGORIES as last read.
	Groups []string `json:"groups,omitempty"`
	Cats   []string `json:"cats,omitempty"`
	// A group card's (Skip SkipGroup): its name and members (UIDKey) as
	// last read or written, and the phone's group it is (its ID);
	// Base.Name is the name both last agreed on.
	Name    string   `json:"name,omitempty"`
	Members []string `json:"members,omitempty"`
	Group   string   `json:"group,omitempty"`
	// PhotoURL is the card's photo's URL as last fetched, PhotoKey the
	// picture it was.
	PhotoURL string `json:"photo_url,omitempty"`
	PhotoKey string `json:"photo_key,omitempty"`
}

// Why a card is no contact.
const (
	SkipGroup = "group"
	SkipEmpty = "empty"
	SkipFull  = "full"
)

// Linked are the phone's contacts the state has a card for.
func (st *State) Linked() []*Item {
	var out []*Item
	for _, it := range st.Items {
		if it.ID != "" {
			out = append(out, it)
		}
	}
	return out
}

// Result is what one run did, and the first card it could not write
// (Problem, Failed of them); the run goes on past those.
type Result struct {
	Added, Changed, Deleted int // on the phone
	Uploaded, Removed       int // on the server
	Conflicts               int
	Failed                  int
	Problem                 error
}

// Progress is called as a run goes: what it is at (PhasePull, PhasePush),
// how far and of how many (0: not known yet).
type Progress func(phase string, done, total int)

const (
	PhasePull = "pull"
	PhasePush = "push"
)

// Sync is one book's sync; Run is one run of it.
type Sync struct {
	Client *Client
	Book   Book
	// Key is the book as the phone's contacts name it (ui.Contact.Book).
	Key string
	// Groups is how a book without groups yet is to keep them
	// (GroupsVCard, GroupsCategories; "": by its server, groups.go).
	Groups  string
	Local   Local
	State   *State
	Save    func(*State) error
	Now     func() time.Time
	NewUID  func() string
	Notify  Progress
	Limit   int // changes a sync-collection page asks for (0: 500)
	Batch   int // cards a multiget asks for, writes between saves (0: 50)
	noLimit bool
	book    *url.URL
	res     Result
	byHref  map[string]*Item
	byID    map[string]*Item
	ops     []Op
	then    []func(Done) error
	// The run's groups (groups.go): how the book keeps them, each
	// contact's merged, the ones whose card's CATEGORIES change, the
	// group cards' names.
	style    string
	want     map[*Item][]string
	catWrite map[*Item]bool
	gname    map[*Item]string
	// gone are the cards this run deleted (UIDKey), out of the group
	// cards; photos the photos it fetched, by card (hrefKey).
	gone   map[string]bool
	photos map[string]fetched
}

type fetched struct{ url, key string }

// Temporary reports whether a run that ended so may well work by itself
// later: no network, the server down or busy. Otherwise someone has to do
// something (the password), or nothing will change.
func Temporary(err error) bool {
	var se *StatusError
	var ne *NetError
	switch {
	case errors.As(err, &ne), errors.Is(err, context.DeadlineExceeded):
		return true
	case errors.As(err, &se):
		return se.Temporary()
	}
	return false
}

// RetryAfter is the wait a server asked for, 0 for none.
func RetryAfter(err error) time.Duration {
	var se *StatusError
	if errors.As(err, &se) {
		return se.RetryAfter
	}
	return 0
}

func randomUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// Same reports whether two contacts show the same: name, numbers, e-mail,
// postal and web addresses (and their labels, in order), photo, notes,
// nickname, organization, title, birthday and anniversary.
func Same(a, b ui.Contact) bool {
	if a.Name != b.Name || a.Notes != b.Notes || a.Photo != b.Photo || len(a.Emails) != len(b.Emails) || len(a.Addresses) != len(b.Addresses) ||
		!sameOthers(a, b) {
		return false
	}
	an, bn := ui.NumbersOf(a), ui.NumbersOf(b)
	if len(an) != len(bn) {
		return false
	}
	for i := range an {
		if an[i].Number != bn[i].Number || an[i].Label != bn[i].Label {
			return false
		}
	}
	for i := range a.Emails {
		if a.Emails[i] != b.Emails[i] {
			return false
		}
	}
	for i := range a.Addresses {
		if a.Addresses[i] != b.Addresses[i] {
			return false
		}
	}
	return true
}

func sameOthers(a, b ui.Contact) bool {
	return a.Nickname == b.Nickname && a.Org == b.Org && a.Title == b.Title && a.Birthday == b.Birthday && a.Anniversary == b.Anniversary &&
		slices.Equal(a.URLs, b.URLs)
}

// shown is what a sync compares of a contact, and its photo's bytes when
// it has them.
func shown(c ui.Contact) ui.Contact {
	return ui.Contact{Name: c.Name, Number: c.Number, Numbers: append([]ui.ContactNumber(nil), ui.NumbersOf(c)...),
		Emails: append([]ui.ContactEmail(nil), c.Emails...), Addresses: append([]ui.ContactAddress(nil), c.Addresses...),
		Notes: c.Notes, Photo: c.Photo, PhotoData: c.PhotoData, Nickname: c.Nickname, Org: c.Org, Title: c.Title,
		Birthday: c.Birthday, Anniversary: c.Anniversary, URLs: slices.Clone(c.URLs)}
}

// mergeSet merges a set of values both sides changed (numbers, e-mail and
// postal addresses, by key): the remote's, without what the local took
// out of the base, relabelled where only the local relabelled, and with
// what the local added.
func mergeSet[T any](base, local, remote []T, key func(T) string, label func(T) string, relabel func(T, string) T) []T {
	labels := func(l []T) map[string]string {
		m := map[string]string{}
		for _, x := range l {
			m[key(x)] = label(x)
		}
		return m
	}
	bk, lk, rk := labels(base), labels(local), labels(remote)
	var out []T
	for _, x := range remote {
		k := key(x)
		_, inBase := bk[k]
		l, inLocal := lk[k]
		switch {
		case inBase && !inLocal:
			continue
		case inBase && l != bk[k] && label(x) == bk[k]:
			x = relabel(x, l)
		}
		out = append(out, x)
	}
	for _, x := range local {
		k := key(x)
		if _, inBase := bk[k]; !inBase {
			if _, inRemote := rk[k]; !inRemote {
				out = append(out, x)
			}
		}
	}
	return out
}

func numberKey(n ui.ContactNumber) string                     { return ui.NumberKey(n.Number) }
func numberLabel(n ui.ContactNumber) string                   { return n.Label }
func setNumber(n ui.ContactNumber, l string) ui.ContactNumber { n.Label = l; return n }
func emailKey(e ui.ContactEmail) string                       { return contacts.EmailKey(e.Address) }
func emailLabel(e ui.ContactEmail) string                     { return e.Label }
func setEmail(e ui.ContactEmail, l string) ui.ContactEmail    { e.Label = l; return e }
func addressLabel(a ui.ContactAddress) string                 { return a.Label }
func setAddress(a ui.ContactAddress, l string) ui.ContactAddress {
	a.Label = l
	return a
}

// Merge is what a contact both sides changed becomes (the rule above).
func Merge(base, local, remote ui.Contact) ui.Contact {
	out := shown(remote)
	for _, f := range []struct{ b, l, r, out *string }{{&base.Name, &local.Name, &remote.Name, &out.Name},
		{&base.Notes, &local.Notes, &remote.Notes, &out.Notes}, {&base.Nickname, &local.Nickname, &remote.Nickname, &out.Nickname},
		{&base.Org, &local.Org, &remote.Org, &out.Org}, {&base.Title, &local.Title, &remote.Title, &out.Title},
		{&base.Birthday, &local.Birthday, &remote.Birthday, &out.Birthday}, {&base.Anniversary, &local.Anniversary, &remote.Anniversary, &out.Anniversary}} {
		if *f.l != *f.b && *f.r == *f.b {
			*f.out = *f.l
		}
	}
	if local.Photo != base.Photo && remote.Photo == base.Photo {
		out.Photo, out.PhotoData = local.Photo, local.PhotoData
	}
	out.Numbers = mergeSet(ui.NumbersOf(base), ui.NumbersOf(local), out.Numbers, numberKey, numberLabel, setNumber)
	out.Emails = mergeSet(base.Emails, local.Emails, out.Emails, emailKey, emailLabel, setEmail)
	out.Addresses = mergeSet(base.Addresses, local.Addresses, out.Addresses, contacts.AddressKey, addressLabel, setAddress)
	out.URLs = mergeNames(base.URLs, local.URLs, out.URLs)
	if c, err := contacts.Clean(out, nil); err == nil {
		return shown(c)
	}
	return shown(local)
}

// link is a contact on the phone and a card on the server found to be the
// same person: the server's name and other fields (the phone's where the
// server has none), the numbers and addresses of both, the server's photo
// (the phone's when the server has none).
func link(local, remote ui.Contact) ui.Contact {
	out := shown(remote)
	for _, f := range []struct{ l, out *string }{{&local.Notes, &out.Notes}, {&local.Nickname, &out.Nickname}, {&local.Org, &out.Org},
		{&local.Title, &out.Title}, {&local.Birthday, &out.Birthday}, {&local.Anniversary, &out.Anniversary}} {
		if *f.out == "" {
			*f.out = *f.l
		}
	}
	if out.Photo == "" {
		out.Photo, out.PhotoData = local.Photo, local.PhotoData
	}
	out.URLs = mergeNames(nil, local.URLs, out.URLs)
	out.Numbers = mergeSet(nil, ui.NumbersOf(local), out.Numbers, numberKey, numberLabel, setNumber)
	out.Emails = mergeSet(nil, local.Emails, out.Emails, emailKey, emailLabel, setEmail)
	out.Addresses = mergeSet(nil, local.Addresses, out.Addresses, contacts.AddressKey, addressLabel, setAddress)
	if c, err := contacts.Clean(out, nil); err == nil {
		return shown(c)
	}
	return out
}

// matches: the same name (any case) and one's numbers within the other's.
func matches(a, b ui.Contact) bool {
	if !strings.EqualFold(a.Name, b.Name) {
		return false
	}
	within := func(x, y ui.Contact) bool {
		have := map[string]bool{}
		for _, n := range ui.NumbersOf(y) {
			have[ui.NumberKey(n.Number)] = true
		}
		for _, n := range ui.NumbersOf(x) {
			if !have[ui.NumberKey(n.Number)] {
				return false
			}
		}
		return true
	}
	return within(a, b) || within(b, a)
}

func (s *Sync) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sync) batch() int {
	if s.Batch > 0 {
		return s.Batch
	}
	return 50
}

func (s *Sync) save() error {
	if s.Save == nil {
		return nil
	}
	return s.Save(s.State)
}

func (s *Sync) progress(phase string, done, total int) {
	if s.Notify != nil {
		s.Notify(phase, done, total)
	}
}

func (s *Sync) index() {
	s.byHref, s.byID = map[string]*Item{}, map[string]*Item{}
	for _, it := range s.State.Items {
		s.byHref[hrefKey(s.book, it.Href)] = it
		if it.ID != "" {
			s.byID[it.ID] = it
		}
	}
}

func (s *Sync) add(it *Item) {
	s.State.Items = append(s.State.Items, it)
	s.byHref[hrefKey(s.book, it.Href)] = it
	if it.ID != "" {
		s.byID[it.ID] = it
	}
	if f, ok := s.photos[hrefKey(s.book, it.Href)]; ok {
		it.PhotoURL, it.PhotoKey = f.url, f.key
	}
}

func (s *Sync) drop(it *Item) {
	for i, x := range s.State.Items {
		if x == it {
			s.State.Items = append(s.State.Items[:i], s.State.Items[i+1:]...)
			break
		}
	}
	delete(s.byHref, hrefKey(s.book, it.Href))
	if it.ID != "" && s.byID[it.ID] == it {
		delete(s.byID, it.ID)
	}
}

func (s *Sync) setID(it *Item, id string) {
	if it.ID != "" && s.byID[it.ID] == it {
		delete(s.byID, it.ID)
	}
	it.ID = id
	if id != "" {
		s.byID[id] = it
	}
}

// queue holds a change to the phone's contacts for the next flush; then
// runs with how it went.
func (s *Sync) queue(op Op, then func(Done) error) {
	s.ops = append(s.ops, op)
	s.then = append(s.then, then)
}

// flush makes the queued changes, in one write.
func (s *Sync) flush() error {
	if len(s.ops) == 0 {
		return nil
	}
	ops, then := s.ops, s.then
	s.ops, s.then = nil, nil
	done, err := s.Local.Apply(ops)
	if err != nil {
		return err
	}
	for i, d := range done {
		if err := then[i](d); err != nil {
			return err
		}
	}
	return nil
}

// failed counts a card that could not be written; an error that is not
// about the card ends the run.
func (s *Sync) failed(err error) error {
	switch {
	case errors.Is(err, ErrAuth), errors.Is(err, ErrFull), errors.Is(err, context.Canceled), Temporary(err):
		return err
	}
	s.res.Failed++
	if s.res.Problem == nil {
		s.res.Problem = err
	}
	return nil
}

// Run is one sync. The state is saved as it goes (Save), so a run cut
// short (no network, the phone off) loses none of what it did.
func (s *Sync) Run(ctx context.Context) (Result, error) {
	s.res, s.ops, s.then = Result{}, nil, nil
	s.want, s.catWrite, s.gname = map[*Item][]string{}, map[*Item]bool{}, map[*Item]string{}
	s.gone, s.photos = map[string]bool{}, map[string]fetched{}
	u, err := url.Parse(s.Book.URL)
	if err != nil {
		return s.res, err
	}
	s.book = u
	if s.State == nil {
		s.State = &State{}
	}
	if s.State.Book != s.Book.URL {
		*s.State = State{Book: s.Book.URL}
	}
	if s.State.Version < stateVersion {
		s.State.Token, s.State.CTag, s.State.Version = "", "", stateVersion
		for _, it := range s.State.Items {
			it.ETag = ""
		}
	}
	if s.NewUID == nil {
		s.NewUID = randomUID
	}
	s.index()
	err = s.pull(ctx)
	if err == nil {
		s.style = s.groupStyle()
		s.State.Groups = s.style
		err = s.linkGroups(ctx)
	}
	if err == nil {
		err = s.members()
	}
	if err == nil && s.Book.ReadOnly {
		err = s.revert(ctx)
	} else if err == nil {
		err = s.push(ctx)
	}
	if err == nil {
		err = s.writeGroups(ctx)
	}
	if ferr := s.flush(); err == nil {
		err = ferr
	}
	if err != nil {
		_ = s.save()
		return s.res, err
	}
	s.State.Synced = true
	return s.res, s.save()
}

// localNow is the book's contacts on the phone, by ID and as a list, and
// every contact's ID with its book. A contact the state links that is in
// another book now is not in them: it moved, and its card goes as the
// card of a contact deleted on the phone.
func (s *Sync) localNow() (map[string]ui.Contact, []ui.Contact, map[string]string, error) {
	list, err := s.Local.List()
	if err != nil {
		return nil, nil, nil, err
	}
	m := map[string]ui.Contact{}
	books := make(map[string]string, len(list))
	var mine []ui.Contact
	for _, c := range list {
		books[c.ID] = c.Book
		if c.Book == s.Key {
			m[c.ID] = c
			mine = append(mine, c)
		}
	}
	return m, mine, books, nil
}

// remoteChanges is what changed on the server: the cards to read, the ones
// deleted, the token to keep once they are in, and whether the listing was
// whole (a card it lacks is gone).
func (s *Sync) remoteChanges(ctx context.Context) (changed []Card, deleted []string, token, ctag string, whole bool, err error) {
	st := s.State
	if !s.Book.Sync {
		_, ctag, err = s.Client.State(ctx, s.book)
		if err != nil {
			return
		}
		if ctag != "" && ctag == st.CTag && st.Synced {
			return nil, nil, "", ctag, false, nil
		}
		changed, err = s.Client.List(ctx, s.book)
		return changed, nil, "", ctag, true, err
	}
	token = st.Token
	whole = token == ""
	limit := s.Limit
	if limit <= 0 {
		limit = 500
	}
	for page := 0; ; page++ {
		lim := limit
		if s.noLimit {
			lim = 0
		}
		var ch Changes
		ch, err = s.Client.SyncCollection(ctx, s.book, token, lim)
		var se *StatusError
		switch {
		case errors.Is(err, ErrToken) && token != "":
			token, whole, changed, deleted, page = "", true, nil, nil, -1
			continue
		case errors.As(err, &se) && !se.Temporary() && page == 0 && lim > 0:
			// A server may refuse a limit (RFC 6578 §3.6): ask again
			// without one.
			s.noLimit, page = true, -1
			continue
		case errors.As(err, &se) && !se.Temporary() && page == 0:
			// No sync-collection after all: compare ETags.
			s.Book.Sync = false
			return s.remoteChanges(ctx)
		case err != nil:
			return nil, nil, "", "", false, err
		}
		changed = append(changed, ch.Changed...)
		deleted = append(deleted, ch.Deleted...)
		if ch.More && ch.Token == token {
			return nil, nil, "", "", false, errors.New("REPORT: the server says there is more but gives the same sync token")
		}
		token = ch.Token
		if !ch.More {
			return changed, deleted, token, "", whole, nil
		}
		s.progress(PhasePull, len(changed), 0)
	}
}

func (s *Sync) pull(ctx context.Context) error {
	changed, deleted, token, ctag, whole, err := s.remoteChanges(ctx)
	if err != nil {
		return err
	}
	local, list, books, err := s.localNow()
	if err != nil {
		return err
	}
	room := len(books) < contacts.MaxContacts
	seen := map[string]bool{}
	var fetch []string
	for _, c := range changed {
		k := hrefKey(s.book, c.Href)
		if seen[k] {
			continue
		}
		seen[k] = true
		it := s.byHref[k]
		if it != nil && c.ETag != "" && it.ETag == c.ETag && !it.New && !(it.Skip == SkipFull && room) {
			continue
		}
		fetch = append(fetch, c.Href)
	}
	for _, it := range s.State.Items {
		if k := hrefKey(s.book, it.Href); room && it.Skip == SkipFull && !seen[k] {
			fetch = append(fetch, it.Href)
			seen[k] = true
		}
	}
	if whole {
		for _, it := range s.State.Items {
			if !it.New && !seen[hrefKey(s.book, it.Href)] {
				deleted = append(deleted, it.Href)
			}
		}
	}
	var unlinked []ui.Contact
	for _, c := range list {
		if s.byID[c.ID] == nil && !s.Book.ReadOnly {
			unlinked = append(unlinked, c)
		}
	}
	if !s.State.Synced && s.Key != "" && !s.Book.ReadOnly {
		all, err := s.Local.List()
		if err != nil {
			return err
		}
		for _, c := range all {
			if c.Book == "" && s.byID[c.ID] == nil {
				unlinked = append(unlinked, c)
			}
		}
	}
	for i := 0; i < len(fetch); i += s.batch() {
		hrefs := fetch[i:min(len(fetch), i+s.batch())]
		cards, missing, err := s.Client.Multiget(ctx, s.book, hrefs)
		if err != nil {
			return err
		}
		for _, card := range cards {
			if err := s.pulled(ctx, card, local, &unlinked); err != nil {
				return err
			}
		}
		deleted = append(deleted, missing...)
		if err := s.flush(); err != nil {
			return err
		}
		s.progress(PhasePull, i+len(hrefs), len(fetch))
		if err := s.save(); err != nil {
			return err
		}
	}
	for _, href := range deleted {
		s.goneRemote(href, local)
	}
	if err := s.flush(); err != nil {
		return err
	}
	if s.Book.Sync {
		s.State.Token = token
	} else if ctag != "" || whole {
		s.State.CTag = ctag
	}
	return s.save()
}

// inBook is a contact of the server's as it goes into the book.
func (s *Sync) inBook(c ui.Contact) ui.Contact {
	c.Book = s.Key
	return c
}

// pulled takes in a card read from the server.
func (s *Sync) pulled(ctx context.Context, card Card, local map[string]ui.Contact, unlinked *[]ui.Contact) error {
	remote, info, perr, err := s.parse(ctx, card)
	if err != nil {
		return err
	}
	it := s.byHref[hrefKey(s.book, card.Href)]
	if it != nil {
		it.UID = firstNonEmpty(info.UID, it.UID)
	}
	switch {
	case info.Group && perr == nil && (it == nil || it.ID == ""):
		if it == nil {
			it = &Item{Href: card.Href, UID: info.UID}
			s.add(it)
		}
		it.Skip, it.ETag, it.Name, it.Members = SkipGroup, card.ETag, remote.Name, info.Members
		return nil
	case it == nil && (info.Group || perr != nil):
		s.add(&Item{Href: card.Href, ETag: card.ETag, UID: info.UID, Skip: SkipEmpty})
		return nil
	case info.Group || perr != nil:
		// Not a contact (any more): what the phone has of it stays.
		it.ETag = card.ETag
		return nil
	case it == nil:
		for i, c := range *unlinked {
			if matches(c, remote) {
				*unlinked = append((*unlinked)[:i], (*unlinked)[i+1:]...)
				it = &Item{Href: card.Href, UID: info.UID, ID: c.ID, Cats: remote.Groups}
				s.add(it)
				return s.reconcile(ctx, it, card, remote, s.inBook(link(c, remote)), c)
			}
		}
		s.addLocal(card, remote, info.UID, nil)
		return nil
	case it.ID == "":
		it.Name, it.Members, it.Group = "", nil, ""
		s.addLocal(card, remote, info.UID, it)
		return nil
	}
	it.Cats = remote.Groups
	loc, ok := local[it.ID]
	if !ok {
		// Deleted on the phone, changed on the server: the edit wins.
		it.New = false
		s.restore(it, card, remote)
		return nil
	}
	merged := shown(remote)
	if !Same(loc, it.Base) && !s.Book.ReadOnly {
		merged = Merge(it.Base, loc, remote)
		s.res.Conflicts++
	}
	it.New = false
	return s.reconcile(ctx, it, card, remote, merged, loc)
}

// parse reads a card from the server as ParseCard does (perr), with the
// picture of a photo at a URL fetched (or known from before); err is a
// failure to fetch it that ends the run (the network), any other leaves
// the contact without the photo.
func (s *Sync) parse(ctx context.Context, card Card) (remote ui.Contact, info contacts.CardInfo, perr, err error) {
	remote, info, perr = contacts.ParseCard(card.Data)
	k := hrefKey(s.book, card.Href)
	it := s.byHref[k]
	if perr != nil || info.Group || info.PhotoURI == "" || remote.Photo != "" {
		if it != nil && perr == nil {
			it.PhotoURL, it.PhotoKey = "", ""
		}
		return remote, info, perr, nil
	}
	if it != nil && it.PhotoURL == info.PhotoURI && it.PhotoKey != "" {
		if data, err := s.Local.Photo(it.PhotoKey); err == nil {
			remote.Photo, remote.PhotoData = it.PhotoKey, data
			return remote, info, nil, nil
		}
	}
	data, err := s.Client.Photo(ctx, s.book, info.PhotoURI)
	switch {
	case err != nil && (Temporary(err) || ctx.Err() != nil):
		return remote, info, nil, err
	case err != nil || !contacts.PhotoOK(data):
		return remote, info, nil, nil
	}
	remote.Photo, remote.PhotoData = contacts.PhotoKey(data), data
	s.photos[k] = fetched{info.PhotoURI, remote.Photo}
	if it != nil {
		it.PhotoURL, it.PhotoKey = info.PhotoURI, remote.Photo
	}
	return remote, info, nil, nil
}

// addLocal adds a card new to the phone (it: the item it had as a card
// that did not fit, or nil).
func (s *Sync) addLocal(card Card, remote ui.Contact, uid string, it *Item) {
	s.queue(Op{Contact: s.inBook(remote)}, func(d Done) error {
		if it == nil {
			it = &Item{Href: card.Href, UID: uid}
			s.add(it)
		}
		it.ETag, it.Cats, it.Groups = card.ETag, remote.Groups, nil
		if errors.Is(d.Err, contacts.ErrFull) {
			it.Skip = SkipFull
			return nil
		}
		if d.Err != nil {
			return d.Err
		}
		s.setID(it, d.Contact.ID)
		it.Base, it.Remote, it.Skip, it.New = shown(d.Contact), true, "", false
		s.res.Added++
		return nil
	})
}

// reconcile makes both sides merged: the server's card (card, read as
// remote) patched with If-Match when it differs, the phone's contact (loc)
// changed when that does.
func (s *Sync) reconcile(ctx context.Context, it *Item, card Card, remote, merged, loc ui.Contact) error {
	if Same(merged, remote) {
		s.setLocal(it, merged, loc, card.ETag, nil, false)
		return nil
	}
	etag, err := s.write(ctx, card, remote, merged)
	switch {
	case errors.Is(err, ErrPrecondition):
		// Changed again meanwhile: the push reads it again and merges,
		// against the same base.
		it.ETag = ""
		return nil
	case err != nil:
		if err := s.failed(err); err != nil {
			return err
		}
		// The phone takes the server's changes; its own are tried
		// again next time, against what the server has now.
		base := shown(remote)
		s.setLocal(it, merged, loc, card.ETag, &base, false)
		return nil
	}
	s.res.Uploaded++
	s.setLocal(it, merged, loc, etag, nil, false)
	return nil
}

// setLocal makes the phone's contact (loc) c, its groups too with groups;
// then the card's ETag is etag and the base both sides agree on c (base,
// when given). Until the store has it, the item stays as it was: a run
// that ends before reads the card again next time. A contact the user
// changed meanwhile keeps the old base and no ETag: the next sync reads
// the card again and merges the user's change against what was agreed
// before.
func (s *Sync) setLocal(it *Item, c, loc ui.Contact, etag string, base *ui.Contact, groups bool) {
	agreed := func(saved ui.Contact) {
		it.ETag, it.Base = etag, shown(saved)
		if base != nil {
			it.Base = *base
		}
		if groups {
			it.Groups, it.Cats = c.Groups, c.Groups
		}
	}
	setGroups := groups && !contacts.SameNames(c.Groups, loc.Groups)
	if Same(c, loc) && !setGroups && (c.Book == "" || c.Book == loc.Book) {
		agreed(loc)
		return
	}
	c.ID = loc.ID
	s.queue(Op{Contact: c, Was: &loc, Groups: setGroups}, func(d Done) error {
		switch {
		case errors.Is(d.Err, ErrChanged):
			it.ETag = ""
			return nil
		case d.Err != nil:
			return d.Err
		}
		if !Same(c, loc) {
			s.res.Changed++
		}
		agreed(d.Contact)
		return nil
	})
}

// withPhoto gives c its photo's bytes when a card is to get them: its
// photo is not the card's (cardPhoto).
func (s *Sync) withPhoto(c ui.Contact, cardPhoto string) (ui.Contact, error) {
	if c.Photo == "" || c.Photo == cardPhoto || c.PhotoData != nil {
		return c, nil
	}
	data, err := s.Local.Photo(c.Photo)
	if err != nil {
		return c, err
	}
	c.PhotoData = data
	return c, nil
}

// write puts card (read as remote) patched to show c, with If-Match: its
// ETag, and is its new ETag.
func (s *Sync) write(ctx context.Context, card Card, remote, c ui.Contact) (string, error) {
	c, err := s.withPhoto(c, remote.Photo)
	if err != nil {
		return "", err
	}
	data, err := contacts.PatchCardFetched(card.Data, c, false, s.now(), remote.Photo)
	if err != nil {
		return "", err
	}
	if s.Book.MaxSize > 0 && int64(len(data)) > s.Book.MaxSize {
		return "", ErrTooLarge
	}
	etag, err := s.Client.Put(ctx, s.book, card.Href, data, card.ETag)
	if err != nil {
		return "", err
	}
	return s.etagAfter(ctx, card.Href, etag), nil
}

// etagAfter is a written card's ETag, read again when the server changed
// the card as it stored it ("" when that fails: the next sync reads it).
func (s *Sync) etagAfter(ctx context.Context, href, etag string) string {
	if etag != "" {
		return etag
	}
	if got, err := s.Client.Get(ctx, s.book, href); err == nil {
		return got.ETag
	}
	return ""
}

// goneRemote: the server deleted the card.
func (s *Sync) goneRemote(href string, local map[string]ui.Contact) {
	it := s.byHref[hrefKey(s.book, href)]
	if it == nil || it.New {
		return
	}
	if it.ID == "" {
		s.drop(it)
		return
	}
	loc, ok := local[it.ID]
	switch {
	case !ok:
		s.drop(it)
	case Same(loc, it.Base) || s.Book.ReadOnly:
		s.queue(Op{Was: &loc, Delete: true}, func(d Done) error {
			switch {
			case errors.Is(d.Err, ErrChanged):
				it.New, it.ETag = true, ""
				return nil
			case d.Err != nil:
				return d.Err
			}
			s.drop(it)
			s.res.Deleted++
			return nil
		})
	default:
		// Changed on the phone, deleted on the server: the edit wins,
		// as a new card where the old one was.
		it.New, it.ETag = true, ""
		s.res.Conflicts++
	}
}

func (s *Sync) push(ctx context.Context) error {
	local, list, _, err := s.localNow()
	if err != nil {
		return err
	}
	type job struct {
		it  *Item
		loc ui.Contact
		del bool
	}
	var jobs []job
	for _, it := range append([]*Item(nil), s.State.Items...) {
		if it.ID == "" {
			continue
		}
		loc, ok := local[it.ID]
		switch {
		case !ok && it.New:
			s.drop(it)
		case !ok:
			jobs = append(jobs, job{it: it, del: true})
		case it.New || !Same(loc, it.Base) || s.catWrite[it]:
			jobs = append(jobs, job{it: it, loc: loc})
		}
	}
	for _, c := range list {
		if s.byID[c.ID] == nil {
			uid := s.NewUID()
			it := &Item{Href: s.book.EscapedPath() + uid + ".vcf", UID: uid, ID: c.ID, Base: shown(c), New: true}
			s.add(it)
			jobs = append(jobs, job{it: it, loc: c})
		}
	}
	if len(jobs) == 0 {
		return nil
	}
	if err := s.save(); err != nil {
		return err
	}
	for i, j := range jobs {
		var err error
		switch {
		case j.del:
			err = s.pushDelete(ctx, j.it)
		case j.it.New:
			err = s.pushNew(ctx, j.it, j.loc)
		default:
			err = s.pushChange(ctx, j.it, j.loc)
		}
		if err != nil {
			if err := s.failed(err); err != nil {
				return err
			}
		}
		if (i+1)%s.batch() == 0 {
			if err := s.flush(); err != nil {
				return err
			}
			s.progress(PhasePush, i+1, len(jobs))
			if err := s.save(); err != nil {
				return err
			}
		}
	}
	s.progress(PhasePush, len(jobs), len(jobs))
	return s.flush()
}

// pushDelete deletes on the server a card deleted on the phone, if the
// server's is still the one both agreed on; else the edit wins and the
// contact comes back.
func (s *Sync) pushDelete(ctx context.Context, it *Item) error {
	for try := 0; try < 2; try++ {
		if it.ETag == "" || try > 0 {
			card, err := s.Client.Get(ctx, s.book, it.Href)
			if errors.Is(err, ErrNotFound) {
				s.drop(it)
				return nil
			}
			if err != nil {
				return err
			}
			remote, _, perr, err := s.parse(ctx, card)
			if err != nil {
				return err
			}
			if perr == nil && !Same(remote, it.Base) {
				s.restore(it, card, remote)
				return nil
			}
			it.ETag = card.ETag
		}
		err := s.Client.Delete(ctx, s.book, it.Href, it.ETag)
		if errors.Is(err, ErrPrecondition) {
			continue
		}
		if err != nil {
			return err
		}
		s.drop(it)
		s.gone[contacts.UIDKey(it.UID)] = true
		s.res.Removed++
		return nil
	}
	return ErrPrecondition
}

// revert is a ReadOnly book's push: the contacts the phone changed or
// deleted get the server's cards back, the phone's own in the book stay
// on the phone; nothing is written to the server.
func (s *Sync) revert(ctx context.Context) error {
	local, _, _, err := s.localNow()
	if err != nil {
		return err
	}
	for _, it := range append([]*Item(nil), s.State.Items...) {
		if it.ID == "" || it.Skip != "" {
			continue
		}
		loc, ok := local[it.ID]
		switch {
		case it.New:
			// A card the phone was making before the book became
			// read-only: never on the server, its contact stays.
			s.drop(it)
			continue
		case ok && Same(loc, it.Base):
			continue
		}
		card, err := s.Client.Get(ctx, s.book, it.Href)
		if errors.Is(err, ErrNotFound) {
			if ok {
				s.queue(Op{Was: &loc, Delete: true}, func(d Done) error {
					if d.Err == nil {
						s.res.Deleted++
					}
					return ignoreChanged(d.Err)
				})
			}
			s.drop(it)
			continue
		}
		if err != nil {
			return err
		}
		remote, _, perr, err := s.parse(ctx, card)
		if err != nil {
			return err
		}
		if perr != nil {
			continue
		}
		if !ok {
			s.restore(it, card, remote)
			continue
		}
		s.setLocal(it, shown(remote), loc, card.ETag, nil, false)
	}
	return s.flush()
}

// restore brings back on the phone a contact deleted there that the
// server changed.
func (s *Sync) restore(it *Item, card Card, remote ui.Contact) {
	s.queue(Op{Contact: s.inBook(remote)}, func(d Done) error {
		if d.Err != nil {
			if errors.Is(d.Err, contacts.ErrFull) {
				it.Skip, it.ETag = SkipFull, card.ETag
				return nil
			}
			return d.Err
		}
		s.setID(it, d.Contact.ID)
		it.ETag, it.Base, it.Cats, it.Groups = card.ETag, shown(d.Contact), remote.Groups, nil
		s.res.Added++
		s.res.Conflicts++
		return nil
	})
}

func (s *Sync) pushNew(ctx context.Context, it *Item, loc ui.Contact) error {
	c, err := s.withPhoto(loc, "")
	if err != nil {
		return err
	}
	cats := s.style == GroupsCategories
	if !cats {
		c.Groups = nil
	}
	data := contacts.NewCard(c, it.UID, s.Book.Version, s.now())
	if s.Book.MaxSize > 0 && int64(len(data)) > s.Book.MaxSize {
		return ErrTooLarge
	}
	etag, err := s.Client.Put(ctx, s.book, it.Href, data, "")
	if errors.Is(err, ErrPrecondition) {
		// Something is there: this card, if an earlier run's PUT went
		// through and its answer was lost; then it is changed as any.
		card, gerr := s.Client.Get(ctx, s.book, it.Href)
		if gerr != nil {
			return gerr
		}
		remote, info, perr, gerr := s.parse(ctx, card)
		if gerr != nil {
			return gerr
		}
		if info.UID != it.UID {
			uid := s.NewUID()
			s.drop(it)
			it.Href, it.UID = s.book.EscapedPath()+uid+".vcf", uid
			s.add(it)
			return s.pushNew(ctx, it, loc)
		}
		it.New, it.ETag = false, card.ETag
		if perr == nil {
			it.Base, it.Cats = shown(remote), remote.Groups
		}
		return s.pushChange(ctx, it, loc)
	}
	if err != nil {
		return err
	}
	it.New, it.Base = false, shown(loc)
	it.ETag = s.etagAfter(ctx, it.Href, etag)
	if cats {
		it.Groups, it.Cats = loc.Groups, loc.Groups
	} else {
		s.want[it] = loc.Groups
	}
	s.res.Uploaded++
	return nil
}

// pushChange writes a contact changed on the phone: the server's card read
// again (the one it.ETag names, or a newer one, merged), patched, put with
// If-Match; a 412 starts over, once. A book that keeps groups in
// CATEGORIES gets the contact's groups merged with the card's.
func (s *Sync) pushChange(ctx context.Context, it *Item, loc ui.Contact) error {
	for try := 0; ; try++ {
		card, err := s.Client.Get(ctx, s.book, it.Href)
		if errors.Is(err, ErrNotFound) {
			// Deleted on the server meanwhile: the edit wins.
			it.New, it.ETag = true, ""
			return s.pushNew(ctx, it, loc)
		}
		if err != nil {
			return err
		}
		remote, _, perr, err := s.parse(ctx, card)
		if err != nil {
			return err
		}
		merged := shown(loc)
		if perr == nil && card.ETag != it.ETag {
			merged = Merge(it.Base, loc, remote)
			s.res.Conflicts++
		}
		cats := s.style == GroupsCategories && perr == nil
		if cats {
			merged.Groups = mergeNames(it.Groups, loc.Groups, remote.Groups)
		}
		patched, err := s.withPhoto(merged, remote.Photo)
		if err != nil {
			return err
		}
		data, err := contacts.PatchCardFetched(card.Data, patched, cats, s.now(), remote.Photo)
		if err != nil {
			return err
		}
		etag := card.ETag
		if !bytes.Equal(data, card.Data) {
			if s.Book.MaxSize > 0 && int64(len(data)) > s.Book.MaxSize {
				return ErrTooLarge
			}
			etag, err = s.Client.Put(ctx, s.book, it.Href, data, card.ETag)
			if errors.Is(err, ErrPrecondition) && try == 0 {
				continue
			}
			if err != nil {
				return err
			}
			etag = s.etagAfter(ctx, it.Href, etag)
			s.res.Uploaded++
		}
		s.setLocal(it, merged, loc, etag, nil, cats)
		return nil
	}
}

// ignoreChanged: a contact the user changed meanwhile is the next sync's.
func ignoreChanged(err error) error {
	if errors.Is(err, ErrChanged) {
		return nil
	}
	return err
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// StoreLocal is the contacts store as Local: not safe for concurrent use,
// so its owner calls it from the goroutine that owns the store. changed,
// if set, runs after a batch that changed something.
func StoreLocal(s *contacts.Store, changed func()) Local { return &storeLocal{s, changed} }

type storeLocal struct {
	s       *contacts.Store
	changed func()
}

func (l *storeLocal) named(c ui.Contact) ui.Contact {
	c.Groups = l.s.GroupNames(c.Groups)
	return c
}

func (l *storeLocal) List() ([]ui.Contact, error) {
	list := l.s.List()
	for i := range list {
		list[i] = l.named(list[i])
	}
	return list, nil
}

func (l *storeLocal) Groups() ([]ui.ContactGroup, error) { return l.s.Groups(), nil }

func (l *storeLocal) Photo(key string) ([]byte, error) { return l.s.Photo(key) }

func (l *storeLocal) Apply(ops []Op) ([]Done, error) {
	out := make([]Done, len(ops))
	n := 0
	err := l.s.Batch(func() {
		for i, op := range ops {
			out[i] = l.apply(op)
			if out[i].Err == nil {
				n++
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if n > 0 && l.changed != nil {
		l.changed()
	}
	return out, nil
}

func (l *storeLocal) apply(op Op) Done {
	if g := op.Group; g != nil {
		if g.ID == "" {
			if got, ok := l.s.GroupNamed(g.Name); ok {
				return Done{Group: got}
			}
			got, err := l.s.AddGroup(g.Name)
			return Done{Group: got, Err: err}
		}
		got, err := l.s.RenameGroup(g.ID, g.Name)
		return Done{Group: got, Err: err}
	}
	c := op.Contact
	c.ID = ""
	if op.Was != nil {
		cur, ok := l.s.Get(op.Was.ID)
		if ok {
			cur = l.named(cur)
		}
		if !ok || !Same(cur, *op.Was) || op.Groups && !contacts.SameNames(cur.Groups, op.Was.Groups) {
			return Done{Err: ErrChanged}
		}
		if op.Delete {
			return Done{Contact: cur, Err: l.s.Delete(cur.ID)}
		}
		c.ID = cur.ID
		if Same(c, cur) && (c.Book == "" || c.Book == cur.Book) {
			if op.Groups {
				if err := l.s.SetGroupsOf(cur.ID, c.Groups); err != nil && !errors.Is(err, contacts.ErrGroupsFull) {
					return Done{Err: err}
				}
			}
			return Done{Contact: cur}
		}
	}
	saved, err := l.s.Save(c)
	if err == nil && op.Groups {
		if err := l.s.SetGroupsOf(saved.ID, c.Groups); err != nil && !errors.Is(err, contacts.ErrGroupsFull) {
			return Done{Err: err}
		}
	}
	return Done{Contact: saved, Err: err}
}
