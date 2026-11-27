package ui

// AccountState is account.state: the contacts accounts the phone syncs
// with, the address book new contacts go to (Default: an AccountBook's ID,
// "" for the phone alone), and the product's Kario account server (""
// when the product has none: Settings offers only other CardDAV servers).
type AccountState struct {
	Accounts    []Account `json:"accounts"`
	Default     string    `json:"default,omitempty"`
	KarioServer string    `json:"kario_server,omitempty"`
	// Unrevoked answers account.remove of an account signed in with a
	// code when the server could not be told to revoke the phone's
	// password (offline): its name on the account's page (Phones signed
	// in), to sign it out there.
	Unrevoked string `json:"unrevoked,omitempty"`
	// LockLeft is the anti-theft lock's binding left on the server for
	// a Kario account signed out of when the server could not be told
	// (the phone's password signed out on the account's page, or no
	// network), nil for none: until its owner removes the phone on the
	// account's page (Anti-theft lock), the phone cannot be bound to
	// another account. It stays until the server says the binding is
	// gone, or the phone is bound again.
	LockLeft *AccountLockLeft `json:"lock_left,omitempty"`
}

// AccountLockLeft is a binding left on the server: the account's user
// name (its e-mail address) and this phone's device ID, as the account's
// page lists it.
type AccountLockLeft struct {
	User   string `json:"user"`
	Device string `json:"device"`
}

// AccountCode answers account.add with SendCode: the address the Kario
// account server sent a sign-in code to, and for how many seconds it
// holds.
type AccountCode struct {
	To        string `json:"to"`
	ExpiresIn int    `json:"expires_in"`
}

// ErrAccountConsent ends the error of account.add with a code for an
// address that has no Kario account yet: the user is to agree to the
// account's privacy terms first (Agree); the code still holds.
const ErrAccountConsent = "a new Kario account needs your consent"

// An account's kind: the Kario account, or another CardDAV server.
const (
	AccountKario   = "kario"
	AccountCardDAV = "carddav"
)

// An account's State: synced and waiting for the next sync, syncing,
// waiting for a network (Waiting says which), the last sync failed and is
// tried again by itself (Error), or the server refused the password and
// nothing is tried until the user acts.
const (
	AccountIdle    = "idle"
	AccountSyncing = "syncing"
	AccountWaiting = "waiting"
	AccountFailed  = "failed"
	AccountAuth    = "auth"
)

// What a waiting account waits for: any network, or Wi-Fi (it does not
// sync over mobile data).
const (
	AccountWaitNetwork = "network"
	AccountWaitWifi    = "wifi"
)

// Account is an account as Settings sees it: never its password. Device:
// the Kario account signed in with a code, and the password the server
// made for this phone has this name on the account's page ("" for a
// password the user typed).
type Account struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Server string `json:"server"`
	User   string `json:"user"`
	Device string `json:"device,omitempty"`
	// Books are the server's address books, those the user may write to
	// first.
	Books   []AccountBook `json:"books"`
	State   string        `json:"state"`
	Waiting string        `json:"waiting,omitempty"`
	Error   string        `json:"error,omitempty"`
	// LastSync and NextSync are Unix seconds (0: none yet, none planned).
	LastSync int64 `json:"last_sync,omitempty"`
	NextSync int64 `json:"next_sync,omitempty"`
	// Contacts, Remote and Pending are its books' together; Failed the
	// cards the last sync could not write.
	Contacts int `json:"contacts"`
	Remote   int `json:"remote"`
	Pending  int `json:"pending,omitempty"`
	Failed   int `json:"failed,omitempty"`
	// While syncing: what it is at ("pull", "push"), how far, of how many
	// (0: not known yet).
	Phase string `json:"phase,omitempty"`
	Done  int    `json:"done,omitempty"`
	Total int    `json:"total,omitempty"`
	// MobileData: it syncs over mobile data too, not only Wi-Fi.
	MobileData bool `json:"mobile_data"`
	// Calendar is the calendar's sync on the same account (CalDAV), nil
	// while it is off and was never on.
	Calendar *AccountCalendar `json:"calendar,omitempty"`
	// LockStays: signing out of this Kario account now would leave the
	// anti-theft lock's binding on the server (AccountState.LockLeft), as
	// the phone cannot end it: no network (AccountWaitNetwork), or the
	// server refuses the account's password (AccountAuth); "" when it can,
	// or the lock is not bound to the account.
	LockStays string `json:"lock_stays,omitempty"`
	// LockTaken: the anti-theft lock could not be bound to this Kario
	// account, as the server has the phone bound to another account still
	// (409; its owner removes it on that account's page): this phone's
	// device ID, as that page lists it. Nothing tries again until
	// account.set with RetryLock, the check of AccountState.LockLeft finds
	// that binding gone, or the account signs in again.
	LockTaken string `json:"lock_taken,omitempty"`
}

