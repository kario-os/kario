package shell

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// calendarStep: the script's steps around the calendar (headless.go).
func (r *Runner) calendarStep(cmd, arg string) error {
	c := r.C
	switch cmd {
	case "clock-at":
		d, err := clockAt(c.now(), arg)
		if err != nil {
			return err
		}
		c.setClockSkew(c.clockSkew + d)
		r.pump(300 * time.Millisecond)
	case "clock-reset":
		c.setClockSkew(0)
		r.pump(300 * time.Millisecond)
	case "calendar-import", "calendar-undo":
		name := strings.TrimSpace(arg)
		var id string
		for i := len(c.files.Items) - 1; i >= 0 && id == ""; i-- {
			if strings.Contains(c.files.Items[i].Name, name) {
				id = c.files.Items[i].ID
			}
		}
		if id == "" {
			return fmt.Errorf("no file in Files named like %q", name)
		}
		if cmd == "calendar-undo" {
			return c.UndoCalendarImport(id)
		}
		return c.ImportCalendar(id)
	case "scheduled":
		f := strings.Fields(arg)
		if len(f) < 2 || len(f) > 3 {
			return fmt.Errorf("scheduled <app-id> <n> [ms]")
		}
		app, ms := f[0], 5000
		want, err := strconv.Atoi(f[1])
		if len(f) == 3 && err == nil {
			ms, err = strconv.Atoi(f[2])
		}
		if err != nil {
			return fmt.Errorf("scheduled <app-id> <n> [ms]")
		}
		if _, ok := r.waitFor(func(string) bool { return len(c.ScheduledFor(app)) == want }, ms); !ok {
			return fmt.Errorf("%s has %d scheduled notifications, want %d: %+v", app, len(c.ScheduledFor(app)), want, c.ScheduledFor(app))
		}
	case "notified":
		rest, ms := lastNum(arg, 8000)
		app, text, _ := strings.Cut(strings.TrimSpace(rest), " ")
		if app == "" || strings.TrimSpace(text) == "" {
			return fmt.Errorf("notified <app-id> <text> [ms]")
		}
		text = strings.TrimSpace(text)
		if _, ok := r.waitFor(func(string) bool { return c.hasNotif(app, text) }, ms); !ok {
			return fmt.Errorf("%s has no notification holding %q", app, text)
		}
	}
	return nil
}

// clockAt is how far to move a clock reading now so that it reads the time
// of day hh:mm[:ss] of the same date.
func clockAt(now time.Time, hhmm string) (time.Duration, error) {
	hhmm = strings.TrimSpace(hhmm)
	at, err := time.Parse("15:04:05", hhmm)
	if err != nil {
		at, err = time.Parse("15:04", hhmm)
	}
	if err != nil {
		return 0, fmt.Errorf("usage: clock-at <HH:MM>[:SS]")
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), at.Hour(), at.Minute(), at.Second(), 0, now.Location())
	return day.Sub(now), nil
}

// hasNotif: the notification centre has one of the app's holding text.
func (c *Core) hasNotif(app, text string) bool {
	for _, n := range c.notifs {
		if n.app != nil && n.app.M.ID == app && strings.Contains(n.n.Title+"\n"+n.n.Body, text) {
			return true
		}
	}
	return false
}
