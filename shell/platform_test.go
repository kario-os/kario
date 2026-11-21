package shell

import (
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/pkg"
	"github.com/kario-os/kario/ui"
)

// An app needing a newer platform than this system's is not installed,
// and one already installed (the system went back to an older slot) does
// not start; the broker does not hold an app to the level it declared.
func TestMinPlatform(t *testing.T) {
	c := &Core{cfg: Config{StateDir: t.TempDir()}, log: log.New(io.Discard, "", 0)}
	pack := func(level string) *pkg.Package {
		return &pkg.Package{Manifest: []byte(`{"id":"com.example.notes","name":"Notes","exec":"app"` + level + `}`),
			Files: []pkg.File{{Path: "app", Mode: 0o755}}}
	}
	for _, ok := range []string{"", `,"min_platform":0`, `,"min_platform":` + strconv.Itoa(ui.PlatformLevel)} {
		if _, err := c.checkPackage(pack(ok)); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	newer := tr.T("“%s” needs a newer system (platform level %d; this phone has %d)", "Notes", ui.PlatformLevel+1, ui.PlatformLevel)
	for bad, why := range map[string]string{
		`,"min_platform":` + strconv.Itoa(ui.PlatformLevel+1): newer,
		`,"min_platform":-1`:  "not a platform level",
		`,"min_platform":"1"`: "min_platform",
	} {
		if _, err := c.checkPackage(pack(bad)); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s: %v, want %q", bad, err, why)
		}
	}
	installed := &App{M: &Manifest{ID: "com.example.notes", Name: "Notes", MinPlatform: ui.PlatformLevel + 1, installed: true}}
	if err := c.verifyInstalled(installed); err == nil || err.Error() != newer {
		t.Errorf("an installed app needing level %d started: %v", ui.PlatformLevel+1, err)
	}
	for cap, level := range ui.CapLevels {
		if _, served := capPerms[cap]; !served && level <= ui.PlatformLevel {
			t.Errorf("%s (level %d) is not served", cap, level)
		}
	}
	for cap := range capPerms {
		if _, ok := ui.CapLevels[cap]; !ok {
			t.Errorf("%s is served but has no level in ui.CapLevels", cap)
		}
	}
}

// An app learns the system's platform level from its environment, to use a
// capability newer than the level it declared where the system has it.
func TestAppsKnowThePlatform(t *testing.T) {
	if !alone(t) {
		return
	}
	r := newHostileRig(t, map[string]string{})
	a := r.launch(probeID)
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", a.proc.Pid))
	if err != nil {
		t.Fatal(err)
	}
	want := ui.EnvPlatform + "=" + strconv.Itoa(ui.PlatformLevel)
	if !slices.Contains(strings.Split(string(raw), "\x00"), want) {
		t.Fatalf("the app's environment has no %s", want)
	}
	r.c.kill(a)
	r.ended(a, 5*time.Second)
}
