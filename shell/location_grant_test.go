package shell

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/location"
	"github.com/kario-os/kario/ui"
)

func (lt *locTest) dialog() *permOverlay {
	lt.t.Helper()
	for _, o := range lt.c.overlays {
		if p, ok := o.(*permOverlay); ok {
			return p
		}
	}
	lt.t.Fatal("no permission dialog")
	return nil
}

func (lt *locTest) answer(o *permOverlay, keys ...string) {
	for _, k := range keys {
		o.key(lt.c, Named(k))
	}
}

func (lt *locTest) located(a *App) ui.Location {
	lt.t.Helper()
	r := lt.reply(a)
	var l ui.Location
	if err := json.Unmarshal(r.Data, &l); err != nil || !r.OK {
		lt.t.Fatalf("%+v %v", r, err)
	}
	return l
}

// The dialog offers the location precise, approximate or not at all; the
// answer is the grant, its precision kept for the app.
func TestLocationDialogChoices(t *testing.T) {
	maps, notes, nope := locApp("com.example.maps"), locApp("com.example.notes"), locApp("com.example.nope")
	lt := newLocTest(t, maps, notes, nope)
	lt.c.fg = maps
	lt.req(maps, ui.CapLocationGet, nil)
	o := lt.dialog()
	if names, sel := o.choices(); strings.Join(names, "|") != tr.T("Precise")+"|"+tr.T("Approximate")+"|"+tr.T("Deny") || sel != 0 || o.note() == "" {
		t.Fatalf("%v, %d selected", names, sel)
	}
	if f := o.frame(lt.c); len(f.Choices) != 3 || f.Choice != 0 || f.Note == "" {
		t.Fatalf("the pixel renderers' dialog: %+v", f)
	}
	lt.answer(o, KeyRight)
	if _, sel := o.choices(); sel != 1 || !o.yes || !o.approx {
		t.Fatalf("Right: %d selected", sel)
	}
	lt.answer(o, KeyEnter)
	if lt.c.broker.perms[maps.M.ID][ui.PermLocation] != "granted" || !lt.c.settings.LocationApprox[maps.M.ID] {
		t.Fatalf("grant %v, approximate %v", lt.c.broker.perms[maps.M.ID], lt.c.settings.LocationApprox)
	}
	lt.pump("the receiver on", lt.gnss.on.Load)
	lt.fix(beijing)
	if l := lt.located(maps); !l.Approximate || l.AltitudeM != nil || l.Satellites != 0 {
		t.Fatalf("approximate chosen: %+v", l)
	}

	lt.c.fg = notes
	lt.req(notes, ui.CapLocationGet, nil)
	o = lt.dialog()
	lt.answer(o, KeyRight, KeyRight, KeyRight)
	if _, sel := o.choices(); sel != 0 || o.approx {
		t.Fatalf("round the ends: %d selected", sel)
	}
	lt.answer(o, KeyEnter)
	if l := lt.located(notes); l.Approximate || l.Satellites != 8 || lt.c.settings.LocationApprox[notes.M.ID] {
		t.Fatalf("precise chosen: %+v", l)
	}

	lt.c.fg = nope
	lt.req(nope, ui.CapLocationGet, nil)
	o = lt.dialog()
	lt.answer(o, KeyLeft)
	if _, sel := o.choices(); sel != 2 || o.yes {
		t.Fatalf("Left from Precise: %d selected", sel)
	}
	lt.answer(o, KeyEnter)
	if r := lt.reply(nope); r.OK || lt.c.broker.perms[nope.M.ID][ui.PermLocation] != "denied" {
		t.Fatalf("denied: %+v", r)
	}
}

