package contacts

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// picture is a small JPEG of one colour.
func picture(t testing.TB, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = shade
	}
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

var patchAt = time.Date(2026, 11, 18, 13, 41, 0, 0, time.UTC)

// iosCard is a card as iPhone Contacts writes one: grouped numbers with
// their labels, e-mail, an address, a photo folded over many lines.
func iosCard() string {
	photo := "PHOTO;ENCODING=b;TYPE=JPEG:" + strings.Repeat("/9j/4AAQSkZJRgABAQAAAQABAAD", 40)
	var b bytes.Buffer
	b.WriteString("BEGIN:VCARD\r\nVERSION:3.0\r\nPRODID:-//Apple Inc.//iPhone OS 18.1//EN\r\n")
	b.WriteString("N:Smith;Alice;;;\r\nFN:Alice Smith\r\nORG:Example Corp;\r\n")
	b.WriteString("item1.TEL;type=pref:+1 (650) 555-0100\r\nitem1.X-ABLabel:_$!<Mobile>!$_\r\n")
	b.WriteString("TEL;type=WORK;type=VOICE:+1 650 555 0199\r\n")
	b.WriteString("item2.EMAIL;type=INTERNET:alice@example.com\r\nitem2.X-ABLabel:_$!<Home>!$_\r\n")
	b.WriteString("item3.ADR;type=HOME:;;1 Infinite Loop;Cupertino;CA;95014;USA\r\n")
	b.WriteString("NOTE:Met at the conference\r\n")
	fold(&b, photo)
	b.WriteString("X-SOCIALPROFILE;type=twitter:https://twitter.com/alice\r\n")
	b.WriteString("UID:6F2A1D2E-0000-4000-8000-000000000001\r\nREV:2026-01-02T03:04:05Z\r\nEND:VCARD\r\n")
	return b.String()
}

func TestParseCard(t *testing.T) {
	c, info, err := ParseCard([]byte(iosCard()))
	if err != nil || c.Name != "Alice Smith" || nums(c) != "other=+1 (650) 555-0100,work=+1 650 555 0199" || c.Notes != "Met at the conference" {
		t.Fatalf("%+v %v", c, err)
	}
	if info.UID != "6F2A1D2E-0000-4000-8000-000000000001" || info.Version != "3.0" || info.Group || info.Members != nil {
		t.Errorf("%+v", info)
	}
	if len(c.Emails) != 1 || c.Emails[0] != (ui.ContactEmail{Address: "alice@example.com", Label: ui.NumberOther}) ||
		len(c.Addresses) != 1 || c.Addresses[0] != (ui.ContactAddress{Label: ui.NumberHome, Street: "1 Infinite Loop", City: "Cupertino", Region: "CA", Postcode: "95014", Country: "USA"}) {
		t.Errorf("%+v %+v", c.Emails, c.Addresses)
	}
	if c.Photo != "" {
		t.Errorf("a photo that is no picture: %q", c.Photo)
	}
	group := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:g1\r\nFN:Family\r\nX-ADDRESSBOOKSERVER-KIND:group\r\nX-ADDRESSBOOKSERVER-MEMBER:urn:uuid:1\r\nEND:VCARD\r\n"
	if g, info, err := ParseCard([]byte(group)); err != nil || !info.Group || g.Name != "Family" || !slices.Equal(info.Members, []string{"1"}) {
		t.Errorf("an Apple group: %+v %v", info, err)
	}
	if _, info, _ := ParseCard([]byte("BEGIN:VCARD\nVERSION:4.0\nKIND:group\nFN:Team\nEND:VCARD\n")); !info.Group || info.Version != "4.0" {
		t.Errorf("a 4.0 group: %+v", info)
	}
	if _, _, err := ParseCard([]byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:x\r\nEND:VCARD\r\n")); err != ErrEmpty {
		t.Errorf("no name, no number: %v", err)
	}
	if _, _, err := ParseCard([]byte("hello")); err != ErrNotVCard {
		t.Errorf("not a card: %v", err)
	}
}

