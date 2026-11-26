package contacts

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kario-os/kario/ui"
)

func parseFile(t *testing.T, name string) ([]ui.Contact, int) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	list, bad, err := Parse(raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return list, bad
}

func nums(c ui.Contact) string {
	var out []string
	for _, n := range c.Numbers {
		out = append(out, n.Label+"="+n.Number)
	}
	return strings.Join(out, ",")
}

func TestParseVCards(t *testing.T) {
	for _, c := range []struct {
		file string
		bad  int
		want []string // name | numbers | notes
	}{
		{"android21.vcf", 0, []string{
			"\u5f20\u4e09|mobile=138-0000-0001,home=010 6552 0000|\u5927\u5b66\u540c\u5b66\n\u7231\u559d\u8336",
			"\u5988\u5988|mobile=+86 139 0000 1111|",
		}},
		{"ios30.vcf", 0, []string{"Alice Smith|other=+86 139 1234 5678,work=+1 (650) 555-0100|Met at the conference, 2026\nLikes tea; andcoffee"}},
		{"v40.vcf", 0, []string{"Bob \u738b|mobile=+86-137-0000-1234,home=010-12345678|"}},
		{"gb2312.vcf", 0, []string{"\u674e\u56db|mobile=13800000002|", "\u738b\u4e94|work=13800000003|"}},
		{"odd.vcf", 1, []string{"Carol|other=+44 20 7946 0000|"}},
	} {
		list, bad := parseFile(t, c.file)
		var got []string
		for _, x := range list {
			got = append(got, x.Name+"|"+nums(x)+"|"+x.Notes)
			if x.Number != x.Numbers[0].Number {
				t.Errorf("%s: %s: Number %q is not the first number", c.file, x.Name, x.Number)
			}
		}
		if !reflect.DeepEqual(got, c.want) || bad != c.bad {
			t.Errorf("%s:\n got %q (bad %d)\nwant %q (bad %d)", c.file, got, bad, c.want, c.bad)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	if _, _, err := Parse([]byte("hello\r\nworld\r\n")); err != ErrNotVCard {
		t.Errorf("not a vCard: %v", err)
	}
	if _, _, err := Parse(make([]byte, MaxVCardBytes+1)); err != ErrTooBig {
		t.Errorf("too big: %v", err)
	}
	if list, bad, err := Parse([]byte("BEGIN:VCARD\nEND:VCARD\n")); err != nil || len(list) != 0 || bad != 1 {
		t.Errorf("an empty card: %v %d %v", list, bad, err)
	}
}

// A UTF-16 file (some desktop programs) and a UTF-8 byte order mark.
func TestParseByteOrderMarks(t *testing.T) {
	card := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:\u8d75\u516d\r\nTEL:13700001234\r\nEND:VCARD\r\n"
	var u16 []byte
	u16 = append(u16, 0xff, 0xfe)
	for _, r := range card {
		u16 = append(u16, byte(r), byte(r>>8))
	}
	for name, raw := range map[string][]byte{"utf-16": u16, "utf-8 bom": append([]byte{0xef, 0xbb, 0xbf}, card...)} {
		list, _, err := Parse(raw)
		if err != nil || len(list) != 1 || list[0].Name != "\u8d75\u516d" || list[0].Number != "13700001234" {
			t.Errorf("%s: %v %v", name, list, err)
		}
	}
}

func TestWriteRoundTrip(t *testing.T) {
	list := []ui.Contact{
		{Name: "\u5f20\u4e09", Numbers: []ui.ContactNumber{{Number: "13800000001", Label: ui.NumberMobile}, {Number: "010-6552 0000", Label: ui.NumberWork}},
			Notes: "line one\nline two; with, commas \\ and a backslash"},
		{Name: strings.Repeat("\u6b27\u9633", 20), Numbers: []ui.ContactNumber{{Number: "+86 139 0000 1111", Label: ui.NumberHome}, {Number: "*#06#", Label: ui.NumberOther}}},
		{Name: "Alice", Number: "12345"},
	}
	raw := Write(list)
	for _, l := range strings.Split(string(raw), "\r\n") {
		if len(l) > 75 {
			t.Errorf("a line of %d bytes: %q", len(l), l)
		}
	}
	got, bad, err := Parse(raw)
	if err != nil || bad != 0 || len(got) != len(list) {
		t.Fatalf("%v %d %v", got, bad, err)
	}
	for i := range list {
		want, _ := Clean(list[i], nil)
		if !reflect.DeepEqual(got[i], want) {
			t.Errorf("contact %d:\n got %+v\nwant %+v", i, got[i], want)
		}
	}
	if !bytes.HasPrefix(raw, []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:")) {
		t.Errorf("not vCard 3.0: %q", raw[:40])
	}
}

func TestIsVCard(t *testing.T) {
	for _, c := range []struct {
		name, mime string
		want       bool
	}{
		{"Zhang San.vcf", "", true}, {"x.VCF", "", true}, {"card", "text/x-vcard", true}, {"c", "text/vcard; charset=utf-8", true},
		{"notes.txt", "text/plain", false}, {"vcf", "", false},
	} {
		if IsVCard(c.name, c.mime) != c.want {
			t.Errorf("%q %q", c.name, c.mime)
		}
	}
}

// An export keeps e-mail and postal addresses, and the photos given; read
// again it is the same.
func TestWriteExtras(t *testing.T) {
	jpg := picture(t, 60)
	list := []ui.Contact{{Name: "Ann", Number: "1001", Emails: []ui.ContactEmail{{Address: "ann@x.test", Label: ui.NumberHome}},
		Addresses: []ui.ContactAddress{{Label: ui.NumberWork, Street: "1 Main St", City: "Town", Postcode: "100000", Country: "China"}},
		Photo:     PhotoKey(jpg), PhotoData: jpg}, {Name: "Bob", Number: "1002"}}
	raw := Write(list)
	got, bad, err := Parse(raw)
	want := make([]ui.Contact, len(list))
	for i, c := range list {
		want[i], _ = Clean(c, nil)
	}
	if err != nil || bad != 0 || !reflect.DeepEqual(got, want) {
		t.Errorf("%+v %d %v\n%s", got, bad, err, raw)
	}
	if !bytes.Contains(raw, []byte("PHOTO;ENCODING=b;TYPE=JPEG:")) || !bytes.Contains(raw, []byte("EMAIL;TYPE=INTERNET,HOME:ann@x.test\r\n")) {
		t.Errorf("%s", raw)
	}
}
