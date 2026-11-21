package main

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Accounts: the contacts accounts the system syncs with
// (account.*), the Kario account and other CardDAV servers, each with its
// address books, each switched on or off, and the book new contacts go
// to. Signing in sends the password to the system once; the pages show
// what the system says of the syncs, refreshed while they are open, and
// never the password.

var acctState struct {
	ui.AccountState
	loaded bool
	// signInErr: why the last sign-in failed, shown on its page.
	signInErr string
}

var acctTick sync.Once

func (p *app) loadAccounts(then func()) {
	var st ui.AccountState
	p.a.Go(ui.CapAccountState, nil, &st, func(err error) {
		if err == nil {
			acctState.AccountState, acctState.loaded = st, true
		}
		if then != nil {
			then()
		}
	})
}

func findAccount(id string) (ui.Account, bool) {
	for _, ac := range acctState.Accounts {
		if ac.ID == id {
			return ac, true
		}
	}
	return ui.Account{}, false
}

func acctName(ac ui.Account) string {
	if ac.Kind == ui.AccountKario {
		return tr.T("Kario account")
	}
	if h := hostOfURL(ac.Server); h != "" {
		return h
	}
	return "CardDAV"
}

func hostOfURL(s string) string {
	s = strings.TrimSpace(s)
	if _, rest, ok := strings.Cut(s, "://"); ok {
		s = rest
	}
	host, _, _ := strings.Cut(s, "/")
	return host
}

// bookLabel names a book for the default's choice: its name and account.
func bookLabel(key string) string {
	for _, ac := range acctState.Accounts {
		for _, b := range ac.Books {
			if b.ID == key {
				return b.Name + " · " + acctName(ac) + " (" + ac.User + ")"
			}
		}
	}
	return tr.T("On the phone only")
}

// acctStatus is a line on how the sync goes.
func acctStatus(ac ui.Account) string {
	switch ac.State {
	case ui.AccountSyncing:
		switch {
		case ac.Phase == "push" && ac.Total > 0:
			return tr.T("Sending changes to the server: %d of %d", ac.Done, ac.Total)
		case ac.Total > 0:
			return tr.T("Receiving contacts: %d of %d", ac.Done, ac.Total)
		}
		return tr.T("Syncing…")
	case ui.AccountWaiting:
		if ac.Waiting == ui.AccountWaitWifi {
			return tr.T("Waiting for Wi-Fi")
		}
		return tr.T("Waiting for a network")
	case ui.AccountAuth:
		return tr.T("Sign-in refused")
	case ui.AccountFailed:
		if ac.NextSync > 0 {
			return tr.T("Not synced; trying again at %s", time.Unix(ac.NextSync, 0).Format("15:04"))
		}
		return tr.T("Not synced")
	}
	if !slices.ContainsFunc(ac.Books, func(b ui.AccountBook) bool { return b.On }) {
		return tr.T("No address book syncs")
	}
	if ac.LastSync == 0 {
		return tr.T("Not synced yet")
	}
	return tr.T("Synced %s ago", ago(ac.LastSync))
}

func (p *app) accountsItem() ui.Node {
	sub := tr.T("Sync contacts with the Kario account or a CardDAV server")
	switch n := len(acctState.Accounts); {
	case n == 1:
		ac := acctState.Accounts[0]
		sub = ac.User + " · " + acctStatus(ac)
	case n > 1:
		var users []string
		for _, ac := range acctState.Accounts {
			users = append(users, ac.User)
		}
		sub = tr.T("%d accounts: %s", n, strings.Join(users, tr.T(", ")))
	}
	return ui.Node{Kind: ui.KindItem, ID: "accounts", Title: tr.T("Accounts"), Subtitle: sub, Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
}

func (p *app) accountsScreen() ui.Screen {
	s := ui.Screen{ID: "accounts", Title: tr.T("Accounts")}
	if len(acctState.Accounts) > 0 {
		s.Body = append(s.Body, sdk.Section(tr.T("Accounts")))
		for _, ac := range acctState.Accounts {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "acct:" + ac.ID, Title: acctName(ac), Subtitle: ac.User + " · " + acctStatus(ac),
				Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "acct-default", Title: tr.T("New contacts go to"), Subtitle: bookLabel(acctState.Default),
			Actions: []ui.Action{sdk.Open(tr.T("Choose"))}})
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Add an account")))
	if acctState.KarioServer != "" {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "acct-kario", Title: tr.T("Kario account"),
			Subtitle: tr.T("Your contacts on the Kario account, also on your other phones and computers"), Actions: []ui.Action{sdk.Open(tr.T("Sign in"))}})
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "acct-carddav", Title: tr.T("Other CardDAV account"),
		Subtitle: tr.T("iCloud, Nextcloud, Fastmail and other CardDAV servers"), Actions: []ui.Action{sdk.Open(tr.T("Sign in"))}},
		sdk.Styled(ui.StyleDim, tr.T("The phone's contacts sync both ways with the address books switched on: what changes on either side reaches the other, groups and photos too. Each contact is in one book, or on the phone only.")))
	return s
}

