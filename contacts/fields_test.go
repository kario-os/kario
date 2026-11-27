package contacts

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kario-os/kario/ui"
)

// iosFieldsCard is a card as iPhone Contacts writes the other fields: a
// year-less birthday, the anniversary as a labelled X-ABDATE beside another
// date, a nickname list, an organization with a unit, labelled web
// addresses (one escaped as Android exports write it).
const iosFieldsCard = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Alice Smith\r\nN:Smith;Alice;;;\r\nNICKNAME:Al,Ally\r\n" +
	"ORG:Example Corp;Sales\r\nTITLE:Engineer\r\nBDAY;X-APPLE-OMIT-YEAR=1604:1604-04-12\r\n" +
	"item1.X-ABDATE;type=pref:2010-06-12\r\nitem1.X-ABLabel:_$!<Anniversary>!$_\r\n" +
	"item2.X-ABDATE:2020-01-01\r\nitem2.X-ABLabel:_$!<Other>!$_\r\n" +
	"item3.URL;type=pref:https://alice.example\r\nitem3.X-ABLabel:_$!<HomePage>!$_\r\n" +
	"URL:http\\://work.example/a\r\nTEL:1001\r\nUID:a1\r\nEND:VCARD\r\n"

func TestOtherFieldsRead(t *testing.T) {
	c, _, err := ParseCard([]byte(iosFieldsCard))
	want := ui.Contact{Nickname: "Al, Ally", Org: "Example Corp", Title: "Engineer", Birthday: "--04-12", Anniversary: "2010-06-12",
		URLs: []string{"https://alice.example", "http://work.example/a"}}
	got := ui.Contact{Nickname: c.Nickname, Org: c.Org, Title: c.Title, Birthday: c.Birthday, Anniversary: c.Anniversary, URLs: c.URLs}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v %v", got, err)
	}
	for _, x := range []struct{ line, bday, ann string }{
		{"BDAY:19850412\nANNIVERSARY:--0612", "1985-04-12", "--06-12"},
		{"BDAY:--04-12\nX-ANNIVERSARY:2010-06-12T00:00:00Z", "--04-12", "2010-06-12"},
		{"BDAY;VALUE=text:circa 1800\nBDAY:1985-04-12", "1985-04-12", ""},
		{"BDAY:1985-02-29\nANNIVERSARY:2000-02-29", "", "2000-02-29"},
		{"BDAY:--0229\nANNIVERSARY:2010-13-01", "--02-29", ""},
	} {
		c, _, _ := ParseCard([]byte("BEGIN:VCARD\nVERSION:4.0\nFN:x\n" + x.line + "\nEND:VCARD\n"))
		if c.Birthday != x.bday || c.Anniversary != x.ann {
			t.Errorf("%q: %q %q", x.line, c.Birthday, c.Anniversary)
		}
	}
	list, _, _ := Parse([]byte(iosFieldsCard))
	if len(list) != 1 || list[0].Birthday != "--04-12" || list[0].Anniversary != "2010-06-12" {
		t.Errorf("a file's: %+v", list)
	}
}

func TestOtherFieldsWritten(t *testing.T) {
	in := ui.Contact{Name: "Bob", Number: "1002", Nickname: "Bobby, B", Org: "Acme; Inc", Title: "CTO", Birthday: "--04-12",
		Anniversary: "2010-06-12", URLs: []string{"https://bob.example/?a=1,b=2"}}
	for v, lines := range map[string][]string{
		"3.0": {"NICKNAME:Bobby,B", "ORG:Acme\\; Inc", "TITLE:CTO", "BDAY;X-APPLE-OMIT-YEAR=1604:1604-04-12", "X-ANNIVERSARY:2010-06-12",
			"URL:https://bob.example/?a=1,b=2"},
		"4.0": {"BDAY:--0412", "ANNIVERSARY:20100612"},
	} {
		raw := NewCard(in, "u", v, patchAt)
		c, _, err := ParseCard(raw)
		want, _ := Clean(in, nil)
		if err != nil || !reflect.DeepEqual(c, want) {
			t.Errorf("%s: %+v %v\n%s", v, c, err, raw)
		}
		for _, l := range lines {
			if !bytes.Contains(raw, []byte(l+"\r\n")) {
				t.Errorf("%s: no %q in\n%s", v, l, raw)
			}
		}
	}
	file := Write([]ui.Contact{in})
	back, _, _ := Parse(file)
	if want, _ := Clean(in, nil); len(back) != 1 || !reflect.DeepEqual(back[0], want) {
		t.Errorf("a file: %+v\n%s", back, file)
	}
}

