package shell

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/location"
	"github.com/kario-os/kario/ui"
)

// alwaysApp declares the location all the time too, with its purposes.
func alwaysApp(id string) *App {
	a := locApp(id)
	a.M.Permissions = append(a.M.Permissions, ui.PermLocationBackground)
	a.M.Purposes = map[string]string{ui.PermLocation: "Nearby places", ui.PermLocationBackground: "Record your walk with the screen off"}
	return a
}

// bgNotif is the system's notification that the app uses the location in
// the background, if it is up.
func bgNotif(c *Core, a *App) *notif {
	for _, n := range c.notifs {
		if n.n.ID == ui.NotifBgLocationUse+":"+a.M.ID {
			return n
		}
	}
	return nil
}

// All the time is a step of its own (Android 11, iOS): the first dialog
// asks for the location while in use only; location.background, from the
// foreground app once that is allowed, brings up a second dialog with the
// app's purpose, "only while in use" selected first, and asks once: the
// answer stands until the location itself is to be asked again.
func TestLocationAlwaysSecondStep(t *testing.T) {
	maps, notes, plain, sys := alwaysApp("com.example.maps"), alwaysApp("com.example.notes"), locApp("com.example.plain"), settingsApp()
	lt := newLocTest(t, maps, notes, plain, sys)
	p := lt.c.broker.perms
	lt.c.fg = maps
	lt.req(maps, ui.CapLocationBackground, nil)
	if r := lt.reply(maps); r.Err != ui.ErrLocationWhileInUse || len(lt.c.overlays) != 0 {
		t.Fatalf("before the location while in use: %+v", r)
	}
	lt.c.loc.last, lt.c.loc.lastAt = beijing, time.Now()
	lt.req(maps, ui.CapLocationGet, ui.LocationGetArgs{MaxAgeS: 60})
	o := lt.dialog()
	if names, _ := o.choices(); strings.Join(names, "|") != tr.T("Precise")+"|"+tr.T("Approximate")+"|"+tr.T("Deny") || strings.Join(o.perms, ",") != ui.PermLocation {
		t.Fatalf("the first dialog: %v for %v", names, o.perms)
	}
	lt.answer(o, KeyEnter)
	lt.located(maps)
	if p[maps.M.ID][ui.PermLocationBackground] != "" {
		t.Fatalf("the first dialog gave all the time: %v", p[maps.M.ID])
	}

	lt.req(maps, ui.CapLocationBackground, nil)
	o = lt.dialog()
	names, sel := o.choices()
	if strings.Join(names, "|") != tr.T("Always")+"|"+tr.T("Only while in use") || sel != 1 || o.yes || o.note() == "" ||
		!strings.Contains(o.label(ui.PermLocationBackground), "Record your walk") {
		t.Fatalf("the second dialog: %v, %d selected, note %q, %q", names, sel, o.note(), o.label(ui.PermLocationBackground))
	}
	if f := o.frame(lt.c); len(f.Choices) != 2 || f.Choice != 1 || f.Note == "" || len(f.Lines) != 2 || !strings.Contains(f.Lines[1], "Record your walk") {
		t.Fatalf("the pixel renderers' dialog: %+v", f)
	}
	lt.answer(o, KeyRight)
	if _, sel := o.choices(); sel != 0 || !o.yes {
		t.Fatalf("Right round the end: %d selected", sel)
	}
	lt.answer(o, KeyEnter)
	r := lt.reply(maps)
	var got struct {
		Background bool `json:"background"`
	}
	if !r.OK || json.Unmarshal(r.Data, &got) != nil || !got.Background || p[maps.M.ID][ui.PermLocationBackground] != "granted" {
		t.Fatalf("all the time chosen: %+v, %v", r, p[maps.M.ID])
	}
	if u := lt.c.broker.usage[maps.M.ID][ui.PermLocationBackground]; u != nil {
		t.Fatalf("asking counted as a use: %+v", u)
	}
	lt.req(maps, ui.CapLocationBackground, nil)
	if r := lt.reply(maps); !r.OK || len(lt.c.overlays) != 0 {
		t.Fatalf("given, asked again: %+v", r)
	}

	lt.c.fg = notes
	lt.grant(notes, "granted")
	lt.req(notes, ui.CapLocationBackground, nil)
	lt.answer(lt.dialog(), KeyEnter)
	if r := lt.reply(notes); r.OK || p[notes.M.ID][ui.PermLocationBackground] != "denied" || p[notes.M.ID][ui.PermLocation] != "granted" {
		t.Fatalf("only while in use: %+v, %v", r, p[notes.M.ID])
	}
	lt.req(notes, ui.CapLocationBackground, nil)
	if r := lt.reply(notes); r.OK || len(lt.c.overlays) != 0 {
		t.Fatalf("kept to while in use, asked again: %+v", r)
	}
	lt.permSet(sys, notes.M.ID, ui.PermLocation, "ask")
	lt.grant(notes, "granted")
	lt.req(notes, ui.CapLocationBackground, nil)
	if names, _ := lt.dialog().choices(); names[0] != tr.T("Always") {
		t.Fatalf("the location asked again, all the time is not asked again: %v", names)
	}
	lt.answer(lt.dialog(), KeyEsc)
	lt.reply(notes)

	lt.c.fg = plain
	lt.req(maps, ui.CapLocationBackground, nil)
	if r := lt.reply(maps); r.Err != ui.ErrLocationBackground || len(lt.c.overlays) != 0 {
		t.Fatalf("from the background: %+v", r)
	}
	lt.c.fg = maps
	lt.c.broker.handle(maps, ui.Msg{T: ui.MsgReq, Req: 8, Cap: ui.CapLocationBackground, Origin: "mini"})
	if r := lt.reply(maps); r.Err != ui.ErrLocationBackground || len(lt.c.overlays) != 0 {
		t.Fatalf("a mini-program: %+v", r)
	}
	lt.c.fg = plain
	lt.grant(plain, "granted")
	lt.req(plain, ui.CapLocationBackground, nil)
	if r := lt.reply(plain); r.OK || !strings.Contains(r.Err, "not declared") {
		t.Fatalf("not declared: %+v", r)
	}
}

