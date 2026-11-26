package shell

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/ui"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: uint8((x ^ y) & 255), A: 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A photo the Contacts app was handed becomes a contact's: its middle
// square, small, a JPEG within the limit; a photo it was not handed is
// refused. The system draws it for apps with the contacts permission
// only, in an item's icon's place.
func TestContactPhoto(t *testing.T) {
	if !alone(t) {
		return
	}
	mgr := &App{M: &Manifest{ID: appContacts, Name: "Contacts", Permissions: []string{ui.PermContacts}, Granted: []string{ui.PermContacts}}, running: true}
	other := &App{M: &Manifest{ID: "com.example.notes", Name: "Notes", installed: true}, running: true}
	c, b := contactsRig(t, mgr, other)
	c.ev = make(chan func(), 64)
	c.media = openMedia(filepath.Join(c.cfg.StateDir, "media"), "")
	it, err := c.media.add(testJPEG(t, 1200, 800), "wide.jpg", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var r answer
	b.dispatchContacts(mgr, req(ui.CapContactsPhoto, ui.ContactPhotoArgs{Media: ui.MediaPrefix + it.ID}), r.reply)
	if r.err == nil || r.err.Error() != tr.T("No such photo") {
		t.Fatalf("a photo not handed over: %+v", r)
	}
	c.media.grant(mgr.M.ID, it)
	b.dispatchContacts(mgr, req(ui.CapContactsPhoto, ui.ContactPhotoArgs{Media: ui.MediaPrefix + it.ID}), r.reply)
	end := time.After(10 * time.Second)
	for r.n < 2 {
		select {
		case f := <-c.ev:
			f()
		case <-end:
			t.Fatal("no answer")
		}
	}
	res, ok := r.data.(ui.ContactPhotoRes)
	if r.err != nil || !ok {
		t.Fatalf("%+v", r)
	}
	raw, err := c.contactBook().Photo(res.Photo)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || format != "jpeg" || cfg.Width != ui.ContactPhotoSide || cfg.Height != ui.ContactPhotoSide || len(raw) > ui.ContactPhotoBytes {
		t.Fatalf("%s %dx%d, %d bytes, %v", format, cfg.Width, cfg.Height, len(raw), err)
	}
	b.dispatchContacts(mgr, req(ui.CapContactsSave, ui.Contact{Name: "Ann", Number: "1001", Photo: res.Photo}), r.reply)
	ann, _ := r.data.(ui.Contact)
	if r.err != nil || ann.Photo != res.Photo {
		t.Fatalf("%+v", r)
	}
	b.dispatchContacts(mgr, req(ui.CapContactsSave, ui.Contact{Name: "Bob", Number: "1002", Photo: "0123456789abcdef"}), r.reply)
	if r.err == nil || r.err.Error() != tr.T("No such photo") {
		t.Errorf("a photo the store has not: %+v", r)
	}

	c.fg = mgr
	if f := c.frameImage(ui.ContactPhotoPrefix+res.Photo, 0, false); f == nil || f.W != 40 || f.H != 40 {
		t.Errorf("the item's icon: %+v", f)
	}
	n := ui.Node{Kind: ui.KindItem, Title: "Ann", Subtitle: "1001", Icon: "A", Src: ui.ContactPhotoPrefix + res.Photo}
	if cv := c.drawItem(&n, 40, false); cv.H != 2 {
		t.Errorf("the photo went under the item: %d rows", cv.H)
	}
	c.fg = other
	if f := c.frameImage(ui.ContactPhotoPrefix+res.Photo, 0, false); f != nil {
		t.Error("an app without the contacts permission was shown the photo")
	}
}

// The export brings the photos along while the file stays one a phone
// reads back, and says when it leaves them out.
func TestExportPhotos(t *testing.T) {
	c, _ := contactsRig(t)
	book := c.contactBook()
	pic := testJPEG(t, 40, 40)
	book.Save(ui.Contact{Name: "Ann", Number: "1001", PhotoData: pic, Emails: []ui.ContactEmail{{Address: "ann@x.test"}}})
	data, photos := c.exportData(c.contacts())
	list, _, err := contacts.Parse(data)
	if err != nil || !photos || len(list) != 1 || list[0].Photo != contacts.PhotoKey(pic) || len(list[0].Emails) != 1 {
		t.Fatalf("%v %v %+v", err, photos, list)
	}
	noise := image.NewGray(image.Rect(0, 0, 700, 700))
	for i := range noise.Pix {
		noise.Pix[i] = uint8(i*7919 + i/700*104729)
	}
	var b bytes.Buffer
	jpeg.Encode(&b, noise, &jpeg.Options{Quality: 100})
	for i := 0; i < 12; i++ {
		if _, err := book.Save(ui.Contact{Name: "Big", Number: fmt.Sprint(2000 + i), PhotoData: b.Bytes()}); err != nil {
			t.Fatal(err)
		}
	}
	data, photos = c.exportData(c.contacts())
	if list, _, err := contacts.Parse(data); photos || err != nil || len(list) != 13 || list[0].Photo != "" {
		t.Fatalf("%d bytes (%d of photos), photos %v, %v", len(data), b.Len()*12, photos, err)
	}
}