// On the narrowest screen the answers stand side by side, whole, in each
// language, with the note on precision (or, all the time asked for, on
// changing it later).
func TestLocationDialogFits(t *testing.T) {
	defer i18n.SetLang(i18n.Lang())
	maps := locApp("com.example.maps")
	lt := newLocTest(t, maps)
	lt.c.cfg.W, lt.c.cfg.H = 36, 20
	for _, lang := range []string{i18n.English, i18n.SimplifiedChinese, i18n.TraditionalChinese, i18n.Spanish} {
		i18n.SetLang(lang)
		for _, o := range []*permOverlay{{app: maps, perms: []string{ui.PermLocation}, yes: true, location: true}, {app: maps, perms: []string{ui.PermLocation}, yes: true, location: true, approxOnly: true, approx: true},
			{app: maps, perms: []string{ui.PermLocationBackground}, always: true}} {
			can := cv.New(36, 20, cv.Style{})
			o.render(lt.c, can)
			screen := can.Plain()
			names, _ := o.choices()
			for i, name := range names {
				if lang == i18n.Spanish && i == 1 && o.location && !o.approxOnly {
					// The longest of three, cut short to fit.
					name = cv.Truncate(name, cv.StringWidth(name)-2)
				}
				if !strings.Contains(screen, " "+name+" ") {
					t.Errorf("%s: %q cut or covered:\n%s", lang, name, screen)
				}
			}
			if !strings.Contains(screen, o.note()) {
				t.Errorf("%s: the note %q not on one line:\n%s", lang, o.note(), screen)
			}
		}
	}
}

// An app may ask for the approximate location only: the dialog offers only
// that, and such a request gets no more whatever the grant.
func TestLocationApproximateOnly(t *testing.T) {
	weather, maps := locApp("com.example.weather"), locApp("com.example.maps")
	lt := newLocTest(t, weather, maps)
	lt.c.fg = weather
	lt.req(weather, ui.CapLocationGet, ui.LocationGetArgs{Approximate: true})
	o := lt.dialog()
	if names, sel := o.choices(); strings.Join(names, "|") != tr.T("Approximate")+"|"+tr.T("Deny") || sel != 0 {
		t.Fatalf("%v %d", names, sel)
	}
	lt.answer(o, KeyEnter)
	if !lt.c.settings.LocationApprox[weather.M.ID] {
		t.Fatal("the grant is not approximate")
	}
	lt.pump("the receiver on", lt.gnss.on.Load)
	lt.fix(beijing)
	if l := lt.located(weather); !l.Approximate || l.AccuracyM != approxM {
		t.Fatalf("%+v", l)
	}

	lt.grant(maps, "granted")
	lt.c.fg = maps
	lt.c.loc.lastAt = time.Now()
	lt.req(maps, ui.CapLocationGet, ui.LocationGetArgs{Approximate: true})
	if l := lt.located(maps); !l.Approximate || len(lt.c.overlays) != 0 {
		t.Fatalf("a precise grant, an approximate request: %+v", l)
	}
	lt.req(maps, ui.CapLocationGet, nil)
	if l := lt.located(maps); l.Approximate {
		t.Fatalf("the precise request after it: %+v", l)
	}
	lt.req(maps, ui.CapLocationWatch, ui.LocationWatchArgs{Approximate: true})
	lt.reply(maps)
	if got := events(maps, ui.EventLocation); len(got) != 1 || !got[0].Approximate {
		t.Fatalf("the watch's first fix: %+v", got)
	}
	w := lt.c.loc.watches[maps]
	w.sentAt = time.Now().Add(-time.Minute)
	lt.fix(location.Fix{Lat: beijing.Lat + 0.0001, Lon: beijing.Lon, AccuracyM: 5})
	if got := events(maps, ui.EventLocation); len(got) != 0 {
		t.Fatalf("the same cell again within %s: %+v", locApproxEvery, got)
	}
	lt.fix(location.Fix{Lat: beijing.Lat + 0.05, Lon: beijing.Lon, AccuracyM: 5})
	if got := events(maps, ui.EventLocation); len(got) != 1 || !got[0].Approximate {
		t.Fatalf("5.5 km on: %+v", got)
	}
}

// settingsApp is the system app that changes grants (perm.set).
func settingsApp() *App {
	return &App{M: &Manifest{ID: healthApp, Name: "Settings", Permissions: []string{ui.PermSystem}}, running: true, ready: true, out: make(chan outbound, 64)}
}

func (lt *locTest) permSet(sys *App, key, perm, state string) ui.Msg {
	lt.req(sys, ui.CapPermSet, ui.PermSetArgs{App: key, Perm: perm, State: state})
	return lt.reply(sys)
}