// AccountCalendar is the calendar's sync as Settings sees it: on or off
// (account.set Calendar; Finding while turning on looks for the calendar),
// the calendar's name on the server, Error for the last sync's problem
// (State as an account's), LastSync (Unix seconds), the events the server
// has, the phone's changes not on the server yet (Pending), and the
// server's the calendar app has not taken (Waiting; Full: it has no room).
type AccountCalendar struct {
	On       bool   `json:"on"`
	Finding  bool   `json:"finding,omitempty"`
	Name     string `json:"name,omitempty"`
	State    string `json:"state,omitempty"`
	Error    string `json:"error,omitempty"`
	LastSync int64  `json:"last_sync,omitempty"`
	Events   int    `json:"events"`
	Pending  int    `json:"pending,omitempty"`
	Waiting  int    `json:"waiting,omitempty"`
	Full     bool   `json:"full,omitempty"`
}

// AccountBook is an address book of an account: its ID (a Contact's
// Book), its name on the server, and whether it syncs; off, its contacts
// stay on the phone as they were and sync again once it is on. Contacts
// are the phone's contacts in it, Remote those of them that came from the
// server (signing out may take them off the phone), Pending the phone's
// changes not on the server yet. A ReadOnly book only comes from the
// server: the phone writes nothing to it.
type AccountBook struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	On       bool   `json:"on"`
	ReadOnly bool   `json:"read_only,omitempty"`
	Contacts int    `json:"contacts"`
	Remote   int    `json:"remote"`
	Pending  int    `json:"pending,omitempty"`
}

// AccountArgs: account.add takes Kind, Server (another CardDAV server's;
// the Kario account's is the product's), User and Password, and answers
// account.state once the server has been found and the password accepted.
// The Kario account also signs in with a code: account.add with User (its
// e-mail address) and SendCode has the server send one there and answers
// AccountCode; then with User and Code (and Agree, the user's consent, for
// an address without an account) instead of a Password, as above.
// account.sync and account.remove take ID; account.set takes ID and
// MobileData or Calendar, or ID, Book and On, or Default alone (an
// AccountBook's ID, "" for the phone), or ID and RetryLock (binding the
// anti-theft lock again after Account.LockTaken; it answers once the
// server did, with an error unless the lock is bound). account.remove,
// and account.set turning a book off, with RemoveContacts also take off
// the phone the contacts that came from the account (or the book);
// account.remove also the calendar's events that came from the account.
// account.state with Check asks the server at once whether
// AccountState.LockLeft is still there, and answers once it knows.
type AccountArgs struct {
	ID             string  `json:"id,omitempty"`
	Kind           string  `json:"kind,omitempty"`
	Server         string  `json:"server,omitempty"`
	User           string  `json:"user,omitempty"`
	Password       string  `json:"password,omitempty"`
	SendCode       bool    `json:"send_code,omitempty"`
	Code           string  `json:"code,omitempty"`
	Agree          bool    `json:"agree,omitempty"`
	RemoveContacts bool    `json:"remove_contacts,omitempty"`
	MobileData     *bool   `json:"mobile_data,omitempty"`
	Calendar       *bool   `json:"calendar,omitempty"`
	Book           string  `json:"book,omitempty"`
	On             *bool   `json:"on,omitempty"`
	Default        *string `json:"default,omitempty"`
	Check          bool    `json:"check,omitempty"`
	RetryLock      bool    `json:"retry_lock,omitempty"`
}

// CalChange is an event of the calendar app with its changed occurrences
// (one UID) as iCalendar, or its deletion (Gone), between the app and the
// system's calendar sync. From the system, Copy marks the phone's own edit
// kept as a new event because the server changed the event too, First one
// of the first sync's (the calendar as it was on the server: nothing new
// to point out), and Seq numbers it for calendar.pull's Taken; from the
// app, All marks the hand-over of all its events that a first sync asks
// for.
type CalChange struct {
	UID   string `json:"uid"`
	ICS   string `json:"ics,omitempty"`
	Gone  bool   `json:"gone,omitempty"`
	Copy  bool   `json:"copy,omitempty"`
	First bool   `json:"first,omitempty"`
	All   bool   `json:"all,omitempty"`
	Seq   int    `json:"seq,omitempty"`
}

// CalPullArgs is calendar.pull: the changes of the last answer the app
// took (Seq), and Full when it stopped for want of room (the rest wait).
type CalPullArgs struct {
	Taken []int `json:"taken,omitempty"`
	Full  bool  `json:"full,omitempty"`
}

// CalPull answers calendar.pull: the account the calendar syncs with (""
// for none: nothing to do), the server's changes in order, whether more
// wait, and WantAll when the app is to hand over all its events. Schedule:
// the account's server schedules (RFC 6638): it sends the invitations,
// updates, cancellations and answers of what the app hands over, so the
// app sends no iTIP files for the events it schedules; Addresses are the
// user's own there (mailto:), the organizer and attendee they are.
type CalPull struct {
	Account   string      `json:"account"`
	Items     []CalChange `json:"items"`
	More      bool        `json:"more,omitempty"`
	WantAll   bool        `json:"want_all,omitempty"`
	Schedule  bool        `json:"schedule,omitempty"`
	Addresses []string    `json:"addresses,omitempty"`
}

// CalPushArgs is calendar.push: the app's changes; Done ends the
// hand-over of all its events.
type CalPushArgs struct {
	Items []CalChange `json:"items"`
	Done  bool        `json:"done,omitempty"`
}

// CalPushRes answers calendar.push: the account ("" for none: the changes
// are not kept) and the changes not on the server yet.
type CalPushRes struct {
	Account string `json:"account"`
	Pending int    `json:"pending"`
}
