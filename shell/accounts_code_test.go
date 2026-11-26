package shell

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/account"
	"github.com/kario-os/kario/account/accounttest"
	"github.com/kario-os/kario/ui"
)

// codeServer puts the account server's sign-in with a code (for acctUser)
// at the CardDAV server's address, as the real server has both, the
// passwords it makes for phones taken where acctPass is.
func (at *accountTest) codeServer() *accounttest.Server {
	auth := accounttest.New()
	auth.AddAccount(acctUser, 0)
	auth.CodeInterval = 0
	dav := devicePasswords(auth, acctUser, acctPass, at.srv)
	at.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
			auth.ServeHTTP(w, r)
			return
		}
		dav.ServeHTTP(w, r)
	}))
	at.t.Cleanup(at.ts.Close)
	at.c.cfg.AccountURL = at.ts.URL
	return auth
}

// sendCode is account.add with SendCode: where the code went.
func (at *accountTest) sendCode(user string) (ui.AccountCode, error) {
	at.t.Helper()
	at.req(at.app, ui.CapAccountAdd, ui.AccountArgs{Kind: ui.AccountKario, User: user, SendCode: true})
	r := at.reply(at.app)
	var out ui.AccountCode
	if !r.OK {
		return out, errors.New(r.Err)
	}
	if err := json.Unmarshal(r.Data, &out); err != nil {
		at.t.Fatal(err)
	}
	return out, nil
}

func (at *accountTest) signInWithCode(user, code string, agree bool) (ui.AccountState, error) {
	at.t.Helper()
	return at.call(ui.CapAccountAdd, ui.AccountArgs{Kind: ui.AccountKario, User: user, Code: code, Agree: agree})
}

func otherCode(code string) string {
	if code == "000000" {
		return "111111"
	}
	return "000000"
}

// Signing in to the Kario account with a code: a wrong code says how many
// tries are left and keeps nothing; the right one gets the phone a
// password of its own, named after the phone, which syncs, is kept sealed
// (the record has its name) and is never in the log, nor the code or the
// address. Signing out revokes it on the server.
func TestAccountSignInWithCode(t *testing.T) {
	if !alone(t) {
		return
	}
	at := newAccountTest(t)
	auth := at.codeServer()
	c := at.c
	sent, err := at.sendCode(" Alice@Kario.test ")
	if err != nil || sent.To != "Alice@Kario.test" || sent.ExpiresIn != int(account.CodeTTL.Seconds()) {
		t.Fatalf("send a code: %+v %v", sent, err)
	}
	code := auth.LastCode(acctUser)
	if _, err := at.signInWithCode(acctUser, otherCode(code), false); err == nil || err.Error() != tr.T("The code is wrong. Tries left: %d", 4) {
		t.Fatalf("a wrong code: %v", err)
	}
	if st, _ := at.call(ui.CapAccountState, nil); len(st.Accounts) != 0 || c.accounts.file.Seq != 0 || len(auth.Devices()) != 0 {
		t.Fatalf("a wrong code left an account: %+v, seq %d", st, c.accounts.file.Seq)
	}
	st, err := at.signInWithCode("Alice@Kario.test", code, false)
	if err != nil || len(st.Accounts) != 1 {
		t.Fatalf("sign in: %+v %v", st, err)
	}
	acct := st.Accounts[0]
	devs := auth.Devices()
	if acct.Kind != ui.AccountKario || acct.User != acctUser || acct.Device != c.accountDeviceName() || len(devs) != 1 ||
		devs[0].Name != c.accountDeviceName() || devs[0].Revoked {
		t.Fatalf("signed in: %+v, the server's: %+v", acct, devs)
	}
	at.synced()
	if _, ok := at.contact("\u674e\u96f7"); !ok || at.view().State != ui.AccountIdle {
		t.Fatalf("the phone's password did not sync: %+v", at.view())
	}
	raw, _ := os.ReadFile(c.accountsPath())
	if !strings.Contains(string(raw), `"device": "`+c.accountDeviceName()+`"`) || strings.Contains(string(raw), devs[0].Password) {
		t.Fatalf("accounts.json: %s", raw)
	}
	st, err = at.call(ui.CapAccountRemove, ui.AccountArgs{ID: acct.ID})
	if err != nil || len(st.Accounts) != 0 || st.Unrevoked != "" {
		t.Fatalf("sign out: %+v %v", st, err)
	}
	if devs := auth.Devices(); !devs[0].Revoked {
		t.Fatalf("signing out did not revoke the phone's password: %+v", devs)
	}
	for _, secret := range []string{code, devs[0].Password, acctUser, "alice", "Alice"} {
		if strings.Contains(at.log.String(), secret) {
			t.Errorf("the log tells %q:\n%s", secret, at.log.String())
		}
	}
	if !strings.Contains(at.log.String(), "password on 127.0.0.1") || !strings.Contains(at.log.String(), "revoked") {
		t.Errorf("%s", at.log.String())
	}
}

