package shell

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// alarmCore is menuCore with the clock app's alarms; ring makes its alarm
// ring now, as the housekeeping does when it is due.
func alarmCore(t *testing.T) (*Core, func() *alarmEntry) {
	c := menuCore(t)
	c.alarms = loadAlarms(filepath.Join(c.cfg.StateDir, "alarms.json"), t.Logf)
	ring := func() *alarmEntry {
		t.Helper()
		if len(c.alarms.list) == 0 {
			if _, err := c.alarms.set("com.phone.clock", ui.Alarm{Time: c.now().Add(time.Minute).Format("15:04"), Label: "Up", On: true}, c.now()); err != nil {
				t.Fatal(err)
			}
		}
		e := c.alarms.list[0]
		c.SetClockSkew(e.Next.Sub(c.now()) + time.Second)
		c.fireAlarms()
		if c.alarmOnTop() == nil || !c.alarmRinging() {
			t.Fatal("the alarm does not ring")
		}
		return e
	}
	return c, ring
}

// A short press of the power key snoozes a ringing alarm, as on Android:
// before, it took the generic path and turned the screen off with the
// alarm still ringing. Held, the key still opens the power menu over the
// alarm, which keeps ringing; OK then stops it.
func TestPowerKeySnoozesTheAlarm(t *testing.T) {
	c, ring := alarmCore(t)
	e := ring()
	c.PowerKey(false)
	if c.ScreenOff() || c.alarmOnTop() != nil || c.alarmRinging() {
		t.Fatalf("a press while the alarm rang: screen off %v, alarm on screen %v, sounding %v", c.ScreenOff(), c.alarmOnTop() != nil, c.alarmRinging())
	}
	if c.toast == nil || c.toast.body != tr.T("Rings again in 5 minutes") {
		t.Fatalf("toast %+v", c.toast)
	}
	if d := e.Next.Sub(c.now()); !e.On || !e.Snoozed || d <= snooze-time.Minute || d > snooze {
		t.Fatalf("snoozed: on %v, snoozed %v, rings again in %s", e.On, e.Snoozed, d)
	}
	want := tr.T("Alarm %s (snoozed)", e.Next.Format("15:04")) + " Up"
	if lines := c.idleInfo().Lines; !slices.Contains(lines, want) {
		t.Fatalf("standby shows %q, not %q", lines, want)
	}
	// The press starts no double press: the next one turns the screen off.
	c.PowerKey(false)
	if !c.ScreenOff() {
		t.Fatal("the press after the snooze left the screen on")
	}
	c.PowerKey(false)

	ring()
	c.PowerKey(true)
	if _, ok := powerMenu(c); !ok || !c.alarmRinging() {
		t.Fatalf("a long press while the alarm rang: power menu %v, sounding %v", ok, c.alarmRinging())
	}
	c.PowerKey(false)
	if c.alarmOnTop() == nil || !c.alarmRinging() {
		t.Fatal("the press closing the power menu did something to the alarm")
	}
	c.Key(Named(KeyEnter))
	if c.alarmOnTop() != nil || c.alarmRinging() || e.On || e.Snoozed {
		t.Fatalf("OK: alarm on screen %v, sounding %v, on %v, snoozed %v", c.alarmOnTop() != nil, c.alarmRinging(), e.On, e.Snoozed)
	}
	if lines := c.idleInfo().Lines; slices.ContainsFunc(lines, func(l string) bool { return l == want || l == tr.T("Alarm %s", e.Time)+" Up" }) {
		t.Fatalf("a stopped alarm on standby: %q", lines)
	}
}

// Where the board has no power key the end key is the power key: over a
// ringing alarm its short press snoozes it (with the toast, never a
// silent stop) and its long press opens the power menu; before, both did
// nothing. Where the board has a power key the end key still leaves the
// alarm alone, and the volume keys set the volume the alarm rings at.
func TestEndKeyAsPowerKeySnoozesTheAlarm(t *testing.T) {
	c, ring := alarmCore(t)
	e := ring()
	c.Key(Named(KeyF4))
	if c.alarmOnTop() != nil || c.alarmRinging() || !e.On || !e.Snoozed {
		t.Fatalf("the end key: alarm on screen %v, sounding %v, on %v, snoozed %v", c.alarmOnTop() != nil, c.alarmRinging(), e.On, e.Snoozed)
	}
	if c.toast == nil || c.toast.body != tr.T("Rings again in 5 minutes") {
		t.Fatalf("toast %+v", c.toast)
	}

	ring()
	c.Key(Key{Name: KeyF4, Long: true})
	if _, ok := powerMenu(c); !ok || !c.alarmRinging() {
		t.Fatalf("holding the end key while the alarm rang: power menu %v, sounding %v", ok, c.alarmRinging())
	}
	c.Key(Named(KeyF4))
	if c.alarmOnTop() == nil || !c.alarmRinging() {
		t.Fatal("the end key closing the power menu did something to the alarm")
	}

	c.SetPowerKey(true)
	c.Key(Named(KeyF4))
	c.Key(Key{Name: KeyF4, Long: true})
	if c.alarmOnTop() == nil || !c.alarmRinging() {
		t.Fatal("with a power key of the board's own the end key did something to the alarm")
	}
	before := c.settings.RingVolume
	c.Key(Named(KeyVolDown))
	if c.alarmOnTop() == nil || !c.alarmRinging() || c.settings.RingVolume >= before {
		t.Fatalf("volume down: alarm on screen %v, sounding %v, ring volume %v from %v", c.alarmOnTop() != nil, c.alarmRinging(), c.settings.RingVolume, before)
	}
	c.Key(Named(KeyEsc))
	if c.alarmOnTop() != nil || !e.Snoozed {
		t.Fatal("Back did not snooze the alarm")
	}
}
