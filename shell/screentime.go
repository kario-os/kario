package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/kario-os/kario/ui"
)

// Screen time (Settings › Screen time, 19-core-apps.md): how long each app
// is on the screen, by day; a daily limit per app, past which it does not
// open (a system screen says so and offers more time, which in minor mode
// takes a parent's PIN, or asking the parent remotely, family_ask.go); and
// downtime, a daily stretch in which only the apps allowed then and calls
// work. The phone app, Settings and an app's call in progress are never
// held back, emergency calls never refused.
// Time counts while the app is in front with the screen on and the phone
// unlocked; apps in the background (music) are not held back.

const (
	screenTimeDays = 14
	stSaveEvery    = time.Minute
	stWarnBefore   = 5 * time.Minute
	stMore         = 15 // minutes of "15 more minutes"
)

// stLimitChoices are the daily limits Settings offers, in minutes.
var stLimitChoices = []int{15, 30, 60, 120, 180}

// Why an app is held back (held).
const (
	heldLimit    = "limit"
	heldDowntime = "downtime"
	heldMinor    = "minor"
)

// downtime is the daily stretch, in minutes after midnight (Start == End:
// none); Apps are allowed in it besides those never held back. Until:
// downtime started by hand lasts until then; Skip: it is put off until
// then (Unix ms).
type downtime struct {
	On    bool     `json:"on,omitempty"`
	Start int      `json:"start"`
	End   int      `json:"end"`
	Apps  []string `json:"apps"`
	Until int64    `json:"until,omitempty"`
	Skip  int64    `json:"skip,omitempty"`
}

// screenTime is state/shell/screen-time.json. Days: the milliseconds on
// the screen by local date and app; Limits: minutes a day by app; More:
// minutes given past the limit on MoreDay; Pass: a parent's grant lets
// the app past downtime until its time on MoreDay reaches this (ms).
type screenTime struct {
	Days     map[string]map[string]int64 `json:"days"`
	Limits   map[string]int              `json:"limits,omitempty"`
	More     map[string]int              `json:"more,omitempty"`
	Pass     map[string]int64            `json:"pass,omitempty"`
	MoreDay  string                      `json:"more_day,omitempty"`
	Downtime downtime                    `json:"downtime"`
}

// stShell counts for app, the one on the screen when it last counted (at
// the monotonic reading mono, with the selftest's clock skew then).
type stShell struct {
	st     *screenTime
	app    *App
	mono   time.Time
	skew   time.Duration
	dirty  bool
	saved  time.Time
	warned map[string]string // app -> the day it was told its limit is near
	// held: the app the held-back screen (sys.held) is about, and why.
	held, heldWhy string
}

func defaultDowntime() downtime {
	return downtime{Start: 22 * 60, End: 7 * 60, Apps: []string{appMessages, appContacts, "com.phone.clock"}}
}

func (c *Core) stPath() string { return filepath.Join(c.cfg.StateDir, "shell", "screen-time.json") }

func (c *Core) screenTime() *screenTime {
	if c.stime.st == nil {
		st := &screenTime{Downtime: defaultDowntime()}
		if raw, err := os.ReadFile(c.stPath()); err == nil {
			_ = json.Unmarshal(raw, st)
		}
		if st.Days == nil {
			st.Days = map[string]map[string]int64{}
		}
		if st.Limits == nil {
			st.Limits = map[string]int{}
		}
		c.stime.st = st
	}
	return c.stime.st
}

func (c *Core) saveScreenTime() {
	raw, _ := json.Marshal(c.screenTime())
	_ = os.MkdirAll(filepath.Dir(c.stPath()), 0o700)
	_ = os.WriteFile(c.stPath(), raw, 0o600)
	c.stime.dirty, c.stime.saved = false, time.Now()
}

// screenTimeClear drops the daily limits, the time given past them and
// past downtime, and downtime; the time counted stays.
func (c *Core) screenTimeClear() {
	st := c.screenTime()
	st.Limits, st.More, st.Pass, st.MoreDay, st.Downtime = map[string]int{}, nil, nil, "", defaultDowntime()
	c.saveScreenTime()
}

func day(t time.Time) string { return t.Format(time.DateOnly) }

// onScreen is the app the user sees now: in front, the screen on, the
// phone unlocked.
func (c *Core) onScreen() *App {
	if c.screen.off || c.locked() {
		return nil
	}
	return c.fg
}

