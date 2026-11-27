package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ui"
)

// The duplicates page offers each set to merge under the name merging
// keeps; a group's page lists its members alone, each to take out.
func TestDuplicatesAndGroupPages(t *testing.T) {
	t.Setenv("PHONE_REGION", "CN")
	b := &book{list: []ui.Contact{
		{ID: "c1", Name: "13800000001", Number: "13800000001"},
		{ID: "c2", Name: "Ann", Number: "+86 138 0000 0001"},
		{ID: "c3", Name: "Bob", Number: "13900000002", Groups: []string{"g1"}},
	}, groups: []ui.ContactGroup{{ID: "g1", Name: "Family", Members: []string{"c3"}}}}
	s := b.dupsScreen()
	if len(s.Body) != 2 || s.Body[0].ID != "d:c1,c2" || !strings.Contains(s.Body[0].Actions[0].Confirm, "Ann") {
		t.Fatalf("duplicates %+v", s.Body)
	}
	g := b.groupScreen(b.groups[0])
	var ids []string
	for _, n := range g.Body {
		if strings.HasPrefix(n.ID, "c:") {
			ids = append(ids, n.ID)
			if a := n.Actions[len(n.Actions)-1]; a.ID != "group-out" {
				t.Errorf("no way out of the group: %+v", n.Actions)
			}
		}
	}
	if strings.Join(ids, ",") != "c:c3" {
		t.Fatalf("group members %v", ids)
	}
	if add := b.groupAddScreen(b.groups[0]); len(add.Body) != 3 {
		t.Fatalf("to add %+v", add.Body)
	}
	b.addQuery = "ann"
	if add := b.groupAddScreen(b.groups[0]); len(add.Body) != 2 || add.Body[1].ID != "c:c2" {
		t.Fatalf("searched %+v", add.Body)
	}
	if v := b.viewScreen(b.list[2]); v.Body[len(v.Body)-1].Text != "Family" {
		t.Fatalf("the contact's groups %+v", v.Body)
	}
}

// The list shows a contact's photo in its icon's place and finds it by
// e-mail; its page shows the photo, e-mail and postal addresses and its
// book; the editor takes them, a new contact's book among them.
func TestContactFields(t *testing.T) {
	defer i18n.SetLang("en")
	i18n.SetLang("en")
	ann := ui.Contact{ID: "c1", Name: "Ann", Number: "1001", Numbers: []ui.ContactNumber{{Number: "1001", Label: ui.NumberMobile}},
		Emails:    []ui.ContactEmail{{Address: "ann@work.test", Label: ui.NumberWork}},
		Addresses: []ui.ContactAddress{{Label: ui.NumberHome, Street: "1 Main St", City: "Town", POBox: "PO 7"}},
		Photo:     "0123456789abcdef", Book: "a1/b1"}
	b := &book{list: []ui.Contact{ann, {ID: "c2", Name: "Bob", Number: "1002"}},
		books: []ui.ContactBook{{ID: "a1/b1", Name: "Contacts", Account: "Kario account"}, {ID: "a2/b1", Name: "Work", Account: "dav.example.org", Default: true}}}
	b.query = "work.test"
	l := b.listScreen()
	var hits []ui.Node
	for _, n := range l.Body {
		if strings.HasPrefix(n.ID, "c:") {
			hits = append(hits, n)
		}
	}
	if len(hits) != 1 || hits[0].Src != ui.ContactPhotoPrefix+ann.Photo || hits[0].Icon != "A" {
		t.Fatalf("search by e-mail: %+v", hits)
	}
	v := b.viewScreen(ann)
	var got []string
	for _, n := range v.Body {
		got = append(got, n.Kind+":"+n.Title+n.Text+n.Src)
	}
	all := strings.Join(got, "|")
	for _, want := range []string{"image:contact:0123456789abcdef", "item:ann@work.test", "item:PO 7, 1 Main St, Town", "In: Contacts · Kario account"} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in %s", want, all)
		}
	}
	b.edit = newEditor(ann)
	e := b.editScreen()
	ids := map[string]ui.Node{}
	for _, n := range e.Body {
		ids[n.ID] = n
	}
	if ids["mail:0"].Value != "ann@work.test" || ids["mail:1"].Value != "" || ids["adr:0"].Title != "PO 7, 1 Main St, Town" ||
		ids["photo"].Src != ui.ContactPhotoPrefix+ann.Photo {
		t.Fatalf("%+v", ids)
	}
	if _, ok := ids["book"]; ok {
		t.Error("a contact's book is chosen when it is made only")
	}
	b.edit.Address = 0
	if !b.saveAddress(map[string]string{"street": "2 New Rd", "city": "City"}, ui.NumberWork) {
		t.Fatal("not taken")
	}
	got1 := b.edited(map[string]string{"name": "Ann", "num:0": "1001", "mail:0": "ann@work.test", "mail:1": "ann@home.test"})
	if len(got1.Emails) != 2 || got1.Emails[1].Label != ui.NumberHome || len(got1.Addresses) != 1 ||
		got1.Addresses[0] != (ui.ContactAddress{Label: ui.NumberWork, POBox: "PO 7", Street: "2 New Rd", City: "City"}) || got1.Photo != ann.Photo {
		t.Fatalf("%+v", got1)
	}
	b.edit = newEditor(ui.Contact{})
	n := ui.Node{}
	for _, x := range b.editScreen().Body {
		if x.ID == "book" {
			n = x
		}
	}
	if n.Subtitle != "Work · dav.example.org" || len(n.Actions) != 4 || !strings.HasSuffix(n.Actions[2].Label, " ✓") {
		t.Fatalf("a new contact's book: %+v", n)
	}
}

