package main

import (
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// The payment code is a barcode a laser scanner reads and a QR code, both
// on a secure surface and asking for full brightness.
func TestPaycodeForTheTill(t *testing.T) {
	n := paycodeNumber("aEXEy1r0Wp/G")
	if len(n) != 18 || !strings.HasPrefix(n, "28") || strings.Trim(n, "0123456789") != "" {
		t.Fatalf("payment code number %q", n)
	}
	if paycodeNumber("aEXEy1r0Wp/G") != n || paycodeNumber("bEXEy1r0Wp/G") == n {
		t.Error("the number does not follow the signature")
	}
	w := &wallet{code: "PAYCODE|U8848|1|sig", number: n, codeAt: time.Now()}
	var bar, qr bool
	for _, node := range w.paycodeScreen().Body {
		if node.Kind != ui.KindImage {
			continue
		}
		if !node.Secure || !node.Bright {
			t.Errorf("%s: secure %v, bright %v", node.Src, node.Secure, node.Bright)
		}
		bar = bar || node.Src == "bar:"+n
		qr = qr || node.Src == "qr:"+w.code
	}
	if !bar || !qr {
		t.Errorf("barcode %v, QR code %v", bar, qr)
	}
}

func TestBankCard(t *testing.T) {
	for in, want := range map[string]string{
		"6222 0202 0011 2233":  "6222020200112233",
		"6222020200112233999":  "6222020200112233999",
		"622202020011223":      "",
		"62220202001122339999": "",
		"6222-0202-0011-2233":  "",
	} {
		if got := cardNumber(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if c := bankCode("6222020200112233"); len(c) != 6 || c != bankCode("6222020200112233") || c == bankCode("6222020200112234") {
		t.Errorf("bank code %q", c)
	}
	if bankCode("6222020200112233") != "233329" {
		t.Error("the selftest's bank SMS (shell/selftest.script) sends 233329 for its card")
	}
}
