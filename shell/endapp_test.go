package shell

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/jsrt"
	"github.com/kario-os/kario/jsrt/nativetest"
	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

// The storage app of jsrt/testdata under kario-jsrt in a Shell of the
// hostile-app rig, its fsyncs waiting while the test holds the writes
// (testdata/slowfs, holdWrites): what it saves is acknowledged at once (a
// toast) and written on a thread of its own, so an app ended while the
// write is under way loses it unless the Shell waits for it.
const storeID = "test.store"

func storeRig(t *testing.T, apps map[string]string) *hostileRig {
	t.Helper()
	bin := jsrtBin(t)
	r := newHostileRig(t, apps)
	if err := os.Symlink(bin, filepath.Join(r.c.cfg.AppBinDir, jsrt.Program)); err != nil {
		t.Fatal(err)
	}
	r.c.cfg.AppEnv = append(r.c.cfg.AppEnv, "LD_PRELOAD="+nativetest.SlowFS(t), "SLOWFS_GATE="+r.writesGate())
	t.Cleanup(r.releaseWrites)
	return r
}

// writesGate is the file that holds the storage app's fsyncs while it
// exists (SLOWFS_GATE).
func (r *hostileRig) writesGate() string { return filepath.Join(r.runDir, "slowfs.gate") }

func (r *hostileRig) holdWrites() {
	r.t.Helper()
	if err := os.WriteFile(r.writesGate(), nil, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *hostileRig) releaseWrites() {
	if err := os.Remove(r.writesGate()); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.t.Error(err)
	}
}