// stCount gives the time since it last counted to the app on the screen
// then: by the monotonic clock, which a corrected wall clock does not move,
// plus what the selftest's timeshift added.
func (c *Core) stCount() {
	t := &c.stime
	now := time.Now()
	if t.app != nil && !t.mono.IsZero() {
		if d := now.Sub(t.mono) + c.clockSkew - t.skew; d > 0 {
			end := c.now()
			c.stCredit(t.app.M.ID, end.Add(-d), end)
		}
	}
	t.app, t.mono, t.skew = c.onScreen(), now, c.clockSkew
}

// stCredit adds from..to to the app, split at midnights.
func (c *Core) stCredit(id string, from, to time.Time) {
	st := c.screenTime()
	for from.Before(to) {
		y, m, d := from.Date()
		end := time.Date(y, m, d+1, 0, 0, 0, 0, from.Location())
		if to.Before(end) {
			end = to
		}
		k := day(from)
		if st.Days[k] == nil {
			st.Days[k] = map[string]int64{}
			c.stPrune(from)
		}
		st.Days[k][id] += end.Sub(from).Milliseconds()
		from = end
	}
	c.stime.dirty = true
}

func (c *Core) stPrune(now time.Time) {
	oldest := day(now.AddDate(0, 0, -(screenTimeDays - 1)))
	for k := range c.screenTime().Days {
		if k < oldest {
			delete(c.stime.st.Days, k)
		}
	}
}

// ChargeScreenTime counts time on the screen today for the app as if it
// had been in front (selftest).
func (c *Core) ChargeScreenTime(id string, d time.Duration) {
	st, k := c.screenTime(), day(c.now())
	if st.Days[k] == nil {
		st.Days[k] = map[string]int64{}
	}
	st.Days[k][id] += d.Milliseconds()
	c.stime.dirty = true
}

// ClearScreenTime forgets the time counted on the screen, every day's
// (selftest: a case that shows the counts starts from none).
func (c *Core) ClearScreenTime() {
	c.stCount()
	c.screenTime().Days = map[string]map[string]int64{}
	c.stime.dirty = true
}

// stUsed is the app's time on the screen today.
func (c *Core) stUsed(id string) time.Duration {
	return time.Duration(c.screenTime().Days[day(c.now())][id]) * time.Millisecond
}

// stAllowed is the app's time today, the limit and what was given past it;
// 0 without a limit.
func (c *Core) stAllowed(id string) time.Duration {
	st := c.screenTime()
	lim := st.Limits[id]
	if lim <= 0 {
		return 0
	}
	if st.MoreDay == day(c.now()) {
		lim += st.More[id]
	}
	return time.Duration(lim) * time.Minute
}

// stPassed: a parent's grant still lets the app past downtime today.
func (c *Core) stPassed(id string) bool {
	st := c.screenTime()
	return st.MoreDay == day(c.now()) && c.stUsed(id).Milliseconds() < st.Pass[id]
}

// neverHeld: the phone and Settings, where minor mode is turned off.
func neverHeld(a *App) bool { return a.M.ID == appDialer || a.M.ID == appSettings }

