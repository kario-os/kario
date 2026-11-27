package shell

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

func schedCore(t *testing.T, path string, apps ...*App) *Core {
	t.Helper()
	c := &Core{log: log.New(io.Discard, "", 0), sys: &App{system: true, M: &Manifest{ID: "system"}}, apps: apps, alarms: &alarmStore{}}
	c.sys.stack = []*View{newView(ui.Screen{ID: "sys.idle"})}
	c.sched = loadSchedule(path, c.now(), c.zone)
	c.broker = &broker{c: c, perms: map[string]map[string]string{}, stats: map[string]*CapStat{}, usage: map[string]map[string]*permUse{}}
	return c
}

func calendarApp() *App {
	return &App{M: &Manifest{ID: "com.example.cal", Name: "Cal", Permissions: []string{ui.PermNotify}}, out: make(chan outbound, 16)}
}

func at(t time.Time) string { return t.Format(ui.ScheduledAtLayout) }

// A reminder an app scheduled comes from the system while the app is
// frozen: the notification is the system's, the app is not woken, and it
// hears of it when it runs again; the next minute nothing comes twice.
func TestScheduledFiresWhileFrozen(t *testing.T) {
	a := calendarApp()
	a.running, a.ready, a.frozen = true, true, true
	c := schedCore(t, filepath.Join(t.TempDir(), "scheduled.json"), a)
	due := time.Now().Add(2 * time.Minute).Truncate(time.Minute)
	n, err := c.sched.schedule(a.M.ID, ui.ScheduledNotif{ID: "r1", At: at(due), Title: "Meeting", Body: "09:00–10:00", Arg: "event:e1"}, c.now())
	if err != nil || n.Next != due.UnixMilli() {
		t.Fatalf("schedule: %+v %v", n, err)
	}
	if w := c.nextWake(c.now()); !w.Equal(due) {
		t.Fatalf("the phone would wake at %s, not for the reminder at %s", w, due)
	}
	c.fireScheduled()
	if len(c.notifs) != 0 {
		t.Fatal("shown before its time")
	}
	c.SetClockSkew(3 * time.Minute)
	c.fireScheduled()
	if len(c.notifs) != 1 || c.notifs[0].n.Title != "Meeting" || c.notifs[0].n.Body != "09:00–10:00" || c.notifs[0].arg != "event:e1" || c.notifs[0].app != a {
		t.Fatalf("notifications: %+v", c.notifs)
	}
	if !a.frozen {
		t.Fatal("the app was woken for it")
	}
	select {
	case m := <-a.out:
		if m.m.Event != ui.EventClock || m.m.Value != "180000" {
			t.Fatalf("the app heard %+v, not that the clock moved", m.m)
		}
	default:
		t.Fatal("the app was not told the clock moved")
	}
	select {
	case m := <-a.out:
		var f ui.NotifyFired
		if m.m.Event != ui.EventNotifyFired || json.Unmarshal([]byte(m.m.Value), &f) != nil || f.ID != "r1" || f.Missed {
			t.Fatalf("the app heard %+v", m.m)
		}
	default:
		t.Fatal("the app was not told (it reads the message once thawed)")
	}
	c.fireScheduled()
	if len(c.notifs) != 1 || len(c.ScheduledFor(a.M.ID)) != 0 {
		t.Fatalf("a one-shot reminder came twice or stayed: %d, %+v", len(c.notifs), c.ScheduledFor(a.M.ID))
	}
}

