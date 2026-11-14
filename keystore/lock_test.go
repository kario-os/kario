package keystore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/hal/fingerprint"
)

// lockStore is a store with a cheap PIN hash, a simulated sensor and a
// clock the test moves.
type lockStore struct {
	*Store
	dir string
	sim *fingerprint.Sim
	at  time.Time
}

func openLock(t *testing.T, dir string) *lockStore {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ls := &lockStore{Store: s, dir: dir, sim: fingerprint.NewSim(), at: time.Now()}
	s.lk.iter = 1000
	s.lk.clock = func() time.Time { return ls.at }
	s.lk.started = ls.at
	s.lk.next = ls.at.Add(pinDelay(s.lk.file.Failures))
	s.SetSensor(ls.sim)
	return ls
}

func (ls *lockStore) pass(d time.Duration) { ls.at = ls.at.Add(d) }

func (ls *lockStore) touch(t *testing.T, finger string) AuthResult {
	t.Helper()
	ls.sim.Touch(finger)
	r, err := ls.Authenticate(context.Background(), "", "")
	if err != nil {
		t.Fatalf("touch %s: %v", finger, err)
	}
	return r
}

// enrol adds a finger with a fresh PIN token.
func (ls *lockStore) enrol(t *testing.T, pin, finger, name string) Finger {
	t.Helper()
	ch, err := ls.EnrollChallenge()
	if err != nil {
		t.Fatal(err)
	}
	r, err := ls.VerifyLockPIN(pin, ch)
	if err != nil || !r.OK {
		t.Fatalf("PIN: %+v %v", r, err)
	}
	steps, err := ls.EnrollBegin(r.Token, name)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= steps; i++ {
		ls.sim.Touch(finger)
		st, err := ls.EnrollNext(context.Background())
		if err != nil || st.Done != i || st.Rejected != "" {
			t.Fatalf("touch %d: %+v %v", i, st, err)
		}
		if i == steps {
			if st.Finger == nil {
				t.Fatal("no finger after the last touch")
			}
			return *st.Finger
		}
	}
	return Finger{}
}

func TestLockPINSetVerifyChange(t *testing.T) {
	ls := openLock(t, "")
	if st := ls.LockStatus(); st.PIN {
		t.Fatal("a new device has a lock PIN")
	}
	for _, bad := range []string{"123", "12345678901234567", "12a4", ""} {
		if _, err := ls.SetLockPIN("", bad); !errors.Is(err, ErrPINFormat) {
			t.Errorf("PIN %q accepted: %v", bad, err)
		}
	}
	if r, err := ls.SetLockPIN("", "2468"); err != nil || !r.OK {
		t.Fatalf("set: %+v %v", r, err)
	}
	raw, _ := os.ReadFile(filepath.Join(ls.dir, "keystore", "lock.json"))
	if strings.Contains(string(raw), "2468") || !strings.Contains(string(raw), `"hash"`) {
		t.Fatalf("lock.json: %s", raw)
	}
	if r, _ := ls.VerifyLockPIN("2468", 7); !r.OK || r.Token == "" {
		t.Fatalf("right PIN: %+v", r)
	}
	if r, _ := ls.VerifyLockPIN("1357", 0); r.OK || r.Left != 4 || r.WaitMS != 0 {
		t.Fatalf("wrong PIN: %+v", r)
	}
	if r, _ := ls.SetLockPIN("0000", "135790"); r.OK {
		t.Fatal("changed with a wrong PIN")
	}
	if r, _ := ls.SetLockPIN("2468", "135790"); !r.OK {
		t.Fatalf("change: %+v", r)
	}
	if r, _ := ls.VerifyLockPIN("2468", 0); r.OK {
		t.Fatal("the old PIN still works")
	}
	if r, _ := ls.VerifyLockPIN("135790", 0); !r.OK {
		t.Fatal("the new PIN does not work")
	}
}

