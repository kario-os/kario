package nfcd

import (
	"testing"
	"time"

	"github.com/kario-os/kario/hal/nfc"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// A fixture calendar, not an official one: Wednesday 2026-11-18 a day
// off, Saturday 2026-11-21 a workday.
var (
	fixtureOff     = []string{"2026-11-18"}
	fixtureWorkday = []string{"2026-11-21"}
)

func TestDayOffByCalendar(t *testing.T) {
	e := newEngine(nfc.NewSim(), t.TempDir(), func(svc.NFCEvent) {})
	day := func(d string) time.Time {
		v, _ := time.ParseInLocation(time.DateOnly, d, beijing)
		return v.Add(9 * time.Hour)
	}
	for d, off := range map[string]bool{"2026-11-18": false, "2026-11-21": true, "2026-11-22": true, "2026-11-23": false} {
		if got := e.dayOff(day(d)); got != off {
			t.Fatalf("no calendar: %s day off %v", d, got)
		}
	}
	e.setPolicy(svc.NFCPolicyArgs{ScreenOn: true, ZoneOffset: 8 * 3600, DaysOff: fixtureOff, Workdays: fixtureWorkday})
	for d, off := range map[string]bool{"2026-11-17": false, "2026-11-18": true, "2026-11-21": false, "2026-11-22": true} {
		if got := e.dayOff(day(d)); got != off {
			t.Fatalf("calendar: %s day off %v", d, got)
		}
	}
	// The date is the one where the tap was: 23:30 on the 17th in Beijing
	// is the 17th, though it is the 18th in Tokyo.
	if e.dayOff(time.Date(2026, 11, 17, 23, 30, 0, 0, beijing)) {
		t.Fatal("the evening before the day off counts as one")
	}
}

// Workday taps count on the make-up Saturday and not on the holiday
// Wednesday; a tap on the holiday shows among the days off.
func TestDoorOnHolidays(t *testing.T) {
	w := newWorld(t, t.TempDir())
	w.policy(true, false, walletCards...)
	w.e.mu.Lock()
	p := w.e.pol
	p.DaysOff, p.Workdays = fixtureOff, fixtureWorkday
	w.e.setPolicy(p)
	w.e.mu.Unlock()
	home := w.door("Home", "04A1B2C3")
	w.door("Office", "04D5E6F7")
	homeDoor := svc.NFCTapArgs{Kind: "door", Name: "home door", Accept: []string{"04A1B2C3"}}
	officeDoor := svc.NFCTapArgs{Kind: "door", Name: "office door", Accept: []string{"04D5E6F7"}}
	mon := time.Date(2026, 11, 16, 9, 0, 0, 0, beijing)
	day := 24 * time.Hour
	w.at(mon)
	w.tap(t, officeDoor, false, "Home")
	w.tap(t, officeDoor, true, "Office")
	w.at(mon.Add(day))
	w.tap(t, officeDoor, true, "Office")
	// The holiday Wednesday at 9:00: no taps on days off, the order.
	w.at(mon.Add(2 * day))
	w.tap(t, officeDoor, false, "Home")
	w.tap(t, officeDoor, true, "Office")
	w.at(mon.Add(2*day + 11*time.Hour))
	w.tap(t, homeDoor, true, "Home")
	// The make-up Saturday at 9:00 is a workday: the office card first.
	w.at(mon.Add(5 * day))
	w.tap(t, officeDoor, true, "Office")
	// The Sunday after is a day off: the holiday's 9:00 tap (Office) counts.
	w.at(mon.Add(6*day + 10*time.Minute))
	w.tap(t, officeDoor, true, "Office")
	st := w.must(svc.NFCState, nil).(ui.NFCState)
	for _, c := range st.Cards {
		if c.ID == home.ID && (len(c.Usual) != 1 || c.Usual[0] != ui.NFCHours{Weekend: true, From: 19 * 60, To: 21 * 60}) {
			t.Fatalf("Home's usual hours %+v", c.Usual)
		}
	}
}
