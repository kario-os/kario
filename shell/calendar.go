package shell

import (
	"errors"
	"time"

	"github.com/kario-os/kario/ui"
)

// appCalendar is the system's calendar app.
const appCalendar = "com.phone.calendar"

// calendarImportWake: an import parses a whole file; the app may need
// longer than a push's few seconds before it is frozen again.
const calendarImportWake = 10 * time.Second

// ImportCalendar hands a calendar file (.ics) of Files to the calendar app
// in the background: the file is granted to it, as sharing it would be,
// and it gets ui.LinkCalendarImport + the file's ID, started or woken for
// it outside the wake budget (the user asked for the import). The app
// reads the file (file.open with read), adds the events it lacks by UID,
// marks them with the file and says how many in a notification.
// UndoCalendarImport (ui.LinkCalendarUndo) takes out exactly those. Moving
// from an old phone (switch.go) can call both with the file it writes.
func (c *Core) ImportCalendar(fileID string) error {
	return c.toCalendar(fileID, ui.LinkCalendarImport)
}

// UndoCalendarImport asks the calendar app to remove what it imported
// from the file (the file itself may be gone by then).
func (c *Core) UndoCalendarImport(fileID string) error {
	return c.toCalendar(fileID, ui.LinkCalendarUndo)
}

func (c *Core) toCalendar(fileID, link string) error {
	a := c.appByID(appCalendar)
	if a == nil {
		return errors.New("this phone has no calendar app")
	}
	if link == ui.LinkCalendarImport {
		f := c.files.get(fileID)
		if f == nil {
			return errors.New("no such file")
		}
		c.files.grant(a.M.ID, f)
	}
	c.log.Printf("calendar: %s%s to %s", link, fileID, a.M.ID)
	c.deliverBackground(a, ui.Msg{T: ui.MsgEvent, Event: ui.EventOpen, Value: link + fileID}, calendarImportWake, true, "calendar file")
	return nil
}
