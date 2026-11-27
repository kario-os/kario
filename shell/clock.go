package shell

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	// The shell shows the time on every image: Go's copy of the tz
	// database stands in for zoneinfo the image lacks. Apps, the renderer
	// and the web processes read the image's /usr/share/zoneinfo by TZ.
	_ "time/tzdata"

	"github.com/godbus/dbus/v5"

	"github.com/kario-os/kario/hal/rtc"
	"github.com/kario-os/kario/ui"
)

// Time: the device's clock (the kernel's and the RTC) runs in UTC; the
// time zone is the shell's and every app's TZ. The zone in force is the
// user's choice, else the network's (NITZ offset matched against the
// zones of the network's country, then the region's), else the region's
// main zone. With "Set time automatically" the clock follows network time:
// NTP through systemd-timesyncd where the image has it (driven through
// systemd-timedated when that is there too) and NITZ while NTP has not
// synchronised; otherwise the user sets it, and the shell (root on the
// device) sets the system clock, and the RTC as the board keeps it (rtc.go).

// zones are the tz database's locations by name, each read once:
// settings.get lists every zone, on the event loop.
var zones sync.Map

// loadZone is time.LoadLocation, from zones. "Local" is not kept: it is
// the process's TZ, not a zone of the tz database.
func loadZone(name string) (*time.Location, error) {
	if loc, ok := zones.Load(name); ok {
		return loc.(*time.Location), nil
	}
	loc, err := time.LoadLocation(name)
	if err == nil && name != "Local" {
		kept, _ := zones.LoadOrStore(name, loc)
		loc = kept.(*time.Location)
	}
	return loc, err
}

// warmZones reads the zones zoneList lists into zones off the event loop,
// so that Settings' first page does not wait on the disk.
func warmZones() {
	for id := range regionData().zoneOf {
		_, _ = loadZone(id)
	}
}

// zoneInForce is the zone the user chose, else autoZone's.
func (c *Core) zoneInForce() (*time.Location, string) {
	if c.settings.Zone != "" {
		if loc, err := loadZone(c.settings.Zone); err == nil {
			return loc, "user"
		}
	}
	return c.autoZone()
}

// autoZone is the zone left to the system and where it comes from:
// "network" or "region".
func (c *Core) autoZone() (*time.Location, string) {
	n := c.locale.net
	country := regionByMCC(n.NetMCC)
	if n.NITZ != nil {
		now := time.Now()
		for _, r := range []string{country, c.region()} {
			if loc := zoneForOffset(r, n.NITZ, now); loc != nil {
				return loc, "network"
			}
		}
		return fixedZone(n.NITZ.Offset), "network"
	}
	if r := regionByCode(country); r != nil && len(r.Zones) > 0 {
		if loc, err := loadZone(r.Zones[0]); err == nil {
			return loc, "network"
		}
	}
	if !c.locale.netKnown && c.settings.NetZone != "" {
		if loc, err := loadZone(c.settings.NetZone); err == nil {
			return loc, "network"
		}
	}
	if r := regionByCode(c.region()); r != nil && len(r.Zones) > 0 {
		if loc, err := loadZone(r.Zones[0]); err == nil {
			return loc, "region"
		}
	}
	return time.UTC, "region"
}

// zoneForOffset is the zone of the region whose offset now is the
// network's (its main zone first), or nil.
func zoneForOffset(region string, n *nitz, now time.Time) *time.Location {
	r := regionByCode(region)
	if r == nil {
		return nil
	}
	for _, id := range r.Zones {
		loc, err := loadZone(id)
		if err != nil {
			continue
		}
		t := now.In(loc)
		if _, off := t.Zone(); off == n.Offset*60 && (n.DST == 0) == !t.IsDST() {
			return loc
		}
	}
	return nil
}

// fixedZone is a zone at a fixed offset: the tz database's Etc/GMT zone for
// whole hours (POSIX signs: Etc/GMT+6 is six hours behind UTC), so apps can
// load it by name.
func fixedZone(minutes int) *time.Location {
	if minutes%60 == 0 && minutes >= -12*60 && minutes <= 14*60 {
		name := "Etc/GMT"
		if h := -minutes / 60; h != 0 {
			name = fmt.Sprintf("Etc/GMT%+d", h)
		}
		if loc, err := loadZone(name); err == nil {
			return loc
		}
	}
	return time.FixedZone(utcOffset(minutes*60), minutes*60)
}

