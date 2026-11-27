package usbhost

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/hal"
)

// descriptors builds a device's raw descriptors: the device descriptor
// (class, vendor, product), then per configuration its attributes and
// bMaxPower and its interfaces' class, subclass and protocol.
func descriptors(class uint8, vid, pid uint16, configs ...config) []byte {
	b := []byte{18, 1, 0x00, 0x02, class, 0, 0, 64, byte(vid), byte(vid >> 8), byte(pid), byte(pid >> 8), 0, 1, 1, 2, 3, byte(len(configs))}
	for i, c := range configs {
		total := 9 + 9*len(c.ifs)
		b = append(b, 9, 2, byte(total), byte(total>>8), byte(len(c.ifs)), byte(i+1), 0, c.attr, c.power)
		for n, f := range c.ifs {
			b = append(b, 9, 4, byte(n), 0, 1, f.Class, f.SubClass, f.Protocol, 0)
		}
	}
	return b
}

type config struct {
	attr, power uint8
	ifs         []Interface
}

var (
	kbdIf     = Interface{ClassHID, 1, bootKeyboard}
	mouseIf   = Interface{ClassHID, 1, bootMouse}
	storageIf = Interface{ClassStorage, 6, 0x50}
	hubIf     = Interface{ClassHub, 0, 0}
)