func TestNewCardRoundTrip(t *testing.T) {
	in := ui.Contact{Name: "\u5f20\u4e09", Notes: "a; b, c\nsecond line",
		Numbers: []ui.ContactNumber{{Number: "+86 138 0013 8000", Label: ui.NumberMobile}, {Number: "010-6552 0000", Label: ui.NumberHome}}}
	for _, v := range []string{"3.0", "4.0"} {
		raw := NewCard(in, "kario-1", v, patchAt)
		c, info, err := ParseCard(raw)
		want, _ := Clean(in, nil)
		if err != nil || !reflect.DeepEqual(c, want) || info.UID != "kario-1" || info.Version != v {
			t.Errorf("%s: %+v %+v %v\n%s", v, c, info, err, raw)
		}
		if !bytes.Contains(raw, []byte("N:\u5f20;\u4e09;;;\r\n")) {
			t.Errorf("%s: N: %s", v, raw)
		}
	}
	raw := string(NewCard(ui.Contact{Name: "Mary Ann Lee", Number: "123"}, "u", "4.0", patchAt))
	for _, line := range []string{"N:Lee;Mary Ann;;;", "TEL;TYPE=cell:123", "REV:20261118T134100Z"} {
		if !strings.Contains(raw, line+"\r\n") {
			t.Errorf("no %q in\n%s", line, raw)
		}
	}
}

func TestPatchUnchanged(t *testing.T) {
	raw := []byte(iosCard())
	c, _, _ := ParseCard(raw)
	c.Numbers[0].Number = "+16505550100"
	c.Emails[0].Address = "Alice@Example.com"
	got, err := PatchCard(raw, c, true, patchAt)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("a number or an address written another way is the same: %v\n%s", err, got)
	}
}

// What the phone does not show survives its edits, byte for byte.
func TestPatchKeepsTheRest(t *testing.T) {
	raw := iosCard()
	base, _, _ := ParseCard([]byte(raw))
	now := ui.Contact{Name: "Alice Jones", Notes: "Moved to Boston", Emails: base.Emails, Addresses: base.Addresses, Org: base.Org,
		Numbers: []ui.ContactNumber{{Number: "+1 650 555 0199", Label: ui.NumberMobile}, {Number: "+1 617 555 0123", Label: ui.NumberHome}}}
	got, err := PatchCard([]byte(raw), now, false, patchAt)
	if err != nil {
		t.Fatal(err)
	}
	c, info, err := ParseCard(got)
	want, _ := Clean(now, nil)
	if err != nil || !reflect.DeepEqual(c, want) || info.UID != "6F2A1D2E-0000-4000-8000-000000000001" {
		t.Fatalf("%+v %v\n%s", c, err, got)
	}
	out := string(got)
	for _, keep := range []string{"PRODID:-//Apple Inc.//iPhone OS 18.1//EN\r\n", "ORG:Example Corp;\r\n",
		"item2.EMAIL;type=INTERNET:alice@example.com\r\nitem2.X-ABLabel:_$!<Home>!$_\r\n",
		"item3.ADR;type=HOME:;;1 Infinite Loop;Cupertino;CA;95014;USA\r\n",
		"X-SOCIALPROFILE;type=twitter:https://twitter.com/alice\r\n"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q", keep)
		}
	}
	var photo bytes.Buffer
	fold(&photo, "PHOTO;ENCODING=b;TYPE=JPEG:"+strings.Repeat("/9j/4AAQSkZJRgABAQAAAQABAAD", 40))
	if !strings.Contains(out, photo.String()) {
		t.Error("the photo is not as it was")
	}
	for _, gone := range []string{"Met at the conference", "item1.", "Alice Smith", "2026-01-02T03:04:05Z"} {
		if strings.Contains(out, gone) {
			t.Errorf("still has %q:\n%s", gone, out)
		}
	}
	for _, line := range []string{"FN:Alice Jones\r\n", "N:Jones;Alice;;;\r\n", "TEL;TYPE=CELL:+1 650 555 0199\r\n",
		"TEL;TYPE=HOME:+1 617 555 0123\r\n", "NOTE:Moved to Boston\r\n", "REV:2026-11-18T13:41:00Z\r\n"} {
		if !strings.Contains(out, line) {
			t.Errorf("no %q in\n%s", line, out)
		}
	}
	if strings.Index(out, "FN:Alice Jones") > strings.Index(out, "ORG:") {
		t.Errorf("the name moved:\n%s", out)
	}
}