// utcOffset writes an offset as the user reads it: "UTC+08:00".
func utcOffset(sec int) string {
	sign := "+"
	if sec < 0 {
		sign, sec = "\u2212", -sec
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, sec/3600, sec%3600/60)
}

// applyZone puts the zone in force into the shell and, for the processes
// it starts from now on, into TZ. The shell's own clock (status bar, idle
// screen, alarms, notification times) reads c.zone(): time.Local is the
// process's and is never replaced, as other goroutines read it at any time.
func (c *Core) applyZone() {
	loc, from := c.zoneInForce()
	c.locale.zoneFrom = from
	if from == "network" && loc.String() != c.settings.NetZone && strings.Contains(loc.String(), "/") {
		c.settings.NetZone = loc.String()
		c.saveSettings()
	}
	old := c.locale.zone.Load()
	if old != nil && old.String() == loc.String() {
		return
	}
	boot := old == nil
	c.locale.zone.Store(loc)
	c.log.Printf("time zone %s (%s)", loc, from)
	c.refreshSystemScreens()
	if !boot {
		for _, a := range c.apps {
			if a.running && a.ready {
				a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventTimeZone, Value: loc.String()})
			}
		}
	}
}

// zone is the zone in force, the process's before the first applyZone;
// any goroutine may ask.
func (c *Core) zone() *time.Location {
	if loc := c.locale.zone.Load(); loc != nil {
		return loc
	}
	return time.Local
}

// zoneName is TZ for the processes the shell starts.
func (c *Core) zoneName() string {
	if loc := c.locale.zone.Load(); loc != nil {
		return loc.String()
	}
	return "UTC"
}

// ZoneName is the time zone in force (selftest, dump).
func (c *Core) ZoneName() string { return c.zoneName() }

// The words of dates: tables of English, translated where they are used.
var (
	weekdayNames = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	monthNames   = [...]string{"January", "February", "March", "April", "May", "June", "July", "August", "September",
		"October", "November", "December"}
)

// dateText is a date as the language writes it, in t's zone: its layout is
// a message whose translation places {weekday}, {month} (the month's name),
// {m} (its number), {day} and {year} as the language does.
func dateText(t time.Time) string {
	return strings.NewReplacer("{weekday}", tr.T(weekdayNames[t.Weekday()]), "{month}", tr.T(monthNames[t.Month()-1]),
		"{m}", strconv.Itoa(int(t.Month())), "{day}", strconv.Itoa(t.Day()), "{year}", strconv.Itoa(t.Year())).
		Replace(tr.T("{weekday}, {month} {day}"))
}

// clockText is a time of day on the clock the user chose: 24-hour, or
// 12-hour with the language's marks for morning and afternoon.
func (c *Core) clockText(t time.Time) string {
	t = t.In(c.zone())
	if !c.settings.Clock12 {
		return t.Format("15:04")
	}
	h := t.Hour() % 12
	if h == 0 {
		h = 12
	}
	if t.Hour() < 12 {
		return tr.T("%d:%02d AM", h, t.Minute())
	}
	return tr.T("%d:%02d PM", h, t.Minute())
}

// zoneEntry is one zone of Settings › Time zone: its city in the language
// and its offset now.
type zoneEntry struct {
	ID     string `json:"id"`
	City   string `json:"city"`
	Offset string `json:"offset"`
}

// zoneList is every zone Settings offers (the tz database's, by ID).
func zoneList(lang string, now time.Time) []zoneEntry {
	ids := make([]string, 0, len(regionData().zoneOf))
	for id := range regionData().zoneOf {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]zoneEntry, 0, len(ids))
	for _, id := range ids {
		loc, err := loadZone(id)
		if err != nil {
			continue
		}
		_, off := now.In(loc).Zone()
		out = append(out, zoneEntry{ID: id, City: zoneCity(id, lang), Offset: utcOffset(off)})
	}
	return out
}

