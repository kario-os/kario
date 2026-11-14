// Package power reads the Linux power supply class
// (/sys/class/power_supply): the battery, and what is charging it. A USB-C
// charger that negotiated USB Power Delivery (the Type-C port controller's
// driver does the negotiation) shows as usb_type [PD] or [PD_PPS] on the
// USB supply, or as a supply of type USB_PD on older drivers.
package power

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kario-os/kario/hal"
)

// Source is what the phone is plugged into.
type Source string

const (
	None   Source = ""
	Mains  Source = "mains"
	USB    Source = "usb"     // a computer's port or a plain USB charger
	USBDCP Source = "usb-dcp" // a dedicated charger (1.5 A and up)
	USBPD  Source = "usb-pd"  // Power Delivery: fast charging
)

// Supply is the battery and its charger.
type Supply struct {
	Level    int  // percent
	Charging bool // on external power (charging or full)
	Source   Source
	// Fast: the charger negotiated more than USB's default (PD, PPS, a
	// dedicated charger's 1.5 A).
	Fast bool
}

func read(d, f string) string {
	b, _ := os.ReadFile(filepath.Join(d, f))
	return strings.TrimSpace(string(b))
}

// active is the bracketed choice of a sysfs list ("SDP DCP [PD] PD_PPS").
func active(list string) string {
	for _, f := range strings.Fields(list) {
		if strings.HasPrefix(f, "[") && strings.HasSuffix(f, "]") {
			return strings.Trim(f, "[]")
		}
	}
	return ""
}

func usbSource(d string) Source {
	switch t := active(read(d, "usb_type")); {
	case strings.HasPrefix(t, "PD"):
		return USBPD
	case t == "DCP" || t == "CDP" || t == "C" || t == "ACA" || t == "APPLE_BRICK_ID":
		return USBDCP
	}
	if read(d, "type") == "USB_PD" || read(d, "type") == "USB_PD_DRP" {
		return USBPD
	}
	return USB
}

// Read is the first battery and the supply charging it; false when the
// machine has no battery (a developer machine, QEMU).
func Read() (Supply, bool) {
	dirs, _ := filepath.Glob(filepath.Join(hal.SysRoot, "class", "power_supply", "*"))
	var s Supply
	found := false
	for _, d := range dirs {
		switch read(d, "type") {
		case "Battery":
			if found {
				continue
			}
			found = true
			s.Level, _ = strconv.Atoi(read(d, "capacity"))
			switch read(d, "status") {
			case "Charging", "Full":
				s.Charging = true
			}
		case "Mains":
			if read(d, "online") == "1" {
				s.Charging, s.Source = true, Mains
			}
		case "USB", "USB_C", "USB_PD", "USB_PD_DRP", "USB_DCP", "USB_CDP":
			if read(d, "online") == "1" {
				s.Charging = true
				if src := usbSource(d); rank(src) > rank(s.Source) {
					s.Source = src
				}
			}
		}
	}
	s.Fast = s.Source == USBPD || s.Source == USBDCP || s.Source == Mains
	return s, found
}

func rank(s Source) int {
	switch s {
	case USB:
		return 1
	case USBDCP:
		return 2
	case Mains:
		return 3
	case USBPD:
		return 4
	}
	return 0
}