// Numbers past MaxNumbers, which the phone never showed, stay when it
// changes the ones it did.
func TestPatchKeepsHiddenNumbers(t *testing.T) {
	var b strings.Builder
	b.WriteString("BEGIN:VCARD\nVERSION:4.0\nUID:urn:uuid:9\nFN:Many\n")
	for i := 0; i < MaxNumbers+2; i++ {
		b.WriteString("TEL;VALUE=uri;TYPE=cell:tel:+1-555-010" + string(rune('0'+i)) + "\n")
	}
	b.WriteString("END:VCARD\n")
	raw := b.String()
	c, _, _ := ParseCard([]byte(raw))
	if len(c.Numbers) != MaxNumbers {
		t.Fatalf("%d numbers", len(c.Numbers))
	}
	c.Numbers = c.Numbers[1:]
	c.Numbers[0].Label = ui.NumberWork
	got, err := PatchCard([]byte(raw), c, false, patchAt)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	if strings.Contains(out, "+1-555-0100") || !strings.Contains(out, "TEL;VALUE=uri;TYPE=work:tel:+1-555-0101\n") ||
		!strings.Contains(out, "TEL;VALUE=uri;TYPE=cell:tel:+1-555-0108\n") || !strings.Contains(out, "TEL;VALUE=uri;TYPE=cell:tel:+1-555-0109\n") {
		t.Errorf("%s", out)
	}
	if strings.Contains(out, "\r") || strings.Contains(out, "REV") {
		t.Errorf("line breaks changed, or a REV the card did not have: %q", out)
	}
	if strings.Contains(out, "\nN:") {
		t.Errorf("vCard 4.0 needs no N:\n%s", out)
	}
}

func TestPatchAddsWhatIsMissing(t *testing.T) {
	raw := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:x\r\nORG:Acme\r\nEMAIL:a@acme.test\r\nEND:VCARD\r\n"
	got, err := PatchCard([]byte(raw), ui.Contact{Name: "Acme Support", Number: "400 800 1234", Notes: "24 h", Org: "Acme",
		Emails: []ui.ContactEmail{{Address: "a@acme.test"}}}, false, patchAt)
	if err != nil {
		t.Fatal(err)
	}
	want := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Acme Support\r\nN:Support;Acme;;;\r\nUID:x\r\nORG:Acme\r\nEMAIL:a@acme.test\r\n" +
		"TEL;TYPE=CELL:400 800 1234\r\nNOTE:24 h\r\nEND:VCARD\r\n"
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if _, err := PatchCard([]byte("BEGIN:VCARD\r\nFN:x\r\n"), ui.Contact{Name: "y"}, false, patchAt); err != ErrNotVCard {
		t.Errorf("no END: %v", err)
	}
}

// A server's card need not be UTF-8: patching one whose number is a run
// of continuation bytes once folded the rewritten line forever.
func TestPatchNotUTF8(t *testing.T) {
	raw := []byte("BEGIN:VCARD\nTEL:0" + strings.Repeat("\xbc", 200) + "\nEND:VCARD")
	done := make(chan error, 1)
	go func() {
		_, err := PatchCard(raw, ui.Contact{Name: "x", Number: "0"}, false, patchAt)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PatchCard does not return")
	}
}

