// Package usbgadget makes the phone a USB device through the kernel's
// gadget configfs (/sys/kernel/config/usb_gadget): one configuration with
// a CDC-NCM network function, which macOS, Linux and Windows 11 drive with
// their own class drivers (no installation on the computer, as an iPhone's
// USB network link needs none). Over that link the computer reaches the
// phone's file sharing, once the user trusts it on the phone (shell). The
// USB device controller (UDC) the board has is whichever the kernel lists
// under /sys/class/udc; its state tells a computer (configured) from a
// charger (powered, or not attached).
package usbgadget

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/kario-os/kario/hal"
)

// Config is the gadget as the phone presents it.
type Config struct {
	Name         string // the configfs directory
	VendorID     uint16
	ProductID    uint16
	Manufacturer string
	Product      string
	Serial       string
	// MaxPowerMA is what the phone draws from a computer's port, at most
	// 500 mA on USB 2 (a charger or USB-C current, negotiated by the port
	// controller, is not this).
	MaxPowerMA int
}

// Default is the prototype's identity: the Linux Foundation's multifunction
// composite gadget ID, until there is a vendor ID of our own.
func Default(serial string) Config {
	return Config{Name: "kario", VendorID: 0x1d6b, ProductID: 0x0104, Manufacturer: "Kario", Product: "Kario phone", Serial: serial, MaxPowerMA: 500}
}

func configfs() string { return filepath.Join(hal.SysRoot, "kernel", "config", "usb_gadget") }

// UDCs are the USB device controllers the kernel has.
func UDCs() []string {
	ents, _ := os.ReadDir(filepath.Join(hal.SysRoot, "class", "udc"))
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// State is a controller's USB state: "not attached", "powered",
// "configured" (a host chose a configuration: a computer), "suspended"...
func State(udc string) string {
	b, _ := os.ReadFile(filepath.Join(hal.SysRoot, "class", "udc", udc, "state"))
	return strings.TrimSpace(string(b))
}

// MACs are the network function's addresses, derived from the serial so a
// computer sees the same device every time: locally administered,
// unicast.
func MACs(serial string) (dev, host string) {
	sum := sha256.Sum256([]byte("kario-usb-ncm\x00" + serial))
	mac := func(b []byte) string {
		return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0]&0xfe|0x02, b[1], b[2], b[3], b[4], b[5])
	}
	return mac(sum[0:6]), mac(sum[6:12])
}

// ErrNoUDC: the machine has no USB device controller (or the kernel no
// gadget support).
var ErrNoUDC = errors.New("no USB device controller")

// Setup creates the gadget (once) and binds it to the first controller;
// it returns the controller's name.
func Setup(c Config) (string, error) {
	udcs := UDCs()
	if len(udcs) == 0 {
		return "", ErrNoUDC
	}
	g := filepath.Join(configfs(), c.Name)
	if b, err := os.ReadFile(filepath.Join(g, "UDC")); err == nil && strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b)), nil
	}
	dev, host := MACs(c.Serial)
	steps := []struct{ path, value string }{
		{"idVendor", fmt.Sprintf("0x%04x", c.VendorID)},
		{"idProduct", fmt.Sprintf("0x%04x", c.ProductID)},
		{"bcdDevice", "0x0100"},
		{"bcdUSB", "0x0200"},
		{"strings/0x409/manufacturer", c.Manufacturer},
		{"strings/0x409/product", c.Product},
		{"strings/0x409/serialnumber", c.Serial},
		{"configs/c.1/strings/0x409/configuration", "NCM"},
		{"configs/c.1/MaxPower", fmt.Sprint(c.MaxPowerMA)},
		{"functions/ncm.usb0/dev_addr", dev},
		{"functions/ncm.usb0/host_addr", host},
		{"os_desc/use", "0"},
	}
	for _, s := range steps {
		p := filepath.Join(g, s.path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		// The NCM addresses refuse any write (EBUSY) once the function has
		// been bound, so a value already in place is left alone.
		if b, err := os.ReadFile(p); err == nil && strings.EqualFold(strings.TrimSpace(string(b)), s.value) {
			continue
		}
		if err := os.WriteFile(p, []byte(s.value), 0o644); err != nil {
			if errors.Is(err, syscall.EBUSY) && strings.HasPrefix(s.path, "functions/ncm.usb0/") {
				continue // bound before with the same serial: the same addresses
			}
			return "", fmt.Errorf("%s: %w", s.path, err)
		}
	}
	// A Shell that ended while sharing over MTP left that function in
	// the configuration, which no longer binds without it.
	if err := os.Remove(filepath.Join(g, "configs", "c.1", mtpFunction)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	link := filepath.Join(g, "configs", "c.1", "ncm.usb0")
	if _, err := os.Lstat(link); err != nil {
		if err := os.Symlink(filepath.Join(g, "functions", "ncm.usb0"), link); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(g, "UDC"), []byte(udcs[0]), 0o644); err != nil {
		return "", fmt.Errorf("bind %s: %w", udcs[0], err)
	}
	return udcs[0], nil
}

// Interface is the network interface the gadget's NCM function made.
func Interface(c Config) string {
	b, err := os.ReadFile(filepath.Join(configfs(), c.Name, "functions", "ncm.usb0", "ifname"))
	if name := strings.TrimSpace(string(b)); err == nil && name != "" && !strings.Contains(name, "%") {
		return name
	}
	return "usb0"
}
