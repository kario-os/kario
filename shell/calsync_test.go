package shell

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/kario-os/kario/caldav"
	caltest "github.com/kario-os/kario/caldav/davtest"
	"github.com/kario-os/kario/carddav/davtest"
	"github.com/kario-os/kario/ui"
)

type calendarTest struct {
	*accountTest
	cal *caltest.Server
	app *App
}

// newCalendarTest is the account test with the calendar app and a CalDAV
// server on the account server's address, holding one event.
func newCalendarTest(t *testing.T) *calendarTest {
	at := newAccountTest(t)
	ct := &calendarTest{accountTest: at, cal: caltest.New(acctUser, acctPass)}
	ct.app = &App{M: &Manifest{ID: appCalendar, Name: "Calendar"}, running: true, ready: true, out: make(chan outbound, 64)}
	at.c.apps = append(at.c.apps, ct.app)
	ct.cal.Put("standup.ics", []byte(calObject("standup@web", "standup")))
	ts := httptest.NewServer(accountSim(at.srv, ct.cal))
	t.Cleanup(ts.Close)
	at.c.cfg.AccountURL = ts.URL
	return ct
}

func calObject(uid, summary string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTART;TZID=Asia/Shanghai:20301015T090000\r\n" +
		"DTEND;TZID=Asia/Shanghai:20301015T100000\r\nSUMMARY:" + summary + "\r\nX-WEB:kept\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

// calendarCall is a capability request as the calendar app.
func (ct *calendarTest) calendarCall(cap string, args, out any) error {
	ct.t.Helper()
	ct.req(ct.app, cap, args)
	r := ct.reply(ct.app)
	if !r.OK {
		return errors.New(r.Err)
	}
	return json.Unmarshal(r.Data, out)
}

// pull is what the app takes: every change, by UID.
func (ct *calendarTest) pull() (ui.CalPull, map[string]ui.CalChange) {
	ct.t.Helper()
	got := map[string]ui.CalChange{}
	var p ui.CalPull
	var taken []int
	for {
		if err := ct.calendarCall(ui.CapCalendarPull, ui.CalPullArgs{Taken: taken}, &p); err != nil {
			ct.t.Fatal(err)
		}
		taken = nil
		for _, it := range p.Items {
			got[it.UID] = it
			taken = append(taken, it.Seq)
		}
		if len(p.Items) == 0 {
			return p, got
		}
	}
}

func (ct *calendarTest) push(args ui.CalPushArgs) ui.CalPushRes {
	ct.t.Helper()
	var res ui.CalPushRes
	if err := ct.calendarCall(ui.CapCalendarPush, args, &res); err != nil {
		ct.t.Fatal(err)
	}
	return res
}

// links are the deep links the calendar app was sent since the last look.
func (ct *calendarTest) links() []string {
	var out []string
	for {
		m, ok := next(ct.app)
		if !ok {
			return out
		}
		if m.T == ui.MsgEvent && m.Event == ui.EventOpen {
			out = append(out, m.Value)
		}
	}
}

func (ct *calendarTest) calendarOn(id string) ui.Account {
	ct.t.Helper()
	on := true
	if st, err := ct.call(ui.CapAccountSet, ui.AccountArgs{ID: id, Calendar: &on}); err != nil || st.Accounts[0].Calendar == nil || !st.Accounts[0].Calendar.On {
		ct.t.Fatalf("turning the calendar on: %+v %v", st, err)
	}
	ct.synced()
	return ct.view()
}

// The calendar syncs with one account at most: turning it on for another
// says which account has it, and signing out of that other account leaves
// the calendar's sync as it was.
func TestCalendarOneAccount(t *testing.T) {
	ct := newCalendarTest(t)
	kario := ct.signIn()
	ct.calendarOn(kario.ID)
	other := davtest.New("bob@example.test", "pw")
	ts := httptest.NewServer(other)
	t.Cleanup(ts.Close)
	st, err := ct.call(ui.CapAccountAdd, ui.AccountArgs{Kind: ui.AccountCardDAV, Server: ts.URL, User: "bob@example.test", Password: "pw"})
	if err != nil || len(st.Accounts) != 2 {
		t.Fatalf("%+v %v", st, err)
	}
	ct.synced()
	second := st.Accounts[1].ID
	on := true
	if _, err := ct.call(ui.CapAccountSet, ui.AccountArgs{ID: second, Calendar: &on}); err == nil {
		t.Fatal("a second account's calendar went on")
	}
	if st, _ := ct.call(ui.CapAccountState, nil); st.Accounts[1].Calendar != nil && st.Accounts[1].Calendar.On {
		t.Fatalf("a second account's calendar: %+v", st.Accounts[1].Calendar)
	}
	if _, err := ct.call(ui.CapAccountRemove, ui.AccountArgs{ID: second}); err != nil {
		t.Fatal(err)
	}
	ct.synced()
	if v := ct.view(); v.Calendar == nil || !v.Calendar.On || ct.c.accounts.cal.state == nil {
		t.Fatalf("signing out of the other account touched the calendar: %+v", v.Calendar)
	}
}

// The calendar on the account: turned on, the calendar app is asked for
// all its events and gets the server's; its changes go up, the server's
// come down, a conflict keeps both; offline the changes wait; signing out
// may take the account's events off the phone.
func TestCalendarSync(t *testing.T) {
	ct := newCalendarTest(t)
	c := ct.c
	acct := ct.signIn()
	if acct.Calendar == nil || acct.Calendar.On {
		t.Fatalf("the calendar starts off: %+v", acct.Calendar)
	}
	v := ct.calendarOn(acct.ID)
	if cal := v.Calendar; cal.Name != "Calendar" || cal.Events != 1 || cal.LastSync == 0 || cal.Error != "" || cal.State != ui.AccountIdle {
		t.Fatalf("on: %+v", cal)
	}
	if links := ct.links(); len(links) == 0 || links[0] != ui.LinkCalendarSync {
		t.Fatalf("the app is woken for the sync: %v", links)
	}
	p, got := ct.pull()
	if p.Account != acct.ID || !p.WantAll || !strings.Contains(got["standup@web"].ICS, "SUMMARY:standup") {
		t.Fatalf("the first pull: %+v %+v", p, got)
	}
	// The app hands over its events; one has the server's UID (the server's wins).
	res := ct.push(ui.CalPushArgs{Items: []ui.CalChange{
		{UID: "mine@kario", ICS: calObject("mine@kario", "made on the phone"), All: true},
		{UID: "standup@web", ICS: calObject("standup@web", "phone's standup"), All: true},
	}, Done: true})
	if res.Account != acct.ID || res.Pending != 2 {
		t.Fatalf("pushed: %+v", res)
	}
	ct.later(accountAfterEdit)
	ct.synced()
	if _, ok := ct.cal.Find("made on the phone"); !ok {
		t.Fatal("the phone's event is not on the server")
	}
	if _, ok := ct.cal.Find("phone's standup"); ok {
		t.Fatal("the first hand-over wrote over the server's event")
	}
	if p, _ := ct.pull(); p.WantAll {
		t.Fatal("the hand-over is over")
	}
	// The phone changes its event: patched into the server's object.
	name, _ := ct.cal.Find("made on the phone")
	ct.cal.Put(name, []byte(strings.Replace(string(ct.cal.Object(name)), "END:VEVENT", "CATEGORIES:web\r\nEND:VEVENT", 1)))
	if _, err := ct.call(ui.CapAccountSync, ui.AccountArgs{ID: acct.ID}); err != nil {
		t.Fatal(err)
	}
	ct.synced()
	ct.pull()
	ct.push(ui.CalPushArgs{Items: []ui.CalChange{{UID: "mine@kario", ICS: calObject("mine@kario", "renamed on the phone")}}})
	ct.later(accountAfterEdit)
	ct.synced()
	if o := string(ct.cal.Object(name)); !strings.Contains(o, "renamed on the phone") || !strings.Contains(o, "CATEGORIES:web") {
		t.Fatalf("patched:\n%s", o)
	}
	// Both change the server's event: the server's wins, the phone's is a copy.
	ct.cal.Put("standup.ics", []byte(calObject("standup@web", "standup moved on the web")))
	ct.push(ui.CalPushArgs{Items: []ui.CalChange{{UID: "standup@web", ICS: calObject("standup@web", "standup moved on the phone")}}})
	ct.later(accountAfterEdit)
	ct.synced()
	_, got = ct.pull()
	var cp ui.CalChange
	for _, it := range got {
		if it.Copy {
			cp = it
		}
	}
	if !strings.Contains(got["standup@web"].ICS, "moved on the web") || !strings.Contains(cp.ICS, "moved on the phone") || cp.UID == "standup@web" {
		t.Fatalf("a conflict: %+v", got)
	}
	if _, ok := ct.cal.Find("moved on the phone"); !ok {
		t.Fatal("the copy is not on the server")
	}
	// Offline: the phone's deletion waits, and goes once the network is back.
	offline := true
	c.accounts.net = func(then func(bool, bool)) { then(!offline, false) }
	ct.push(ui.CalPushArgs{Items: []ui.CalChange{{UID: "mine@kario", Gone: true}}})
	ct.later(accountAfterEdit)
	ct.synced()
	if v := ct.view(); v.State != ui.AccountWaiting || v.Calendar.Pending != 1 {
		t.Fatalf("offline: %+v %+v", v, v.Calendar)
	}
	offline = false
	c.accounts.rtOf(c.accounts.calAccount().ID).netAt = c.now().Add(-accountNetCheck)
	ct.synced()
	if _, ok := ct.cal.Find("renamed on the phone"); ok || ct.view().Calendar.Pending != 0 {
		t.Fatal("the deletion did not go")
	}
	// Deleted on the server: the app is told.
	ct.cal.Delete("standup.ics")
	if _, err := ct.call(ui.CapAccountSync, ui.AccountArgs{ID: acct.ID}); err != nil {
		t.Fatal(err)
	}
	ct.synced()
	if _, got := ct.pull(); !got["standup@web"].Gone {
		t.Fatalf("deleted on the server: %+v", got)
	}
	// Only the system's calendar app.
	ct.req(ct.accountTest.app, ui.CapCalendarPull, nil)
	if r := ct.reply(ct.accountTest.app); r.OK {
		t.Fatal("another app pulled the calendar")
	}
	ct.links()
	if _, err := ct.call(ui.CapAccountRemove, ui.AccountArgs{ID: acct.ID, RemoveContacts: true}); err != nil {
		t.Fatal(err)
	}
	if links := ct.links(); len(links) == 0 || links[0] != ui.LinkCalendarUndo+ui.LinkCalendarAccount+acct.ID {
		t.Fatalf("the app takes the account's events off: %v", links)
	}
	for _, p := range []string{c.calStatePath(acct.ID), c.calQueuePath(acct.ID)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind", p)
		}
	}
	var pr ui.CalPull
	if err := ct.calendarCall(ui.CapCalendarPull, nil, &pr); err != nil || pr.Account != "" {
		t.Fatalf("signed out: %+v %v", pr, err)
	}
	for _, secret := range []string{acctPass, acctUser} {
		if strings.Contains(ct.log.String(), secret) {
			t.Errorf("the log tells %q", secret)
		}
	}
}