// Five wrong codes end the code; a new address asks for the consent with
// the code holding, and a sign-in that then fails on the phone revokes the
// password the server made; codes are for the Kario account only, to an
// address, as often as the server lets them go; one signed in already
// gets none.
func TestAccountCodeRefusals(t *testing.T) {
	at := newAccountTest(t)
	auth := at.codeServer()
	if _, err := at.sendCode(acctUser); err != nil {
		t.Fatal(err)
	}
	code := auth.LastCode(acctUser)
	for i := 1; i <= account.CodeAttempts; i++ {
		_, err := at.signInWithCode(acctUser, otherCode(code), false)
		want := tr.T("The code is wrong. Tries left: %d", account.CodeAttempts-i)
		if i == account.CodeAttempts {
			want = tr.T("The code is wrong, has expired or was tried too often: send a new one")
		}
		if err == nil || err.Error() != want {
			t.Fatalf("wrong code %d: %v", i, err)
		}
	}
	if _, err := at.signInWithCode(acctUser, code, false); err == nil || err.Error() != tr.T("The code is wrong, has expired or was tried too often: send a new one") {
		t.Fatalf("the right code after too many wrong ones: %v", err)
	}

	if _, err := at.sendCode("bob@kario.test"); err != nil {
		t.Fatal(err)
	}
	bob := auth.LastCode("bob@kario.test")
	if _, err := at.signInWithCode("bob@kario.test", bob, false); err == nil || !strings.HasSuffix(err.Error(), ui.ErrAccountConsent) {
		t.Fatalf("a new address without consent: %v", err)
	}
	if _, err := at.signInWithCode("bob@kario.test", bob, true); err == nil || err.Error() != accountProblemText(problemAuth, "") {
		t.Fatalf("bob's sign-in, which the CardDAV server refuses: %v", err)
	}
	at.pump("the failed sign-in's password revoked", func() bool {
		d := auth.Devices()
		return len(d) == 1 && d[0].Revoked
	})
	if !auth.HasAccount("bob@kario.test") || len(at.c.accounts.file.Accounts) != 0 {
		t.Fatal("the consent made no account, or the failed sign-in kept one")
	}

	for args, want := range map[ui.AccountArgs]string{
		{Kind: ui.AccountCardDAV, User: acctUser, SendCode: true}: tr.T("Only the Kario account signs in with a code"),
		{Kind: ui.AccountCardDAV, User: acctUser, Code: "123456"}: tr.T("Only the Kario account signs in with a code"),
		{Kind: ui.AccountKario, User: "alice", SendCode: true}:    tr.T("Enter the e-mail address of your Kario account"),
	} {
		if _, err := at.call(ui.CapAccountAdd, args); err == nil || err.Error() != want {
			t.Errorf("%+v: %v", args, err)
		}
	}
	auth.CodeInterval = time.Minute
	if _, err := at.sendCode("carol@kario.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := at.sendCode("carol@kario.test"); err == nil || err.Error() != tr.T("Too many tries: try again in %d min", 1) {
		t.Fatalf("a second code at once: %v", err)
	}
	auth.Down = true
	if _, err := at.sendCode("dave@kario.test"); err == nil || err.Error() != tr.T("The server answered with an error (%s)", "503") {
		t.Fatalf("a server down: %v", err)
	}
	auth.Down = false
	at.signIn()
	if _, err := at.sendCode(acctUser); err == nil || err.Error() != tr.T("“%s” is signed in already", acctUser) {
		t.Fatalf("a code for an account signed in: %v", err)
	}
	if strings.Contains(at.log.String(), "bob") || strings.Contains(at.log.String(), "carol") {
		t.Errorf("the log names an address:\n%s", at.log.String())
	}
}

