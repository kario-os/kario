// Package power reads the Linux power supply class
// (/sys/class/power_supply): the battery, and what is charging it. A USB-C
// charger that negotiated USB Power Delivery (the Type-C port controller's
// driver does the negotiation) shows as usb_type [PD] or [PD_PPS] on the
// USB supply, or as a supply of type USB_PD on older drivers.
package power

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

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
	// Detecting: a charger is plugged in and its driver is still finding
	// out what it is (usb_type answers EAGAIN: the PMI8998's detection
	// takes a few hundred milliseconds after the plug); Source and Fast
	// are not known yet.
	Detecting bool
	// Plug: a charger says whether it is plugged in (online), so Charging
	// is the cable's word, not the battery's status.
	Plug bool
}

func read(d, f string) string {
	s, _ := attr(d, f)
	return s
}

func attr(d, f string) (string, error) {
	b, err := hal.ReadAttr(filepath.Join(d, f))
	return strings.TrimSpace(string(b)), err
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

func usbSource(d string) (Source, bool) {
	list, err := attr(d, "usb_type")
	switch t := active(list); {
	case errors.Is(err, syscall.EAGAIN):
		return USB, true
	case strings.HasPrefix(t, "PD"):
		return USBPD, false
	case t == "DCP" || t == "CDP" || t == "C" || t == "ACA" || t == "APPLE_BRICK_ID":
		return USBDCP, false
	}
	if read(d, "type") == "USB_PD" || read(d, "type") == "USB_PD_DRP" {
		return USBPD, false
	}
	return USB, false
}

// online is a charger's online: whether it is plugged in, and whether it
// said (an attribute missing or failing says nothing).
func online(d string) (on, known bool) {
	switch read(d, "online") {
	case "1":
		return true, true
	case "0":
		return false, true
	}
	return false, false
}

// Read is the first battery and the supply charging it; false when the
// machine has no battery (a developer machine, QEMU). External power is
// what the chargers (Mains, USB) say with online; the battery's own status
// counts only on a machine where no charger says, and then only
// "Charging": a fuel gauge keeps saying "Full" at 100% after the charger
// is gone (the OnePlus 6T's bq27411).
func Read() (Supply, bool) {
	dirs, _ := filepath.Glob(filepath.Join(hal.SysRoot, "class", "power_supply", "*"))
	var s Supply
	found, chargers, batteryCharging := false, false, false
	for _, d := range dirs {
		switch read(d, "type") {
		case "Battery":
			if found {
				continue
			}
			found = true
			s.Level, _ = strconv.Atoi(read(d, "capacity"))
			batteryCharging = read(d, "status") == "Charging"
		case "Mains":
			on, known := online(d)
			chargers = chargers || known
			if on {
				s.Charging = true
				if rank(Mains) > rank(s.Source) {
					s.Source = Mains
				}
			}
		case "USB", "USB_C", "USB_PD", "USB_PD_DRP", "USB_DCP", "USB_CDP":
			on, known := online(d)
			chargers = chargers || known
			if on {
				s.Charging = true
				src, detecting := usbSource(d)
				s.Detecting = s.Detecting || detecting
				if rank(src) > rank(s.Source) {
					s.Source = src
				}
			}
		}
	}
	if !chargers {
		s.Charging = batteryCharging
	}
	s.Plug = chargers
	s.Fast = s.Source == USBPD || s.Source == USBDCP || s.Source == Mains
	s.Detecting = s.Detecting && !s.Fast
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
