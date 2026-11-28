package ime

import "testing"

func TestPinyin(t *testing.T) {
	for name, want := range map[string]string{
		"\u5f20\u4e09":                "zhang san",
		"\u674e\u56db":                "li si",
		"\u5988\u5988":                "ma ma",
		"\u4e2d\u56fd\u79fb\u52a8":    "zhong guo yi dong",
		"\u5355\u7530\u82b3":          "shan tian fang",
		"\u66fe\u5c0f\u8d24":          "zeng xiao xian",
		"\u6b27\u9633\u5a1c\u5a1c":    "ou yang na na",
		"\u957f\u5b59\u65e0\u5fcc":    "zhang sun wu ji",
		"\u91cd\u9633":                "chong yang",
		"Alice Smith":                 "alice smith",
		"Jos\u00e9 Mar\u00eda":        "jose maria",
		"Bob \u738b":                  "bob wang",
		"\u8001\u738b (\u5355\u4f4d)": "lao wang dan wei",
		"Room 101":                    "room 101",
		"":                            "",
		"\U0001F600":                  "",
	} {
		if got := Pinyin(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
}
