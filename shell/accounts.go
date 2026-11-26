package shell

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/caldav"
	"github.com/kario-os/kario/carddav"
	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The contacts accounts (Settings › Accounts; account.*): CardDAV accounts,
// the Kario account (the product's server: Config.AccountURL,
// services.env's PHONE_ACCOUNT_SERVER) or other servers', each with its
// address books on it (those the user may write to first, then the
// read-only ones), each book switched on or off; the books on are kept in
// two-way sync with the contacts store by package carddav (a read-only one
// only from the server: its contacts are not changed, moved or deleted on
// the phone, contacts.go), a contact in one book at most (ui.Contact.Book).
// New contacts go to the default book (accountFile.Default, never a
// read-only one), or stay on the phone. The Shell runs the syncs, one account at a time (its books one
// after the other): as a system job every accountEvery (jobs.go: it rides
// the apps' wakes and wakes the phone for its deadline), accountAfterEdit
// after a change on the phone, on Sync now, and at once when the network
// comes back or changes (netwatch.go) for an account that waits for one or
// failed for want of one. A run goes off the loop, and the phone stays
// awake for it (sleep.go); its changes to the contacts come to the loop a
// batch at a time. Without a network, or without Wi-Fi for an account that
// does not use mobile data, it waits; a network event (or, should none
// come, a look every accountNetCheck) starts it. A run the network or the
// server cut short is tried again after accountRetryMin, doubling up to
// accountRetryMax (or the server's Retry-After), sooner on a network event
// but at most every accountNetMin; a password the server refuses stops the
// syncs until Sync now or signing in again. The account's calendar syncs
// in the same runs (calsync.go).
//
// An app-specific password comes from Settings once and phone-keystored
// seals it (keystore.SealSecret, for "carddav:<id>"): the account's record
// (accounts.json, 0600) has only the sealed form, a run opens it for itself
// and drops it after. Nothing logs it, nor the user name: the log names an
// account by its ID and its server's host.

const (
	accountJob       = "contacts-sync"
	accountEvery     = 30 * time.Minute
	accountAfterEdit = 20 * time.Second
	accountNetCheck  = 5 * time.Minute
	accountNetMin    = 10 * time.Second
	accountRetryMin  = time.Minute
	accountRetryMax  = 6 * time.Hour
	// accountTimeout bounds one run: a first sync of thousands of cards
	// takes minutes.
	accountTimeout = 30 * time.Minute
)

// Why the last run ended without syncing (accountRecord.Problem).
const (
	problemAuth     = "auth"
	problemNetwork  = "network"
	problemServer   = "server"
	problemFull     = "full"
	problemGone     = "gone"
	problemKeystore = "keystore"
	problemStore    = "store"
	problemOther    = "other"
)

type accountRecord struct {
	ID     string        `json:"id"`
	Kind   string        `json:"kind"`
	Server string        `json:"server"`
	User   string        `json:"user"`
	Sealed string        `json:"password_sealed"`
	Device string        `json:"device,omitempty"` // signed in with a code: the name of the server's password for the phone (accounts_code.go)
	Books  []*bookRecord `json:"books"`
	// Book is the one book of a record from before accounts had several;
	// loadAccounts makes it the first of Books.
	Book *carddav.Book `json:"book,omitempty"`
	// MobileData: syncs over mobile data too.
	MobileData bool  `json:"mobile_data"`
	Added      int64 `json:"added"`
	LastSync   int64 `json:"last_sync,omitempty"`
	// Problem and Detail: why the last run failed (problem*), and what
	// the server or the network said, for the log and Settings.
	Problem string `json:"problem,omitempty"`
	Detail  string `json:"detail,omitempty"`
	// Failures counts the runs in a row that failed on their own; the
	// next is not before NotBefore.
	Failures  int       `json:"failures,omitempty"`
	NotBefore time.Time `json:"not_before,omitempty"`
	// LockTaken: binding the anti-theft lock to the account got 409, the
	// server having the phone bound to another account; this phone's
	// device ID (antitheft.go).
	LockTaken string `json:"lock_taken,omitempty"`
	// Failed and Reason: cards the last run could not write, and why.
	Failed int    `json:"failed,omitempty"`
	Reason string `json:"reason,omitempty"`
	// The calendar's sync (calsync.go): on, the calendar found, and how
	// its last run went, as the fields above for the contacts.
	CalOn       bool            `json:"calendar_on,omitempty"`
	Calendar    caldav.Calendar `json:"calendar"`
	CalLastSync int64           `json:"calendar_last_sync,omitempty"`
	CalProblem  string          `json:"calendar_problem,omitempty"`
	CalDetail   string          `json:"calendar_detail,omitempty"`
	CalFailures int             `json:"calendar_failures,omitempty"`
	CalFailed   int             `json:"calendar_failed,omitempty"`
	CalReason   string          `json:"calendar_reason,omitempty"`
}

// bookRecord is an address book of an account: its ID in the account
// ("b1"), the book as discovery found it, and whether it syncs.
type bookRecord struct {
	ID   string       `json:"id"`
	Book carddav.Book `json:"book"`
	On   bool         `json:"on"`
}

// bookKey is a book as the contacts name it (ui.Contact.Book).
func bookKey(account, book string) string { return account + "/" + book }

// readOnlyBook: key (a Contact's Book) is a read-only book of an account.
func (c *Core) readOnlyBook(key string) bool {
	if key == "" {
		return false
	}
	if !c.accounts.loaded {
		c.loadAccounts()
	}
	_, b := c.accounts.bookOf(key)
	return b != nil && b.Book.ReadOnly
}

// bookTakes says why a contact may not go to key (a Contact's Book or
// ui.BookPhone): a book that is not on, or a read-only one.
func (c *Core) bookTakes(key string) error {
	if key == ui.BookPhone {
		return nil
	}
	_, b := c.accounts.bookOf(key)
	switch {
	case b == nil || !b.On:
		return errors.New(tr.T("No such address book"))
	case b.Book.ReadOnly:
		return errors.New(tr.T("“%s” is a read-only address book", b.Book.Name))
	}
	return nil
}

