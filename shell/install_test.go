package shell

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

// An app's code is synced to the disk, each file (the install record too)
// and directory, before its directory is renamed over the version in
// place; a file that does not sync installs nothing and leaves the old
// version. Before, nothing was synced: a power cut soon after an install
// could leave the app's files empty under their names.
func TestInstallSyncsTheCodeBeforeTheRename(t *testing.T) {
	c := loopCore(t, t.TempDir())
	store := filepath.Join(t.TempDir(), "devstore")
	final := filepath.Join(c.cfg.StateDir, "packages", "com.example.synced")
	type seen struct {
		files []string
		// inPlace: the version the record in place had then.
		inPlace string
	}
	var synced []seen
	saved := syncTree
	t.Cleanup(func() { syncTree = saved })
	syncTree = func(dir string) error {
		s := seen{inPlace: readInstallRecord(final).Version}
		_ = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() {
				rel, _ := filepath.Rel(dir, p)
				s.files = append(s.files, filepath.ToSlash(rel))
			}
			return err
		})
		synced = append(synced, s)
		return saved(dir)
	}
	install := func(version string) error {
		raw, key := devPackage(t, store, map[string]string{
			"manifest.json": `{"id": "com.example.synced", "name": "Synced", "runtime": "js", "version": "` + version + `"}`,
			"main.js":       `phone.ui.render({});`,
			"js/util.js":    `export const x = 1;`,
		})
		return c.onLoop(func() error {
			c.settings.DeveloperMode = true
			if _, err := c.devTrust(key); err != nil {
				return err
			}
			p, err := pkg.Open(raw, c.storeRoots())
			if err != nil {
				return err
			}
			m, err := c.checkPackage(p)
			if err != nil {
				return err
			}
			return c.unpack(p, m, c.appByID(m.ID))
		})
	}
	want := []string{installRecordFile, pkg.SigPath, "js/util.js", "main.js", "manifest.json"}
	slices.Sort(want)
	for _, version := range []string{"1.0", "2.0"} {
		synced = nil
		if err := install(version); err != nil {
			t.Fatal(err)
		}
		if len(synced) != 1 {
			t.Fatalf("%s: synced %d times before the rename", version, len(synced))
		}
		slices.Sort(synced[0].files)
		if !slices.Equal(synced[0].files, want) {
			t.Errorf("%s: synced %v, want %v", version, synced[0].files, want)
		}
		if old := map[string]string{"1.0": "", "2.0": "1.0"}[version]; synced[0].inPlace != old {
			t.Errorf("%s: synced with %q in place, want %q", version, synced[0].inPlace, old)
		}
		if got := readInstallRecord(final).Version; got != version {
			t.Fatalf("installed %q, want %s", got, version)
		}
	}
	syncTree = func(string) error { return syscall.EIO }
	if err := install("3.0"); !errors.Is(err, syscall.EIO) {
		t.Fatalf("a file that does not sync: %v", err)
	}
	if got := readInstallRecord(final).Version; got != "2.0" {
		t.Fatalf("in place after the failed install: %q", got)
	}
	if ents, _ := os.ReadDir(filepath.Dir(final)); len(ents) != 1 {
		t.Fatalf("left in packages/: %v", ents)
	}
}

// cutID is the app the tests of installs cut short install.
const cutID = "com.example.cut"

// cutPhone is a phone whose image trusts a development store kept in its
// root, so the packages it signs install and start without developer
// mode.
type cutPhone struct {
	t    *testing.T
	root string
}

