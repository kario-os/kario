// Package phonenum reads, compares and writes phone numbers the way
// libphonenumber does, from a compact table (metadata.json): each region's
// country calling code, mobile country codes, international and national
// (trunk) prefixes, the lengths of its national significant numbers and a
// few display patterns. A number written nationally is read in a region
// (the SIM's country, or the one the user chose); one with "+" or an
// international prefix carries its own. Numbers compare by Key: E.164
// ("+8613800138000") when the length is one the country uses, else the
// digits as written (short codes such as 10086 or 911), a sender with
// letters lower-cased. Format is the number as a person reads it:
// national for the region's own numbers, international for the others;
// Display applies it to numbers written with a country code.
package phonenum

import (
	_ "embed"
	"encoding/json"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

//go:embed metadata.json
var metadataJSON []byte

// Region is one region's numbering plan, as metadata.json gives it.
type Region struct {
	Code string   `json:"region"`
	CC   string   `json:"cc"`
	MCC  []string `json:"mcc"`
	// Intl: the prefixes dialled before a country code from the region.
	Intl []string `json:"intl"`
	// National: the prefixes written before a national significant number
	// inside the country, the trunk prefix first (shown by Format).
	National []string `json:"national"`
	Lengths  []int    `json:"lengths"`
	// Mobile15: Argentina's mobile numbers are written nationally with
	// "15" after the area code and internationally with "9" before it.
	Mobile15 bool     `json:"mobile15,omitempty"`
	Formats  []format `json:"formats"`
}

// format is a display pattern for national significant numbers that start
// with one of Lead (comma separated; "" any) and are as long as the
// pattern's digits: x a digit, _ a digit left out, anything else as it is.
type format struct {
	Lead string `json:"lead"`
	Nat  string `json:"nat"`
	Intl string `json:"intl"`
}

type tables struct {
	Regions []Region `json:"regions"`
	byCode  map[string]*Region
	byCC    map[string]*Region // the first region of each country code
	byMCC   map[string]string
}

var data = sync.OnceValue(func() *tables {
	t := &tables{byCode: map[string]*Region{}, byCC: map[string]*Region{}, byMCC: map[string]string{}}
	if err := json.Unmarshal(metadataJSON, t); err != nil {
		panic("phonenum: metadata.json: " + err.Error())
	}
	for i := range t.Regions {
		r := &t.Regions[i]
		t.byCode[r.Code] = r
		if t.byCC[r.CC] == nil {
			t.byCC[r.CC] = r
		}
		for _, m := range r.MCC {
			t.byMCC[m] = r.Code
		}
		// Longest first: "0011" before "00".
		sort.SliceStable(r.Intl, func(i, j int) bool { return len(r.Intl[i]) > len(r.Intl[j]) })
	}
	return t
})

// Regions lists the regions the table covers.
func Regions() []string {
	var out []string
	for _, r := range data().Regions {
		out = append(out, r.Code)
	}
	return out
}

// Lookup is a region's plan (ISO 3166 code), nil when the table lacks it.
func Lookup(code string) *Region { return data().byCode[strings.ToUpper(strings.TrimSpace(code))] }

// RegionOfMCC is the region of a mobile country code ("334": MX), "".
func RegionOfMCC(mcc string) string {
	if len(mcc) < 3 {
		return ""
	}
	return data().byMCC[mcc[:3]]
}

var region atomic.Pointer[string]

// DefaultRegion: a phone without a SIM, a region setting or PHONE_REGION.
const DefaultRegion = "CN"

// SetDefault sets the region numbers written nationally are read in by
// this process (the shell's Settings › Region, what phone-smsd hears).
func SetDefault(code string) {
	code = strings.ToUpper(strings.TrimSpace(code))
	region.Store(&code)
}

// Default is the region SetDefault set, else PHONE_REGION, else
// DefaultRegion.
func Default() string {
	if r := region.Load(); r != nil && *r != "" {
		return *r
	}
	if r := strings.ToUpper(strings.TrimSpace(os.Getenv("PHONE_REGION"))); r != "" {
		return r
	}
	return DefaultRegion
}

// Number is a number as read: its country calling code ("" for one written
// nationally in a region the table lacks, or with letters), its national
// significant number, whether it was written with a country code, and
// whether its length is one the country uses. Alpha is a sender with
// letters (a company's name), lower-cased; the rest is then empty.
type Number struct {
	CC    string
	NSN   string
	Intl  bool
	Valid bool
	Alpha string
}

// Two-digit country codes; every other one starting with 2-9 has three,
// 1 and 7 have one (the codes are prefix-free, ITU-T E.164 Annex).
var cc2 = []string{"20", "27", "30", "31", "32", "33", "34", "36", "39", "40", "41", "43", "44", "45", "46", "47", "48", "49",
	"51", "52", "53", "54", "55", "56", "57", "58", "60", "61", "62", "63", "64", "65", "66", "81", "82", "84", "86",
	"90", "91", "92", "93", "94", "95", "98"}

func splitCC(digits string) (cc, rest string) {
	switch {
	case digits == "":
		return "", ""
	case digits[0] == '1' || digits[0] == '7':
		return digits[:1], digits[1:]
	case digits[0] == '0':
		return "", digits
	case len(digits) >= 2 && slices.Contains(cc2, digits[:2]):
		return digits[:2], digits[2:]
	case len(digits) >= 3:
		return digits[:3], digits[3:]
	}
	return "", digits
}

// maxDigits: E.164 allows 15; anything longer is not a phone number.
const maxDigits = 17

// clean keeps the digits and a leading plus; "(0)" after a country code
// ("+44 (0)20 ...") goes. ok is false for text with letters.
func clean(raw string) (digits string, plus, ok bool) {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "00") {
		s = strings.Replace(s, "(0)", "", 1)
	}
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			if b.Len() < maxDigits+1 {
				b.WriteRune(r)
			}
		case r == '+' && i == 0:
			plus = true
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')' || r == '/' || r == '\u00a0':
		default:
			return "", false, false
		}
	}
	return b.String(), plus, true
}

