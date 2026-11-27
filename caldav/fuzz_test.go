package caldav

import (
	"testing"
	"time"
)

// What a server sends and what the app writes never make the system panic,
// and a patch it writes reads back with the phone's UID.
func FuzzPatch(f *testing.F) {
	f.Add([]byte(ev("a", "a", "ATTENDEE;CN=\"B; C\":mailto:b@x\r\n")), []byte(ev("a", "b", "ATTENDEE;PARTSTAT=DECLINED:mailto:B@x\r\n")))
	f.Add([]byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nRECURRENCE-ID;TZID=Nowhere:20261208T090000\r\nUID:a\r\n END:VEVENT\r\n"), []byte("BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:a\nEND:VEVENT\nEND:VCALENDAR"))
	f.Fuzz(func(t *testing.T, server, phone []byte) {
		if len(server)+len(phone) > 64<<10 {
			return
		}
		_ = UID(server)
		_ = HasEvents(server)
		_ = WithUID(server, "x")
		out, err := Patch(server, phone, time.Unix(0, 0))
		if err != nil {
			return
		}
		if uid := UID(phone); uid != "" && HasEvents(out) && UID(out) == "" {
			t.Fatalf("the patch lost the UID:\n%s", out)
		}
		if _, err := parse(out); err != nil {
			t.Fatalf("the patch does not read: %v\n%s", err, out)
		}
	})
}
