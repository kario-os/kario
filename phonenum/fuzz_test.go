package phonenum

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzParse is a sender's number from anyone (an SMS, a call, an MMS's
// headers): it is read without a panic, its key reads back as itself, and
// a valid number's E.164 form parses to the same number in any region.
func FuzzParse(f *testing.F) {
	for _, s := range []string{"13800138000", "+86 138 0013 8000", "(201) 555-0123", "+44 (0)20 7946 0000", "011 15-1234-5678",
		"10086", "ICBC", "", "+", "00", "0086", "+1", "8 (912) 345-67-89", "+380441234567", "\u00a0+34\u00a0612345678"} {
		f.Add(s, "CN")
		f.Add(s, "AR")
	}
	f.Fuzz(func(t *testing.T, raw, region string) {
		if len(raw) > 256 || len(region) > 8 {
			return
		}
		n := Parse(raw, region)
		k := Key(raw, region)
		if len(n.NSN) > maxDigits+1 || len(n.CC) > 3 {
			t.Fatalf("%q: %+v", raw, n)
		}
		if utf8.ValidString(raw) && n.Alpha == "" && Key(k, region) != k {
			t.Fatalf("%q in %s: key %q reads as %q", raw, region, k, Key(k, region))
		}
		if e := n.E164(); e != "" {
			if !strings.HasPrefix(e, "+") || len(e) > 1+maxDigits {
				t.Fatalf("%q: E.164 %q", raw, e)
			}
			for _, r := range []string{"CN", "US", "AR", "XX"} {
				if m := Parse(e, r); m.E164() != e {
					t.Fatalf("%q: %q read in %s is %+v", raw, e, r, m)
				}
			}
		}
		_ = Format(raw, region)
	})
}
