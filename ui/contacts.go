package ui

import (
	"strings"

	"github.com/kario-os/kario/phonenum"
)

// ContactGroup is a group of contacts (contacts.groups): its members are
// contact IDs.
type ContactGroup struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// ContactGroupArgs: contacts.group's Op is ContactGroupCreate (Name),
// ContactGroupRename (ID, Name), ContactGroupDelete (ID: the contacts
// stay), ContactGroupAdd or ContactGroupRemove (ID, Contacts); it answers
// the group (ContactGroupDelete: the ID alone).
type ContactGroupArgs struct {
	Op       string   `json:"op"`
	ID       string   `json:"id,omitempty"`
	Name     string   `json:"name,omitempty"`
	Contacts []string `json:"contacts,omitempty"`
}

const (
	ContactGroupCreate = "create"
	ContactGroupRename = "rename"
	ContactGroupDelete = "delete"
	ContactGroupAdd    = "add"
	ContactGroupRemove = "remove"
)

// ContactMergeArgs: contacts.merge keeps the first contact with the names,
// numbers, e-mail and postal addresses, notes and groups of the others
// (its photo, or the first of theirs), and deletes the others; it answers
// the contact kept.
type ContactMergeArgs struct {
	IDs []string `json:"ids"`
}

// ContactPhotoArgs: contacts.photo takes a photo of the library the app
// may see (Media, its ID as media.pick handed it over) and answers
// the Photo a contact saved with it shows: the photo's middle square,
// at most ContactPhotoSide pixels, as a JPEG of at most
// ContactPhotoBytes.
type ContactPhotoArgs struct {
	Media string `json:"media"`
}

type ContactPhotoRes struct {
	Photo string `json:"photo"`
}

const (
	ContactPhotoSide  = 256
	ContactPhotoBytes = 48 << 10
)

