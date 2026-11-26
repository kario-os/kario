package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/ui"
)

// Scheduled notifications (notify.schedule): the Shell keeps them and
// shows them itself when they come due, so an app frozen or not running
// gets its reminder without being woken; one that runs hears of it
// (notify.fired). They live in one file, written whole and atomically
// with every change, and the phone's sleep wakes for the next one as for
// an alarm. Each keeps the wall time it is due next; the instant comes
// from that wall time on its zone's clock (the phone's for a floating
// one) whenever it is needed, so a change of zone moves floating
// reminders with it. One that came due before this Shell started by more
// than schedMissedAfter was missed (the phone was off): it is shown at the
// start, marked so, and a repeating one goes on from its next time.

const (
	schedMissedAfter = time.Minute
	maxSchedID       = 64
	maxSchedArg      = 256
)

type schedEntry struct {
	App string `json:"app"`
	ui.ScheduledNotif
	// Due is the wall time it comes next ("YYYY-MM-DDTHH:MM").
	Due string `json:"due"`
	// missed: due before the Shell started (not kept: it is shown then).
	missed bool
}

type schedStore struct {
	path  string
	list  []*schedEntry
	zones map[string]*time.Location
	// local is the phone's zone in force (Core.zone).
	local func() *time.Location
	// readOnly: the file is there but could not be read (permissions, an
	// I/O error); nothing is written over it.
	readOnly bool
}

// loadSchedule reads the store and marks what came due while no Shell
// ran. A file that is not JSON is moved aside (.damaged) and the store
// starts empty; one that cannot be read is left alone and never written.
func loadSchedule(path string, now time.Time, local func() *time.Location) *schedStore {
	s := &schedStore{path: path, zones: map[string]*time.Location{}, local: local}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s
	case err != nil:
		s.readOnly = true
		return s
	}
	if json.Unmarshal(raw, &s.list) != nil {
		_ = os.Rename(path, path+".damaged")
		s.list = nil
		return s
	}
	s.list = slices.DeleteFunc(s.list, func(e *schedEntry) bool { return e == nil || e.App == "" })
	for _, e := range s.list {
		if t, ok := s.instant(e); !ok || t.Before(now.Add(-schedMissedAfter)) {
			e.missed = ok
		}
	}
	return s
}

func (s *schedStore) save() {
	if s.readOnly {
		return
	}
	raw, _ := json.MarshalIndent(s.list, "", "  ")
	_ = contacts.WriteFile(s.path, raw)
}

// zone is the clock an entry's wall time is on: its IANA zone, or the
// phone's for a floating one.
func (s *schedStore) zone(name string) (*time.Location, error) {
	if name == "" {
		return s.local(), nil
	}
	if loc := s.zones[name]; loc != nil {
		return loc, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil || name == "Local" {
		return nil, fmt.Errorf("unknown time zone %q", name)
	}
	s.zones[name] = loc
	return loc, nil
}

func (s *schedStore) instant(e *schedEntry) (time.Time, bool) {
	loc, err := s.zone(e.Zone)
	if err != nil {
		return time.Time{}, false
	}
	w, err := time.Parse(ui.ScheduledAtLayout, e.Due)
	if err != nil {
		return time.Time{}, false
	}
	return wallTime(w, loc), true
}

// wallTime is when loc's clock shows w's date and time of day: in an hour
// a daylight-saving change repeats the first of the two, in one it skips
// as far past the change as w is past its start (02:30 in a 02:00-03:00
// gap comes at 03:30). time.Date leaves that choice open.
func wallTime(w time.Time, loc *time.Location) time.Time {
	y, mo, d := w.Date()
	h, mi := w.Hour(), w.Minute()
	t := time.Date(y, mo, d, h, mi, 0, 0, loc)
	var best time.Time
	for _, c := range []time.Time{t.Add(-time.Hour), t, t.Add(time.Hour)} {
		l := c.In(loc)
		if ly, lm, ld := l.Date(); ly == y && lm == mo && ld == d && l.Hour() == h && l.Minute() == mi && (best.IsZero() || c.Before(best)) {
			best = c
		}
	}
	if !best.IsZero() {
		return best
	}
	_, before := t.Add(-6 * time.Hour).In(loc).Zone()
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC).Add(-time.Duration(before) * time.Second)
}

