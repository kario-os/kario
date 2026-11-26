package ota

import (
	"encoding/json"
	"testing"
)

// FuzzHolidays is holidays.json: what parses marshals to a calendar that
// parses.
func FuzzHolidays(f *testing.F) {
	f.Add([]byte(`{"format":"kario-holidays/1","regions":{"CN":{"source":"State Council","days_off":["2026-10-01","2026-10-02"],"workdays":["2026-09-27"]}}}`))
	f.Add([]byte(`{"format":"kario-holidays/1","regions":{"cn":{}}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		h, err := ParseHolidays(raw)
		if err != nil {
			return
		}
		again, _ := json.Marshal(h)
		if _, err := ParseHolidays(again); err != nil && len(again) <= MaxHolidays {
			t.Fatalf("%s parses, its encoding %s does not: %v", raw, again, err)
		}
	})
}