// ContactBook is an address book a contacts account syncs (contacts.books):
// its ID (a Contact's Book), its name on the server, the account's name
// (the Kario account, or the server's host), and whether new contacts go
// to it. No book is the default when they stay on the phone. A ReadOnly
// book's contacts come from the server and are not changed on the phone;
// no contact goes into it.
type ContactBook struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Account  string `json:"account"`
	Default  bool   `json:"default,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// ContactDate is a contact's birthday and anniversary as contacts.dates
// gives them to the system's calendar app (Contact's formats): only
// contacts with one.
type ContactDate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Birthday    string `json:"birthday,omitempty"`
	Anniversary string `json:"anniversary,omitempty"`
}

// BookPhone, as a new contact's Book, keeps it on the phone alone.
const BookPhone = "phone"

// ContactDateParts reads a Contact's Birthday or Anniversary: its year (0
// for "--MM-DD", a date without one), month and day; ok is false for
// anything else (29 February is a date without a year, or in a leap year).
func ContactDateParts(s string) (y, m, d int, ok bool) {
	num := func(t string) int {
		n := 0
		for _, c := range t {
			if c < '0' || c > '9' {
				return -1
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	switch {
	case len(s) == 10 && s[4] == '-' && s[7] == '-':
		y, m, d = num(s[:4]), num(s[5:7]), num(s[8:])
		if y < 1 {
			return 0, 0, 0, false
		}
	case len(s) == 7 && s[:2] == "--" && s[4] == '-':
		m, d = num(s[2:4]), num(s[5:])
	default:
		return 0, 0, 0, false
	}
	days := [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if m < 1 || m > 12 || d < 1 || d > days[m] {
		return 0, 0, 0, false
	}
	if m == 2 && d == 29 && y != 0 && (y%4 != 0 || y%100 == 0 && y%400 != 0) {
		return 0, 0, 0, false
	}
	return y, m, d, true
}

// NumbersOf is a contact's numbers: Numbers, or Number alone in a contact
// written before numbers had labels.
func NumbersOf(c Contact) []ContactNumber {
	if len(c.Numbers) > 0 {
		return c.Numbers
	}
	if c.Number != "" {
		return []ContactNumber{{Number: c.Number, Label: NumberMobile}}
	}
	return nil
}

// NumberKey is how numbers are compared, as phone-smsd compares them
// (phonenum.Key): E.164 when the number has a length its country uses,
// read in the process's region (phonenum.Default: the shell's region,
// PHONE_REGION in an app), so "138 0013 8000", "+86 138-0013-8000" and
// "0086 13800138000" are one in China; a short code as its digits; a
// "number" with letters (a company's name) lower-cased.
func NumberKey(number string) string { return phonenum.Key(number, phonenum.Default()) }

// NumberText is a number as the phone shows it (phonenum.Display): one
// written with a country code in the national form for the region's own
// numbers, grouped after +<country code> for the others; one written
// nationally, a short code or a name as written.
func NumberText(number string) string { return phonenum.Display(number, phonenum.Default()) }

var keypadDigit = map[rune]byte{}

func init() {
	for d, letters := range []string{"", "", "abc", "def", "ghi", "jkl", "mno", "pqrs", "tuv", "wxyz"} {
		for _, r := range letters {
			keypadDigit[r] = byte('0' + d)
		}
	}
}

// KeypadDigits spells letters as the 9-key keypad's digits ("zs" is "97");
// other characters are left out.
func KeypadDigits(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if d, ok := keypadDigit[r]; ok {
			b.WriteByte(d)
		}
	}
	return b.String()
}

// ContactDuplicates are the sets of contacts that look like one person:
// they share a number (as NumberKey compares them), or have the same name
// in any case (a name that is only the contact's number does not count).
// Each set holds IDs in the list's order, the sets in the order of their
// first contact.
func ContactDuplicates(list []Contact) [][]string {
	parent := make([]int, len(list))
	for i := range parent {
		parent[i] = i
	}
	var root func(int) int
	root = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	join := func(a, b int) {
		if ra, rb := root(a), root(b); ra != rb {
			parent[max(ra, rb)] = min(ra, rb)
		}
	}
	first := map[string]int{}
	for i, c := range list {
		keys := []string{}
		if n := strings.ToLower(strings.TrimSpace(c.Name)); n != "" && c.Name != c.Number {
			keys = append(keys, "name:"+n)
		}
		for _, n := range NumbersOf(c) {
			if k := NumberKey(n.Number); k != "" {
				keys = append(keys, "number:"+k)
			}
		}
		for _, k := range keys {
			if j, ok := first[k]; ok {
				join(i, j)
			} else {
				first[k] = i
			}
		}
	}
	sets := map[int][]string{}
	var order []int
	for i, c := range list {
		r := root(i)
		if _, ok := sets[r]; !ok {
			order = append(order, r)
		}
		sets[r] = append(sets[r], c.ID)
	}
	var out [][]string
	for _, r := range order {
		if len(sets[r]) > 1 {
			out = append(out, sets[r])
		}
	}
	return out
}

// ContactMatches is the search of every contacts list (smart dialling in
// Phone, the search of Contacts): what was typed is in the name, in a
// number, in an e-mail address (unless it is digits alone), or starts the
// pinyin, its initials, or the rest of them from any syllable on ("zs",
// "zhangs", "san"); digits also match those spelt on the 9-key keypad
// ("97", "726").
func ContactMatches(c Contact, typed string) bool {
	q := strings.ToLower(strings.TrimSpace(typed))
	if q == "" {
		return true
	}
	if strings.Contains(strings.ToLower(c.Name), q) {
		return true
	}
	digits := strings.Trim(q, "0123456789+*#") == ""
	if !digits {
		for _, e := range c.Emails {
			if strings.Contains(strings.ToLower(e.Address), q) {
				return true
			}
		}
	}
	if digits {
		for _, n := range NumbersOf(c) {
			if strings.Contains(n.Number, q) || strings.Contains(NumberKey(n.Number), q) {
				return true
			}
		}
	}
	syl := strings.Fields(c.Pinyin)
	for i := range syl {
		full, initials := strings.Join(syl[i:], ""), ""
		for _, s := range syl[i:] {
			initials += s[:1]
		}
		if digits {
			full, initials = KeypadDigits(full), KeypadDigits(initials)
		}
		if strings.HasPrefix(full, q) || strings.HasPrefix(initials, q) {
			return true
		}
	}
	return false
}