func TestLockPINWaitsGrowAndSurviveARestart(t *testing.T) {
	ls := openLock(t, "")
	ls.SetLockPIN("", "2468")
	var r PINResult
	for i := 1; i <= 5; i++ {
		r, _ = ls.VerifyLockPIN("0000", 0)
		if r.OK || r.Failures != i {
			t.Fatalf("attempt %d: %+v", i, r)
		}
	}
	if r.WaitMS != 30_000 || r.Left != 0 {
		t.Fatalf("after 5 wrong: %+v", r)
	}
	if r, _ := ls.VerifyLockPIN("2468", 0); r.OK || r.Failures != 5 || r.WaitMS != 30_000 {
		t.Fatalf("the right PIN during the wait: %+v", r)
	}
	ls.pass(31 * time.Second)
	if r, _ := ls.VerifyLockPIN("0000", 0); r.OK || r.Failures != 6 || r.WaitMS != 30_000 {
		t.Fatalf("6th: %+v", r)
	}
	again := openLock(t, ls.dir)
	if st := again.LockStatus(); st.Failures != 6 || st.WaitMS != 30_000 {
		t.Fatalf("after a restart: %+v", st)
	}
	if r, _ := again.VerifyLockPIN("2468", 0); r.OK {
		t.Fatal("a restart skipped the wait")
	}
	again.pass(30 * time.Second)
	for i := 7; i < 10; i++ {
		again.VerifyLockPIN("0000", 0)
		again.pass(30 * time.Second)
	}
	if r, _ := again.VerifyLockPIN("0000", 0); r.Failures != 10 || r.WaitMS != 60_000 {
		t.Fatalf("10th: %+v", r)
	}
	again.pass(time.Minute)
	if r, _ := again.VerifyLockPIN("2468", 0); !r.OK {
		t.Fatalf("right PIN after the wait: %+v", r)
	}
	if st := again.LockStatus(); st.Failures != 0 || st.WaitMS != 0 {
		t.Fatalf("not reset: %+v", st)
	}
}

func TestTokens(t *testing.T) {
	ls := openLock(t, "")
	ls.SetLockPIN("", "2468")
	r, _ := ls.VerifyLockPIN("2468", 42)
	ls.lk.mu.Lock()
	tok, age, err := ls.checkToken(r.Token)
	ls.lk.mu.Unlock()
	if err != nil || tok.Challenge != 42 || tok.Type != AuthPIN || tok.User == 0 || age != 0 {
		t.Fatalf("token: %+v %v %v", tok, age, err)
	}
	raw := []byte(r.Token)
	raw[10] ^= 1
	ls.lk.mu.Lock()
	_, _, err = ls.checkToken(string(raw))
	ls.lk.mu.Unlock()
	if err == nil {
		t.Fatal("a tampered token passed")
	}
	other := openLock(t, ls.dir)
	other.lk.mu.Lock()
	_, _, err = other.checkToken(r.Token)
	other.lk.mu.Unlock()
	if !errors.Is(err, ErrBadToken) {
		t.Fatal("a token outlived a restart")
	}
}

func TestEnrolNeedsAFreshPINToken(t *testing.T) {
	ls := openLock(t, "")
	if _, err := ls.EnrollChallenge(); !errors.Is(err, ErrNoPIN) {
		t.Fatalf("challenge without a PIN: %v", err)
	}
	ls.SetLockPIN("", "2468")
	r, _ := ls.VerifyLockPIN("2468", 0)
	if _, err := ls.EnrollBegin(r.Token, "Thumb"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("a token for no challenge: %v", err)
	}
	ch, _ := ls.EnrollChallenge()
	r, _ = ls.VerifyLockPIN("2468", ch)
	ls.pass(11 * time.Minute)
	if _, err := ls.EnrollBegin(r.Token, "Thumb"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("an old token: %v", err)
	}
	f := ls.enrol(t, "2468", "thumb", "Thumb")
	if f.ID != 1 || f.Name != "Thumb" {
		t.Fatalf("finger %+v", f)
	}
	raw, _ := os.ReadFile(filepath.Join(ls.dir, "keystore", "fingerprints.json"))
	if !strings.Contains(string(raw), `"Thumb"`) {
		t.Fatalf("fingerprints.json: %s", raw)
	}
}

func TestEnrolRejectsOtherAndEnrolledFingers(t *testing.T) {
	ls := openLock(t, "")
	ls.SetLockPIN("", "2468")
	ls.enrol(t, "2468", "thumb", "Thumb")
	ch, _ := ls.EnrollChallenge()
	r, _ := ls.VerifyLockPIN("2468", ch)
	if _, err := ls.EnrollBegin(r.Token, "Index"); err != nil {
		t.Fatal(err)
	}
	ls.sim.Touch("thumb")
	if st, _ := ls.EnrollNext(context.Background()); st.Rejected != fingerprint.RejectEnrolled || st.Done != 0 {
		t.Fatalf("enrolled finger: %+v", st)
	}
	ls.sim.Touch("index")
	ls.EnrollNext(context.Background())
	ls.sim.Touch("ring")
	if st, _ := ls.EnrollNext(context.Background()); st.Rejected != fingerprint.RejectOtherFinger || st.Done != 1 {
		t.Fatalf("other finger: %+v", st)
	}
	ls.CancelSensor()
	if _, err := ls.EnrollNext(context.Background()); !errors.Is(err, ErrNoEnrolment) {
		t.Fatalf("after cancel: %v", err)
	}
}

