package carddav

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/kario-os/kario/carddav/davtest"
	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/ui"
)

// members are the names of a group's members on the phone, sorted.
func (e *env) members(group string) string {
	g, ok := e.store.GroupNamed(group)
	if !ok {
		return "none"
	}
	var out []string
	for _, id := range g.Members {
		if c, ok := e.store.Get(id); ok {
			out = append(out, c.Name)
		}
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}

func (e *env) join(group, name string, in bool) {
	e.t.Helper()
	g, ok := e.store.GroupNamed(group)
	if !ok {
		var err error
		if g, err = e.store.AddGroup(group); err != nil {
			e.t.Fatal(err)
		}
	}
	c, _ := e.byName(name)
	if _, err := e.store.SetMembers(g.ID, []string{c.ID}, in); err != nil {
		e.t.Fatal(err)
	}
}

func cats(t *testing.T, raw []byte) []string {
	t.Helper()
	c, _, err := contacts.ParseCard(raw)
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	return c.Groups
}

// Nextcloud's and Google's groups: each card's CATEGORIES. They come to
// the phone as its groups, and a change on either side goes to the other,
// merged per contact.
func TestGroupsCategories(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) {
		s.Put("ann.vcf", card3("ann", "Ann", "TEL:1001", "CATEGORIES:Family,Friends"))
		s.Put("bob.vcf", card3("bob", "Bob", "TEL:1002"))
	})
	e.start()
	e.run()
	if e.sync.State.Groups != GroupsCategories || e.members("Family") != "Ann" || e.members("Friends") != "Ann" {
		t.Fatalf("%s: Family %s, Friends %s", e.sync.State.Groups, e.members("Family"), e.members("Friends"))
	}
	if res := e.run(); res != (Result{}) || e.srv.Count("PUT") != 0 {
		t.Errorf("again: %+v, %d PUTs", res, e.srv.Count("PUT"))
	}
	e.join("Family", "Bob", true)
	e.join("Friends", "Ann", false)
	e.add(ui.Contact{Name: "Cat", Number: "1003"})
	e.join("Family", "Cat", true)
	e.run()
	if got := cats(t, e.srv.Card("bob.vcf")); !slices.Equal(got, []string{"Family"}) {
		t.Errorf("Bob's card: %q", got)
	}
	if got := cats(t, e.srv.Card("ann.vcf")); !slices.Equal(got, []string{"Family"}) {
		t.Errorf("Ann's card: %q", got)
	}
	name, _ := e.srv.Find("FN:Cat")
	if got := cats(t, e.srv.Card(name)); !slices.Equal(got, []string{"Family"}) {
		t.Errorf("Cat's new card: %q\n%s", got, e.srv.Card(name))
	}
	puts := e.srv.Count("PUT")
	if res := e.run(); res != (Result{}) || e.srv.Count("PUT") != puts {
		t.Errorf("again: %+v", res)
	}
	// Both sides at once: the server adds Bob to Work, the phone takes
	// him out of Family.
	e.srv.Put("bob.vcf", card3("bob", "Bob", "TEL:1002", "CATEGORIES:Family,Work"))
	e.join("Family", "Bob", false)
	e.run()
	if got := cats(t, e.srv.Card("bob.vcf")); !slices.Equal(got, []string{"Work"}) || e.members("Work") != "Bob" || e.members("Family") != "Ann,Cat" {
		t.Errorf("Bob's card %q; Work %s, Family %s", got, e.members("Work"), e.members("Family"))
	}
}