// The page shows the nickname, title and organization, the dates and web
// addresses; the editor has them, a date without its year in this year's
// date field; a read-only contact's page offers only a copy, which the
// editor saves as a new contact, and moves go to another writable book.
func TestOtherFieldsReadOnlyMove(t *testing.T) {
	defer i18n.SetLang("en")
	i18n.SetLang("en")
	ann := ui.Contact{ID: "c1", Name: "Ann", Number: "1001", Nickname: "Annie", Org: "Acme", Title: "CTO", Birthday: "--04-12",
		Anniversary: "2000-06-12", URLs: []string{"https://ann.example"}, Book: "a1/b1"}
	boss := ui.Contact{ID: "c2", Name: "Boss", Number: "1001", Book: "a2/b1", ReadOnly: true}
	b := &book{list: []ui.Contact{ann, boss, {ID: "c3", Name: "Ann", Number: "1002"}},
		books: []ui.ContactBook{{ID: "a1/b1", Name: "Contacts", Account: "Kario account", Default: true},
			{ID: "a2/b1", Name: "System", Account: "dav.example.org", ReadOnly: true}}}
	v := b.viewScreen(ann)
	var got []string
	for _, n := range v.Body {
		got = append(got, n.ID+":"+n.Title+n.Text+"/"+n.Subtitle)
	}
	all := strings.Join(got, "|")
	for _, want := range []string{"Nickname: Annie", ":CTO · Acme/", "bd:04-12/Birthday", "an:2000-06-12/Anniversary · ", "u:0:https://ann.example/"} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in %s", want, all)
		}
	}
	if mv := b.moveScreen(ann); len(mv.Body) != 2 || mv.Body[0].ID != "to:"+ui.BookPhone {
		t.Errorf("only the phone: not the read-only book: %+v", mv.Body)
	}
	b.edit = newEditor(ann)
	ids := map[string]ui.Node{}
	for _, n := range b.editScreen().Body {
		ids[n.ID] = n
	}
	year := time.Now().Year()
	if !b.edit.BirthNoYear || ids["birthday"].Value != fmt.Sprintf("%d-04-12", year) || ids["anniversary"].Value != "2000-06-12" ||
		ids["nick"].Value != "Annie" || ids["url:0"].Value != "https://ann.example" || ids["url:1"].Value != "" ||
		!strings.HasSuffix(ids["birthday"].Actions[0].Label, "✓") {
		t.Fatalf("%+v %+v", b.edit, ids["birthday"])
	}
	c := b.edited(map[string]string{"name": "Ann", "birthday": fmt.Sprintf("%d-05-01", year), "anniversary": "", "nick": "A",
		"org": "New", "jobtitle": "", "url:0": "https://ann.example", "url:1": "b.example"})
	if c.Birthday != "--05-01" || c.Anniversary != "" || c.Nickname != "A" || c.Org != "New" || strings.Join(c.URLs, " ") != "https://ann.example b.example" {
		t.Errorf("%+v", c)
	}
	if v, noYear := fieldDate("--02-29", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)); v != "2028-02-29" || !noYear {
		t.Errorf("29 February without a year: %q", v)
	}

	if acts := contactActions(boss); acts[len(acts)-1].ID != "edit" || acts[len(acts)-1].Label != "Edit a copy" {
		t.Errorf("%+v", acts)
	}
	rv := b.viewScreen(boss)
	if len(rv.Actions) != 3 || rv.Actions[2].Label != "Edit a copy" || !strings.Contains(rv.Body[len(rv.Body)-1].Text, "Read-only") {
		t.Errorf("%+v %+v", rv.Actions, rv.Body)
	}
	b.edit = newEditor(boss)
	if b.edit.ID != "" || b.edit.CopyOf != "c2" || b.editScreen().Title != "Copy of a contact" {
		t.Errorf("%+v", b.edit)
	}
	if d := b.dupsScreen(); len(d.Body) != 2 || d.Body[0].ID != "d:c1,c3" {
		t.Errorf("a read-only contact among duplicates: %+v", d.Body)
	}
}