// accountScreen is one account: its sync, its books, signing out.
func (p *app) accountScreen(id string) ui.Screen {
	ac, ok := findAccount(id)
	s := ui.Screen{ID: "acct:" + id, Title: acctName(ac)}
	if !ok {
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("No such account"))}
		return s
	}
	sync := ui.Action{ID: "acct_sync", Label: tr.T("Sync now"), Key: "s"}
	keep := ui.Action{ID: "acct_out", Label: tr.T("Sign out, keep the contacts"), Key: "o",
		Confirm: tr.T("Sign out of “%s”? The contacts stay on the phone and on the server.", ac.User)}
	drop := ui.Action{ID: "acct_out_remove", Label: tr.T("Sign out and remove its contacts"), Key: "r", Group: ui.GroupDanger,
		Confirm: tr.T("Sign out of “%s” and take the %d contacts that came from it off the phone? They stay on the server; contacts made on this phone stay here.", ac.User, ac.Remote)}
	if ac.Calendar != nil && ac.Calendar.On {
		drop.Confirm = tr.T("Sign out of “%s” and take the %d contacts and the calendar's events that came from it off the phone? They stay on the server; what was made on this phone stays here.", ac.User, ac.Remote)
	}
	s.Actions = []ui.Action{sync, keep, drop}
	row := func(id, title, value string) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: id, Title: title, Subtitle: value}
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "acct-status", Title: ac.User, Subtitle: acctStatus(ac),
		Actions: []ui.Action{{ID: "acct_sync", Label: tr.T("Sync now"), Primary: true}}})
	if ac.Error != "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, ac.Error))
	}
	if ac.State == ui.AccountAuth {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("A revoked or mistyped password: sign out and sign in again with a new app-specific password")))
	}
	if ac.Server != "" {
		s.Body = append(s.Body, row("acct-server", tr.T("Server"), ac.Server))
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Address books")))
	for _, b := range ac.Books {
		n := ui.Node{Kind: ui.KindItem, ID: "book:" + b.ID, Title: b.Name, Trailing: onOff(b.On)}
		if b.On {
			n.Subtitle = tr.T("%d contacts, %d of them from the server", b.Contacts, b.Remote)
			if b.ReadOnly {
				n.Subtitle = tr.T("Read-only: %d contacts from the server, not changed on the phone", b.Contacts)
			}
			if b.Pending > 0 && !b.ReadOnly {
				n.Subtitle += " · " + tr.T("%d changes not sent yet", b.Pending)
			}
			n.Actions = []ui.Action{
				{ID: "book_off", Label: tr.T("Turn off, keep its contacts"), Key: "f", Primary: true,
					Confirm: tr.T("Stop syncing “%s”? Its contacts stay on the phone as they are, and sync again when it is on.", b.Name)},
				{ID: "book_off_remove", Label: tr.T("Turn off and remove its contacts"), Key: "x", Group: ui.GroupDanger,
					Confirm: tr.T("Stop syncing “%s” and take the %d contacts that came from it off the phone? They stay on the server.", b.Name, b.Remote)},
			}
		} else {
			n.Subtitle = tr.T("Not synced: its contacts are not on the phone")
			if b.Contacts > 0 {
				n.Subtitle = tr.T("Not synced: %d contacts stay on the phone as they were", b.Contacts)
			}
			if b.ReadOnly {
				n.Subtitle = tr.T("Read-only") + " · " + n.Subtitle
			}
			n.Actions = []ui.Action{{ID: "book_on", Label: tr.T("Turn on"), Primary: true}}
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, calendarRows(ac)...)
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "acct-mobile", Title: tr.T("Use mobile data"),
		Subtitle: tr.T("Off: it waits for Wi-Fi"), Trailing: onOff(ac.MobileData), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}},
		sdk.Section(tr.T("Sign out")),
		ui.Node{Kind: ui.KindItem, ID: "acct-out", Title: tr.T("Sign out, keep the contacts"), Actions: []ui.Action{{ID: keep.ID, Label: tr.T("Sign out"), Primary: true, Confirm: keep.Confirm}}},
		ui.Node{Kind: ui.KindItem, ID: "acct-out-remove", Title: tr.T("Sign out and remove its contacts"),
			Subtitle: tr.T("%d contacts came from the server", ac.Remote), Actions: []ui.Action{{ID: drop.ID, Label: tr.T("Sign out"), Primary: true, Group: ui.GroupDanger, Confirm: drop.Confirm}}},
		sdk.Styled(ui.StyleDim, tr.T("The system syncs every half hour, soon after a change on the phone, when the network comes back and when asked; without a network it waits. It keeps the password sealed and never shows it again.")))
	return s
}