func (r *accountRecord) book(key string) *bookRecord {
	for _, b := range r.Books {
		if bookKey(r.ID, b.ID) == key {
			return b
		}
	}
	return nil
}

// syncs: the account has something on to sync, a book or the calendar.
func (r *accountRecord) syncs() bool { return r.CalOn || r.anyOn() }

func (r *accountRecord) anyOn() bool {
	for _, b := range r.Books {
		if b.On {
			return true
		}
	}
	return false
}

type accountFile struct {
	Seq      int              `json:"seq"`
	Accounts []*accountRecord `json:"accounts"`
	// Default is the book new contacts go to, "" for the phone.
	Default string `json:"default,omitempty"`
	// LockLeft: the anti-theft lock's binding left on the server when
	// the phone signed out (antitheft.go).
	LockLeft *leftBinding `json:"lock_left,omitempty"`
}

type accountRun struct {
	id     string
	gen    int
	at     time.Time // when it started, on the loop's clock
	cancel context.CancelFunc
	phase  string
	done   int
	total  int
}

// accountRT is what an account's syncs are waiting for, on the loop.
type accountRT struct {
	// gen counts sign-ins and sign-outs of the account: a run of an
	// account gone does not save its state or touch the contacts.
	gen int
	// pending: a run is wanted from dueAt on (the job, an edit, Sync
	// now); forced: by the user, past a refused password and a backoff;
	// netRetry: by a network event, past the backoff once.
	pending, forced, netRetry bool
	dueAt                     time.Time
	// waiting is what the account waits for (ui.AccountWait*), and when
	// the network was last looked at; tried: when the last run began.
	waiting string
	netAt   time.Time
	probing bool
	tried   time.Time
}

type accountsShell struct {
	loaded bool
	file   accountFile
	// states are the books' sync states while no run has them, by key.
	states map[string]*carddav.State
	rt     map[string]*accountRT
	run    *accountRun
	// gens outlive an account's runtime: a run of an account signed out
	// and in again (a new ID, but a run may still end) is not its run.
	gen int
	// saveMu orders the state files' writes (by the run) with their
	// removal.
	saveMu  sync.Mutex
	addBusy bool
	// net tells whether there is a network and whether it is metered
	// (nil: the real one; the tests' and the selftest's otherwise).
	net func(then func(online, metered bool))
	// netWatching: the network's events are followed (netwatch.go).
	netWatching bool
	// offline: there was no network when it was last looked at for
	// signing out with the anti-theft lock bound (lockStays), at offAt;
	// offBusy: looking.
	offline, offBusy bool
	offAt            time.Time
	// sim is the selftest's server (accounts_headless.go).
	sim *carddavSim
	// cal is the calendar's sync on the account (calsync.go).
	cal calendarShell
}

func (c *Core) accountsPath() string { return filepath.Join(c.cfg.StateDir, "shell", "accounts.json") }

func (c *Core) syncStatePath(key string) string {
	return filepath.Join(c.cfg.StateDir, "shell", "carddav-"+strings.ReplaceAll(key, "/", "-")+".json")
}

func (a *accountsShell) rtOf(id string) *accountRT {
	if a.rt == nil {
		a.rt = map[string]*accountRT{}
	}
	rt := a.rt[id]
	if rt == nil {
		a.gen++
		rt = &accountRT{gen: a.gen}
		a.rt[id] = rt
	}
	return rt
}

// genOf is the account's generation, -1 once it is gone.
func (a *accountsShell) genOf(id string) int {
	if rt := a.rt[id]; rt != nil && a.find(id) != nil {
		return rt.gen
	}
	return -1
}

func (c *Core) loadAccounts() {
	a := &c.accounts
	a.loaded = true
	a.states = map[string]*carddav.State{}
	if raw, err := os.ReadFile(c.accountsPath()); err == nil {
		if err := json.Unmarshal(raw, &a.file); err != nil {
			c.log.Printf("accounts: %s: %v", c.accountsPath(), err)
		}
	}
	for _, r := range a.file.Accounts {
		if r.Book != nil && len(r.Books) == 0 {
			c.migrateAccount(r)
		}
	}
	if len(a.file.Accounts) == 0 {
		c.setSystemJob(accountJob, 0)
		return
	}
	c.setSystemJob(accountJob, accountEvery)
	for _, r := range a.file.Accounts {
		if r.CalOn {
			c.loadCalendar(r)
		}
		rt := a.rtOf(r.ID)
		rt.pending, rt.dueAt = true, c.now()
		n := 0
		for _, b := range r.Books {
			if b.On {
				n += len(c.bookState(bookKey(r.ID, b.ID)).Linked())
			}
		}
		c.log.Printf("accounts: %s on %s, %d books, %d contacts%s", r.ID, hostOf(r.Server, firstBookURL(r)), len(r.Books), n, calendarLog(r))
	}
	c.watchNetEvents()
}

// migrateAccount makes an account of the time when there was one with one
// book an account with that book, the default, and every contact in it,
// as they were all synced with it.
func (c *Core) migrateAccount(r *accountRecord) {
	a := &c.accounts
	r.Books, r.Book = []*bookRecord{{ID: "b1", Book: *r.Book, On: true}}, nil
	key := bookKey(r.ID, "b1")
	old := filepath.Join(c.cfg.StateDir, "shell", "carddav-"+r.ID+".json")
	if err := os.Rename(old, c.syncStatePath(key)); err != nil && !errors.Is(err, os.ErrNotExist) {
		c.log.Printf("accounts: %v", err)
	}
	a.file.Default = key
	book := c.contactBook()
	book.NewBook = key
	if _, err := book.MoveBook("", key); err != nil {
		c.log.Printf("accounts: %v", err)
	}
	if err := c.saveAccounts(); err != nil {
		c.log.Printf("accounts: %v", err)
	}
	c.log.Printf("accounts: %s has its book as %s now", r.ID, key)
}

