package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/hal"
)

// hostSys is a made-up sysfs with the port's root hub (usb1) and
// dummy_hcd's (usb2, the emulator's computer).
type hostSys struct {
	t    *testing.T
	root string
}

func newHostSys(t *testing.T) hostSys {
	root := t.TempDir()
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	usb := filepath.Join(root, "bus", "usb", "devices")
	port := filepath.Join(root, "devices", "platform", "a600000.usb", "xhci-hcd.0.auto", "usb1")
	dummy := filepath.Join(root, "devices", "platform", "dummy_hcd.0", "usb2")
	for _, d := range []string{usb, port, dummy} {
		os.MkdirAll(d, 0o755)
	}
	for _, d := range []string{port, dummy} {
		os.WriteFile(filepath.Join(d, "authorized_default"), []byte("1\n"), 0o644)
		os.Symlink(d, filepath.Join(usb, filepath.Base(d)))
	}
	return hostSys{t, root}
}

// plug puts a device in: its descriptors (device class, then one
// configuration: bmAttributes, bMaxPower, interface classes).
func (s hostSys) plug(name, product string, class, attr, power uint8, ifs [][3]uint8, authorized bool) {
	s.t.Helper()
	raw := []byte{18, 1, 0, 2, class, 0, 0, 64, 0x34, 0x12, 0x78, 0x56, 0, 1, 1, 2, 3, 1}
	total := 9 + 9*len(ifs)
	raw = append(raw, 9, 2, byte(total), 0, byte(len(ifs)), 1, 0, attr, power)
	for i, f := range ifs {
		raw = append(raw, 9, 4, byte(i), 0, 1, f[0], f[1], f[2], 0)
	}
	dir := filepath.Join(s.root, "bus", "usb", "devices", name)
	os.MkdirAll(dir, 0o755)
	auth := map[bool]string{true: "1", false: "0"}[authorized]
	for f, v := range map[string]string{"descriptors": string(raw), "authorized": auth, "product": product, "speed": "12"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(v), 0o644); err != nil {
			s.t.Fatal(err)
		}
	}
}

func (s hostSys) unplug(name string) {
	os.RemoveAll(filepath.Join(s.root, "bus", "usb", "devices", name))
}

func (s hostSys) authorized(name string) bool {
	b, _ := os.ReadFile(filepath.Join(s.root, "bus", "usb", "devices", name, "authorized"))
	return strings.TrimSpace(string(b)) == "1"
}

func (s hostSys) held(bus string) bool {
	b, _ := os.ReadFile(filepath.Join(s.root, "bus", "usb", "devices", bus, "authorized_default"))
	return strings.TrimSpace(string(b)) == "0"
}

var (
	ifKeyboard = [3]uint8{3, 1, 1}
	ifMouse    = [3]uint8{3, 1, 2}
	ifStorage  = [3]uint8{8, 6, 0x50}
)

func hostCore(t *testing.T) (*Core, hostSys) {
	s := newHostSys(t)
	c, _ := lockCore(t)
	c.cfg.SKU.USB = SKUUSB{Host: true, VBUSMA: 500}
	return c, s
}