func TestParseTellsWhatADeviceIs(t *testing.T) {
	kbd, err := Parse(descriptors(0, 0x046d, 0xc31c, config{0x80, 50, []Interface{kbdIf, {ClassHID, 0, 0}}}), false)
	if err != nil || !kbd.HID() || !kbd.Keyboard() || kbd.Mouse() || kbd.Storage() || kbd.Hub() || kbd.MaxPowerMA != 100 || kbd.SelfPowered {
		t.Errorf("keyboard: %+v, %v", kbd, err)
	}
	if kbd.VendorID != 0x046d || kbd.ProductID != 0xc31c {
		t.Errorf("ids %04x:%04x", kbd.VendorID, kbd.ProductID)
	}
	mouse, _ := Parse(descriptors(0, 1, 2, config{0xa0, 49, []Interface{mouseIf}}), false)
	if !mouse.Mouse() || mouse.Keyboard() || mouse.MaxPowerMA != 98 {
		t.Errorf("mouse: %+v", mouse)
	}
	// A stick that is a keyboard too, the keyboard only in its second
	// configuration: both count.
	duck, _ := Parse(descriptors(0, 1, 2, config{0x80, 100, []Interface{storageIf}}, config{0x80, 250, []Interface{storageIf, kbdIf}}), false)
	if !duck.Storage() || !duck.HID() || duck.MaxPowerMA != 500 || len(duck.Interfaces) != 2 {
		t.Errorf("storage and keyboard: %+v", duck)
	}
	hub, _ := Parse(descriptors(ClassHub, 1, 2, config{0xe0, 0, []Interface{hubIf}}), false)
	if !hub.Hub() || !hub.SelfPowered || hub.HID() {
		t.Errorf("hub: %+v", hub)
	}
	ss, _ := Parse(descriptors(0, 1, 2, config{0x80, 112, []Interface{storageIf}}), true)
	if ss.MaxPowerMA != 896 {
		t.Errorf("SuperSpeed bMaxPower counts 8 mA: %d", ss.MaxPowerMA)
	}
	for name, raw := range map[string][]byte{
		"empty":            nil,
		"no configuration": descriptors(0, 1, 2),
		"cut":              descriptors(0, 1, 2, config{0x80, 50, []Interface{kbdIf}})[:30],
		"zero length":      append(descriptors(0, 1, 2, config{0x80, 50, nil}), 0, 4),
	} {
		if _, err := Parse(raw, false); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// sysfs makes a device directory under root's /sys/bus/usb/devices.
func sysfs(t *testing.T, root, name string, raw []byte, authorized string, attrs map[string]string) {
	t.Helper()
	dir := filepath.Join(root, "bus", "usb", "devices", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"descriptors": string(raw), "authorized": authorized, "speed": "480"}
	for k, v := range attrs {
		files[k] = v
	}
	for k, v := range files {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDevicesBusesAndAuthorize(t *testing.T) {
	root := t.TempDir()
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	usb := filepath.Join(root, "bus", "usb", "devices")
	for _, bus := range []string{"usb1", "usb2"} {
		os.MkdirAll(filepath.Join(usb, bus), 0o755)
		os.WriteFile(filepath.Join(usb, bus, "authorized_default"), []byte("1\n"), 0o644)
	}
	// dummy_hcd's root hub: sysfs links it under the dummy_hcd platform device.
	dummy := filepath.Join(root, "devices", "platform", "dummy_hcd.0", "usb3")
	os.MkdirAll(dummy, 0o755)
	os.Symlink(dummy, filepath.Join(usb, "usb3"))
	sysfs(t, root, "1-1", descriptors(0, 0x0627, 0x0001, config{0xa0, 50, []Interface{kbdIf}}), "0\n",
		map[string]string{"product": "QEMU USB Keyboard\n", "manufacturer": "QEMU\n", "idVendor": "0627\n", "idProduct": "0001\n"})
	sysfs(t, root, "1-1:1.0", nil, "1", nil)
	sysfs(t, root, "3-1", descriptors(0, 0x1d6b, 0x0104, config{0x80, 250, []Interface{{0x02, 0x0d, 0}}}), "1", nil)
	devs := Devices(func(bus int) bool { return bus != 3 })
	if len(devs) != 1 || devs[0].Name != "1-1" || devs[0].Label() != "QEMU USB Keyboard" || devs[0].Authorized || devs[0].Bus() != 1 {
		t.Fatalf("Devices = %+v", devs)
	}
	buses := Buses()
	if len(buses) != 3 || buses[0].Dummy || buses[0].Held || !buses[2].Dummy {
		t.Fatalf("Buses = %+v", buses)
	}
	if err := Hold(1); err != nil {
		t.Fatal(err)
	}
	if !Buses()[0].Held {
		t.Error("Hold left the bus authorizing")
	}
	if err := Authorize("1-1", true); err != nil {
		t.Fatal(err)
	}
	if d, _ := Read("1-1"); !d.Authorized {
		t.Error("Authorize did not authorize")
	}
}

func TestDrawCountsWhatThePortPowers(t *testing.T) {
	dev := func(name string, ma int, self, auth bool) Device {
		return Device{Name: name, MaxPowerMA: ma, SelfPowered: self, Authorized: auth}
	}
	// A bus-powered hub (100 mA) with a keyboard (100) and a mouse (98)
	// behind it; a self-powered hub's disk draws from the hub's supply.
	devs := []Device{
		dev("1-1", 100, false, true), dev("1-1.1", 100, false, true), dev("1-1.2", 98, false, true),
		dev("2-1", 0, true, true), dev("2-1.1", 896, false, true),
	}
	if got := Draw(devs); got != 298 {
		t.Errorf("Draw = %d, want 298", got)
	}
	disk := dev("1-1.3", 500, false, false)
	if Within(append(devs, disk), disk, 500) {
		t.Error("a 500 mA disk behind the bus-powered hub fits a 500 mA budget with 298 mA drawn")
	}
	if !Within(append(devs, disk), disk, 900) {
		t.Error("it fits 900 mA")
	}
	behind := dev("2-1.2", 900, false, false)
	if !Within(append(devs, behind), behind, 100) {
		t.Error("a device behind a self-powered hub counts against the phone")
	}
	if Draw([]Device{dev("1-1", 500, false, false)}) != 0 {
		t.Error("an unauthorized device counted")
	}
}

func TestPortsAndRoles(t *testing.T) {
	root := t.TempDir()
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	if Role() != "" || !PowersVBUS() {
		t.Errorf("no port, no switch: role %q, powers %v", Role(), PowersVBUS())
	}
	sw := filepath.Join(root, "class", "usb_role", "a600000.usb-role-switch")
	os.MkdirAll(sw, 0o755)
	os.WriteFile(filepath.Join(sw, "role"), []byte("device\n"), 0o644)
	if Role() != RoleDevice {
		t.Errorf("a role switch alone: %q", Role())
	}
	port := filepath.Join(root, "class", "typec", "port0")
	os.MkdirAll(port, 0o755)
	write := func(dir, f, v string) { os.WriteFile(filepath.Join(dir, f), []byte(v+"\n"), 0o644) }
	write(port, "data_role", "host [device]")
	write(port, "power_role", "source [sink]")
	if p := Ports(); len(p) != 1 || p[0].Partner || !p[0].Swappable || p[0].DataRole != RoleDevice || p[0].PowerRole != "sink" {
		t.Fatalf("Ports = %+v", p)
	}
	if Role() != RoleNone {
		t.Errorf("nothing plugged in: %q", Role())
	}
	partner := filepath.Join(root, "class", "typec", "port0-partner")
	os.MkdirAll(partner, 0o755)
	write(partner, "supports_usb_power_delivery", "yes")
	write(partner, "accessory_mode", "none")
	if p := Ports(); !p[0].Partner || !p[0].PartnerPD || Role() != RoleDevice || PowersVBUS() {
		t.Errorf("a dock as the source: %+v, role %q", p, Role())
	}
	if err := SetDataRole("port0", RoleHost); err != nil {
		t.Fatal(err)
	}
	write(port, "data_role", "[host] device")
	write(port, "power_role", "[source] sink")
	if Role() != RoleHost || !PowersVBUS() {
		t.Errorf("a keyboard: role %q, powers %v", Role(), PowersVBUS())
	}
	write(port, "data_role", "[host]")
	if Ports()[0].Swappable {
		t.Error("a host-only port taken for swappable")
	}
}
