package contacts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/kario-os/kario/statefile"
	"github.com/kario-os/kario/ui"
)

// Groups are a file beside the contacts' (contact groups by name, each
// with the IDs of its members), so that a contact changed by a sync, which
// rewrites the whole contact, keeps its groups. A contacts account syncs
// them by name (package carddav). A member that is no contact any more
// (deleted) is left out when read and when the groups are written.

// Limits of the groups.
const (
	MaxGroups    = 100
	MaxGroupName = 40 // characters
)

var (
	ErrGroupName   = errors.New("a group needs a name")
	ErrGroupExists = errors.New("there is a group of that name")
	ErrGroupsFull  = fmt.Errorf("there are %d groups already", MaxGroups)
	ErrMergeTwo    = errors.New("merging takes two contacts or more")
	ErrMergeFull   = fmt.Errorf("a contact holds at most %d numbers", MaxNumbers)
)

func (s *Store) groupsPath() string {
	return filepath.Join(filepath.Dir(s.path), "contact_groups.json")
}

// groupsFile is the groups' file: the groups, and the number of the last
// group ID given (IDs are not given twice).
type groupsFile struct {
	Seq    int               `json:"seq"`
	Groups []ui.ContactGroup `json:"groups"`
}

// loadGroups reads the groups; a file that does not parse is moved aside
// and the groups start empty.
func (s *Store) loadGroups() {
	raw, err := os.ReadFile(s.groupsPath())
	if err != nil {
		return
	}
	var f groupsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		_ = os.Rename(s.groupsPath(), s.groupsPath()+".damaged")
		return
	}
	s.gseq = max(f.Seq, 0)
	seen := map[string]bool{}
	for _, g := range f.Groups {
		if n, ok := strings.CutPrefix(g.ID, "g"); ok {
			if v, err := strconv.Atoi(n); err == nil && v > s.gseq {
				s.gseq = v
			}
		}
		g.Name = cut(oneLine(g.Name), MaxGroupName)
		if g.ID == "" || seen[g.ID] || g.Name == "" || len(s.groups) == MaxGroups {
			continue
		}
		seen[g.ID] = true
		g.Members = slices.Compact(slices.Clone(g.Members))
		s.groups = append(s.groups, g)
	}
}

// Groups are the groups in the order they were made, each with the
// members that are contacts (a copy).
func (s *Store) Groups() []ui.ContactGroup { return s.liveGroups(s.groups) }

// liveGroups is a copy of list with only the members that are contacts.
func (s *Store) liveGroups(list []ui.ContactGroup) []ui.ContactGroup {
	live := make(map[string]bool, len(s.list))
	for _, c := range s.list {
		live[c.ID] = true
	}
	out := make([]ui.ContactGroup, len(list))
	for i, g := range list {
		g.Members = slices.DeleteFunc(slices.Clone(g.Members), func(id string) bool { return !live[id] })
		if g.Members == nil {
			g.Members = []string{}
		}
		out[i] = g
	}
	return out
}

func (s *Store) cloneGroups() []ui.ContactGroup {
	out := make([]ui.ContactGroup, len(s.groups))
	for i, g := range s.groups {
		g.Members = slices.Clone(g.Members)
		out[i] = g
	}
	return out
}

// GroupNamed is the group of that name, in any case.
func (s *Store) GroupNamed(name string) (ui.ContactGroup, bool) {
	name = cut(oneLine(name), MaxGroupName)
	for i, g := range s.groups {
		if strings.EqualFold(g.Name, name) {
			return s.groupAt(i), true
		}
	}
	return ui.ContactGroup{}, false
}

// SetGroupsOf puts a contact in the groups of these names, made when
// there are none, and takes it out of the others. A name past MaxGroups
// is ErrGroupsFull, after the rest is done.
func (s *Store) SetGroupsOf(id string, names []string) error {
	if s.at(id) < 0 {
		return ErrNotFound
	}
	want := map[string]bool{}
	for _, n := range names {
		if n = cut(oneLine(n), MaxGroupName); n != "" {
			want[strings.ToLower(n)] = true
		}
	}
	var full error
	return errors.Join(s.changeGroups(func(list []ui.ContactGroup) []ui.ContactGroup {
		have := map[string]bool{}
		for i := range list {
			k := strings.ToLower(list[i].Name)
			in := slices.Contains(list[i].Members, id)
			switch {
			case want[k] && !in:
				list[i].Members = append(list[i].Members, id)
			case !want[k] && in:
				list[i].Members = slices.DeleteFunc(list[i].Members, func(x string) bool { return x == id })
			}
			have[k] = true
		}
		for _, n := range names {
			n = cut(oneLine(n), MaxGroupName)
			if k := strings.ToLower(n); n != "" && !have[k] {
				have[k] = true
				if len(list) >= MaxGroups {
					full = ErrGroupsFull
					continue
				}
				s.gseq++
				list = append(list, ui.ContactGroup{ID: "g" + strconv.Itoa(s.gseq), Name: n, Members: []string{id}})
			}
		}
		return list
	}), full)
}

