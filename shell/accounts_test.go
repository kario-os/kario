package shell

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/carddav/davtest"
	"github.com/kario-os/kario/contacts"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

const (
	acctUser = "alice@kario.test"
	acctPass = "wxyz-abcd-efgh-ijkl"
)

type accountTest struct {
	*hotspotTest
	srv *davtest.Server
	ts  *httptest.Server
	ks  *keystore.Store
}

// newAccountTest is the Shell with Settings, phone-keystored's secrets,
// the demo contacts (a developer machine) and a CardDAV server as the
// product's Kario account server, holding one card.
func newAccountTest(t *testing.T) *accountTest {
	at := &accountTest{hotspotTest: newHotspotTest(t, newSimWifi()), srv: davtest.New(acctUser, acctPass)}
	c := at.c
	c.cfg.RunDir = t.TempDir()
	c.broker.clients = map[string]*wire.Client{}
	ks, err := keystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	at.ks = ks
	go wire.Serve(filepath.Join(c.cfg.RunDir, svc.KeystoredSock), func(method string, params json.RawMessage) (any, error) {
		var p struct{ Purpose, Data, Sealed string }
		_ = json.Unmarshal(params, &p)
		switch method {
		case keystore.MethodSecretSeal:
			raw, _ := base64.StdEncoding.DecodeString(p.Data)
			s, err := ks.SealSecret(p.Purpose, raw)
			return map[string]string{"sealed": s}, err
		case keystore.MethodSecretOpen:
			raw, err := ks.OpenSecret(p.Purpose, p.Sealed)
			return map[string]string{"data": base64.StdEncoding.EncodeToString(raw)}, err
		}
		return nil, errors.New("unknown method " + method)
	})
	at.srv.Put("lei.vcf", []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:lei\r\nFN:\u674e\u96f7\r\nTEL;TYPE=CELL:+86 139 1234 0000\r\n"+
		"EMAIL:lei@example.com\r\nPHOTO;ENCODING=b;TYPE=JPEG:/9j/4AAQSkZJRg\r\nEND:VCARD\r\n"))
	at.ts = httptest.NewServer(at.srv)
	t.Cleanup(at.ts.Close)
	c.cfg.AccountURL = at.ts.URL
	return at
}

func (at *accountTest) call(cap string, args any) (ui.AccountState, error) {
	at.t.Helper()
	at.req(at.app, cap, args)
	r := at.reply(at.app)
	var st ui.AccountState
	if !r.OK {
		return st, errors.New(r.Err)
	}
	if err := json.Unmarshal(r.Data, &st); err != nil {
		at.t.Fatal(err)
	}
	return st, nil
}

func (at *accountTest) view() ui.Account {
	at.t.Helper()
	st, err := at.call(ui.CapAccountState, nil)
	if err != nil || len(st.Accounts) == 0 {
		at.t.Fatalf("%+v %v", st, err)
	}
	return st.Accounts[0]
}

// record is the first account's.
func (at *accountTest) record() *accountRecord { return at.c.accounts.file.Accounts[0] }

func (at *accountTest) signIn() ui.Account {
	at.t.Helper()
	st, err := at.call(ui.CapAccountAdd, ui.AccountArgs{Kind: ui.AccountKario, User: acctUser, Password: acctPass})
	if err != nil || len(st.Accounts) != 1 {
		at.t.Fatalf("sign in: %+v %v", st, err)
	}
	at.synced()
	return st.Accounts[0]
}

// synced ticks, as the Shell's loop does, until no run is under way and
// none starts.
func (at *accountTest) synced() {
	at.t.Helper()
	at.pump("the sync", func() bool {
		at.c.accountsTick()
		for _, rt := range at.c.accounts.rt {
			if rt.probing {
				return false
			}
		}
		return at.c.accounts.run == nil
	})
}

// later moves the Shell's clock on.
func (at *accountTest) later(d time.Duration) { at.c.clockSkew += d }