func TestAtMostFiveFingers(t *testing.T) {
	ls := openLock(t, "")
	ls.SetLockPIN("", "2468")
	for i, f := range []string{"a", "b", "c", "d", "e"} {
		if got := ls.enrol(t, "2468", f, f); got.ID != i+1 {
			t.Fatalf("finger %d: %+v", i, got)
		}
	}
	ch, _ := ls.EnrollChallenge()
	r, _ := ls.VerifyLockPIN("2468", ch)
	if _, err := ls.EnrollBegin(r.Token, "f"); !errors.Is(err, ErrTooManyFingers) {
		t.Fatalf("sixth: %v", err)
	}
	if err := ls.RenameFinger(3, "Left thumb"); err != nil {
		t.Fatal(err)
	}
	if err := ls.RenameFinger(3, ""); !errors.Is(err, ErrFingerName) {
		t.Fatalf("empty name: %v", err)
	}
	if err := ls.RemoveFinger("x", 3); err == nil {
		t.Fatal("removed without a token")
	}
	if err := ls.RemoveFinger(r.Token, 3); err != nil {
		t.Fatal(err)
	}
	st := ls.LockStatus()
	if len(st.Fingers) != 4 || st.Fingers[2].ID != 4 {
		t.Fatalf("after removing: %+v", st.Fingers)
	}
}

func TestMatchRules(t *testing.T) {
	ls := openLock(t, "")
	ls.SetLockPIN("", "2468")
	ls.enrol(t, "2468", "thumb", "Thumb")
	if r := ls.touch(t, "thumb"); !r.Match || r.Finger != 1 || r.Token == "" {
		t.Fatalf("enrolled finger: %+v", r)
	}
	for i := 1; i <= FingerFailLimit; i++ {
		r := ls.touch(t, "stranger")
		if r.Match || r.Failures != i || r.Disabled != (i == FingerFailLimit) {
			t.Fatalf("stranger %d: %+v", i, r)
		}
	}
	ls.sim.Touch("thumb")
	if _, err := ls.Authenticate(context.Background(), "", ""); !errors.Is(err, ErrPINRequired) || !strings.HasSuffix(err.Error(), BlockedLockout) {
		t.Fatalf("after five failures: %v", err)
	}
	ls.VerifyLockPIN("2468", 0)
	if r := ls.touch(t, "thumb"); !r.Match {
		t.Fatalf("after the PIN: %+v", r)
	}
	ls.pass(PINEvery + time.Minute)
	if st := ls.LockStatus(); st.Blocked != BlockedTimeout {
		t.Fatalf("48 hours later: %+v", st)
	}
	ls.VerifyLockPIN("2468", 0)
	restarted := openLock(t, ls.dir)
	if st := restarted.LockStatus(); st.Blocked != BlockedRestart || len(st.Fingers) != 1 {
		t.Fatalf("after a restart: %+v", st)
	}
	restarted.VerifyLockPIN("2468", 0)
	if r := restarted.touch(t, "thumb"); !r.Match {
		t.Fatalf("the template did not survive the restart: %+v", r)
	}
}