// step is how far a repeating entry moves on: a day or a week of the
// calendar (not 24 hours: the clock keeps its time over a change).
func step(repeat string) int {
	if repeat == ui.RepeatWeekly {
		return 7
	}
	return 1
}

// nextDue is the first of w, w + step, ... whose instant is after now.
func (s *schedStore) nextDue(e *schedEntry, w time.Time, now time.Time) string {
	loc, _ := s.zone(e.Zone)
	// A repeat a year or more behind jumps close to now first.
	if days := int(now.Sub(w).Hours() / 24); days > 2*7 {
		n := step(e.Repeat)
		w = w.AddDate(0, 0, (days-n)/n*n)
	}
	for !wallTime(w, loc).After(now) {
		w = w.AddDate(0, 0, step(e.Repeat))
	}
	return w.Format(ui.ScheduledAtLayout)
}

func checkSchedArgs(n ui.ScheduledNotif) error {
	switch {
	case n.ID == "" || len(n.ID) > maxSchedID:
		return fmt.Errorf("a scheduled notification needs an id of 1 to %d bytes", maxSchedID)
	case n.Title == "":
		return errors.New("a scheduled notification needs a title")
	case len(n.Arg) > maxSchedArg:
		return fmt.Errorf("arg is at most %d bytes", maxSchedArg)
	case n.Repeat != "" && n.Repeat != ui.RepeatDaily && n.Repeat != ui.RepeatWeekly:
		return fmt.Errorf("repeat must be %q, %q or empty", ui.RepeatDaily, ui.RepeatWeekly)
	}
	return nil
}

// schedule keeps n for app (replacing the one with its ID) and answers it
// with when it comes.
func (s *schedStore) schedule(app string, n ui.ScheduledNotif, now time.Time) (ui.ScheduledNotif, error) {
	if err := checkSchedArgs(n); err != nil {
		return n, err
	}
	w, err := time.Parse(ui.ScheduledAtLayout, n.At)
	if err != nil {
		return n, errors.New("at must be YYYY-MM-DDTHH:MM")
	}
	if _, err := s.zone(n.Zone); err != nil {
		return n, err
	}
	n.Title, n.Body, n.Next = capRunes(n.Title, maxNotifTitle), capRunes(n.Body, maxNotifBody), 0
	e := &schedEntry{App: app, ScheduledNotif: n, Due: n.At}
	if n.Repeat != "" {
		e.Due = s.nextDue(e, w, now)
	} else if t, _ := s.instant(e); !t.After(now) {
		return n, errors.New("that time has passed")
	}
	i := slices.IndexFunc(s.list, func(x *schedEntry) bool { return x.App == app && x.ID == n.ID })
	if i < 0 && len(s.of(app)) >= ui.MaxScheduledNotifs {
		return n, fmt.Errorf("an app may have at most %d scheduled notifications", ui.MaxScheduledNotifs)
	}
	if i >= 0 {
		s.list[i] = e
	} else {
		s.list = append(s.list, e)
	}
	s.save()
	return s.answer(e), nil
}

func (s *schedStore) answer(e *schedEntry) ui.ScheduledNotif {
	n := e.ScheduledNotif
	if t, ok := s.instant(e); ok {
		n.Next = t.UnixMilli()
	}
	return n
}

// cancel takes back the app's entry with id, or all of them; how many.
func (s *schedStore) cancel(app, id string, all bool) int {
	before := len(s.list)
	s.list = slices.DeleteFunc(s.list, func(e *schedEntry) bool { return e.App == app && (all || e.ID == id) })
	if n := before - len(s.list); n > 0 {
		s.save()
		return n
	}
	return 0
}

// of lists the app's entries, the soonest first.
func (s *schedStore) of(app string) []ui.ScheduledNotif {
	out := []ui.ScheduledNotif{}
	for _, e := range s.list {
		if e.App == app {
			out = append(out, s.answer(e))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Next < out[j].Next })
	return out
}

// due takes the entries due by now (the missed first), moving repeating
// ones on and dropping the others.
func (s *schedStore) due(now time.Time) []schedEntry {
	var out []schedEntry
	changed := false
	keep := s.list[:0]
	for _, e := range s.list {
		t, ok := s.instant(e)
		if !ok || t.After(now) && !e.missed {
			keep = append(keep, e)
			continue
		}
		out = append(out, *e)
		changed = true
		e.missed = false
		if e.Repeat == "" {
			continue
		}
		w, _ := time.Parse(ui.ScheduledAtLayout, e.Due)
		e.Due = s.nextDue(e, w, now)
		keep = append(keep, e)
	}
	clear(s.list[len(keep):])
	s.list = keep
	if changed {
		s.save()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].missed && !out[j].missed })
	return out
}

