package carddav

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/ui"
)

// Groups go both ways, by name: a group on the phone is the group of that
// name in each book its contacts are in. Servers keep groups one of two
// ways, and a book is synced the way its cards do (State.Groups):
//
//   - GroupsVCard: a card for each group (vCard 4.0's KIND:group with
//     MEMBER, Apple's X-ADDRESSBOOKSERVER-KIND and -MEMBER: iCloud, iPhone
//     Contacts) naming its members by UID;
//   - GroupsCategories: each contact's card names its groups (CATEGORIES:
//     Nextcloud, Google, DAVx⁵ by default).
//
// A book with a group card is GroupsVCard (the CATEGORIES its cards may
// have then stay as they are, unshown), one with CATEGORIES on a card and
// no group card GroupsCategories; one with neither keeps what the last
// sync found, and a new one is what the Sync says (Sync.Groups: the Shell
// says GroupsVCard for the Kario account, whose other clients are iPhones),
// else GroupsVCard on iCloud (iPhone Contacts shows no other) and
// GroupsCategories elsewhere (any server keeps those, in the cards it
// keeps as they are).
//
// A contact's groups merge as its numbers do, against the names both sides
// last agreed on (Item.Groups): one added on either side stays, one taken
// out on either side goes. A group card renamed on the server renames the
// phone's group; one renamed on the phone, not on the server, renames the
// card; a group deleted on the phone deletes its card (unless the server
// renamed it meanwhile); one deleted on the server takes its contacts out
// of the phone's group, which stays. A group without a member in the book
// gets no card until it has one. A read-only book's groups are the
// server's: a group the phone renamed or deleted is, for its contacts, a
// group of the card's name again, and no card is written.

const (
	GroupsVCard      = "vcard"
	GroupsCategories = "categories"
)

// mergeNames merges a set of group names both sides changed, in any case.
func mergeNames(base, local, remote []string) []string {
	key := func(n string) string { return strings.ToLower(n) }
	label := func(string) string { return "" }
	relabel := func(n, _ string) string { return n }
	return mergeSet(base, local, remote, key, label, relabel)
}

// groupStyle is how the book keeps groups (the rule above).
func (s *Sync) groupStyle() string {
	for _, it := range s.State.Items {
		if it.Skip == SkipGroup {
			return GroupsVCard
		}
	}
	for _, it := range s.State.Items {
		if len(it.Cats) > 0 {
			return GroupsCategories
		}
	}
	if s.State.Groups != "" {
		return s.State.Groups
	}
	if s.Groups != "" {
		return s.Groups
	}
	if h := strings.ToLower(s.book.Hostname()); h == "icloud.com" || strings.HasSuffix(h, ".icloud.com") {
		return GroupsVCard
	}
	return GroupsCategories
}

// groupName is a group card's name as this run has it.
func (s *Sync) groupName(it *Item) string {
	if n, ok := s.gname[it]; ok {
		return n
	}
	return it.Name
}