// The second dialog is a system decision as the first is (R55): a key
// pressed before it came up, or an Enter in its first moment, does not
// answer it; the arrows move it at once, and the computer's keys cannot.
func TestLocationAlwaysDialogKeys(t *testing.T) {
	maps := alwaysApp("com.example.maps")
	lt := newLocTest(t, maps)
	lt.grant(maps, "granted")
	lt.c.fg = maps
	before := time.Now()
	lt.req(maps, ui.CapLocationBackground, nil)
	o := lt.dialog()
	switch {
	case lt.c.overlayTakes(o, Key{Name: KeyEnter, At: before}):
		t.Fatal("an Enter pressed before the dialog answers it")
	case lt.c.overlayTakes(o, Named(KeyEnter)):
		t.Fatal("an Enter in its first moment answers it")
	case !lt.c.overlayTakes(o, Named(KeyLeft)):
		t.Fatal("the arrows do not move it at once")
	case lt.c.overlayTakes(o, Key{Name: KeyLeft, Injected: true}):
		t.Fatal("an injected key moves it")
	}
	lt.c.overlayShown[o] = time.Now().Add(-overlayArmDelay)
	if !lt.c.overlayTakes(o, Named(KeyEnter)) {
		t.Fatal("no answer after its first moment")
	}
}