// Keyboards, mice and hubs go on at once; storage waits for the user, and
// a stick that says it is a keyboard too is asked about as storage, its
// keyboard off until then; a device the port cannot power stays off until
// it can. dummy_hcd's computer is left alone.
func TestUSBHostDecidesEachDevice(t *testing.T) {
	c, s := hostCore(t)
	s.plug("1-1", "QEMU USB Keyboard", 0, 0xa0, 50, [][3]uint8{ifKeyboard}, false)
	c.startUSBHost()
	if !s.held("usb1") || s.held("usb2") {
		t.Fatalf("held: the port %v, dummy_hcd %v", s.held("usb1"), s.held("usb2"))
	}
	if !s.authorized("1-1") || c.usb.host.devs["1-1"].state != hostAllowed {
		t.Fatalf("the keyboard: authorized %v, %+v", s.authorized("1-1"), c.usb.host.devs["1-1"])
	}
	s.plug("1-2", "Stick", 0, 0x80, 100, [][3]uint8{ifStorage, ifKeyboard}, false)
	c.checkUSBHost()
	p, ok := overlayOf[*confirmOverlay](c)
	if !ok || !p.trust || !strings.Contains(p.text, "Stick") || !strings.Contains(p.text, tr.T("Allow the USB storage “%s”? It also says it is a keyboard or a mouse: allowed, it can type and click on this phone.", "Stick")) {
		t.Fatalf("the stick: prompt %+v", p)
	}
	if s.authorized("1-2") {
		t.Fatal("the stick's keyboard went on before the user said")
	}
	c.Key(Named(KeyEsc))
	c.checkUSBHost()
	if s.authorized("1-2") || c.usb.host.devs["1-2"].state != hostRefused || len(c.overlays) != 0 {
		t.Fatalf("refused: authorized %v, %+v, overlays %d", s.authorized("1-2"), c.usb.host.devs["1-2"], len(c.overlays))
	}
	s.unplug("1-2")
	c.checkUSBHost()
	if c.usb.host.devs["1-2"] != nil {
		t.Fatal("an unplugged device kept")
	}
	s.plug("1-2", "Card reader", 0, 0x80, 100, [][3]uint8{ifStorage}, false)
	c.checkUSBHost()
	p, _ = overlayOf[*confirmOverlay](c)
	if p == nil || p.text != tr.T("Allow the USB storage “%s”?", "Card reader") {
		t.Fatalf("plain storage: %+v", p)
	}
	c.usb.host.prompt.onYes()
	c.popOverlay(p)
	if !s.authorized("1-2") || c.toast == nil || c.toast.body != tr.T("Allowed. The phone cannot open USB storage yet.") {
		t.Fatalf("allowed: authorized %v, toast %+v", s.authorized("1-2"), c.toast)
	}
	// 100 mA (keyboard) + 200 mA (reader) drawn of 500: a 400 mA device
	// does not fit until the reader goes.
	s.plug("1-3", "Disk", 0, 0x80, 200, [][3]uint8{{0xff, 0, 0}}, false)
	c.checkUSBHost()
	if s.authorized("1-3") || c.usb.host.devs["1-3"].state != hostPower {
		t.Fatalf("over the budget: authorized %v, %+v", s.authorized("1-3"), c.usb.host.devs["1-3"])
	}
	if c.toast == nil || c.toast.body != tr.T("“%s” needs %d mA; the phone gives %d mA. Plug it into a powered hub.", "Disk", 400, 500) {
		t.Errorf("toast %+v", c.toast)
	}
	s.unplug("1-2")
	c.checkUSBHost()
	if !s.authorized("1-3") || c.usb.host.devs["1-3"].state != hostAllowed {
		t.Fatalf("room again: authorized %v, %+v", s.authorized("1-3"), c.usb.host.devs["1-3"])
	}
	// A self-powered hub supplies what is behind it.
	s.plug("1-4", "Powered hub", 9, 0xe0, 0, [][3]uint8{{9, 0, 0}}, false)
	c.checkUSBHost()
	s.plug("1-4.1", "Big disk", 0, 0x80, 250, [][3]uint8{{0xff, 0, 0}}, false)
	s.plug("1-4.2", "Mouse", 0, 0xa0, 50, [][3]uint8{ifMouse}, false)
	c.checkUSBHost()
	for _, d := range []string{"1-4", "1-4.1", "1-4.2"} {
		if !s.authorized(d) {
			t.Errorf("%s behind a powered hub left off", d)
		}
	}
}

// Under a PIN lock storage is not asked about (a stick that types would
// otherwise get its question answered by itself): the phone says to unlock,
// and asks once it is. A stick plugged in before the Shell held the bus
// (at boot) is taken off and asked about too; locking while the question
// is up asks it again after the unlock instead of refusing.
func TestUSBHostStorageWaitsForTheUnlock(t *testing.T) {
	c, s := hostCore(t)
	s.plug("1-1", "Boot stick", 0, 0x80, 100, [][3]uint8{ifStorage, ifKeyboard}, true)
	c.lock.pin = true
	c.lockNow("test")
	c.startUSBHost()
	if s.authorized("1-1") || len(c.overlays) != 0 || c.usb.host.devs["1-1"].state != hostWaiting {
		t.Fatalf("locked: authorized %v, overlays %d, %+v", s.authorized("1-1"), len(c.overlays), c.usb.host.devs["1-1"])
	}
	if c.toast == nil || c.toast.body != tr.T("Unlock the phone to use the USB storage") {
		t.Errorf("toast %+v", c.toast)
	}
	c.unlockNow("test")
	c.checkUSBHost()
	if _, ok := overlayOf[*confirmOverlay](c); !ok {
		t.Fatal("not asked after the unlock")
	}
	c.lockNow("test")
	if c.usb.host.prompt != nil || c.usb.host.devs["1-1"].state != hostWaiting {
		t.Fatalf("the lock closed the question: %+v", c.usb.host.devs["1-1"])
	}
	c.unlockNow("test")
	c.checkUSBHost()
	if _, ok := overlayOf[*confirmOverlay](c); !ok {
		t.Fatal("not asked again after the next unlock")
	}
	s.unplug("1-1")
	c.checkUSBHost()
	if len(c.overlays) != 0 || c.usb.host.prompt != nil {
		t.Error("the question outlived the stick")
	}
}

