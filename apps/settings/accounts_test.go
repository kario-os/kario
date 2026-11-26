package main

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/sdk"
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

// The Kario account signs in with a code: the address, then the code
// (digits, focused), the consent for a new address; the app-specific
// password is the page's last row. An account signed in with a code says
// so, and signing out of it says the sign-in is revoked; one the server
// could not be told of stays on the list.
func TestAccountCodePages(t *testing.T) {
	defer i18n.SetLang("en")
	i18n.SetLang("en")
	defer func() {
		acctState.AccountState, acctState.code, acctState.signInErr, acctState.unrevoked = ui.AccountState{}, acctCode{}, "", ""
	}()
	acctState.AccountState = ui.AccountState{KarioServer: "https://account.example.cn"}
	var p app
	s := p.acctCodeScreen()
	if n := node(s, "user"); n.Kind != ui.KindInput || n.Keyboard != ui.KeyboardEmail || n.Submit != "acct_code" || node(s, "code").ID != "" ||
		node(s, "acct_code").ID == "" || s.Body[len(s.Body)-1].ID != "acct-pass" || !strings.Contains(s.Body[0].Text, "https://account.example.cn") {
		t.Fatalf("the address's page: %+v", s)
	}
	acctState.code = acctCode{to: "ann@kario.test", minutes: 10}
	acctState.signInErr = "The code is wrong. Tries left: 4"
	s = p.acctCodeScreen()
	if n := node(s, "code"); n.Keyboard != ui.KeyboardNumber || n.Submit != "acct_signin_code" || s.Focus != "code" ||
		node(s, "user").Value != "ann@kario.test" || !strings.Contains(texts(s), "A code went to ann@kario.test; it holds 10 minutes.") ||
		!strings.Contains(texts(s), "Tries left: 4") || s.Body[len(s.Body)-1].ID != "acct-pass" {
		t.Fatalf("the code's page: %+v", s)
	}
	acctState.code.consent, acctState.signInErr = true, ""
	s = p.acctCodeScreen()
	if node(s, "code").ID != "" || node(s, "acct_agree").ID == "" || !strings.Contains(texts(s), "No Kario account has this address yet.") {
		t.Fatalf("the consent's page: %+v", s)
	}

	acctState.Accounts = []ui.Account{{ID: "a1", Kind: ui.AccountKario, User: "ann@kario.test", Device: "Kario (qemu-t9)", State: ui.AccountAuth,
		Error: "no longer"}}
	a := p.accountScreen("a1")
	if n := node(a, "acct-device"); n.Title != "Signed in with a code" || !strings.Contains(n.Subtitle, "“Kario (qemu-t9)”") ||
		strings.Contains(texts(a), "app-specific password") {
		t.Fatalf("an account signed in with a code: %+v", a.Body)
	}
	for _, act := range a.Actions {
		if (act.ID == "acct_out" || act.ID == "acct_out_remove") && !strings.HasSuffix(act.Confirm, "The Kario account revokes this phone's sign-in.") {
			t.Errorf("%s: %q", act.ID, act.Confirm)
		}
	}
	acctState.unrevoked = "Kario (qemu-t9)"
	if got := texts(p.accountsScreen()); !strings.Contains(got, "on the account's page, sign “Kario (qemu-t9)” out under Phones signed in") {
		t.Errorf("the list: %q", got)
	}
	if n := node(p.accountsScreen(), "acct-lockleft"); n.ID != "" {
		t.Errorf("a check of a binding there is none of: %+v", n)
	}
	acctState.LockLeft = &ui.AccountLockLeft{User: "ann@kario.test", Device: "PH-0123456789ab"}
	s = p.accountsScreen()
	if got := texts(s); !strings.Contains(got, "still on for this phone at the Kario account ann@kario.test: remove the phone (device ID PH-0123456789ab) on the account's page under Anti-theft lock") {
		t.Errorf("the list with a binding left: %q", got)
	}
	if n := node(s, "acct-lockleft"); len(n.Actions) != 1 || n.Actions[0].ID != "acct_lock_check" {
		t.Errorf("the binding's check: %+v", n)
	}
}