func storageMain(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "jsrt", "testdata", "storage", "main.js"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// systemStoreRig has the storage app in the system image.
func systemStoreRig(t *testing.T) *hostileRig {
	r := storeRig(t, map[string]string{storeID: `,"runtime":"js"`})
	if err := os.WriteFile(filepath.Join(r.app(storeID).M.dir, "main.js"), []byte(storageMain(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	return r
}

// install installs (or updates to) version of the storage app from a
// development store the Shell trusts.
func (r *hostileRig) install(id, version string) *App {
	t := r.t
	t.Helper()
	files := map[string]string{
		"manifest.json": `{"id": "` + id + `", "name": "Store", "runtime": "js", "version": "` + version + `"}`,
		"main.js":       storageMain(t),
	}
	raw, key := devPackage(t, filepath.Join(filepath.Dir(r.state), "devstore"), files)
	r.c.settings.DeveloperMode = true
	if _, err := r.c.devTrust(key); err != nil {
		t.Fatal(err)
	}
	p, err := pkg.Open(raw, r.c.storeRoots())
	if err != nil {
		t.Fatal(err)
	}
	m, err := r.c.checkPackage(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.c.unpack(p, m, r.c.appByID(id)); err != nil {
		t.Fatal(err)
	}
	return r.app(id)
}

// open starts the app on the screen and waits for its page.
func (r *hostileRig) open(id string) *App {
	r.t.Helper()
	a := r.launch(id)
	if !r.until(10*time.Second, func() bool { return a.top() != nil && a.top().S.ID == "main" }) {
		r.t.Fatalf("%s shows no page", id)
	}
	return a
}

// save stores v and waits for the app to acknowledge it; the write may
// still be under way.
func (r *hostileRig) save(a *App, v string) {
	r.t.Helper()
	a.send(ui.Msg{T: ui.MsgAction, ScreenID: "main", Action: "save", Value: v})
	if !r.until(10*time.Second, func() bool { return r.c.toast != nil && r.c.toast.body == "saved "+v }) {
		r.t.Fatalf("%s did not acknowledge saving %q", a.M.ID, v)
	}
}

// stored is the value the app's storage file holds ("" without one).
func (r *hostileRig) stored(id string) string {
	r.t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.state, "apps", id, jsrt.StorageFile))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		r.t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		r.t.Fatalf("a torn storage file: %v\n%s", err, raw)
	}
	var v string
	_ = json.Unmarshal([]byte(m["v"]), &v)
	return v
}

// saved stores v, waits for its write, then holds the writes and stores
// next: when this returns, the write of next is under way, and stays so
// until releaseWrites.
func (r *hostileRig) saved(a *App, v, next string) {
	r.t.Helper()
	r.save(a, v)
	if !r.until(10*time.Second, func() bool { return r.stored(a.M.ID) == v }) {
		r.t.Fatalf("storage %q, want %q", r.stored(a.M.ID), v)
	}
	r.holdWrites()
	r.save(a, next)
	if got := r.stored(a.M.ID); got != v {
		r.t.Fatalf("the write of %q is done already (storage %q): fsync is not held", next, got)
	}
}

// shellLog is what the Shell logged so far.
func (r *hostileRig) shellLog() string {
	raw, _ := os.ReadFile(filepath.Join(r.runDir, "shell.log"))
	return string(raw)
}

// exit is how the app's last process ended, as the Shell logged it.
func (r *hostileRig) exit(id string) string {
	exit := ""
	for _, l := range strings.Split(r.shellLog(), "\n") {
		if _, rest, ok := strings.Cut(l, "app "+id+" exited: "); ok {
			exit = rest
		}
	}
	return exit
}

// endedOnItsOwn waits for a to end and fails unless it ended by itself
// (the Shell did not kill it).
func (r *hostileRig) endedOnItsOwn(a *App, within time.Duration) {
	t := r.t
	t.Helper()
	if !r.ended(a, within) {
		t.Fatalf("%s still running %v after it was asked to end", a.M.ID, within)
	}
	if exit := r.exit(a.M.ID); exit == "signal: killed" || strings.Contains(r.shellLog(), a.M.ID+": still running") {
		t.Errorf("%s was killed (exit %q), not left to end", a.M.ID, exit)
	}
}

// finishedWriting keeps the write of a, asked to end while the write is
// held (saved), under way for hold, then lets it finish and waits for a to
// end by itself; it fails unless the loop went on meanwhile: it turned
// while the process was still there, never standing still for long.
func (r *hostileRig) finishedWriting(a *App, hold time.Duration) {
	t := r.t
	t.Helper()
	pid, turns := a.proc.Pid, 0
	r.loop = nil
	alive := func() bool {
		if processAlive(pid) {
			turns++
		}
		return !a.running
	}
	r.until(hold, alive)
	r.releaseWrites()
	r.until(10*time.Second, alive)
	r.endedOnItsOwn(a, 0)
	if turns < 3 {
		t.Errorf("the loop turned %d times while %s finished its write", turns, a.M.ID)
	}
	if p := quantile(r.loop, 1); p > maxLoopLatency {
		t.Errorf("the loop stood still for %v while %s ended", p, a.M.ID)
	}
}

// The low-memory killer asks its victim to end: the save it acknowledged
// is on disk when it is gone, and it keeps its tombstone. Killed at once,
// the file kept the save before.
func TestLowMemoryKillLetsTheWriteFinish(t *testing.T) {
	r := systemStoreRig(t)
	a := r.open(storeID)
	r.saved(a, "a", "b")
	r.c.setForeground(nil)
	r.pump(50 * time.Millisecond)
	t0 := time.Now()
	r.c.SimulateLowMemory(1)
	r.c.checkMemory()
	if took := time.Since(t0); took > 100*time.Millisecond || r.c.LowMemoryKills() != 1 {
		t.Fatalf("the low-memory check took %v, %d kills", took, r.c.LowMemoryKills())
	}
	// Slow flash: the write goes on half a second after the ask, inside
	// the killer's short grace.
	r.finishedWriting(a, 500*time.Millisecond)
	if got := r.stored(storeID); got != "b" {
		t.Fatalf("storage %q after the low-memory kill, want the acknowledged %q", got, "b")
	}
	if a.tomb == nil {
		t.Fatal("no tombstone after the low-memory kill")
	}
	if ended, itself := r.c.EndedItself(storeID); !ended || !itself {
		t.Fatalf("EndedItself = %v, %v", ended, itself)
	}
}

// A frozen app is thawed before it is asked to end (stopped, it could not
// take the end in), and finishes its write.
func TestFrozenAppIsThawedToEnd(t *testing.T) {
	r := systemStoreRig(t)
	a := r.open(storeID)
	r.saved(a, "a", "b")
	r.c.setForeground(nil)
	r.pump(50 * time.Millisecond)
	if err := r.c.freezeApp(a, true); err != nil {
		t.Fatal(err)
	}
	a.frozen = true
	if !r.until(time.Second, func() bool { return r.c.ProcState(storeID) == "frozen" }) {
		t.Fatalf("the app is %s, not frozen", r.c.ProcState(storeID))
	}
	if err := r.c.EndInBackground(storeID); err != nil {
		t.Fatal(err)
	}
	r.finishedWriting(a, 500*time.Millisecond)
	if got := r.stored(storeID); got != "b" {
		t.Fatalf("storage %q after ending the frozen app, want %q", got, "b")
	}
}

// An update asks the old version to end: the new one is in place at once,
// the loop goes on, and the old one's acknowledged save is on disk when it
// is gone, for the new version to read.
func TestUpdateLetsTheWriteFinish(t *testing.T) {
	const id = "com.example.store"
	r := storeRig(t, map[string]string{})
	r.install(id, "1.0")
	a := r.open(id)
	r.saved(a, "a", "b")
	old := a.proc.Pid
	t0 := time.Now()
	r.install(id, "2.0")
	if took := time.Since(t0); took > time.Second || a.M.Version != "2.0" {
		t.Fatalf("the update took %v, version %s in place", took, a.M.Version)
	}
	if !processAlive(old) {
		t.Fatal("the old version was gone before its write was done")
	}
	// Seconds of slow flash, inside the end grace.
	r.finishedWriting(a, 1500*time.Millisecond)
	if got := r.stored(id); got != "b" {
		t.Fatalf("storage %q after the update, want the acknowledged %q", got, "b")
	}
	a = r.open(id)
	if a.proc.Pid == old {
		t.Fatal("the old process again")
	}
}

// Uninstalling asks the app to end too; its files go at once and its
// write in flight leaves nothing behind.
func TestUninstallAsksTheAppToEnd(t *testing.T) {
	const id = "com.example.store"
	r := storeRig(t, map[string]string{})
	r.install(id, "1.0")
	a := r.open(id)
	r.saved(a, "a", "b")
	data, pkgDir := filepath.Join(r.state, "apps", id), a.M.dir
	r.c.removeApp(a)
	gone := func() bool {
		_, e1 := os.Stat(data)
		_, e2 := os.Stat(pkgDir)
		return errors.Is(e1, os.ErrNotExist) && errors.Is(e2, os.ErrNotExist)
	}
	if !gone() || r.c.appByID(id) != nil {
		t.Fatal("the app's files or entry outlived the uninstall")
	}
	r.finishedWriting(a, 1500*time.Millisecond)
	r.pump(200 * time.Millisecond)
	if !gone() {
		t.Fatal("the app's last write left files behind")
	}
}

// A push for an app that is ending waits for its next process, which the
// Shell starts in the background once the old one is gone, instead of
// going to the one that no longer reads.
func TestPushWhileEndingWakesTheNextProcess(t *testing.T) {
	r := systemStoreRig(t)
	a := r.open(storeID)
	r.saved(a, "a", "b")
	r.c.setForeground(nil)
	r.pump(50 * time.Millisecond)
	if err := r.c.EndInBackground(storeID); err != nil {
		t.Fatal(err)
	}
	old := a.proc.Pid
	ev := ui.Msg{T: ui.MsgEvent, Event: ui.EventPush, Value: `{"id":"p1"}`}
	r.c.deliverBackground(a, ev, pushWakeBudget, false, "push p1")
	r.releaseWrites()
	if !r.until(10*time.Second, func() bool { return a.running && a.proc.Pid != old && a.ready && len(a.pushQ) == 0 }) {
		t.Fatalf("no new process took the push: running %v, %d queued; log:\n%s", a.running, len(a.pushQ), r.shellLog())
	}
	if r.c.fg == a {
		t.Fatal("the push brought the app to the screen")
	}
}

// Network given back to an app that had none restarts it the usual way;
// taken away, the app is killed at once: a grace would leave it the
// network a little longer.
func TestNetworkChangeRestart(t *testing.T) {
	r := systemStoreRig(t)
	sys := settingsApp()
	r.c.broker.perms[storeID] = map[string]string{ui.PermNet: "denied"}
	set := func(state string) {
		t.Helper()
		args, _ := json.Marshal(ui.PermSetArgs{App: storeID, Perm: ui.PermNet, State: state})
		r.c.broker.handle(sys, ui.Msg{T: ui.MsgReq, Req: 1, Cap: ui.CapPermSet, Args: args})
		if m := <-sys.out; m.m.Err != "" {
			t.Fatalf("perm.set %s: %s", state, m.m.Err)
		}
	}
	a := r.open(storeID)
	set("granted")
	r.endedOnItsOwn(a, 5*time.Second)
	a = r.open(storeID)
	set("denied")
	if !r.ended(a, 5*time.Second) {
		t.Fatal("still running with its network taken away")
	}
	if exit := r.exit(storeID); exit != "signal: killed" {
		t.Fatalf("exit %q with its network taken away, want killed", exit)
	}
}
