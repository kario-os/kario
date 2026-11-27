package shell

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/kario-os/kario/ui"
)

// What the hotspot's devices used (Settings › Personal hotspot): the
// access point interface's byte counters, looked at while it is on (every
// few seconds, when Settings asks, and as it goes off), added up for the
// session and for each day, the days kept in the Shell's state
// (hotspot-usage.json) for a month. What the interface receives is the
// devices' upload, what it sends their download. A counter that went
// down (the interface made again) counts from zero. The same bytes go into
// data usage as the hotspot's line (datausage.go). With a daily limit, the
// user is warned once a day at hotspotWarnPercent of it and the hotspot
// goes off once the devices reach it; turned on again that day, it stays
// on.

// hotspotUsageDays is how many days the file keeps.
const hotspotUsageDays = 31

type hotspotUsage struct {
	// Days: date (local, 2006-01-02) → bytes down and up; Session: since
	// the hotspot last came on.
	Days    map[string][2]uint64 `json:"days"`
	Session [2]uint64            `json:"session"`
	// Warned, Cut: the day and the limit of the last warning and of the
	// last time the limit turned the hotspot off.
	Warned string `json:"warned,omitempty"`
	Cut    string `json:"cut,omitempty"`
	// last: the counters (received, sent) at the last look; have: there
	// was one this session. saved: when the file was last written.
	last  [2]uint64
	have  bool
	saved time.Time
}

func (c *Core) hotspotUsagePath() string {
	return filepath.Join(c.cfg.StateDir, "shell", "hotspot-usage.json")
}

func (c *Core) loadHotspotUsage() {
	u := &c.hotspot.usage
	if raw, err := os.ReadFile(c.hotspotUsagePath()); err == nil {
		_ = json.Unmarshal(raw, u)
	}
	if u.Days == nil {
		u.Days = map[string][2]uint64{}
	}
}

// counterSample is a look at the access point's counters.
type counterSample struct {
	rx, tx uint64
	ok     bool
}

func sampleCounters(hs hotspotRadio) counterSample {
	rx, tx, err := hs.hotspotCounters()
	return counterSample{rx, tx, err == nil}
}

// hotspotRebase goes on counting the session from the counters of an
// access point made again (a new name or password).
func (c *Core) hotspotRebase(rx, tx uint64) {
	u := &c.hotspot.usage
	u.last, u.have = [2]uint64{rx, tx}, true
}

// hotspotSession starts the count of a session at the counters now.
func (c *Core) hotspotSession(rx, tx uint64, ok bool) {
	u := &c.hotspot.usage
	u.Session, u.last, u.have = [2]uint64{}, [2]uint64{rx, tx}, ok
	c.saveHotspotUsage(true)
}

// hotspotCount adds what the counters moved since the last look; final:
// the session ends, and the file is written whatever the time.
func (c *Core) hotspotCount(rx, tx uint64, final bool) {
	u := &c.hotspot.usage
	if u.Days == nil {
		u.Days = map[string][2]uint64{}
	}
	if u.have {
		up, down := since(rx, u.last[0]), since(tx, u.last[1])
		u.Session[0] += down
		u.Session[1] += up
		day := c.now().Format(time.DateOnly)
		d := u.Days[day]
		d[0] += down
		d[1] += up
		u.Days[day] = d
		c.hotspotData(down, up)
	}
	u.last, u.have = [2]uint64{rx, tx}, !final
	c.saveHotspotUsage(final)
	c.checkHotspotLimit()
}

// checkHotspotLimit warns as the devices near the daily limit and turns
// the hotspot off once they reach it, each once a day (again for a new
// limit).
func (c *Core) checkHotspotLimit() {
	limit := c.settings.HotspotDayLimit
	if limit == 0 {
		return
	}
	u := &c.hotspot.usage
	day := c.now().Format(time.DateOnly)
	used := u.Days[day][0] + u.Days[day][1]
	key := day + "/" + strconv.FormatUint(limit, 10)
	notify := func(title, body string) {
		if s := c.appByID(healthApp); s != nil {
			c.addNotif(s, ui.Notification{ID: ui.NotifHotspotLimit, Title: title, Body: body})
		}
	}
	switch {
	case used >= limit && u.Cut != key:
		u.Cut, u.Warned = key, key
		c.saveHotspotUsage(true)
		c.log.Printf("hotspot: the devices used %d of today's %d bytes", used, limit)
		notify(tr.T("Personal hotspot is off"), tr.T("The devices on it used %s today, the daily limit. Turned on again, it stays on for the rest of the day.", sizeText(used)))
		if c.settings.Hotspot {
			off := false
			c.setHotspot(ui.HotspotArgs{Enabled: &off}, func(err error) {
				if err != nil {
					c.log.Printf("hotspot: %v", err)
				}
			})
		}
	case used < limit && used >= limit*hotspotWarnPercent/100 && u.Warned != key:
		u.Warned = key
		c.saveHotspotUsage(true)
		notify(tr.T("Hotspot data warning"), tr.T("The devices on the hotspot used %s of today's %s; at the limit it turns off.", sizeText(used), sizeText(limit)))
	}
}

func since(now, then uint64) uint64 {
	if now < then {
		return now
	}
	return now - then
}

// saveHotspotUsage writes the file at most every half minute, unless now.
func (c *Core) saveHotspotUsage(now bool) {
	u := &c.hotspot.usage
	if !now && time.Since(u.saved) < 30*time.Second {
		return
	}
	days := make([]string, 0, len(u.Days))
	for d := range u.Days {
		days = append(days, d)
	}
	sort.Strings(days)
	for _, d := range days[:max(0, len(days)-hotspotUsageDays)] {
		delete(u.Days, d)
	}
	raw, err := json.Marshal(u)
	if err != nil {
		return
	}
	if err := os.WriteFile(c.hotspotUsagePath(), raw, 0o600); err != nil {
		c.log.Printf("hotspot: usage: %v", err)
		return
	}
	u.saved = time.Now()
}

func (c *Core) hotspotUsageView() ui.HotspotUsage {
	u := c.hotspot.usage
	today := u.Days[c.now().Format(time.DateOnly)]
	limit := c.settings.HotspotDayLimit
	return ui.HotspotUsage{SessionDown: u.Session[0], SessionUp: u.Session[1], TodayDown: today[0], TodayUp: today[1],
		LimitReached: limit > 0 && today[0]+today[1] >= limit}
}
