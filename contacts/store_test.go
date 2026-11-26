package contacts

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// spell is a stand-in for ime.Pinyin: the name's words, lower case.
func spell(name string) string { return strings.ToLower(name) }

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shell", "contacts.json")
	return Open(path, spell), path
}

func TestStoreSaveChangeDelete(t *testing.T) {
	s, path := open(t)
	if !s.Created || s.Len() != 0 {
		t.Fatalf("a new store: created %v, %d contacts", s.Created, s.Len())
	}
	bob, err := s.Save(ui.Contact{Name: " Bob\tSmith ", Numbers: []ui.ContactNumber{
		{Number: "+86 138-0000-0009"}, {Number: "13800000009", Label: ui.NumberWork}, {Number: "abc"}, {Number: "010 1234", Label: "fax"}}})
	if err != nil {
		t.Fatal(err)
	}
	if bob.ID == "" || bob.Name != "Bob Smith" || bob.Number != "+86 138-0000-0009" || nums(bob) != "mobile=+86 138-0000-0009,other=010 1234" || bob.Pinyin != "bob smith" {
		t.Fatalf("saved: %+v", bob)
	}
	ann, _ := s.Save(ui.Contact{Name: "Ann", Number: "10086"})
	if l := s.List(); len(l) != 2 || l[0].Name != "Ann" || l[1].Name != "Bob Smith" {
		t.Fatalf("by pinyin: %v", l)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the file: %v %v", st, err)
	}
	if c, ok := s.ByNumber("138 0000 0009"); !ok || c.ID != bob.ID {
		t.Fatalf("by number: %v %v", c, ok)
	}
	bob.Name, bob.Numbers, bob.Number = "Robert", nil, "13900000000"
	if _, err := s.Save(bob); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ByNumber("13800000009"); ok {
		t.Fatal("the old number still names the contact")
	}
	again := Open(path, spell)
	if c, ok := again.Get(bob.ID); !ok || c.Name != "Robert" || c.Number != "13900000000" || again.Created {
		t.Fatalf("reopened: %+v %v", c, ok)
	}
	if err := s.Delete(ann.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ann.ID); err != ErrNotFound {
		t.Fatalf("deleting twice: %v", err)
	}
	if _, err := s.Save(ui.Contact{ID: "c99", Name: "x"}); err != ErrNotFound {
		t.Fatalf("changing a contact that is not there: %v", err)
	}
	if _, err := s.Save(ui.Contact{Name: " \n", Numbers: []ui.ContactNumber{{Number: "--"}}}); err != ErrEmpty {
		t.Fatalf("an empty contact: %v", err)
	}
	if n, _ := s.Save(ui.Contact{Number: "12345"}); n.Name != "12345" {
		t.Fatalf("a contact with a number only is named by it: %+v", n)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".*"))
	if len(leftovers) > 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}
}

func TestStoreLimits(t *testing.T) {
	s, _ := open(t)
	many := []ui.ContactNumber{{Number: strings.Repeat("1", 60)}}
	for i := 0; i < 20; i++ {
		many = append(many, ui.ContactNumber{Number: fmt.Sprintf("1380000%04d", i)})
	}
	c, err := s.Save(ui.Contact{Name: strings.Repeat("\u5f20", 100), Numbers: many, Notes: strings.Repeat("n", 5000)})
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(c.Name)) != MaxName || len(c.Numbers) != MaxNumbers || len([]rune(c.Notes)) != MaxNotes {
		t.Fatalf("limits: name %d, numbers %d, notes %d", len([]rune(c.Name)), len(c.Numbers), len([]rune(c.Notes)))
	}
	for _, n := range c.Numbers {
		if len(n.Number) > MaxNumber {
			t.Fatalf("a number of %d bytes", len(n.Number))
		}
	}
}

