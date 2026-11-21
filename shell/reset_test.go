package shell

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/antitheft/antitheftest"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wipe"
	"github.com/kario-os/kario/wire"
)

// resetTest is stCore with phone-keystored (a real store, over its socket),
// a persistent area, a restart that is counted, and the account server's
// anti-theft API.
type resetTest struct {
	*locTest
	ks       *keystore.Store
	srv      *antitheftest.Server
	restarts int
}

func newResetTest(t *testing.T) *resetTest {
	lt, _ := stCore(t)
	c := lt.c
	c.cfg.RunDir, c.cfg.PersistDir = t.TempDir(), t.TempDir()
	_ = os.MkdirAll(filepath.Join(c.cfg.StateDir, "shell"), 0o700)
	c.broker.clients = map[string]*wire.Client{}
	ks, err := keystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	go wire.Serve(filepath.Join(c.cfg.RunDir, svc.KeystoredSock), func(method string, params json.RawMessage) (any, error) {
		var p struct{ App, Nonce, Old, PIN, Purpose, Data, Sealed string }
		_ = json.Unmarshal(params, &p)
		switch method {
		case keystore.MethodLockVerify:
			return ks.VerifyLockPIN(p.PIN, 0)
		case keystore.MethodLockStatus:
			return ks.LockStatus(), nil
		case keystore.MethodParentStatus:
			return ks.ParentStatus(), nil
		case keystore.MethodParentVerify:
			return ks.VerifyParentPIN(p.PIN)
		case svc.KeyAttest:
			return ks.Attest(p.App, p.Nonce, "")
		case keystore.MethodSecretOpen:
			plain, err := ks.OpenSecret(p.Purpose, p.Sealed)
			return map[string]string{"data": base64.StdEncoding.EncodeToString(plain)}, err
		}
		return nil, errors.New("unknown method " + method)
	})
	rt := &resetTest{locTest: lt, ks: ks, srv: antitheftest.New()}
	c.cfg.Reboot = func() { rt.restarts++ }
	ts := httptest.NewServer(rt.srv)
	t.Cleanup(ts.Close)
	key := filepath.Join(t.TempDir(), "account.pub")
	if err := os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(rt.srv.Pub)), 0o644); err != nil {
		t.Fatal(err)
	}
	c.cfg.AccountURL, c.cfg.FamilyKey = ts.URL, key
	c.accounts.loaded = true
	return rt
}

// signIn is a Kario account signed in on the phone, its password sealed.
func (rt *resetTest) signIn() {
	rt.t.Helper()
	rt.srv.AddAccount("lei@kario.test", "app-pass")
	sealed, err := rt.ks.SealSecret("carddav:a1", []byte("app-pass"))
	if err != nil {
		rt.t.Fatal(err)
	}
	rt.c.accounts.file.Accounts = []*accountRecord{{ID: "a1", Kind: ui.AccountKario, User: "Lei@kario.test", Sealed: sealed}}
}

// arm signs in and waits until the lock is armed.
func (rt *resetTest) arm() {
	rt.t.Helper()
	rt.signIn()
	rt.c.theftCheck()
	rt.pump("the binding", func() bool { return rt.c.theft.armed })
	if _, err := os.Stat(rt.c.theftPath()); err != nil {
		rt.t.Fatalf("no record in the persistent area: %v", err)
	}
	if rt.c.theftMark() != rt.c.theft.rec.Token {
		rt.t.Fatal("the data area does not have the record's token")
	}
	if len(rt.srv.Devices()) != 1 {
		rt.t.Fatalf("the server's bound phones: %v", rt.srv.Devices())
	}
}

// boot is the phone after the restart: the wipe the request asked for
// (package wipe), and the Shell's start reading what is left.
func (rt *resetTest) boot() {
	rt.t.Helper()
	c := rt.c
	if _, err := wipe.Boot(c.cfg.StateDir, c.cfg.PersistDir); err != nil {
		rt.t.Fatal(err)
	}
	c.theft, c.reset, c.overlays = theftShell{}, resetShell{}, nil
	c.startTheft()
}

func (rt *resetTest) pin(o interface{ key(*Core, Key) }, digits string) {
	for _, r := range digits {
		o.key(rt.c, Runes(string(r)))
	}
	o.key(rt.c, Named(KeyEnter))
}