// defaultScreen chooses the book new contacts go to.
func (p *app) defaultScreen() ui.Screen {
	s := ui.Screen{ID: "acctdefault", Title: tr.T("New contacts go to")}
	pick := func(key, title, sub string) ui.Node {
		n := ui.Node{Kind: ui.KindItem, ID: "def:" + key, Title: title, Subtitle: sub, Actions: []ui.Action{{ID: "def_pick", Label: tr.T("Choose"), Primary: true}}}
		if key == acctState.Default {
			n.Trailing = "✓"
		}
		return n
	}
	for _, ac := range acctState.Accounts {
		for _, b := range ac.Books {
			if b.On && !b.ReadOnly {
				s.Body = append(s.Body, pick(b.ID, b.Name, acctName(ac)+" · "+ac.User))
			}
		}
	}
	s.Body = append(s.Body, pick("", tr.T("On the phone only"), tr.T("Not synced with any account")),
		sdk.Styled(ui.StyleDim, tr.T("Contacts made on the phone, imported or moved from an old phone go here; Contacts can put one elsewhere as it is made.")))
	return s
}

// calendarRows are the calendar's sync on the account: a switch (turning it
// on finds the calendar on the server first), how it goes and its trouble,
// the calendar's name and what waits.
func calendarRows(ac ui.Account) []ui.Node {
	cal := ac.Calendar
	if cal == nil {
		return nil
	}
	var sub string
	switch {
	case cal.Finding:
		sub = tr.T("Looking for the calendar…")
	case !cal.On:
		sub = tr.T("Off: the events stay on this phone only")
	case cal.State == ui.AccountSyncing:
		sub = tr.T("Syncing…")
	case cal.Full:
		sub = tr.T("The phone's calendar is full: %d events from the server wait", cal.Waiting)
	case cal.State == ui.AccountAuth:
		sub = tr.T("Sign-in refused")
	case cal.State == ui.AccountFailed:
		sub = tr.T("Not synced")
	case ac.State == ui.AccountWaiting:
		sub = acctStatus(ac)
	case cal.LastSync == 0:
		sub = tr.T("Not synced yet")
	default:
		sub = tr.T("Synced %s ago", ago(cal.LastSync))
	}
	out := []ui.Node{{Kind: ui.KindItem, ID: "acct-calendar", Title: tr.T("Calendar"), Subtitle: sub, Trailing: onOff(cal.On),
		Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}}}
	if cal.Error != "" {
		out = append(out, sdk.Styled(ui.StyleWarn, cal.Error))
	}
	if !cal.On {
		return out
	}
	name := cal.Name
	if name == "" {
		name = tr.T("Calendar")
	}
	out = append(out, ui.Node{Kind: ui.KindItem, ID: "acct-calendar-name", Title: tr.T("Calendar on the server"), Subtitle: tr.T("%s, %d events", name, cal.Events)})
	if cal.Pending > 0 {
		out = append(out, ui.Node{Kind: ui.KindItem, ID: "acct-calendar-pending", Title: tr.T("Not sent yet"), Subtitle: tr.T("%d changes in the calendar", cal.Pending)})
	}
	return out
}

