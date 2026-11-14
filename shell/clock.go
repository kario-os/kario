package shell

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	// The shell shows the time on every image: Go's copy of the tz
	// database stands in for zoneinfo the image lacks. Apps, the renderer
	// and the web processes read the image's /usr/share/zoneinfo by TZ.
	_ "time/tzdata"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// Time: the device's clock (the kernel's and the RTC) runs in UTC; the
// time zone is the shell's and every app's TZ. The zone in force is the
// user's choice, else the network's (NITZ offset matched against the
// zones of the network's country, then the region's), else the region's
// main zone. With "Set time automatically" the clock follows network time:
// NTP through systemd-timesyncd where the image has it (driven through
// systemd-timedated when that is there too) and NITZ while NTP has not
// synchronised; otherwise the user sets it, and the shell (root on the
// device) sets the system clock and the RTC.

// zoneInForce is the zone the user chose, else autoZone's.
func (c *Core) zoneInForce() (*time.Location, string) {
	if c.settings.Zone != "" {
		if loc, err := time.LoadLocation(c.settings.Zone); err == nil {
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
		if loc, err := time.LoadLocation(r.Zones[0]); err == nil {
			return loc, "network"
		}
	}
	if !c.locale.netKnown && c.settings.NetZone != "" {
		if loc, err := time.LoadLocation(c.settings.NetZone); err == nil {
			return loc, "network"
		}
	}
	if r := regionByCode(c.region()); r != nil && len(r.Zones) > 0 {
		if loc, err := time.LoadLocation(r.Zones[0]); err == nil {
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
		loc, err := time.LoadLocation(id)
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
		if loc, err := time.LoadLocation(name); err == nil {
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
// screen, alarms, notification times) reads time.Local.
func (c *Core) applyZone() {
	loc, from := c.zoneInForce()
	c.locale.zoneFrom = from
	if from == "network" && loc.String() != c.settings.NetZone && strings.Contains(loc.String(), "/") {
		c.settings.NetZone = loc.String()
		c.saveSettings()
	}
	if c.locale.zone != nil && c.locale.zone.String() == loc.String() {
		return
	}
	c.locale.zone = loc
	// Replaced on the loop goroutine; elsewhere it is only read (a word).
	time.Local = loc
	c.log.Printf("time zone %s (%s)", loc, from)
	c.refreshSystemScreens()
}

// zoneName is TZ for the processes the shell starts.
func (c *Core) zoneName() string {
	if c.locale.zone == nil {
		return "UTC"
	}
	return c.locale.zone.String()
}

// ZoneName is the time zone in force (selftest, dump).
func (c *Core) ZoneName() string { return c.zoneName() }

// The words of dates: tables of English, translated where they are used.
var (
	weekdayNames = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	monthNames   = [...]string{"January", "February", "March", "April", "May", "June", "July", "August", "September",
		"October", "November", "December"}
)

// dateText is a date as the language writes it: its layout is a message
// whose translation places {weekday}, {month} (the month's name), {m} (its
// number), {day} and {year} as the language does.
func dateText(t time.Time) string {
	t = t.In(time.Local)
	return strings.NewReplacer("{weekday}", tr.T(weekdayNames[t.Weekday()]), "{month}", tr.T(monthNames[t.Month()-1]),
		"{m}", strconv.Itoa(int(t.Month())), "{day}", strconv.Itoa(t.Day()), "{year}", strconv.Itoa(t.Year())).
		Replace(tr.T("{weekday}, {month} {day}"))
}

// clockText is a time of day on the clock the user chose: 24-hour, or
// 12-hour with the language's marks for morning and afternoon.
func (c *Core) clockText(t time.Time) string {
	t = t.In(time.Local)
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
		loc, err := time.LoadLocation(id)
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
// "timesyncd" alone, or "" (NITZ only). Only a shell running as root (the
// device) touches the clock.
func detectTimeSync() string {
	if os.Geteuid() != 0 {
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

// canSetClock: the shell may set the clock here (root: the device).
var canSetClock = func() bool { return os.Geteuid() == 0 }

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
	if err := setSystemClock(at, c.locale.timeSync); err != nil {
		c.log.Printf("clock from the network's time (NITZ): %v", err)
		return
	}
	c.log.Printf("clock set from the network's time (NITZ): %s", at.UTC().Format(time.RFC3339))
}

const nitzSlack = 2 * time.Second

// setSystemClock sets the kernel's clock and the RTC: through timedated
// where there is one, else directly.
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
	tv := unix.NsecToTimeval(t.UnixNano())
	if err := unix.Settimeofday(&tv); err != nil {
		return err
	}
	fd, err := unix.Open("/dev/rtc0", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil
	}
	defer unix.Close(fd)
	u := t.UTC()
	return unix.IoctlSetRTCTime(fd, &unix.RTCTime{Sec: int32(u.Second()), Min: int32(u.Minute()), Hour: int32(u.Hour()),
		Mday: int32(u.Day()), Mon: int32(u.Month()) - 1, Year: int32(u.Year()) - 1900})
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
	at := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, time.Local)
	if err := setSystemClock(at, c.locale.timeSync); err != nil {
		return err
	}
	c.log.Printf("clock set by the user: %s", at.Format(time.RFC3339))
	c.refreshSystemScreens()
	return nil
}
