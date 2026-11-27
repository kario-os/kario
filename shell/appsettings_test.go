package shell

import (
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
)

// A setting changed while its app is frozen in the background (Settings is
// on screen) reaches the app at once: it is woken long enough to answer,
// e.g. to put the new unit on its standby widget.
func TestSettingWakesFrozenApp(t *testing.T) {
	m := &Manifest{ID: "org.example.w", Settings: []ui.AppSetting{{Key: "units", Title: "Units", Type: "choice", Choices: []string{"C", "F"}}}}
	a := &App{M: m, out: make(chan outbound, 4), running: true, ready: true, frozen: true, bgSince: time.Now().Add(-time.Minute)}
	c := &Core{log: log.New(io.Discard, "", 0), apps: []*App{a}}
	c.cfg.StateDir = t.TempDir()
	c.loadPrefs()
	if err := c.setAppSetting(m.ID, "units", "F"); err != nil {
		t.Fatal(err)
	}
	if a.frozen || time.Since(a.bgSince) >= c.freezeAfter() {
		t.Fatalf("frozen %v, in background for %s: left frozen or frozen again at once", a.frozen, time.Since(a.bgSince))
	}
	select {
	case o := <-a.out:
		if o.m.Event != ui.EventSettings || !strings.Contains(o.m.Value, `"value":"F"`) {
			t.Fatalf("the app heard %+v", o.m)
		}
	default:
		t.Fatal("the app was not told")
	}
}

func TestSettingTypes(t *testing.T) {
	c := &Core{}
	c.cfg.StateDir = t.TempDir()
	c.loadPrefs()
	m := &Manifest{ID: "org.example.x", Settings: []ui.AppSetting{
		{Key: "n", Title: "Number", Type: "number", Min: 1, Max: 5},
		{Key: "s", Title: "Text", Type: "text", MaxLen: 4},
		{Key: "c", Title: "Choice", Type: "choice", Choices: []string{"A", "B"}},
	}}
	if err := checkSettingDecls(m); err != nil {
		t.Fatal(err)
	}
	c.apps = []*App{{M: m}}
	// The text limit counts characters: four Chinese ones fit, five do not.
	for _, ok := range []struct {
		key string
		v   any
	}{{"n", 3.0}, {"s", "\u56db\u4e2a\u5b57\u7b26"}, {"c", "B"}} {
		if err := c.setAppSetting("org.example.x", ok.key, ok.v); err != nil {
			t.Errorf("%s=%v: %v", ok.key, ok.v, err)
		}
	}
	for _, bad := range []struct {
		key string
		v   any
	}{{"n", 9.0}, {"n", "3"}, {"s", "\u4e94\u4e2a\u5b57\u7b26\u4e86"}, {"s", "a\nb"}, {"c", "C"}, {"zz", true}} {
		if err := c.setAppSetting("org.example.x", bad.key, bad.v); err == nil {
			t.Errorf("%s=%v accepted", bad.key, bad.v)
		}
	}
	for _, d := range [][]ui.AppSetting{
		{{Key: "n", Title: "Number", Type: "number", Min: 5, Max: 5}},
		{{Key: "c", Title: "Choice", Type: "choice", Choices: []string{"A"}}},
		{{Key: "", Title: "No key", Type: "toggle"}},
	} {
		if err := checkSettingDecls(&Manifest{Settings: d}); err == nil {
			t.Errorf("%+v accepted", d)
		}
	}
}