// While an app allowed all the time watches in the background, the status
// bar names it and a notification in Settings' name (no banner, not
// counted unread) offers to stop that; it goes while the app is in front
// and comes again when it leaves. Stop pauses the watch and refuses the
// app the location out of the foreground, with its background mode and
// so its mobile data while background data is off, until it is in front
// again.
func TestLocationBackgroundIndicator(t *testing.T) {
	maps, fg, sys := alwaysApp("com.example.maps"), locApp("com.example.fg"), settingsApp()
	maps.M.Background = []string{"location"}
	lt := newLocTest(t, maps, fg, sys)
	lt.c.sys = &App{M: &Manifest{ID: "system"}, system: true}
	lt.c.broker.perms[maps.M.ID] = map[string]string{ui.PermLocation: "granted", ui.PermLocationBackground: "granted"}
	lt.c.setForeground(maps)
	lt.req(maps, ui.CapLocationWatch, nil)
	lt.reply(maps)
	lt.pump("the receiver on", lt.gnss.on.Load)
	if _, kind := lt.c.statusMid(); kind != "" || len(lt.c.notifs) != 0 {
		t.Fatalf("in front: %q, %d notifications", kind, len(lt.c.notifs))
	}

	lt.c.setForeground(fg)
	lt.fix(beijing)
	if got := events(maps, ui.EventLocation); len(got) != 1 {
		t.Fatalf("the watch in the background: %+v", got)
	}
	if mid, kind := lt.c.statusMid(); kind != "location" || !strings.Contains(mid, maps.M.Name) {
		t.Fatalf("the status bar: %q (%s)", mid, kind)
	}
	n := bgNotif(lt.c, maps)
	if n == nil || n.app != sys || !n.seen || lt.c.banner != nil || !strings.Contains(n.n.Title, maps.M.Name) || len(n.n.Actions) != 1 || n.n.Actions[0].ID != "stop" {
		t.Fatalf("the notification: %+v (banner %v)", n, lt.c.banner)
	}
	if bg, _ := lt.c.locationView()["using_background"].([]string); strings.Join(bg, ",") != maps.M.ID {
		t.Fatalf("Settings › Location: %v", lt.c.locationView()["using_background"])
	}
	if !lt.c.runsInBackground(maps) || !lt.c.netFront(maps) {
		t.Fatal("watching with the location background mode, it is not kept running")
	}

	lt.c.setForeground(maps)
	if _, kind := lt.c.statusMid(); kind == "location" || bgNotif(lt.c, maps) != nil {
		t.Fatalf("in front again: %q, %+v", kind, bgNotif(lt.c, maps))
	}
	lt.c.setForeground(fg)
	if bgNotif(lt.c, maps) == nil {
		t.Fatal("no notification when it left again")
	}
	lt.c.loc.lastAt = time.Now().Add(-time.Minute)
	lt.req(maps, ui.CapLocationGet, nil)
	lt.c.sendNotifAction(bgNotif(lt.c, maps), "stop", "")
	if r := lt.reply(maps); r.Err != ui.ErrLocationBackground {
		t.Fatalf("the get waiting when stopped: %+v", r)
	}
	if got := events(maps, ui.EventLocationStopped); len(got) != 1 {
		t.Fatal("the app is not told")
	}
	lt.c.loc.watches[maps].sentAt = time.Now().Add(-time.Minute)
	lt.fix(location.Fix{Lat: 31.2304, Lon: 121.4737, AccuracyM: 5})
	if got := events(maps, ui.EventLocation); len(got) != 0 {
		t.Fatalf("stopped, the watch goes on: %+v", got)
	}
	if _, kind := lt.c.statusMid(); kind == "location" || bgNotif(lt.c, maps) != nil {
		t.Fatalf("stopped: %q, %+v", kind, bgNotif(lt.c, maps))
	}
	lt.req(maps, ui.CapLocationGet, ui.LocationGetArgs{MaxAgeS: 60})
	if r := lt.reply(maps); r.Err != ui.ErrLocationBackground {
		t.Fatalf("stopped, a get in the background: %+v", r)
	}
	if lt.c.runsInBackground(maps) || lt.c.netFront(maps) {
		t.Fatal("stopped, its background mode still keeps it running")
	}

	lt.c.setForeground(maps)
	if got := events(maps, ui.EventLocation); len(got) != 1 || got[0].Lat != 31.2304 {
		t.Fatalf("in front, the watch does not go on: %+v", got)
	}
	lt.c.setForeground(fg)
	if bgNotif(lt.c, maps) == nil || !lt.c.runsInBackground(maps) {
		t.Fatal("opened since, its location in the background does not go on")
	}
}