// GroupNames are the names of the groups with these IDs.
func (s *Store) GroupNames(ids []string) []string {
	var out []string
	for _, g := range s.groups {
		if slices.Contains(ids, g.ID) {
			out = append(out, g.Name)
		}
	}
	return out
}

// groupsOf maps each contact to the groups it is in.
func (s *Store) groupsOf() map[string][]string {
	out := map[string][]string{}
	for _, g := range s.groups {
		for _, id := range g.Members {
			out[id] = append(out[id], g.ID)
		}
	}
	return out
}

func (s *Store) group(id string) int {
	return slices.IndexFunc(s.groups, func(g ui.ContactGroup) bool { return g.ID == id })
}

// groupName is a name as a group keeps it; ErrGroupName for none,
// ErrGroupExists when another group (not except) has it.
func (s *Store) groupName(name, except string) (string, error) {
	name = cut(oneLine(name), MaxGroupName)
	if name == "" {
		return "", ErrGroupName
	}
	for _, g := range s.groups {
		if g.ID != except && strings.EqualFold(g.Name, name) {
			return "", ErrGroupExists
		}
	}
	return name, nil
}

// AddGroup makes a group without members.
func (s *Store) AddGroup(name string) (ui.ContactGroup, error) {
	name, err := s.groupName(name, "")
	if err != nil {
		return ui.ContactGroup{}, err
	}
	if len(s.groups) >= MaxGroups {
		return ui.ContactGroup{}, ErrGroupsFull
	}
	s.gseq++
	g := ui.ContactGroup{ID: "g" + strconv.Itoa(s.gseq), Name: name, Members: []string{}}
	if err := s.changeGroups(func(list []ui.ContactGroup) []ui.ContactGroup { return append(list, g) }); err != nil {
		s.gseq--
		return ui.ContactGroup{}, err
	}
	return g, nil
}

// RenameGroup gives a group another name.
func (s *Store) RenameGroup(id, name string) (ui.ContactGroup, error) {
	i := s.group(id)
	if i < 0 {
		return ui.ContactGroup{}, ErrNotFound
	}
	name, err := s.groupName(name, id)
	if err != nil {
		return ui.ContactGroup{}, err
	}
	err = s.changeGroups(func(list []ui.ContactGroup) []ui.ContactGroup { list[i].Name = name; return list })
	return s.groupAt(i), err
}

// DeleteGroup removes a group; its contacts stay.
func (s *Store) DeleteGroup(id string) error {
	i := s.group(id)
	if i < 0 {
		return ErrNotFound
	}
	return s.changeGroups(func(list []ui.ContactGroup) []ui.ContactGroup { return slices.Delete(list, i, i+1) })
}

// SetMembers puts contacts into a group (in) or takes them out; IDs of no
// contact are left alone.
func (s *Store) SetMembers(id string, contacts []string, in bool) (ui.ContactGroup, error) {
	i := s.group(id)
	if i < 0 {
		return ui.ContactGroup{}, ErrNotFound
	}
	err := s.changeGroups(func(list []ui.ContactGroup) []ui.ContactGroup {
		m := list[i].Members
		for _, c := range contacts {
			has := slices.Contains(m, c)
			switch {
			case in && !has && s.at(c) >= 0:
				m = append(m, c)
			case !in && has:
				m = slices.DeleteFunc(m, func(x string) bool { return x == c })
			}
		}
		list[i].Members = m
		return list
	})
	return s.groupAt(i), err
}

func (s *Store) groupAt(i int) ui.ContactGroup {
	if i < len(s.groups) {
		return s.Groups()[i]
	}
	return ui.ContactGroup{}
}

