package ui

import (
	"reflect"
	"strings"
	"testing"
)

func TestContactMatches(t *testing.T) {
	zhang := Contact{Name: "\u5f20\u4e09", Number: "13800000001", Pinyin: "zhang san",
		Numbers: []ContactNumber{{Number: "138 0000 0001", Label: NumberMobile}, {Number: "010-6552 0000", Label: NumberWork}}}
	alice := Contact{Name: "Alice Smith", Pinyin: "alice smith", Numbers: []ContactNumber{{Number: "+86 139 1234 5678"}},
		Emails: []ContactEmail{{Address: "Alice.S@Example.com"}, {Address: "a42@work.test"}}}
	old := Contact{Name: "\u4e2d\u56fd\u79fb\u52a8", Number: "10086", Pinyin: "zhong guo yi dong"}
	for _, c := range []struct {
		c     Contact
		typed string
		want  bool
	}{
		{zhang, "", true},
		{alice, "alice.s@", true},
		{alice, "work.test", true},
		{alice, "42", false},
		{zhang, "\u5f20", true},
		{zhang, "zs", true},
		{zhang, "ZhangS", true},
		{zhang, "san", true},
		{zhang, "97", true},
		{zhang, "94264", true},
		{zhang, "726", true},
		{zhang, "65520", true},
		{zhang, "zx", false},
		{zhang, "98", false},
		{alice, "ali", true},
		{alice, "smi", true},
		{alice, "as", true},
		{alice, "25423", true},
		{alice, "13912345678", true},
		{alice, "bob", false},
		{old, "0086", true},
		{old, "zgyd", true},
		{old, "yd", true},
		{old, "*#06#", false},
	} {
		if got := ContactMatches(c.c, c.typed); got != c.want {
			t.Errorf("%s / %q: %v, want %v", c.c.Name, c.typed, got, c.want)
		}
	}
}

func TestNumberKey(t *testing.T) {
	t.Setenv("PHONE_REGION", "")
	for in, want := range map[string]string{
		"+86 138-0000-0001": "+8613800000001",
		"13800000001":       "+8613800000001",
		"0086 10086":        "10086",
		"(010) 6552.0000":   "+861065520000",
		"+1 650 555 0100":   "+16505550100",
		"+8612":             "12",
		"Kario":             "kario",
	} {
		if got := NumberKey(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	t.Setenv("PHONE_REGION", "ES")
	if NumberKey("612 34 56 78") != NumberKey("+34 612345678") || NumberText("+34612345678") != "612 34 56 78" || NumberText("612345678") != "612345678" {
		t.Errorf("in Spain: %q, %q", NumberKey("612 34 56 78"), NumberText("+34612345678"))
	}
}

func TestBlockList(t *testing.T) {
	t.Setenv("PHONE_REGION", "")
	b := BlockList{Numbers: []string{BlockEntry("+86 139 0000 0001", "CN"), BlockEntry("1065*", "CN"), BlockEntry("0571*", "CN"), BlockEntry("+44*", "CN")}}
	if !reflect.DeepEqual(b.Numbers, []string{"+8613900000001", "1065*", "0571*", "+44*"}) {
		t.Fatalf("entries %v", b.Numbers)
	}
	contacts := func(n string) bool { return NumberKey(n) == "+8613800000001" }
	for number, want := range map[string]string{
		"13900000001":       BlockedNumber,
		"106555551234":      BlockedPrefix,
		"+86 571 8888 7777": BlockedPrefix,
		"+447700900123":     BlockedPrefix,
		"13800000001":       "",
		"13700000001":       "",
		"":                  "",
		"Private":           "",
	} {
		if got := b.Blocked(number, "CN", contacts); got != want {
			t.Errorf("%q: %q, want %q", number, got, want)
		}
	}
	b.Hidden, b.Unknown = true, true
	for number, want := range map[string]string{"": BlockedHidden, "Anonymous": BlockedHidden, "13700000001": BlockedUnknown, "13800000001": "", "ICBC": BlockedUnknown} {
		if got := b.Blocked(number, "CN", contacts); got != want {
			t.Errorf("%q: %q, want %q", number, got, want)
		}
	}
	if b.Blocked("13700000001", "CN", nil) != "" {
		t.Error("without the contacts, nothing is unknown")
	}
	if BlockEntry("*", "CN") != "" || BlockEntry("+*", "CN") != "" {
		t.Error("an empty prefix blocks nothing")
	}
}

func TestNumbersOf(t *testing.T) {
	if n := NumbersOf(Contact{Number: "10086"}); len(n) != 1 || n[0].Number != "10086" || n[0].Label != NumberMobile {
		t.Fatalf("a contact with Number only: %v", n)
	}
	if n := NumbersOf(Contact{Number: "1", Numbers: []ContactNumber{{Number: "2"}, {Number: "3"}}}); len(n) != 2 || n[0].Number != "2" {
		t.Fatalf("Numbers come first: %v", n)
	}
	if NumbersOf(Contact{Name: "x"}) != nil {
		t.Fatal("a contact without numbers")
	}
}

func TestContactDuplicates(t *testing.T) {
	list := []Contact{
		{ID: "a", Name: "Ann", Number: "13800000001"},
		{ID: "b", Name: "Bob", Number: "13800000002"},
		{ID: "c", Name: "13800000003", Number: "13800000003"},
		{ID: "d", Name: "ANN ", Number: "13900000009"},
		{ID: "e", Name: "Bobby", Numbers: []ContactNumber{{Number: "+86 138 0000 0002"}}},
		{ID: "f", Name: "13800000003", Number: "13700000000"},
		{ID: "g", Name: "Dee", Numbers: []ContactNumber{{Number: "13900000009"}, {Number: "13800000002"}}},
	}
	got := ContactDuplicates(list)
	if len(got) != 1 || strings.Join(got[0], ",") != "a,b,d,e,g" {
		t.Fatalf("sets %q", got)
	}
	if got := ContactDuplicates(list[:3]); got != nil {
		t.Fatalf("no duplicates: %q", got)
	}
}

func TestBlockedText(t *testing.T) {
	b := BlockList{Words: []string{"Free Gift", "\u53d1\u7968", "--"}}
	for text, want := range map[string]string{
		"claim your free-gift today":                        "Free Gift",
		"\uff26\uff52\uff45\uff45 \uff27\uff49\uff46\uff54": "Free Gift",
		"\u4ee3\u5f00\u00b7\u53d1\u00b7\u7968":              "\u53d1\u7968",
		"a free ride, a gift":                               "",
		"-- --":                                             "",
	} {
		if got := b.BlockedText(text); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
	if (BlockList{}).BlockedText("anything") != "" || WordKey("A-b c\u3000\uff11") != "abc1" {
		t.Error("WordKey")
	}
}

// A birthday or anniversary: with its year, or "--MM-DD" without; 29
// February without a year or in a leap year only.
func TestContactDateParts(t *testing.T) {
	for s, want := range map[string][3]int{"1990-04-12": {1990, 4, 12}, "--12-31": {0, 12, 31}, "--02-29": {0, 2, 29}, "2000-02-29": {2000, 2, 29}} {
		if y, m, d, ok := ContactDateParts(s); !ok || [3]int{y, m, d} != want {
			t.Errorf("%s: %d %d %d %v", s, y, m, d, ok)
		}
	}
	for _, s := range []string{"", "1990-4-12", "1990-13-01", "1990-04-31", "1900-02-29", "0000-01-01", "--0412", "--13-01", "19900412", "1990-04-12T00:00"} {
		if _, _, _, ok := ContactDateParts(s); ok {
			t.Errorf("%q is a date", s)
		}
	}
}
