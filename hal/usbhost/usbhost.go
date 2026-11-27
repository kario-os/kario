// Package usbhost is the phone as a USB host on its USB-C port (OTG): the
// keyboards, mice and hubs plugged into it, and the port's Type-C role
// (typec.go). The kernel enumerates a device whether or not it is
// authorized; on a bus whose authorized_default is 0 it reads the device's
// descriptors but binds no driver until user space writes 1 to the
// device's authorized file, as USBGuard does. The Shell decides from the
// descriptors (every configuration's interfaces, what it draws from VBUS)
// before the device can type or be mounted.
package usbhost

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/kario-os/kario/hal"
)

// USB classes the Shell tells apart (the USB-IF's class codes).
const (
	ClassHID     = 0x03
	ClassStorage = 0x08
	ClassHub     = 0x09
)

// HID boot protocols (an interface of subclass 1, boot).
const (
	bootKeyboard = 1
	bootMouse    = 2
)

// Interface is one interface of one of the device's configurations.
type Interface struct {
	Class, SubClass, Protocol uint8
}

// Device is a USB device on a host bus as sysfs shows it.
type Device struct {
	// Name is its sysfs name: bus-port[.port...] ("1-1", "1-1.3").
	Name                          string
	Manufacturer, Product, Serial string
	VendorID, ProductID           uint16
	Class                         uint8
	// Interfaces of every configuration: the kernel picks one, and a
	// device may offer a harmless one first.
	Interfaces []Interface
	// MaxPowerMA: the most any configuration draws from VBUS; SelfPowered:
	// every configuration is self-powered (a hub with its own supply).
	MaxPowerMA  int
	SelfPowered bool
	Authorized  bool
}

func (d Device) has(class uint8) bool {
	if d.Class == class {
		return true
	}
	for _, i := range d.Interfaces {
		if i.Class == class {
			return true
		}
	}
	return false
}

// Hub: the device is a hub.
func (d Device) Hub() bool { return d.Class == ClassHub }

// HID: some interface is a human interface device: a keyboard, a mouse, a
// device's buttons. Its report descriptor, not read before it is bound,
// may make any HID interface a keyboard.
func (d Device) HID() bool { return d.has(ClassHID) }

// Storage: some interface is mass storage.
func (d Device) Storage() bool { return d.has(ClassStorage) }

// Mouse: every HID interface is a boot mouse.
func (d Device) Mouse() bool {
	n := 0
	for _, i := range d.Interfaces {
		if i.Class == ClassHID {
			if i.SubClass != 1 || i.Protocol != bootMouse {
				return false
			}
			n++
		}
	}
	return n > 0
}

// Keyboard: some HID interface is a boot keyboard.
func (d Device) Keyboard() bool {
	for _, i := range d.Interfaces {
		if i.Class == ClassHID && i.SubClass == 1 && i.Protocol == bootKeyboard {
			return true
		}
	}
	return false
}

// Label is how the user is told of the device: its product name, else its
// IDs.
func (d Device) Label() string {
	if p := strings.TrimSpace(d.Product); p != "" {
		return p
	}
	return fmt.Sprintf("%04x:%04x", d.VendorID, d.ProductID)
}

// Parent is the sysfs name of the hub the device is plugged into, "" for
// one on the root hub (in the port itself).
func (d Device) Parent() string {
	if i := strings.LastIndexByte(d.Name, '.'); i > 0 {
		return d.Name[:i]
	}
	return ""
}

// Bus is the device's bus number.
func (d Device) Bus() int {
	n, _ := strconv.Atoi(strings.SplitN(d.Name, "-", 2)[0])
	return n
}

var errShort = errors.New("usb descriptors cut short")

// Parse reads a device's raw descriptors (sysfs "descriptors": the device
// descriptor and every configuration's, as the device gave them); super:
// the device runs at SuperSpeed or faster, whose bMaxPower counts 8 mA,
// not 2.
func Parse(raw []byte, super bool) (Device, error) {
	var d Device
	if len(raw) < 18 || raw[0] < 18 || raw[1] != 1 {
		return d, errShort
	}
	d.Class = raw[4]
	d.VendorID = uint16(raw[8]) | uint16(raw[9])<<8
	d.ProductID = uint16(raw[10]) | uint16(raw[11])<<8
	unit := 2
	if super {
		unit = 8
	}
	configs := 0
	d.SelfPowered = true
	for off := int(raw[0]); off < len(raw); {
		n := int(raw[off])
		if n < 2 || off+n > len(raw) {
			return d, errShort
		}
		b := raw[off : off+n]
		switch b[1] {
		case 2:
			if n < 9 {
				return d, errShort
			}
			configs++
			d.MaxPowerMA = max(d.MaxPowerMA, int(b[8])*unit)
			if b[7]&0x40 == 0 {
				d.SelfPowered = false
			}
		case 4:
			if n < 9 {
				return d, errShort
			}
			i := Interface{Class: b[5], SubClass: b[6], Protocol: b[7]}
			if !hasInterface(d.Interfaces, i) {
				d.Interfaces = append(d.Interfaces, i)
			}
		}
		off += n
	}
	if configs == 0 {
		return d, errShort
	}
	return d, nil
}

