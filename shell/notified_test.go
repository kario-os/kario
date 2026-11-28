package shell

import (
	"io"
	"testing"

	"github.com/kario-os/kario/ui"
)

// The script's "notified" finds an app's notification in the centre after its
// banner is gone, and not another app's.
func TestNotifiedStep(t *testing.T) {
	lt, apps := stCore(t)
	c := lt.c
	c.addNotif(apps["com.phone.calc"], ui.Notification{ID: "import:1", Title: "Calendar", Body: "Imported 1 events from “Calendar 2030-01-09.ics”"})
	c.banner = nil
	r := &Runner{C: c, Out: io.Discard}
	if err := r.step("notified", "com.phone.calc Imported 1 events from “Calendar 50"); err != nil {
		t.Fatal(err)
	}
	if err := r.step("notified", "com.phone.snake Imported 1 events 50"); err == nil {
		t.Error("found another app's notification")
	}
	if err := r.step("notified", "com.phone.calc 50"); err == nil {
		t.Error("took the step without a text")
	}
}
