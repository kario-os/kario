package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kario-os/kario/keyset"
	"github.com/kario-os/kario/ota"
	"github.com/kario-os/kario/statefile"
	"github.com/kario-os/kario/ui"
)

// The holiday calendar (ota.Holidays): the update server's holidays.json,
// signed like latest.json by the update server's key, fetched once a day
// while the phone has an update server and kept in the state directory.
// The dates of the phone's region go to phone-nfcd with the NFC policy,
// which tells workdays from days off by them (30-nfc.md §4).

const (
	holidaysEvery = 24 * time.Hour
	holidaysRetry = time.Hour
)

// holidayCache is the calendar kept, with when the server signed it (one
// signed earlier is refused) and when it was fetched.
type holidayCache struct {
	Issued   int64        `json:"issued"`
	Fetched  time.Time    `json:"fetched"`
	Calendar ota.Holidays `json:"calendar"`
}

func (c *Core) holidaysPath() string { return filepath.Join(c.cfg.StateDir, "shell", ota.HolidaysFile) }

func loadHolidays(path string) *holidayCache {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var h holidayCache
	if json.Unmarshal(raw, &h) != nil || h.Calendar.Format != ota.HolidaysFormat {
		return nil
	}
	return &h
}

// regionDays are the calendar's dates for region (none without one).
func (h *holidayCache) regionDays(region string) (daysOff, workdays []string) {
	if h == nil {
		return nil, nil
	}
	r := h.Calendar.Regions[region]
	return r.DaysOff, r.Workdays
}

// holidayCalendar is holidays.get: the region's dates of the calendar kept
// (apps' calendars mark them as the card pack's door cards use them).
func (c *Core) holidayCalendar() ui.HolidayCalendar {
	r := c.region()
	h := ui.HolidayCalendar{Region: r, DaysOff: []string{}, Workdays: []string{}}
	if hc := c.nfc.holidays; hc != nil {
		days := hc.Calendar.Regions[r]
		h.Source = days.Source
		h.DaysOff = append(h.DaysOff, days.DaysOff...)
		h.Workdays = append(h.Workdays, days.Workdays...)
	}
	return h
}

// updateServerKey is the update server's public keys, one a line: the one
// it signs with and, while it is rotated, the next or the replaced one
// (nil without a file).
func updateServerKey(path string) (keyset.Set, error) {
	if path == "" {
		return nil, nil
	}
	k, err := keyset.Load(path)
	if err != nil {
		return nil, fmt.Errorf("the update server's key (%s) is unreadable", path)
	}
	return k, nil
}

// checkHolidays fetches the calendar in the background when the one kept
// is a day old, or an hour after a failed try.
func (c *Core) checkHolidays() {
	n := &c.nfc
	if c.cfg.UpdateURL == "" || n.holidaysBusy || time.Since(n.holidaysTried) < holidaysRetry {
		return
	}
	if h := n.holidays; h != nil && time.Since(h.Fetched) < holidaysEvery && !h.Fetched.After(time.Now()) {
		return
	}
	n.holidaysTried = time.Now()
	key, err := updateServerKey(c.cfg.UpdateKey)
	if err != nil {
		c.log.Printf("holidays: %v", err)
		return
	}
	c.fetchHolidays(strings.TrimSuffix(c.cfg.UpdateURL, "/")+"/"+ota.HolidaysFile, key, nil)
}

// fetchHolidays fetches and keeps the calendar at url, then hands nfcd
// the region's days; done, if not nil, hears how it went.
func (c *Core) fetchHolidays(url string, key keyset.Set, done func(error)) {
	c.nfc.holidaysBusy = true
	path := c.holidaysPath()
	go func() {
		h, err := fetchHolidayFile(url, key, path, time.Now())
		c.Post(func() {
			c.nfc.holidaysBusy = false
			if err != nil {
				c.log.Printf("holidays: %v", err)
			} else {
				c.nfc.holidays = h
				off, work := h.regionDays(c.region())
				c.log.Printf("holidays: calendar of %d regions; %s: %d days off, %d workdays", len(h.Calendar.Regions), c.region(), len(off), len(work))
				c.syncNFC()
			}
			if done != nil {
				done(err)
			}
		})
	}()
}

// fetchHolidayFile reads the calendar at url; with key it must carry the
// update server's signature, not older than offerMaxAge nor than the
// calendar kept at path, where it is kept.
func fetchHolidayFile(url string, key keyset.Set, path string, now time.Time) (*holidayCache, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update server: %s: %s", ota.HolidaysFile, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, ota.MaxHolidays+1))
	if err != nil {
		return nil, err
	}
	var issued int64
	if key != nil {
		issued, err = ota.VerifyHeaders(resp.Header, key, func(at int64) []byte { return ota.DataMessage(ota.HolidaysFile, at, body) })
		if err != nil {
			return nil, fmt.Errorf("update server: holidays.json is not signed by the update server's key: %w", err)
		}
		switch at := time.Unix(issued, 0); {
		case now.Sub(at) > offerMaxAge:
			return nil, fmt.Errorf("update server: holidays.json was signed %s, too long ago", at.Format(time.DateOnly))
		case at.Sub(now) > 24*time.Hour:
			return nil, errors.New("update server: holidays.json is signed in the future (is the clock right?)")
		}
	}
	cal, err := ota.ParseHolidays(body)
	if err != nil {
		return nil, err
	}
	if old := loadHolidays(path); old != nil && issued < old.Issued {
		return nil, errors.New("update server: holidays.json is older than the calendar kept")
	}
	h := &holidayCache{Issued: issued, Fetched: now, Calendar: *cal}
	raw, _ := json.Marshal(h)
	return h, statefile.Write(path, raw, 0o600)
}