func (r *Region) valid(nsn string) bool { return r != nil && slices.Contains(r.Lengths, len(nsn)) }

// national takes a national prefix off when what is left has a length the
// country uses and does not start with the prefix again (national numbers
// do not start with their country's trunk prefix, so "010 1234 5678" is
// not an 11-digit number, and a key reads back as itself); with Mobile15,
// "15" after the area code becomes the "9" in front.
func (r *Region) national(d string) string {
	for _, p := range r.National {
		if rest, ok := strings.CutPrefix(d, p); ok && r.valid(rest) && !strings.HasPrefix(rest, p) {
			return rest
		}
	}
	if r.valid(d) {
		return d
	}
	for _, p := range r.National {
		if rest, ok := strings.CutPrefix(d, p); ok {
			if m := r.mobile15(rest); m != "" {
				return m
			}
		}
	}
	if m := r.mobile15(d); m != "" {
		return m
	}
	return d
}

func (r *Region) mobile15(d string) string {
	if !r.Mobile15 || len(d) != 12 {
		return ""
	}
	order := []int{3, 4, 2}
	if strings.HasPrefix(d, "11") {
		order = []int{2, 3, 4}
	}
	for _, n := range order {
		if d[n:n+2] == "15" {
			return "9" + d[:n] + d[n+2:]
		}
	}
	return ""
}

// Parse reads a number written in a region (ISO 3166 code).
func Parse(raw, region string) Number {
	d, plus, ok := clean(raw)
	if !ok {
		return Number{Alpha: strings.ToLower(strings.TrimSpace(raw))}
	}
	if len(d) > maxDigits {
		return Number{NSN: d}
	}
	home := Lookup(region)
	intl := plus
	if !intl && home != nil {
		for _, p := range home.Intl {
			if rest, ok := strings.CutPrefix(d, p); ok && len(rest) >= 7 {
				d, intl = rest, true
				break
			}
		}
	}
	if !intl && home == nil && len(d) >= 9 && strings.HasPrefix(d, "00") {
		d, intl = d[2:], true
	}
	if intl {
		cc, rest := splitCC(d)
		if cc == "" {
			return Number{NSN: d, Intl: true}
		}
		r := data().byCC[cc]
		if r == nil {
			return Number{CC: cc, NSN: rest, Intl: true, Valid: len(rest) >= 6 && len(cc)+len(rest) <= 15}
		}
		nsn := r.national(rest)
		return Number{CC: cc, NSN: nsn, Intl: true, Valid: r.valid(nsn)}
	}
	if home == nil {
		return Number{NSN: d}
	}
	nsn := home.national(d)
	return Number{CC: home.CC, NSN: nsn, Valid: home.valid(nsn)}
}