func (rt *resetTest) confirmShown() *confirmOverlay {
	rt.t.Helper()
	o, ok := overlayOf[*confirmOverlay](rt.c)
	if !ok || !o.trust {
		rt.t.Fatalf("no last confirmation of the system's own: %T", rt.c.overlays)
	}
	return o
}

// From Settings: the lock-screen PIN on the system's screen, then a last
// confirmation that keys from a computer cannot answer; backing out there
// erases nothing. Confirmed, the request waits in the persistent area and
// the phone restarts.
func TestFactoryResetFromSettings(t *testing.T) {
	rt := newResetTest(t)
	c := rt.c
	if r, err := rt.ks.SetLockPIN("", "2468"); err != nil || !r.OK {
		t.Fatal(r, err)
	}
	c.lock.pin = true
	var got []error
	done := func(err error) { got = append(got, err) }
	if !c.resetSetting(ui.SettingArgs{Key: ui.SettingFactoryReset, Value: 1}, done) {
		t.Fatal("factory_reset is not the reset's")
	}
	pad, ok := overlayOf[*lockSetOverlay](c)
	if !ok || pad.op != "erase" {
		t.Fatalf("no PIN screen to erase: %T", c.overlays)
	}
	rt.pin(pad, "1111")
	rt.pump("the key service", func() bool { return !pad.busy })
	if _, ok := overlayOf[*lockSetOverlay](c); !ok || len(got) != 0 {
		t.Fatal("a wrong PIN went on")
	}
	rt.pin(pad, "2468")
	rt.pump("the key service", func() bool { return !pad.busy })
	o := rt.confirmShown()
	if !strings.Contains(o.text, tr.T("Erase everything on this phone?")) {
		t.Fatalf("confirmation %q", o.text)
	}
	if c.overlayTakes(o, Key{Name: KeyEnter, Injected: true}) {
		t.Fatal("a computer's key answered the last confirmation")
	}
	o.key(c, Named(KeyEsc))
	if len(got) != 1 || !ui.Cancelled(got[0]) || wipe.Requested(c.cfg.PersistDir) {
		t.Fatalf("backing out: %v, requested %v", got, wipe.Requested(c.cfg.PersistDir))
	}

	c.resetSetting(ui.SettingArgs{Key: ui.SettingFactoryReset, Value: 1}, done)
	pad, _ = overlayOf[*lockSetOverlay](c)
	rt.pin(pad, "2468")
	rt.pump("the key service", func() bool { return !pad.busy })
	rt.confirmShown().key(c, Named(KeyEnter))
	if len(got) != 2 || got[1] != nil {
		t.Fatalf("confirmed: %v", got)
	}
	if _, ok := overlayOf[*wipingOverlay](c); !ok {
		t.Fatal("nothing says the phone is being erased")
	}
	rt.pump("the restart", func() bool { return rt.restarts == 1 })
	if wipe.Why(c.cfg.PersistDir) != wipe.FromSettings {
		t.Fatalf("request %q", wipe.Why(c.cfg.PersistDir))
	}
}

// In minor mode a reset takes the parental PIN, every time: not the
// lock-screen PIN (the child's), and not the minutes a parent's PIN entered
// in Settings counts for otherwise.
func TestFactoryResetInMinorMode(t *testing.T) {
	rt := newResetTest(t)
	c := rt.c
	if r, err := rt.ks.SetParentPIN("", "1357"); err != nil || !r.OK {
		t.Fatal(r, err)
	}
	c.setMinor(true)
	c.lock.pin = true
	c.fg, c.parental.openUntil = c.appByID(appSettings), c.now().Add(parentOpenFor)
	if !c.parentOpen() {
		t.Fatal("the parent's PIN does not count in Settings")
	}
	var got []error
	c.resetSetting(ui.SettingArgs{Key: ui.SettingFactoryReset, Value: 1}, func(err error) { got = append(got, err) })
	o, ok := overlayOf[*parentPadOverlay](c)
	if !ok || o.op != "verify" {
		t.Fatalf("no parental PIN screen: %T", c.overlays)
	}
	enterPIN(rt.locTest, "1357")
	rt.confirmShown()
	if len(got) != 0 {
		t.Fatalf("answered before the last confirmation: %v", got)
	}
}