// A server that schedules (RFC 6638): the run finds out and calendar.pull
// tells the app, with the user's address; the app's invitation goes up
// for the server to send and comes back with its delivery status, the
// attendee's answer comes back, the inbox's messages are read and
// deleted; someone's invitation comes in and the app's answer goes up as
// a reply. A server that stops scheduling is noticed at the next day's
// look, and the app is told.
func TestCalendarScheduling(t *testing.T) {
	ct := newCalendarTest(t)
	ct.cal.Schedule = true
	acct := ct.signIn()
	ct.calendarOn(acct.ID)
	p, first := ct.pull()
	if !p.Schedule || len(p.Addresses) != 1 || p.Addresses[0] != "mailto:"+acctUser || !first["standup@web"].First {
		t.Fatalf("the pull: %+v %+v", p, first)
	}
	if v := ct.c.accounts.calAccount(); v.Calendar.Checked == 0 || !strings.Contains(calendarLog(v), "scheduling true") {
		t.Fatalf("the calendar: %+v", v.Calendar)
	}
	invite := strings.Replace(calObject("party@kario", "party"), "X-WEB:kept", "ORGANIZER:mailto:"+acctUser+"\r\nATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:bob@kario.test", 1)
	ct.push(ui.CalPushArgs{Items: []ui.CalChange{{UID: "party@kario", ICS: invite}}})
	ct.later(accountAfterEdit)
	ct.synced()
	if sent := ct.cal.SentLog(); len(sent) != 1 || sent[0] != "REQUEST party@kario bob@kario.test" {
		t.Fatalf("sent: %v", sent)
	}
	if _, got := ct.pull(); !strings.Contains(got["party@kario"].ICS, "SCHEDULE-STATUS=1.2") {
		t.Fatalf("the delivery status comes back: %+v", got)
	}
	if !ct.cal.Reply("party@kario", "bob@kario.test", "ACCEPTED") {
		t.Fatal("no invitation to answer")
	}
	ct.cal.Invite("lunch.ics", []byte(strings.Replace(calObject("lunch@web", "lunch"), "X-WEB:kept",
		"ORGANIZER:mailto:bob@kario.test\r\nATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:"+acctUser, 1)))
	if _, err := ct.call(ui.CapAccountSync, ui.AccountArgs{ID: acct.ID}); err != nil {
		t.Fatal(err)
	}
	ct.synced()
	_, got := ct.pull()
	if !strings.Contains(got["party@kario"].ICS, "PARTSTAT=ACCEPTED") || !strings.Contains(got["lunch@web"].ICS, "SUMMARY:lunch") || got["lunch@web"].First || ct.cal.InboxLen() != 0 {
		t.Fatalf("the answer and the invitation come in, the inbox read (%d left): %+v", ct.cal.InboxLen(), got)
	}
	accepted := strings.Replace(got["lunch@web"].ICS, "PARTSTAT=NEEDS-ACTION;RSVP=TRUE", "PARTSTAT=ACCEPTED", 1)
	ct.push(ui.CalPushArgs{Items: []ui.CalChange{{UID: "lunch@web", ICS: accepted}}})
	ct.later(accountAfterEdit)
	ct.synced()
	if sent := ct.cal.SentLog(); sent[len(sent)-1] != "REPLY lunch@web ACCEPTED" {
		t.Fatalf("the answer as a reply: %v", sent)
	}
	ct.links()
	ct.cal.Lock()
	ct.cal.Schedule = false
	ct.cal.Unlock()
	ct.later(calendarScheduleCheck)
	if _, err := ct.call(ui.CapAccountSync, ui.AccountArgs{ID: acct.ID}); err != nil {
		t.Fatal(err)
	}
	ct.synced()
	if links := ct.links(); len(links) == 0 || links[0] != ui.LinkCalendarSync {
		t.Fatalf("the app is told: %v", links)
	}
	if p, _ := ct.pull(); p.Schedule || len(p.Addresses) != 0 {
		t.Fatalf("no longer scheduling: %+v", p)
	}
}

