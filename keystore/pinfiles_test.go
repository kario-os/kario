package keystore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/kario-os/kario/statefile"
)

// cut cuts each of the files there to half, as a write cut short by a power
// cut left lock.json before it was written whole; the first must be there.
func cut(t *testing.T, paths ...string) {
	t.Helper()
	for i, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			if i == 0 {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, raw[:len(raw)/2], 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func pinStore(t *testing.T) *lockStore {
	t.Helper()
	ls := openLock(t, "")
	if r, err := ls.SetLockPIN("", "2468"); err != nil || !r.OK {
		t.Fatalf("set the PIN: %+v %v", r, err)
	}
	if r, err := ls.SetParentPIN("", "1357"); err != nil || !r.OK {
		t.Fatalf("set the parental PIN: %+v %v", r, err)
	}
	return ls
}

func wholeJSON(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || !json.Valid(raw) {
		t.Fatalf("%s is not whole: %q %v", path, raw, err)
	}
}

// phone-keystored's lock.json and parent.json cut short, or lock.json gone
// with its copy left: the store opens from the copies kept, the PINs work
// and the files are whole again. Before, Open failed and phone-keystored
// did not start, so the lock screen could not be unlocked.
func TestPINFilesFromTheirCopies(t *testing.T) {
	ls := pinStore(t)
	lock, parent := LockPath(ls.dir), ParentPath(ls.dir)
	cut(t, lock)
	cut(t, parent)
	again := openLock(t, ls.dir)
	if st := again.LockStatus(); !st.PIN {
		t.Fatalf("lock status: %+v", st)
	}
	if r, err := again.VerifyLockPIN("2468", 0); err != nil || !r.OK {
		t.Fatalf("the PIN: %+v %v", r, err)
	}
	if r, err := again.VerifyParentPIN("1357"); err != nil || !r.OK {
		t.Fatalf("the parental PIN: %+v %v", r, err)
	}
	wholeJSON(t, lock)
	wholeJSON(t, parent)
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if !LockPINSet(ls.dir) {
		t.Fatal("the PIN's copy left: no PIN set")
	}
	again = openLock(t, ls.dir)
	if r, err := again.VerifyLockPIN("2468", 0); err != nil || !r.OK {
		t.Fatalf("the PIN with lock.json gone: %+v %v", r, err)
	}
	wholeJSON(t, lock)
}

// lock.json and parent.json and their copies all damaged: phone-keystored
// opens all the same; the phone keeps a PIN that no PIN matches, so it
// stays locked, and minor mode stays on. Nothing is written over the
// damaged files, the fingerprints do not stand in, and only a parent's
// account reset replaces the parental PIN. Before, Open failed.
func TestPINFilesNeitherWholeStayLocked(t *testing.T) {
	ls := pinStore(t)
	ls.enrol(t, "2468", "thumb", "Thumb")
	lock, parent := LockPath(ls.dir), ParentPath(ls.dir)
	cut(t, lock, lock+statefile.Bak)
	cut(t, parent, parent+statefile.Bak)
	files := map[string][]byte{}
	for _, p := range []string{lock, lock + statefile.Bak, parent, parent + statefile.Bak} {
		files[p], _ = os.ReadFile(p)
	}
	again := openLock(t, ls.dir)
	if st := again.LockStatus(); !st.PIN || !st.Damaged {
		t.Fatalf("lock status: %+v", st)
	}
	if st := again.ParentStatus(); !st.PIN || !st.Damaged {
		t.Fatalf("parental status: %+v", st)
	}
	if len(again.Damage()) != 2 {
		t.Fatalf("damage noted: %q", again.Damage())
	}
	if r, err := again.VerifyLockPIN("2468", 0); r.OK || !errors.Is(err, ErrLockDamaged) {
		t.Fatalf("the PIN: %+v %v", r, err)
	}
	if r, err := again.SetLockPIN("", "1111"); r.OK || !errors.Is(err, ErrLockDamaged) {
		t.Fatalf("a new PIN without the old: %+v %v", r, err)
	}
	if r, err := again.ClearLockPIN("2468"); r.OK || !errors.Is(err, ErrLockDamaged) {
		t.Fatalf("removing it: %+v %v", r, err)
	}
	again.press("thumb")
	if r, err := again.Authenticate(context.Background(), "", ""); r.Match || err == nil {
		t.Fatalf("a fingerprint: %+v %v", r, err)
	}
	if r, err := again.VerifyParentPIN("1357"); r.OK || !errors.Is(err, ErrParentDamaged) {
		t.Fatalf("the parental PIN: %+v %v", r, err)
	}
	if r, err := again.SetParentPIN("", "1111"); r.OK || !errors.Is(err, ErrParentDamaged) {
		t.Fatalf("a new parental PIN without the old: %+v %v", r, err)
	}
	if r, err := again.ClearParentPIN("1357"); r.OK || !errors.Is(err, ErrParentDamaged) {
		t.Fatalf("removing the parental PIN: %+v %v", r, err)
	}
	for p, raw := range files {
		if now, _ := os.ReadFile(p); !bytes.Equal(now, raw) {
			t.Errorf("%s written over: %q", p, now)
		}
	}
	if !LockPINSet(ls.dir) || !ParentPINSet(ls.dir) {
		t.Fatal("the Shell would start with no PIN set")
	}
	if r, err := again.ResetParentPIN("2580"); err != nil || !r.OK {
		t.Fatalf("a parent's account reset: %+v %v", r, err)
	}
	if r, err := again.VerifyParentPIN("2580"); err != nil || !r.OK {
		t.Fatalf("the reset parental PIN: %+v %v", r, err)
	}
	wholeJSON(t, parent)
	wholeJSON(t, parent+statefile.Bak)
	third := openLock(t, ls.dir)
	if st := third.LockStatus(); !st.PIN || !st.Damaged {
		t.Fatalf("lock status after a restart: %+v", st)
	}
	if st := third.ParentStatus(); !st.PIN || st.Damaged {
		t.Fatalf("parental status after the reset: %+v", st)
	}
}

// A phone whose files the code before the copies wrote (no .bak) opens
// as before, and the next write keeps a copy.
func TestPINFilesWithoutCopies(t *testing.T) {
	ls := pinStore(t)
	for _, p := range []string{LockPath(ls.dir), ParentPath(ls.dir)} {
		if err := os.Remove(p + statefile.Bak); err != nil {
			t.Fatal(err)
		}
	}
	again := openLock(t, ls.dir)
	if st := again.LockStatus(); !st.PIN || st.Damaged || len(again.Damage()) != 0 {
		t.Fatalf("lock status: %+v, %q", st, again.Damage())
	}
	if r, _ := again.VerifyLockPIN("0000", 0); r.OK || r.Failures != 1 {
		t.Fatalf("a wrong PIN: %+v", r)
	}
	wholeJSON(t, LockPath(ls.dir)+statefile.Bak)
	if r, _ := again.VerifyParentPIN("1357"); !r.OK {
		t.Fatalf("the parental PIN: %+v", r)
	}
	if r, _ := again.ClearLockPIN("2468"); !r.OK {
		t.Fatalf("removing the PIN: %+v", r)
	}
	if LockPINSet(ls.dir) {
		t.Fatal("a PIN set after it was removed")
	}
}
