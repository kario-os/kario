package netfw

import "testing"

func TestCounterNames(t *testing.T) {
	for _, n := range CounterNames(10003) {
		id, kind, ok := parseCounter(n)
		if !ok || id != 10003 || (kind != "wifi-rx" && kind != "wifi-tx" && kind != "mobile-rx" && kind != "mobile-tx") {
			t.Errorf("%s: %d %q %v", n, id, kind, ok)
		}
	}
	for _, bad := range []string{"x10003-wifi-rx", "u-wifi-rx", "u99999999999-wifi-rx", "u10003"} {
		if _, _, ok := parseCounter(bad); ok {
			t.Errorf("%s accepted", bad)
		}
	}
}
