package carddav

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/carddav/davtest"
	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/ui"
)

// DiscoverAll finds every book of the home: those the user may write to
// first (the read-only system book comes first by path), then that one.
func TestDiscoverAll(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) { s.AddBook("work", "Work") })
	books, err := e.client.DiscoverAll(context.Background(), e.ts.URL)
	if err != nil || len(books) != 3 || books[0].URL != e.ts.URL+davtest.Book || books[1].URL != e.ts.URL+davtest.BookPath("work") || books[1].Name != "Work" ||
		books[0].ReadOnly || books[1].ReadOnly || !books[2].ReadOnly || books[2].URL != e.ts.URL+davtest.BookPath(davtest.SystemBook) {
		t.Fatalf("%+v %v", books, err)
	}
	one, err := e.client.DiscoverAll(context.Background(), e.ts.URL+davtest.BookPath("work"))
	if err != nil || len(one) != 1 || one[0].Name != "Work" {
		t.Errorf("one book typed: %+v %v", one, err)
	}
}

// Two books on the phone at once: each sync has the contacts of its book,
// takes the server's into it, and sends only its own.
func TestSeveralBooks(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) {
		s.AddBook("work", "Work")
		s.Put("ann.vcf", card3("ann", "Ann", "TEL:1001"))
		s.PutIn("work", "boss.vcf", card3("boss", "Boss", "TEL:2001"))
	})
	books, _ := e.client.DiscoverAll(context.Background(), e.ts.URL)
	n := 0
	sync := func(b Book, key string) *Sync {
		return &Sync{Client: e.client, Book: b, Key: key, Local: StoreLocal(e.store, nil), State: &State{},
			NewUID: func() string { n++; return "kario-" + string(rune('a'+n)) }, Now: func() time.Time { return time.Unix(0, 0) }}
	}
	home, work := sync(books[0], "a1/b1"), sync(books[1], "a1/b2")
	e.store.NewBook = "a1/b2"
	e.add(ui.Contact{Name: "Colleague", Number: "2002"})
	e.add(ui.Contact{Name: "Only here", Number: "3003", Book: ui.BookPhone})
	for _, s := range []*Sync{home, work} {
		if _, err := s.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for name, book := range map[string]string{"Ann": "a1/b1", "Boss": "a1/b2", "Colleague": "a1/b2", "Only here": ""} {
		if c, ok := e.byName(name); !ok || c.Book != book {
			t.Errorf("%s: %+v", name, c)
		}
	}
	if len(e.srv.Cards()) != 1 || len(e.srv.CardsIn("work")) != 2 {
		t.Errorf("%d cards, %d in work", len(e.srv.Cards()), len(e.srv.CardsIn("work")))
	}
	if _, ok := e.srv.FindIn("work", "Only here"); ok {
		t.Error("a contact on the phone alone went up")
	}
	for _, s := range []*Sync{home, work} {
		if res, _ := s.Run(context.Background()); res != (Result{}) {
			t.Errorf("again: %+v", res)
		}
	}
}

// A book's first sync links the server's card to the same person on the
// phone alone, who joins the book; the others on the phone alone stay so.
func TestFirstSyncTakesThePhonesOwn(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) { s.Put("ann.vcf", card3("ann", "Ann", "TEL:1001")) })
	e.add(ui.Contact{Name: "ann", Number: "1001"})
	e.add(ui.Contact{Name: "Bob", Number: "1002"})
	e.start()
	e.sync.Key = "a2/b1"
	e.run()
	ann, _ := e.byName("Ann")
	bob, _ := e.byName("Bob")
	if ann.Book != "a2/b1" || bob.Book != "" || len(e.store.List()) != 2 || len(e.srv.Cards()) != 1 {
		t.Errorf("%+v %+v, %d cards", ann, bob, len(e.srv.Cards()))
	}
}

