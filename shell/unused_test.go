package shell

import (
	"io"
	"log"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// An installed app not opened for three months loses its runtime grants,
// its mini-programs' too, and the user is told once; an app in use, a
// system app and the grants that are not runtime ones keep theirs.
func TestUnusedAppsLoseGrants(t *testing.T) {
	dir := t.TempDir()
	notes := &App{M: &Manifest{ID: "com.example.notes", Name: "Notes", installed: true}}
	maps := &App{M: &Manifest{ID: "com.example.maps", Name: "Maps", installed: true}}
	settings := &App{M: &Manifest{ID: healthApp, Name: "Settings"}}
	camera := &App{M: &Manifest{ID: "com.phone.camera", Name: "Camera"}}
	c := &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: dir}, apps: []*App{notes, maps, settings, camera}}
	c.broker = &broker{c: c, path: filepath.Join(dir, "perms.json"), perms: map[string]map[string]string{
		"com.example.notes":      {ui.PermCamera: "granted", ui.PermLocation: "denied", ui.PermNet: "wifi"},
		"com.example.notes/mini": {ui.PermLocation: "granted"},
		"com.example.maps":       {ui.PermLocation: "granted"},
		"com.phone.camera":       {ui.PermCamera: "granted"},
	}}
	c.checkUnused()
	c.opened(maps)
	c.clockSkew = unusedAfter + time.Hour
	c.opened(maps)
	c.checkUnused()

	p := c.broker.perms
	if p["com.example.notes"][ui.PermCamera] != "" || p["com.example.notes/mini"][ui.PermLocation] != "" {
		t.Fatalf("unused app kept its grants: %v", p)
	}
	if p["com.example.notes"][ui.PermLocation] != "denied" || p["com.example.notes"][ui.PermNet] != "wifi" {
		t.Fatalf("a refusal or a network rule was undone: %v", p["com.example.notes"])
	}
	if p["com.example.maps"][ui.PermLocation] != "granted" || p["com.phone.camera"][ui.PermCamera] != "granted" {
		t.Fatalf("an app in use or a system app lost a grant: %v", p)
	}
	if len(c.notifs) != 1 || c.notifs[0].app != settings || c.notifs[0].n.ID != ui.NotifUnusedPerms {
		t.Fatalf("notifications %+v", c.notifs)
	}
	if e := c.broker.entry("com.example.notes", "Notes", ui.PermCamera, permLabel(ui.PermCamera), "ask"); e.Reset == 0 {
		t.Fatal("Settings does not show the grant was taken back")
	}
	c.clearReset("com.example.notes", ui.PermCamera)
	if c.resetAt("com.example.notes", ui.PermCamera) != 0 {
		t.Fatal("the mark stays after the user decided again")
	}
}
