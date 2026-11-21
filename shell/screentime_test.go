package shell

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// stCore is the Shell with Settings, the phone, Messages and two other
// apps, all running, and the system's standby screen.
func stCore(t *testing.T) (*locTest, map[string]*App) {
	apps := map[string]*App{}
	var list []*App
	for _, id := range []string{appSettings, appDialer, appMessages, "com.phone.calc", "com.phone.snake"} {
		a := &App{M: &Manifest{ID: id, Name: id[len("com.phone."):], Permissions: []string{ui.PermSystem}}, running: true, ready: true, out: make(chan outbound, 64)}
		apps[id] = a
		list = append(list, a)
	}
	lt := newLocTest(t, list...)
	lt.c.sys = &App{M: &Manifest{ID: "system"}, system: true, running: true, ready: true}
	lt.c.sys.stack = []*View{newView(ui.Screen{ID: "sys.idle"})}
	lt.c.settings = defaultSettings()
	lt.c.ime = &imeClient{}
	lt.c.parental.asked = true
	return lt, apps
}

func (c *Core) sysTop() string {
	if top := c.sys.top(); top != nil {
		return top.S.ID
	}
	return ""
}

// The time on the screen goes to the app in front while the screen is on
// and the phone unlocked, by day, split at midnight.
func TestScreenTimeCountsTheAppOnScreen(t *testing.T) {
	lt, apps := stCore(t)
	c, calc := lt.c, apps["com.phone.calc"]
	c.setForeground(calc)
	c.clockSkew += 10 * time.Minute
	c.stCount()
	if got := c.stUsed(calc.M.ID); got < 10*time.Minute || got > 11*time.Minute {
		t.Fatalf("10 minutes in front: %v", got)
	}
	c.screen.off = true
	c.stCount()
	c.clockSkew += 5 * time.Minute
	c.stCount()
	c.screen.off = false
	c.keylock = true
	c.clockSkew += 5 * time.Minute
	c.stCount()
	c.keylock = false
	if got := c.stUsed(calc.M.ID); got > 11*time.Minute {
		t.Fatalf("counted with the screen off or locked: %v", got)
	}
	c.setForeground(nil)
	c.clockSkew += time.Hour
	c.stCount()
	if got := c.stUsed(calc.M.ID); got > 11*time.Minute {
		t.Fatalf("counted from the standby screen: %v", got)
	}

	midnight := time.Date(2026, 3, 2, 0, 0, 0, 0, time.Local)
	c.stCredit("com.phone.snake", midnight.Add(-20*time.Minute), midnight.Add(10*time.Minute))
	days := c.screenTime().Days
	if days["2026-03-01"]["com.phone.snake"] != 20*60000 || days["2026-03-02"]["com.phone.snake"] != 10*60000 {
		t.Fatalf("across midnight: %v", days)
	}
	c.saveScreenTime()
	raw, _ := os.ReadFile(c.stPath())
	if !strings.Contains(string(raw), `"com.phone.calc"`) {
		t.Fatalf("not kept: %s", raw)
	}
}

