package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/audio"
	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/statefile"
	"github.com/kario-os/kario/ui"
)

// Alarms are kept and fired by the system (like Android's AlarmManager):
// apps are frozen in the background and may not be running at all, but the
// alarm screen still comes up on time. Production adds an RTC wake-up so
// alarms also fire from suspend.

type alarmEntry struct {
	App string `json:"app"`
	ui.Alarm
	Next time.Time `json:"next"`
	// Snoozed: Next is a snooze's, not the alarm's own time.
	Snoozed bool `json:"snoozed,omitempty"`
}

type alarmStore struct {
	path string
	list []*alarmEntry
	seq  int
}

func loadAlarms(path string, logf func(string, ...any)) *alarmStore {
	s := &alarmStore{path: path}
	readState(logf, path, &s.list)
	s.seq = len(s.list)
	return s
}

func (s *alarmStore) save() {
	raw, _ := json.MarshalIndent(s.list, "", "  ")
	_ = statefile.Write(s.path, raw, 0o600)
}

func nextAt(hhmm string, now time.Time) (time.Time, error) {
	var h, m int
	if _, err := fmt.Sscanf(strings.TrimSpace(hhmm), "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, errors.New("time must be HH:MM")
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t, nil
}

func (s *alarmStore) set(app string, a ui.Alarm, now time.Time) (ui.Alarm, error) {
	next, err := nextAt(a.Time, now)
	if err != nil {
		return a, err
	}
	if a.ID == "" {
		s.seq++
		a.ID = fmt.Sprintf("a%d", s.seq)
	}
	if !a.On {
		next = time.Time{}
	}
	for _, e := range s.list {
		if e.App == app && e.ID == a.ID {
			e.Alarm, e.Next, e.Snoozed = a, next, false
			s.save()
			return a, nil
		}
	}
	s.list = append(s.list, &alarmEntry{App: app, Alarm: a, Next: next})
	s.save()
	return a, nil
}

func (s *alarmStore) cancel(app, id string) bool {
	for i, e := range s.list {
		if e.App == app && e.ID == id {
			s.list = append(s.list[:i], s.list[i+1:]...)
			s.save()
			return true
		}
	}
	return false
}

func (s *alarmStore) of(app string) []ui.Alarm {
	out := []ui.Alarm{}
	for _, e := range s.list {
		if e.App == app {
			out = append(out, e.Alarm)
		}
	}
	return out
}

// due returns the alarms that should ring now and reschedules them.
func (s *alarmStore) due(now time.Time) []*alarmEntry {
	var out []*alarmEntry
	for _, e := range s.list {
		if e.Next.IsZero() || e.Next.After(now) {
			continue
		}
		out = append(out, e)
		e.Snoozed = false
		if e.Daily {
			e.Next, _ = nextAt(e.Time, now.Add(time.Minute))
		} else {
			e.On, e.Next = false, time.Time{}
		}
	}
	if len(out) > 0 {
		s.save()
	}
	return out
}

// now is the wall clock in the zone in force: Go's monotonic reading stops
// while the system sleeps, and alarms and jobs must count the time asleep.
func (c *Core) now() time.Time { return time.Now().Round(0).Add(c.clockSkew).In(c.zone()) }

// ago is how long before now the shell's clock read then. A reading after
// now (the clock set back since: by the user, the network, a selftest's
// next part) is long ago, so a check it timed runs again instead of
// waiting until the clock is back there.
func ago(now, then time.Time) time.Duration {
	if then.After(now) {
		return math.MaxInt64
	}
	return now.Sub(then)
}

// AlarmIn sets the clock app's alarm for d from the shell's clock, to the
// minute (selftest: without the app's pages).
func (c *Core) AlarmIn(d time.Duration, label string) error {
	_, err := c.alarms.set("com.phone.clock", ui.Alarm{Time: c.now().Add(d).Format("15:04"), Label: label, On: true}, c.now())
	return err
}

// SetClockSkew moves the shell's clock forward by d (selftest only).
func (c *Core) SetClockSkew(d time.Duration) { c.setClockSkew(c.clockSkew + d) }

// setClockSkew puts the shell's clock d ahead of the machine's (behind when
// negative), and the apps' with it (EventClock): an alarm, today's date or a
// scheduled message's minute must read the same in the app as in the shell.
func (c *Core) setClockSkew(d time.Duration) {
	if d == c.clockSkew {
		return
	}
	c.clockSkew = d
	for _, a := range c.apps {
		if a.running && a.ready {
			c.tellClock(a)
		}
	}
}

// tellClock tells an app the clock's offset when it knows another one; a
// frozen app hears it when it thaws.
func (c *Core) tellClock(a *App) {
	if a.clock == c.clockSkew {
		return
	}
	a.clock = c.clockSkew
	a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventClock, Value: strconv.FormatInt(c.clockSkew.Milliseconds(), 10)})
}

