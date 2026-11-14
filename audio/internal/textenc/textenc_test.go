package textenc

import (
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

func TestDecodings(t *testing.T) {
	zh := "\u6625\u6c5f\u82b1\u6708\u591c"
	gbk, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(zh))
	big5, _ := traditionalchinese.Big5.NewEncoder().Bytes([]byte("\u6625\u6c5f"))
	for name, c := range map[string]struct {
		got, want string
	}{
		"utf-8":          {Decode([]byte(zh)), zh},
		"utf-8 with BOM": {Decode(append([]byte{0xef, 0xbb, 0xbf}, zh...)), zh},
		"utf-16 le":      {Decode([]byte{0xff, 0xfe, 'h', 0, 'i', 0}), "hi"},
		"utf-16 be":      {Decode([]byte{0xfe, 0xff, 0, 'h', 0, 'i'}), "hi"},
		"gbk":            {Decode(gbk), zh},
		"legacy gbk":     {Legacy(gbk), zh},
		"legacy ascii":   {Legacy([]byte("Abc")), "Abc"},
		"legacy latin-1": {Legacy([]byte("Caf\xe9 \xe0 la cr\xe8me")), "Caf\u00e9 \u00e0 la cr\u00e8me"},
		"sacd gb2312":    {Charset(gbk, GB2312), zh},
		"sacd big5":      {Charset(big5, Big5), "\u6625\u6c5f"},
		"sacd latin-1":   {Charset([]byte("\xe9t\xe9"), ISO8859_1), "\u00e9t\u00e9"},
	} {
		if c.got != c.want {
			t.Errorf("%s: %q, want %q", name, c.got, c.want)
		}
	}
}