// Without a lock-screen PIN only the last confirmation stands in the way;
// a device without a persistent area refuses.
func TestFactoryResetWithoutAPIN(t *testing.T) {
	rt := newResetTest(t)
	c := rt.c
	c.resetSetting(ui.SettingArgs{Key: ui.SettingFactoryReset, Value: 1}, func(error) {})
	rt.confirmShown()
	c.overlays = nil
	c.cfg.PersistDir = ""
	var got error
	c.resetSetting(ui.SettingArgs{Key: ui.SettingFactoryReset, Value: 1}, func(err error) { got = err })
	if got == nil || len(c.overlays) != 0 {
		t.Fatalf("no persistent area: %v %T", got, c.overlays)
	}
}

// The lock screen offers to erase a phone whose PIN is forgotten only after
// forgotAfter wrong PINs in a row; the anti-theft lock then holds: after
// the restart the phone is locked until the account's code releases it.
func TestForgottenPINAndTheLock(t *testing.T) {
	rt := newResetTest(t)
	c := rt.c
	rt.arm()
	c.lock.pin, c.keylock = true, true
	c.openLockPad("", false)
	pad, _ := overlayOf[*lockPadOverlay](c)
	c.lock.st.Failures = forgotAfter - 1
	pad.key(c, Runes("#"))
	if _, ok := overlayOf[*lockPadOverlay](c); !ok {
		t.Fatal("# erased the phone before enough wrong PINs")
	}
	c.lock.st.Failures = forgotAfter
	if lines, _, _ := pad.lines(c); !strings.Contains(strings.Join(lines, " "), "#") {
		t.Fatalf("the pad does not offer it: %q", lines)
	}
	pad.key(c, Runes("#"))
	o := rt.confirmShown()
	if !strings.Contains(o.text, tr.T("Erase this phone without its PIN?")) || !strings.Contains(o.text, "l***i@kario.test") {
		t.Fatalf("confirmation %q", o.text)
	}
	o.key(c, Named(KeyEnter))
	rt.pump("the restart", func() bool { return rt.restarts == 1 })
	if wipe.Why(c.cfg.PersistDir) != wipe.FromLockScreen || c.theft.rec.Locked != wipe.FromLockScreen {
		t.Fatalf("request %q, record %+v", wipe.Why(c.cfg.PersistDir), c.theft.rec)
	}

	rt.boot()
	th, ok := overlayOf[*theftOverlay](c)
	if !c.TheftLocked() || !ok {
		t.Fatal("the phone started unlocked after a reset without the PIN")
	}
	if _, err := os.Stat(filepath.Join(c.cfg.StateDir, "shell", "antitheft.json")); err == nil {
		t.Fatal("the data area's token outlived the wipe")
	}
	th.key(c, Named(KeyEnter))
	rt.pump("the server", func() bool { return !th.busy })
	if !c.TheftLocked() || !th.bad {
		t.Fatalf("checking released it: %q", th.note)
	}
	th.key(c, Named(KeyTab))
	rt.pump("the code", func() bool { return !th.busy })
	if th.sent != "l***i@kario.test" || rt.srv.LastCode("lei@kario.test") == "" {
		t.Fatalf("code sent to %q: %q", th.sent, th.note)
	}
	rt.pin(th, "000000")
	rt.pump("the server", func() bool { return !th.busy })
	if !c.TheftLocked() {
		t.Fatal("a wrong code released it")
	}
	rt.pin(th, rt.srv.LastCode("lei@kario.test"))
	rt.pump("the release", func() bool { return !c.TheftLocked() })
	if _, err := os.Stat(c.theftPath()); err == nil || len(rt.srv.Devices()) != 0 {
		t.Fatalf("after the release: record %v, bound %v", err, rt.srv.Devices())
	}
	if _, ok := overlayOf[*theftOverlay](c); ok {
		t.Fatal("the lock's screen stayed")
	}
}