// The calendar's sync with a scheduling server ends: signing out (the
// account's events kept or taken off) and turning the calendar off wake
// the running calendar app for a pull, which says there is no account to
// schedule for any more; a calendar app that is not running is not
// started for that.
func TestCalendarSyncEndTellsTheApp(t *testing.T) {
	signOut := func(events bool) func(*calendarTest, string) error {
		return func(ct *calendarTest, id string) error {
			_, err := ct.call(ui.CapAccountRemove, ui.AccountArgs{ID: id, RemoveContacts: events})
			return err
		}
	}
	for _, tc := range []struct {
		name          string
		running, undo bool
		end           func(ct *calendarTest, id string) error
	}{
		{"signed out, events kept", true, false, signOut(false)},
		{"signed out, events taken off", true, true, signOut(true)},
		{"calendar off", true, false, func(ct *calendarTest, id string) error {
			off := false
			_, err := ct.call(ui.CapAccountSet, ui.AccountArgs{ID: id, Calendar: &off})
			return err
		}},
		{"signed out, app not running", false, false, signOut(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ct := newCalendarTest(t)
			ct.cal.Schedule = true
			acct := ct.signIn()
			ct.calendarOn(acct.ID)
			if p, _ := ct.pull(); !p.Schedule || len(p.Addresses) == 0 {
				t.Fatalf("the server schedules: %+v", p)
			}
			ct.links()
			ct.app.running = tc.running
			if err := tc.end(ct, acct.ID); err != nil {
				t.Fatal(err)
			}
			if !tc.running {
				if len(ct.app.pushQ) != 0 {
					t.Fatalf("the calendar app was started for it: %v", ct.app.pushQ)
				}
				return
			}
			want := []string{ui.LinkCalendarSync}
			if tc.undo {
				want = append([]string{ui.LinkCalendarUndo + ui.LinkCalendarAccount + acct.ID}, want...)
			}
			if links := ct.links(); !slices.Equal(links, want) {
				t.Fatalf("the app was sent %q, want %q", links, want)
			}
			var p ui.CalPull
			if err := ct.calendarCall(ui.CapCalendarPull, nil, &p); err != nil || p.Account != "" || p.Schedule || len(p.Addresses) != 0 {
				t.Fatalf("the pull after: %+v %v", p, err)
			}
		})
	}
}