// changeGroups writes the groups f makes of a copy of them; when that
// fails they stay as they were. In a Batch, f changes them in memory and
// the batch writes them.
func (s *Store) changeGroups(f func([]ui.ContactGroup) []ui.ContactGroup) error {
	if s.batch {
		s.groups, s.gdirty = f(s.groups), true
		return nil
	}
	return s.saveGroups(f(s.liveGroups(s.groups)))
}

// saveGroups writes list, without members that are no contacts.
func (s *Store) saveGroups(list []ui.ContactGroup) error {
	if s.unreadable != nil {
		return fmt.Errorf("the contacts file could not be read, so nothing is written: %w", s.unreadable)
	}
	list = s.liveGroups(list)
	raw, err := json.MarshalIndent(groupsFile{Seq: s.gseq, Groups: list}, "", "  ")
	if err != nil {
		return err
	}
	if err := statefile.Write(s.groupsPath(), raw, 0o600); err != nil {
		return err
	}
	s.groups = list
	return nil
}

// Merge makes the contacts with these IDs one: the first, with the
// numbers, e-mail, postal and web addresses, notes and groups of the
// others (its name, unless it is only its number and another has a name;
// its photo, nickname, organization, title, birthday and anniversary, or
// the first of theirs), in one write; the others go. It refuses what would
// hold more than MaxNumbers numbers rather than drop some.
func (s *Store) Merge(ids []string) (ui.Contact, error) {
	var list []ui.Contact
	for _, id := range ids {
		if c, ok := s.Get(id); ok && !slices.ContainsFunc(list, func(x ui.Contact) bool { return x.ID == id }) {
			list = append(list, c)
		}
	}
	if len(list) < 2 {
		return ui.Contact{}, ErrMergeTwo
	}
	merged := list[0]
	merged.Groups = nil
	keys := map[string]bool{}
	var nums []ui.ContactNumber
	var notes []string
	merged.Emails, merged.Addresses, merged.URLs = nil, nil, nil
	for _, c := range list {
		if merged.Name == merged.Number && c.Name != c.Number {
			merged.Name = c.Name
		}
		if merged.Photo == "" {
			merged.Photo = c.Photo
		}
		merged.Nickname = firstOf(merged.Nickname, c.Nickname)
		merged.Org = firstOf(merged.Org, c.Org)
		merged.Title = firstOf(merged.Title, c.Title)
		merged.Birthday = firstOf(merged.Birthday, c.Birthday)
		merged.Anniversary = firstOf(merged.Anniversary, c.Anniversary)
		merged.Emails = append(merged.Emails, c.Emails...)
		merged.Addresses = append(merged.Addresses, c.Addresses...)
		merged.URLs = append(merged.URLs, c.URLs...)
		for _, n := range ui.NumbersOf(c) {
			if k := ui.NumberKey(n.Number); !keys[k] {
				keys[k] = true
				nums = append(nums, n)
			}
		}
		if n := strings.TrimSpace(c.Notes); n != "" && !slices.Contains(notes, n) {
			notes = append(notes, n)
		}
	}
	if len(nums) > MaxNumbers {
		return ui.Contact{}, ErrMergeFull
	}
	merged.Numbers, merged.Number, merged.Notes = nums, "", strings.Join(notes, "\n")
	in := s.groupsOf()
	var groups []string
	for _, c := range list[1:] {
		for _, g := range in[c.ID] {
			if !slices.Contains(in[merged.ID], g) && !slices.Contains(groups, g) {
				groups = append(groups, g)
			}
		}
	}
	if len(groups) > 0 {
		err := s.changeGroups(func(gl []ui.ContactGroup) []ui.ContactGroup {
			for i := range gl {
				if slices.Contains(groups, gl[i].ID) {
					gl[i].Members = append(gl[i].Members, merged.ID)
				}
			}
			return gl
		})
		if err != nil {
			return ui.Contact{}, err
		}
	}
	var saved ui.Contact
	var err error
	berr := s.Batch(func() {
		if saved, err = s.Save(merged); err != nil {
			return
		}
		for _, c := range list[1:] {
			if err = s.Delete(c.ID); err != nil {
				return
			}
		}
	})
	if err == nil {
		err = berr
	}
	if err != nil {
		return ui.Contact{}, err
	}
	saved.Groups = s.groupsOf()[saved.ID]
	return saved, nil
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