// codeTheftTest is the reset test with the account server's sign-in with
// a code (for lei@kario.test) and its anti-theft lock on one address, the
// passwords it makes for phones taken where the account's is, the phone's
// requests counted by path, and what happens on the server after the next
// request to a path is answered (then).
type codeTheftTest struct {
	*resetTest
	auth *accounttest.Server
	ts   *httptest.Server
	mu   sync.Mutex
	hits map[string]int
	then map[string]func()
}

func newCodeTheftTest(t *testing.T) *codeTheftTest {
	ct := &codeTheftTest{resetTest: newResetTest(t), auth: accounttest.New(), hits: map[string]int{}, then: map[string]func(){}}
	ct.auth.AddAccount("lei@kario.test", 0)
	ct.auth.CodeInterval = 0
	ct.srv.AddAccount("lei@kario.test", "app-pass")
	theft := devicePasswords(ct.auth, "lei@kario.test", "app-pass", ct.srv)
	ct.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct.mu.Lock()
		ct.hits[r.URL.Path]++
		then := ct.then[r.URL.Path]
		delete(ct.then, r.URL.Path)
		ct.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
			ct.auth.ServeHTTP(w, r)
		} else {
			theft.ServeHTTP(w, r)
		}
		if then != nil {
			then()
		}
	}))
	t.Cleanup(ct.ts.Close)
	ct.c.cfg.AccountURL = ct.ts.URL
	return ct
}

func (ct *codeTheftTest) hit(path string) int {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return ct.hits[path]
}

func (ct *codeTheftTest) after(path string, f func()) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.then[path] = f
}

// signInArmed signs the Kario account in on the phone with a code, as
// account id, its password sealed, and waits until the lock is armed.
func (ct *codeTheftTest) signInArmed(id string) account.Device {
	ct.t.Helper()
	cl := &account.Client{Server: ct.ts.URL}
	ctx := context.Background()
	if _, err := cl.RequestCode(ctx, "lei@kario.test"); err != nil {
		ct.t.Fatal(err)
	}
	dev, err := cl.SignIn(ctx, account.SignIn{Email: "lei@kario.test", Code: ct.auth.LastCode("lei@kario.test"), Device: "Kario"})
	if err != nil {
		ct.t.Fatal(err)
	}
	sealed, err := ct.ks.SealSecret("carddav:"+id, []byte(dev.Password))
	if err != nil {
		ct.t.Fatal(err)
	}
	ct.c.accounts.file.Accounts = []*accountRecord{{ID: id, Kind: ui.AccountKario, User: "lei@kario.test", Sealed: sealed, Device: dev.Name}}
	ct.c.theftCheck()
	ct.pump("the binding", func() bool { return ct.c.theft.armed })
	return dev
}

// account is an account.* request as Settings makes it, and its answer.
func (ct *codeTheftTest) account(capName string, args ui.AccountArgs) (ui.AccountState, error) {
	ct.t.Helper()
	var st ui.AccountState
	var serr error
	n := 0
	raw, _ := json.Marshal(args)
	ct.c.dispatchAccount(ui.Msg{Cap: capName, Args: raw}, func(v any, err error) {
		st, _ = v.(ui.AccountState)
		serr, n = err, n+1
	})
	ct.pump(capName, func() bool { return n == 1 })
	return st, serr
}

// An account signed in with a code signs out in order: the anti-theft
// lock's binding ends first, signing in with the phone's password, and
// only then is the password revoked (the other way round, the server
// would keep the phone bound to the account).
func TestSignOutWithACodeUnbindsFirst(t *testing.T) {
	ct := newCodeTheftTest(t)
	ct.signInArmed("a1")
	st, serr := ct.account(ui.CapAccountRemove, ui.AccountArgs{ID: "a1"})
	if serr != nil || st.Unrevoked != "" || st.LockLeft != nil || !ct.auth.Devices()[0].Revoked {
		t.Fatalf("signed out: %+v %v, the server's passwords %+v", st, serr, ct.auth.Devices())
	}
	if devs := ct.srv.Devices(); len(devs) != 0 {
		t.Fatalf("the phone is still bound: %v", devs)
	}
}