func hasInterface(l []Interface, i Interface) bool {
	for _, j := range l {
		if j == i {
			return true
		}
	}
	return false
}

func devices() string { return filepath.Join(hal.SysRoot, "bus", "usb", "devices") }

// deviceName: bus-port[.port...], not a root hub (usbN) nor an interface
// (1-1:1.0).
var deviceName = regexp.MustCompile(`^[0-9]+-[0-9]+(\.[0-9]+)*$`)

func read(dir, attr string) string {
	b, _ := os.ReadFile(filepath.Join(dir, attr))
	return strings.TrimSpace(string(b))
}

func hex16(s string) uint16 {
	n, _ := strconv.ParseUint(s, 16, 16)
	return uint16(n)
}

// Read reads one device by its sysfs name.
func Read(name string) (Device, error) {
	dir := filepath.Join(devices(), name)
	raw, err := os.ReadFile(filepath.Join(dir, "descriptors"))
	if err != nil {
		return Device{}, err
	}
	speed, _ := strconv.Atoi(read(dir, "speed"))
	d, err := Parse(raw, speed >= 5000)
	if err != nil {
		return d, fmt.Errorf("%s: %w", name, err)
	}
	d.Name = name
	d.Manufacturer, d.Product, d.Serial = read(dir, "manufacturer"), read(dir, "product"), read(dir, "serial")
	if v := read(dir, "idVendor"); v != "" {
		d.VendorID, d.ProductID = hex16(v), hex16(read(dir, "idProduct"))
	}
	d.Authorized = read(dir, "authorized") != "0"
	return d, nil
}

// Devices are the devices on the buses for which on says yes, by name; a
// device whose descriptors cannot be read (gone meanwhile) is left out.
func Devices(on func(bus int) bool) []Device {
	ents, _ := os.ReadDir(devices())
	var out []Device
	for _, e := range ents {
		if !deviceName.MatchString(e.Name()) {
			continue
		}
		d, err := Read(e.Name())
		if err != nil || !on(d.Bus()) {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Bus is a host controller's root hub.
type Bus struct {
	Num int
	// Path: the root hub's device path in sysfs, through its controller.
	Path string
	// Dummy: dummy_hcd's host side, the emulator's computer at the other
	// end of the phone's own gadget, not a port of the phone.
	Dummy bool
	// Held: devices plugged into it wait for the Shell (authorized_default
	// is 0).
	Held bool
}

// Buses are the root hubs.
func Buses() []Bus {
	ents, _ := os.ReadDir(devices())
	var out []Bus
	for _, e := range ents {
		n, err := strconv.Atoi(strings.TrimPrefix(e.Name(), "usb"))
		if !strings.HasPrefix(e.Name(), "usb") || err != nil {
			continue
		}
		dir := filepath.Join(devices(), e.Name())
		target, _ := filepath.EvalSymlinks(dir)
		out = append(out, Bus{Num: n, Path: target, Dummy: strings.Contains(target, "dummy_hcd"), Held: read(dir, "authorized_default") == "0"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Num < out[j].Num })
	return out
}

// Hold makes the devices plugged into bus from now on wait for Authorize.
func Hold(bus int) error {
	return os.WriteFile(filepath.Join(devices(), fmt.Sprintf("usb%d", bus), "authorized_default"), []byte("0"), 0o644)
}

// Authorize lets the kernel configure the device and bind its drivers (on),
// or unbinds them and leaves it unconfigured.
func Authorize(name string, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return os.WriteFile(filepath.Join(devices(), name, "authorized"), []byte(v), 0o644)
}

// Draw is what the authorized devices draw from the phone's VBUS (devs:
// every device on the port's buses): each one's MaxPower, unless a
// self-powered hub on its way to the port supplies it. An unauthorized
// device stays unconfigured and within a unit load (100 mA), which the
// port gives whatever the budget.
func Draw(devs []Device) int {
	byName := map[string]Device{}
	for _, d := range devs {
		byName[d.Name] = d
	}
	sum := 0
	for _, d := range devs {
		if d.Authorized && fromPort(byName, d) {
			sum += d.MaxPowerMA
		}
	}
	return sum
}

// fromPort: no self-powered hub stands between d and the port.
func fromPort(byName map[string]Device, d Device) bool {
	for p := d.Parent(); p != ""; {
		h, ok := byName[p]
		if !ok {
			return true
		}
		if h.SelfPowered {
			return false
		}
		p = h.Parent()
	}
	return true
}

// Within: authorizing d keeps the draw from VBUS within budgetMA.
func Within(devs []Device, d Device, budgetMA int) bool {
	byName := map[string]Device{}
	for _, o := range devs {
		byName[o.Name] = o
	}
	if !fromPort(byName, d) {
		return true
	}
	now := Draw(devs)
	if o, ok := byName[d.Name]; ok && o.Authorized {
		return now <= budgetMA
	}
	return now+d.MaxPowerMA <= budgetMA
}