// linkGroups ties each group card to the phone's group it is: renamed
// either way, deleted on the server for one deleted on the phone, made
// on the phone for one new on the server.
func (s *Sync) linkGroups(ctx context.Context) error {
	if s.style != GroupsVCard {
		return nil
	}
	groups, err := s.Local.Groups()
	if err != nil {
		return err
	}
	byID, byName := map[string]ui.ContactGroup{}, map[string]ui.ContactGroup{}
	for _, g := range groups {
		byID[g.ID], byName[strings.ToLower(g.Name)] = g, g
	}
	for _, it := range append([]*Item(nil), s.State.Items...) {
		if it.Skip != SkipGroup {
			continue
		}
		if it.Group != "" {
			pg, ok := byID[it.Group]
			switch {
			case s.Book.ReadOnly && (!ok || pg.Name != it.Name):
				it.Group = ""
			case !ok && it.Name == it.Base.Name:
				if err := s.deleteGroupCard(ctx, it); err != nil {
					if err := s.failed(err); err != nil {
						return err
					}
				}
				continue
			case !ok:
				it.Group = ""
			case it.Name != it.Base.Name:
				if other, taken := byName[strings.ToLower(it.Name)]; taken && other.ID != pg.ID {
					it.Group = other.ID
				} else if pg.Name != it.Name {
					s.queue(Op{Group: &ui.ContactGroup{ID: pg.ID, Name: it.Name}}, func(d Done) error { return d.Err })
				}
				it.Base.Name = it.Name
				continue
			case pg.Name != it.Base.Name:
				s.gname[it] = pg.Name
				continue
			default:
				continue
			}
		}
		if g, ok := byName[strings.ToLower(it.Name)]; ok {
			it.Group, it.Base.Name = g.ID, it.Name
			continue
		}
		s.queue(Op{Group: &ui.ContactGroup{Name: it.Name}}, func(d Done) error {
			switch {
			case errors.Is(d.Err, contacts.ErrGroupsFull):
				return nil
			case d.Err != nil:
				return d.Err
			}
			it.Group, it.Base.Name = d.Group.ID, it.Name
			return nil
		})
	}
	return s.flush()
}

// deleteGroupCard deletes the card of a group deleted on the phone, if it
// is still the one last read.
func (s *Sync) deleteGroupCard(ctx context.Context, it *Item) error {
	err := s.Client.Delete(ctx, s.book, it.Href, it.ETag)
	if errors.Is(err, ErrPrecondition) {
		it.ETag, it.Group = "", ""
		return nil
	}
	if err != nil {
		return err
	}
	s.drop(it)
	s.res.Removed++
	return nil
}

// members merges each contact's groups and makes the phone's side so; the
// server's waits for the push (CATEGORIES) or writeGroups (group cards).
func (s *Sync) members() error {
	local, _, _, err := s.localNow()
	if err != nil {
		return err
	}
	in := map[string][]string{}
	if s.style == GroupsVCard {
		for _, g := range s.State.Items {
			if g.Skip == SkipGroup {
				for _, m := range g.Members {
					in[m] = append(in[m], s.groupName(g))
				}
			}
		}
	}
	for _, it := range s.State.Items {
		loc, ok := local[it.ID]
		if it.ID == "" || it.Skip != "" || it.New || !ok {
			continue
		}
		remote := it.Cats
		if s.style == GroupsVCard {
			remote = in[contacts.UIDKey(it.UID)]
		}
		merged := mergeNames(it.Groups, loc.Groups, remote)
		if s.Book.ReadOnly {
			merged = remote
		}
		s.want[it] = merged
		done := contacts.SameNames(merged, remote)
		if s.style == GroupsCategories && !done {
			s.catWrite[it] = true
		}
		if contacts.SameNames(merged, loc.Groups) {
			if done {
				it.Groups = merged
			}
			continue
		}
		c := loc
		c.Groups = merged
		s.queue(Op{Contact: c, Was: &loc, Groups: true}, func(d Done) error {
			switch {
			case errors.Is(d.Err, ErrChanged):
				delete(s.want, it)
				delete(s.catWrite, it)
				return nil
			case d.Err != nil:
				return d.Err
			}
			if done {
				it.Groups = merged
			}
			return nil
		})
	}
	return s.flush()
}