func firstBookURL(r *accountRecord) string {
	if len(r.Books) > 0 {
		return r.Books[0].Book.URL
	}
	return ""
}

// accountDefault is the book new contacts go to ("" the phone).
func (c *Core) accountDefault() string {
	if !c.accounts.loaded {
		c.loadAccounts()
	}
	return c.accounts.file.Default
}

func (c *Core) setDefaultBook(key string) {
	c.accounts.file.Default = key
	c.contactBook().NewBook = key
}

func (c *Core) saveAccounts() error {
	raw, err := json.MarshalIndent(c.accounts.file, "", "  ")
	if err != nil {
		return err
	}
	return contacts.WriteFile(c.accountsPath(), raw)
}

func (c *Core) loadSyncState(key string) *carddav.State {
	st := &carddav.State{}
	if raw, err := os.ReadFile(c.syncStatePath(key)); err == nil {
		if err := json.Unmarshal(raw, st); err != nil {
			c.log.Printf("accounts: %s: the sync state did not parse (%v); syncing from the start", key, err)
			st = &carddav.State{}
		}
	}
	return st
}

// bookState is a book's sync state while no run has it.
func (c *Core) bookState(key string) *carddav.State {
	a := &c.accounts
	if st := a.states[key]; st != nil {
		return st
	}
	st := c.loadSyncState(key)
	a.states[key] = st
	return st
}

// saveSyncState writes a run's state, unless its account is gone (gen).
func (c *Core) saveSyncState(id, key string, gen int, st *carddav.State) error {
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
	return contacts.WriteFile(c.syncStatePath(key), raw)
}

var errAccountGone = errors.New("the account was removed")