// inWindow: the minute of the day falls in [start, end), which may span
// midnight.
func inWindow(m, start, end int) bool {
	if start <= end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// active: downtime is in force at now.
func (d downtime) active(now time.Time) bool {
	ms := now.UnixMilli()
	switch {
	case d.Skip > ms:
		return false
	case d.Until > ms:
		return true
	}
	return d.On && d.Start != d.End && inWindow(now.Hour()*60+now.Minute(), d.Start, d.End)
}

// dayMinute is the next time of day m (minutes after midnight) after now.
func dayMinute(now time.Time, m int) time.Time {
	y, mo, d := now.Date()
	t := time.Date(y, mo, d, m/60, m%60, 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// endsAt: when the downtime in force now ends (the next end of the
// schedule when none is).
func (d downtime) endsAt(now time.Time) time.Time {
	end := time.UnixMilli(d.Until)
	if d.On && d.Start != d.End && inWindow(now.Hour()*60+now.Minute(), d.Start, d.End) {
		end = later(end, dayMinute(now, d.End))
	}
	if !end.After(now) {
		end = dayMinute(now, d.End)
	}
	return end
}

// held says why the app may not come to the screen now, "" when it may.
func (c *Core) held(a *App) string {
	switch {
	case a == nil:
		return ""
	case c.minorHidden(a):
		return heldMinor
	case neverHeld(a) || c.callApp(a):
		return ""
	}
	if dt := c.screenTime().Downtime; dt.active(c.now()) && !slices.Contains(dt.Apps, a.M.ID) && !c.stPassed(a.M.ID) {
		return heldDowntime
	}
	if lim := c.stAllowed(a.M.ID); lim > 0 && c.stUsed(a.M.ID) >= lim {
		return heldLimit
	}
	return ""
}

// heldLabel is the menu tile's line for an app held back.
func heldLabel(why string) string {
	switch why {
	case heldLimit:
		return tr.T("Time limit reached for today")
	case heldDowntime:
		return tr.T("Downtime")
	}
	return ""
}

// holdLaunch shows why the app does not open instead of opening it; false
// when it may open.
func (c *Core) holdLaunch(a *App) bool {
	why := c.held(a)
	if why == "" {
		return false
	}
	c.showHeld(a, why)
	return true
}

func (c *Core) showHeld(a *App, why string) {
	c.log.Printf("%s held back (%s)", a.M.ID, why)
	c.stime.held, c.stime.heldWhy = a.M.ID, why
	c.setForeground(nil)
	c.sys.stack = append(c.sys.stack[:1], newView(c.heldScreen(a, why)))
}

// heldScreen is the system's screen for an app held back.
func (c *Core) heldScreen(a *App, why string) ui.Screen {
	s := ui.Screen{ID: "sys.held", Title: tr.T("Screen time"), Right: "held_ok",
		Actions: []ui.Action{{ID: "held_ok", Label: tr.T("OK")}}}
	text := tr.T("“%s” is not allowed in minor mode.", a.M.Name)
	var more []ui.Node
	item := func(id, title string) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: id, Title: title, Actions: []ui.Action{{ID: ui.ActionOpen, Label: title, Primary: true}}}
	}
	switch why {
	case heldLimit:
		text = tr.T("Today's limit for “%s” is used up: %s.", a.M.Name, durationText(c.stAllowed(a.M.ID)))
		more = []ui.Node{item("held-more", tr.T("15 more minutes")), item("held-day", tr.T("No limit for the rest of today"))}
	case heldDowntime:
		text = tr.T("Downtime until %s: only the apps allowed then, and calls.", c.clockText(c.screenTime().Downtime.endsAt(c.now())))
		more = []ui.Node{item("held-more", tr.T("15 more minutes")), item("held-day", tr.T("End downtime for now"))}
	}
	s.Body = []ui.Node{{Kind: ui.KindText, Style: ui.StyleBig, Text: a.M.Name}, {Kind: ui.KindText, Text: text}, item("held-ok", tr.T("OK"))}
	guarded := len(more) > 0 && c.minorOn()
	if guarded {
		if ask, ok := c.familyAskItem(a); ok {
			s.Body = append(s.Body, ask)
		}
	}
	s.Body = append(s.Body, more...)
	if guarded {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleDim, Text: tr.T("More time needs a parent's PIN.")})
	}
	return s
}

// heldAction runs a choice of the held-back screen, or of the screen
// asking a parent for time over it; false for another screen's.
func (c *Core) heldAction(v *View, node *ui.Node, act ui.Action) bool {
	switch v.S.ID {
	case "sys.ask":
		c.familyAskAction(v, node, act)
		return true
	case "sys.held":
	default:
		return false
	}
	id, why := c.stime.held, c.stime.heldWhy
	choice := act.ID
	if node != nil && act.ID == ui.ActionOpen {
		choice = node.ID
	}
	if choice == "held_ok" || choice == "held-ok" {
		c.back()
		return true
	}
	a := c.appByID(id)
	if choice == "held-ask" && a != nil && why != heldMinor {
		c.familyAskStart(a, why)
		return true
	}
	if a == nil || (choice != "held-more" && choice != "held-day") || why == heldMinor {
		return true
	}
	grant := func() {
		c.giveMore(id, why, choice == "held-day")
		c.saveScreenTime()
		if top := c.sys.top(); top != nil && top.S.ID == "sys.held" {
			c.sys.stack = c.sys.stack[:1]
		}
		c.Launch(a, "")
	}
	if !c.minorOn() {
		grant()
		return true
	}
	c.askParentPIN("verify", tr.T("A parent's PIN gives “%s” more time.", a.M.Name), func(err error) {
		if err == nil {
			grant()
		}
	})
	return true
}