func (at *accountTest) contact(name string) (ui.Contact, bool) {
	for _, x := range at.c.contacts() {
		if x.Name == name {
			return x, true
		}
	}
	return ui.Contact{}, false
}

// Signing in to the Kario account syncs both ways; a change on the phone
// goes out by itself after a while; signing out may take the account's
// contacts off the phone, never the phone's own. The password is kept
// sealed, and neither it nor the user name is ever in the log.
func TestAccountSignInSyncSignOut(t *testing.T) {
	at := newAccountTest(t)
	c := at.c
	demo := len(c.contacts())
	acct := at.signIn()
	if acct.Kind != ui.AccountKario || acct.Server != at.ts.URL || acct.User != acctUser {
		t.Errorf("%+v", acct)
	}
	if _, ok := at.contact("\u674e\u96f7"); !ok || len(c.contacts()) != demo+1 {
		t.Fatalf("the server's contact is not on the phone: %d contacts", len(c.contacts()))
	}
	if len(at.srv.Cards()) != demo+1 {
		t.Fatalf("the phone's contacts are not on the server: %d cards", len(at.srv.Cards()))
	}
	v := at.view()
	if v.State != ui.AccountIdle || v.Contacts != demo+1 || v.Remote != 1 || v.Pending != 0 || v.LastSync == 0 || len(v.Books) != 2 ||
		v.Books[0] != (ui.AccountBook{ID: acct.ID + "/b1", Name: "Contacts", On: true, Contacts: demo + 1, Remote: 1}) ||
		v.Books[1] != (ui.AccountBook{ID: acct.ID + "/b2", Name: "System", ReadOnly: true}) || v.NextSync != 0 {
		t.Errorf("%+v", v)
	}
	for _, x := range c.contacts() {
		if x.Book != acct.ID+"/b1" {
			t.Errorf("%s is in %q", x.Name, x.Book)
		}
	}
	var file struct{ Accounts []map[string]any }
	raw, _ := os.ReadFile(c.accountsPath())
	if json.Unmarshal(raw, &file) != nil || len(file.Accounts) != 1 || !strings.HasPrefix(file.Accounts[0]["password_sealed"].(string), "s1.") ||
		strings.Contains(string(raw), acctPass) {
		t.Fatalf("accounts.json: %s", raw)
	}
	if st, err := os.Stat(c.accountsPath()); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("accounts.json: %v %v", st, err)
	}
	if runs, _ := c.JobRuns(systemJob, accountJob); runs != 0 || len(c.jobs) != 1 || c.jobs[0].every() != accountEvery {
		t.Errorf("the system job: %+v", c.jobs)
	}

	lei, _ := at.contact("\u674e\u96f7")
	lei.Notes = "met at the station"
	if _, err := c.contactBook().Save(lei); err != nil {
		t.Fatal(err)
	}
	c.contactsChanged()
	if v := at.view(); v.Pending != 1 || v.NextSync == 0 {
		t.Errorf("a change waiting: %+v", v)
	}
	puts := at.srv.Count("PUT")
	c.accountsTick()
	if c.accounts.run != nil {
		t.Fatal("synced at once after a change")
	}
	at.later(accountAfterEdit)
	at.synced()
	card := string(at.srv.Card("lei.vcf"))
	if at.srv.Count("PUT") != puts+1 || !strings.Contains(card, "NOTE:met at the station") || !strings.Contains(card, "PHOTO;ENCODING=b;TYPE=JPEG:/9j/4AAQSkZJRg") ||
		!strings.Contains(card, "EMAIL:lei@example.com") {
		t.Errorf("%d PUTs\n%s", at.srv.Count("PUT")-puts, card)
	}

	at.srv.Put("lei.vcf", []byte(strings.Replace(card, "+86 139 1234 0000", "+86 139 1234 9999", 1)))
	if _, err := at.call(ui.CapAccountSync, ui.AccountArgs{ID: acct.ID}); err != nil {
		t.Fatal(err)
	}
	at.synced()
	if lei, _ := at.contact("\u674e\u96f7"); lei.Number != "+86 139 1234 9999" {
		t.Errorf("Sync now: %+v", lei)
	}

	st, err := at.call(ui.CapAccountRemove, ui.AccountArgs{ID: acct.ID, RemoveContacts: true})
	if err != nil || len(st.Accounts) != 0 || st.KarioServer != at.ts.URL {
		t.Fatalf("%+v %v", st, err)
	}
	if _, ok := at.contact("\u674e\u96f7"); ok || len(c.contacts()) != demo {
		t.Errorf("after signing out: %d contacts", len(c.contacts()))
	}
	for _, x := range c.contacts() {
		if x.Book != "" {
			t.Errorf("%s is still in %q", x.Name, x.Book)
		}
	}
	if _, err := os.Stat(c.syncStatePath(acct.ID + "/b1")); !os.IsNotExist(err) || len(c.jobs) != 0 {
		t.Errorf("left behind: %v, %d jobs", err, len(c.jobs))
	}
	if len(at.srv.Cards()) != demo+1 {
		t.Error("signing out deleted on the server")
	}
	for _, secret := range []string{acctPass, acctUser, "alice"} {
		if strings.Contains(at.log.String(), secret) {
			t.Errorf("the log tells %q:\n%s", secret, at.log.String())
		}
	}
	if !strings.Contains(at.log.String(), "signed out") {
		t.Errorf("%s", at.log.String())
	}
}