// next is the soonest instant an entry comes due (zero for none).
func (s *schedStore) next() time.Time {
	var best time.Time
	for _, e := range s.list {
		if t, ok := s.instant(e); ok && (best.IsZero() || t.Before(best)) {
			best = t
		}
	}
	return best
}

func (s *schedStore) removeApp(app string) { s.cancel(app, "", true) }

// fireScheduled shows the notifications that came due, in their app's
// name, with the user's switch for its notifications respected.
func (c *Core) fireScheduled() {
	if c.sched == nil {
		return
	}
	for _, e := range c.sched.due(c.now()) {
		a := c.appByID(e.App)
		if a == nil {
			continue
		}
		n := ui.Notification{ID: e.ID, Title: e.Title, Body: e.Body}
		if e.missed {
			when := e.Due
			if w, err := time.Parse(ui.ScheduledAtLayout, e.Due); err == nil {
				when = fmt.Sprintf("%d-%02d-%02d %s", w.Year(), w.Month(), w.Day(), w.Format("15:04"))
			}
			n.Body = tr.T("Missed while the phone was off (due %s)", when)
			if e.Body != "" {
				n.Body += "\n" + e.Body
			}
		}
		limitNotif(&n)
		c.log.Printf("%s: scheduled notification %s due%s", a.M.ID, e.ID, map[bool]string{true: " (missed)"}[e.missed])
		c.notifyArg(a, n, e.Arg)
		if a.running {
			raw, _ := json.Marshal(ui.NotifyFired{ID: e.ID, Missed: e.missed})
			c.deliver(a, ui.Msg{T: ui.MsgEvent, Event: ui.EventNotifyFired, Value: string(raw)})
		}
	}
}

func (b *broker) dispatchSchedule(a *App, m ui.Msg, reply func(any, error)) {
	c := b.c
	switch m.Cap {
	case ui.CapNotifySchedule:
		var args ui.ScheduledNotif
		if err := json.Unmarshal(m.Args, &args); err != nil {
			reply(nil, errors.New("notify.schedule takes {id, at, zone?, repeat?, title, body?, arg?}"))
			return
		}
		reply(c.sched.schedule(a.M.ID, args, c.now()))
	case ui.CapNotifyCancel:
		var args ui.NotifyCancelArgs
		_ = json.Unmarshal(m.Args, &args)
		if args.ID == "" && !args.All {
			reply(nil, errors.New("notify.cancel takes {id} or {all: true}"))
			return
		}
		reply(map[string]int{"cancelled": c.sched.cancel(a.M.ID, args.ID, args.All)}, nil)
	case ui.CapNotifyList:
		reply(c.sched.of(a.M.ID), nil)
	}
}

// zoneOffsets is time.offsets: the spans of one offset of a zone between
// two instants, from the tz database the Shell carries.
func (s *schedStore) zoneOffsets(args ui.ZoneOffsetsArgs) (ui.ZoneOffsets, error) {
	out := ui.ZoneOffsets{Zone: args.Zone, Spans: []ui.ZoneSpan{}}
	if args.Zone == "" {
		return out, errors.New("time.offsets needs a zone")
	}
	loc, err := s.zone(args.Zone)
	if err != nil {
		return out, err
	}
	if args.To < args.From || args.To-args.From > ui.MaxZoneSpanYears*366*86400 {
		return out, fmt.Errorf("from and to are at most %d years apart", ui.MaxZoneSpanYears)
	}
	t := time.Unix(args.From, 0).In(loc)
	for len(out.Spans) < 4*ui.MaxZoneSpanYears+2 {
		_, off := t.Zone()
		start, end := t.ZoneBounds()
		at := t.Unix()
		if !start.IsZero() {
			at = start.Unix()
		}
		out.Spans = append(out.Spans, ui.ZoneSpan{Start: at, Offset: off})
		if end.IsZero() || end.Unix() > args.To {
			break
		}
		t = end.In(loc)
	}
	return out, nil
}

// ScheduledFor lists an app's scheduled notifications (tests, selftest).
func (c *Core) ScheduledFor(app string) []ui.ScheduledNotif { return c.sched.of(app) }
