package shell

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

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
