package sensor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kario-os/kario/hal"
)

func fakeIIO(t *testing.T, root, dev string, files map[string]string) {
	t.Helper()
	d := filepath.Join(root, "bus", "iio", "devices", dev)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, v := range files {
		os.WriteFile(filepath.Join(d, f), []byte(v+"\n"), 0o644)
	}
}

// The first device that measures illuminance, in lux or as raw count times
// scale; a device named by the board, or none.
func TestOpenLight(t *testing.T) {
	root := t.TempDir()
	hal.SysRoot = root
	t.Cleanup(func() { hal.SysRoot = "/sys" })
	if OpenLight("") != nil {
		t.Fatal("a light sensor where there is none")
	}
	fakeIIO(t, root, "iio:device0", map[string]string{"name": "bmi160", "in_accel_x_raw": "12"})
	fakeIIO(t, root, "iio:device1", map[string]string{"name": "stk3310", "in_illuminance_raw": "250", "in_illuminance_scale": "0.5"})
	fakeIIO(t, root, "iio:device2", map[string]string{"name": "tsl2772", "in_illuminance_input": "812.25"})
	s := OpenLight("")
	if s == nil || s.Name() != "stk3310" {
		t.Fatalf("OpenLight() = %v", s)
	}
	if lux, err := s.Lux(); err != nil || lux != 125 {
		t.Errorf("raw 250 at scale 0.5: %v, %v", lux, err)
	}
	s = OpenLight("tsl2772")
	if s == nil {
		t.Fatal("OpenLight(tsl2772)")
	}
	if lux, err := s.Lux(); err != nil || lux != 812.25 {
		t.Errorf("in_illuminance_input: %v, %v", lux, err)
	}
	if OpenLight("ltr501") != nil || OpenLight("bmi160") != nil {
		t.Error("a sensor by a name that is not there, or that measures no light")
	}
	fakeIIO(t, root, "iio:device1", map[string]string{"in_illuminance_raw": "-40", "in_illuminance_offset": "10"})
	if lux, _ := OpenLight("stk3310").Lux(); lux != 0 {
		t.Errorf("below zero: %v", lux)
	}
	fakeIIO(t, root, "iio:device2", map[string]string{"in_illuminance_input": "nan"})
	if _, err := OpenLight("tsl2772").Lux(); err == nil {
		t.Error("a reading that is not a number")
	}
}

func TestSim(t *testing.T) {
	s := NewSim()
	if lux, _ := s.Lux(); lux != SimLux {
		t.Errorf("start: %v", lux)
	}
	s.Set(-3)
	if lux, _ := s.Lux(); lux != 0 {
		t.Errorf("below zero: %v", lux)
	}
}
