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
	case "clock-reset":
		c.clockSkew = 0
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
	}
	return nil
}
