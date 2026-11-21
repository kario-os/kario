package main

import (
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ui"
)

// Several accounts each have a page with their books, each to switch on or
// off; the list says where new contacts go, the default's page offers
// every book on and the phone.
func TestAccountsPages(t *testing.T) {
	defer i18n.SetLang("en")
	i18n.SetLang("en")
	defer func() { acctState.AccountState = ui.AccountState{} }()
	acctState.AccountState = ui.AccountState{Default: "a2/b1", KarioServer: "https://account.example.cn", Accounts: []ui.Account{
		{ID: "a1", Kind: ui.AccountKario, User: "ann@kario.test", State: ui.AccountIdle, LastSync: 1,
			Books: []ui.AccountBook{{ID: "a1/b1", Name: "Contacts", On: true, Contacts: 3, Remote: 2}}},
		{ID: "a2", Kind: ui.AccountCardDAV, Server: "https://dav.example.org/", User: "bob", State: ui.AccountIdle, LastSync: 1,
			Books: []ui.AccountBook{{ID: "a2/b1", Name: "Personal", On: true}, {ID: "a2/b2", Name: "Shared", On: false, Contacts: 4},
				{ID: "a2/b3", Name: "System", On: true, ReadOnly: true, Contacts: 7, Remote: 7}}},
	}}
	var p app
	s := p.accountsScreen()
	if n, ok := nodeOf(s, "acct:a2"); !ok || n.Title != "dav.example.org" || !strings.HasPrefix(n.Subtitle, "bob · ") {
		t.Errorf("%+v", n)
	}
	if n, ok := nodeOf(s, "acct-default"); !ok || n.Subtitle != "Personal · dav.example.org (bob)" {
		t.Errorf("%+v", n)
	}
	if _, ok := nodeOf(s, "acct-kario"); !ok {
		t.Error("no way to add another account")
	}
	a := p.accountScreen("a2")
	on, _ := nodeOf(a, "book:a2/b1")
	off, _ := nodeOf(a, "book:a2/b2")
	if on.Trailing != "On" || on.Actions[0].ID != "book_off" || on.Actions[1].ID != "book_off_remove" || off.Actions[0].ID != "book_on" ||
		!strings.Contains(off.Subtitle, "4 contacts stay") {
		t.Errorf("%+v\n%+v", on, off)
	}
	if ro, _ := nodeOf(a, "book:a2/b3"); !strings.HasPrefix(ro.Subtitle, "Read-only: 7 contacts") {
		t.Errorf("a read-only book: %+v", ro)
	}
	d := p.defaultScreen()
	var ids []string
	for _, n := range d.Body {
		if strings.HasPrefix(n.ID, "def:") {
			ids = append(ids, n.ID+n.Trailing)
		}
	}
	if strings.Join(ids, " ") != "def:a1/b1 def:a2/b1✓ def:" {
		t.Errorf("%v", ids)
	}
	if got := p.accountsItem().Subtitle; got != "2 accounts: ann@kario.test, bob" {
		t.Errorf("%q", got)
	}
}

// The account's page has the calendar's row under the contacts': a switch
// with how the calendar's sync goes, its trouble, its name and what waits;
// signing out says the events go too while the calendar syncs.
func TestAccountCalendarRows(t *testing.T) {
	defer i18n.SetLang("en")
	i18n.SetLang("en")
	acct := func(cal *ui.AccountCalendar) ui.Account {
		return ui.Account{ID: "a1", Kind: ui.AccountKario, User: "alice@kario.test", State: ui.AccountIdle, LastSync: time.Now().Unix(), Calendar: cal}
	}
	sub := func(cal *ui.AccountCalendar) string {
		rows := calendarRows(acct(cal))
		if len(rows) == 0 || rows[0].ID != "acct-calendar" {
			t.Fatalf("no calendar row: %+v", rows)
		}
		return rows[0].Subtitle
	}
	if rows := calendarRows(acct(nil)); rows != nil {
		t.Errorf("a system without calendar sync: %+v", rows)
	}
	for _, c := range []struct {
		cal  ui.AccountCalendar
		want string
	}{
		{ui.AccountCalendar{}, "Off: the events stay on this phone only"},
		{ui.AccountCalendar{Finding: true}, "Looking for the calendar…"},
		{ui.AccountCalendar{On: true, State: ui.AccountSyncing}, "Syncing…"},
		{ui.AccountCalendar{On: true, State: ui.AccountIdle, Full: true, Waiting: 3}, "The phone's calendar is full: 3 events from the server wait"},
		{ui.AccountCalendar{On: true, State: ui.AccountFailed, Error: "x"}, "Not synced"},
		{ui.AccountCalendar{On: true, State: ui.AccountIdle}, "Not synced yet"},
	} {
		if got := sub(&c.cal); got != c.want {
			t.Errorf("%+v: %q, want %q", c.cal, got, c.want)
		}
	}
	rows := calendarRows(acct(&ui.AccountCalendar{On: true, State: ui.AccountFailed, Error: "The calendar on the server is full", Name: "Calendar", Events: 12, Pending: 2}))
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID+":"+r.Subtitle+r.Text)
	}
	want := []string{"acct-calendar:Not synced", ":The calendar on the server is full", "acct-calendar-name:Calendar, 12 events", "acct-calendar-pending:2 changes in the calendar"}
	if len(ids) != len(want) {
		t.Fatalf("%q", ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("row %d: %q, want %q", i, ids[i], want[i])
		}
	}
	acctState.Accounts = []ui.Account{acct(&ui.AccountCalendar{On: true})}
	defer func() { acctState.Accounts = nil }()
	var p app
	found := false
	for _, a := range p.accountScreen("a1").Actions {
		if a.ID != "acct_out_remove" {
			continue
		}
		found = true
		if a.Confirm != "Sign out of “alice@kario.test” and take the 0 contacts and the calendar's events that came from it off the phone? They stay on the server; what was made on this phone stays here." {
			t.Errorf("signing out: %q", a.Confirm)
		}
	}
	if !found {
		t.Error("no sign-out action on the account's page")
	}
}
