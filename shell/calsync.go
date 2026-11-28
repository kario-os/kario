package shell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/kario-os/kario/caldav"
	"github.com/kario-os/kario/carddav"
	"github.com/kario-os/kario/statefile"
	"github.com/kario-os/kario/ui"
)

// The account's calendar (Settings › Accounts › Calendar; account.set
// calendar): CalDAV, package caldav, with the contacts' account and
// app-specific password. The Shell runs the sync because it holds the
// password (sealed by phone-keystored) and the account, and the calendar
// app cannot be given either; the app keeps its events in its own storage,
// which the Shell cannot read. So the events go between them as whole
// objects (an event and its changed occurrences, one UID: caldav.Queue,
// <state>/shell/calqueue-<id>.json): the server's changes wait in the
// Shell until the app takes them (calendar.pull, once LinkCalendarSync
// wakes it, or when it starts), the app's wait there until a run writes
// them (calendar.push: the app's offline queue). A run is the account's:
// after the contacts, with the same password, network rules and Sync now;
// a calendar's own trouble is its own (Settings shows it on its row) and
// its retries back off as the contacts' do. Turning it on finds the
// calendar and asks the app for all its events (WantAll: the first sync,
// where the server's version of an event both have by UID wins); turning
// it off drops the queue and the state, the events stay on the phone, and
// a running calendar app is told, as when the user signs out. A password
// the server refuses to the calendar is the account's refused, as to the
// contacts: all its syncs stop until the user acts.
// Whether the server schedules (RFC 6638: caldav.Client.Scheduling) is
// looked at by a run once a day; calendar.pull tells the app, which then
// leaves the invitations of its account's events to the server.

const (
	// A calendar.pull answer holds at most this many changes and about
	// this much iCalendar.
	calendarPage      = 50
	calendarPageBytes = 512 << 10
	// How often a run looks whether the server schedules.
	calendarScheduleCheck = 24 * time.Hour
)

type calendarShell struct {
	// state is the sync's while no run has it.
	state   *caldav.State
	queue   caldav.Queue
	finding bool
	// full: the app said it had no room for the server's events.
	full  bool
	dirty bool
}

func (c *Core) calStatePath(id string) string {
	return filepath.Join(c.cfg.StateDir, "shell", "caldav-"+id+".json")
}

func (c *Core) calQueuePath(id string) string {
	return filepath.Join(c.cfg.StateDir, "shell", "calqueue-"+id+".json")
}

// loadCalendar reads the calendar's state and queue for an account.
func (c *Core) loadCalendar(r *accountRecord) {
	cs := &c.accounts.cal
	*cs = calendarShell{}
	if !r.CalOn {
		return
	}
	cs.state = &caldav.State{}
	if raw, err := os.ReadFile(c.calStatePath(r.ID)); err == nil {
		if err := json.Unmarshal(raw, cs.state); err != nil {
			c.log.Printf("accounts: %s: the calendar's sync state did not parse (%v); syncing it from the start", r.ID, err)
			cs.state = &caldav.State{}
		}
	}
	if raw, err := os.ReadFile(c.calQueuePath(r.ID)); err == nil {
		if err := json.Unmarshal(raw, &cs.queue); err != nil {
			c.log.Printf("accounts: %s: the calendar's queue did not parse (%v); the app hands over its events again", r.ID, err)
			cs.queue = caldav.Queue{WantAll: true}
		}
	}
}

// calAccount is the account the calendar syncs with (one at most: Settings
// turns it on for one account only), nil for none.
func (a *accountsShell) calAccount() *accountRecord {
	for _, r := range a.file.Accounts {
		if r.CalOn {
			return r
		}
	}
	return nil
}

// saveCalQueue writes the queue (on the loop).
func (c *Core) saveCalQueue() error {
	a := &c.accounts
	r := a.calAccount()
	if r == nil {
		return nil
	}
	raw, err := json.Marshal(&a.cal.queue)
	if err != nil {
		return err
	}
	a.cal.dirty = false
	return statefile.Write(c.calQueuePath(r.ID), raw, 0o600)
}

// saveCalState writes a run's state, unless its account is gone (gen).
func (c *Core) saveCalState(id string, gen int, st *caldav.State) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	a := &c.accounts
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	if gen != a.genOf(id) {
		return errAccountGone
	}
	return statefile.Write(c.calStatePath(id), raw, 0o600)
}