// E164 is the number as +<country code><national number> when its length
// is one the country uses, else "".
func (n Number) E164() string {
	if !n.Valid || n.CC == "" {
		return ""
	}
	return "+" + n.CC + n.NSN
}

// Key is how numbers are compared (threads, contacts, the block list):
// E.164 when the number is valid; a number of the region's own country
// that is not (a short code) as its digits, so "10086" and "+8610086"
// are one; another country's as "+" and its digits; letters lower-cased.
func Key(raw, region string) string {
	n := Parse(raw, region)
	home := Lookup(region)
	switch {
	case n.Alpha != "":
		return n.Alpha
	case n.Valid:
		return n.E164()
	case n.Intl && n.CC == "":
		return "+" + n.NSN
	case n.Intl && (home == nil || home.CC != n.CC):
		return "+" + n.CC + n.NSN
	case n.Intl:
		// Digits that would read as something else written alone keep
		// their country code.
		if m := Parse(n.NSN, region); m.Intl || m.Valid || m.NSN != n.NSN {
			return "+" + n.CC + n.NSN
		}
	}
	return n.NSN
}

// Format is the number as the region's people write it: their own numbers
// in the national form, the others' as +<country code> and the groups; one
// whose length the country does not use, as written.
func Format(raw, region string) string {
	n := Parse(raw, region)
	if n.Alpha != "" || !n.Valid {
		return strings.TrimSpace(raw)
	}
	r := data().byCC[n.CC]
	if home := Lookup(region); home != nil && home.CC == n.CC {
		r = home
		for _, f := range r.Formats {
			if s, ok := f.apply(n.NSN, f.Nat); ok {
				return s
			}
		}
		trunk := ""
		if len(r.National) > 0 {
			trunk = r.National[0]
		}
		return trunk + groups(n.NSN)
	}
	if r != nil {
		for _, f := range r.Formats {
			if s, ok := f.apply(n.NSN, f.Intl); ok {
				return "+" + n.CC + " " + s
			}
		}
	}
	return "+" + n.CC + " " + groups(n.NSN)
}

// Display is a number as the phone shows it: one written with a country
// code (as networks send senders) in the region's way, Format's; one
// written nationally, or not a valid number, as it was written.
func Display(raw, region string) string {
	if n := Parse(raw, region); n.Intl && n.Valid {
		return Format(raw, region)
	}
	return strings.TrimSpace(raw)
}

func (f format) apply(nsn, pattern string) (string, bool) {
	if strings.Count(pattern, "x")+strings.Count(pattern, "_") != len(nsn) {
		return "", false
	}
	if f.Lead != "" && !slices.ContainsFunc(strings.Split(f.Lead, ","), func(p string) bool { return strings.HasPrefix(nsn, p) }) {
		return "", false
	}
	var b strings.Builder
	i := 0
	for _, c := range pattern {
		switch c {
		case 'x':
			b.WriteByte(nsn[i])
			i++
		case '_':
			i++
		default:
			b.WriteRune(c)
		}
	}
	return b.String(), true
}

// groups splits digits a pattern does not cover: the last four together,
// threes before them, what is left first.
func groups(d string) string {
	if len(d) <= 4 {
		return d
	}
	parts := []string{d[len(d)-4:]}
	head := d[:len(d)-4]
	for len(head) > 3 {
		parts = append([]string{head[len(head)-3:]}, parts...)
		head = head[:len(head)-3]
	}
	if head != "" {
		parts = append([]string{head}, parts...)
	}
	return strings.Join(parts, " ")
}

// Match: two numbers are the same, read in the region.
func Match(a, b, region string) bool {
	ka := Key(a, region)
	return ka != "" && ka == Key(b, region)
}