// Past its limit an app does not open: the system's screen says why and
// gives 15 more minutes (no PIN without minor mode). One in front is moved
// away when its time is up, after a warning five minutes before; the phone
// and Settings have no limit.
func TestLimitHoldsTheAppBack(t *testing.T) {
	lt, apps := stCore(t)
	c, calc := lt.c, apps["com.phone.calc"]
	done := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if c.screenTimeSetting(ui.SettingArgs{Key: ui.SettingScreenLimit, Text: appDialer, Value: 15}, func(err error) {
		if err == nil {
			t.Fatal("a limit for the phone")
		}
	}) != true {
		t.Fatal("not a screen time setting")
	}
	c.screenTimeSetting(ui.SettingArgs{Key: ui.SettingScreenLimit, Text: calc.M.ID, Value: 15}, done)
	c.Launch(calc, "")
	if c.fg != calc {
		t.Fatal("held back before its limit")
	}
	c.clockSkew += 11 * time.Minute
	c.screenTimeTick()
	if c.fg != calc || c.toast == nil || !strings.Contains(c.toast.body, "calc") {
		t.Fatalf("no warning before the limit: %+v", c.toast)
	}
	c.clockSkew += 5 * time.Minute
	c.screenTimeTick()
	if c.fg != nil || c.sysTop() != "sys.held" || c.stime.heldWhy != heldLimit {
		t.Fatalf("still in front past the limit: fg %v, top %q", c.fg, c.sysTop())
	}
	c.sys.stack = c.sys.stack[:1]
	c.Launch(calc, "")
	if c.fg != nil || c.sysTop() != "sys.held" {
		t.Fatal("opened past its limit")
	}
	v := c.sys.top()
	more := v.S.Body[v.indexOf("held-more")]
	c.systemAction(v, &more, more.Actions[0])
	if c.fg != calc || c.held(calc) != "" {
		t.Fatalf("15 more minutes: fg %v, held %q", c.fg, c.held(calc))
	}
	c.clockSkew += 16 * time.Minute
	if c.stCount(); c.held(calc) != heldLimit {
		t.Fatal("the 15 minutes do not end")
	}
	c.Launch(apps[appSettings], "")
	if c.fg != apps[appSettings] {
		t.Fatal("Settings held back")
	}
	c.screenTimeSetting(ui.SettingArgs{Key: ui.SettingScreenLimit, Text: calc.M.ID, Value: 0}, done)
	if c.held(calc) != "" {
		t.Fatal("held back without a limit")
	}
}

// Downtime holds back every app but those allowed then, the phone,
// Settings and an app in a call; started by hand it lasts until its end,
// and it can be put off.
func TestDowntimeHoldsAllButTheAllowed(t *testing.T) {
	lt, apps := stCore(t)
	c := lt.c
	done := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	c.screenTimeSetting(ui.SettingArgs{Key: ui.SettingDowntimeNow, Value: 1}, done)
	for id, want := range map[string]string{"com.phone.calc": heldDowntime, appMessages: "", appDialer: "", appSettings: ""} {
		if got := c.held(apps[id]); got != want {
			t.Errorf("%s: held %q, want %q", id, got, want)
		}
	}
	c.calls = map[string]*callInfo{"v1": {id: "v1", app: apps["com.phone.snake"]}}
	if c.held(apps["com.phone.snake"]) != "" {
		t.Error("an app in a call is held back")
	}
	c.calls = map[string]*callInfo{}
	c.screenTimeSetting(ui.SettingArgs{Key: ui.SettingDowntimeApp, Text: "com.phone.calc", Value: 1}, done)
	if c.held(apps["com.phone.calc"]) != "" {
		t.Error("an app allowed in downtime is held back")
	}
	c.Launch(apps["com.phone.snake"], "")
	v := c.sys.top()
	if c.fg != nil || v.S.ID != "sys.held" {
		t.Fatal("opened in downtime")
	}
	day := v.S.Body[v.indexOf("held-day")]
	c.systemAction(v, &day, day.Actions[0])
	if c.fg != apps["com.phone.snake"] || c.screenTime().Downtime.active(c.now()) {
		t.Fatal("downtime not ended")
	}

	at := func(h, m int) time.Time { return time.Date(2026, 3, 2, h, m, 0, 0, time.Local) }
	d := downtime{On: true, Start: 22 * 60, End: 7 * 60}
	for _, tc := range []struct {
		t    time.Time
		want bool
	}{{at(23, 0), true}, {at(6, 59), true}, {at(7, 0), false}, {at(12, 0), false}, {at(22, 0), true}} {
		if got := d.active(tc.t); got != tc.want {
			t.Errorf("%s: %v", tc.t.Format(time.Kitchen), got)
		}
	}
	if end := d.endsAt(at(23, 0)); !end.Equal(at(31, 0)) {
		t.Errorf("ends at %v", end)
	}
	d.Skip = at(23, 15).UnixMilli()
	if d.active(at(23, 10)) || !d.active(at(23, 20)) {
		t.Error("put off")
	}
}