// A release must be signed by the product's account server: one under
// another key leaves the phone locked.
func TestAntiTheftReleaseUnderAnotherKey(t *testing.T) {
	rt := newResetTest(t)
	c := rt.c
	rt.arm()
	c.theft.rec.Locked = wipe.FromLockScreen
	_ = c.saveTheft()
	rt.boot()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	other := filepath.Join(t.TempDir(), "other.pub")
	_ = os.WriteFile(other, []byte(base64.StdEncoding.EncodeToString(pub)), 0o644)
	c.cfg.FamilyKey = other
	th, _ := overlayOf[*theftOverlay](c)
	th.key(c, Named(KeyTab))
	rt.pump("the code", func() bool { return !th.busy })
	rt.pin(th, rt.srv.LastCode("lei@kario.test"))
	rt.pump("the release", func() bool { return !th.busy })
	if !c.TheftLocked() || !th.bad {
		t.Fatalf("a release under another key unlocked it: %q", th.note)
	}
}

// The owner removing the phone on the account's page releases it without a
// code, at the next check.
func TestAntiTheftRemovedOnThePage(t *testing.T) {
	rt := newResetTest(t)
	c := rt.c
	rt.arm()
	_ = os.Remove(c.theftMarkPath())
	rt.boot()
	if c.theft.rec.Locked != lockedByData {
		t.Fatalf("a data area without the token: %+v", c.theft.rec)
	}
	if err := rt.srv.Remove(rt.srv.Devices()[0]); err != nil {
		t.Fatal(err)
	}
	c.key(Named(KeyF4))
	c.key(Named(KeyEsc))
	c.closeForLock()
	th, ok := overlayOf[*theftOverlay](c)
	if !ok {
		t.Fatal("the end key, Back or locking took the anti-theft screen away")
	}
	th.key(c, Named(KeyEnter))
	rt.pump("the release", func() bool { return !c.TheftLocked() })
}

// From Settings with the PIN the lock comes off (the server hears the
// binding ended) unless the owner keeps it; kept, the phone starts locked.
func TestFactoryResetAndTheLock(t *testing.T) {
	rt := newResetTest(t)
	c := rt.c
	rt.arm()
	for _, f := range c.theftForReset(wipe.FromSettings, false) {
		f(context.Background())
	}
	if _, err := os.Stat(c.theftPath()); err == nil {
		t.Fatal("the record outlived a reset with the PIN")
	}
	rt.pump("the unbinding", func() bool { return len(rt.srv.Devices()) == 0 })
	rt.boot()
	if c.TheftLocked() {
		t.Fatal("locked after a reset with the PIN")
	}

	rt.arm()
	c.factoryReset(wipe.FromSettings, true)
	rt.pump("the restart", func() bool { return rt.restarts == 1 })
	rt.boot()
	if !c.TheftLocked() || len(rt.srv.Devices()) != 1 {
		t.Fatalf("kept on: locked %v, bound %v", c.TheftLocked(), rt.srv.Devices())
	}
}

// Signing out of the Kario account takes the lock off, after the
// lock-screen PIN; backing out keeps both.
func TestSignOutTakesTheLockOff(t *testing.T) {
	rt := newResetTest(t)
	c := rt.c
	rt.arm()
	if r, err := rt.ks.SetLockPIN("", "2468"); err != nil || !r.OK {
		t.Fatal(r, err)
	}
	c.lock.pin = true
	var got []error
	c.theftSignOut(c.karioAccount(), func(err error) { got = append(got, err) })
	pad, ok := overlayOf[*lockSetOverlay](c)
	if !ok || pad.op != "signout" {
		t.Fatalf("no PIN screen: %T", c.overlays)
	}
	pad.key(c, Named(KeyEsc))
	if len(got) != 1 || got[0] == nil || c.theft.rec == nil {
		t.Fatalf("backing out: %v, record %v", got, c.theft.rec)
	}
	c.theftSignOut(c.karioAccount(), func(err error) { got = append(got, err) })
	pad, _ = overlayOf[*lockSetOverlay](c)
	rt.pin(pad, "2468")
	rt.pump("the key service", func() bool { return len(got) == 2 })
	if got[1] != nil || c.theft.rec != nil {
		t.Fatalf("signed out: %v, record %v", got[1], c.theft.rec)
	}
	rt.pump("the unbinding", func() bool { return len(rt.srv.Devices()) == 0 })
}