// Network time. timeSync is what the image has for NTP: "timedated"
// (systemd-timedated, which runs systemd-timesyncd and sets the RTC),
// "timesyncd" alone, or "" (NITZ only). Only a shell that owns the clock
// (canSetClock) touches it: in the chroot kit the image's timedated is
// there, but the system bus and the clock are the host's.
func detectTimeSync() string {
	if !canSetClock() {
		return ""
	}
	for _, d := range []string{"/usr/lib/systemd", "/lib/systemd"} {
		if _, err := os.Stat(d + "/systemd-timedated"); err == nil {
			return "timedated"
		}
	}
	for _, d := range []string{"/usr/lib/systemd", "/lib/systemd"} {
		if _, err := os.Stat(d + "/systemd-timesyncd"); err == nil {
			return "timesyncd"
		}
	}
	return ""
}

// ntpSynced: timesyncd has synchronised the clock since boot.
var ntpSynced = func() bool {
	_, err := os.Stat("/run/systemd/timesync/synchronized")
	return err == nil
}

// canSetClock: the shell may set the clock here: root on the device, not
// in a chroot on another system (rtc.Ours).
var canSetClock = rtc.Ours

// applyTimeSync starts or stops NTP as "Set time automatically" says.
func (c *Core) applyTimeSync() {
	if c.locale.timeSync == "" {
		return
	}
	on, how := !c.settings.ManualTime, c.locale.timeSync
	go func() {
		bus, err := dbus.ConnectSystemBus()
		if err == nil {
			defer bus.Close()
			if how == "timedated" {
				err = bus.Object("org.freedesktop.timedate1", "/org/freedesktop/timedate1").
					Call("org.freedesktop.timedate1.SetNTP", 0, on, false).Err
			} else {
				method := map[bool]string{true: "StartUnit", false: "StopUnit"}[on]
				err = bus.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1").
					Call("org.freedesktop.systemd1.Manager."+method, 0, "systemd-timesyncd.service", "replace").Err
			}
		}
		c.Post(func() { c.log.Printf("network time (NTP through %s) on=%v: %v", how, on, err) })
	}()
}

// syncFromNITZ sets the clock from the network's time while NTP has not
// (automatic time, the device): a NITZ more than nitzSlack off.
func (c *Core) syncFromNITZ(n *nitz) {
	if n == nil || n.Time == 0 || c.settings.ManualTime || !canSetClock() || ntpSynced() {
		return
	}
	at := time.Unix(n.Time, 0).Add(time.Since(time.Unix(n.Received, 0)))
	if d := time.Until(at); d > -nitzSlack && d < nitzSlack {
		return
	}
	// Directly: timedated refuses SetTime while NTP is on, which is when
	// NITZ sets the clock.
	if err := c.setClock(at, "", "network"); err != nil {
		c.log.Printf("clock from the network's time (NITZ): %v", err)
		return
	}
	c.log.Printf("clock set from the network's time (NITZ): %s", at.UTC().Format(time.RFC3339))
}

const nitzSlack = 2 * time.Second

// setClock sets the system clock (how: as setSystemClock) and keeps the RTC
// with it (keepRTC; by: what set it).
func (c *Core) setClock(t time.Time, how, by string) error {
	if err := setSystemClock(t, how); err != nil {
		return err
	}
	c.keepRTC(how, by)
	return nil
}

// setSystemClock sets the kernel's clock: through timedated where there is
// one (which also writes the RTC where it can), else directly.
var setSystemClock = func(t time.Time, how string) error {
	if how == "timedated" {
		bus, err := dbus.ConnectSystemBus()
		if err != nil {
			return err
		}
		defer bus.Close()
		return bus.Object("org.freedesktop.timedate1", "/org/freedesktop/timedate1").
			Call("org.freedesktop.timedate1.SetTime", 0, t.UnixMicro(), false, false).Err
	}
	return rtc.SetSystem(t)
}

// setManualTime sets the clock to a wall-clock date and time in the zone
// in force, given as seconds since 1970-01-01 00:00 of that wall clock
// (settings.set "time": the app need not know the zone).
func (c *Core) setManualTime(wall float64) error {
	if !c.settings.ManualTime {
		return errors.New("turn automatic time off first")
	}
	if !canSetClock() {
		return errors.New("only the device's shell sets the clock")
	}
	w := time.Unix(int64(wall), 0).UTC()
	if w.Year() < 2020 || w.Year() > 2099 {
		return errors.New("the date must be between 2020 and 2099")
	}
	at := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, c.zone())
	if err := c.setClock(at, c.locale.timeSync, "user"); err != nil {
		return err
	}
	c.log.Printf("clock set by the user: %s", at.Format(time.RFC3339))
	c.refreshSystemScreens()
	return nil
}