// takeCalendarState is the calendar's state for a run of the account (nil
// when the calendar is off): the run has it until calendarDone.
func (c *Core) takeCalendarState(r *accountRecord) *caldav.State {
	cs := &c.accounts.cal
	if !r.CalOn {
		return nil
	}
	st := cs.state
	if st == nil {
		st = &caldav.State{}
	}
	cs.state = nil
	return st
}

// calendarRun is what a run did with the calendar.
type calendarRun struct {
	ran bool
	cal caldav.Calendar
	res caldav.Result
	err error
}

// calendarMayRun: the contacts' run ended so that the calendar's can go on
// (not a refused password, no network, the server down).
func calendarMayRun(err error) bool {
	return !errors.Is(err, carddav.ErrAuth) && !errors.Is(err, errKeystore) && !errors.Is(err, errAccountGone) && !carddav.Temporary(err)
}

// runCalendar is the calendar's part of a run, off the loop. A calendar
// the server no longer has at its address is looked for again, once.
func (c *Core) runCalendar(ctx context.Context, r accountRecord, server string, run *accountRun, st *caldav.State) calendarRun {
	out := calendarRun{ran: true, cal: r.Calendar}
	pass, err := c.openPassword(r)
	if err != nil {
		out.err = err
		return out
	}
	client := &caldav.Client{Client: &carddav.Client{User: r.User, Password: pass, UserAgent: Brand + " CalDAV"}}
	local := &loopCalendar{c: c, id: r.ID, gen: run.gen}
	s := &caldav.Sync{Client: client, Calendar: r.Calendar, Local: local, State: st,
		Save: func(st *caldav.State) error {
			if err := local.flush(); err != nil {
				return err
			}
			return c.saveCalState(r.ID, run.gen, st)
		},
		Notify: func(phase string, done, total int) {
			c.Post(func() { run.phase, run.done, run.total = phase, done, total })
		}}
	c.Post(func() { run.phase, run.done, run.total = caldav.PhaseCalendar, 0, 0 })
	scheduling := func() {
		if run.at.Sub(time.Unix(s.Calendar.Checked, 0)) < calendarScheduleCheck {
			return
		}
		if cal, err := client.Scheduling(ctx, s.Calendar); err == nil {
			cal.Checked = run.at.Unix()
			s.Calendar = cal
		}
	}
	scheduling()
	out.res, out.err = s.Run(ctx)
	if errors.Is(out.err, carddav.ErrNotFound) {
		if cal, derr := client.Discover(ctx, server); derr == nil {
			s.Calendar = cal
			scheduling()
			out.res, out.err = s.Run(ctx)
		}
	}
	if ferr := local.flush(); out.err == nil {
		out.err = ferr
	}
	out.cal = s.Calendar
	return out
}

// calendarDone takes the calendar's part of a run on the loop.
func (c *Core) calendarDone(run *accountRun, st *caldav.State, cr calendarRun) {
	a := &c.accounts
	if run.gen != a.genOf(run.id) {
		return
	}
	r := a.find(run.id)
	if r == nil || !r.CalOn {
		return
	}
	a.cal.state = st
	if !cr.ran {
		return
	}
	told := r.Calendar.Schedule != cr.cal.Schedule || !slices.Equal(r.Calendar.Addresses, cr.cal.Addresses)
	r.Calendar = cr.cal
	r.CalFailed, r.CalReason = cr.res.Failed, ""
	if cr.res.Problem != nil {
		r.CalReason = cardReason(cr.res.Problem)
	}
	now := c.now()
	if cr.err == nil {
		r.CalProblem, r.CalDetail, r.CalFailures = "", "", 0
		r.CalLastSync = now.Unix()
		c.log.Printf("accounts: %s calendar synced: %d to the app, %d deleted there, %d written and %d deleted on the server, %d conflicts, %d not written, %d scheduling messages read",
			r.ID, cr.res.Delivered, cr.res.Deleted, cr.res.Uploaded, cr.res.Removed, cr.res.Conflicts, cr.res.Failed, cr.res.Inbox)
	} else {
		r.CalProblem, r.CalDetail = problemOf(cr.err)
		if r.CalProblem == problemAuth {
			c.accountRefused(r)
		}
		if carddav.Temporary(cr.err) {
			r.CalFailures++
			wait := max(min(accountRetryMin<<min(r.CalFailures-1, 16), accountRetryMax), carddav.RetryAfter(cr.err))
			next, rt := now.Add(wait), a.rtOf(r.ID)
			if !rt.pending || next.After(rt.dueAt) {
				rt.pending, rt.dueAt = true, next
			}
		}
		c.log.Printf("accounts: %s calendar did not sync (%s %s)", r.ID, r.CalProblem, r.CalDetail)
	}
	if told {
		c.log.Printf("accounts: %s calendar: the server schedules %v", r.ID, r.Calendar.Schedule)
	}
	if len(a.cal.queue.Inbox) > 0 && !a.cal.full || a.cal.queue.WantAll || told {
		c.tellCalendarApp()
	}
	if err := c.saveAccounts(); err != nil {
		c.log.Printf("accounts: %v", err)
	}
}

