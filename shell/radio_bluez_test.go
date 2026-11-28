package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/kario-os/kario/hal"
)

// btSys is a sysfs whose Bluetooth controllers are virtual (hci_vhci's,
// btvirt's) or of the phone's own (the 6T's WCN3990 on its UART).
func btSys(t *testing.T, virtual map[string]bool) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "class", "bluetooth"), 0o755); err != nil {
		t.Fatal(err)
	}
	for hci, v := range virtual {
		dev := filepath.Join("devices", "platform", "soc@0", "8c0000.geniqup", "898000.serial", "serial0", "serial0-0", "bluetooth", hci)
		if v {
			dev = filepath.Join("devices", "virtual", "bluetooth", hci)
		}
		if err := os.MkdirAll(filepath.Join(root, dev), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "..", dev), filepath.Join(root, "class", "bluetooth", hci)); err != nil {
			t.Fatal(err)
		}
	}
	old := hal.SysRoot
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = old })
}

func btObjs(hcis ...string) bluezObjs {
	o := bluezObjs{}
	for _, h := range hcis {
		o[dbus.ObjectPath("/org/bluez/"+h)] = map[string]map[string]dbus.Variant{bluezAdapter: {}}
	}
	return o
}

// The phone's adapter is its own controller even where virtual ones came
// first (btvirt's, started before the phone's controller was set up, as
// hci0 and hci1): Settings, airplane mode and the search work on the radio.
func TestBluezPhoneAdapterIsItsOwn(t *testing.T) {
	btSys(t, map[string]bool{"hci0": true, "hci1": true, "hci2": false})
	r := &bluezRadio{}
	if a := r.adapter(btObjs("hci0", "hci1", "hci2")); a != "/org/bluez/hci2" {
		t.Errorf("the phone's adapter is %s, not its own controller hci2", a)
	}
	btSys(t, map[string]bool{"hci0": true, "hci1": true})
	r.peer = "/org/bluez/hci1"
	if a := r.adapter(btObjs("hci0", "hci1")); a != "/org/bluez/hci0" {
		t.Errorf("virtual controllers only (the emulator): the phone's adapter is %s, not hci0", a)
	}
}

// The selftest's device is a virtual controller (btvirt), which reaches the
// other virtual ones only: beside the 6T's own controller, or on a kernel
// without hci_vhci, the cases that pair with it are skipped, saying why.
func TestBTPeerNeedsVirtualControllers(t *testing.T) {
	if !alone(t) {
		return
	}
	vhci := filepath.Join(t.TempDir(), "vhci")
	old := vhciDev
	vhciDev = vhci
	t.Cleanup(func() { vhciDev = old })
	r := &bluezRadio{}
	btSys(t, map[string]bool{"hci0": false, "hci1": true, "hci2": true})
	_ = os.WriteFile(vhci, nil, 0o600)
	if off := r.peerOff(btObjs("hci0", "hci1", "hci2")); !strings.Contains(off, "hci0 is a controller of its own") {
		t.Errorf("beside the phone's own controller: %q", off)
	}
	btSys(t, map[string]bool{"hci0": true, "hci1": true})
	if off := r.peerOff(btObjs("hci0", "hci1")); off != "" {
		t.Errorf("two virtual controllers (the emulator): %q", off)
	}
	os.Remove(vhci)
	btSys(t, map[string]bool{"hci0": false})
	if off := r.peerOff(btObjs()); !strings.Contains(off, "hci_vhci") {
		t.Errorf("no adapter, no hci_vhci (the 6T whose controller has no address): %q", off)
	}

	c := snapCore(t)
	bt := c.bt
	t.Cleanup(func() { c.bt = bt })
	var out strings.Builder
	r2 := &Runner{C: c, Out: &out}
	c.bt = nil
	r2.Run("== peer\nrequire-bt-peer\nbt-push notes.txt hello\n")
	if got, m := caseLine(r2.Cases), r2.Cases[0].Message; got != "peer SKIP" || m != "needs bt-peer: no Bluetooth" {
		t.Fatalf("no Bluetooth: %s %q\n%s", got, m, out.String())
	}
	c.bt = &simBT{enabled: true}
	r2.Run("== peer\nrequire-bt-peer\n")
	if got := caseLine(r2.Cases); got != "peer PASS" {
		t.Fatalf("the simulated radio: %s\n%s", got, out.String())
	}
}