// clockEnv is the EnvClockOffset of an app starting now: none with the
// machine's clock.
func (c *Core) clockEnv(a *App) []string {
	a.clock = c.clockSkew
	if c.clockSkew == 0 {
		return nil
	}
	return []string{ui.EnvClockOffset + "=" + strconv.FormatInt(c.clockSkew.Milliseconds(), 10)}
}

func (c *Core) fireAlarms() {
	for _, e := range c.alarms.due(c.now()) {
		c.pushOverlay(&alarmOverlay{entry: e, at: time.Now()})
		c.sound("alarm:"+e.ID, "alarm", audio.Alarm)
		if a := c.appByID(e.App); a != nil && a.running {
			a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventAlarm, Value: e.ID})
		}
	}
}

type alarmOverlay struct {
	entry *alarmEntry
	at    time.Time
}

const snooze = 5 * time.Minute

func (o *alarmOverlay) key(c *Core, k Key) {
	switch k.Name {
	case KeyEnter:
		c.quiet("alarm:" + o.entry.ID)
		c.popOverlay(o)
	case KeyEsc, KeyF2:
		o.snooze(c)
	}
}

// snooze stops the alarm and rings it again in snooze: Back, the right
// soft key, and a short press of the power key (of the end key where the
// board has no power key, cardPickKey), as the power key does on Android.
func (o *alarmOverlay) snooze(c *Core) {
	c.quiet("alarm:" + o.entry.ID)
	c.popOverlay(o)
	o.entry.On, o.entry.Next, o.entry.Snoozed = true, c.now().Add(snooze), true
	c.alarms.save()
	c.showToast(tr.T("Rings again in 5 minutes"))
}

// alarmOnTop is the alarm ringing on top of everything, if one is.
func (c *Core) alarmOnTop() *alarmOverlay {
	if n := len(c.overlays); n > 0 {
		o, _ := c.overlays[n-1].(*alarmOverlay)
		return o
	}
	return nil
}

func (o *alarmOverlay) label() string { return firstNonEmpty(o.entry.Label, tr.T("Alarm")) }

func (o *alarmOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	bg := cv.RGB{R: 40, G: 30, B: 12}
	can.Fill(0, 1, W, H-1, st(colFG, bg))
	center := func(y int, s string, sty cv.Style) { can.Text((W-cv.StringWidth(s))/2, y, s, sty, W) }
	center(4, tr.T("The alarm is ringing"), st(colWarn, bg))
	center(7, o.entry.Time, bold(st(colFG, bg)))
	center(9, o.label(), st(colFG, bg))
	center(H-4, tr.T("Enter stop     Esc snooze (5 minutes)"), bold(st(colFG, bg)))
}

func (o *alarmOverlay) frame(c *Core) *FrameOverlay {
	phase, period := c.overlayPhase(o.at, alarmCycle)
	return &FrameOverlay{Kind: "alarm", Title: o.entry.Time, Lines: []string{o.label()},
		Choices: []string{tr.T("Stop"), tr.T("Snooze")}, Phase: phase, Period: period,
		Footer: []string{tr.T("OK stop · Back rings again in 5 minutes")}}
}

func (s *alarmStore) removeApp(app string) {
	keep := s.list[:0]
	for _, e := range s.list {
		if e.App != app {
			keep = append(keep, e)
		}
	}
	s.list = keep
	s.save()
}
