package keystore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The parental PIN is kept apart from the lock-screen PIN: its own file
// with a slow hash only, its own count of wrong tries; changing and
// removing it need it.
func TestParentPINApartFromTheLockPIN(t *testing.T) {
	ls := openLock(t, "")
	if st := ls.ParentStatus(); st.PIN {
		t.Fatal("a new device has a parental PIN")
	}
	if _, err := ls.VerifyParentPIN("1357"); !errors.Is(err, ErrNoPIN) {
		t.Fatalf("verify without a PIN: %v", err)
	}
	if _, err := ls.SetParentPIN("", "12a4"); !errors.Is(err, ErrPINFormat) {
		t.Fatalf("a bad PIN: %v", err)
	}
	ls.SetLockPIN("", "2468")
	if r, err := ls.SetParentPIN("", "1357"); err != nil || !r.OK {
		t.Fatalf("set: %+v %v", r, err)
	}
	raw, _ := os.ReadFile(filepath.Join(ls.dir, "keystore", "parent.json"))
	// The PIN would be a JSON string of its own; its digits may well turn
	// up inside the base64 salt and hash or the random user ID.
	if strings.Contains(string(raw), `"1357"`) || !strings.Contains(string(raw), `"hash"`) {
		t.Fatalf("parent.json: %s", raw)
	}
	if r, _ := ls.VerifyParentPIN("2468"); r.OK || r.Failures != 1 || r.Left != 4 {
		t.Fatalf("the lock-screen PIN as the parental one: %+v", r)
	}
	if r, _ := ls.VerifyLockPIN("1357", 0); r.OK {
		t.Fatal("the parental PIN unlocks the phone")
	}
	if st := ls.LockStatus(); st.Failures != 1 {
		t.Fatalf("lock failures %d", st.Failures)
	}
	if r, _ := ls.VerifyParentPIN("1357"); !r.OK || r.Token != "" {
		t.Fatalf("right PIN: %+v", r)
	}
	if r, _ := ls.SetParentPIN("0000", "9753"); r.OK {
		t.Fatal("changed with a wrong PIN")
	}
	if r, _ := ls.SetParentPIN("1357", "9753"); !r.OK {
		t.Fatalf("change: %+v", r)
	}
	if r, _ := ls.ClearParentPIN("1357"); r.OK {
		t.Fatal("removed with the old PIN")
	}
	if r, _ := ls.ClearParentPIN("9753"); !r.OK {
		t.Fatalf("remove: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(ls.dir, "keystore", "parent.json")); !os.IsNotExist(err) {
		t.Fatalf("parent.json after removing: %v", err)
	}
	if !ls.LockStatus().PIN {
		t.Fatal("removing the parental PIN removed the lock-screen PIN")
	}
}

// A reset replaces the parental PIN without the old one, which stops
// working, and forgets the wrong tries and their wait; there is nothing to
// reset with minor mode off, and the lock-screen PIN is not touched.
func TestParentPINReset(t *testing.T) {
	ls := openLock(t, "")
	if _, err := ls.ResetParentPIN("2468"); !errors.Is(err, ErrNoPIN) {
		t.Fatalf("a reset without a parental PIN: %v", err)
	}
	ls.SetLockPIN("", "1111")
	ls.SetParentPIN("", "1357")
	for i := 0; i < 5; i++ {
		ls.VerifyParentPIN("0000")
	}
	if _, err := ls.ResetParentPIN("12a4"); !errors.Is(err, ErrPINFormat) {
		t.Fatalf("a bad new PIN: %v", err)
	}
	if r, err := ls.ResetParentPIN("2468"); err != nil || !r.OK {
		t.Fatalf("reset: %+v %v", r, err)
	}
	if st := ls.ParentStatus(); !st.PIN || st.Failures != 0 || st.WaitMS != 0 {
		t.Fatalf("after the reset: %+v", st)
	}
	if r, _ := ls.VerifyParentPIN("1357"); r.OK {
		t.Fatal("the old PIN still works")
	}
	if r, _ := ls.VerifyParentPIN("2468"); !r.OK {
		t.Fatalf("the new PIN: %+v", r)
	}
	again := openLock(t, ls.dir)
	if r, _ := again.VerifyParentPIN("2468"); !r.OK {
		t.Fatal("the new PIN after a restart")
	}
	if r, _ := again.VerifyLockPIN("1111", 0); !r.OK {
		t.Fatal("the reset touched the lock-screen PIN")
	}
}

// Wrong parental PINs wait as wrong lock-screen PINs do, and a restart
// keeps the wait.
func TestParentPINWaitsSurviveARestart(t *testing.T) {
	ls := openLock(t, "")
	ls.SetParentPIN("", "1357")
	var r PINResult
	for i := 1; i <= 5; i++ {
		r, _ = ls.VerifyParentPIN("0000")
	}
	if r.OK || r.Failures != 5 || r.WaitMS != 30_000 {
		t.Fatalf("after 5 wrong: %+v", r)
	}
	if r, _ := ls.VerifyParentPIN("1357"); r.OK || r.WaitMS != 30_000 {
		t.Fatalf("the right PIN during the wait: %+v", r)
	}
	if r, _ := ls.ClearParentPIN("1357"); r.OK {
		t.Fatal("removed during the wait")
	}
	again := openLock(t, ls.dir)
	if st := again.ParentStatus(); !st.PIN || st.Failures != 5 || st.WaitMS != 30_000 {
		t.Fatalf("after a restart: %+v", st)
	}
	again.pass(31 * time.Second)
	if r, _ := again.VerifyParentPIN("1357"); !r.OK {
		t.Fatalf("after the wait: %+v", r)
	}
	if st := again.ParentStatus(); st.Failures != 0 || st.WaitMS != 0 {
		t.Fatalf("a right PIN keeps the count: %+v", st)
	}
}