// A change rewrites only the fields that changed: the birthday as the card
// had it, the anniversary's X-ABDATE and its label gone with it (the other
// date stays), the organization's unit kept, a web address and its label.
func TestPatchOtherFields(t *testing.T) {
	raw := []byte(iosFieldsCard)
	c, _, _ := ParseCard(raw)
	if same, err := PatchCard(raw, c, false, patchAt); err != nil || !bytes.Equal(same, raw) {
		t.Fatalf("unchanged: %v\n%s", err, same)
	}
	now := c
	now.Birthday, now.Anniversary, now.Org, now.Nickname, now.Title = "1990-04-12", "", "New Corp", "Al", ""
	now.URLs = []string{"http://work.example/a", "https://new.example"}
	got, err := PatchCard(raw, now, false, patchAt)
	if err != nil {
		t.Fatal(err)
	}
	back, _, _ := ParseCard(got)
	want, _ := Clean(now, nil)
	if !reflect.DeepEqual(back, want) {
		t.Errorf("%+v\n%s", back, got)
	}
	out := string(got)
	for _, keep := range []string{"BDAY:1990-04-12\r\n", "ORG:New Corp;Sales\r\n", "NICKNAME:Al\r\n", "item2.X-ABDATE:2020-01-01\r\nitem2.X-ABLabel:_$!<Other>!$_\r\n",
		"URL:http\\://work.example/a\r\n", "URL:https://new.example\r\n"} {
		if !strings.Contains(out, keep) {
			t.Errorf("no %q in\n%s", keep, out)
		}
	}
	for _, gone := range []string{"item1.", "item3.", "TITLE", "1604"} {
		if strings.Contains(out, gone) {
			t.Errorf("still %q in\n%s", gone, out)
		}
	}
	now.Anniversary = "--06-13"
	got, _ = PatchCard(raw, now, false, patchAt)
	if !strings.Contains(string(got), "item1.X-ABDATE;X-APPLE-OMIT-YEAR=1604:1604-06-13\r\nitem1.X-ABLabel:_$!<Anniversary>!$_\r\n") {
		t.Errorf("iPhone's anniversary changed where it was:\n%s", got)
	}
	v4 := []byte("BEGIN:VCARD\nVERSION:4.0\nFN:x\nUID:u\nEND:VCARD\n")
	x, _, _ := ParseCard(v4)
	x.Birthday, x.Anniversary = "--01-02", "2001-02-03"
	got, _ = PatchCard(v4, x, false, patchAt)
	if !strings.Contains(string(got), "BDAY:--0102\nANNIVERSARY:20010203\n") {
		t.Errorf("4.0 added:\n%s", got)
	}
}

// A photo at a URL a sync fetched stays the card's while the contact
// shows it; another photo replaces the line, none drops it.
func TestPatchFetchedPhoto(t *testing.T) {
	raw := []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ann\r\nTEL:1\r\nPHOTO;VALUE=uri:https://dav.example/p/ann.jpg\r\nUID:u\r\nEND:VCARD\r\n")
	c, info, _ := ParseCard(raw)
	if c.Photo != "" || info.PhotoURI != "https://dav.example/p/ann.jpg" {
		t.Fatalf("%+v %+v", c, info)
	}
	jpg := picture(t, 40)
	c.Photo = PhotoKey(jpg)
	if got, err := PatchCardFetched(raw, c, false, patchAt, c.Photo); err != nil || !bytes.Equal(got, raw) {
		t.Errorf("the fetched photo is the card's: %v\n%s", err, got)
	}
	if got, _ := PatchCardFetched(raw, ui.Contact{Name: "Ann", Number: "1"}, false, patchAt, c.Photo); strings.Contains(string(got), "PHOTO") {
		t.Errorf("removed on the phone:\n%s", got)
	}
	other := picture(t, 90)
	got, _ := PatchCardFetched(raw, ui.Contact{Name: "Ann", Number: "1", Photo: PhotoKey(other), PhotoData: other}, false, patchAt, c.Photo)
	if back, _, _ := ParseCard(got); back.Photo != PhotoKey(other) || strings.Contains(string(got), "ann.jpg") {
		t.Errorf("a new photo:\n%s", got)
	}
}

func TestCleanOtherFields(t *testing.T) {
	c, _ := Clean(ui.Contact{Name: "x", Birthday: "1985-4-12", Anniversary: " --12-31 ", Nickname: "a\nb",
		URLs: []string{"a.example", " a.example ", "b .example", strings.Repeat("u", MaxURL+1), "c", "d", "e"}}, nil)
	if c.Birthday != "" || c.Anniversary != "--12-31" || c.Nickname != "a b" || !reflect.DeepEqual(c.URLs, []string{"a.example", "b.example", "c", "d"}) {
		t.Errorf("%+v", c)
	}
	s := Open(filepath.Join(t.TempDir(), "c.json"), nil)
	a, _ := s.Save(ui.Contact{Name: "Ann", Number: "1", Birthday: "1990-01-02", URLs: []string{"a.example"}})
	b, _ := s.Save(ui.Contact{Name: "Ann", Number: "2", Org: "Acme", Anniversary: "--03-04", URLs: []string{"b.example"}})
	m, err := s.Merge([]string{a.ID, b.ID})
	if err != nil || m.Birthday != "1990-01-02" || m.Org != "Acme" || m.Anniversary != "--03-04" || !reflect.DeepEqual(m.URLs, []string{"a.example", "b.example"}) {
		t.Errorf("merged: %+v %v", m, err)
	}
}
