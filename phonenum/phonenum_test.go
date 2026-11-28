package phonenum

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestKey(t *testing.T) {
	for _, c := range []struct{ raw, region, want string }{
		{"13800138000", "CN", "+8613800138000"},
		{"+86 138 0013 8000", "CN", "+8613800138000"},
		{"0086-138-0013-8000", "CN", "+8613800138000"},
		{"010 1234 5678", "CN", "+861012345678"},
		{"+86 10 1234 5678", "CN", "+861012345678"},
		{"10086", "CN", "10086"},
		{"+8610086", "CN", "10086"},
		{"1065 5555 1234", "CN", "106555551234"},
		{"ICBC Bank", "CN", "icbc bank"},
		{"", "CN", ""},
		// The same Spanish mobile, written in Spain and from China.
		{"612 34 56 78", "ES", "+34612345678"},
		{"+34 612 345 678", "CN", "+34612345678"},
		{"0034612345678", "CN", "+34612345678"},
		// NANP: the trunk 1 and the international prefix 011.
		{"(201) 555-0123", "US", "+12015550123"},
		{"1-201-555-0123", "US", "+12015550123"},
		{"011 86 138 0013 8000", "US", "+8613800138000"},
		{"911", "US", "911"},
		// A UK number written with its trunk prefix after the country code.
		{"+44 (0)20 7946 0000", "CN", "+442079460000"},
		{"+44 020 7946 0000", "CN", "+442079460000"},
		{"020 7946 0000", "GB", "+442079460000"},
		// Mexico's old mobile "1" and Argentina's "15".
		{"+52 1 55 1234 5678", "MX", "+525512345678"},
		{"55 1234 5678", "MX", "+525512345678"},
		{"011 15-1234-5678", "AR", "+5491112345678"},
		{"+54 9 11 1234-5678", "CN", "+5491112345678"},
		{"0351 15 123 4567", "AR", "+5493511234567"},
		// Italy keeps the leading zero of fixed numbers.
		{"+39 06 1234 5678", "CN", "+390612345678"},
		{"06 1234 5678", "IT", "+390612345678"},
		// Russia's trunk 8 and international 810.
		{"8 (912) 345-67-89", "RU", "+79123456789"},
		{"810 86 138 0013 8000", "RU", "+8613800138000"},
		// Hong Kong has no trunk prefix; Taiwan's is 0.
		{"9123 4567", "HK", "+85291234567"},
		{"0912 345 678", "TW", "+886912345678"},
		// A country the table lacks keeps its code; an unknown region reads digits as written.
		{"+380 44 123 4567", "CN", "+380441234567"},
		{"0441234567", "XX", "0441234567"},
		{"+86 138 0013 8000", "XX", "+8613800138000"},
	} {
		if got := Key(c.raw, c.region); got != c.want {
			t.Errorf("Key(%q, %s) = %q, want %q", c.raw, c.region, got, c.want)
		}
	}
}

func TestFormat(t *testing.T) {
	for _, c := range []struct{ raw, region, want string }{
		{"+8613800138000", "CN", "138 0013 8000"},
		{"+861012345678", "CN", "010 1234 5678"},
		{"+8657112345678", "CN", "0571 1234 5678"},
		{"+8613800138000", "ES", "+86 138 0013 8000"},
		{"612345678", "ES", "612 34 56 78"},
		{"+12015550123", "US", "(201) 555-0123"},
		{"+12015550123", "CN", "+1 201-555-0123"},
		{"+442079460000", "GB", "020 7946 0000"},
		{"+447700900123", "CN", "+44 7700 900123"},
		{"+5491112345678", "AR", "011 15-1234-5678"},
		{"+5491112345678", "CN", "+54 9 11 1234-5678"},
		{"+79123456789", "RU", "8 (912) 345-67-89"},
		{"+886912345678", "TW", "0912 345 678"},
		{"+33612345678", "FR", "06 12 34 56 78"},
		{"10086", "CN", "10086"},
		{"Kario Bank", "CN", "Kario Bank"},
		{"+380441234567", "CN", "+380 44 123 4567"},
	} {
		if got := Format(c.raw, c.region); got != c.want {
			t.Errorf("Format(%q, %s) = %q, want %q", c.raw, c.region, got, c.want)
		}
	}
}

func TestDisplay(t *testing.T) {
	for _, c := range []struct{ raw, region, want string }{
		{"+8613800138000", "CN", "138 0013 8000"},
		{"13800138000", "CN", "13800138000"},
		{"+34612345678", "CN", "+34 612 34 56 78"},
		{"+34612345678", "ES", "612 34 56 78"},
		{" 10086 ", "CN", "10086"},
		{"+8610086", "CN", "+8610086"},
	} {
		if got := Display(c.raw, c.region); got != c.want {
			t.Errorf("Display(%q, %s) = %q, want %q", c.raw, c.region, got, c.want)
		}
	}
}

func TestMatch(t *testing.T) {
	if !Match("13800138000", "+86 138-0013-8000", "CN") || Match("13800138000", "13800138001", "CN") {
		t.Error("Chinese mobile numbers")
	}
	if !Match("07700 900123", "+447700900123", "GB") || Match("07700 900123", "+447700900123", "CN") {
		t.Error("a UK number matches its national form only read in the UK")
	}
	if Match("", "", "CN") {
		t.Error("empty numbers match nothing")
	}
}

func TestRegionOfMCC(t *testing.T) {
	for mcc, want := range map[string]string{"460": "CN", "46001": "CN", "334": "MX", "310": "US", "302": "CA", "234": "GB", "999": "", "": ""} {
		if got := RegionOfMCC(mcc); got != want {
			t.Errorf("RegionOfMCC(%q) = %q, want %q", mcc, got, want)
		}
	}
}

func TestDefault(t *testing.T) {
	t.Setenv("PHONE_REGION", "es")
	region.Store(nil)
	if Default() != "ES" {
		t.Errorf("PHONE_REGION: %q", Default())
	}
	SetDefault("mx")
	defer region.Store(nil)
	if Default() != "MX" {
		t.Errorf("SetDefault: %q", Default())
	}
}

// TestTable: every pattern has as many digits as one of its region's
// lengths, the country codes split as splitCC does, and each region the
// system offers a language for (i18n/languages.json) is in the table.
func TestTable(t *testing.T) {
	for _, r := range data().Regions {
		if cc, rest := splitCC(r.CC + "123456789"); cc != r.CC || rest != "123456789" {
			t.Errorf("%s: country code %s splits as %q", r.Code, r.CC, cc)
		}
		for _, f := range r.Formats {
			for _, p := range []string{f.Nat, f.Intl} {
				n := strings.Count(p, "x") + strings.Count(p, "_")
				if !r.valid(strings.Repeat("1", n)) {
					t.Errorf("%s: pattern %q has %d digits, not one of %v", r.Code, p, n, r.Lengths)
				}
			}
		}
		if len(r.MCC) == 0 || len(r.Intl) == 0 || len(r.Lengths) == 0 {
			t.Errorf("%s: incomplete", r.Code)
		}
	}
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "i18n", "languages.json"))
	if err != nil {
		t.Fatal(err)
	}
	var langs struct {
		Languages []struct {
			Regions []string `json:"regions"`
		} `json:"languages"`
	}
	if err := json.Unmarshal(raw, &langs); err != nil {
		t.Fatal(err)
	}
	for _, l := range langs.Languages {
		for _, r := range l.Regions {
			if Lookup(r) == nil {
				t.Errorf("region %s of the offered languages is not in metadata.json", r)
			}
		}
	}
}