func TestRemovingThePINRemovesTheFingers(t *testing.T) {
	ls := openLock(t, "")
	ls.SetLockPIN("", "2468")
	ls.enrol(t, "2468", "thumb", "Thumb")
	if r, _ := ls.ClearLockPIN("1111"); r.OK {
		t.Fatal("removed with a wrong PIN")
	}
	if r, err := ls.ClearLockPIN("2468"); err != nil || !r.OK {
		t.Fatalf("remove: %+v %v", r, err)
	}
	st := ls.LockStatus()
	if st.PIN || len(st.Fingers) != 0 {
		t.Fatalf("after removing: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(ls.dir, "keystore", "lock.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock.json left: %v", err)
	}
}

func TestCancelEndsAWait(t *testing.T) {
	ls := openLock(t, "")
	ls.SetLockPIN("", "2468")
	ls.enrol(t, "2468", "thumb", "Thumb")
	done := make(chan error, 1)
	go func() {
		_, err := ls.Authenticate(context.Background(), "", "")
		done <- err
	}()
	for {
		ls.lk.mu.Lock()
		waiting := ls.lk.op != nil
		ls.lk.mu.Unlock()
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	ls.CancelSensor()
	select {
	case err := <-done:
		if err == nil || !strings.HasSuffix(err.Error(), "cancelled") {
			t.Fatalf("cancelled wait: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not end")
	}
}

func TestBiometricBoundKey(t *testing.T) {
	ls := openLock(t, "")
	root, _ := LoadRootPub(RootPubPath(ls.dir))
	const app, prompt, payload = "com.phone.wallet", "Pay Morning Light Cafe ¥18.00", `{"amount":1800}`
	if err := ls.EnsureBioKey(app, "pay-bio", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ls.dir, "keystore", "apps", app, "pay-bio.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a biometric key without fingerprints")
	}
	ls.SetLockPIN("", "2468")
	ls.enrol(t, "2468", "thumb", "Thumb")
	ls.sim.Touch("thumb")
	if _, err := ls.Authenticate(context.Background(), app, "pay-bio"); !errors.Is(err, ErrNoBioKey) {
		t.Fatalf("before the PIN made the key: %v", err)
	}
	if err := ls.EnsureBioKey(app, "pay-bio", ""); err != nil {
		t.Fatal(err)
	}
	ls.CancelSensor()
	ls.sim.Touch("thumb")
	r, err := ls.Authenticate(context.Background(), app, "pay-bio")
	if err != nil || !r.Match {
		t.Fatalf("match: %+v %v", r, err)
	}
	p, err := ls.ConfirmBio(app, "pay-bio", "", prompt, payload, r.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Bio || !p.UserAuth {
		t.Fatalf("proof %+v", p)
	}
	if err := VerifyConfirm(root, p, app, "pay-bio", prompt, payload); err != nil {
		t.Fatalf("the server rejects it: %v", err)
	}
	if _, err := ls.ConfirmBio(app, "pay-bio", "", prompt, payload, r.Token); !errors.Is(err, ErrBadToken) {
		t.Fatalf("a token used twice: %v", err)
	}
	q := *p
	q.Bio = false
	if VerifyConfirm(root, &q, app, "pay-bio", prompt, payload) == nil {
		t.Fatal("a biometric key passed for a PIN key")
	}
	if _, _, err := ls.Confirm(app, "pay-bio", "", prompt, payload, DefaultPIN); err == nil {
		t.Fatal("the payment PIN signed with the biometric key")
	}
	pin := ls.touch(t, "thumb")
	if _, err := ls.ConfirmBio(app, "pay-bio", "", prompt, payload, pin.Token); !errors.Is(err, ErrBadToken) {
		t.Fatalf("an unlock's token signed: %v", err)
	}

	ls.enrol(t, "2468", "index", "Index")
	ls.sim.Touch("thumb")
	if _, err := ls.Authenticate(context.Background(), app, "pay-bio"); !errors.Is(err, ErrKeyInvalidated) {
		t.Fatalf("after another finger was enrolled: %v", err)
	}
	ls.CancelSensor()
	if err := ls.EnsureBioKey(app, "pay-bio", ""); err != nil {
		t.Fatal(err)
	}
	ls.sim.Touch("index")
	r, err = ls.Authenticate(context.Background(), app, "pay-bio")
	if err != nil || !r.Match {
		t.Fatalf("the new key: %+v %v", r, err)
	}
	p2, err := ls.ConfirmBio(app, "pay-bio", "", prompt, payload, r.Token)
	if err != nil || p2.AppPub == p.AppPub {
		t.Fatalf("not a new key: %v", err)
	}
	if err := VerifyConfirm(root, p2, app, "pay-bio", prompt, payload); err != nil {
		t.Fatal(err)
	}
}

func TestBiometricKeyExpiresWithItsToken(t *testing.T) {
	ls := openLock(t, "")
	ls.SetLockPIN("", "2468")
	ls.enrol(t, "2468", "thumb", "Thumb")
	ls.EnsureBioKey("com.phone.wallet", "pay-bio", "sm2")
	ls.sim.Touch("thumb")
	r, err := ls.Authenticate(context.Background(), "com.phone.wallet", "pay-bio")
	if err != nil || !r.Match {
		t.Fatalf("%+v %v", r, err)
	}
	ls.pass(bioTokenFor + time.Second)
	if _, err := ls.ConfirmBio("com.phone.wallet", "pay-bio", "sm2", "x", "y", r.Token); !errors.Is(err, ErrBadToken) {
		t.Fatalf("an old token: %v", err)
	}
}