func TestAccountSignInRefused(t *testing.T) {
	at := newAccountTest(t)
	_, err := at.call(ui.CapAccountAdd, ui.AccountArgs{Kind: ui.AccountKario, User: acctUser, Password: "wrong"})
	if err == nil || err.Error() != accountProblemText(problemAuth, "") {
		t.Fatalf("%v", err)
	}
	if st, _ := at.call(ui.CapAccountState, nil); len(st.Accounts) != 0 {
		t.Errorf("%+v", st)
	}
	if _, err := os.Stat(at.c.accountsPath()); !os.IsNotExist(err) || at.c.accounts.file.Seq != 0 {
		t.Errorf("a refused sign-in left a record, or used an ID: %d", at.c.accounts.file.Seq)
	}
	if _, err := at.call(ui.CapAccountAdd, ui.AccountArgs{Kind: ui.AccountCardDAV, User: "alice", Password: "x"}); err == nil {
		t.Error("no server and no e-mail address")
	}
	at.c.cfg.AccountURL = ""
	if _, err := at.call(ui.CapAccountAdd, ui.AccountArgs{Kind: ui.AccountKario, User: acctUser, Password: acctPass}); err == nil {
		t.Error("a Kario account on a product without one")
	}
	if _, err := at.call(ui.CapAccountAdd, ui.AccountArgs{Kind: ui.AccountCardDAV, Server: at.ts.URL, User: acctUser, Password: acctPass}); err != nil {
		t.Fatal(err)
	}
	at.synced()
	if _, err := at.call(ui.CapAccountAdd, ui.AccountArgs{Kind: ui.AccountCardDAV, Server: at.ts.URL, User: acctUser, Password: acctPass}); err == nil {
		t.Error("the same account twice")
	}
}

