package power

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

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

// The OnePlus 6T unplugged at 100%: its bq27411 fuel gauge still says
// "Full", its PMI8998 charger is offline (docs/39-oneplus-6t.md §16.10).
// Not on external power: before, "Full" counted as charging, so the plug
// that followed changed nothing and never lit the screen.
func TestAFullBatteryUnpluggedIsNotCharging(t *testing.T) {
	supplies(t, map[string]map[string]string{
		"bq27411-0":       {"type": "Battery", "capacity": "100", "status": "Full"},
		"pmi8998-charger": {"type": "USB", "online": "0", "status": "Discharging", "usb_type": "[Unknown] SDP DCP CDP"},
	})
	if s, ok := Read(); !ok || s.Level != 100 || s.Charging || s.Source != None || s.Fast || !s.Plug {
		t.Fatalf("Read() = %+v, %v", s, ok)
	}
}

// Plugged in again, full: the charger's online says so, whatever the
// battery's status; "Not charging" (the charger resting at 100%) too.
func TestAFullBatteryPluggedInIsCharging(t *testing.T) {
	for _, status := range []string{"Full", "Not charging", "Discharging"} {
		supplies(t, map[string]map[string]string{
			"bq27411-0":       {"type": "Battery", "capacity": "100", "status": status},
			"pmi8998-charger": {"type": "USB", "online": "1", "status": "Full", "usb_type": "Unknown [SDP] DCP CDP"},
		})
		if s, _ := Read(); !s.Charging || s.Source != USB || s.Fast {
			t.Fatalf("battery %q: Read() = %+v", status, s)
		}
	}
}

func TestChargingOnUSB(t *testing.T) {
	supplies(t, map[string]map[string]string{
		"bq27411-0":       {"type": "Battery", "capacity": "60", "status": "Charging"},
		"pmi8998-charger": {"type": "USB", "online": "1", "status": "Charging", "usb_type": "Unknown SDP [DCP] CDP"},
	})
	if s, _ := Read(); !s.Charging || s.Source != USBDCP || !s.Fast || s.Detecting {
		t.Fatalf("Read() = %+v", s)
	}
}

// A laptop's AC adapter (a Mains supply) decides as a USB charger does.
func TestAMainsSupply(t *testing.T) {
	supplies(t, map[string]map[string]string{
		"AC":   {"type": "Mains", "online": "1"},
		"BAT0": {"type": "Battery", "capacity": "100", "status": "Not charging"},
	})
	if s, _ := Read(); !s.Charging || s.Source != Mains || !s.Fast {
		t.Fatalf("on AC: Read() = %+v", s)
	}
	supplies(t, map[string]map[string]string{
		"AC":   {"type": "Mains", "online": "0"},
		"BAT0": {"type": "Battery", "capacity": "100", "status": "Full"},
	})
	if s, _ := Read(); s.Charging || s.Source != None {
		t.Fatalf("off AC: Read() = %+v", s)
	}
}

// No charger supply at all: the battery's status is all there is, and
// only "Charging" means external power.
func TestNoChargerSupply(t *testing.T) {
	for status, want := range map[string]bool{"Charging": true, "Full": false, "Discharging": false, "Unknown": false} {
		supplies(t, map[string]map[string]string{
			"battery": {"type": "Battery", "capacity": "100", "status": status},
		})
		if s, ok := Read(); !ok || s.Charging != want || s.Plug {
			t.Errorf("battery %q alone: Read() = %+v, %v; want charging %v, no plug known", status, s, ok, want)
		}
	}
	// A charger whose online says nothing does not count as one.
	supplies(t, map[string]map[string]string{
		"battery": {"type": "Battery", "capacity": "80", "status": "Charging"},
		"usb":     {"type": "USB"},
	})
	if s, _ := Read(); !s.Charging {
		t.Errorf("a charger without online: Read() = %+v", s)
	}
}

// Just plugged in, the PMI8998 has not detected the charger yet and its
// usb_type answers EAGAIN (a FIFO with nothing in it stands in): Read
// returns at once, on external power, the charger not known yet. Before,
// os.ReadFile parked on the EAGAIN for good, and with it the Shell's loop.
func TestAChargerStillBeingDetected(t *testing.T) {
	supplies(t, map[string]map[string]string{
		"bq27411-0":       {"type": "Battery", "capacity": "100", "status": "Full"},
		"pmi8998-charger": {"type": "USB", "online": "1", "status": "Not charging"},
	})
	p := filepath.Join(hal.SysRoot, "class", "power_supply", "pmi8998-charger", "usb_type")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip("no FIFO:", err)
	}
	fd, err := syscall.Open(p, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(fd) })
	got := make(chan Supply, 1)
	go func() {
		s, _ := Read()
		got <- s
	}()
	select {
	case s := <-got:
		if !s.Charging || s.Source != USB || s.Fast || !s.Detecting {
			t.Fatalf("Read() = %+v", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read waited on usb_type answering EAGAIN")
	}
}

func TestSupplyEvents(t *testing.T) {
	ev := func(fields ...string) []byte {
		var b []byte
		for _, f := range fields {
			b = append(b, f...)
			b = append(b, 0)
		}
		return b
	}
	plug := ev("change@/devices/platform/soc@0/c440000.spmi/spmi-0/0-02/c440000.spmi:pmic@2:charger@1000/power_supply/pmi8998-charger",
		"ACTION=change", "DEVPATH=/devices/platform/soc@0/c440000.spmi/spmi-0/0-02/c440000.spmi:pmic@2:charger@1000/power_supply/pmi8998-charger",
		"SUBSYSTEM=power_supply", "POWER_SUPPLY_NAME=pmi8998-charger", "POWER_SUPPLY_ONLINE=1", "SEQNUM=4242")
	if !supplyEvent(plug) {
		t.Error("the charger's uevent not taken")
	}
	if supplyEvent(ev("add@/devices/virtual/input/input9", "ACTION=add", "SUBSYSTEM=input", "NAME=power_supply")) {
		t.Error("an input device's uevent taken")
	}
}
