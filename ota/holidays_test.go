package ota

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseHolidays(t *testing.T) {
	ok := `{"format": "kario-holidays/1", "regions": {"CN": {"source": "test fixture", "days_off": ["2031-10-01", "2031-10-02"], "workdays": ["2031-09-28"]}}}`
	h, err := ParseHolidays([]byte(ok))
	if err != nil || len(h.Regions["CN"].DaysOff) != 2 || h.Regions["CN"].Workdays[0] != "2031-09-28" {
		t.Fatalf("%+v %v", h, err)
	}
	for why, raw := range map[string]string{
		"format":      `{"format": "kario-holidays/2", "regions": {}}`,
		"region":      `{"format": "kario-holidays/1", "regions": {"china": {}}}`,
		"date":        `{"format": "kario-holidays/1", "regions": {"CN": {"days_off": ["2031-02-30"]}}}`,
		"both":        `{"format": "kario-holidays/1", "regions": {"CN": {"days_off": ["2031-10-01"], "workdays": ["2031-10-01"]}}}`,
		"not JSON":    `{"format": `,
		"too large":   `{"format": "kario-holidays/1", "regions": {}, "x": "` + strings.Repeat("x", MaxHolidays) + `"}`,
		"time format": `{"format": "kario-holidays/1", "regions": {"CN": {"workdays": ["2031-9-28"]}}}`,
	} {
		if _, err := ParseHolidays([]byte(raw)); err == nil {
			t.Fatalf("%s: accepted", why)
		}
	}
}

// The signed message names the file: a signature over holidays.json is
// none over latest.json, or over another file.
func TestDataMessage(t *testing.T) {
	body := []byte(`{}`)
	a, b := DataMessage(HolidaysFile, 1, body), DataMessage("other.json", 1, body)
	if bytes.Equal(a, b) || bytes.Equal(a, OfferMessage(1, body)) || !bytes.HasSuffix(a, body) {
		t.Fatalf("%q %q", a, b)
	}
}