func TestStoreAddAll(t *testing.T) {
	s, _ := open(t)
	s.Save(ui.Contact{Name: "Ann", Numbers: []ui.ContactNumber{{Number: "10086"}, {Number: "10010"}}})
	added, skipped, err := s.AddAll([]ui.Contact{
		{Name: "ann", Number: "+86 10086"},
		{Name: "Ann", Number: "10000"},
		{Name: "Cy", Number: "1"},
		{Name: ""},
	})
	if err != nil || added != 2 || skipped != 2 || s.Len() != 3 {
		t.Fatalf("added %d, skipped %d, %v; %v", added, skipped, err, s.List())
	}
	if added, skipped, _ := s.AddAll([]ui.Contact{{Name: "Cy", Number: "1"}}); added != 0 || skipped != 1 {
		t.Fatalf("again: added %d, skipped %d", added, skipped)
	}
}

// Import gives the IDs of what it added, by place; DeleteIDs takes exactly
// those out again, in one write, leaving the rest.
func TestStoreImportDeleteIDs(t *testing.T) {
	s, path := open(t)
	ann, _ := s.Save(ui.Contact{Name: "Ann", Number: "10086"})
	ids, err := s.Import([]ui.Contact{{Name: "Bo", Number: "2"}, {Name: "Ann", Number: "10086"}, {Name: ""}, {Name: "Cy", Number: "3"}})
	if err != nil || len(ids) != 4 || ids[0] == "" || ids[1] != "" || ids[2] != "" || ids[3] == "" || s.Len() != 3 {
		t.Fatalf("import: %q %v", ids, err)
	}
	if n, err := s.DeleteIDs([]string{ids[0], ids[3], "c999"}); err != nil || n != 2 {
		t.Fatalf("delete: %d %v", n, err)
	}
	if n, _ := s.DeleteIDs([]string{ids[0]}); n != 0 {
		t.Fatalf("deleting twice: %d", n)
	}
	again := Open(path, spell)
	if l := again.List(); len(l) != 1 || l[0].ID != ann.ID {
		t.Fatalf("left: %v", l)
	}
	if _, ok := again.ByNumber("2"); ok {
		t.Fatal("a deleted contact still names its number")
	}
}

// The prototype's file, a JSON array of name, number and pinyin, is read
// as it is; its contacts get IDs and labelled numbers.
func TestStoreReadsTheOldFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	old := `[{"name": "\u5f20\u4e09", "number": "13800000001", "pinyin": "zhangsan"}, {"name": "Ann", "number": "10086"}, {"id": "c7", "name": "Cy", "number": "1"}, {"id": "c7", "name": "Di", "number": "2"}]`
	os.WriteFile(path, []byte(old), 0o600)
	s := Open(path, spell)
	l := s.List()
	if len(l) != 4 || l[0].Name != "Ann" || l[0].Pinyin != "ann" || l[3].Pinyin != "zhangsan" || nums(l[3]) != "mobile=13800000001" {
		t.Fatalf("%+v", l)
	}
	ids := map[string]bool{}
	for _, c := range l {
		if c.ID == "" || ids[c.ID] {
			t.Fatalf("IDs: %+v", l)
		}
		ids[c.ID] = true
	}
	if c, _ := s.Save(ui.Contact{Name: "Ed", Number: "3"}); c.ID == "c7" || ids[c.ID] {
		t.Fatalf("a new ID that is taken: %s", c.ID)
	}
}

func TestStoreKeepsWhatItCannotRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contacts.json")
	os.WriteFile(path, []byte(`[{"name": "Ann"`), 0o600)
	s := Open(path, spell)
	if s.Kept != path+".damaged" || s.Len() != 0 {
		t.Fatalf("kept %q, %d contacts", s.Kept, s.Len())
	}
	if raw, _ := os.ReadFile(s.Kept); string(raw) != `[{"name": "Ann"` {
		t.Fatalf("kept: %q", raw)
	}
	if _, err := s.Save(ui.Contact{Name: "Bo", Number: "1"}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(`{`), 0o600)
	if s := Open(path, spell); s.Kept != path+".damaged-2" {
		t.Fatalf("a second damaged file: %q", s.Kept)
	}
	// A file that is there but cannot be read is never written over.
	locked := filepath.Join(dir, "locked")
	os.Mkdir(locked, 0o700)
	u := Open(locked, spell)
	if _, err := u.Save(ui.Contact{Name: "Cy", Number: "2"}); err == nil {
		t.Fatal("saved over a file it could not read")
	}
	if st, err := os.Stat(locked); err != nil || !st.IsDir() {
		t.Fatalf("%v %v", st, err)
	}
}

