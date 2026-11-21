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