// A state from before the phone showed e-mail and photos reads every card
// again: a change on the phone then keeps the card's e-mail address.
func TestOldStateReadsAgain(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) { s.Put("ann.vcf", card3("ann", "Ann", "TEL:1001", "EMAIL:ann@x.test")) })
	ann := e.add(ui.Contact{Name: "Ann", Number: "1001"})
	e.start()
	base := shown(ann)
	base.Emails = nil
	e.sync.State = &State{Book: e.sync.Book.URL, Synced: true, Token: "http://davtest.invalid/sync/1/1",
		Items: []*Item{{Href: davtest.Book + "ann.vcf", ETag: "old", UID: "ann", ID: ann.ID, Base: base, Remote: true}}}
	gets := e.srv.Count("REPORT multiget")
	e.run()
	if e.srv.Count("REPORT multiget") != gets+1 || e.sync.State.Version != stateVersion {
		t.Fatalf("not read again: %d multigets", e.srv.Count("REPORT multiget")-gets)
	}
	ann, _ = e.byName("Ann")
	if len(ann.Emails) != 1 {
		t.Fatalf("%+v", ann)
	}
	ann.Notes = "x"
	e.store.Save(ann)
	e.run()
	if card := string(e.srv.Card("ann.vcf")); !strings.Contains(card, "EMAIL:ann@x.test") || !strings.Contains(card, "NOTE:x") {
		t.Errorf("%s", card)
	}
}

// A read-only book comes to the phone and goes on coming as the server
// has it; the phone writes nothing to it: a contact changed or deleted on
// the phone gets the server's card back, the server's changes and
// deletions win, its group cards are the server's.
func TestReadOnlyBook(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) {
		s.AddReadOnlyBook("shared", "Shared")
		s.PutIn("shared", "ann.vcf", card3("ann", "Ann", "TEL:1001", "BDAY:1990-01-02"))
		s.PutIn("shared", "bob.vcf", card3("bob", "Bob", "TEL:1002"))
		s.PutIn("shared", "team.vcf", card3("team", "Team", "X-ADDRESSBOOKSERVER-KIND:group", "X-ADDRESSBOOKSERVER-MEMBER:urn:uuid:ann"))
	})
	books, _ := e.client.DiscoverAll(context.Background(), e.ts.URL)
	var shared Book
	for _, b := range books {
		if b.Name == "Shared" {
			shared = b
		}
	}
	if !shared.ReadOnly {
		t.Fatalf("%+v", books)
	}
	e.add(ui.Contact{Name: "Ann", Number: "1001", Book: ui.BookPhone})
	s := &Sync{Client: e.client, Book: shared, Key: "a1/b3", Local: StoreLocal(e.store, nil), State: &State{}}
	run := func() Result {
		t.Helper()
		res, err := s.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	run()
	if e.names() != "Ann,Ann,Bob" {
		t.Fatalf("the phone's own Ann is not linked into a read-only book: %s", e.names())
	}
	var ann ui.Contact
	for _, c := range e.store.List() {
		if c.Name == "Ann" && c.Book == "a1/b3" {
			ann = c
		}
	}
	if ann.Birthday != "1990-01-02" || len(e.store.GroupNames(ann.Groups)) != 1 {
		t.Fatalf("%+v", ann)
	}
	ann.Notes, ann.Groups = "mine", nil
	e.store.Save(ann)
	e.store.SetGroupsOf(ann.ID, nil)
	bob, _ := e.byName("Bob")
	e.store.Delete(bob.ID)
	res := run()
	if got, _ := e.store.Get(ann.ID); got.Notes != "" || len(e.store.GroupNames(got.Groups)) != 1 {
		t.Errorf("the phone's change stayed: %+v", got)
	}
	if _, ok := e.byName("Bob"); !ok || res.Uploaded != 0 || res.Removed != 0 {
		t.Errorf("Bob did not come back, or something was written: %+v", res)
	}
	if n := e.srv.Count("PUT") + e.srv.Count("DELETE"); n != 0 {
		t.Errorf("%d writes to a read-only book", n)
	}
	e.srv.PutIn("shared", "ann.vcf", card3("ann", "Ann", "TEL:1001", "BDAY:1990-01-03"))
	e.srv.DeleteIn("shared", "bob.vcf")
	run()
	if got, _ := e.store.Get(ann.ID); got.Birthday != "1990-01-03" {
		t.Errorf("%+v", got)
	}
	if _, ok := e.byName("Bob"); ok {
		t.Error("Bob deleted on the server stayed")
	}
	if res := run(); res != (Result{}) {
		t.Errorf("again: %+v", res)
	}
}

