package contacts

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kario-os/kario/ui"
)

// Groups are made, renamed, filled and emptied, kept across a restart;
// a contact a sync rewrites stays in its groups; a deleted one leaves them.
func TestGroups(t *testing.T) {
	s, path := open(t)
	ann, _ := s.Save(ui.Contact{Name: "Ann", Number: "13800000001"})
	bob, _ := s.Save(ui.Contact{Name: "Bob", Number: "13800000002"})
	fam, err := s.AddGroup("  Family ")
	if err != nil || fam.Name != "Family" || fam.ID == "" {
		t.Fatalf("group: %+v %v", fam, err)
	}
	if _, err := s.AddGroup("FAMILY"); !errors.Is(err, ErrGroupExists) {
		t.Fatalf("the same name twice: %v", err)
	}
	if _, err := s.AddGroup(" \n"); !errors.Is(err, ErrGroupName) {
		t.Fatalf("no name: %v", err)
	}
	work, _ := s.AddGroup("Work")
	if g, err := s.SetMembers(fam.ID, []string{ann.ID, bob.ID, "c999"}, true); err != nil || !slices.Equal(g.Members, []string{ann.ID, bob.ID}) {
		t.Fatalf("members: %+v %v", g, err)
	}
	_, _ = s.SetMembers(work.ID, []string{bob.ID}, true)
	if g, _ := s.RenameGroup(work.ID, "Office"); g.Name != "Office" {
		t.Fatalf("renamed: %+v", g)
	}
	if _, err := s.RenameGroup(work.ID, "family"); !errors.Is(err, ErrGroupExists) {
		t.Fatalf("renamed to another's name: %v", err)
	}
	if st, err := os.Stat(filepath.Join(filepath.Dir(path), "contact_groups.json")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the groups file: %v %v", st, err)
	}
	s = Open(path, spell)
	if c, _ := s.Get(bob.ID); !slices.Equal(c.Groups, []string{fam.ID, work.ID}) {
		t.Fatalf("Bob's groups after a restart: %v", c.Groups)
	}
	// A sync writes the contact anew, without groups.
	if _, err := s.Save(ui.Contact{ID: bob.ID, Name: "Robert", Number: "13800000002"}); err != nil {
		t.Fatal(err)
	}
	if l := s.List(); l[1].Name != "Robert" || len(l[1].Groups) != 2 {
		t.Fatalf("after a rewrite: %+v", l)
	}
	if strings.Contains(readFile(t, path), "groups") {
		t.Fatal("groups written into the contacts file")
	}
	_ = s.Delete(ann.ID)
	if g := s.Groups(); len(g) != 2 || !slices.Equal(g[0].Members, []string{bob.ID}) {
		t.Fatalf("after Ann went: %+v", g)
	}
	if g, _ := s.SetMembers(fam.ID, []string{bob.ID}, false); len(g.Members) != 0 {
		t.Fatalf("taken out: %+v", g)
	}
	if err := s.DeleteGroup(work.ID); err != nil {
		t.Fatal(err)
	}
	s = Open(path, spell)
	if g := s.Groups(); len(g) != 1 || g[0].ID != fam.ID {
		t.Fatalf("after deleting a group: %+v", g)
	}
	if c, ok := s.Get(bob.ID); !ok || len(c.Groups) != 0 {
		t.Fatalf("the contacts stay: %+v", c)
	}
	if g, _ := s.AddGroup("New"); g.ID == work.ID || g.ID == fam.ID {
		t.Fatalf("an ID used again: %s", g.ID)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Merging keeps the first contact with every number, the notes and the
// groups of the others, which go; it takes the name of another when the
// first has only its number, and refuses more numbers than a contact
// holds.
func TestMerge(t *testing.T) {
	s, path := open(t)
	a, _ := s.Save(ui.Contact{Number: "13800000001", Emails: []ui.ContactEmail{{Address: "ann@x.test"}}})
	b, _ := s.Save(ui.Contact{Name: "Ann", Numbers: []ui.ContactNumber{{Number: "+86 138 0000 0001"}, {Number: "010 6552 9988", Label: ui.NumberWork}}, Notes: "school",
		Emails: []ui.ContactEmail{{Address: "ANN@x.test"}, {Address: "ann@work.test"}}, PhotoData: picture(t, 3)})
	c, _ := s.Save(ui.Contact{Name: "ann", Number: "13900000003", Notes: "school", Addresses: []ui.ContactAddress{{City: "Town"}}, PhotoData: picture(t, 4)})
	g, _ := s.AddGroup("Friends")
	_, _ = s.SetMembers(g.ID, []string{c.ID}, true)
	if _, err := s.Merge([]string{a.ID, a.ID}); !errors.Is(err, ErrMergeTwo) {
		t.Fatalf("one contact: %v", err)
	}
	got, err := s.Merge([]string{a.ID, b.ID, c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID || got.Name != "Ann" || nums(got) != "mobile=13800000001,work=010 6552 9988,mobile=13900000003" || got.Notes != "school" || !slices.Equal(got.Groups, []string{g.ID}) {
		t.Fatalf("merged: %+v", got)
	}
	if len(got.Emails) != 2 || got.Emails[1].Address != "ann@work.test" || len(got.Addresses) != 1 || got.Photo != b.Photo {
		t.Fatalf("merged: %+v", got)
	}
	s = Open(path, spell)
	if l := s.List(); len(l) != 1 || l[0].ID != a.ID || len(l[0].Groups) != 1 {
		t.Fatalf("after a restart: %+v", l)
	}
	var many []ui.ContactNumber
	for i := range MaxNumbers {
		many = append(many, ui.ContactNumber{Number: "1390000000" + string(rune('0'+i))})
	}
	d, _ := s.Save(ui.Contact{Name: "Many", Numbers: many})
	if _, err := s.Merge([]string{d.ID, a.ID}); !errors.Is(err, ErrMergeFull) {
		t.Fatalf("too many numbers: %v", err)
	}
	if s.Len() != 2 {
		t.Fatal("a refused merge changed the contacts")
	}
}