// Kept across a restart of the Shell; a reminder that came due while the
// phone was off comes at the next start, marked missed, and a daily one
// goes on from its next time.
func TestScheduledSurvivesRestartAndMissedAtBoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduled.json")
	a := calendarApp()
	c := schedCore(t, path, a)
	soon := time.Now().Add(10 * time.Minute)
	for _, n := range []ui.ScheduledNotif{
		{ID: "once", At: at(soon), Title: "Dentist"},
		{ID: "daily", At: at(soon.Add(time.Minute)), Repeat: ui.RepeatDaily, Title: "Pills"},
		{ID: "later", At: at(soon.Add(48 * time.Hour)), Zone: "Asia/Shanghai", Title: "Flight"},
	} {
		if _, err := c.sched.schedule(a.M.ID, n, c.now()); err != nil {
			t.Fatal(err)
		}
	}
	again := schedCore(t, path, a)
	if got := again.ScheduledFor(a.M.ID); len(got) != 3 || got[0].ID != "once" || got[2].Zone != "Asia/Shanghai" {
		t.Fatalf("after a restart: %+v", got)
	}
	// The phone is off for an hour: the next Shell starts after both were due.
	off := schedCore(t, path, a)
	off.clockSkew = time.Hour
	off.sched = loadSchedule(path, off.now(), off.zone)
	off.fireScheduled()
	if len(off.notifs) != 2 {
		t.Fatalf("%d notifications at boot, want the 2 missed", len(off.notifs))
	}
	mark, _, _ := strings.Cut(tr.T("Missed while the phone was off (due %s)", "\x00"), "\x00")
	for _, n := range off.notifs {
		if !strings.HasPrefix(n.n.Body, mark) {
			t.Fatalf("not marked missed: %+v", n.n)
		}
	}
	left := off.ScheduledFor(a.M.ID)
	if len(left) != 2 || left[0].ID != "daily" || time.UnixMilli(left[0].Next).Before(off.now()) {
		t.Fatalf("the daily one does not go on from its next time: %+v", left)
	}
	if want := soon.Add(time.Minute).AddDate(0, 0, 1).Truncate(time.Minute); left[0].Next != want.UnixMilli() {
		t.Fatalf("daily next %s, want %s", time.UnixMilli(left[0].Next), want)
	}
	// A Shell restarted within the minute shows a due one as it is.
	path2 := filepath.Join(t.TempDir(), "scheduled.json")
	c2 := schedCore(t, path2, a)
	due2 := time.Now().Add(2 * time.Minute).Truncate(time.Minute)
	if _, err := c2.sched.schedule(a.M.ID, ui.ScheduledNotif{ID: "now", At: at(due2), Title: "Soon"}, c2.now()); err != nil {
		t.Fatal(err)
	}
	quick := schedCore(t, path2, a)
	quick.clockSkew = time.Until(due2) + 20*time.Second
	quick.sched = loadSchedule(path2, quick.now(), quick.zone)
	quick.fireScheduled()
	if len(quick.notifs) != 1 || strings.HasPrefix(quick.notifs[0].n.Body, mark) {
		t.Fatalf("a reminder due during a restart: %+v", quick.notifs)
	}
}

// A file that is not JSON is moved aside, not written over.
func TestScheduledDamagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduled.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := schedCore(t, path)
	if len(c.sched.list) != 0 {
		t.Fatal("entries from a broken file")
	}
	if raw, err := os.ReadFile(path + ".damaged"); err != nil || string(raw) != "{not json" {
		t.Fatalf("the broken file is not kept aside: %v", err)
	}
}