// writeGroups writes the group cards the merged groups change, and new
// ones for the groups of the book's contacts that have none. Once all are
// written, each contact's groups are agreed.
func (s *Sync) writeGroups(ctx context.Context) error {
	if s.style != GroupsVCard || s.Book.ReadOnly {
		for it, want := range s.want {
			if s.Book.ReadOnly {
				it.Groups = want
			}
		}
		return nil
	}
	mine := map[string]bool{}
	in := map[string][]string{}
	names := map[string]string{}
	for _, it := range s.State.Items {
		if it.ID == "" || it.Skip != "" || it.New || it.UID == "" {
			continue
		}
		mine[contacts.UIDKey(it.UID)] = true
		want, ok := s.want[it]
		if !ok {
			want = it.Groups
		}
		for _, n := range want {
			k := strings.ToLower(n)
			in[k] = append(in[k], it.UID)
			if names[k] == "" {
				names[k] = n
			}
		}
	}
	ok := true
	have := map[string]bool{}
	for _, g := range append([]*Item(nil), s.State.Items...) {
		if g.Skip != SkipGroup {
			continue
		}
		name := s.groupName(g)
		k := strings.ToLower(name)
		have[k] = true
		var members []string
		for _, m := range g.Members {
			if !mine[m] && !s.gone[m] {
				members = append(members, m)
			}
		}
		members = append(members, in[k]...)
		if name == g.Name && sameMembers(members, g.Members) {
			continue
		}
		if err := s.putGroup(ctx, g, name, members); err != nil {
			ok = false
			if err := s.groupFailed(err); err != nil {
				return err
			}
		}
	}
	var lgroups []ui.ContactGroup
	for k, uids := range in {
		if have[k] || len(uids) == 0 {
			continue
		}
		if lgroups == nil {
			var err error
			if lgroups, err = s.Local.Groups(); err != nil {
				return err
			}
		}
		uid := s.NewUID()
		g := &Item{Href: s.book.EscapedPath() + uid + ".vcf", UID: uid, Skip: SkipGroup, New: true}
		for _, lg := range lgroups {
			if strings.EqualFold(lg.Name, names[k]) {
				g.Group = lg.ID
			}
		}
		s.add(g)
		if err := s.putGroup(ctx, g, names[k], uids); err != nil {
			ok = false
			if err := s.groupFailed(err); err != nil {
				return err
			}
		}
	}
	if ok {
		for it, want := range s.want {
			it.Groups = want
		}
	}
	return nil
}

// groupFailed: a group card changed meanwhile is read again next time,
// as any other is counted (failed).
func (s *Sync) groupFailed(err error) error {
	if errors.Is(err, ErrPrecondition) {
		return nil
	}
	return s.failed(err)
}

func sameMembers(a []string, keys []string) bool {
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	seen := map[string]bool{}
	for _, m := range a {
		k := contacts.UIDKey(m)
		if !set[k] {
			return false
		}
		seen[k] = true
	}
	return len(seen) == len(set)
}

// putGroup writes a group card with its name and members: a new one (New),
// or the server's read again and patched, with If-Match the ETag last
// read (one changed meanwhile waits for the next sync, read again).
func (s *Sync) putGroup(ctx context.Context, g *Item, name string, members []string) error {
	var data []byte
	etag := ""
	if g.New {
		data = contacts.NewGroupCard(name, g.UID, members, s.Book.Version, s.now())
	} else {
		card, err := s.Client.Get(ctx, s.book, g.Href)
		if errors.Is(err, ErrNotFound) {
			s.drop(g)
			return nil
		}
		if err != nil {
			return err
		}
		if card.ETag != g.ETag {
			g.ETag = ""
			return ErrPrecondition
		}
		if data, err = contacts.PatchGroupCard(card.Data, name, members, s.now()); err != nil {
			return err
		}
		etag = card.ETag
	}
	got, err := s.Client.Put(ctx, s.book, g.Href, data, etag)
	if errors.Is(err, ErrPrecondition) {
		if g.New {
			s.drop(g)
		}
		g.ETag = ""
		return err
	}
	if err != nil {
		if g.New {
			s.drop(g)
		}
		return err
	}
	keys := make([]string, 0, len(members))
	for _, m := range members {
		if k := contacts.UIDKey(m); !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	g.New, g.Name, g.Base.Name, g.Members = false, name, name, keys
	g.ETag = s.etagAfter(ctx, g.Href, got)
	s.res.Uploaded++
	return nil
}