// All the time is given in Settings only, to an app that declares it, and
// allows the location with it; the location taken back takes it back,
// asked again the precision too. Settings lists it "denied" until given.
func TestLocationBackgroundGrant(t *testing.T) {
	maps, plain, sys := locApp("com.example.maps"), locApp("com.example.plain"), settingsApp()
	maps.M.Permissions = append(maps.M.Permissions, ui.PermLocationBackground)
	lt := newLocTest(t, maps, plain, sys)
	p := lt.c.broker.perms
	for _, bad := range []struct{ key, state string }{{plain.M.ID, "granted"}, {maps.M.ID + "/mini", "granted"}, {maps.M.ID, "ask"}} {
		if r := lt.permSet(sys, bad.key, ui.PermLocationBackground, bad.state); r.OK {
			t.Errorf("all the time %s for %s taken", bad.state, bad.key)
		}
	}
	if r := lt.permSet(sys, maps.M.ID, ui.PermLocationBackground, "granted"); !r.OK || p[maps.M.ID][ui.PermLocation] != "granted" || p[maps.M.ID][ui.PermLocationBackground] != "granted" {
		t.Fatalf("%+v %v", r, p[maps.M.ID])
	}
	lt.c.settings.LocationApprox = map[string]bool{maps.M.ID: true}
	lt.req(sys, ui.CapPermList, nil)
	var list []ui.PermEntry
	_ = json.Unmarshal(lt.reply(sys).Data, &list)
	states := map[string]ui.PermEntry{}
	for _, e := range list {
		states[e.App+" "+e.Perm] = e
	}
	if e := states[maps.M.ID+" "+ui.PermLocation]; e.State != "granted" || !e.Approximate {
		t.Fatalf("%+v", e)
	}
	if e := states[maps.M.ID+" "+ui.PermLocationBackground]; e.State != "granted" || e.Label != permLabel(ui.PermLocationBackground) || e.Label == ui.PermLocationBackground {
		t.Fatalf("%+v", e)
	}
	lt.permSet(sys, maps.M.ID, ui.PermLocation, "denied")
	if p[maps.M.ID][ui.PermLocationBackground] != "" || !lt.c.settings.LocationApprox[maps.M.ID] {
		t.Fatalf("location denied: %v, approximate %v", p[maps.M.ID], lt.c.settings.LocationApprox)
	}
	lt.permSet(sys, maps.M.ID, ui.PermLocation, "ask")
	if lt.c.settings.LocationApprox[maps.M.ID] {
		t.Fatal("asked again, the old precision stays")
	}
	lt.req(sys, ui.CapPermList, nil)
	list = nil
	_ = json.Unmarshal(lt.reply(sys).Data, &list)
	for _, e := range list {
		if e.App == maps.M.ID && e.Perm == ui.PermLocationBackground && e.State != "denied" {
			t.Fatalf("all the time, never given: %+v", e)
		}
	}
}

