package shell

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/ota"
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

// updateServerKey is the update server's public key (nil without one).
func updateServerKey(path string) (ed25519.PublicKey, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	k, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || derr != nil || len(k) != ed25519.PublicKeySize {
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
func (c *Core) fetchHolidays(url string, key ed25519.PublicKey, done func(error)) {
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
func fetchHolidayFile(url string, key ed25519.PublicKey, path string, now time.Time) (*holidayCache, error) {
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
		issued, err = strconv.ParseInt(resp.Header.Get("X-Offer-Issued"), 10, 64)
		sig, serr := base64.StdEncoding.DecodeString(resp.Header.Get("X-Offer-Signature"))
		if err != nil || serr != nil || !ed25519.Verify(key, ota.DataMessage(ota.HolidaysFile, issued, body), sig) {
			return nil, errors.New("update server: holidays.json is not signed by the update server's key")
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path+".tmp", raw, 0o600); err != nil {
		return nil, err
	}
	return h, os.Rename(path+".tmp", path)
}