// Signed out on the account's page, then here: the binding cannot be
// ended with the refused password, so it stays on the server, and the
// sign-out's answer says so (LockLeft: the account and this phone's device
// ID, as the page lists it). The note is kept until the server says the
// binding is gone, which account.state with Check asks at once and plain
// account.state (Settings looking) at most every leftEvery; a Check while
// such a look is under way asks again after it (the owner removed the
// phone meanwhile). Signed out offline, the password and the binding are
// both left; a check that cannot reach the server says so; binding again
// takes the note away.
func TestSignOutLeavesTheBinding(t *testing.T) {
	ct := newCodeTheftTest(t)
	c := ct.c
	dev := ct.signInArmed("a1")
	device := c.theft.rec.Device
	want := ui.AccountLockLeft{User: "lei@kario.test", Device: device}
	ct.auth.Revoke(dev.ID)
	st, err := ct.account(ui.CapAccountRemove, ui.AccountArgs{ID: "a1"})
	if err != nil || len(st.Accounts) != 0 || st.Unrevoked != "" || st.LockLeft == nil || *st.LockLeft != want {
		t.Fatalf("signed out after the page signed the phone out: %+v %v", st, err)
	}
	if _, ok := ct.srv.Bound(device); !ok {
		t.Fatal("the binding went with a refused password")
	}
	var file accountFile
	raw, _ := os.ReadFile(c.accountsPath())
	if json.Unmarshal(raw, &file) != nil || file.LockLeft == nil || file.LockLeft.Device != device || file.LockLeft.Account == "" {
		t.Fatalf("accounts.json: %s", raw)
	}

	st, err = ct.account(ui.CapAccountState, ui.AccountArgs{Check: true})
	if err != nil || st.LockLeft == nil || ct.hit("/v1/antitheft/status") != 1 {
		t.Fatalf("checked while bound: %+v %v, %d asked", st, err, ct.hit("/v1/antitheft/status"))
	}
	if st, _ = ct.account(ui.CapAccountState, ui.AccountArgs{}); st.LockLeft == nil || ct.hit("/v1/antitheft/status") != 1 {
		t.Fatalf("asked again within %v: %+v", leftEvery, st)
	}
	c.clockSkew += leftEvery
	ct.after("/v1/antitheft/status", func() { _ = ct.srv.Remove(device) })
	if st, _ = ct.account(ui.CapAccountState, ui.AccountArgs{}); st.LockLeft == nil {
		t.Fatalf("a look under way: %+v", st)
	}
	st, err = ct.account(ui.CapAccountState, ui.AccountArgs{Check: true})
	raw, _ = os.ReadFile(c.accountsPath())
	if err != nil || st.LockLeft != nil || ct.hit("/v1/antitheft/status") != 3 || strings.Contains(string(raw), "lock_left") {
		t.Fatalf("checked after a look under way, removed on the page meanwhile: %+v %v, %d asked\n%s", st, err, ct.hit("/v1/antitheft/status"), raw)
	}

	dev = ct.signInArmed("a2")
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	c.cfg.AccountURL = down.URL
	st, err = ct.account(ui.CapAccountRemove, ui.AccountArgs{ID: "a2"})
	if err != nil || st.Unrevoked != dev.Name || st.LockLeft == nil || *st.LockLeft != want {
		t.Fatalf("signed out offline: %+v %v", st, err)
	}
	if _, err := ct.account(ui.CapAccountState, ui.AccountArgs{Check: true}); err == nil || err.Error() != tr.T("Cannot reach the Kario account server") ||
		c.accounts.file.LockLeft == nil {
		t.Fatalf("checked offline: %v", err)
	}
	c.cfg.AccountURL = ct.ts.URL
	ct.signInArmed("a3")
	if st, _ = ct.account(ui.CapAccountState, ui.AccountArgs{}); st.LockLeft != nil {
		t.Fatalf("bound again: %+v", st.LockLeft)
	}
}