// giveMore puts the limit or downtime off: by stMore minutes, or for the
// rest of the day (the limit) or of this downtime.
func (c *Core) giveMore(id, why string, rest bool) {
	st, now := c.screenTime(), c.now()
	if why == heldDowntime {
		end := st.Downtime.endsAt(now)
		st.Downtime.Skip = now.Add(stMore * time.Minute).UnixMilli()
		if rest {
			st.Downtime.Until, st.Downtime.Skip = 0, end.UnixMilli()
		}
		c.log.Printf("downtime put off until %s", time.UnixMilli(st.Downtime.Skip).Format(time.TimeOnly))
		return
	}
	c.stMoreToday()
	delete(c.stime.warned, id)
	if rest {
		st.More[id] += 24 * 60
	} else {
		used := int(c.stUsed(id) / time.Minute)
		st.More[id] = max(st.More[id], used-st.Limits[id]) + stMore
	}
	c.log.Printf("%s: %d more minutes today", id, st.More[id])
}

// stMoreToday starts the day's More and Pass afresh on a new day.
func (c *Core) stMoreToday() {
	st, today := c.screenTime(), day(c.now())
	if st.MoreDay != today {
		st.More, st.Pass, st.MoreDay = map[string]int{}, map[string]int64{}, today
	}
	if st.More == nil {
		st.More = map[string]int{}
	}
	if st.Pass == nil {
		st.Pass = map[string]int64{}
	}
}

// giveGranted gives the app minutes more today, granted by a parent
// remotely: past its limit, and past downtime, both counted from its time
// on the screen now.
func (c *Core) giveGranted(id string, minutes int) {
	st := c.screenTime()
	c.stMoreToday()
	delete(c.stime.warned, id)
	used := c.stUsed(id)
	if lim := st.Limits[id]; lim > 0 {
		st.More[id] = max(st.More[id], int(used/time.Minute)-lim) + minutes
	}
	st.Pass[id] = max(st.Pass[id], used.Milliseconds()) + int64(minutes)*time.Minute.Milliseconds()
	c.log.Printf("%s: %d more minutes today, granted", id, minutes)
}

// screenTimeTick counts, warns the app in front of its limit, and moves
// away from an app that may not stay: its limit or downtime came, or minor
// mode hides it.
func (c *Core) screenTimeTick() {
	c.minorStart()
	c.familyTick()
	c.stCount()
	a := c.onScreen()
	if why := c.held(a); why != "" {
		c.showHeld(a, why)
	} else if a != nil {
		c.stWarn(a)
	}
	if c.stime.dirty && time.Since(c.stime.saved) >= stSaveEvery {
		c.saveScreenTime()
	}
}

func (c *Core) stWarn(a *App) {
	lim := c.stAllowed(a.M.ID)
	if lim == 0 || c.stUsed(a.M.ID) < lim-stWarnBefore || c.stime.warned[a.M.ID] == day(c.now()) {
		return
	}
	if c.stime.warned == nil {
		c.stime.warned = map[string]string{}
	}
	c.stime.warned[a.M.ID] = day(c.now())
	c.showToast(tr.T("“%s”: %d minutes left today", a.M.Name, int((lim-c.stUsed(a.M.ID)+time.Minute-1)/time.Minute)))
}

// durationText is a time on the screen as the system writes it.
func durationText(d time.Duration) string {
	m := int(d / time.Minute)
	switch {
	case m < 1 && d > 0:
		return tr.T("Under a minute")
	case m < 60:
		return tr.T("%d min", m)
	case m%60 == 0:
		return tr.T("%d h", m/60)
	}
	return tr.T("%d h %d min", m/60, m%60)
}

// stApp is an app's row of Settings › Screen time.
type stApp struct {
	App   string `json:"app"`
	Name  string `json:"name"`
	Today int64  `json:"today_ms"`
	Week  int64  `json:"week_ms"`
	Limit int    `json:"limit,omitempty"`
	// Fixed: never held back (no limit, always allowed in downtime).
	Fixed bool `json:"fixed,omitempty"`
}

type stDay struct {
	Date  string `json:"date"`
	Label string `json:"label"`
	MS    int64  `json:"ms"`
}