func (a *accountsShell) find(id string) *accountRecord {
	for _, r := range a.file.Accounts {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// bookOf is the account and book of a book's key.
func (a *accountsShell) bookOf(key string) (*accountRecord, *bookRecord) {
	id, _, _ := strings.Cut(key, "/")
	if r := a.find(id); r != nil {
		return r, r.book(key)
	}
	return nil, nil
}

// hostOf is the server's host for the log, never its path (a user's name
// may be in it).
func hostOf(urls ...string) string {
	for _, s := range urls {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return "?"
}

func (c *Core) accountServer(kind, typed string) string {
	if kind == ui.AccountKario {
		return c.cfg.AccountURL
	}
	return strings.TrimSpace(typed)
}

// accountsTick runs from the shell's tick: a run due, once the network
// lets it; one account at a time.
func (c *Core) accountsTick() {
	a := &c.accounts
	if !a.loaded {
		c.loadAccounts()
	}
	c.theftCheck()
	if a.run != nil {
		return
	}
	now := c.now()
	for _, r := range a.file.Accounts {
		rt := a.rtOf(r.ID)
		if !rt.pending || rt.probing {
			continue
		}
		if now.Before(rt.dueAt) || (!rt.forced && (r.Problem == problemAuth || !rt.netRetry && now.Before(r.NotBefore))) {
			continue
		}
		if !r.syncs() {
			rt.pending, rt.forced, rt.netRetry = false, false, false
			continue
		}
		if rt.waiting != "" && now.Sub(rt.netAt) < accountNetCheck {
			continue
		}
		id := r.ID
		rt.netAt, rt.probing = now, true
		c.accountNet(r, func(online, metered bool) {
			rt.probing = false
			r := a.find(id)
			if r == nil || a.run != nil || !rt.pending {
				return
			}
			was := rt.waiting
			switch {
			case !online:
				rt.waiting = ui.AccountWaitNetwork
			case metered && !r.MobileData:
				rt.waiting = ui.AccountWaitWifi
			default:
				rt.waiting = ""
			}
			if rt.waiting != "" {
				if rt.waiting != was {
					c.log.Printf("accounts: %s waits for %s", r.ID, rt.waiting)
				}
				return
			}
			c.startSync(r)
		})
		return
	}
}

// accountNet: whether there is a network for the account's server, and
// whether it is metered (mobile data). A developer machine has its own,
// and a server on this machine needs none.
func (c *Core) accountNet(r *accountRecord, then func(online, metered bool)) {
	if f := c.accounts.net; f != nil {
		f(then)
		return
	}
	if c.cfg.SKU.ID == "" || loopbackURL(firstBookURL(r)) {
		then(true, false)
		return
	}
	mobile := !c.settings.Airplane && !c.dataOff() && c.tel != nil && c.tel.DataState == "connected"
	wifi := c.wifi
	go func() {
		onWifi := false
		if wifi != nil {
			st, err := wifi.state()
			onWifi = err == nil && st.State == "connected"
		}
		c.Post(func() { then(onWifi || mobile, !onWifi) })
	}()
}

func loopbackURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
}

// accountsNetChanged: the network came back or changed. An account that
// waits for one looks at once; one whose last run failed for want of one
// tries again, past its backoff, unless it tried within accountNetMin;
// what signing out would leave behind is looked at again (lookOffline).
func (c *Core) accountsNetChanged() {
	a := &c.accounts
	if !a.loaded {
		return
	}
	now := c.now()
	for _, r := range a.file.Accounts {
		rt := a.rtOf(r.ID)
		switch {
		case rt.waiting != "" && rt.pending:
			rt.netAt = time.Time{}
		case r.Problem == problemNetwork && rt.pending && now.Sub(rt.tried) >= accountNetMin:
			rt.netRetry, rt.netAt = true, time.Time{}
			if rt.dueAt.After(now) {
				rt.dueAt = now
			}
		}
	}
	c.lookOffline(true, nil)
	c.accountsTick()
}

// accountsDue: the job's time; a run of each account once the network
// lets it.
func (c *Core) accountsDue() {
	a := &c.accounts
	for _, r := range a.file.Accounts {
		rt := a.rtOf(r.ID)
		rt.pending = true
		if (a.run == nil || a.run.id != r.ID) && c.now().After(rt.dueAt) {
			rt.dueAt = c.now()
		}
	}
}

// accountsLocalChange: the user changed the contacts; a run soon after
// the last change.
func (c *Core) accountsLocalChange() {
	a := &c.accounts
	if !a.loaded {
		c.loadAccounts()
	}
	for _, r := range a.file.Accounts {
		rt := a.rtOf(r.ID)
		rt.pending, rt.dueAt = true, c.now().Add(accountAfterEdit)
	}
}

// bookRun is one book of a run: its key, the book and its state.
type bookRun struct {
	key  string
	book carddav.Book
	st   *carddav.State
}

// startSync starts a run of the account's books, off the loop.
func (c *Core) startSync(r *accountRecord) {
	a := &c.accounts
	rt := a.rtOf(r.ID)
	ctx, cancel := context.WithTimeout(context.Background(), accountTimeout)
	run := &accountRun{id: r.ID, gen: rt.gen, at: c.now(), cancel: cancel}
	a.run, rt.pending, rt.forced, rt.netRetry, rt.tried = run, false, false, false, c.now()
	var books []*bookRun
	for _, b := range r.Books {
		if b.On {
			key := bookKey(r.ID, b.ID)
			books = append(books, &bookRun{key: key, book: b.Book, st: c.bookState(key)})
			delete(a.states, key)
		}
	}
	cst := c.takeCalendarState(r)
	rec, server := *r, c.accountServer(r.Kind, r.Server)
	c.log.Printf("accounts: %s syncing %d books with %s", rec.ID, len(books), hostOf(firstBookURL(r)))
	go func() {
		defer cancel()
		res, err := c.syncRun(ctx, rec, server, run, books)
		var cr calendarRun
		if cst != nil && calendarMayRun(err) {
			cr = c.runCalendar(ctx, rec, server, run, cst)
		}
		c.Post(func() {
			c.syncDone(run, books, res, err)
			if cst != nil {
				c.calendarDone(run, cst, cr)
			}
		})
	}()
}

// syncRun is one run, off the loop: each book in turn, the first error
// ending it. A book the server no longer has at its address is looked for
// again, once, from server, by its name.
func (c *Core) syncRun(ctx context.Context, r accountRecord, server string, run *accountRun, books []*bookRun) (carddav.Result, error) {
	var total carddav.Result
	pass, err := c.openPassword(r)
	if err != nil {
		return total, err
	}
	client := &carddav.Client{User: r.User, Password: pass, UserAgent: Brand + " CardDAV"}
	groups := ""
	if r.Kind == ui.AccountKario {
		groups = carddav.GroupsVCard
	}
	for _, b := range books {
		key := b.key
		s := &carddav.Sync{Client: client, Book: b.book, Key: key, Groups: groups, Local: &loopContacts{c: c, id: r.ID, gen: run.gen}, State: b.st,
			Save: func(st *carddav.State) error { return c.saveSyncState(r.ID, key, run.gen, st) },
			Notify: func(phase string, done, total int) {
				c.Post(func() { run.phase, run.done, run.total = phase, done, total })
			}}
		res, err := s.Run(ctx)
		if errors.Is(err, carddav.ErrNotFound) {
			if found, derr := client.DiscoverAll(ctx, server); derr == nil {
				for _, nb := range found {
					if nb.Name == b.book.Name || len(found) == 1 {
						s.Book = nb
						res, err = s.Run(ctx)
						break
					}
				}
			}
		}
		b.book = s.Book
		total.Added += res.Added
		total.Changed += res.Changed
		total.Deleted += res.Deleted
		total.Uploaded += res.Uploaded
		total.Removed += res.Removed
		total.Conflicts += res.Conflicts
		total.Failed += res.Failed
		if total.Problem == nil {
			total.Problem = res.Problem
		}
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// openPassword asks phone-keystored to open the account's password.
func (c *Core) openPassword(r accountRecord) (string, error) {
	var out struct {
		Data string `json:"data"`
	}
	if err := c.broker.rpc(svc.Keystored, keystore.MethodSecretOpen, map[string]string{"purpose": "carddav:" + r.ID, "sealed": r.Sealed}, &out); err != nil {
		return "", fmt.Errorf("%w: %v", errKeystore, err)
	}
	raw, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		return "", errKeystore
	}
	return string(raw), nil
}

func (c *Core) sealPassword(id, pass string) (string, error) {
	var out struct {
		Sealed string `json:"sealed"`
	}
	args := map[string]string{"purpose": "carddav:" + id, "data": base64.StdEncoding.EncodeToString([]byte(pass))}
	if err := c.broker.rpc(svc.Keystored, keystore.MethodSecretSeal, args, &out); err != nil || out.Sealed == "" {
		return "", fmt.Errorf("%w: %v", errKeystore, err)
	}
	return out.Sealed, nil
}

var errKeystore = errors.New("the keystore cannot keep or open the password")

// problemOf is what an error a run or a sign-in ended with comes to.
func problemOf(err error) (problem, detail string) {
	var se *carddav.StatusError
	var ne *carddav.NetError
	switch {
	case errors.Is(err, carddav.ErrAuth):
		return problemAuth, ""
	case errors.As(err, &ne), errors.Is(err, context.DeadlineExceeded):
		return problemNetwork, err.Error()
	case errors.As(err, &se):
		return problemServer, strconv.Itoa(se.Code)
	case errors.Is(err, carddav.ErrFull):
		return problemFull, ""
	case errors.Is(err, carddav.ErrNotFound), errors.Is(err, carddav.ErrNoBook):
		return problemGone, ""
	case errors.Is(err, errKeystore):
		return problemKeystore, ""
	case errors.Is(err, errLoopBusy):
		return problemStore, err.Error()
	}
	return problemOther, err.Error()
}

// accountRefused: the server refused the account's password, to the
// contacts' sync, the calendar's or the anti-theft lock's binding, which
// all sign in with it: nothing tries it again until the user acts (Sync
// now, signing in again). The caller saves.
func (c *Core) accountRefused(r *accountRecord) {
	r.Problem, r.Detail, r.Failures, r.NotBefore = problemAuth, "", 0, time.Time{}
}

// syncDone takes a run's end on the loop.
func (c *Core) syncDone(run *accountRun, books []*bookRun, res carddav.Result, err error) {
	a := &c.accounts
	if a.run == run {
		a.run = nil
	}
	if run.gen != a.genOf(run.id) {
		return
	}
	r := a.find(run.id)
	for _, b := range books {
		a.states[b.key] = b.st
		if br := r.book(b.key); br != nil {
			br.Book = b.book
		}
	}
	r.Failed, r.Reason = res.Failed, ""
	if res.Problem != nil {
		r.Reason = cardReason(res.Problem)
	}
	now := c.now()
	if err == nil {
		r.Problem, r.Detail, r.Failures, r.NotBefore = "", "", 0, time.Time{}
		r.LastSync = now.Unix()
		c.log.Printf("accounts: %s synced: +%d ~%d -%d on the phone, %d written and %d deleted on the server, %d merged, %d not written",
			r.ID, res.Added, res.Changed, res.Deleted, res.Uploaded, res.Removed, res.Conflicts, res.Failed)
	} else {
		r.Problem, r.Detail = problemOf(err)
		wait := time.Duration(0)
		if carddav.Temporary(err) {
			r.Failures++
			wait = accountRetryMin << min(r.Failures-1, 16)
			wait = max(min(wait, accountRetryMax), carddav.RetryAfter(err))
			r.NotBefore = now.Add(wait)
			rt := a.rtOf(r.ID)
			rt.pending, rt.dueAt = true, r.NotBefore
		}
		c.log.Printf("accounts: %s did not sync (%s %s); next try %s", r.ID, r.Problem, r.Detail, map[bool]string{true: "in " + wait.Round(time.Second).String(), false: "when asked"}[wait > 0])
	}
	if serr := c.saveAccounts(); serr != nil {
		c.log.Printf("accounts: %v", serr)
	}
}

// cardReason is why cards could not be written, for Settings.
func cardReason(err error) string {
	switch {
	case errors.Is(err, carddav.ErrTooLarge):
		return "too_large"
	case errors.Is(err, carddav.ErrRefused):
		return "refused"
	}
	return "other"
}

// loopContacts is the contacts store for a run: each call is one step of
// the loop, none once the account is gone.
type loopContacts struct {
	c   *Core
	id  string
	gen int
}

var errLoopBusy = errors.New("the system did not get to the contacts in time")

func (l *loopContacts) onLoop(f func()) error {
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

func (l *loopContacts) local() carddav.Local {
	return carddav.StoreLocal(l.c.contactBook(), l.c.notifyContacts)
}

func (l *loopContacts) List() ([]ui.Contact, error) {
	var out []ui.Contact
	var lerr error
	err := l.onLoop(func() { out, lerr = l.local().List() })
	return out, errors.Join(err, lerr)
}

func (l *loopContacts) Groups() ([]ui.ContactGroup, error) {
	var out []ui.ContactGroup
	var lerr error
	err := l.onLoop(func() { out, lerr = l.local().Groups() })
	return out, errors.Join(err, lerr)
}

func (l *loopContacts) Photo(key string) ([]byte, error) {
	var out []byte
	var lerr error
	err := l.onLoop(func() { out, lerr = l.local().Photo(key) })
	return out, errors.Join(err, lerr)
}

func (l *loopContacts) Apply(ops []carddav.Op) ([]carddav.Done, error) {
	var out []carddav.Done
	var aerr error
	err := l.onLoop(func() { out, aerr = l.local().Apply(ops) })
	return out, errors.Join(err, aerr)
}

// accountView is the account as Settings sees it.
func (c *Core) accountView(r *accountRecord) ui.Account {
	a := &c.accounts
	rt := a.rtOf(r.ID)
	v := ui.Account{ID: r.ID, Kind: r.Kind, Server: r.Server, User: r.User, Device: r.Device, LastSync: r.LastSync, MobileData: r.MobileData,
		Failed: r.Failed, Books: []ui.AccountBook{}}
	if r.Kind == ui.AccountKario {
		v.Server = c.cfg.AccountURL
	}
	list := c.contacts()
	for _, b := range r.Books {
		key := bookKey(r.ID, b.ID)
		bv := ui.AccountBook{ID: key, Name: b.Book.Name, On: b.On, ReadOnly: b.Book.ReadOnly}
		if st := a.states[key]; st != nil || a.run == nil || a.run.id != r.ID {
			bv.Contacts, bv.Remote, bv.Pending = syncCounts(list, key, c.bookState(key))
		}
		v.Books = append(v.Books, bv)
		v.Contacts, v.Remote, v.Pending = v.Contacts+bv.Contacts, v.Remote+bv.Remote, v.Pending+bv.Pending
	}
	running := a.run != nil && a.run.id == r.ID
	switch {
	case running:
		v.State, v.Phase, v.Done, v.Total = ui.AccountSyncing, a.run.phase, a.run.done, a.run.total
	case rt.waiting != "" && rt.pending:
		v.State, v.Waiting = ui.AccountWaiting, rt.waiting
	case r.Problem == problemAuth:
		v.State = ui.AccountAuth
	case r.Problem != "":
		v.State = ui.AccountFailed
	default:
		v.State = ui.AccountIdle
	}
	if r.Problem == problemAuth && r.Device != "" {
		v.Error = deviceAuthText()
	} else if r.Problem != "" {
		v.Error = accountProblemText(r.Problem, r.Detail)
	} else if r.Failed > 0 {
		v.Error = cardReasonText(r.Failed, r.Reason)
	}
	v.Calendar = c.calendarView(r)
	v.LockStays, v.LockTaken = c.lockStays(r), r.LockTaken
	if rt.pending && r.syncs() {
		next := rt.dueAt
		if r.NotBefore.After(next) {
			next = r.NotBefore
		}
		v.NextSync = next.Unix()
	}
	return v
}

// syncCounts: the phone's contacts in the book, how many came from the
// server, and the phone's changes not on the server yet.
func syncCounts(list []ui.Contact, key string, st *carddav.State) (linked, remote, pending int) {
	local := map[string]ui.Contact{}
	for _, x := range list {
		if x.Book == key {
			local[x.ID] = x
		}
	}
	seen := map[string]bool{}
	for _, it := range st.Linked() {
		x, ok := local[it.ID]
		seen[it.ID] = true
		switch {
		case !ok || it.New || !carddav.Same(x, it.Base):
			pending++
		}
		if ok {
			linked++
			if it.Remote {
				remote++
			}
		}
	}
	for id := range local {
		if !seen[id] {
			pending++
		}
	}
	return linked, remote, pending
}

func accountProblemText(problem, detail string) string {
	switch problem {
	case problemAuth:
		return tr.T("The server did not accept the user name and password. iCloud and the Kario account want an app-specific password, not the account's own")
	case problemNetwork:
		return tr.T("Cannot reach the server")
	case problemServer:
		return tr.T("The server answered with an error (%s)", detail)
	case problemFull:
		return tr.T("The address book on the server is full")
	case problemGone:
		return tr.T("The server no longer has this address book")
	case problemKeystore:
		return tr.T("The system cannot open the saved password: sign out and sign in again")
	case problemStore:
		return tr.T("The contacts on the phone could not be written")
	}
	return tr.T("The sync failed: %s", detail)
}

func cardReasonText(n int, reason string) string {
	switch reason {
	case "too_large":
		return tr.T("%d contacts are too large for the server", n)
	case "refused":
		return tr.T("The server refused %d contacts", n)
	}
	return tr.T("%d contacts could not be written to the server", n)
}

func (c *Core) accountState() ui.AccountState {
	a := &c.accounts
	if !a.loaded {
		c.loadAccounts()
	}
	st := ui.AccountState{Accounts: []ui.Account{}, KarioServer: c.cfg.AccountURL, Default: a.file.Default}
	for _, r := range a.file.Accounts {
		st.Accounts = append(st.Accounts, c.accountView(r))
	}
	if l := a.file.LockLeft; l != nil {
		st.LockLeft = &ui.AccountLockLeft{User: l.User, Device: l.Device}
	}
	return st
}

// accountLabel names an account for the contacts' pages: the Kario
// account, or its server's host.
func (c *Core) accountLabel(r *accountRecord) string {
	if r.Kind == ui.AccountKario {
		return tr.T("Kario account")
	}
	return hostOf(r.Server, "https://"+r.Server, firstBookURL(r))
}

// contactBooks are the books on (contacts.books): those contacts may go
// to, and the read-only ones, marked. A system app also gets each
// account's user name.
func (c *Core) contactBooks(system bool) []ui.ContactBook {
	a := &c.accounts
	out := []ui.ContactBook{}
	def := c.accountDefault()
	for _, r := range a.file.Accounts {
		label := c.accountLabel(r)
		if system {
			label += " · " + r.User
		}
		for _, b := range r.Books {
			if key := bookKey(r.ID, b.ID); b.On {
				out = append(out, ui.ContactBook{ID: key, Name: firstNonEmpty(b.Book.Name, tr.T("Contacts")), Account: label, Default: key == def,
					ReadOnly: b.Book.ReadOnly})
			}
		}
	}
	return out
}

// dispatchAccount serves account.*.
func (c *Core) dispatchAccount(m ui.Msg, reply func(any, error)) {
	var args ui.AccountArgs
	_ = json.Unmarshal(m.Args, &args)
	a := &c.accounts
	if !a.loaded {
		c.loadAccounts()
	}
	switch m.Cap {
	case ui.CapAccountAdd:
		c.addAccount(args, reply)
		return
	case ui.CapAccountState:
		if args.Check {
			c.checkLeftBinding(true, func(err error) {
				if err != nil {
					reply(nil, errors.New(tr.T("Cannot reach the Kario account server")))
					return
				}
				reply(c.accountState(), nil)
			})
			return
		}
		c.checkLeftBinding(false, nil)
		c.lookOffline(false, func() { reply(c.accountState(), nil) })
		return
	case ui.CapAccountSet:
		if args.Default != nil {
			reply(c.accountState(), c.setDefault(*args.Default))
			return
		}
	}
	r := a.find(args.ID)
	if r == nil {
		reply(nil, errors.New(tr.T("No such account")))
		return
	}
	rt := a.rtOf(r.ID)
	switch m.Cap {
	case ui.CapAccountSync:
		rt.pending, rt.forced, rt.dueAt, rt.netAt = true, true, c.now(), time.Time{}
		c.accountsTick()
		reply(c.accountState(), nil)
	case ui.CapAccountSet:
		if args.Calendar != nil {
			c.setCalendar(r, *args.Calendar, reply)
			return
		}
		if args.RetryLock {
			id := r.ID
			c.retryLock(r, func() { reply(c.accountState(), c.lockRetried(id)) })
			return
		}
		var err error
		switch {
		case args.Book != "" && args.On != nil:
			err = c.setBook(r, args.Book, *args.On, args.RemoveContacts)
		case args.MobileData != nil:
			r.MobileData = *args.MobileData
			rt.netAt = time.Time{}
			err = c.saveAccounts()
		}
		reply(c.accountState(), err)
	case ui.CapAccountRemove:
		c.theftSignOut(r, func(unbind func(context.Context) error, err error) {
			if err != nil {
				reply(nil, err)
				return
			}
			c.removeAccount(r, args.RemoveContacts, unbind, reply)
		})
	}
}

// setDefault makes a book on (or "" the phone) the one new contacts go to.
func (c *Core) setDefault(key string) error {
	if key != "" {
		if err := c.bookTakes(key); err != nil {
			return err
		}
	}
	old := c.accounts.file.Default
	c.setDefaultBook(key)
	if err := c.saveAccounts(); err != nil {
		c.setDefaultBook(old)
		return err
	}
	c.log.Printf("accounts: new contacts go to %s", firstNonEmpty(key, "the phone"))
	return nil
}

// setBook switches a book on (its sync starts) or off: off, its contacts
// stay on the phone as they are and sync again once it is on; with
// removeContacts those that came from the server go and the others stay
// on the phone alone, and the book starts afresh when it is on again.
func (c *Core) setBook(r *accountRecord, key string, on, removeContacts bool) error {
	a := &c.accounts
	b := r.book(key)
	if b == nil {
		return errors.New(tr.T("No such address book"))
	}
	if a.run != nil && a.run.id == r.ID {
		a.run.cancel()
		a.run = nil
		rt := a.rtOf(r.ID)
		a.saveMu.Lock()
		a.gen++
		rt.gen = a.gen
		a.saveMu.Unlock()
	}
	b.On = on
	removed := 0
	if !on && removeContacts {
		n, err := c.dropBook(key)
		if err != nil {
			b.On = true
			return contactsErr(err)
		}
		removed = n
	}
	if !on && a.file.Default == key {
		c.setDefaultBook(c.firstBookOn())
	}
	if err := c.saveAccounts(); err != nil {
		return err
	}
	rt := a.rtOf(r.ID)
	rt.pending, rt.dueAt = true, c.now()
	c.setSystemJob(accountJob, accountEvery)
	c.log.Printf("accounts: %s turned %s, %d contacts taken off the phone", key, map[bool]string{true: "on", false: "off"}[on], removed)
	c.accountsTick()
	return nil
}

// firstBookOn is the first book on that contacts may go to (not a
// read-only one) of any account, "" for none.
func (c *Core) firstBookOn() string {
	for _, r := range c.accounts.file.Accounts {
		for _, b := range r.Books {
			if b.On && !b.Book.ReadOnly {
				return bookKey(r.ID, b.ID)
			}
		}
	}
	return ""
}

// dropBook forgets a book's sync: the contacts that came from its server
// go, the others stay on the phone alone; the state file goes. It says
// how many went.
func (c *Core) dropBook(key string) (int, error) {
	a := &c.accounts
	st := a.states[key]
	if st == nil {
		st = c.loadSyncState(key)
	}
	var ids []string
	for _, it := range st.Linked() {
		if it.Remote {
			ids = append(ids, it.ID)
		}
	}
	book := c.contactBook()
	n, err := book.DeleteIDs(ids)
	if err != nil {
		return 0, err
	}
	if _, err := book.MoveBook(key, ""); err != nil {
		return n, err
	}
	a.saveMu.Lock()
	delete(a.states, key)
	err = os.Remove(c.syncStatePath(key))
	a.saveMu.Unlock()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		c.log.Printf("accounts: %v", err)
	}
	c.notifyContacts()
	return n, nil
}

// addAccount signs in: the password sealed, the address books found with
// it; only then is the account kept, and its first sync starts. Its first
// book is on (the first the user may write to; a read-only one when there
// is no other), the others off; the first account's book on becomes the
// default, and the contacts on the phone alone join it, unless it is
// read-only.
func (c *Core) addAccount(args ui.AccountArgs, reply func(any, error)) {
	a := &c.accounts
	kind := args.Kind
	if kind != ui.AccountKario {
		kind = ui.AccountCardDAV
	}
	server := c.accountServer(kind, args.Server)
	user := strings.TrimSpace(args.User)
	code := strings.TrimSpace(args.Code)
	withCode := args.SendCode || code != ""
	switch {
	case a.addBusy:
		reply(nil, errors.New(tr.T("Signing in already")))
		return
	case kind == ui.AccountKario && server == "":
		reply(nil, errors.New(tr.T("This phone has no Kario account server")))
		return
	case withCode:
		if why := codeArgsText(kind, server, user); why != "" {
			reply(nil, errors.New(why))
			return
		}
	case user == "" || args.Password == "":
		reply(nil, errors.New(tr.T("Enter the user name and the password")))
		return
	case server == "" && !strings.Contains(user, "@"):
		reply(nil, errors.New(tr.T("Enter the server's address, or an e-mail address as the user name")))
		return
	}
	if c.signedIn(kind, args.Server, user) != nil {
		reply(nil, errors.New(tr.T("“%s” is signed in already", user)))
		return
	}
	if args.SendCode {
		c.sendAccountCode(user, reply)
		return
	}
	a.addBusy = true
	a.file.Seq++
	seq := a.file.Seq
	id := "a" + strconv.Itoa(seq)
	pass, device, minted := args.Password, c.accountDeviceName(), false
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var err error
		if code != "" {
			var p, name, username string
			if p, name, username, err = c.signInWithCode(ctx, user, code, args.Agree, device); err == nil {
				pass, device, user, minted = p, name, username, true
			}
		}
		var sealed string
		var books []carddav.Book
		if err == nil {
			sealed, err = c.sealPassword(id, pass)
		}
		if err == nil {
			client := &carddav.Client{User: user, Password: pass, UserAgent: Brand + " CardDAV"}
			books, err = client.DiscoverAll(ctx, server)
		}
		c.Post(func() {
			a.addBusy = false
			if err != nil {
				// Its sealed password goes with it: the ID was never used.
				if a.file.Seq == seq {
					a.file.Seq--
				}
				p, detail := problemOf(err)
				if code != "" {
					detail = accountErrLog(err)
				}
				c.log.Printf("accounts: signing in to %s failed: %s %s", hostOf(server, "https://"+server), p, detail)
				if minted {
					c.revokeAfterFailure(user, pass)
				}
				reply(nil, errors.New(signInText(err, p, detail)))
				return
			}
			if c.signedIn(kind, args.Server, user) != nil {
				if a.file.Seq == seq {
					a.file.Seq--
				}
				if minted {
					c.revokeAfterFailure(user, pass)
				}
				reply(nil, errors.New(tr.T("“%s” is signed in already", user)))
				return
			}
			r := &accountRecord{ID: id, Kind: kind, Server: args.Server, User: user, Sealed: sealed, MobileData: true, Added: c.now().Unix()}
			if kind == ui.AccountKario {
				r.Server = ""
			}
			if minted {
				r.Device = device
			}
			for i, b := range books {
				r.Books = append(r.Books, &bookRecord{ID: "b" + strconv.Itoa(i+1), Book: b, On: i == 0})
			}
			on := r.Books[0]
			first := c.firstBookOn() == "" && !on.Book.ReadOnly
			a.file.Accounts = append(a.file.Accounts, r)
			key := bookKey(id, on.ID)
			a.states[key] = &carddav.State{}
			old := a.file.Default
			if first {
				c.setDefaultBook(key)
			}
			if err := c.saveAccounts(); err != nil {
				a.file.Accounts = a.file.Accounts[:len(a.file.Accounts)-1]
				c.setDefaultBook(old)
				if minted {
					c.revokeAfterFailure(user, pass)
				}
				reply(nil, err)
				return
			}
			if first {
				if n, err := c.contactBook().MoveBook("", key); err != nil {
					c.log.Printf("accounts: %v", err)
				} else if n > 0 {
					c.notifyContacts()
				}
			}
			c.setSystemJob(accountJob, accountEvery)
			c.log.Printf("accounts: %s signed in to %s (%s, %d books, sync-collection %v, vCard %s)", id, hostOf(books[0].URL), kind, len(books), books[0].Sync, books[0].Version)
			rt := a.rtOf(id)
			rt.pending, rt.forced, rt.dueAt, rt.netAt, rt.waiting = true, true, c.now(), time.Time{}, ""
			c.watchNetEvents()
			c.accountsTick()
			reply(c.accountState(), nil)
		})
	}()
}

// signedIn is the account of that server and user, if there is one.
func (c *Core) signedIn(kind, server, user string) *accountRecord {
	for _, r := range c.accounts.file.Accounts {
		if r.Kind == kind && strings.EqualFold(r.User, user) && (kind == ui.AccountKario || hostOf(r.Server, "https://"+r.Server) == hostOf(server, "https://"+server)) {
			return r
		}
	}
	return nil
}

func signInText(err error, problem, detail string) string {
	if t := accountCodeText(err); t != "" {
		return t
	}
	switch {
	case errors.Is(err, carddav.ErrNoBook):
		return tr.T("The server has no address book for this account")
	case errors.Is(err, carddav.ErrNoServer):
		return tr.T("Enter the server's address, or an e-mail address as the user name")
	case errors.Is(err, carddav.ErrInsecure):
		return tr.T("The server is not on HTTPS: the password would go unencrypted")
	}
	return accountProblemText(problem, detail)
}

// removeAccount signs out of one account: it, its sealed password and its
// books' sync states go; with removeContacts so do the phone's contacts
// that came from it (those made on the phone stay, on the phone alone, as
// do all its contacts without). Nothing is deleted on the server; unbind
// (theftSignOut's, nil for none) tells the server the anti-theft lock is
// off, and a password the server made for the phone (an account signed in
// with a code) is revoked after that (signOutServer).
func (c *Core) removeAccount(r *accountRecord, removeContacts bool, unbind func(context.Context) error, reply func(any, error)) {
	a := &c.accounts
	if a.run != nil && a.run.id == r.ID {
		a.run.cancel()
		a.run = nil
	}
	failed := func(err error) {
		c.theftUnbindLater(unbind)
		reply(nil, contactsErr(err))
	}
	removed := 0
	for _, b := range r.Books {
		key := bookKey(r.ID, b.ID)
		if removeContacts {
			n, err := c.dropBook(key)
			if err != nil {
				failed(err)
				return
			}
			removed += n
			continue
		}
		if _, err := c.contactBook().MoveBook(key, ""); err != nil {
			failed(err)
			return
		}
		a.saveMu.Lock()
		delete(a.states, key)
		err := os.Remove(c.syncStatePath(key))
		a.saveMu.Unlock()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			c.log.Printf("accounts: %v", err)
		}
	}
	c.signOutCalendar(r, removeContacts)
	a.saveMu.Lock()
	for i, x := range a.file.Accounts {
		if x == r {
			a.file.Accounts = append(a.file.Accounts[:i], a.file.Accounts[i+1:]...)
			break
		}
	}
	delete(a.rt, r.ID)
	a.saveMu.Unlock()
	if _, b := a.bookOf(a.file.Default); b == nil || !b.On {
		c.setDefaultBook(c.firstBookOn())
	}
	if len(a.file.Accounts) == 0 {
		c.setSystemJob(accountJob, 0)
	}
	c.notifyContacts()
	c.log.Printf("accounts: %s signed out of %s, %d contacts taken off the phone", r.ID, hostOf(firstBookURL(r)), removed)
	saved := c.saveAccounts()
	if r.Device != "" || unbind != nil {
		c.signOutServer(*r, unbind, saved, reply)
		return
	}
	reply(c.accountState(), saved)
}