// E-mail and postal addresses, a photo inline (3.0's base64, 4.0's data:
// URI) and CATEGORIES are read; a photo at a URL is not fetched.
func TestParseCardExtras(t *testing.T) {
	jpg := picture(t, 90)
	b64 := base64.StdEncoding.EncodeToString(jpg)
	var v3 bytes.Buffer
	v3.WriteString("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:a\r\nFN:Ann\r\nEMAIL;TYPE=INTERNET,WORK:ann@work.test\r\nEMAIL;TYPE=HOME:mailto:ann@home.test\r\n")
	v3.WriteString("ADR;TYPE=WORK:;Floor 3;1 Main St;Springfield;IL;62701;USA\r\nCATEGORIES:Family,Friends\\, close\r\nCATEGORIES:family,Work\r\n")
	fold(&v3, "PHOTO;ENCODING=b;TYPE=JPEG:"+b64)
	v3.WriteString("END:VCARD\r\n")
	c, _, err := ParseCard(v3.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if want := []ui.ContactEmail{{Address: "ann@work.test", Label: ui.NumberWork}, {Address: "ann@home.test", Label: ui.NumberHome}}; !reflect.DeepEqual(c.Emails, want) {
		t.Errorf("%+v", c.Emails)
	}
	if want := []ui.ContactAddress{{Label: ui.NumberWork, Extended: "Floor 3", Street: "1 Main St", City: "Springfield", Region: "IL", Postcode: "62701", Country: "USA"}}; !reflect.DeepEqual(c.Addresses, want) {
		t.Errorf("%+v", c.Addresses)
	}
	if !slices.Equal(c.Groups, []string{"Family", "Friends, close", "Work"}) {
		t.Errorf("categories %q", c.Groups)
	}
	if c.Photo != PhotoKey(jpg) || !bytes.Equal(c.PhotoData, jpg) {
		t.Errorf("photo %q", c.Photo)
	}
	v4 := "BEGIN:VCARD\nVERSION:4.0\nUID:b\nFN:Bo\nPHOTO:data:image/jpeg;base64," + b64 + "\nEND:VCARD\n"
	if c, _, _ := ParseCard([]byte(v4)); c.Photo != PhotoKey(jpg) {
		t.Errorf("a data: URI: %q", c.Photo)
	}
	url := "BEGIN:VCARD\nVERSION:4.0\nUID:c\nFN:Cy\nPHOTO:https://example.com/cy.jpg\nEND:VCARD\n"
	if c, _, _ := ParseCard([]byte(url)); c.Photo != "" || c.PhotoData != nil {
		t.Errorf("a URL: %q", c.Photo)
	}
}

func TestNewCardExtras(t *testing.T) {
	jpg := picture(t, 30)
	in := ui.Contact{Name: "Ann", Number: "1001", Emails: []ui.ContactEmail{{Address: "a@x.test", Label: ui.NumberWork}, {Address: "b@x.test"}},
		Addresses: []ui.ContactAddress{{Label: ui.NumberHome, Street: "1; Main, St", City: "Town"}}, Groups: []string{"Family", "A, B"},
		Photo: PhotoKey(jpg), PhotoData: jpg}
	for _, v := range []string{"3.0", "4.0"} {
		raw := NewCard(in, "u1", v, patchAt)
		c, _, err := ParseCard(raw)
		want, _ := Clean(in, nil)
		want.Groups = in.Groups
		if err != nil || !reflect.DeepEqual(c, want) {
			t.Errorf("%s: %+v %v\n%s", v, c, err, raw)
		}
	}
}

// The phone's changes to e-mail and postal addresses rewrite just those
// lines; an unchanged photo stays as it came, byte for byte.
func TestPatchEmailsAddresses(t *testing.T) {
	jpg := picture(t, 120)
	var raw bytes.Buffer
	raw.WriteString("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:a\r\nFN:Ann\r\nTEL;TYPE=CELL:1001\r\n")
	raw.WriteString("item1.EMAIL;type=INTERNET:old@x.test\r\nitem1.X-ABLabel:_$!<Other>!$_\r\n")
	raw.WriteString("EMAIL;CHARSET=UTF-8;TYPE=HOME:keep@x.test\r\nADR;TYPE=HOME:;;1 Old Rd;Town;;;\r\n")
	fold(&raw, "PHOTO;ENCODING=b;TYPE=JPEG:"+base64.StdEncoding.EncodeToString(jpg))
	raw.WriteString("X-KARIO:keep me\r\nEND:VCARD\r\n")
	base, _, _ := ParseCard(raw.Bytes())
	now := base
	now.PhotoData = nil
	now.Emails = []ui.ContactEmail{{Address: "keep@x.test", Label: ui.NumberWork}, {Address: "new@x.test", Label: ui.NumberHome}}
	now.Addresses = []ui.ContactAddress{{Label: ui.NumberWork, Street: "2 New Rd", City: "Town"}}
	got, err := PatchCard(raw.Bytes(), now, false, patchAt)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	for _, want := range []string{"EMAIL;CHARSET=UTF-8;TYPE=INTERNET,WORK:keep@x.test\r\n", "EMAIL;TYPE=INTERNET,HOME:new@x.test\r\n",
		"ADR;TYPE=WORK:;;2 New Rd;Town;;;\r\n", "X-KARIO:keep me\r\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	for _, gone := range []string{"old@x.test", "item1.", "1 Old Rd"} {
		if strings.Contains(out, gone) {
			t.Errorf("still has %q:\n%s", gone, out)
		}
	}
	var photo bytes.Buffer
	fold(&photo, "PHOTO;ENCODING=b;TYPE=JPEG:"+base64.StdEncoding.EncodeToString(jpg))
	if !strings.Contains(out, photo.String()) {
		t.Errorf("the photo was written again:\n%s", out)
	}
	c, _, _ := ParseCard(got)
	if !reflect.DeepEqual(c.Emails, now.Emails) || !reflect.DeepEqual(c.Addresses, []ui.ContactAddress{{Label: ui.NumberWork, Street: "2 New Rd", City: "Town"}}) {
		t.Errorf("%+v %+v", c.Emails, c.Addresses)
	}
}

func TestPatchPhotoAndCategories(t *testing.T) {
	old, pic := picture(t, 10), picture(t, 200)
	raw := []byte("BEGIN:VCARD\nVERSION:4.0\nUID:a\nFN:Ann\nTEL:1001\nCATEGORIES:Family\nPHOTO:data:image/jpeg;base64," +
		base64.StdEncoding.EncodeToString(old) + "\nEND:VCARD\n")
	base, _, _ := ParseCard(raw)
	now := base
	now.Photo, now.PhotoData = PhotoKey(pic), pic
	now.Groups = []string{"family", "Work"}
	got, err := PatchCard(raw, now, true, patchAt)
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := ParseCard(got)
	if c.Photo != PhotoKey(pic) || !SameNames(c.Groups, []string{"Family", "Work"}) || strings.Count(string(got), "PHOTO") != 1 ||
		!strings.Contains(string(got), "PHOTO:data:image/jpeg;base64,") {
		t.Errorf("%+v\n%s", c, got)
	}
	if kept, _ := PatchCard(raw, now, false, patchAt); !strings.Contains(string(kept), "CATEGORIES:Family\n") {
		t.Errorf("without categories its CATEGORIES stay:\n%s", kept)
	}
	now.Photo, now.PhotoData = "", nil
	if got, _ := PatchCard(raw, now, false, patchAt); strings.Contains(string(got), "PHOTO") {
		t.Errorf("the photo removed:\n%s", got)
	}
	now.Photo = PhotoKey(pic)
	if _, err := PatchCard(raw, now, false, patchAt); err != ErrNoPhoto {
		t.Errorf("a new photo without its bytes: %v", err)
	}
}

// Group cards both ways: Apple's for 3.0, KIND and MEMBER for 4.0; a
// change keeps the way the card writes its members and what else it has.
func TestGroupCards(t *testing.T) {
	for _, v := range []string{"3.0", "4.0"} {
		raw := NewGroupCard("Family", "g1", []string{"a", "urn:uuid:B"}, v, patchAt)
		c, info, err := ParseCard(raw)
		if err != nil || !info.Group || c.Name != "Family" || !slices.Equal(info.Members, []string{"a", "b"}) {
			t.Errorf("%s: %+v %+v %v\n%s", v, c, info, err, raw)
		}
		if strings.Contains(string(raw), "urn:uuid:urn:uuid:") {
			t.Errorf("%s: %s", v, raw)
		}
	}
	apple := []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nPRODID:-//Apple Inc.//iOS//EN\r\nN:Team\r\nFN:Team\r\nX-ADDRESSBOOKSERVER-KIND:group\r\n" +
		"X-ADDRESSBOOKSERVER-MEMBER:urn:uuid:A\r\nX-ADDRESSBOOKSERVER-MEMBER:urn:uuid:B\r\nX-ADDRESSBOOKSERVER-MEMBER:mailto:x@y.test\r\nUID:g\r\nEND:VCARD\r\n")
	if got, err := PatchGroupCard(apple, "Team", []string{"a", "B"}, patchAt); err != nil || !bytes.Equal(got, apple) {
		t.Errorf("unchanged: %v\n%s", err, got)
	}
	got, err := PatchGroupCard(apple, "Team A", []string{"A", "c"}, patchAt)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	for _, want := range []string{"PRODID:-//Apple Inc.//iOS//EN\r\n", "N:Team A;;;;\r\n", "FN:Team A\r\n", "X-ADDRESSBOOKSERVER-MEMBER:urn:uuid:A\r\n",
		"X-ADDRESSBOOKSERVER-MEMBER:urn:uuid:c\r\n", "X-ADDRESSBOOKSERVER-MEMBER:mailto:x@y.test\r\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "urn:uuid:B") {
		t.Errorf("%s", out)
	}
	v4 := []byte("BEGIN:VCARD\nVERSION:4.0\nKIND:group\nFN:X\nUID:g2\nMEMBER:urn:uuid:a\nEND:VCARD\n")
	if got, _ := PatchGroupCard(v4, "X", []string{"a", "b"}, patchAt); !strings.Contains(string(got), "MEMBER:urn:uuid:a\nMEMBER:urn:uuid:b\n") {
		t.Errorf("%s", got)
	}
}