// screenTimeView is settings.get's "screen_time": today's and this week's
// time (the week starting on the region's first day), the apps (the most
// used today first), the limits and downtime.
func (c *Core) screenTimeView() map[string]any {
	c.stCount()
	st, now := c.screenTime(), c.now()
	back := (int(now.Weekday()) - firstWeekday(c.region()) + 7) % 7
	var days []stDay
	week := map[string]int64{}
	for i := back; i >= 0; i-- {
		t := now.AddDate(0, 0, -i)
		var sum int64
		for id, ms := range st.Days[day(t)] {
			sum += ms
			week[id] += ms
		}
		days = append(days, stDay{Date: day(t), Label: dateText(t), MS: sum})
	}
	today := st.Days[day(now)]
	apps := []stApp{}
	for _, a := range c.apps {
		if c.minorHidden(a) {
			continue
		}
		apps = append(apps, stApp{App: a.M.ID, Name: a.M.Name, Today: today[a.M.ID], Week: week[a.M.ID], Limit: st.Limits[a.M.ID], Fixed: neverHeld(a)})
	}
	sort.SliceStable(apps, func(i, j int) bool { return apps[i].Today > apps[j].Today })
	dt := st.Downtime
	at := func(m int) string { return c.clockText(time.Date(2000, 1, 1, m/60, m%60, 0, 0, c.zone())) }
	return map[string]any{"days": days, "apps": apps, "limit_choices": stLimitChoices,
		"downtime": map[string]any{"on": dt.On, "start": dt.Start, "end": dt.End, "start_text": at(dt.Start), "end_text": at(dt.End),
			"apps": dt.Apps, "active": dt.active(now), "until": c.clockText(dt.endsAt(now))},
		"guarded": c.minorOn()}
}

// screenTimeSetting is settings.set for Settings › Screen time; false for
// another key. In minor mode every change takes the parental PIN.
func (c *Core) screenTimeSetting(args ui.SettingArgs, done func(error)) bool {
	st := c.screenTime()
	var change func() error
	switch args.Key {
	case ui.SettingScreenLimit:
		change = func() error {
			a := c.appByID(args.Text)
			switch m := int(args.Value); {
			case a == nil || neverHeld(a):
				return fmt.Errorf("no limit for %q", args.Text)
			case m <= 0:
				delete(st.Limits, a.M.ID)
			case m > 24*60 || float64(m) != args.Value:
				return errors.New("screen_limit takes minutes")
			default:
				st.Limits[a.M.ID] = m
			}
			return nil
		}
	case ui.SettingDowntime:
		change = func() error {
			st.Downtime.On = args.Value != 0
			if !st.Downtime.On {
				st.Downtime.Skip = 0
			}
			return nil
		}
	case ui.SettingDowntimeStart, ui.SettingDowntimeEnd:
		change = func() error {
			m := int(args.Value)
			if m < 0 || m >= 24*60 || float64(m) != args.Value {
				return errors.New("a time of day in minutes")
			}
			if args.Key == ui.SettingDowntimeStart {
				st.Downtime.Start = m
			} else {
				st.Downtime.End = m
			}
			return nil
		}
	case ui.SettingDowntimeNow:
		change = func() error {
			now := c.now()
			st.Downtime.Until, st.Downtime.Skip = 0, 0
			if args.Value != 0 {
				st.Downtime.Until = dayMinute(now, st.Downtime.End).UnixMilli()
			} else if st.Downtime.active(now) {
				st.Downtime.Skip = st.Downtime.endsAt(now).UnixMilli()
			}
			return nil
		}
	case ui.SettingDowntimeApp:
		change = func() error {
			a := c.appByID(args.Text)
			if a == nil || neverHeld(a) {
				return fmt.Errorf("%q is always allowed or no app", args.Text)
			}
			st.Downtime.Apps = slices.DeleteFunc(st.Downtime.Apps, func(id string) bool { return id == a.M.ID })
			if args.Value != 0 {
				st.Downtime.Apps = append(st.Downtime.Apps, a.M.ID)
			}
			return nil
		}
	default:
		return false
	}
	c.parentGate(tr.T("A parent's PIN is needed to change screen time."), func() {
		err := change()
		if err == nil {
			c.log.Printf("screen time: %s %q %v", args.Key, args.Text, args.Value)
			c.saveScreenTime()
			if c.minorOn() && args.Key != ui.SettingDowntimeNow {
				c.familyLocalChange()
			}
			c.refreshSystemScreens()
		}
		done(err)
	}, done)
	return true
}