// acctAddScreen signs in to the Kario account (kind kario) or to another
// CardDAV server.
func (p *app) acctAddScreen(kind string) ui.Screen {
	s := ui.Screen{ID: "acctadd:" + kind, Title: tr.T("Other CardDAV account")}
	var body []ui.Node
	if kind == ui.AccountKario {
		s.Title = tr.T("Kario account")
		body = append(body, sdk.Styled(ui.StyleDim, tr.T("Your account's e-mail address and an app-specific password made for this phone on the Kario account (iPhone and DAVx⁵ get one each, too). Server: %s", acctState.KarioServer)))
	} else {
		body = append(body, sdk.Styled(ui.StyleDim, tr.T("The server's address (contacts.icloud.com, your Nextcloud's address, carddav.fastmail.com), or leave it empty to find it from an e-mail address. iCloud and Fastmail want an app-specific password.")),
			ui.Node{Kind: ui.KindInput, ID: "server", Placeholder: tr.T("Server"), Keyboard: "url"})
	}
	body = append(body,
		ui.Node{Kind: ui.KindInput, ID: "user", Placeholder: tr.T("User name or e-mail address"), Keyboard: "email"},
		ui.Node{Kind: ui.KindInput, ID: "pass", Placeholder: tr.T("App-specific password"), Secret: true, Submit: "acct_signin"},
		sdk.Button("acct_signin", tr.T("Sign in"), "acct_signin"))
	if acctState.signInErr != "" {
		body = append(body, sdk.Styled(ui.StyleWarn, acctState.signInErr))
	}
	s.Body = append(body, sdk.Styled(ui.StyleDim, tr.T("Only the system keeps the password, sealed by the keystore.")))
	return s
}

// accountsScreenByID builds one of these pages again (a restored process).
func (p *app) accountsScreenByID(id string) (ui.Screen, bool) {
	switch {
	case id == "accounts":
		p.watchAccounts()
		return p.accountsScreen(), true
	case strings.HasPrefix(id, "acct:"):
		p.watchAccounts()
		return p.accountScreen(strings.TrimPrefix(id, "acct:")), true
	case id == "acctdefault":
		return p.defaultScreen(), true
	case strings.HasPrefix(id, "acctadd:"):
		return p.acctAddScreen(strings.TrimPrefix(id, "acctadd:")), true
	}
	return ui.Screen{}, false
}

func (p *app) refreshAccounts() {
	for _, id := range p.pages {
		switch {
		case id == "accounts":
			p.a.Update(p.accountsScreen())
		case strings.HasPrefix(id, "acct:"):
			p.a.Update(p.accountScreen(strings.TrimPrefix(id, "acct:")))
		case id == "acctdefault":
			p.a.Update(p.defaultScreen())
		}
	}
	p.updateMenu()
}

// watchAccounts refreshes the pages every two seconds while one is open.
func (p *app) watchAccounts() {
	acctTick.Do(func() {
		go func() {
			for range time.Tick(2 * time.Second) {
				p.a.Post(func() {
					if slices.ContainsFunc(p.pages, func(id string) bool { return id == "accounts" || strings.HasPrefix(id, "acct:") }) {
						p.loadAccounts(p.refreshAccounts)
					}
				})
			}
		}()
	})
}

