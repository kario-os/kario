package usbgadget

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/hal"
)

func TestSetupWritesTheGadgetAndBinds(t *testing.T) {
	root := t.TempDir()
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	if _, err := Setup(Default("KARIO0001")); err != ErrNoUDC {
		t.Fatalf("no controller: %v", err)
	}
	os.MkdirAll(filepath.Join(root, "class", "udc", "dummy_udc.0"), 0o755)
	os.WriteFile(filepath.Join(root, "class", "udc", "dummy_udc.0", "state"), []byte("configured\n"), 0o644)
	udc, err := Setup(Default("KARIO0001"))
	if err != nil || udc != "dummy_udc.0" {
		t.Fatalf("Setup = %q, %v", udc, err)
	}
	g := filepath.Join(root, "kernel", "config", "usb_gadget", "kario")
	for f, want := range map[string]string{
		"UDC": "dummy_udc.0", "idVendor": "0x1d6b", "strings/0x409/serialnumber": "KARIO0001",
		"configs/c.1/MaxPower": "500",
	} {
		b, _ := os.ReadFile(filepath.Join(g, f))
		if string(b) != want {
			t.Errorf("%s = %q, want %q", f, b, want)
		}
	}
	if target, err := os.Readlink(filepath.Join(g, "configs", "c.1", "ncm.usb0")); err != nil || !strings.HasSuffix(target, "functions/ncm.usb0") {
		t.Errorf("function link: %q, %v", target, err)
	}
	if State("dummy_udc.0") != "configured" {
		t.Errorf("State = %q", State("dummy_udc.0"))
	}
	if again, err := Setup(Default("KARIO0001")); err != nil || again != udc {
		t.Errorf("second Setup = %q, %v", again, err)
	}
}

func TestMACsAreStableAndLocal(t *testing.T) {
	d1, h1 := MACs("A")
	d2, h2 := MACs("A")
	d3, _ := MACs("B")
	if d1 != d2 || h1 != h2 || d1 == d3 || d1 == h1 {
		t.Fatalf("MACs: %s %s %s %s %s", d1, h1, d2, h2, d3)
	}
	for _, m := range []string{d1, h1, d3} {
		var b byte
		if _, err := fmt.Sscanf(m[:2], "%02x", &b); err != nil || b&0x02 == 0 || b&0x01 != 0 {
			t.Errorf("%s is not a local unicast address", m)
		}
	}
}
