package power

import "testing"

func TestGaugeOfTheFirstBattery(t *testing.T) {
	supplies(t, map[string]map[string]string{
		"bq27411-0": {"type": "Battery", "capacity": "100", "status": "Full", "capacity_level": "Full", "current_now": "1520000"},
		"pmi8998":   {"type": "USB", "online": "1"},
		"zz-second": {"type": "Battery", "capacity": "3", "status": "Charging", "capacity_level": "Critical"},
	})
	g, ok := ReadGauge()
	if !ok || g != (Gauge{Status: "Full", Current: 1520000, HasCurrent: true}) {
		t.Fatalf("ReadGauge() = %+v, %v", g, ok)
	}
}

func TestGaugeCriticalWithoutCurrentOrCapacity(t *testing.T) {
	supplies(t, map[string]map[string]string{
		"battery": {"type": "Battery", "status": "Charging", "capacity_level": "Critical"},
	})
	g, ok := ReadGauge()
	if !ok || g != (Gauge{Status: "Charging", NoCapacity: true, Critical: true}) {
		t.Fatalf("ReadGauge() = %+v, %v", g, ok)
	}
	supplies(t, map[string]map[string]string{"usb": {"type": "USB", "online": "1"}})
	if g, ok := ReadGauge(); ok || g != (Gauge{}) {
		t.Errorf("no battery: ReadGauge() = %+v, %v", g, ok)
	}
}