// Wall times: a zone's clock over its daylight-saving changes, and a
// floating time that follows the phone's zone.
func TestScheduledWallTimes(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	for _, c := range []struct{ wall, utc string }{
		{"2026-03-08T02:30", "2026-03-08T07:30"}, // skipped: 03:30 EDT
		{"2026-11-01T01:30", "2026-11-01T05:30"}, // twice: the first, EDT
		{"2026-07-01T09:00", "2026-07-01T13:00"},
		{"2026-12-01T09:00", "2026-12-01T14:00"},
	} {
		w, _ := time.Parse(ui.ScheduledAtLayout, c.wall)
		if got := wallTime(w, ny).UTC().Format(ui.ScheduledAtLayout); got != c.utc {
			t.Errorf("%s New York is %s UTC, want %s", c.wall, got, c.utc)
		}
	}
	a := calendarApp()
	c := schedCore(t, filepath.Join(t.TempDir(), "s.json"), a)
	sh, _ := time.LoadLocation("Asia/Shanghai")
	c.locale.zone.Store(sh)
	next := time.Now().In(sh).AddDate(0, 0, 2)
	wall := time.Date(next.Year(), next.Month(), next.Day(), 8, 0, 0, 0, sh)
	if _, err := c.sched.schedule(a.M.ID, ui.ScheduledNotif{ID: "wake", At: at(wall), Repeat: ui.RepeatDaily, Title: "Up"}, c.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.sched.schedule(a.M.ID, ui.ScheduledNotif{ID: "call", At: at(wall), Zone: "Asia/Shanghai", Title: "Call"}, c.now()); err != nil {
		t.Fatal(err)
	}
	c.locale.zone.Store(ny)
	byID := map[string]time.Time{}
	for _, n := range c.ScheduledFor(a.M.ID) {
		byID[n.ID] = time.UnixMilli(n.Next)
	}
	if h := byID["wake"].In(ny); h.Hour() != 8 {
		t.Errorf("a floating 08:00 comes at %s in New York", h)
	}
	if !byID["call"].Equal(wall) {
		t.Errorf("08:00 in Shanghai moved to %s", byID["call"].In(sh))
	}
	for _, bad := range []ui.ScheduledNotif{
		{ID: "", At: at(wall), Title: "x"},
		{ID: "x", At: "tomorrow", Title: "x"},
		{ID: "x", At: at(wall), Zone: "Mars/Olympus", Title: "x"},
		{ID: "x", At: at(wall), Title: ""},
		{ID: "x", At: at(wall), Repeat: "hourly", Title: "x"},
		{ID: "x", At: "2000-01-01T00:00", Title: "x"},
		{ID: "x", At: at(wall), Title: "x", Arg: strings.Repeat("a", 300)},
	} {
		if _, err := c.sched.schedule(a.M.ID, bad, c.now()); err == nil {
			t.Errorf("%+v was taken", bad)
		}
	}
}

// Only an app with the notifications permission schedules; each has at
// most 64 waiting, and one app never sees or cancels another's.
func TestScheduledPermissionAndLimits(t *testing.T) {
	a := calendarApp()
	other := &App{M: &Manifest{ID: "com.example.other", Name: "O", Permissions: []string{ui.PermNotify}}}
	none := &App{M: &Manifest{ID: "com.example.none", Name: "N"}}
	c := schedCore(t, filepath.Join(t.TempDir(), "s.json"), a, other, none)
	call := func(app *App, cap string, args any) ui.Msg {
		raw, _ := json.Marshal(args)
		var got ui.Msg
		c.broker.handleFor(app, ui.Msg{T: ui.MsgReq, Req: 1, Cap: cap, Args: raw}, func(m ui.Msg, _ []*os.File) { got = m })
		return got
	}
	when := at(time.Now().Add(time.Hour))
	if r := call(none, ui.CapNotifySchedule, ui.ScheduledNotif{ID: "x", At: when, Title: "x"}); r.OK || !strings.Contains(r.Err, "permission") {
		t.Fatalf("without the permission: %+v", r)
	}
	for i := 0; i < ui.MaxScheduledNotifs; i++ {
		if r := call(a, ui.CapNotifySchedule, ui.ScheduledNotif{ID: "r" + string(rune('A'+i%26)) + string(rune('a'+i/26)), At: when, Title: "x"}); !r.OK {
			t.Fatalf("%d: %+v", i, r)
		}
	}
	if r := call(a, ui.CapNotifySchedule, ui.ScheduledNotif{ID: "one-too-many", At: when, Title: "x"}); r.OK {
		t.Fatal("past the limit")
	}
	if r := call(a, ui.CapNotifySchedule, ui.ScheduledNotif{ID: "rAa", At: when, Title: "replaced"}); !r.OK {
		t.Fatalf("replacing one at the limit: %+v", r)
	}
	if r := call(other, ui.CapNotifyCancel, ui.NotifyCancelArgs{All: true}); !r.OK || string(r.Data) != `{"cancelled":0}` || len(c.ScheduledFor(a.M.ID)) != ui.MaxScheduledNotifs {
		t.Fatalf("another app cancelled them: %+v", r)
	}
	var list []ui.ScheduledNotif
	r := call(other, ui.CapNotifyList, nil)
	if _ = json.Unmarshal(r.Data, &list); len(list) != 0 {
		t.Fatalf("another app sees them: %s", r.Data)
	}
	if r := call(a, ui.CapNotifyCancel, ui.NotifyCancelArgs{ID: "rAa"}); string(r.Data) != `{"cancelled":1}` {
		t.Fatalf("cancel one: %+v", r)
	}
	c.sched.removeApp(a.M.ID)
	if len(c.ScheduledFor(a.M.ID)) != 0 {
		t.Fatal("left after uninstalling")
	}
}

// time.offsets gives a zone's offsets and their changes.
func TestZoneOffsets(t *testing.T) {
	c := schedCore(t, filepath.Join(t.TempDir(), "s.json"))
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	z, err := c.sched.zoneOffsets(ui.ZoneOffsetsArgs{Zone: "America/New_York", From: from, To: from + 365*86400})
	if err != nil {
		t.Fatal(err)
	}
	want := []ui.ZoneSpan{
		{Start: time.Date(2025, 11, 2, 6, 0, 0, 0, time.UTC).Unix(), Offset: -5 * 3600},
		{Start: time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC).Unix(), Offset: -4 * 3600},
		{Start: time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC).Unix(), Offset: -5 * 3600},
	}
	if len(z.Spans) != len(want) {
		t.Fatalf("spans %+v", z.Spans)
	}
	for i := range want {
		if z.Spans[i] != want[i] {
			t.Errorf("span %d: %+v, want %+v", i, z.Spans[i], want[i])
		}
	}
	sh, err := c.sched.zoneOffsets(ui.ZoneOffsetsArgs{Zone: "Asia/Shanghai", From: from, To: from + 10*366*86400})
	if err != nil || len(sh.Spans) != 1 || sh.Spans[0].Offset != 8*3600 {
		t.Fatalf("Shanghai: %+v %v", sh, err)
	}
	for _, bad := range []ui.ZoneOffsetsArgs{{Zone: "Mars/Olympus", From: from, To: from}, {Zone: "", From: from, To: from}, {Zone: "UTC", From: from, To: from + 60*366*86400}, {Zone: "UTC", From: from, To: from - 1}} {
		if _, err := c.sched.zoneOffsets(bad); err == nil {
			t.Errorf("%+v was answered", bad)
		}
	}
}
