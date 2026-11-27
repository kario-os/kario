package usbhost

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kario-os/kario/hal"
)

// The port's data role. The kernel's Type-C port driver (the typec class,
// /sys/class/typec) chooses it from the CC lines when something is plugged
// in: a computer or a charger pulls up (it is the source) and the phone is
// the device; a keyboard, a mouse or an OTG adapter pulls down and the phone
// is the host, and powers it on VBUS. The driver sets the USB controller's
// role through its role switch (/sys/class/usb_role). The one case it
// leaves wrong is a powered hub or dock: it is the source, so the phone
// starts as its device, and only a USB PD data role swap makes the phone
// the host behind it; most docks ask for one, and for those that do not
// the Shell does (usbhost.go).

// Roles as the typec and usb_role classes write them.
const (
	RoleHost   = "host"
	RoleDevice = "device"
	RoleNone   = "none"
)

// Port is a Type-C port (typec class).
type Port struct {
	Name string // port0
	// DataRole: host or device; PowerRole: source or sink.
	DataRole, PowerRole string
	// Swappable: the port can take either data role (data_role lists both).
	Swappable bool
	// Partner: something is plugged in; PartnerPD: it speaks USB Power
	// Delivery, so a data role swap can be asked of it.
	Partner, PartnerPD bool
	// Accessory: the partner's accessory mode ("analog_audio", "debug";
	// "" or "none" for none).
	Accessory string
}

func typecDir() string { return filepath.Join(hal.SysRoot, "class", "typec") }

// chosen is the bracketed word of a sysfs choice ("[host] device"), or
// the whole value when there is no choice.
func chosen(s string) string {
	if i := strings.IndexByte(s, '['); i >= 0 {
		if j := strings.IndexByte(s[i:], ']'); j > 0 {
			return s[i+1 : i+j]
		}
	}
	return strings.TrimSpace(s)
}

// Ports are the Type-C ports the kernel has.
func Ports() []Port {
	ents, _ := os.ReadDir(typecDir())
	var out []Port
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, "port") || strings.Contains(n, "-") {
			continue
		}
		dir := filepath.Join(typecDir(), n)
		data := read(dir, "data_role")
		p := Port{Name: n, DataRole: chosen(data), PowerRole: chosen(read(dir, "power_role")),
			Swappable: strings.Contains(data, RoleHost) && strings.Contains(data, RoleDevice)}
		partner := filepath.Join(typecDir(), n+"-partner")
		if _, err := os.Stat(partner); err == nil {
			p.Partner = true
			p.PartnerPD = read(partner, "supports_usb_power_delivery") == "yes"
			p.Accessory = read(partner, "accessory_mode")
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SetDataRole asks the port for a data role: with a PD partner the driver
// sends a data role swap, which the partner may refuse (an error).
func SetDataRole(port, role string) error {
	return os.WriteFile(filepath.Join(typecDir(), port, "data_role"), []byte(role), 0o644)
}

// Switch is a USB controller's role switch (usb_role class).
type Switch struct {
	Name string
	Role string // host, device or none
}

// Switches are the role switches the kernel has.
func Switches() []Switch {
	dir := filepath.Join(hal.SysRoot, "class", "usb_role")
	ents, _ := os.ReadDir(dir)
	var out []Switch
	for _, e := range ents {
		out = append(out, Switch{Name: e.Name(), Role: read(filepath.Join(dir, e.Name()), "role")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Role is the port's data role now: the first Type-C port's, else the
// first role switch's; "" where the kernel has neither (the emulator, whose
// host and device are controllers of their own).
func Role() string {
	for _, p := range Ports() {
		if !p.Partner {
			return RoleNone
		}
		return p.DataRole
	}
	for _, s := range Switches() {
		return s.Role
	}
	return ""
}

// PowersVBUS: the phone supplies VBUS to what is plugged in (it is the
// host and the power source), so what the devices draw counts against the
// board's budget. Without a Type-C port to say, a host is taken to power
// its devices.
func PowersVBUS() bool {
	for _, p := range Ports() {
		return p.Partner && p.DataRole == RoleHost && p.PowerRole != "sink"
	}
	return true
}
