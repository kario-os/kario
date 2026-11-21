package shellcmd

import (
	"strings"
	"testing"

	"github.com/kario-os/kario/shell"
)

// The Shell's own telephonyd drives the phone's modem when the Shell runs a
// phone by itself (the chroot on Nura); scripts and the developer machine
// keep the simulated baseband.
func TestPrivateTelephonyFollowsTheSKU(t *testing.T) {
	phone := shell.SKU{ID: "oneplus-fajita-t9", Modem: "modemmanager"}
	for _, tc := range []struct {
		sku      shell.SKU
		headless bool
		want     string
	}{
		{phone, false, "modemmanager"},
		{phone, true, ""},
		{shell.SKU{Modem: "modemmanager"}, false, ""},
		{shell.SKU{ID: "qemu-t9", Modem: "sim"}, false, "sim"},
		{shell.SKU{}, false, ""},
	} {
		if got := privateModem(tc.sku, tc.headless); got != tc.want {
			t.Errorf("privateModem(%+v, headless %v) = %q, want %q", tc.sku, tc.headless, got, tc.want)
		}
	}
	if got := strings.Join(telephonydArgs("/r", "/s", "modemmanager"), " "); got != "--run-dir /r --state-dir /s --modem modemmanager" {
		t.Errorf("args %q", got)
	}
	if got := strings.Join(telephonydArgs("/r", "/s", ""), " "); got != "--run-dir /r --state-dir /s" {
		t.Errorf("args without a modem %q", got)
	}
}