// The pages go through account.add as the system answers: a code to the
// address, a wrong code shown on the page, a new address's consent, then
// the sign-in with the code kept for it, back on the list; signing out
// when the server could not be told says so.
func TestAccountCodeSignIn(t *testing.T) {
	others := make(chan ui.Msg, 64)
	p, shell, reqs := fakeShellWith(t, others)
	onApp := func(f func()) {
		done := make(chan bool)
		p.a.Post(func() { f(); done <- true })
		<-done
	}
	onApp(func() { acctState.AccountState = ui.AccountState{KarioServer: "https://account.example.cn"} })
	t.Cleanup(func() {
		onApp(func() {
			acctState.AccountState, acctState.code, acctState.signInErr, acctState.unrevoked = ui.AccountState{}, acctCode{}, "", ""
		})
	})
	var mu sync.Mutex
	var stack, toasts []string
	screens := map[string]ui.Screen{}
	go func() {
		for m := range others {
			mu.Lock()
			switch m.T {
			case ui.MsgPush:
				stack = append(stack, m.Screen.ID)
				screens[m.Screen.ID] = *m.Screen
			case ui.MsgReplace:
				stack[len(stack)-1] = m.Screen.ID
				screens[m.Screen.ID] = *m.Screen
			case ui.MsgUpdate:
				screens[m.Screen.ID] = *m.Screen
			case ui.MsgPop:
				id := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				_ = shell.Send(ui.Msg{T: ui.MsgPopped, ScreenID: id})
			case ui.MsgToast:
				toasts = append(toasts, m.Text)
			}
			mu.Unlock()
		}
	}()
	var asked []ui.AccountArgs
	go func() {
		for m := range reqs {
			var args ui.AccountArgs
			_ = json.Unmarshal(m.Args, &args)
			var data any
			errText := ""
			switch {
			case m.Cap == ui.CapAccountAdd && args.SendCode:
				data = ui.AccountCode{To: args.User, ExpiresIn: 600}
			case m.Cap == ui.CapAccountAdd && args.Code == "000000":
				errText = "The code is wrong. Tries left: 4"
			case m.Cap == ui.CapAccountAdd && !args.Agree:
				errText = "account: " + ui.ErrAccountConsent
			case m.Cap == ui.CapAccountAdd:
				data = ui.AccountState{KarioServer: "https://account.example.cn", Accounts: []ui.Account{{ID: "a1", Kind: ui.AccountKario, User: args.User, Device: "Kario"}}}
			case m.Cap == ui.CapAccountRemove:
				data = ui.AccountState{KarioServer: "https://account.example.cn", Unrevoked: "Kario",
					LockLeft: &ui.AccountLockLeft{User: "ann@kario.test", Device: "PH-0123456789ab"}}
			default:
				data = ui.AccountState{KarioServer: "https://account.example.cn"}
			}
			if m.Cap == ui.CapAccountAdd || m.Cap == ui.CapAccountRemove || args.Check {
				mu.Lock()
				asked = append(asked, args)
				mu.Unlock()
			}
			raw, _ := json.Marshal(data)
			_ = shell.Send(ui.Msg{T: ui.MsgRes, Req: m.Req, OK: errText == "", Err: errText, Data: raw})
		}
	}()
	act := func(e sdk.ActionEvent) { p.a.Post(func() { p.action(e) }) }
	wait := func(what string, cond func() bool) {
		t.Helper()
		for end := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			mu.Lock()
			ok := cond()
			mu.Unlock()
			if ok {
				return
			}
			if time.Now().After(end) {
				t.Fatalf("%s: stack %v, toasts %v, asked %+v", what, stack, toasts, asked)
			}
		}
	}
	p.a.Post(func() { p.push(p.accountsScreen()) })
	act(sdk.ActionEvent{Screen: "accounts", Node: "acct-kario", Action: ui.ActionOpen})
	wait("the code's page", func() bool { return len(stack) == 2 && stack[1] == "acctcode" })
	act(sdk.ActionEvent{Screen: "acctcode", Action: "acct_code", Fields: map[string]string{"user": " ann@kario.test "}})
	wait("the code sent", func() bool { return node(screens["acctcode"], "code").ID != "" })
	if s := screens["acctcode"]; s.Focus != "code" || asked[0] != (ui.AccountArgs{Kind: ui.AccountKario, User: "ann@kario.test", SendCode: true}) {
		t.Fatalf("after the code went: %+v, %+v", s, asked)
	}
	act(sdk.ActionEvent{Screen: "acctcode", Node: "code", Action: "acct_signin_code", Fields: map[string]string{"user": "ann@kario.test", "code": "000000"}})
	wait("the wrong code", func() bool { return strings.Contains(texts(screens["acctcode"]), "Tries left: 4") })
	act(sdk.ActionEvent{Screen: "acctcode", Node: "code", Action: "acct_signin_code", Fields: map[string]string{"user": "ann@kario.test", "code": " 123456 "}})
	wait("the consent", func() bool { return node(screens["acctcode"], "acct_agree").ID != "" })
	act(sdk.ActionEvent{Screen: "acctcode", Node: "acct_agree", Action: "acct_agree", Fields: map[string]string{"user": "ann@kario.test"}})
	wait("signed in", func() bool {
		return len(stack) == 1 && stack[0] == "accounts" && slices.Contains(toasts, "Signed in; the contacts are syncing")
	})
	if a := asked[len(asked)-1]; a != (ui.AccountArgs{Kind: ui.AccountKario, User: "ann@kario.test", Code: "123456", Agree: true}) {
		t.Fatalf("the sign-in after the consent: %+v", a)
	}
	p.a.Post(func() { p.push(p.accountScreen("a1")) })
	wait("the account's page", func() bool { return len(stack) == 2 })
	act(sdk.ActionEvent{Screen: "acct:a1", Node: "acct-out", Action: "acct_out"})
	wait("signed out", func() bool {
		list := texts(screens["accounts"])
		return len(stack) == 1 && strings.Contains(list, "sign “Kario” out under Phones signed in") &&
			strings.Contains(list, "device ID PH-0123456789ab") && node(screens["accounts"], "acct-lockleft").ID != "" &&
			toasts[len(toasts)-1] == "Signed out; the Kario account could not be reached: see the notes in Accounts"
	})
	act(sdk.ActionEvent{Screen: "accounts", Node: "acct-lockleft", Action: "acct_lock_check"})
	wait("checked", func() bool {
		return toasts[len(toasts)-1] == "The anti-theft lock is off at the Kario account" && node(screens["accounts"], "acct-lockleft").ID == "" &&
			!strings.Contains(texts(screens["accounts"]), "PH-0123456789ab")
	})
	if a := asked[len(asked)-1]; a != (ui.AccountArgs{Check: true}) {
		t.Fatalf("the check: %+v", a)
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

// Signing out of the Kario account says beforehand that the anti-theft
// lock stays on at the account when the phone cannot end its binding (no
// network, or the account refusing this phone's sign-in), in place of the
// line on revoking the sign-in; the usual confirmation is as it was.
func TestSignOutSaysTheLockStays(t *testing.T) {
	defer i18n.SetLang("en")
	i18n.SetLang("en")
	defer func() { acctState.AccountState = ui.AccountState{} }()
	const (
		keep    = "Sign out of “ann@kario.test”? The contacts stay on the phone and on the server."
		drop    = "Sign out of “ann@kario.test” and take the 0 contacts that came from it off the phone? They stay on the server; contacts made on this phone stay here."
		revoked = " The Kario account revokes this phone's sign-in."
		offline = " There is no network, so the anti-theft lock stays on for this phone at the Kario account: remove the phone on the account's page under Anti-theft lock afterwards."
		refused = " The Kario account no longer accepts this phone's sign-in, so the anti-theft lock stays on for this phone there: remove the phone on the account's page under Anti-theft lock afterwards."
	)
	var p app
	for _, c := range []struct {
		stays, device, more string
	}{
		{"", "Kario", revoked},
		{"", "", ""},
		{ui.AccountWaitNetwork, "Kario", offline},
		{ui.AccountWaitNetwork, "", offline},
		{ui.AccountAuth, "Kario", refused},
	} {
		acctState.Accounts = []ui.Account{{ID: "a1", Kind: ui.AccountKario, User: "ann@kario.test", Device: c.device, State: ui.AccountIdle, LockStays: c.stays}}
		s := p.accountScreen("a1")
		got := map[string]string{"acct-out": node(s, "acct-out").Actions[0].Confirm, "acct-out-remove": node(s, "acct-out-remove").Actions[0].Confirm}
		for _, a := range s.Actions {
			got[a.ID] = a.Confirm
		}
		for id, want := range map[string]string{"acct_out": keep, "acct-out": keep, "acct_out_remove": drop, "acct-out-remove": drop} {
			if got[id] != want+c.more {
				t.Errorf("%q %q: %s: %q", c.stays, c.device, id, got[id])
			}
		}
	}
}

// An account the server would not bind the anti-theft lock to (409: the
// phone is bound to another account) has a note on the list with this
// phone's device ID and a row to try again, which asks the system
// (account.set with RetryLock): its refusal is a toast and the note
// stays; once bound the note goes and the toast says the lock is on.
func TestAccountLockTaken(t *testing.T) {
	others := make(chan ui.Msg, 64)
	p, shell, reqs := fakeShellWith(t, others)
	onApp := func(f func()) {
		done := make(chan bool)
		p.a.Post(func() { f(); done <- true })
		<-done
	}
	taken := ui.AccountState{KarioServer: "https://account.example.cn", Accounts: []ui.Account{{ID: "a1", Kind: ui.AccountKario, User: "ann@kario.test",
		State: ui.AccountIdle, LockTaken: "PH-0123456789ab"}}}
	bound := ui.AccountState{KarioServer: "https://account.example.cn", Accounts: []ui.Account{{ID: "a1", Kind: ui.AccountKario, User: "ann@kario.test",
		State: ui.AccountIdle}}}
	onApp(func() { acctState.AccountState = taken })
	t.Cleanup(func() { onApp(func() { acctState.AccountState = ui.AccountState{} }) })
	var mu sync.Mutex
	var toasts []string
	var asked []ui.AccountArgs
	screens := map[string]ui.Screen{}
	refuse := true
	go func() {
		for m := range others {
			mu.Lock()
			switch m.T {
			case ui.MsgPush, ui.MsgReplace, ui.MsgUpdate:
				screens[m.Screen.ID] = *m.Screen
			case ui.MsgToast:
				toasts = append(toasts, m.Text)
			}
			mu.Unlock()
		}
	}()
	go func() {
		for m := range reqs {
			var args ui.AccountArgs
			_ = json.Unmarshal(m.Args, &args)
			mu.Lock()
			data, errText := taken, ""
			if !refuse {
				data = bound
			}
			if m.Cap == ui.CapAccountSet {
				asked = append(asked, args)
				if refuse {
					errText = "The previous owner has not removed the phone yet"
				}
			}
			mu.Unlock()
			raw, _ := json.Marshal(data)
			_ = shell.Send(ui.Msg{T: ui.MsgRes, Req: m.Req, OK: errText == "", Err: errText, Data: raw})
		}
	}()
	wait := func(what string, cond func() bool) {
		t.Helper()
		for end := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			mu.Lock()
			ok := cond()
			mu.Unlock()
			if ok {
				return
			}
			if time.Now().After(end) {
				t.Fatalf("%s: toasts %v, asked %+v, list %q", what, toasts, asked, texts(screens["accounts"]))
			}
		}
	}
	last := func() string {
		if len(toasts) == 0 {
			return ""
		}
		return toasts[len(toasts)-1]
	}
	act := func() {
		p.a.Post(func() {
			p.action(sdk.ActionEvent{Screen: "accounts", Node: "acct-lockretry:a1", Action: "acct_lock_retry"})
		})
	}
	p.a.Post(func() { p.push(p.accountsScreen()) })
	wait("the note", func() bool {
		return strings.Contains(texts(screens["accounts"]), "The anti-theft lock is not on for ann@kario.test: this phone is still locked to another Kario account. Its previous owner must remove it (device ID PH-0123456789ab) on that account's page under Anti-theft lock.") &&
			node(screens["accounts"], "acct-lockretry:a1").Actions[0].ID == "acct_lock_retry"
	})
	act()
	wait("refused", func() bool { return strings.Contains(last(), "The previous owner has not removed the phone yet") })
	mu.Lock()
	if len(asked) != 1 || asked[0] != (ui.AccountArgs{ID: "a1", RetryLock: true}) || node(screens["accounts"], "acct-lockretry:a1").ID == "" {
		t.Fatalf("tried again: %+v", asked)
	}
	refuse = false
	mu.Unlock()
	act()
	wait("bound", func() bool {
		return last() == "The anti-theft lock is on" && node(screens["accounts"], "acct-lockretry:a1").ID == "" &&
			!strings.Contains(texts(screens["accounts"]), "PH-0123456789ab")
	})
}
