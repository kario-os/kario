package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// The labels of e-mail and postal addresses.
var places = []string{ui.NumberHome, ui.NumberWork, ui.NumberOther}

func placeName(l string) string {
	switch l {
	case ui.NumberHome:
		return tr.T("Home")
	case ui.NumberWork:
		return tr.T("Work")
	}
	return tr.T("Other")
}

// addressLine is a postal address on one line.
func addressLine(a ui.ContactAddress) string {
	var parts []string
	for _, p := range []string{a.POBox, a.Extended, a.Street, a.City, a.Region, a.Postcode, a.Country} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, tr.T(", "))
}

// photoSrc is a contact's photo as an item's src ("" without one): the
// system draws it in the icon's place.
func photoSrc(c ui.Contact) string {
	if c.Photo == "" {
		return ""
	}
	return ui.ContactPhotoPrefix + c.Photo
}

// newBook is where a new contact goes: its choice, or the default.
func (b *book) newBook(choice string) string {
	if choice != "" {
		return choice
	}
	for _, x := range b.books {
		if x.Default {
			return x.ID
		}
	}
	return ui.BookPhone
}

// bookChoices are where a new contact may go: the books (not the
// read-only ones), the phone.
func (b *book) bookChoices() []string {
	var out []string
	for _, x := range b.books {
		if !x.ReadOnly {
			out = append(out, x.ID)
		}
	}
	return append(out, ui.BookPhone)
}

// moveChoices are where a contact may move to: another book of
// bookChoices, or the phone when it is in a book.
func (b *book) moveChoices(c ui.Contact) []string {
	if c.ReadOnly || len(b.books) == 0 {
		return nil
	}
	var out []string
	for _, id := range b.bookChoices() {
		if id != c.Book && !(id == ui.BookPhone && c.Book == "") {
			out = append(out, id)
		}
	}
	return out
}

// bookName names an address book ("" and ui.BookPhone: the phone).
func (b *book) bookName(id string) string {
	for _, x := range b.books {
		if x.ID == id {
			return x.Name + " · " + x.Account
		}
	}
	return tr.T("On the phone only")
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// dateText is a birthday or an anniversary as the page shows it: the
// date field's form, without the year when it has none.
func dateText(d string) string {
	if strings.HasPrefix(d, "--") {
		return d[2:]
	}
	return d
}

// yearsText is how many years a date with a year is ago, after a dot:
// as an age for a birthday.
func yearsText(d string, birthday bool) string {
	y, m, day, ok := ui.ContactDateParts(d)
	if !ok || y == 0 {
		return ""
	}
	now := sdk.Now()
	n := now.Year() - y
	if int(now.Month()) < m || int(now.Month()) == m && now.Day() < day {
		n--
	}
	switch {
	case n < 0:
		return ""
	case birthday:
		return " · " + tr.T("%d years old", n)
	}
	return " · " + tr.T("%d years", n)
}

// fieldDate is a contact's date as its date field holds it: a date
// without its year in this year (or the next leap year for 29 February),
// noYear set.
func fieldDate(d string, now time.Time) (value string, noYear bool) {
	y, m, day, ok := ui.ContactDateParts(d)
	switch {
	case !ok:
		return "", false
	case y != 0:
		return d, false
	}
	y = now.Year()
	for m == 2 && day == 29 && (y%4 != 0 || y%100 == 0 && y%400 != 0) {
		y++
	}
	return fmt.Sprintf("%04d-%02d-%02d", y, m, day), true
}

// contactDate is what a date field holds as a contact's date ("" none):
// without its year when noYear.
func contactDate(v string, noYear bool) string {
	v = strings.TrimSpace(v)
	if _, _, _, ok := ui.ContactDateParts(v); !ok || strings.HasPrefix(v, "--") {
		return ""
	}
	if noYear {
		return "--" + v[5:]
	}
	return v
}

// pickPhoto has the user choose a photo in the system's picker and makes
// it a contact's (contacts.photo); then has its Photo.
func (b *book) pickPhoto(then func(photo string)) {
	go func() {
		var it ui.MediaItem
		_, err := b.app.CallFile(ui.CapMediaPick, ui.MediaArgs{Kind: ui.MediaImage}, &it, func(f *os.File) (int64, error) { return 0, nil })
		var res ui.ContactPhotoRes
		if err == nil {
			err = b.app.Call(ui.CapContactsPhoto, ui.ContactPhotoArgs{Media: it.ID}, &res)
		}
		b.app.Post(func() {
			if err != nil {
				if !ui.Cancelled(err) {
					b.app.Toast(tr.T("No photo: %v", err))
				}
				return
			}
			then(res.Photo)
		})
	}()
}