// A board with a controller inside names the port's: the other's devices
// (a fingerprint sensor) are not held. A board that is not a host holds
// nothing.
func TestUSBHostOnlyThePortsController(t *testing.T) {
	c, s := hostCore(t)
	c.cfg.SKU.USB.Controller = "8c00000.usb"
	c.startUSBHost()
	if s.held("usb1") || len(c.usb.host.buses) != 0 {
		t.Error("another controller's bus held")
	}
	c2, s2 := hostCore(t)
	c2.cfg.SKU.USB = SKUUSB{}
	c2.startUSBHost()
	s2.plug("1-1", "Stick", 0, 0x80, 100, [][3]uint8{ifStorage}, false)
	c2.checkUSBHost()
	if s2.held("usb1") || c2.usb.host.on {
		t.Error("a board that is not a host held its bus")
	}
}

// A powered dock is the source, so the port starts as its device: with no
// computer on the bus after usbHostProbe the partner is asked for the host
// role, once; a computer (the gadget reset and configured) is not.
func TestUSBHostAsksADockForTheHostRole(t *testing.T) {
	c, s := hostCore(t)
	c.startUSBHost()
	port := filepath.Join(s.root, "class", "typec", "port0")
	partner := filepath.Join(s.root, "class", "typec", "port0-partner")
	udc := filepath.Join(s.root, "class", "udc", "a600000.usb")
	for _, d := range []string{port, partner, udc} {
		os.MkdirAll(d, 0o755)
	}
	write := func(p, v string) { os.WriteFile(p, []byte(v+"\n"), 0o644) }
	write(filepath.Join(port, "data_role"), "host [device]")
	write(filepath.Join(port, "power_role"), "source [sink]")
	write(filepath.Join(partner, "supports_usb_power_delivery"), "yes")
	write(filepath.Join(udc, "state"), "configured")
	role := func() string {
		b, _ := os.ReadFile(filepath.Join(port, "data_role"))
		return strings.TrimSpace(string(b))
	}

	c.checkUSBHost()
	c.usb.host.partnerAt = time.Now().Add(-usbHostProbe)
	c.checkUSBHost()
	if role() != "host [device]" {
		t.Fatalf("a computer was asked for the host role: %q", role())
	}
	os.RemoveAll(partner)
	c.checkUSBHost()
	os.MkdirAll(partner, 0o755)
	write(filepath.Join(partner, "supports_usb_power_delivery"), "yes")
	write(filepath.Join(udc, "state"), "powered")
	c.checkUSBHost()
	if role() != "host [device]" {
		t.Fatalf("asked before %s: %q", usbHostProbe, role())
	}
	c.usb.host.partnerAt = time.Now().Add(-usbHostProbe)
	c.checkUSBHost()
	if role() != "host" || !c.usb.host.swapAsked {
		t.Fatalf("the dock was not asked: %q", role())
	}
	write(filepath.Join(port, "data_role"), "host [device]")
	c.checkUSBHost()
	if role() != "host [device]" {
		t.Error("asked twice in one plug")
	}
}

// sku.json's usb as tools/sku.py writes it, and what the Shell refuses
// should a file get past it.
func TestSKUUSB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sku.json")
	load := func(usb string) (SKU, error) {
		if err := os.WriteFile(path, []byte(`{"device":"bar-t9"`+usb+`}`), 0o644); err != nil {
			t.Fatal(err)
		}
		return LoadSKUFile(path)
	}
	s, err := load(`,"usb":{"host":true,"vbus_ma":500,"controller":"a600000.usb"}`)
	if err != nil || s.USB != (SKUUSB{Host: true, VBUSMA: 500, Controller: "a600000.usb"}) {
		t.Fatalf("%+v, %v", s.USB, err)
	}
	if s, err := load(``); err != nil || s.USB.Host {
		t.Fatalf("no usb: %+v, %v", s.USB, err)
	}
	for _, bad := range []string{`{"host":true,"vbus_ma":50}`, `{"host":true,"vbus_ma":3500}`, `{"vbus_ma":500}`, `{"controller":"a600000.usb"}`} {
		if _, err := load(`,"usb":` + bad); err == nil {
			t.Errorf("usb %s accepted", bad)
		}
	}
}