// Without all the time an app out of the foreground has no location (and
// raises no dialog); with it, it has, while it is not frozen: its get is
// served, its watch goes on, and the use is recorded as in the background.
// A mini-program never has it.
func TestLocationBackgroundUse(t *testing.T) {
	maps, fg := locApp("com.example.maps"), locApp("com.example.fg")
	maps.M.Permissions = append(maps.M.Permissions, ui.PermLocationBackground)
	lt := newLocTest(t, maps, fg)
	lt.grant(maps, "granted")
	lt.c.setForeground(maps)
	lt.req(maps, ui.CapLocationWatch, nil)
	lt.reply(maps)
	lt.pump("the receiver on", lt.gnss.on.Load)
	lt.c.setForeground(fg)
	lt.req(maps, ui.CapLocationGet, nil)
	if r := lt.reply(maps); r.Err != ui.ErrLocationBackground || len(lt.c.overlays) != 0 {
		t.Fatalf("without all the time: %+v", r)
	}
	lt.fix(beijing)
	if got := events(maps, ui.EventLocation); len(got) != 0 || lt.c.locKeepsRunning(maps) {
		t.Fatalf("a watch in the background without all the time: %+v", got)
	}

	lt.c.broker.perms[maps.M.ID][ui.PermLocationBackground] = "granted"
	lt.c.locSync()
	lt.c.loc.watches[maps].sentAt = time.Now().Add(-time.Minute)
	lt.fix(location.Fix{Lat: 31.2304, Lon: 121.4737, AccuracyM: 5})
	if got := events(maps, ui.EventLocation); len(got) != 1 || got[0].Lat != 31.2304 {
		t.Fatalf("all the time, in the background: %+v", got)
	}
	lt.req(maps, ui.CapLocationGet, ui.LocationGetArgs{MaxAgeS: 60})
	if l := lt.located(maps); l.Lat != 31.2304 {
		t.Fatalf("%+v", l)
	}
	if r := lt.c.loc.recent[maps.M.ID]; r == nil || r.BgAt == 0 {
		t.Fatalf("not recorded as in the background: %+v", r)
	}
	maps.M.Background = []string{"location"}
	if !lt.c.runsInBackground(maps) {
		t.Fatal("watching with the location background mode, it would be frozen")
	}
	maps.frozen = true
	if lt.c.locActive(lt.c.loc.watches[maps]) {
		t.Fatal("a frozen app's watch is active")
	}
	maps.frozen = false
	if err := lt.c.locRefused(maps, ui.CapLocationGet, maps.M.ID+"/mini"); err == nil || err.Error() != ui.ErrLocationBackground {
		t.Fatalf("a mini-program in the background: %v", err)
	}
	lt.c.broker.perms[maps.M.ID][ui.PermLocation] = "denied"
	if lt.c.runsInBackground(maps) || lt.c.locActive(lt.c.loc.watches[maps]) {
		t.Fatal("still watching in the background with the location denied")
	}
}

// Background use is named in a reminder from Settings, again only a week
// later and only if the app used it again.
func TestLocationRemind(t *testing.T) {
	maps, fg, sys := locApp("com.example.maps"), locApp("com.example.fg"), settingsApp()
	maps.M.Permissions = append(maps.M.Permissions, ui.PermLocationBackground)
	lt := newLocTest(t, maps, fg, sys)
	lt.grant(maps, "granted")
	lt.c.broker.perms[maps.M.ID][ui.PermLocationBackground] = "granted"
	lt.c.fg = fg
	lt.c.loc.last, lt.c.loc.lastAt = beijing, time.Now()
	lt.c.locRemind()
	if len(lt.c.notifs) != 0 {
		t.Fatal("a reminder with nothing used")
	}
	lt.req(maps, ui.CapLocationGet, ui.LocationGetArgs{MaxAgeS: 60})
	lt.located(maps)
	lt.c.locRemind()
	if len(lt.c.notifs) != 0 {
		t.Fatal("looked again within the hour")
	}
	lt.c.clockSkew = locRemindCheck
	lt.c.locRemind()
	if len(lt.c.notifs) != 1 || lt.c.notifs[0].app != sys || lt.c.notifs[0].n.ID != ui.NotifBgLocation || !strings.Contains(lt.c.notifs[0].n.Body, maps.M.Name) {
		t.Fatalf("%+v", lt.c.notifs)
	}
	lt.c.notifs = nil
	lt.c.clockSkew += 2 * locRemindCheck
	lt.req(maps, ui.CapLocationGet, ui.LocationGetArgs{MaxAgeS: 60})
	lt.located(maps)
	lt.c.clockSkew += locRemindCheck
	lt.c.locRemind()
	if len(lt.c.notifs) != 0 {
		t.Fatal("reminded again within the week")
	}
	lt.c.clockSkew += locRemindEvery
	lt.c.locRemind()
	if len(lt.c.notifs) != 1 {
		t.Fatal("no reminder a week later, used again since")
	}
	lt.c.notifs = nil
	lt.c.clockSkew += locRemindEvery
	lt.c.locRemind()
	if len(lt.c.notifs) != 0 {
		t.Fatal("reminded with no use since")
	}
	again := &Core{log: lt.c.log, cfg: lt.c.cfg, apps: lt.c.apps}
	again.locInit()
	if again.loc.reminded[maps.M.ID] == 0 || again.loc.recent[maps.M.ID].BgAt == 0 {
		t.Fatal("the reminders are forgotten across a restart")
	}
}