// A server down: tried again after a backoff that doubles, at least what
// Retry-After says; Sync now does not wait for it.
func TestAccountBackoff(t *testing.T) {
	at := newAccountTest(t)
	acct := at.signIn()
	at.srv.Lock()
	at.srv.Down, at.srv.RetryAfter = true, 90
	at.srv.Unlock()
	at.c.accountsDue()
	at.synced()
	r := at.record()
	if r.Problem != problemServer || r.Failures != 1 || r.NotBefore.Sub(at.c.now()) < 89*time.Second {
		t.Fatalf("%+v", r)
	}
	if v := at.view(); v.State != ui.AccountFailed || !strings.Contains(v.Error, "503") {
		t.Errorf("%+v", v)
	}
	down := at.srv.Count("down")
	at.c.accountsDue()
	at.c.accountsTick()
	if at.c.accounts.run != nil || at.srv.Count("down") != down {
		t.Fatal("tried again before the backoff")
	}
	at.srv.Lock()
	at.srv.RetryAfter = 0
	at.srv.Unlock()
	at.later(91 * time.Second)
	at.synced()
	if wait := r.NotBefore.Sub(at.c.now()); r.Failures != 2 || wait > 2*accountRetryMin || wait < 2*accountRetryMin-time.Second {
		t.Errorf("%+v", r)
	}
	at.srv.Lock()
	at.srv.Down = false
	at.srv.Unlock()
	if _, err := at.call(ui.CapAccountSync, ui.AccountArgs{ID: acct.ID}); err != nil {
		t.Fatal(err)
	}
	at.synced()
	if r.Problem != "" || r.Failures != 0 || at.view().State != ui.AccountIdle {
		t.Errorf("%+v", r)
	}
}

// A password the server stops taking (revoked) stops the syncs until the
// user asks.
func TestAccountAuthFailure(t *testing.T) {
	at := newAccountTest(t)
	acct := at.signIn()
	at.srv.Lock()
	at.srv.Pass = "revoked"
	at.srv.Unlock()
	at.c.accountsDue()
	at.synced()
	if v := at.view(); v.State != ui.AccountAuth || v.Error != accountProblemText(problemAuth, "") {
		t.Fatalf("%+v", v)
	}
	refused := at.srv.Count("unauthorized")
	at.later(accountEvery)
	at.c.accountsDue()
	at.synced()
	if at.srv.Count("unauthorized") != refused {
		t.Error("the job tried a refused password again")
	}
	at.call(ui.CapAccountSync, ui.AccountArgs{ID: acct.ID})
	at.synced()
	if at.srv.Count("unauthorized") == refused {
		t.Error("Sync now did not try")
	}
}

// Without a network, or without Wi-Fi for an account kept off mobile
// data, a sync waits and goes once there is one.
func TestAccountWaitsForNetwork(t *testing.T) {
	at := newAccountTest(t)
	acct := at.signIn()
	online, metered := false, false
	at.c.accounts.net = func(then func(bool, bool)) { then(online, metered) }
	asked := at.srv.Count("REPORT sync")
	at.call(ui.CapAccountSync, ui.AccountArgs{ID: acct.ID})
	at.synced()
	if v := at.view(); v.State != ui.AccountWaiting || v.Waiting != ui.AccountWaitNetwork || at.srv.Count("REPORT sync") != asked {
		t.Fatalf("%+v", v)
	}
	online, metered = true, true
	at.call(ui.CapAccountSet, ui.AccountArgs{ID: acct.ID, MobileData: on(false)})
	at.synced()
	if v := at.view(); v.State != ui.AccountWaiting || v.Waiting != ui.AccountWaitWifi || v.MobileData {
		t.Fatalf("%+v", v)
	}
	// On Wi-Fi now: the network's event starts it at once, not the next
	// look.
	metered = false
	at.later(time.Second)
	at.c.accountsTick()
	if at.srv.Count("REPORT sync") != asked {
		t.Fatal("looked before the event or the next look")
	}
	at.c.accountsNetChanged()
	at.synced()
	if v := at.view(); v.State != ui.AccountIdle || at.srv.Count("REPORT sync") != asked+1 {
		t.Errorf("%+v", v)
	}
}