// Before signing out, the account's view says whether the sign-out would
// leave the anti-theft lock's binding on the server (LockStays): not while
// the phone can end it; for want of a network once a network event, or
// Settings' look at most every lockNetEvery, finds none; for a password
// the server refuses, which the sign-out then indeed leaves behind. A
// Kario account the lock is not bound to says nothing, offline too.
func TestSignOutWarnsOfTheLock(t *testing.T) {
	ct := newCodeTheftTest(t)
	c := ct.c
	stays := func() string {
		t.Helper()
		st, err := ct.account(ui.CapAccountState, ui.AccountArgs{})
		if err != nil || len(st.Accounts) != 1 {
			t.Fatalf("account.state: %+v %v", st, err)
		}
		return st.Accounts[0].LockStays
	}
	sealed, err := ct.ks.SealSecret("carddav:a1", []byte("app-pass"))
	if err != nil {
		t.Fatal(err)
	}
	c.accounts.file.Accounts = []*accountRecord{{ID: "a1", Kind: ui.AccountKario, User: "lei@kario.test", Sealed: sealed}}
	online := false
	c.accounts.net = func(then func(online, metered bool)) { then(online, false) }
	c.accountsNetChanged()
	if got := stays(); got != "" {
		t.Fatalf("not bound yet, offline: %q", got)
	}
	online = true
	c.accountsNetChanged()
	ct.pump("the binding", func() bool { return c.theft.armed })
	if got := stays(); got != "" {
		t.Fatalf("bound, online: %q", got)
	}
	online = false
	c.accountsNetChanged()
	if got := stays(); got != ui.AccountWaitNetwork {
		t.Fatalf("bound, the network gone: %q", got)
	}
	online = true
	if got := stays(); got != ui.AccountWaitNetwork {
		t.Fatalf("looked at again within %v: %q", lockNetEvery, got)
	}
	c.clockSkew += lockNetEvery
	if got := stays(); got != "" {
		t.Fatalf("the network back, looked at again: %q", got)
	}
	ct.srv.AddAccount("lei@kario.test", "changed-on-the-page")
	c.accountRefused(c.karioAccount())
	if got := stays(); got != ui.AccountAuth {
		t.Fatalf("the password refused: %q", got)
	}
	st, err := ct.account(ui.CapAccountRemove, ui.AccountArgs{ID: "a1"})
	if err != nil || st.LockLeft == nil {
		t.Fatalf("signed out as warned: %+v %v", st, err)
	}
}