// iCloud's groups: a card for each group, its members by UID. They come to
// the phone as its groups; members, names and deletes go both ways, in
// group cards (the contacts' own cards are not touched for them).
func TestGroupsVCard(t *testing.T) {
	family := "BEGIN:VCARD\r\nVERSION:3.0\r\nPRODID:-//Apple Inc.//iOS 18//EN\r\nN:Family\r\nFN:Family\r\nX-ADDRESSBOOKSERVER-KIND:group\r\n" +
		"X-ADDRESSBOOKSERVER-MEMBER:urn:uuid:ann\r\nX-ADDRESSBOOKSERVER-MEMBER:urn:uuid:not-here\r\nUID:g-family\r\nEND:VCARD\r\n"
	e := newEnv(t, func(s *davtest.Server) {
		s.Put("ann.vcf", card3("ann", "Ann", "TEL:1001"))
		s.Put("bob.vcf", card3("bob", "Bob", "TEL:1002", "CATEGORIES:Ignored"))
		s.Put("g-family.vcf", []byte(family))
	})
	e.start()
	e.run()
	if e.sync.State.Groups != GroupsVCard || e.members("Family") != "Ann" || e.members("Ignored") != "none" {
		t.Fatalf("%s: Family %s, Ignored %s", e.sync.State.Groups, e.members("Family"), e.members("Ignored"))
	}
	bob := string(e.srv.Card("bob.vcf"))
	if res := e.run(); res != (Result{}) || e.srv.Count("PUT") != 0 {
		t.Errorf("again: %+v, %d PUTs", res, e.srv.Count("PUT"))
	}
	e.join("Family", "Bob", true)
	e.run()
	g := string(e.srv.Card("g-family.vcf"))
	for _, want := range []string{"X-ADDRESSBOOKSERVER-MEMBER:urn:uuid:ann\r\n", "X-ADDRESSBOOKSERVER-MEMBER:urn:uuid:bob\r\n",
		"X-ADDRESSBOOKSERVER-MEMBER:urn:uuid:not-here\r\n", "PRODID:-//Apple Inc.//iOS 18//EN\r\n"} {
		if !strings.Contains(g, want) {
			t.Errorf("no %q in\n%s", want, g)
		}
	}
	if string(e.srv.Card("bob.vcf")) != bob {
		t.Errorf("Bob's own card was written:\n%s", e.srv.Card("bob.vcf"))
	}

	// Renamed on the phone, then on the server.
	fam, _ := e.store.GroupNamed("Family")
	e.store.RenameGroup(fam.ID, "Family & co")
	e.run()
	if c, _, _ := contacts.ParseCard(e.srv.Card("g-family.vcf")); c.Name != "Family & co" {
		t.Errorf("renamed on the phone: %s", e.srv.Card("g-family.vcf"))
	}
	e.srv.Put("g-family.vcf", []byte(strings.ReplaceAll(string(e.srv.Card("g-family.vcf")), "Family & co", "Kin")))
	e.run()
	if e.members("Kin") != "Ann,Bob" || e.members("Family & co") != "none" {
		t.Errorf("renamed on the server: Kin %s", e.members("Kin"))
	}

	// A group new on the phone gets a card once it has a member; a new
	// contact joins it there.
	e.add(ui.Contact{Name: "Cat", Number: "1003"})
	e.join("Work", "Cat", true)
	e.join("Work", "Ann", true)
	e.run()
	name, ok := e.srv.Find("FN:Work")
	if !ok {
		t.Fatalf("no card for the new group: %v", e.srv.Cards())
	}
	w, info, _ := contacts.ParseCard(e.srv.Card(name))
	catName, _ := e.srv.Find("FN:Cat")
	cat, catInfo, _ := contacts.ParseCard(e.srv.Card(catName))
	if !info.Group || w.Name != "Work" || len(info.Members) != 2 || !slices.Contains(info.Members, contacts.UIDKey(catInfo.UID)) || len(cat.Groups) != 0 {
		t.Errorf("%+v\n%s\n%s", info, e.srv.Card(name), e.srv.Card(catName))
	}
	if !strings.Contains(string(e.srv.Card(name)), "X-ADDRESSBOOKSERVER-KIND:group") {
		t.Errorf("a 3.0 book gets Apple's group card:\n%s", e.srv.Card(name))
	}

	// Deleted on the phone: its card goes; deleted on the server: the
	// phone's group loses its members and stays.
	wg, _ := e.store.GroupNamed("Work")
	e.store.DeleteGroup(wg.ID)
	e.srv.Delete("g-family.vcf")
	e.run()
	if _, ok := e.srv.Find("FN:Work"); ok {
		t.Error("the group deleted on the phone is still on the server")
	}
	if e.members("Kin") != "" {
		t.Errorf("Kin %s", e.members("Kin"))
	}
	if res := e.run(); res != (Result{}) {
		t.Errorf("again: %+v", res)
	}
}

// A 4.0 book gets 4.0 group cards; an empty book on a server that is not
// iCloud keeps the phone's groups in CATEGORIES.
func TestGroupStyleDefaults(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) { s.Version = "4.0" })
	e.add(ui.Contact{Name: "Ann", Number: "1001"})
	e.join("Family", "Ann", true)
	e.start()
	e.run()
	name, _ := e.srv.Find("FN:Ann")
	if got := cats(t, e.srv.Card(name)); e.sync.State.Groups != GroupsCategories || !slices.Equal(got, []string{"Family"}) {
		t.Errorf("%s %q", e.sync.State.Groups, got)
	}

	v := newEnv(t, func(s *davtest.Server) {
		s.Version = "4.0"
		s.Put("g.vcf", []byte("BEGIN:VCARD\nVERSION:4.0\nKIND:group\nFN:Team\nUID:g\nEND:VCARD\n"))
	})
	v.add(ui.Contact{Name: "Ann", Number: "1001"})
	v.join("Team", "Ann", true)
	v.start()
	v.run()
	g := string(v.srv.Card("g.vcf"))
	if !strings.Contains(g, "MEMBER:urn:uuid:kario-0001\n") || strings.Contains(g, "X-ADDRESSBOOKSERVER") {
		t.Errorf("%s", g)
	}
	icloud := &Sync{State: &State{}}
	icloud.book, _ = url.Parse("https://p42-contacts.icloud.com/123/carddavhome/card/")
	if icloud.groupStyle() != GroupsVCard {
		t.Error("iCloud without groups yet")
	}
	kario := &Sync{State: &State{}, Groups: GroupsVCard, book: v.sync.book}
	if kario.groupStyle() != GroupsVCard {
		t.Error("the default the Shell gives")
	}
}