// tellCalendarApp wakes the calendar app for the sync's changes
// (calendar.pull); it is the user's own account, so outside the wake budget.
func (c *Core) tellCalendarApp() {
	if a := c.appByID(appCalendar); a != nil {
		c.deliverBackground(a, ui.Msg{T: ui.MsgEvent, Event: ui.EventOpen, Value: ui.LinkCalendarSync}, calendarImportWake, true, "calendar sync")
	}
}

// loopCalendar is the queue for a run: each call is one step of the loop,
// none once the account is gone. The inbox is saved before the run saves
// the state that follows from it (a token past what was delivered would
// lose those changes); the outbox's sent changes go with the next save.
type loopCalendar struct {
	c   *Core
	id  string
	gen int
}

func (l *loopCalendar) on(f func()) error {
	ran := make(chan bool, 1)
	l.c.Post(func() {
		if l.c.accounts.genOf(l.id) != l.gen {
			ran <- false
			return
		}
		f()
		ran <- true
	})
	select {
	case ok := <-ran:
		if !ok {
			return errAccountGone
		}
		return nil
	case <-time.After(30 * time.Second):
		return errLoopBusy
	}
}

func (l *loopCalendar) Pending() ([]caldav.Change, error) {
	var out []caldav.Change
	err := l.on(func() { out = l.c.accounts.cal.queue.Pending() })
	return out, err
}

func (l *loopCalendar) Done(ch caldav.Change) error {
	return l.on(func() {
		l.c.accounts.cal.queue.Done(ch)
		l.c.accounts.cal.dirty = true
	})
}

func (l *loopCalendar) Queue(ch caldav.Change) error {
	return l.on(func() {
		l.c.accounts.cal.queue.Push(ch)
		l.c.accounts.cal.dirty = true
	})
}

func (l *loopCalendar) Deliver(chs []caldav.Change) error {
	var err error
	if oerr := l.on(func() {
		l.c.accounts.cal.queue.Deliver(chs)
		err = l.c.saveCalQueue()
	}); oerr != nil {
		return oerr
	}
	return err
}

func (l *loopCalendar) flush() error {
	var err error
	if oerr := l.on(func() {
		if l.c.accounts.cal.dirty {
			err = l.c.saveCalQueue()
		}
	}); oerr != nil {
		return oerr
	}
	return err
}

// systemCalendar: the system's calendar app, itself (not a page it hosts).
func systemCalendar(a *App, origin string) bool {
	return a.M.ID == appCalendar && !a.M.installed && origin == ""
}

func toCal(ch ui.CalChange) caldav.Change {
	return caldav.Change{UID: ch.UID, ICS: ch.ICS, Gone: ch.Gone, All: ch.All}
}

func fromCal(ch caldav.Change) ui.CalChange {
	return ui.CalChange{UID: ch.UID, ICS: ch.ICS, Gone: ch.Gone, Copy: ch.Copy, First: ch.First, Seq: ch.Seq}
}

var errNotCalendarApp = errors.New("only the system's calendar app syncs the calendar")

