package carddav

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"strings"
	"testing"

	"github.com/kario-os/kario/carddav/davtest"
	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/ui"
)

// picture is a small JPEG of one shade.
func picture(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 6, 6))
	for i := range img.Pix {
		img.Pix[i] = shade
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func photoLine(data []byte) string {
	var b strings.Builder
	line := "PHOTO;ENCODING=b;TYPE=JPEG:" + base64.StdEncoding.EncodeToString(data)
	for first := true; len(line) > 0; first = false {
		n := min(len(line), map[bool]int{true: 75, false: 74}[first])
		if !first {
			b.WriteString(" ")
		}
		b.WriteString(line[:n] + "\r\n")
		line = line[n:]
	}
	return strings.TrimSuffix(b.String(), "\r\n")
}

// E-mail and postal addresses and the photo come to the phone and go back;
// a photo nobody changed is not written again, so its card's ETag stays.
func TestSyncEmailsAddressesPhoto(t *testing.T) {
	jpg := picture(t, 40)
	e := newEnv(t, func(s *davtest.Server) {
		s.Put("ann.vcf", card3("ann", "Ann", "TEL;TYPE=CELL:1001", "EMAIL;TYPE=INTERNET,WORK:ann@work.test",
			"ADR;TYPE=HOME:;;1 Main St;Town;;100000;China", photoLine(jpg), "X-KARIO:keep"))
	})
	e.start()
	e.run()
	ann, ok := e.byName("Ann")
	if !ok || len(ann.Emails) != 1 || ann.Emails[0] != (ui.ContactEmail{Address: "ann@work.test", Label: ui.NumberWork}) ||
		len(ann.Addresses) != 1 || ann.Addresses[0].Street != "1 Main St" || ann.Photo != contacts.PhotoKey(jpg) {
		t.Fatalf("%+v", ann)
	}
	if raw, err := e.store.Photo(ann.Photo); err != nil || !bytes.Equal(raw, jpg) {
		t.Fatalf("the phone's photo: %v", err)
	}
	if res := e.run(); res != (Result{}) || e.srv.Count("PUT") != 0 {
		t.Errorf("a sync with nothing new: %+v, %d PUTs", res, e.srv.Count("PUT"))
	}

	ann.Emails = append(ann.Emails, ui.ContactEmail{Address: "ann@home.test", Label: ui.NumberHome})
	ann.Addresses[0].City = "City"
	ann.Notes = "note"
	e.store.Save(ann)
	if res := e.run(); res.Uploaded != 1 {
		t.Errorf("%+v", res)
	}
	card := string(e.srv.Card("ann.vcf"))
	for _, want := range []string{"EMAIL;TYPE=INTERNET,WORK:ann@work.test\r\n", "EMAIL;TYPE=INTERNET,HOME:ann@home.test\r\n",
		"ADR;TYPE=HOME:;;1 Main St;City;;100000;China\r\n", photoLine(jpg) + "\r\n", "X-KARIO:keep\r\n"} {
		if !strings.Contains(card, want) {
			t.Errorf("no %q in\n%s", want, card)
		}
	}
	if strings.Contains(card, ";Town;") {
		t.Errorf("the old address stayed:\n%s", card)
	}

	pic := picture(t, 200)
	key, _ := e.store.PutPhoto(pic)
	ann, _ = e.byName("Ann")
	ann.Photo = key
	e.store.Save(ann)
	e.run()
	if c, _, _ := contacts.ParseCard(e.srv.Card("ann.vcf")); c.Photo != key || strings.Count(string(e.srv.Card("ann.vcf")), "PHOTO") != 1 {
		t.Errorf("a new photo: %s", e.srv.Card("ann.vcf"))
	}
	puts := e.srv.Count("PUT")
	if res := e.run(); res != (Result{}) || e.srv.Count("PUT") != puts {
		t.Errorf("the photo went again: %+v", res)
	}

	other := picture(t, 120)
	e.srv.Put("ann.vcf", card3("ann", "Ann", "TEL;TYPE=CELL:1001", photoLine(other)))
	e.run()
	if ann, _ := e.byName("Ann"); ann.Photo != contacts.PhotoKey(other) || len(ann.Emails) != 0 {
		t.Errorf("the server's new photo: %+v", ann)
	}
	ann, _ = e.byName("Ann")
	ann.Photo = ""
	e.store.Save(ann)
	e.run()
	if strings.Contains(string(e.srv.Card("ann.vcf")), "PHOTO") {
		t.Errorf("removed on the phone: %s", e.srv.Card("ann.vcf"))
	}
}

// A new contact with a photo goes up with it; a photo at a URL is left
// on the server.
func TestNewContactPhoto(t *testing.T) {
	jpg := picture(t, 77)
	e := newEnv(t, func(s *davtest.Server) {
		s.Put("url.vcf", card3("u", "Url", "TEL:1002", "PHOTO;VALUE=uri:https://example.com/u.jpg"))
	})
	e.add(ui.Contact{Name: "Ann", Number: "1001", PhotoData: jpg, Emails: []ui.ContactEmail{{Address: "a@x.test"}}})
	e.start()
	e.run()
	name, ok := e.srv.Find("FN:Ann")
	if c, _, _ := contacts.ParseCard(e.srv.Card(name)); !ok || c.Photo != contacts.PhotoKey(jpg) || len(c.Emails) != 1 {
		t.Errorf("%s", e.srv.Card(name))
	}
	u, _ := e.byName("Url")
	if u.Photo != "" {
		t.Fatalf("a URL was taken: %+v", u)
	}
	u.Notes = "x"
	e.store.Save(u)
	e.run()
	if !strings.Contains(string(e.srv.Card("url.vcf")), "PHOTO;VALUE=uri:https://example.com/u.jpg") {
		t.Errorf("%s", e.srv.Card("url.vcf"))
	}
}

func TestMergeRuleExtras(t *testing.T) {
	a, b := ui.ContactEmail{Address: "a@x.test", Label: ui.NumberHome}, ui.ContactEmail{Address: "b@x.test", Label: ui.NumberWork}
	c := ui.ContactEmail{Address: "c@x.test", Label: ui.NumberOther}
	home := ui.ContactAddress{Label: ui.NumberHome, City: "Home"}
	base := ui.Contact{Name: "Ann", Number: "1", Emails: []ui.ContactEmail{a, b}, Addresses: []ui.ContactAddress{home}, Photo: "0000000000000001"}
	local := base
	local.Emails = []ui.ContactEmail{{Address: "a@x.test", Label: ui.NumberWork}}
	local.Photo = "0000000000000002"
	remote := base
	remote.Emails = []ui.ContactEmail{a, b, c}
	remote.Addresses = nil
	got := Merge(base, local, remote)
	if len(got.Emails) != 2 || got.Emails[0] != (ui.ContactEmail{Address: "a@x.test", Label: ui.NumberWork}) || got.Emails[1] != c ||
		len(got.Addresses) != 0 || got.Photo != "0000000000000002" {
		t.Errorf("%+v", got)
	}
	remote.Photo = "0000000000000003"
	if got := Merge(base, local, remote); got.Photo != "0000000000000003" {
		t.Errorf("both changed the photo: the server's wins: %q", got.Photo)
	}
}

// A photo at a URL on the book's own server comes to the phone once (a
// change of the card that keeps the URL does not fetch it again), and the
// card keeps its URL until the phone's photo changes; one on another
// server, one too large or not a picture is left there.
func TestPhotoAtURL(t *testing.T) {
	jpg := picture(t, 33)
	e := newEnv(t, func(s *davtest.Server) {
		s.PutFile("/dav/photos/ann.jpg", jpg)
		s.PutFile("/dav/photos/big.jpg", bytes.Repeat([]byte{0xff}, contacts.MaxPhotoBytes+1))
		s.PutFile("/dav/photos/text.jpg", []byte("not a picture"))
		s.Put("ann.vcf", card3("ann", "Ann", "TEL:1001", "PHOTO;VALUE=uri:/dav/photos/ann.jpg"))
		s.Put("big.vcf", card3("big", "Big", "TEL:1002", "PHOTO;VALUE=uri:/dav/photos/big.jpg"))
		s.Put("text.vcf", card3("text", "Text", "TEL:1003", "PHOTO;VALUE=uri:/dav/photos/text.jpg"))
		s.Put("far.vcf", card3("far", "Far", "TEL:1004", "PHOTO:https://elsewhere.example/far.jpg"))
	})
	e.start()
	e.run()
	ann, _ := e.byName("Ann")
	if ann.Photo != contacts.PhotoKey(jpg) {
		t.Fatalf("%+v", ann)
	}
	for _, n := range []string{"Big", "Text", "Far"} {
		if c, _ := e.byName(n); c.Photo != "" {
			t.Errorf("%s: %+v", n, c)
		}
	}
	if got := e.srv.Count("GET file"); got != 3 {
		t.Errorf("%d fetches, want 3 (none for another server)", got)
	}
	e.srv.Put("ann.vcf", card3("ann", "Ann", "TEL:1001", "NOTE:x", "PHOTO;VALUE=uri:/dav/photos/ann.jpg"))
	e.run()
	if ann, _ = e.byName("Ann"); ann.Notes != "x" || ann.Photo != contacts.PhotoKey(jpg) || e.srv.Count("GET file") != 3 {
		t.Errorf("fetched again: %+v, %d", ann, e.srv.Count("GET file"))
	}
	ann.Nickname = "Annie"
	e.store.Save(ann)
	e.run()
	card := string(e.srv.Card("ann.vcf"))
	if !strings.Contains(card, "PHOTO;VALUE=uri:/dav/photos/ann.jpg\r\n") || !strings.Contains(card, "NICKNAME:Annie\r\n") {
		t.Errorf("the URL did not stay:\n%s", card)
	}
	if res := e.run(); res != (Result{}) {
		t.Errorf("again: %+v", res)
	}
	ann, _ = e.byName("Ann")
	ann.Photo = ""
	e.store.Save(ann)
	e.run()
	if strings.Contains(string(e.srv.Card("ann.vcf")), "PHOTO") {
		t.Errorf("removed on the phone:\n%s", e.srv.Card("ann.vcf"))
	}
}

// The other fields go both ways and merge field by field: one only the
// phone changed is the phone's, one both changed the server's, web
// addresses as a set.
func TestSyncOtherFields(t *testing.T) {
	e := newEnv(t, func(s *davtest.Server) {
		s.Put("ann.vcf", card3("ann", "Ann", "TEL:1001", "BDAY;X-APPLE-OMIT-YEAR=1604:1604-04-12", "ORG:Acme;Sales", "TITLE:Engineer",
			"URL:https://a.example", "item1.X-ABDATE:2010-06-12", "item1.X-ABLabel:_$!<Anniversary>!$_"))
	})
	e.start()
	e.run()
	ann, _ := e.byName("Ann")
	if ann.Birthday != "--04-12" || ann.Org != "Acme" || ann.Title != "Engineer" || ann.Anniversary != "2010-06-12" || len(ann.URLs) != 1 {
		t.Fatalf("%+v", ann)
	}
	ann.Nickname, ann.Title, ann.URLs = "Annie", "Lead", append(ann.URLs, "https://b.example")
	e.store.Save(ann)
	e.srv.Put("ann.vcf", card3("ann", "Ann", "TEL:1001", "BDAY;X-APPLE-OMIT-YEAR=1604:1604-04-12", "ORG:Acme;Sales", "TITLE:Manager",
		"URL:https://a.example", "URL:https://c.example", "item1.X-ABDATE:2011-06-12", "item1.X-ABLabel:_$!<Anniversary>!$_"))
	e.run()
	ann, _ = e.byName("Ann")
	if ann.Nickname != "Annie" || ann.Title != "Manager" || ann.Anniversary != "2011-06-12" || strings.Join(ann.URLs, " ") != "https://a.example https://c.example https://b.example" {
		t.Errorf("merged: %+v", ann)
	}
	card := string(e.srv.Card("ann.vcf"))
	for _, want := range []string{"NICKNAME:Annie\r\n", "TITLE:Manager\r\n", "URL:https://b.example\r\n", "item1.X-ABDATE:2011-06-12\r\n", "ORG:Acme;Sales\r\n"} {
		if !strings.Contains(card, want) {
			t.Errorf("no %q in\n%s", want, card)
		}
	}
	old := &State{Book: e.sync.Book.URL, Synced: true, Version: 2, Token: e.sync.State.Token, Items: e.sync.State.Items}
	e.sync.State = old
	gets := e.srv.Count("REPORT multiget")
	e.run()
	if e.srv.Count("REPORT multiget") == gets || old.Version != stateVersion {
		t.Error("a state from before these fields did not read the cards again")
	}
}
