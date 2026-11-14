package power

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/hal"
)

func supplies(t *testing.T, s map[string]map[string]string) {
	t.Helper()
	root := t.TempDir()
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	for name, files := range s {
		d := filepath.Join(root, "class", "power_supply", name)
		os.MkdirAll(d, 0o755)
		for f, v := range files {
			os.WriteFile(filepath.Join(d, f), []byte(v+"\n"), 0o644)
		}
	}
}

func TestPowerDeliveryIsFastCharging(t *testing.T) {
	supplies(t, map[string]map[string]string{
		"battery": {"type": "Battery", "capacity": "41", "status": "Charging"},
		"usb":     {"type": "USB", "online": "1", "usb_type": "Unknown SDP DCP CDP C [PD] PD_DRP PD_PPS"},
	})
	s, ok := Read()
	if !ok || s.Level != 41 || !s.Charging || s.Source != USBPD || !s.Fast {
		t.Fatalf("Read() = %+v, %v", s, ok)
	}
}

func TestAComputersPortIsNotFast(t *testing.T) {
	supplies(t, map[string]map[string]string{
		"bms": {"type": "Battery", "capacity": "90", "status": "Charging"},
		"usb": {"type": "USB", "online": "1", "usb_type": "Unknown [SDP] DCP CDP"},
	})
	if s, _ := Read(); s.Source != USB || s.Fast {
		t.Fatalf("Read() = %+v", s)
	}
}

func TestOldDriversNamePDInTheType(t *testing.T) {
	supplies(t, map[string]map[string]string{
		"battery": {"type": "Battery", "capacity": "10", "status": "Charging"},
		"pd":      {"type": "USB_PD", "online": "1"},
		"usb":     {"type": "USB", "online": "1"},
	})
	if s, _ := Read(); s.Source != USBPD || !s.Fast {
		t.Fatalf("Read() = %+v", s)
	}
}

func TestUnpluggedAndNoBattery(t *testing.T) {
	supplies(t, map[string]map[string]string{
		"battery": {"type": "Battery", "capacity": "77", "status": "Discharging"},
		"usb":     {"type": "USB", "online": "0", "usb_type": "[PD]"},
	})
	if s, ok := Read(); !ok || s.Charging || s.Source != None {
		t.Fatalf("Read() = %+v, %v", s, ok)
	}
	supplies(t, nil)
	if _, ok := Read(); ok {
		t.Fatal("a battery where there is none")
	}
}