// A run that failed for want of a network backs off; the network coming
// back (its event) tries again at once, past the backoff, but not more
// often than accountNetMin.
func TestAccountNetworkEventRetries(t *testing.T) {
	at := newAccountTest(t)
	at.signIn()
	r := at.record()
	at.ts.Close()
	at.c.accountsDue()
	at.synced()
	if r.Problem != problemNetwork || r.Failures != 1 || !r.NotBefore.After(at.c.now()) {
		t.Fatalf("%+v", r)
	}
	tries := r.Failures
	at.c.accountsNetChanged()
	at.synced()
	if r.Failures != tries {
		t.Fatalf("an event right after the try tried again: %+v", r)
	}
	at.later(accountNetMin)
	at.c.accountsNetChanged()
	at.synced()
	if r.Failures != tries+1 || r.NotBefore.Sub(at.c.now()) < 2*accountRetryMin-time.Second {
		t.Fatalf("the event did not try past the backoff, or the backoff did not grow: %+v", r)
	}
}

// The job wakes the sync: it is one of the Shell's own, run in its wake.
func TestAccountJob(t *testing.T) {
	at := newAccountTest(t)
	at.signIn()
	asked := at.srv.Count("REPORT sync")
	at.later(accountEvery + maxJobFlex)
	at.c.runJobs()
	if runs, _ := at.c.JobRuns(systemJob, accountJob); runs != 1 || !at.c.accounts.rt[at.record().ID].pending {
		t.Fatalf("%d runs, not pending", runs)
	}
	at.synced()
	if at.srv.Count("REPORT sync") != asked+1 {
		t.Error("the job did not sync")
	}
}