// A contact moved to another book goes from the old book's server (its
// group card too) and comes as a new card in the new one, with its photo
// and its groups; moved to the phone alone it only goes.
func TestMoveBetweenBooks(t *testing.T) {
	jpg := picture(t, 60)
	e := newEnv(t, func(s *davtest.Server) {
		s.AddBook("work", "Work")
		s.Put("ann.vcf", card3("ann", "Ann", "TEL:1001", photoLine(jpg)))
		s.Put("fam.vcf", card3("fam", "Family", "X-ADDRESSBOOKSERVER-KIND:group", "X-ADDRESSBOOKSERVER-MEMBER:urn:uuid:ann"))
		s.Put("bob.vcf", card3("bob", "Bob", "TEL:1002"))
	})
	books, _ := e.client.DiscoverAll(context.Background(), e.ts.URL)
	n := 0
	sync := func(b Book, key string) *Sync {
		return &Sync{Client: e.client, Book: b, Key: key, Local: StoreLocal(e.store, nil), State: &State{},
			NewUID: func() string { n++; return fmt.Sprintf("kario-%d", n) }}
	}
	home, work := sync(books[0], "a1/b1"), sync(books[1], "a1/b2")
	runAll := func() {
		t.Helper()
		for _, s := range []*Sync{home, work} {
			if _, err := s.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	}
	runAll()
	ann, _ := e.byName("Ann")
	if ann.Photo != contacts.PhotoKey(jpg) || len(ann.Groups) != 1 {
		t.Fatalf("%+v", ann)
	}
	ann.Book = "a1/b2"
	e.store.Save(ann)
	runAll()
	if e.srv.Card("ann.vcf") != nil {
		t.Errorf("the old card stayed:\n%s", e.srv.Card("ann.vcf"))
	}
	if strings.Contains(string(e.srv.Card("fam.vcf")), "urn:uuid:ann") {
		t.Errorf("the old book's group card still has it:\n%s", e.srv.Card("fam.vcf"))
	}
	name, ok := e.srv.FindIn("work", "FN:Ann")
	card := string(e.srv.CardIn("work", name))
	if !ok || !strings.Contains(card, photoLine(jpg)) || !strings.Contains(card, "CATEGORIES:Family\r\n") {
		t.Fatalf("not in the new book with its photo and group:\n%s", card)
	}
	if got, _ := e.store.Get(ann.ID); got.Book != "a1/b2" || got.Photo != ann.Photo || len(got.Groups) != 1 {
		t.Errorf("the phone's contact changed: %+v", got)
	}
	bob, _ := e.byName("Bob")
	bob.Book = ""
	e.store.Save(bob)
	bob, _ = e.store.Get(bob.ID)
	if bob.Book != "a1/b1" {
		t.Fatalf("Save keeps the book of an empty Book: %+v", bob)
	}
	bob.Book = ui.BookPhone
	e.store.Save(bob)
	runAll()
	if _, ok := e.srv.Find("FN:Bob"); ok {
		t.Error("moved to the phone alone, its card stayed")
	}
	if _, ok := e.byName("Bob"); !ok {
		t.Error("the contact went too")
	}
}

// A book that became read-only with a card the phone was still making:
// the contact stays on the phone, nothing is written.
func TestReadOnlyKeepsThePhonesNew(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) { s.AddReadOnlyBook("shared", "Shared") })
	books, _ := e.client.DiscoverAll(context.Background(), e.ts.URL)
	mine := e.add(ui.Contact{Name: "Mine", Number: "1003", Book: "a1/b3"})
	s := &Sync{Client: e.client, Book: books[2], Key: "a1/b3", Local: StoreLocal(e.store, nil),
		State: &State{Book: books[2].URL, Version: stateVersion, Synced: true, Items: []*Item{{Href: davtest.BookPath("shared") + "x.vcf", UID: "x", ID: mine.ID, Base: shown(mine), New: true}}}}
	if _, err := s.Run(context.Background()); err != nil || books[2].Name != "Shared" {
		t.Fatal(err, books[2].Name)
	}
	if _, ok := e.store.Get(mine.ID); !ok || len(s.State.Linked()) != 0 || e.srv.Count("PUT") != 0 {
		t.Errorf("the phone's contact: %v, %d linked, %d PUTs", ok, len(s.State.Linked()), e.srv.Count("PUT"))
	}
}