// A calendar the app has no room for waits in the Shell; turning the
// calendar off drops the queue and the state; a server without a calendar
// says so and stays off.
// An account that syncs only the calendar (its address book off) whose
// password the server stops taking: the calendar's 401 is the account's
// sign-in refused, as the contacts' would be, so Settings says so and the
// half-hourly job does not try the password again; Sync now does.
func TestCalendarSignInRefused(t *testing.T) {
	ct := newCalendarTest(t)
	acct := ct.signIn()
	ct.calendarOn(acct.ID)
	off := false
	if _, err := ct.call(ui.CapAccountSet, ui.AccountArgs{ID: acct.ID, Book: acct.Books[0].ID, On: &off}); err != nil {
		t.Fatal(err)
	}
	ct.synced()
	ct.cal.Lock()
	ct.cal.Pass = "signed-out"
	ct.cal.Unlock()
	if _, err := ct.call(ui.CapAccountSync, ui.AccountArgs{ID: acct.ID}); err != nil {
		t.Fatal(err)
	}
	ct.synced()
	if v := ct.view(); v.State != ui.AccountAuth || v.Error != accountProblemText(problemAuth, "") || v.Calendar.State != ui.AccountAuth {
		t.Fatalf("the calendar refused: %+v %+v", v, v.Calendar)
	}
	refused := ct.cal.Count("unauthorized")
	ct.later(accountEvery)
	ct.c.accountsDue()
	ct.synced()
	if n := ct.cal.Count("unauthorized"); n != refused {
		t.Fatalf("the job tried the refused password %d more times", n-refused)
	}
	if _, err := ct.call(ui.CapAccountSync, ui.AccountArgs{ID: acct.ID}); err != nil {
		t.Fatal(err)
	}
	ct.synced()
	if ct.cal.Count("unauthorized") == refused {
		t.Fatal("Sync now did not try the password")
	}
}