func (p *app) acctCall(capName string, args ui.AccountArgs, then func()) {
	var st ui.AccountState
	p.a.Go(capName, args, &st, func(err error) {
		if err != nil {
			if !ui.Cancelled(err) {
				p.a.Toast(err.Error())
			}
			p.loadAccounts(p.refreshAccounts)
			return
		}
		acctState.AccountState = st
		p.refreshAccounts()
		if then != nil {
			then()
		}
	})
}

// accountsAction handles the accounts pages; false for the rest.
func (p *app) accountsAction(e sdk.ActionEvent) bool {
	id, onAccount := strings.CutPrefix(e.Screen, "acct:")
	ac, _ := findAccount(id)
	switch {
	case onMenu(e.Screen) && e.Node == "accounts":
		p.watchAccounts()
		p.loadAccounts(func() { p.push(p.accountsScreen()) })
	case e.Screen == "accounts" && strings.HasPrefix(e.Node, "acct:"):
		p.push(p.accountScreen(strings.TrimPrefix(e.Node, "acct:")))
	case e.Screen == "accounts" && e.Node == "acct-default":
		p.push(p.defaultScreen())
	case e.Screen == "accounts" && (e.Node == "acct-kario" || e.Node == "acct-carddav"):
		acctState.signInErr = ""
		p.push(p.acctAddScreen(strings.TrimPrefix(e.Node, "acct-")))
	case strings.HasPrefix(e.Screen, "acctadd:") && e.Action == "acct_signin":
		kind := strings.TrimPrefix(e.Screen, "acctadd:")
		p.a.Toast(tr.T("Signing in…"))
		args := ui.AccountArgs{Kind: kind, Server: strings.TrimSpace(e.Fields["server"]), User: strings.TrimSpace(e.Fields["user"]), Password: e.Fields["pass"]}
		var st ui.AccountState
		p.a.Go(ui.CapAccountAdd, args, &st, func(err error) {
			if err != nil {
				acctState.signInErr = err.Error()
				p.a.Update(p.acctAddScreen(kind))
				return
			}
			acctState.AccountState, acctState.signInErr = st, ""
			p.a.Toast(tr.T("Signed in; the contacts are syncing"))
			// The page under it opens afresh, at its top.
			p.a.Pop()
			p.a.Replace(p.accountsScreen())
			p.updateMenu()
		})
	case e.Screen == "acctdefault" && strings.HasPrefix(e.Node, "def:"):
		key := strings.TrimPrefix(e.Node, "def:")
		p.acctCall(ui.CapAccountSet, ui.AccountArgs{Default: &key}, func() {
			p.a.Toast(tr.T("New contacts go to %s", bookLabel(key)))
			p.a.Pop()
		})
	case !onAccount:
		return false
	case e.Action == "acct_sync":
		p.acctCall(ui.CapAccountSync, ui.AccountArgs{ID: ac.ID}, nil)
	case e.Node == "acct-calendar" && e.Action == "toggle":
		on := ac.Calendar == nil || !ac.Calendar.On
		if on {
			p.a.Toast(tr.T("Looking for the calendar…"))
		}
		p.acctCall(ui.CapAccountSet, ui.AccountArgs{ID: ac.ID, Calendar: &on}, nil)
	case e.Node == "acct-mobile" && e.Action == "toggle":
		on := !ac.MobileData
		p.acctCall(ui.CapAccountSet, ui.AccountArgs{ID: ac.ID, MobileData: &on}, nil)
	case strings.HasPrefix(e.Node, "book:") && (e.Action == "book_on" || e.Action == "book_off" || e.Action == "book_off_remove"):
		on := e.Action == "book_on"
		p.acctCall(ui.CapAccountSet, ui.AccountArgs{ID: ac.ID, Book: strings.TrimPrefix(e.Node, "book:"), On: &on, RemoveContacts: e.Action == "book_off_remove"}, nil)
	case e.Action == "acct_out" || e.Action == "acct_out_remove":
		user := ac.User
		p.acctCall(ui.CapAccountRemove, ui.AccountArgs{ID: ac.ID, RemoveContacts: e.Action == "acct_out_remove"}, func() {
			p.a.Toast(tr.T("Signed out of “%s”", user))
			p.a.Pop()
		})
	default:
		return false
	}
	return true
}