func newCutPhone(t *testing.T) *cutPhone {
	t.Helper()
	ph := &cutPhone{t: t, root: t.TempDir()}
	sys := filepath.Join(ph.root, "apps", "sys")
	_ = os.MkdirAll(sys, 0o755)
	if err := os.WriteFile(filepath.Join(sys, "manifest.json"), []byte(`{"id": "com.test.sys", "name": "Sys", "exec": "true"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return ph
}

func (ph *cutPhone) state() string    { return filepath.Join(ph.root, "state") }
func (ph *cutPhone) packages() string { return filepath.Join(ph.state(), "packages") }

// pkg signs a version of the app: a program that opens on a page titled
// with its version, and a few files more.
func (ph *cutPhone) pkg(version string) []byte {
	ph.t.Helper()
	hello, _ := json.Marshal(ui.Msg{T: ui.MsgHello, App: cutID, Proto: ui.Proto})
	page, _ := json.Marshal(ui.Msg{T: ui.MsgReset, Screen: &ui.Screen{ID: "main", Title: "v" + version,
		Body: []ui.Node{{Kind: ui.KindText, Text: version}}}})
	raw, _ := devPackage(ph.t, ph.root, map[string]string{
		"manifest.json": `{"id": "` + cutID + `", "name": "Cut", "exec": "app", "version": "` + version + `"}`,
		"app":           "#!/bin/sh\nprintf '%s\\n' '" + string(hello) + "' '" + string(page) + "' >&3\nexec cat <&3 >/dev/null\n",
		"lib/a.txt":     strings.Repeat("a"+version, 100),
		"lib/b.txt":     strings.Repeat("b"+version, 100),
		"res/c.txt":     strings.Repeat("c"+version, 100),
	}, "app")
	return raw
}

// start starts a Shell on the phone's state, as the phone's next start
// does; with loop, its event loop runs on a goroutine of its own.
func (ph *cutPhone) start(loop bool) (*Core, *syncBuffer) {
	t := ph.t
	t.Helper()
	run, err := os.MkdirTemp("", "installcut-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	logs := &syncBuffer{}
	c, err := New(Config{W: 40, H: 24, AppsDir: filepath.Join(ph.root, "apps"), AppBinDir: filepath.Join(ph.root, "bin"),
		RunDir: run, StateDir: ph.state(), FreezeAfter: -1, Log: logs})
	if err != nil {
		t.Fatal(err)
	}
	if loop {
		runLoop(t, c)
	} else {
		t.Cleanup(c.Shutdown)
	}
	return c, logs
}

// powerCut stops an install as the power going would.
type powerCut struct{}

// install installs raw on a Shell started on the phone's state, the power
// going after the install's n-th step (unpackStep; 0: never): nothing
// after it runs, not even an error's cleanup. It returns the steps taken.
func (ph *cutPhone) install(raw []byte, n int) (steps []string) {
	t := ph.t
	t.Helper()
	c, _ := ph.start(false)
	saved := unpackStep
	defer func() { unpackStep = saved }()
	unpackStep = func(step string) {
		steps = append(steps, step)
		if len(steps) == n {
			panic(powerCut{})
		}
	}
	defer func() {
		if r := recover(); r != nil && r != (powerCut{}) {
			panic(r)
		}
	}()
	p, err := pkg.Open(raw, c.storeRoots())
	if err != nil {
		t.Fatal(err)
	}
	m, err := c.checkPackage(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.unpack(p, m, c.appByID(m.ID)); err != nil {
		t.Fatal(err)
	}
	return steps
}

// names lists packages/.
func (ph *cutPhone) names() []string {
	ents, _ := os.ReadDir(ph.packages())
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// holding is the directory in packages/ whose install record says
// version, "" if none.
func (ph *cutPhone) holding(version string) string {
	for _, name := range ph.names() {
		if dir := filepath.Join(ph.packages(), name); readInstallRecord(dir).Version == version {
			return dir
		}
	}
	return ""
}

// unpacked is the directory an install unpacks into, "" if none.
func (ph *cutPhone) unpacked() string {
	for _, name := range ph.names() {
		if strings.HasPrefix(name, cutID+".new-") {
			return filepath.Join(ph.packages(), name)
		}
	}
	return ""
}

// halve removes every other file under dir, as a power cut halfway
// through writing or removing it leaves it.
func halve(t *testing.T, dir string) {
	t.Helper()
	if dir == "" {
		t.Fatal("no directory to cut short")
	}
	var files []string
	_ = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			files = append(files, p)
		}
		return err
	})
	for i := 0; i < len(files); i += 2 {
		if err := os.Remove(files[i]); err != nil {
			t.Fatal(err)
		}
	}
}

// putRight checks what a start did with the leftovers an install cut
// short left in packages/ (before): nothing is left of it but the app's
// own directory, and the log has one line for each.
func putRight(t *testing.T, ph *cutPhone, before []string, logs string) {
	t.Helper()
	after := ph.names()
	if len(after) > 1 || len(after) == 1 && after[0] != cutID {
		t.Errorf("packages/ after the start: %v (before: %v)", after, before)
	}
	for _, name := range before {
		if n := strings.Count(logs, name); name != cutID && n != 1 {
			t.Errorf("%s in %d lines of the log, want 1:\n%s", name, n, logs)
		}
	}
}

type cutCase struct {
	name string
	n    int
	// damage is what the step under way when the power went had done.
	damage func(ph *cutPhone)
}

// An update cut short by a power cut (or a crash of the Shell) after any
// of its steps, or halfway through unpacking the new version or removing
// the old one: after the next start the app is there, the old version or
// the new one, every file as signed, its data kept, and it opens; nothing
// else is left in packages/ and the log says what was put right; once the
// new version is in place a cut no longer brings the old one back. Before,
// the old version was removed and then the new one renamed into place:
// cut between the two the app was not among the apps until installed
// again, cut while removing the old one it failed verification, and the
// unpacked directory stayed for good.
func TestUpdateCutShortLeavesAVersionWhole(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the app runs without phone-sandbox, which root refuses")
	}
	ph := newCutPhone(t)
	ph.install(ph.pkg("1.0"), 0)
	steps := ph.install(ph.pkg("2.0"), 0)
	var cases []cutCase
	for n, step := range steps {
		cases = append(cases, cutCase{name: "after " + step, n: n + 1})
	}
	versions := make([]string, len(steps))
	// removing: the last step after which the old version is still on the
	// disk; the step after it removes it.
	removing := 0
	run := func(t *testing.T, cc cutCase) string {
		ph := newCutPhone(t)
		ph.install(ph.pkg("1.0"), 0)
		data := filepath.Join(ph.state(), "apps", cutID, "notes")
		_ = os.MkdirAll(filepath.Dir(data), 0o700)
		if err := os.WriteFile(data, []byte("kept"), 0o600); err != nil {
			t.Fatal(err)
		}
		ph.install(ph.pkg("2.0"), cc.n)
		if cc.damage != nil {
			cc.damage(ph)
		} else if ph.holding("1.0") != "" {
			removing = max(removing, cc.n)
		}
		before := ph.names()
		c, logs := ph.start(true)
		putRight(t, ph, before, logs.String())
		var a *App
		var version string
		var verr error
		_ = c.onLoop(func() error {
			if a = c.appByID(cutID); a != nil {
				version, verr = readInstallRecord(a.M.dir).Version, c.verifyInstalled(a)
			}
			return nil
		})
		switch {
		case a == nil:
			t.Fatalf("%s is not among the apps; packages/ was %v", cutID, before)
		case a.M.dir != filepath.Join(ph.packages(), cutID):
			t.Fatalf("installed in %s", a.M.dir)
		case version != "1.0" && version != "2.0" || verr != nil:
			t.Fatalf("version %q, verified: %v", version, verr)
		}
		if raw, err := os.ReadFile(data); err != nil || string(raw) != "kept" {
			t.Errorf("its data: %q, %v", raw, err)
		}
		_ = c.onLoop(func() error { c.Launch(a, ""); return nil })
		until(t, c, "the page of "+version, func() bool { return a.ready && title(a) == "v"+version })
		return version
	}
	for i, cc := range cases {
		t.Run(cc.name, func(t *testing.T) { versions[i] = run(t, cc) })
	}
	if first := slices.Index(versions, "2.0"); !t.Failed() && (first < 1 || slices.ContainsFunc(versions[first:], func(v string) bool { return v != "2.0" })) {
		t.Errorf("after each step %v: %v, want 1.0 until the new version is in place, then 2.0", steps, versions)
	}
	for _, cc := range []cutCase{
		{name: "unpacking cut short", n: 1, damage: func(ph *cutPhone) { halve(ph.t, ph.unpacked()) }},
		{name: "removing the old version cut short", n: removing, damage: func(ph *cutPhone) { halve(ph.t, ph.holding("1.0")) }},
	} {
		t.Run(cc.name, func(t *testing.T) { run(t, cc) })
	}
}

// A first install cut short after any of its steps, or halfway through
// unpacking: after the next start nothing of the app is left in packages/
// and it is not among the apps, or, cut once it was in place, it is there
// whole. Before, the unpacked directory stayed for good.
func TestFirstInstallCutShortLeavesNothing(t *testing.T) {
	ph := newCutPhone(t)
	steps := ph.install(ph.pkg("1.0"), 0)
	var cases []cutCase
	for n, step := range steps {
		cases = append(cases, cutCase{name: "after " + step, n: n + 1})
	}
	cases = append(cases, cutCase{name: "unpacking cut short", n: 1, damage: func(ph *cutPhone) { halve(ph.t, ph.unpacked()) }})
	var gone []bool
	for _, cc := range cases {
		t.Run(cc.name, func(t *testing.T) {
			ph := newCutPhone(t)
			ph.install(ph.pkg("1.0"), cc.n)
			if cc.damage != nil {
				cc.damage(ph)
			}
			before := ph.names()
			c, logs := ph.start(false)
			putRight(t, ph, before, logs.String())
			a := c.appByID(cutID)
			if a != nil {
				if err := c.verifyInstalled(a); err != nil || readInstallRecord(a.M.dir).Version != "1.0" {
					t.Errorf("installed %q, verified: %v", readInstallRecord(a.M.dir).Version, err)
				}
			} else if left := ph.names(); len(left) != 0 {
				t.Errorf("not installed, and left in packages/: %v", left)
			}
			gone = append(gone, a == nil)
		})
	}
	if !t.Failed() && (!gone[0] || gone[len(steps)-1]) {
		t.Errorf("after each step %v, not installed: %v", steps, gone[:len(steps)])
	}
}

// At start, what installs cut short left in packages/ is put right, one
// line in the log each: a version moved aside goes back where the app's
// own directory is missing (of several, the one with the highest number)
// and goes where it is there; one unpacked goes. Directories of other
// names, and files and links of these names, stay as they are.
func TestPackagesLeftoversAtStart(t *testing.T) {
	ph := newCutPhone(t)
	mk := func(name, id, mark string) {
		dir := filepath.Join(ph.packages(), name)
		_ = os.MkdirAll(dir, 0o755)
		for file, body := range map[string]string{"manifest.json": `{"id": "` + id + `", "name": "N", "exec": "app"}`, "mark": mark} {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("com.example.a.old-5", "com.example.a", "a")
	mk("com.example.b", "com.example.b", "b")
	mk("com.example.b.old-7", "com.example.b", "b replaced")
	mk("com.example.c.new-9", "com.example.c", "c unpacked")
	mk("com.example.d.old-3", "com.example.d", "d 3")
	mk("com.example.d.old-12", "com.example.d", "d 12")
	mk("com.example.d.new-12", "com.example.d", "d unpacked")
	recovered := []string{"com.example.a.old-5", "com.example.b.old-7", "com.example.c.new-9", "com.example.d.old-3", "com.example.d.old-12", "com.example.d.new-12"}
	others := []string{"com.example.e.old-x", "com.example.e.older-1", "com.example.e.old-", "com.example.e.new-1.tmp", "Com.Example.e.new-1",
		"notes.new-1", ".com.example.e.new-1", "com.example.e.old-1.new-2", "com.example.e.old-99999999999999999999999"}
	for _, name := range others {
		mk(name, "com.example.e", name)
	}
	if err := os.WriteFile(filepath.Join(ph.packages(), "com.example.f.new-1"), []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(ph.packages(), "com.example.f.old-1")); err != nil {
		t.Fatal(err)
	}
	others = append(others, "com.example.f.new-1", "com.example.f.old-1")
	c, logs := ph.start(false)
	want := append([]string{"com.example.a", "com.example.b", "com.example.d"}, others...)
	slices.Sort(want)
	if got := ph.names(); !slices.Equal(got, want) {
		t.Errorf("packages/ after the start:\n%v\nwant\n%v", got, want)
	}
	for id, mark := range map[string]string{"com.example.a": "a", "com.example.b": "b", "com.example.d": "d 12"} {
		raw, _ := os.ReadFile(filepath.Join(ph.packages(), id, "mark"))
		if a := c.appByID(id); string(raw) != mark || a == nil || !a.M.installed {
			t.Errorf("%s: %q, want %q; among the apps: %v", id, raw, mark, a != nil)
		}
	}
	for _, name := range append(recovered, others...) {
		if n, want := strings.Count(logs.String(), " "+name+","), map[bool]int{true: 1}[slices.Contains(recovered, name)]; n != want {
			t.Errorf("%s in %d lines of the log, want %d", name, n, want)
		}
	}
	if n := strings.Count(logs.String(), "packages: "); n != len(recovered) {
		t.Errorf("%d lines of recovery in the log, want %d:\n%s", n, len(recovered), logs)
	}
	if _, err := os.Stat(elsewhere); err != nil {
		t.Errorf("the link's target: %v", err)
	}
}

// IDs under "system." are the system's own (the keystore attests update
// reports as ota.ReportApp): no package may take one.
func TestSystemIDsAreNotInstallable(t *testing.T) {
	c := &Core{log: log.New(io.Discard, "", 0)}
	pack := func(id string) *pkg.Package {
		return &pkg.Package{Manifest: []byte(`{"id":"` + id + `","name":"N","exec":"app"}`),
			Files: []pkg.File{{Path: "app", Mode: 0o755}}}
	}
	if _, err := c.checkPackage(pack("system.update")); err == nil || !strings.Contains(err.Error(), "system.") {
		t.Fatalf("system.update installable: %v", err)
	}
	if _, err := c.checkPackage(pack("com.example.notes")); err != nil {
		t.Fatalf("an ordinary id refused: %v", err)
	}
}

// The store app's catalogue goes through the system: signed by a store the
// image trusts, and never older than the last one taken from that store
// (an update hidden by replaying yesterday's list).
func TestCheckCatalog(t *testing.T) {
	dir := t.TempDir()
	store, err := pkg.DevStore(filepath.Join(dir, "store"), filepath.Join(dir, "share", "phone", "trust"))
	if err != nil {
		t.Fatal(err)
	}
	c := &Core{cfg: Config{StateDir: dir, AppsDir: filepath.Join(dir, "share", "phone", "apps")}, log: log.New(io.Discard, "", 0)}
	_ = os.MkdirAll(filepath.Join(dir, "shell"), 0o700)
	encode := func(version string, at time.Time) string {
		cat, err := store.SignCatalog([]map[string]string{{"id": "com.example.notes", "version": version}}, at)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(cat)
		return string(raw)
	}
	now := time.Now()
	res, err := c.checkCatalog(encode("1.1", now))
	if err != nil || !strings.Contains(string(res.Apps), `"1.1"`) {
		t.Fatalf("a fresh catalogue: %v %s", err, res.Apps)
	}
	if _, err := c.checkCatalog(encode("1.0", now.Add(-time.Hour))); err == nil {
		t.Fatal("an older catalogue was taken after a newer one")
	}
	tampered := strings.Replace(encode("1.2", now), `"1.2"`, `"1.3"`, 1)
	if _, err := c.checkCatalog(tampered); err == nil {
		t.Fatal("a changed catalogue was taken")
	}
}

// A store app may handle intents, a few, with plain action names.
func TestIntentDeclarations(t *testing.T) {
	c := &Core{log: log.New(io.Discard, "", 0)}
	pack := func(intents string) *pkg.Package {
		return &pkg.Package{Manifest: []byte(`{"id":"com.example.pay","name":"N","exec":"app","intents":` + intents + `}`),
			Files: []pkg.File{{Path: "app", Mode: 0o755}}}
	}
	if _, err := c.checkPackage(pack(`[{"action":"pay","label":"Pay"}]`)); err != nil {
		t.Fatalf("a payment handler refused: %v", err)
	}
	for _, bad := range []string{`[{"action":"Pay!","label":"Pay"}]`, `[{"action":"pay","label":""}]`, `[{"action":"pay","label":"\u4e00\u4e8c\u4e09\u56db\u4e94\u516d\u4e03\u516b\u4e5d"}]`} {
		if _, err := c.checkPackage(pack(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// A runtime permission comes with the app's purpose for it, which the
// system's dialog shows; a purpose needs a permission it explains.
func TestPermissionPurposes(t *testing.T) {
	c := &Core{log: log.New(io.Discard, "", 0)}
	pack := func(perms, purposes string) *pkg.Package {
		return &pkg.Package{Manifest: []byte(`{"id":"com.example.scan","name":"N","exec":"app","permissions":` + perms + `,"purposes":` + purposes + `}`),
			Files: []pkg.File{{Path: "app", Mode: 0o755}}}
	}
	if _, err := c.checkPackage(pack(`["camera","net"]`, `{"camera":"Scan the code on a parcel"}`)); err != nil {
		t.Fatalf("a camera app with its purpose refused: %v", err)
	}
	for _, bad := range []struct{ perms, purposes, why string }{
		{`["camera"]`, `{}`, "needs a purpose"},
		{`["camera"]`, `{"camera":"  "}`, "needs a purpose"},
		{`["net"]`, `{"location":"Weather"}`, "does not declare"},
		{`["camera"]`, `{"camera":"` + strings.Repeat("x", 81) + `"}`, "1–80"},
		{`["location","location_background"]`, `{"location":"Weather where you are"}`, `"location_background" needs a purpose`},
		{`["location_background"]`, `{"location_background":"Weather where you go"}`, `"location_background" needs "location" too`},
	} {
		if _, err := c.checkPackage(pack(bad.perms, bad.purposes)); err == nil || !strings.Contains(err.Error(), bad.why) {
			t.Errorf("%s %s: %v", bad.perms, bad.purposes, err)
		}
	}
	o := &permOverlay{app: &App{M: &Manifest{Purposes: map[string]string{"camera": "Scan the code on a parcel"}}}, perms: []string{"camera"}}
	if got := o.label("camera"); !strings.Contains(got, "Scan the code on a parcel") {
		t.Errorf("the dialog shows %q", got)
	}
	o.origin = "Some mini-program"
	if got := o.label("camera"); strings.Contains(got, "parcel") {
		t.Errorf("a mini-program's request shows the host's purpose: %q", got)
	}
	o.purposes = map[string]string{"camera": "Scan the code on the table"}
	if got := o.label("camera"); !strings.Contains(got, "on the table") {
		t.Errorf("a mini-program's request hides its own purpose: %q", got)
	}
	o.purposes["camera"] = strings.Repeat("x", 81)
	if got := o.label("camera"); strings.Contains(got, "xxx") {
		t.Errorf("an over-long purpose is shown: %q", got)
	}
}

// The script's app-file-drop takes away a package pkg-build put into an
// app's directory, and only inside it; widget off takes an app's standby
// line away.
func TestAppFileDropAndWidgetOffSteps(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		a := &App{M: &Manifest{ID: "com.example.store", Name: "Store", Widget: true}}
		c.apps = append(c.apps, a)
		r := &Runner{C: c, Out: io.Discard}
		file := c.appFile(a, "downloads/notes.phoneapp")
		_ = os.MkdirAll(filepath.Dir(file), 0o755)
		_ = os.WriteFile(file, []byte("pkg"), 0o644)
		outside := filepath.Join(c.cfg.StateDir, "outside")
		_ = os.WriteFile(outside, []byte("x"), 0o644)
		if err := r.step("app-file-drop", "com.example.store downloads/notes.phoneapp"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Errorf("the package is still there: %v", err)
		}
		if err := r.step("app-file-drop", "com.example.store downloads/notes.phoneapp"); err == nil {
			t.Error("dropped a file that is not there")
		}
		_ = r.step("app-file-drop", "com.example.store ../../outside")
		if _, err := os.Stat(outside); err != nil {
			t.Errorf("a file outside the app's directory went: %v", err)
		}
		if err := r.step("app-file-drop", "com.example.none downloads/x"); err == nil {
			t.Error("took an app that is not there")
		}
		if err := c.setWidget(a, "Beijing 3°C", "today"); err != nil {
			t.Fatal(err)
		}
		if err := r.step("widget", "com.example.store on"); err == nil {
			t.Error("took the step without off")
		}
		if err := r.step("widget", "com.example.store off"); err != nil || len(c.widgetLines()) != 0 {
			t.Errorf("after widget off: %v %v", err, c.widgetLines())
		}
		return nil
	})
}

// Uninstalling takes the app's notifications, standby line and settings
// with it: a notification left behind would open an app that is gone, and
// the same app installed again would start with the old one's settings.
func TestUninstallDropsNotificationsAndPrefs(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		dir := filepath.Join(t.TempDir(), "com.example.sms")
		_ = os.MkdirAll(dir, 0o755)
		gone := &App{M: &Manifest{ID: "com.example.sms", Name: "SMS", Permissions: []string{ui.PermNotify}, Widget: true,
			Settings: []ui.AppSetting{{Key: "sound", Title: "Sound", Type: "toggle", Default: true}}, installed: true, dir: dir}}
		stays := &App{M: &Manifest{ID: "com.example.other", Name: "Other", Permissions: []string{ui.PermNotify}, Widget: true}}
		c.apps = append(c.apps, gone, stays)
		c.addNotif(gone, ui.Notification{ID: "sms:1", Title: "13800000011"})
		c.addNotif(stays, ui.Notification{ID: "x", Title: "kept"})
		for _, a := range []*App{gone, stays} {
			if err := c.setWidget(a, "a line", ""); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.setAppSetting(gone.M.ID, "sound", false); err != nil {
			t.Fatal(err)
		}
		c.removeApp(gone)
		if len(c.notifs) != 1 || c.notifs[0].app != stays {
			t.Errorf("the notifications after uninstalling: %v", c.idleNotifLines())
		}
		if _, ok := c.prefs.Widgets[gone.M.ID]; ok || c.prefs.Widgets[stays.M.ID] == "" {
			t.Errorf("the standby lines after uninstalling: %v", c.prefs.Widgets)
		}
		if _, ok := c.prefs.Values[gone.M.ID]; ok {
			t.Errorf("the settings outlived the app: %v", c.prefs.Values)
		}
		c.loadPrefs()
		if _, ok := c.prefs.Widgets[gone.M.ID]; ok {
			t.Errorf("the standby line comes back from the file: %v", c.prefs.Widgets)
		}
		return nil
	})
}