// Two accounts, one with two books: each book can be switched on and off,
// new contacts go to the default book, and signing out of one account
// takes off the phone only its contacts, if asked.
func TestAccountsAndBooks(t *testing.T) {
	at := newAccountTest(t)
	c := at.c
	kario := at.signIn()
	other := davtest.New("bob@example.test", "pw")
	other.AddBook("work", "Work")
	other.Put("ann.vcf", []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:ann\r\nFN:Ann Other\r\nTEL:2001\r\nEND:VCARD\r\n"))
	other.PutIn("work", "boss.vcf", []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:boss\r\nFN:Boss\r\nTEL:2002\r\nEND:VCARD\r\n"))
	ts := httptest.NewServer(other)
	t.Cleanup(ts.Close)
	st, err := at.call(ui.CapAccountAdd, ui.AccountArgs{Kind: ui.AccountCardDAV, Server: ts.URL, User: "bob@example.test", Password: "pw"})
	if err != nil || len(st.Accounts) != 2 {
		t.Fatalf("%+v %v", st, err)
	}
	at.synced()
	st, _ = at.call(ui.CapAccountState, nil)
	acct := st.Accounts[1]
	if len(acct.Books) != 3 || !acct.Books[0].On || acct.Books[1].On || acct.Books[1].Name != "Work" || !acct.Books[2].ReadOnly || st.Default != kario.ID+"/b1" {
		t.Fatalf("%+v default %q", acct.Books, st.Default)
	}
	if x, ok := at.contact("Ann Other"); !ok || x.Book != acct.ID+"/b1" {
		t.Fatalf("%+v", x)
	}
	if _, ok := at.contact("Boss"); ok {
		t.Fatal("a book that is off synced")
	}
	if len(other.Cards()) != 1 {
		t.Errorf("the first account's contacts went to the second: %d cards", len(other.Cards()))
	}

	work := acct.Books[1].ID
	if _, err := at.call(ui.CapAccountSet, ui.AccountArgs{Default: &work}); err == nil {
		t.Error("a book that is off as the default")
	}
	if _, err := at.call(ui.CapAccountSet, ui.AccountArgs{ID: acct.ID, Book: work, On: on(true)}); err != nil {
		t.Fatal(err)
	}
	at.synced()
	if x, ok := at.contact("Boss"); !ok || x.Book != work {
		t.Fatalf("%+v", x)
	}
	if _, err := at.call(ui.CapAccountSet, ui.AccountArgs{Default: &work}); err != nil {
		t.Fatal(err)
	}
	mgr := &App{M: &Manifest{ID: appContacts, Name: "Contacts", Permissions: []string{ui.PermContacts}}, running: true}
	var r answer
	c.broker.dispatchContacts(mgr, req(ui.CapContactsSave, ui.Contact{Name: "New Colleague", Number: "2003"}), r.reply)
	c.broker.dispatchContacts(mgr, req(ui.CapContactsSave, ui.Contact{Name: "Kept here", Number: "2004", Book: ui.BookPhone}), r.reply)
	c.broker.dispatchContacts(mgr, req(ui.CapContactsBooks, nil), r.reply)
	books, _ := r.data.([]ui.ContactBook)
	if len(books) != 3 || !books[2].Default || books[2].Name != "Work" || !strings.Contains(books[2].Account, "127.0.0.1") {
		t.Errorf("%+v", books)
	}
	at.later(accountAfterEdit)
	at.synced()
	if _, ok := other.FindIn("work", "FN:New Colleague"); !ok {
		t.Error("the new contact is not in the default book")
	}
	if _, ok := at.srv.Find("Kept here"); ok {
		t.Error("a contact kept on the phone went up")
	}

	if _, err := at.call(ui.CapAccountSet, ui.AccountArgs{ID: acct.ID, Book: work, On: on(false), RemoveContacts: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := at.contact("Boss"); ok {
		t.Error("the book's contacts stayed")
	}
	if x, ok := at.contact("New Colleague"); !ok || x.Book != "" {
		t.Errorf("one made on the phone: %+v", x)
	}
	if st, _ := at.call(ui.CapAccountState, nil); st.Default != kario.ID+"/b1" {
		t.Errorf("the default is %q", st.Default)
	}

	if _, err := at.call(ui.CapAccountRemove, ui.AccountArgs{ID: acct.ID, RemoveContacts: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := at.contact("Ann Other"); ok {
		t.Error("the account's contact stayed")
	}
	if x, ok := at.contact("\u674e\u96f7"); !ok || x.Book != kario.ID+"/b1" {
		t.Errorf("the other account's contact: %+v", x)
	}
	if st, _ := at.call(ui.CapAccountState, nil); len(st.Accounts) != 1 {
		t.Errorf("%+v", st)
	}
}

// An account from before several books: its book is the first of its
// books and the default, its sync state goes with it, and every contact
// is in it (all were synced with it).
func TestAccountMigration(t *testing.T) {
	at := newAccountTest(t)
	c := at.c
	at.signIn()
	r := at.record()
	key := r.ID + "/b1"
	old := map[string]any{"seq": 1, "accounts": []map[string]any{{"id": r.ID, "kind": r.Kind, "user": r.User, "password_sealed": r.Sealed,
		"book": r.Books[0].Book, "mobile_data": true}}}
	raw, _ := json.Marshal(old)
	os.WriteFile(c.accountsPath(), raw, 0o600)
	os.Rename(c.syncStatePath(key), filepath.Join(c.cfg.StateDir, "shell", "carddav-"+r.ID+".json"))
	c.contactBook().MoveBook(key, "")
	c.accounts = accountsShell{}
	c.loadAccounts()
	got := at.record()
	if len(got.Books) != 1 || got.Book != nil || !got.Books[0].On || c.accounts.file.Default != key {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(c.syncStatePath(key)); err != nil {
		t.Fatal(err)
	}
	for _, x := range c.contacts() {
		if x.Book != key {
			t.Errorf("%s in %q", x.Name, x.Book)
		}
	}
	puts := at.srv.Count("PUT")
	at.synced()
	if at.srv.Count("PUT") != puts || len(at.srv.Cards()) != len(c.contacts()) {
		t.Errorf("after the move: %d PUTs, %d cards for %d contacts", at.srv.Count("PUT")-puts, len(at.srv.Cards()), len(c.contacts()))
	}
}

// A change to the groups is a change to the contacts: it syncs soon after;
// the Kario account's book without groups yet gets a group card.
func TestAccountGroupChangeSyncs(t *testing.T) {
	at := newAccountTest(t)
	at.signIn()
	mgr := &App{M: &Manifest{ID: appContacts, Name: "Contacts", Permissions: []string{ui.PermContacts}}, running: true}
	var r answer
	at.c.broker.dispatchContacts(mgr, req(ui.CapContactsGroup, ui.ContactGroupArgs{Op: ui.ContactGroupCreate, Name: "Family"}), r.reply)
	g, _ := r.data.(ui.ContactGroup)
	lei, _ := at.contact("\u674e\u96f7")
	rt := at.c.accounts.rt[at.record().ID]
	rt.pending = false
	at.c.broker.dispatchContacts(mgr, req(ui.CapContactsGroup, ui.ContactGroupArgs{Op: ui.ContactGroupAdd, ID: g.ID, Contacts: []string{lei.ID}}), r.reply)
	if r.err != nil || !rt.pending || !rt.dueAt.After(at.c.now()) {
		t.Fatalf("%+v pending %v at %v", r, rt.pending, rt.dueAt)
	}
	at.later(accountAfterEdit)
	at.synced()
	name, ok := at.srv.Find("FN:Family")
	if _, info, _ := contacts.ParseCard(at.srv.Card(name)); !ok || !info.Group || len(info.Members) != 1 || info.Members[0] != "lei" {
		t.Errorf("%v %+v", at.srv.Cards(), info)
	}
	if strings.Contains(string(at.srv.Card("lei.vcf")), "CATEGORIES") {
		t.Errorf("%s", at.srv.Card("lei.vcf"))
	}
}

// A read-only book syncs from the server only: its contacts are marked for
// the apps, and nothing changes, deletes, regroups or merges them, nor is
// one the default or a new contact's book. The Contacts app moves a
// contact to another book (another app cannot); the old book's card goes.
func TestReadOnlyBookAndMove(t *testing.T) {
	at := newAccountTest(t)
	c := at.c
	at.srv.PutIn(davtest.SystemBook, "boss.vcf", []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:boss\r\nFN:Boss\r\nTEL:2002\r\nBDAY:1970-05-06\r\nEND:VCARD\r\n"))
	acct := at.signIn()
	sys := acct.ID + "/b2"
	if _, err := at.call(ui.CapAccountSet, ui.AccountArgs{ID: acct.ID, Book: sys, On: on(true)}); err != nil {
		t.Fatal(err)
	}
	at.synced()
	mgr := &App{M: &Manifest{ID: appContacts, Name: "Contacts", Permissions: []string{ui.PermContacts}}, running: true}
	var r answer
	c.broker.dispatchContacts(mgr, req(ui.CapContacts, nil), r.reply)
	var boss ui.Contact
	for _, x := range r.data.([]ui.Contact) {
		if x.Name == "Boss" {
			boss = x
		}
	}
	if !boss.ReadOnly || boss.Book != sys || boss.Birthday != "1970-05-06" {
		t.Fatalf("%+v", boss)
	}
	changed := boss
	changed.Notes = "x"
	lei, _ := at.contact("\u674e\u96f7")
	c.broker.dispatchContacts(mgr, req(ui.CapContactsGroup, ui.ContactGroupArgs{Op: ui.ContactGroupCreate, Name: "Team"}), r.reply)
	g, _ := r.data.(ui.ContactGroup)
	for what, m := range map[string]ui.Msg{
		"save":   req(ui.CapContactsSave, changed),
		"delete": req(ui.CapContactsDelete, ui.ContactIDArgs{ID: boss.ID}),
		"group":  req(ui.CapContactsGroup, ui.ContactGroupArgs{Op: ui.ContactGroupAdd, ID: g.ID, Contacts: []string{boss.ID}}),
		"merge":  req(ui.CapContactsMerge, ui.ContactMergeArgs{IDs: []string{lei.ID, boss.ID}}),
		"move":   req(ui.CapContactsSave, ui.Contact{ID: boss.ID, Name: "Boss", Number: "2002", Book: ui.BookPhone}),
	} {
		r = answer{}
		c.broker.dispatchContacts(mgr, m, r.reply)
		if r.err == nil || r.err.Error() != readOnlyErr("Boss").Error() {
			t.Errorf("%s: %v", what, r.err)
		}
	}
	if x, _ := at.contact("Boss"); x.Notes != "" || x.Book != sys {
		t.Errorf("%+v", x)
	}
	if _, err := at.call(ui.CapAccountSet, ui.AccountArgs{Default: &sys}); err == nil {
		t.Error("a read-only book as the default")
	}
	r = answer{}
	c.broker.dispatchContacts(mgr, req(ui.CapContactsSave, ui.Contact{Name: "New", Number: "2005", Book: sys}), r.reply)
	if x, ok := at.contact("New"); !ok || x.Book != acct.ID+"/b1" {
		t.Errorf("a new contact in a read-only book: %+v %v", x, r.err)
	}
	c.broker.dispatchContacts(mgr, req(ui.CapContactsBooks, nil), r.reply)
	if books, _ := r.data.([]ui.ContactBook); len(books) != 2 || books[0].ReadOnly || !books[1].ReadOnly {
		t.Errorf("%+v", books)
	}

	other := &App{M: &Manifest{ID: "com.example.app", Name: "Other", Permissions: []string{ui.PermContacts}}, running: true}
	moved := lei
	moved.Book = ui.BookPhone
	r = answer{}
	c.broker.dispatchContacts(other, req(ui.CapContactsSave, moved), r.reply)
	if n := len(c.overlays); n > 0 {
		ov, ok := c.overlays[n-1].(*confirmOverlay)
		if !ok {
			t.Fatalf("%T", c.overlays[n-1])
		}
		ov.onYes()
	}
	if x, _ := at.contact("\u674e\u96f7"); x.Book != acct.ID+"/b1" {
		t.Fatalf("another app moved it: %+v", x)
	}
	r = answer{}
	c.broker.dispatchContacts(mgr, req(ui.CapContactsSave, moved), r.reply)
	if x, _ := at.contact("\u674e\u96f7"); r.err != nil || x.Book != "" || len(x.Emails) != 1 {
		t.Fatalf("not moved: %+v %v", x, r.err)
	}
	at.later(accountAfterEdit)
	at.synced()
	if at.srv.Card("lei.vcf") != nil {
		t.Error("its card stayed in the old book")
	}
	if n := at.srv.Count("refused"); n != 0 {
		t.Errorf("%d writes to the read-only book", n)
	}
}

// The system's calendar app reads the birthdays and anniversaries, and
// nothing else of the contacts; no other app may.
func TestContactDates(t *testing.T) {
	at := newAccountTest(t)
	c := at.c
	book := c.contactBook()
	book.Save(ui.Contact{Name: "Ann", Number: "3001", Birthday: "--04-12", Notes: "private"})
	book.Save(ui.Contact{Name: "Bob", Number: "3002", Anniversary: "2010-06-12"})
	cal := &App{M: &Manifest{ID: appCalendar, Name: "Calendar"}, running: true}
	var r answer
	c.broker.dispatchContacts(cal, req(ui.CapContactsDates, nil), r.reply)
	got, _ := r.data.([]ui.ContactDate)
	if len(got) != 2 || got[0].Name != "Ann" || got[0].Birthday != "--04-12" || got[1].Anniversary != "2010-06-12" {
		t.Fatalf("%+v %v", got, r.err)
	}
	r = answer{}
	c.broker.dispatchContacts(&App{M: &Manifest{ID: "com.example.app"}, running: true}, req(ui.CapContactsDates, nil), r.reply)
	if r.err == nil {
		t.Error("another app read them")
	}
}
