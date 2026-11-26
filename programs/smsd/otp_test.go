package smsd

import "testing"

// The Chinese texts are written as escapes: code holds no Chinese
// (scripts/no-chinese.py).
func TestFindCode(t *testing.T) {
	for _, c := range []struct{ text, code string }{
		// "[China Merchants Bank] your verification code is 482913, valid for 5 minutes"
		{"\u3010\u62db\u5546\u94f6\u884c\u3011\u60a8\u7684\u9a8c\u8bc1\u7801\u662f 482913\uff0c5 \u5206\u949f\u5185\u6709\u6548", "482913"},
		{"\u9a8c\u8bc1\u7801 123456", "123456"},
		{"Your code is 1234.", "1234"},
		{"G-482913 is your Google verification code.", "482913"},
		{"Your verification code: 123-456", "123456"},
		{"Use PIN 0042 to sign in", "0042"},
		// A number to call before the word, the code after it.
		{"\u5982\u975e\u672c\u4eba\u64cd\u4f5c\u8bf7\u81f4\u7535 95555\uff0c\u9a8c\u8bc1\u7801 662211", "662211"},
		// An amount, a year, a time and a phone number are not codes.
		{"\u8f6c\u8d26 5000 \u5143\uff0c\u52a8\u6001\u7801 778899", "778899"},
		{"\u672c\u6708\u8bdd\u8d39 1234.56\uff0c\u9a8c\u8bc1\u7801 8848", "8848"},
		{"2026\u5e74\u9a8c\u8bc1\u7801 1122", "1122"},
		{"Code valid until 10:20", ""},
		{"Call 138-0013-8000 for your code", ""},
		{"Your balance is 88.88.", ""},
		{"Meet at 1930 near the barcode scanner", ""},
		{"Hello, see you at 5", ""},
	} {
		if got := findCode(c.text); got != c.code {
			t.Errorf("findCode(%q) = %q, want %q", c.text, got, c.code)
		}
	}
}

func TestAlertLevel(t *testing.T) {
	for ch, want := range map[int]string{4370: "presidential", 4383: "presidential", 4371: "extreme", 4352: "extreme",
		4375: "severe", 4379: "amber", 4380: "test", 4355: "test", 919: "test"} {
		if got := alertLevel(ch); got != want {
			t.Errorf("alertLevel(%d) = %s, want %s", ch, got, want)
		}
	}
}
