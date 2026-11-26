package power

import (
	"path/filepath"
	"strconv"

	"github.com/kario-os/kario/hal"
)

// Gauge is what the battery's fuel gauge says besides its level (Read);
// the zero value is a gauge that says nothing more.
type Gauge struct {
	// Status is the battery's own status: "Charging", "Discharging",
	// "Not charging", "Full", "Unknown" ("" without one).
	Status string
	// NoCapacity: the battery has no capacity to read, so Read's level
	// (0) means nothing.
	NoCapacity bool
	// Critical: capacity_level is "Critical", the gauge's own nearly
	// empty (the bq27411's SOCF flag, about 2% by default).
	Critical bool
	// Current is current_now in microamps, when HasCurrent: positive into
	// the battery by the sysfs ABI, though older drivers and test_power
	// report a charge as negative.
	Current    int
	HasCurrent bool
}

// ReadGauge is the first battery's gauge, the battery Read reads; false
// when there is none.
func ReadGauge() (Gauge, bool) {
	dirs, _ := filepath.Glob(filepath.Join(hal.SysRoot, "class", "power_supply", "*"))
	for _, d := range dirs {
		if read(d, "type") != "Battery" {
			continue
		}
		g := Gauge{Status: read(d, "status"), Critical: read(d, "capacity_level") == "Critical"}
		if _, err := strconv.Atoi(read(d, "capacity")); err != nil {
			g.NoCapacity = true
		}
		if v, err := strconv.Atoi(read(d, "current_now")); err == nil {
			g.Current, g.HasCurrent = v, true
		}
		return g, true
	}
	return Gauge{}, false
}