func TestStoreSeed(t *testing.T) {
	s, path := open(t)
	if err := s.Seed([]ui.Contact{{Name: "Ann", Number: "10086", Pinyin: "ann"}}); err != nil {
		t.Fatal(err)
	}
	var onDisk []ui.Contact
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &onDisk); err != nil || len(onDisk) != 1 || onDisk[0].ID == "" {
		t.Fatalf("%s %v", raw, err)
	}
}

// A batch writes once, at its end, in order; a batch whose write fails
// leaves the store as it was.
func TestStoreBatch(t *testing.T) {
	s, path := open(t)
	ann, _ := s.Save(ui.Contact{Name: "Ann", Number: "1001"})
	before, _ := os.ReadFile(path)
	var ids []string
	err := s.Batch(func() {
		for i, n := range []string{"Zoe", "Bob", "Cat"} {
			c, err := s.Save(ui.Contact{Name: n, Number: fmt.Sprint(2000 + i)})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, c.ID)
			if raw, _ := os.ReadFile(path); string(raw) != string(before) {
				t.Fatal("written before the batch's end")
			}
		}
		ann.Notes = "changed"
		if _, err := s.Save(ann); err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(ids[2]); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range Open(path, spell).List() {
		names = append(names, c.Name+":"+c.Notes)
	}
	if strings.Join(names, ",") != "Ann:changed,Bob:,Zoe:" {
		t.Errorf("%v", names)
	}
	if c, ok := s.ByNumber("2001"); !ok || c.ID != ids[1] {
		t.Errorf("by number after the batch: %+v", c)
	}
	s.unreadable = os.ErrPermission
	err = s.Batch(func() {
		s.Save(ui.Contact{Name: "Dan", Number: "3"})
		s.Delete(ann.ID)
	})
	if err == nil || s.Len() != 3 {
		t.Errorf("%v, %d contacts", err, s.Len())
	}
	if _, ok := s.Get(ann.ID); !ok {
		t.Error("the failed batch's delete stayed")
	}
}

// E-mail and postal addresses are kept within their limits; a photo given
// as PhotoData is kept as a file under its key, one the store does not
// have is refused; a photo no contact shows goes after a while.
func TestStoreEmailsAddressesPhotos(t *testing.T) {
	s, path := open(t)
	var emails []ui.ContactEmail
	for i := 0; i < MaxEmails+2; i++ {
		emails = append(emails, ui.ContactEmail{Address: fmt.Sprintf(" a%d@x.test ", i), Label: "fax"})
	}
	emails = append(emails, ui.ContactEmail{Address: "A0@X.TEST"})
	jpg := picture(t, 50)
	ann, err := s.Save(ui.Contact{Name: "Ann", Number: "1001", Emails: emails, PhotoData: jpg,
		Addresses: []ui.ContactAddress{{Street: strings.Repeat("x", MaxAddressPart+5), Label: ui.NumberWork}, {}, {City: "Town\nCentre"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ann.Emails) != MaxEmails || ann.Emails[0] != (ui.ContactEmail{Address: "a0@x.test", Label: ui.NumberOther}) {
		t.Errorf("%+v", ann.Emails)
	}
	if len(ann.Addresses) != 2 || len([]rune(ann.Addresses[0].Street)) != MaxAddressPart || ann.Addresses[1].City != "Town Centre" {
		t.Errorf("%+v", ann.Addresses)
	}
	if ann.Photo != PhotoKey(jpg) || ann.PhotoData != nil {
		t.Fatalf("photo %q", ann.Photo)
	}
	if raw, err := s.Photo(ann.Photo); err != nil || !bytes.Equal(raw, jpg) {
		t.Fatalf("%v", err)
	}
	if raw, _ := os.ReadFile(path); bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(jpg)[:20])) {
		t.Error("the photo is in contacts.json")
	}
	if _, err := s.Save(ui.Contact{Name: "Bob", Photo: "0123456789abcdef"}); !errors.Is(err, ErrNoPhoto) {
		t.Errorf("a photo the store does not have: %v", err)
	}
	if _, err := s.Save(ui.Contact{Name: "Bob", PhotoData: []byte("not a picture")}); !errors.Is(err, ErrPhoto) {
		t.Errorf("not a picture: %v", err)
	}
	if again := Open(path, spell); len(again.List()) != 1 || again.List()[0].Photo != ann.Photo || len(again.List()[0].Emails) != MaxEmails {
		t.Errorf("read again: %+v", again.List())
	}
	loose, _ := s.PutPhoto(picture(t, 99))
	ann.Photo = ""
	s.Save(ann)
	old := time.Now().Add(-2 * photoKeep)
	for _, k := range []string{loose, PhotoKey(jpg)} {
		os.Chtimes(filepath.Join(s.photosDir(), k), old, old)
	}
	fresh, _ := s.PutPhoto(picture(t, 7))
	s.gcAt = time.Time{}
	s.gcPhotos(time.Now())
	if s.HasPhoto(loose) || s.HasPhoto(PhotoKey(jpg)) || !s.HasPhoto(fresh) {
		t.Errorf("after the clean-up: %v %v %v", s.HasPhoto(loose), s.HasPhoto(PhotoKey(jpg)), s.HasPhoto(fresh))
	}
}

// A new contact goes to its book, NewBook when it names none, the phone
// alone for BookPhone; a change never moves it; MoveBook does.
func TestStoreBooks(t *testing.T) {
	s, _ := open(t)
	s.NewBook = "a1/b1"
	ann, _ := s.Save(ui.Contact{Name: "Ann", Number: "1"})
	bob, _ := s.Save(ui.Contact{Name: "Bob", Number: "2", Book: ui.BookPhone})
	cat, _ := s.Save(ui.Contact{Name: "Cat", Number: "3", Book: "a2/b1"})
	if ann.Book != "a1/b1" || bob.Book != "" || cat.Book != "a2/b1" {
		t.Fatalf("%q %q %q", ann.Book, bob.Book, cat.Book)
	}
	ann.Book, ann.Notes = "", "changed"
	if got, _ := s.Save(ann); got.Book != "a1/b1" {
		t.Errorf("a change moved it: %q", got.Book)
	}
	ids, _ := s.Import([]ui.Contact{{Name: "Dan", Number: "4"}})
	if d, _ := s.Get(ids[0]); d.Book != "a1/b1" {
		t.Errorf("imported to %q", d.Book)
	}
	if n, err := s.MoveBook("a1/b1", ""); n != 2 || err != nil {
		t.Errorf("%d %v", n, err)
	}
	if a, _ := s.Get(ann.ID); a.Book != "" {
		t.Errorf("%q", a.Book)
	}
}

// A sync puts contacts in groups by name, in a batch that writes the
// groups once.
func TestSetGroupsOf(t *testing.T) {
	s, path := open(t)
	ann, _ := s.Save(ui.Contact{Name: "Ann", Number: "1"})
	bob, _ := s.Save(ui.Contact{Name: "Bob", Number: "2"})
	fam, _ := s.AddGroup("Family")
	s.SetMembers(fam.ID, []string{ann.ID}, true)
	groupsFile := filepath.Join(filepath.Dir(path), "contact_groups.json")
	before, _ := os.ReadFile(groupsFile)
	err := s.Batch(func() {
		if err := s.SetGroupsOf(bob.ID, []string{"family", "Work"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetGroupsOf(ann.ID, []string{"Work"}); err != nil {
			t.Fatal(err)
		}
		if raw, _ := os.ReadFile(groupsFile); !bytes.Equal(raw, before) {
			t.Fatal("written before the batch's end")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	again := Open(path, spell)
	got := map[string][]string{}
	for _, g := range again.Groups() {
		got[g.Name] = g.Members
	}
	if !slices.Equal(got["Family"], []string{bob.ID}) || !slices.Equal(got["Work"], []string{bob.ID, ann.ID}) || len(got) != 2 {
		t.Errorf("%v", got)
	}
	if g, ok := again.GroupNamed("WORK"); !ok || g.Name != "Work" {
		t.Errorf("%+v", g)
	}
	if names := again.GroupNames(again.List()[1].Groups); !slices.Equal(names, []string{"Family", "Work"}) {
		t.Errorf("%v", names)
	}
}