func TestCalendarFullOffAndNone(t *testing.T) {
	ct := newCalendarTest(t)
	acct := ct.signIn()
	ct.calendarOn(acct.ID)
	var p ui.CalPull
	if err := ct.calendarCall(ui.CapCalendarPull, ui.CalPullArgs{Full: true}, &p); err != nil || len(p.Items) != 0 || !p.More {
		t.Fatalf("full: %+v %v", p, err)
	}
	if v := ct.view(); !v.Calendar.Full || v.Calendar.Waiting != 1 {
		t.Fatalf("waiting for room: %+v", v.Calendar)
	}
	off := false
	st, err := ct.call(ui.CapAccountSet, ui.AccountArgs{ID: acct.ID, Calendar: &off})
	if err != nil || st.Accounts[0].Calendar.On {
		t.Fatalf("off: %+v %v", st, err)
	}
	if _, err := os.Stat(ct.c.calQueuePath(acct.ID)); !os.IsNotExist(err) {
		t.Error("the queue stays")
	}
	if res := ct.push(ui.CalPushArgs{Items: []ui.CalChange{{UID: "x", ICS: calObject("x", "x")}}}); res.Account != "" {
		t.Fatalf("off takes no changes: %+v", res)
	}
	ct.c.cfg.AccountURL = ct.ts.URL
	on := true
	if _, err := ct.call(ui.CapAccountSet, ui.AccountArgs{ID: acct.ID, Calendar: &on}); err == nil || err.Error() != calendarFindText(caldav.ErrNoCalendar, "", "") {
		t.Fatalf("a server without CalDAV: %v", err)
	}
	if v := ct.view(); v.Calendar.On {
		t.Fatal("on without a calendar")
	}
}
