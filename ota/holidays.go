package ota

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// The holiday calendar: holidays.json, which the update server serves
// next to latest.json and signs the same way (X-Offer-Issued and
// X-Offer-Signature, over DataMessage). Per region it lists the public
// holidays and the weekend days that are workdays (China's make-up
// workdays); phone-nfcd tells workdays from days off by it when it
// chooses door cards (30-nfc.md §4). The dates are each government's
// (in China the State Council's yearly notice): the platform ships none,
// and a calendar needs checking against the notice before it is served.

const (
	HolidaysFile   = "holidays.json"
	HolidaysFormat = "kario-holidays/1"
	// MaxHolidays bounds the file a phone reads.
	MaxHolidays = 256 << 10
)

type Holidays struct {
	Format  string                    `json:"format"`
	Regions map[string]RegionHolidays `json:"regions"`
}

// RegionHolidays are one region's dates, YYYY-MM-DD in its own time;
// Source says where they are from.
type RegionHolidays struct {
	Source   string   `json:"source,omitempty"`
	DaysOff  []string `json:"days_off,omitempty"`
	Workdays []string `json:"workdays,omitempty"`
}

// DataMessage is what an update server signs for a file of system data
// it serves besides latest.json: the file's name, the time and the exact
// bytes it serves.
func DataMessage(name string, issued int64, body []byte) []byte {
	return append([]byte(fmt.Sprintf("kario-data/1\n%s\n%d\n", name, issued)), body...)
}

var regionCode = regexp.MustCompile(`^[A-Z]{2}$`)

// ParseHolidays reads a calendar and checks it: its format, regions by
// their ISO 3166 codes, dates that exist, no date both a day off and a
// workday, at most 2000 dates a region.
func ParseHolidays(raw []byte) (*Holidays, error) {
	if len(raw) > MaxHolidays {
		return nil, errors.New("holidays: too large")
	}
	var h Holidays
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("holidays: %w", err)
	}
	if h.Format != HolidaysFormat {
		return nil, fmt.Errorf("holidays: format %q, this reads %q", h.Format, HolidaysFormat)
	}
	for code, r := range h.Regions {
		if !regionCode.MatchString(code) {
			return nil, fmt.Errorf("holidays: region %q is not an ISO 3166 code", code)
		}
		if len(r.DaysOff)+len(r.Workdays) > 2000 {
			return nil, fmt.Errorf("holidays: %s: too many dates", code)
		}
		off := map[string]bool{}
		for _, d := range r.DaysOff {
			if _, err := time.Parse(time.DateOnly, d); err != nil {
				return nil, fmt.Errorf("holidays: %s: day off %q is not a date", code, d)
			}
			off[d] = true
		}
		for _, d := range r.Workdays {
			if _, err := time.Parse(time.DateOnly, d); err != nil {
				return nil, fmt.Errorf("holidays: %s: workday %q is not a date", code, d)
			}
			if off[d] {
				return nil, fmt.Errorf("holidays: %s: %s is both a day off and a workday", code, d)
			}
		}
	}
	return &h, nil
}