// Binding the lock while the server has the phone bound to another
// account (a previous owner's binding left behind) is 409: the account's
// view and accounts.json keep this phone's device ID, the log says so, and
// nothing tries again however long the phone waits. Trying again
// (account.set RetryLock) asks once and answers why it did not bind; once
// the previous owner removed the phone it binds. A binding this phone left
// behind itself has the next account bind at once when the check finds it
// gone.
func TestBindingTaken(t *testing.T) {
	ct := newCodeTheftTest(t)
	c := ct.c
	logs := &syncBuffer{}
	c.log = log.New(logs, "", 0)
	id, pub, _ := ct.ks.CSR()
	if err := ct.srv.BindTo(id, pub, "previous@kario.test"); err != nil {
		t.Fatal(err)
	}
	signIn := func(acct, user, pass string) {
		t.Helper()
		sealed, err := ct.ks.SealSecret("carddav:"+acct, []byte(pass))
		if err != nil {
			t.Fatal(err)
		}
		c.accounts.file.Accounts = []*accountRecord{{ID: acct, Kind: ui.AccountKario, User: user, Sealed: sealed}}
		c.theftCheck()
		ct.pump("the binding's answer", func() bool { return !c.theft.arming })
	}
	view := func() ui.Account {
		t.Helper()
		st, err := ct.account(ui.CapAccountState, ui.AccountArgs{})
		if err != nil || len(st.Accounts) != 1 {
			t.Fatalf("account.state: %+v %v", st, err)
		}
		return st.Accounts[0]
	}
	signIn("a1", "lei@kario.test", "app-pass")
	if v := view(); c.theft.armed || v.LockTaken != id || v.State != ui.AccountIdle {
		t.Fatalf("bound to another account: armed %v, %+v", c.theft.armed, v)
	}
	if raw, _ := os.ReadFile(c.accountsPath()); !strings.Contains(string(raw), `"lock_taken": "`+id+`"`) {
		t.Fatalf("accounts.json: %s", raw)
	}
	if !strings.Contains(logs.String(), "bound to another account") || !strings.Contains(logs.String(), id) {
		t.Errorf("the log: %s", logs.String())
	}
	binds := ct.hit("/v1/antitheft/bind")
	c.clockSkew += theftRetryMax + time.Minute
	c.theftCheck()
	if c.theft.arming || ct.hit("/v1/antitheft/bind") != binds {
		t.Fatal("the binding was tried again while the phone is another account's")
	}
	st, err := ct.account(ui.CapAccountSet, ui.AccountArgs{ID: "a1", RetryLock: true})
	if err == nil || err.Error() != tr.T("The previous owner has not removed the phone yet") || ct.hit("/v1/antitheft/bind") != binds+1 ||
		len(st.Accounts) != 1 || st.Accounts[0].LockTaken != id {
		t.Fatalf("tried again: %+v %v, %d binds", st, err, ct.hit("/v1/antitheft/bind")-binds)
	}
	if err := ct.srv.Remove(id); err != nil {
		t.Fatal(err)
	}
	st, err = ct.account(ui.CapAccountSet, ui.AccountArgs{ID: "a1", RetryLock: true})
	if err != nil || !c.theft.armed || st.Accounts[0].LockTaken != "" {
		t.Fatalf("tried again after the previous owner removed it: armed %v, %+v %v", c.theft.armed, st, err)
	}

	ct.srv.AddAccount("lei@kario.test", "changed-on-the-page")
	if st, err = ct.account(ui.CapAccountRemove, ui.AccountArgs{ID: "a1"}); err != nil || st.LockLeft == nil {
		t.Fatalf("signed out, the binding left behind: %+v %v", st, err)
	}
	ct.srv.AddAccount("ann@kario.test", "ann-pass")
	signIn("a2", "ann@kario.test", "ann-pass")
	if v := view(); c.theft.armed || v.LockTaken != id {
		t.Fatalf("bound to the account signed out of: %+v", v)
	}
	if err := ct.srv.Remove(id); err != nil {
		t.Fatal(err)
	}
	if st, err = ct.account(ui.CapAccountState, ui.AccountArgs{Check: true}); err != nil || st.LockLeft != nil {
		t.Fatalf("checked: %+v %v", st, err)
	}
	ct.pump("the binding after the check", func() bool { return c.theft.armed })
	if v := view(); v.LockTaken != "" {
		t.Fatalf("bound after the check: %+v", v)
	}
}

// The phone's password revoked on the account's page stops the sync with
// its own explanation; signing out then is no trouble. Signing out while
// the server cannot be told takes the account off the phone all the same
// and names the password to revoke on the page.
func TestAccountDeviceRevokedAndOffline(t *testing.T) {
	at := newAccountTest(t)
	auth := at.codeServer()
	sign := func() ui.Account {
		t.Helper()
		if _, err := at.sendCode(acctUser); err != nil {
			t.Fatal(err)
		}
		st, err := at.signInWithCode(acctUser, auth.LastCode(acctUser), false)
		if err != nil {
			t.Fatal(err)
		}
		at.synced()
		return st.Accounts[0]
	}
	acct := sign()
	auth.Revoke(auth.Devices()[0].ID)
	if _, err := at.call(ui.CapAccountSync, ui.AccountArgs{ID: acct.ID}); err != nil {
		t.Fatal(err)
	}
	at.synced()
	if v := at.view(); v.State != ui.AccountAuth || v.Error != deviceAuthText() {
		t.Fatalf("revoked on the page: %+v", v)
	}
	if st, err := at.call(ui.CapAccountRemove, ui.AccountArgs{ID: acct.ID}); err != nil || st.Unrevoked != "" {
		t.Fatalf("signing out with a password revoked on the page: %+v %v", st, err)
	}

	acct = sign()
	auth.Down = true
	st, err := at.call(ui.CapAccountRemove, ui.AccountArgs{ID: acct.ID, RemoveContacts: true})
	if err != nil || len(st.Accounts) != 0 || st.Unrevoked != acct.Device {
		t.Fatalf("signing out offline: %+v %v", st, err)
	}
	if d := auth.Devices(); d[len(d)-1].Revoked {
		t.Fatal("revoked while down")
	}
	if !strings.Contains(at.log.String(), "is not revoked: the server answered 503") {
		t.Errorf("%s", at.log.String())
	}
}