// dispatchCalendar serves calendar.pull and calendar.push.
func (c *Core) dispatchCalendar(a *App, m ui.Msg, reply func(any, error)) {
	if !systemCalendar(a, m.Origin) {
		reply(nil, errNotCalendarApp)
		return
	}
	acc := &c.accounts
	if !acc.loaded {
		c.loadAccounts()
	}
	r := acc.calAccount()
	on := r != nil
	q := &acc.cal.queue
	switch m.Cap {
	case ui.CapCalendarPull:
		var args ui.CalPullArgs
		_ = json.Unmarshal(m.Args, &args)
		out := ui.CalPull{Items: []ui.CalChange{}}
		if !on {
			reply(out, nil)
			return
		}
		q.Take(args.Taken)
		acc.cal.full = args.Full
		out.Account, out.WantAll, out.Schedule, out.Addresses = r.ID, q.WantAll, r.Calendar.Schedule, r.Calendar.Addresses
		if args.Full {
			out.More = len(q.Inbox) > 0
		} else {
			items, more := q.Offer(calendarPage, calendarPageBytes)
			for _, it := range items {
				out.Items = append(out.Items, fromCal(it))
			}
			out.More = more
		}
		reply(out, c.saveCalQueue())
	case ui.CapCalendarPush:
		var args ui.CalPushArgs
		_ = json.Unmarshal(m.Args, &args)
		if !on {
			reply(ui.CalPushRes{}, nil)
			return
		}
		for _, it := range args.Items {
			if it.UID == "" || len(it.UID) > 255 || len(it.ICS) > caldav.MaxObject || (!it.Gone && it.ICS == "") {
				reply(nil, errors.New("calendar.push: an item without a UID, or past the bounds"))
				return
			}
			if !q.Push(toCal(it)) {
				_ = c.saveCalQueue()
				reply(nil, errors.New(tr.T("Too many calendar changes wait for the server")))
				return
			}
		}
		if args.Done {
			q.WantAll = false
		}
		if len(args.Items) > 0 {
			c.accountsLocalChange()
		}
		reply(ui.CalPushRes{Account: r.ID, Pending: len(q.Outbox)}, c.saveCalQueue())
	}
}

// setCalendar turns the calendar's sync on (the calendar found first, with
// the account's password) or off.
func (c *Core) setCalendar(r *accountRecord, on bool, reply func(any, error)) {
	a := &c.accounts
	if !on {
		if r.CalOn {
			r.CalOn, r.CalProblem, r.CalDetail, r.CalFailed, r.CalFailures = false, "", "", 0, 0
			c.dropCalendarFiles(r.ID)
			a.cal = calendarShell{}
			c.log.Printf("accounts: %s calendar off", r.ID)
			c.calendarSyncEnded()
		}
		reply(c.accountState(), c.saveAccounts())
		return
	}
	if r.CalOn {
		reply(c.accountState(), nil)
		return
	}
	if a.cal.finding {
		reply(nil, errors.New(tr.T("Looking for the calendar already")))
		return
	}
	for _, x := range a.file.Accounts {
		if x != r && x.CalOn {
			reply(nil, errors.New(tr.T("The calendar syncs with %s already; turn it off there first.", hostOf(c.accountServer(x.Kind, x.Server), firstBookURL(x)))))
			return
		}
	}
	a.cal.finding = true
	rec, gen, server := *r, a.genOf(r.ID), c.accountServer(r.Kind, r.Server)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		pass, err := c.openPassword(rec)
		var cal caldav.Calendar
		if err == nil {
			client := &caldav.Client{Client: &carddav.Client{User: rec.User, Password: pass, UserAgent: Brand + " CalDAV"}}
			cal, err = client.Discover(ctx, server)
		}
		c.Post(func() {
			a.cal.finding = false
			r := a.find(rec.ID)
			if r == nil || gen != a.genOf(rec.ID) {
				reply(nil, errAccountGone)
				return
			}
			if err != nil {
				p, detail := problemOf(err)
				c.log.Printf("accounts: %s found no calendar on %s: %s %s", rec.ID, hostOf(server, firstBookURL(&rec)), p, detail)
				reply(nil, errors.New(calendarFindText(err, p, detail)))
				return
			}
			r.CalOn, r.Calendar, r.CalLastSync, r.CalProblem, r.CalDetail, r.CalFailed, r.CalFailures = true, cal, 0, "", "", 0, 0
			a.cal = calendarShell{state: &caldav.State{}, queue: caldav.Queue{WantAll: true}}
			if err := c.saveAccounts(); err != nil {
				r.CalOn = false
				reply(nil, err)
				return
			}
			_ = c.saveCalQueue()
			c.log.Printf("accounts: %s calendar on at %s (sync-collection %v)", r.ID, hostOf(cal.URL), cal.Sync)
			c.tellCalendarApp()
			rt := a.rtOf(r.ID)
			rt.pending, rt.forced, rt.dueAt, rt.netAt = true, true, c.now(), time.Time{}
			c.accountsTick()
			reply(c.accountState(), nil)
		})
	}()
}

