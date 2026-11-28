package smsd

import (
	"strings"
	"testing"
)

// FuzzFindCode is an SMS from anyone: a verification code found in it is
// 4 to 8 of its digits.
func FuzzFindCode(f *testing.F) {
	f.Add("\u3010\u62db\u5546\u94f6\u884c\u3011\u60a8\u7684\u9a8c\u8bc1\u7801\u662f 482913\uff0c5 \u5206\u949f\u5185\u6709\u6548")
	f.Add("Your verification code: 123-456")
	f.Add("\u8f6c\u8d26 5000 \u5143\uff0c\u52a8\u6001\u7801 778899")
	f.Add("Code valid until 10:20")
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 8<<10 {
			return
		}
		code := findCode(text)
		if code == "" {
			return
		}
		if len(code) < 4 || len(code) > 8 || strings.Trim(code, "0123456789") != "" {
			t.Fatalf("%q in %q", code, text)
		}
		if !strings.Contains(strings.NewReplacer(" ", "", "-", "").Replace(text), code) {
			t.Fatalf("%q is not in %q", code, text)
		}
	})
}
