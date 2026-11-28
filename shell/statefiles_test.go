package shell

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/statefile"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wipe"
	"github.com/kario-os/kario/wire"
)

// truncate cuts the file to half, as a write cut short by a power cut or a
// kill left it before state files were written whole.
func truncate(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw[:len(raw)/2], 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	raw, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// startPhone starts a Shell on root's state as the phone's next start does,
// with nothing answering for phone-keystored (it has not started yet, or
// cannot).
func startPhone(t *testing.T, root string) (*Core, *bytes.Buffer) {
	t.Helper()
	run, err := os.MkdirTemp("", "statefiles-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	app := filepath.Join(root, "apps", "test.still")
	_ = os.MkdirAll(app, 0o755)
	if err := os.WriteFile(filepath.Join(app, "manifest.json"), []byte(`{"id":"test.still","name":"Still","exec":"none","permissions":["net"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	c, err := New(Config{W: 40, H: 24, AppsDir: filepath.Join(root, "apps"), AppBinDir: filepath.Join(root, "bin"), RunDir: run,
		StateDir: filepath.Join(root, "state"), FreezeAfter: -1, Log: &logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Shutdown)
	return c, &logs
}

// A phone with a lock-screen PIN starts locked however lock.json was lost:
// cut short, removed, or written again without the PIN. Before, each
// started unlocked until phone-keystored answered, and stayed unlocked
// when it did not. Without a PIN a damaged lock.json starts unlocked, as
// before, and says so in the log.
func TestLockStartsLockedWithLockFileDamaged(t *testing.T) {
	for _, damage := range []string{"cut short", "removed", "rewritten"} {
		t.Run(damage, func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "state")
			ks, err := keystore.Open(state)
			if err != nil {
				t.Fatal(err)
			}
			if r, err := ks.SetLockPIN("", "2468"); err != nil || !r.OK {
				t.Fatalf("set PIN: %+v, %v", r, err)
			}
			lockFile := filepath.Join(state, "shell", "lock.json")
			writeJSONFile(t, lockFile, lockPrefs{Grace: 30, FingerUnlock: true, Secure: true})
			switch damage {
			case "cut short":
				truncate(t, lockFile)
			case "removed":
				_ = os.Remove(lockFile)
			case "rewritten":
				writeJSONFile(t, lockFile, lockPrefs{FingerUnlock: true})
			}
			c, _ := startPhone(t, root)
			if !c.Locked() || !c.secureLocked() {
				t.Fatalf("lock.json %s: locked %v, secure %v", damage, c.Locked(), c.secureLocked())
			}
		})
	}
	root := t.TempDir()
	lockFile := filepath.Join(root, "state", "shell", "lock.json")
	writeJSONFile(t, lockFile, lockPrefs{FingerUnlock: true})
	truncate(t, lockFile)
	c, logs := startPhone(t, root)
	if c.Locked() {
		t.Fatal("no PIN: started locked")
	}
	if !strings.Contains(logs.String(), "state: "+lockFile+": damaged") {
		t.Fatalf("not in the log:\n%s", logs)
	}
}

// phone-keystored's record of the lock-screen PIN damaged with its copy:
// phone-keystored starts, the phone starts locked and stays so (no PIN
// unlocks it), the lock screen says so and offers at once the erase a
// forgotten PIN gets after forgotAfter wrong ones, and the erase takes the
// record with the data. Before, phone-keystored did not start.
func TestLockWithThePINsRecordDamaged(t *testing.T) {
	rt := newResetTest(t)
	c := rt.c
	ks, err := keystore.Open(c.cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := ks.SetLockPIN("", "2468"); err != nil || !r.OK {
		t.Fatalf("set PIN: %+v, %v", r, err)
	}
	lock := keystore.LockPath(c.cfg.StateDir)
	for _, p := range []string{lock, lock + statefile.Bak} {
		if _, err := os.Stat(p); err == nil {
			truncate(t, p)
		}
	}
	if ks, err = keystore.Open(c.cfg.StateDir); err != nil {
		t.Fatalf("phone-keystored does not start: %v", err)
	}
	c.cfg.RunDir = t.TempDir()
	c.broker.clients = map[string]*wire.Client{}
	go wire.Serve(filepath.Join(c.cfg.RunDir, svc.KeystoredSock), func(method string, params json.RawMessage) (any, error) {
		var p struct{ PIN string }
		_ = json.Unmarshal(params, &p)
		switch method {
		case keystore.MethodLockStatus:
			return ks.LockStatus(), nil
		case keystore.MethodLockVerify:
			return ks.VerifyLockPIN(p.PIN, 0)
		}
		return nil, errors.New("unknown method " + method)
	})
	c.startLock()
	rt.pump("phone-keystored's answer", func() bool { return c.lock.heard })
	if !c.secureLocked() || !c.lock.st.Damaged {
		t.Fatalf("locked %v, secure %v, %+v", c.Locked(), c.secureLocked(), c.lock.st)
	}
	damaged := tr.T("The lock-screen PIN's record is damaged: no PIN unlocks this phone.")
	c.openLockPad("", false)
	pad, _ := overlayOf[*lockPadOverlay](c)
	if lines, _, _ := pad.lines(c); !slices.Contains(lines, damaged) || !slices.Contains(lines, tr.T("# erases this phone and everything on it.")) {
		t.Fatalf("the pad says %q", lines)
	}
	rt.pin(pad, "2468")
	rt.pump("the key service", func() bool { return !pad.busy })
	if !c.secureLocked() || pad.note != damaged {
		t.Fatalf("the PIN: locked %v, %q", c.secureLocked(), pad.note)
	}
	pad.key(c, Runes("#"))
	o := rt.confirmShown()
	if !strings.Contains(o.text, tr.T("Erase this phone without its PIN?")) {
		t.Fatalf("confirmation %q", o.text)
	}
	o.key(c, Named(KeyEnter))
	rt.pump("the restart", func() bool { return rt.restarts == 1 })
	if wipe.Why(c.cfg.PersistDir) != wipe.FromLockScreen {
		t.Fatalf("request %q", wipe.Why(c.cfg.PersistDir))
	}
	rt.boot()
	if keystore.LockPINSet(c.cfg.StateDir) {
		t.Fatal("the damaged record outlived the erase")
	}
}

// installStoreApp puts an app installed from the store (or a computer)
// into the state.
func installStoreApp(t *testing.T, state, id string, perms ...string) {
	t.Helper()
	m, _ := json.Marshal(map[string]any{"id": id, "name": id, "exec": "none", "permissions": perms})
	dir := filepath.Join(state, "packages", id)
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), m, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A damaged perms.json grants nothing. Before, the Shell did not start at
// all. With the copy kept, its refusals stay and its grants do not; with
// neither file whole, installed apps lose the network and notifications as
// well, the system's own apps keep theirs.
func TestGrantsFromADamagedFile(t *testing.T) {
	for _, copyKept := range []bool{true, false} {
		t.Run(map[bool]string{true: "copy kept", false: "no copy"}[copyKept], func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "state")
			installStoreApp(t, state, "test.store", ui.PermCamera, ui.PermNet, ui.PermNotify)
			installStoreApp(t, state, "test.other", ui.PermNet)
			path := filepath.Join(state, "shell", "perms.json")
			raw, _ := json.Marshal(map[string]map[string]string{"test.store": {ui.PermCamera: "granted", ui.PermNotify: "denied"}, "test.other": {ui.PermNet: "wifi"}})
			if copyKept {
				if err := statefile.WriteKeep(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				writeJSONFile(t, path, json.RawMessage(raw))
			}
			truncate(t, path)
			c, _ := startPhone(t, root)
			store, other := c.appByID("test.store"), c.appByID("test.other")
			if store == nil || other == nil || !store.M.installed {
				t.Fatalf("apps %v %v", store, other)
			}
			if c.broker.holds(store, ui.PermCamera) {
				t.Error("the camera is granted")
			}
			want := map[string]map[string]string{"test.store": {ui.PermNotify: "denied"}, "test.other": {ui.PermNet: "wifi"}}
			if !copyKept {
				want = map[string]map[string]string{"test.store": {ui.PermNet: "denied", ui.PermNotify: "denied"}, "test.other": {ui.PermNet: "denied"}}
			}
			if !reflect.DeepEqual(c.broker.perms, want) {
				t.Errorf("perms %v, want %v", c.broker.perms, want)
			}
			if got := c.netState("test.still"); got != "granted" {
				t.Errorf("the system's app's network is %q", got)
			}
			var again map[string]map[string]string
			if _, err := statefile.ReadJSON(path, &again); err != nil || !reflect.DeepEqual(again, want) {
				t.Errorf("written again %v, %v", again, err)
			}
		})
	}
}

// A parent's daily limits and downtime outlive a damaged screen-time.json:
// they are read from the copy kept. Before, a damaged file read as none.
func TestScreenTimeLimitsSurviveADamagedFile(t *testing.T) {
	lt, _ := stCore(t)
	c := lt.c
	st := c.screenTime()
	st.Limits[appMessages] = 30
	st.Downtime = downtime{On: true, Start: 20 * 60, End: 7 * 60, Apps: []string{appDialer}}
	c.saveScreenTime()
	truncate(t, c.stPath())
	next, _ := stCore(t)
	next.c.cfg.StateDir = c.cfg.StateDir
	var logs bytes.Buffer
	next.c.log = log.New(&logs, "", 0)
	got := next.c.screenTime()
	if got.Limits[appMessages] != 30 || !reflect.DeepEqual(got.Downtime, st.Downtime) {
		t.Fatalf("limits %v, downtime %+v", got.Limits, got.Downtime)
	}
	if !strings.Contains(logs.String(), "read the copy kept of it") {
		t.Fatalf("not in the log:\n%s", logs.String())
	}
}

// The store's takedowns outlive a damaged revocations.json (from the copy
// kept); with neither whole, no installed app starts until the store's
// next list, the system's own still do. Before, a damaged list took
// nothing down.
func TestTakedownsSurviveADamagedFile(t *testing.T) {
	setup := func(t *testing.T) (*Core, []*App) {
		apps := []*App{{M: &Manifest{ID: "test.store", installed: true}}, {M: &Manifest{ID: "test.other", installed: true}}, {M: &Manifest{ID: "test.image"}}}
		c := &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: t.TempDir()}, apps: apps}
		raw, _ := json.Marshal(revocations{Issued: 100, Revoked: []pkg.Revoked{{App: "test.store", Reason: "malware"}}})
		if err := statefile.WriteKeep(c.revocationsPath(), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		truncate(t, c.revocationsPath())
		return c, apps
	}
	t.Run("copy kept", func(t *testing.T) {
		c, _ := setup(t)
		if !c.Revoked("test.store") || c.Revoked("test.other") || c.loadRevocations().Issued != 100 {
			t.Fatalf("store %v, other %v, %+v", c.Revoked("test.store"), c.Revoked("test.other"), c.loadRevocations())
		}
	})
	t.Run("neither whole", func(t *testing.T) {
		c, apps := setup(t)
		truncate(t, c.revocationsPath()+statefile.Bak)
		for _, a := range apps[:2] {
			err := c.verifyInstalled(a)
			if !c.Revoked(a.M.ID) || err == nil || err.Error() != tr.T("The store's list of apps taken down was lost: open the Store to fetch it again") {
				t.Fatalf("%s revoked %v, %v", a.M.ID, c.Revoked(a.M.ID), err)
			}
		}
		if c.Revoked("test.image") || c.verifyInstalled(apps[2]) != nil {
			t.Fatal("the system's own app is held")
		}
	})
}

// The anti-theft record in the persistent area and its mark in the data
// area are read from their copies when damaged, so the owner's phone is
// not locked, or locked to an unknown account; with neither copy of the
// record whole the phone still locks, as before.
func TestTheftRecordFromItsCopy(t *testing.T) {
	setup := func(t *testing.T) *Core {
		lt, _ := stCore(t)
		c := lt.c
		c.cfg.PersistDir = t.TempDir()
		c.theft.rec = &theftRecord{Account: "acct-1", Email: "o***r@example.com", Device: "dev-1", Token: "tok-1", Since: 1}
		if err := c.saveTheft(); err != nil {
			t.Fatal(err)
		}
		if err := statefile.WriteKeep(c.theftMarkPath(), []byte(`{"token":"tok-1"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		c.theft, c.overlays = theftShell{}, nil
		return c
	}
	for _, name := range []string{"record", "mark"} {
		t.Run(name+" cut short", func(t *testing.T) {
			c := setup(t)
			truncate(t, map[string]string{"record": c.theftPath(), "mark": c.theftMarkPath()}[name])
			c.startTheft()
			if c.theft.locked || !c.theft.armed || c.theft.rec.Email != "o***r@example.com" {
				t.Fatalf("locked %v, armed %v, %+v", c.theft.locked, c.theft.armed, c.theft.rec)
			}
		})
	}
	t.Run("neither whole", func(t *testing.T) {
		c := setup(t)
		truncate(t, c.theftPath())
		truncate(t, c.theftPath()+statefile.Bak)
		c.startTheft()
		if !c.theft.locked || c.theft.rec.Account != "?" {
			t.Fatalf("locked %v, %+v", c.theft.locked, c.theft.rec)
		}
	})
}

// Minor mode outlives a damaged minor.json: the parent's choices from the
// copy, or, with neither file there while phone-keystored's file of the
// parental PIN is, minor mode with the apps it allows at first. Before,
// the phone started out of minor mode until phone-keystored answered.
func TestMinorModeSurvivesADamagedFile(t *testing.T) {
	t.Run("copy kept", func(t *testing.T) {
		lt, _ := stCore(t)
		c := lt.c
		c.parental.p = &minorPolicy{On: true, Apps: []string{"com.phone.calc"}, Contacts: []minorContact{{Name: "Mum", Number: "+861"}}}
		c.saveMinor()
		truncate(t, c.minorPath())
		want := *c.parental.p
		c.parental.p = nil
		if got := c.minor(); !reflect.DeepEqual(*got, want) {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("lost with a parental PIN", func(t *testing.T) {
		lt, _ := stCore(t)
		c := lt.c
		ks, err := keystore.Open(c.cfg.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		if r, err := ks.SetParentPIN("", "1357"); err != nil || !r.OK {
			t.Fatalf("set the parental PIN: %+v, %v", r, err)
		}
		c.parental.p = nil
		if got := c.minor(); !got.On || len(got.Apps) == 0 {
			t.Fatalf("%+v", got)
		}
	})
}

// The app uids outlive a damaged uids.json; with neither whole none is
// given out (as before), rather than one that may be another app's.
func TestAppUIDsFromTheirCopy(t *testing.T) {
	c := &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: t.TempDir()}}
	a, err := c.appUID("test.a")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := c.appUID("test.b")
	path := filepath.Join(c.cfg.StateDir, "shell", "uids.json")
	truncate(t, path)
	if got := c.appUIDs(); got["test.a"] != a || got["test.b"] != b {
		t.Fatalf("for the firewall: %v", got)
	}
	if again, err := c.appUID("test.a"); err != nil || again != a {
		t.Fatalf("test.a again: %d, %v (was %d)", again, err, a)
	}
	if n, err := c.appUID("test.c"); err != nil || n == a || n == b {
		t.Fatalf("test.c: %d, %v (a %d, b %d)", n, err, a, b)
	}
	truncate(t, path)
	truncate(t, path+statefile.Bak)
	if n, err := c.appUID("test.d"); err == nil {
		t.Fatalf("neither whole: given %d", n)
	}
}