func (c *Core) dropCalendarFiles(id string) {
	for _, p := range []string{c.calStatePath(id), c.calQueuePath(id)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.log.Printf("accounts: %v", err)
		}
	}
}

// signOutCalendar: signing out drops the calendar's state and queue; with
// removeEvents the app takes off the phone the events that came from the
// account (those made on the phone stay), and a running app is told the
// sync is over either way.
func (c *Core) signOutCalendar(r *accountRecord, removeEvents bool) {
	c.dropCalendarFiles(r.ID)
	if !r.CalOn {
		return
	}
	c.accounts.cal = calendarShell{}
	if removeEvents {
		if a := c.appByID(appCalendar); a != nil {
			c.deliverBackground(a, ui.Msg{T: ui.MsgEvent, Event: ui.EventOpen, Value: ui.LinkCalendarUndo + ui.LinkCalendarAccount + r.ID}, calendarImportWake, true, "calendar sign-out")
		}
	}
	c.calendarSyncEnded()
}

// calendarSyncEnded wakes a running calendar app when the sync ends
// (signed out, the calendar turned off): its pull says there is no
// account, so it stops leaving invitations to that server at once, the
// pages open too. One not running learns it from its pull at start.
func (c *Core) calendarSyncEnded() {
	if a := c.appByID(appCalendar); a != nil && a.running {
		c.tellCalendarApp()
	}
}

// calendarView is the calendar's sync as Settings sees it.
func (c *Core) calendarView(r *accountRecord) *ui.AccountCalendar {
	a := &c.accounts
	q := &a.cal.queue
	v := &ui.AccountCalendar{On: r.CalOn, Finding: a.cal.finding}
	if !r.CalOn {
		return v
	}
	v.Name, v.LastSync, v.Pending, v.Waiting, v.Full = r.Calendar.Name, r.CalLastSync, len(q.Outbox), len(q.Inbox), a.cal.full && len(q.Inbox) > 0
	if st := a.cal.state; st != nil {
		v.Events = st.Events()
	}
	switch {
	case a.run != nil && a.run.phase == caldav.PhaseCalendar:
		v.State = ui.AccountSyncing
	case r.CalProblem == problemAuth:
		v.State = ui.AccountAuth
	case r.CalProblem != "":
		v.State = ui.AccountFailed
	default:
		v.State = ui.AccountIdle
	}
	switch {
	case r.CalProblem == problemAuth && r.Device != "":
		v.Error = deviceAuthText()
	case r.CalProblem != "":
		v.Error = calendarProblemText(r.CalProblem, r.CalDetail)
	case r.CalFailed > 0:
		v.Error = eventReasonText(r.CalFailed, r.CalReason)
	}
	return v
}

func calendarProblemText(problem, detail string) string {
	switch problem {
	case problemFull:
		return tr.T("The calendar on the server is full")
	case problemGone:
		return tr.T("The server no longer has this calendar")
	case problemStore:
		return tr.T("The calendar's changes could not be kept on the phone")
	}
	return accountProblemText(problem, detail)
}

func eventReasonText(n int, reason string) string {
	switch reason {
	case "too_large":
		return tr.T("%d events are too large for the server or the phone", n)
	case "refused":
		return tr.T("The server refused %d events", n)
	}
	return tr.T("%d events could not be written to the server", n)
}

func calendarFindText(err error, problem, detail string) string {
	if errors.Is(err, caldav.ErrNoCalendar) || errors.Is(err, carddav.ErrNotFound) || errors.Is(err, carddav.ErrNoServer) {
		return tr.T("The server has no calendar for this account")
	}
	if errors.Is(err, carddav.ErrInsecure) {
		return tr.T("The server is not on HTTPS: the password would go unencrypted")
	}
	return accountProblemText(problem, detail)
}

// calendarLog is the calendar's part of the account's line in the log.
func calendarLog(r *accountRecord) string {
	if !r.CalOn {
		return ""
	}
	return ", calendar on " + hostOf(r.Calendar.URL) + " (sync-collection " + strconv.FormatBool(r.Calendar.Sync) + ", scheduling " + strconv.FormatBool(r.Calendar.Schedule) + ")"
}
