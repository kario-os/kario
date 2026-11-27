package contacts

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unicode/utf8"

	"github.com/kario-os/kario/ui"
)

// FuzzParse is a vCard file from anywhere (Files, a Bluetooth push): it
// parses or is refused, what it gives is within the store's limits, and
// written out again it reads back the same.
func FuzzParse(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.vcf"))
	for _, name := range files {
		if raw, err := os.ReadFile(name); err == nil && len(raw) < 8<<10 {
			f.Add(raw)
		}
	}
	f.Add([]byte("BEGIN:VCARD\nN;ENCODING=QUOTED-PRINTABLE:=\n=E5=BC=A0=\nEND:VCARD"))
	f.Add([]byte("BEGIN:VCARD\r\nFN;CHARSET=BIG5;ENCODING=BASE64:pKSk5g==\r\nTEL;VALUE=uri:tel:+1;ext=2\r\nEND:VCARD"))
	f.Fuzz(func(t *testing.T, data []byte) {
		list, bad, err := Parse(data)
		if err != nil {
			return
		}
		if bad < 0 || len(list) > MaxContacts {
			t.Fatalf("%d contacts, %d bad", len(list), bad)
		}
		for _, c := range list {
			if c.Name == "" || !utf8.ValidString(c.Name) || utf8.RuneCountInString(c.Name) > MaxName ||
				!utf8.ValidString(c.Notes) || utf8.RuneCountInString(c.Notes) > MaxNotes || len(c.Numbers) > MaxNumbers {
				t.Fatalf("outside the limits: %+v", c)
			}
			for _, n := range c.Numbers {
				if n.Number == "" || len(n.Number) > MaxNumber {
					t.Fatalf("a number outside the limits: %+v", c)
				}
			}
			if len(c.Numbers) > 0 && c.Number != c.Numbers[0].Number {
				t.Fatalf("Number is not the first: %+v", c)
			}
		}
		again, bad, err := Parse(Write(list))
		if len(list) == 0 {
			return
		}
		if err != nil || bad != 0 || !reflect.DeepEqual(again, list) {
			t.Fatalf("written and read again:\n%+v\n%+v (bad %d, %v)", list, again, bad, err)
		}
	})
}

// FuzzPatchCard is a card from a server patched to show a contact: it
// never comes back changed when the contact is what the card shows, and
// patched it reads back with the contact's name and notes.
func FuzzPatchCard(f *testing.F) {
	f.Add([]byte(iosCard()), "Alice Jones", "+1 650 555 0199", "notes")
	f.Add([]byte("BEGIN:VCARD\nVERSION:4.0\nUID:u\nFN:A\nTEL;VALUE=uri:tel:+1-555\nEND:VCARD\n"), "B", "123", "")
	f.Add([]byte("BEGIN:VCARD\r\nVERSION:2.1\r\nN;ENCODING=QUOTED-PRINTABLE:=E5=BC=\r\n=A0\r\nAGENT:\r\nBEGIN:VCARD\r\nFN:Inner\r\nEND:VCARD\r\nEND:VCARD\r\n"), "Outer", "", "x")
	f.Fuzz(func(t *testing.T, data []byte, name, number, notes string) {
		if len(data) > 64<<10 {
			return
		}
		base, _, err := ParseCard(data)
		if err != nil && err != ErrEmpty {
			return
		}
		if err == nil {
			same, perr := PatchCard(data, base, true, patchAt)
			if perr != nil || !bytes.Equal(same, data) {
				t.Fatalf("the contact the card shows changed it: %v\n%q\n%q", perr, data, same)
			}
		}
		now, err := Clean(ui.Contact{Name: name, Number: number, Notes: notes}, nil)
		if err != nil {
			return
		}
		out, err := PatchCard(data, now, false, patchAt)
		if err != nil {
			return
		}
		got, _, err := ParseCard(out)
		if err != nil || got.Name != now.Name || got.Notes != now.Notes {
			t.Fatalf("patched to %+v, read back %+v (%v)\n%q\n%q", now, got, err, data, out)
		}
	})
}
