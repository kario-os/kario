package shell

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kario-os/kario/location"
	"github.com/kario-os/kario/ui"
)

// fakeGNSS is a receiver the test hands fixes to; fail makes Run fail at
// once.
type fakeGNSS struct {
	fixes chan location.Fix
	runs  atomic.Int32
	on    atomic.Bool
	fail  error
}

func (f *fakeGNSS) Run(ctx context.Context, fix func(location.Fix)) error {
	f.runs.Add(1)
	if f.fail != nil {
		return f.fail
	}
	f.on.Store(true)
	defer f.on.Store(false)
	for {
		select {
		case <-ctx.Done():
			return nil
		case x := <-f.fixes:
			fix(x)
		}
	}
}

var beijing = location.Fix{Lat: 39.9087, Lon: 116.3975, AccuracyM: 4.5, AltitudeM: 44.5, HasAltitude: true, HasSpeed: true, Satellites: 8}

type locTest struct {
	t    *testing.T
	c    *Core
	gnss *fakeGNSS
}

func newLocTest(t *testing.T, apps ...*App) *locTest {
	c := &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: t.TempDir()}, ev: make(chan func(), 64), apps: apps}
	c.broker = &broker{c: c, perms: map[string]map[string]string{}, usage: map[string]map[string]*permUse{}, stats: map[string]*CapStat{},
		path: filepath.Join(c.cfg.StateDir, "shell", "perms.json")}
	g := &fakeGNSS{fixes: make(chan location.Fix, 8)}
	c.locInit()
	c.loc.spec, c.loc.src, c.loc.sim = location.Spec{Kind: "test"}, g, nil
	c.loc.net = locNet{}
	return &locTest{t: t, c: c, gnss: g}
}

func locApp(id string) *App {
	return &App{M: &Manifest{ID: id, Name: id, Permissions: []string{ui.PermLocation}}, running: true, ready: true, out: make(chan outbound, 64)}
}

func (lt *locTest) grant(a *App, state string) {
	lt.c.broker.perms[a.M.ID] = map[string]string{ui.PermLocation: state}
}

// pump runs the loop's closures until cond holds.
func (lt *locTest) pump(what string, cond func() bool) {
	lt.t.Helper()
	end := time.After(5 * time.Second)
	for !cond() {
		select {
		case f := <-lt.c.ev:
			f()
		case <-time.After(5 * time.Millisecond):
		case <-end:
			lt.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// fix hands the receiver a fix and waits until the loop has it.
func (lt *locTest) fix(f location.Fix) {
	lt.t.Helper()
	before := lt.c.loc.fixes
	lt.gnss.fixes <- f
	lt.pump("the fix", func() bool { return lt.c.loc.fixes > before })
}

// req sends a capability request as the app; the reply comes on its queue.
func (lt *locTest) req(a *App, cap string, args any) {
	raw, _ := json.Marshal(args)
	lt.c.broker.handle(a, ui.Msg{T: ui.MsgReq, Req: 7, Cap: cap, Args: raw})
}

// next is the app's next message (a reply or an event), if one is queued.
func next(a *App) (ui.Msg, bool) {
	select {
	case o := <-a.out:
		return o.m, true
	default:
		return ui.Msg{}, false
	}
}

func (lt *locTest) reply(a *App) ui.Msg {
	lt.t.Helper()
	var m ui.Msg
	lt.pump("a reply to "+a.M.ID, func() bool {
		var ok bool
		for {
			if m, ok = next(a); !ok || m.T == ui.MsgRes {
				return ok
			}
		}
	})
	return m
}

func events(a *App, name string) []ui.Location {
	var out []ui.Location
	for {
		m, ok := next(a)
		if !ok {
			return out
		}
		if m.T == ui.MsgEvent && m.Event == name {
			var l ui.Location
			_ = json.Unmarshal([]byte(m.Value), &l)
			out = append(out, l)
		}
	}
}

// Only the foreground app gets the location, with the permission granted;
// the receiver comes on for the request and goes off after it.
func TestLocationGet(t *testing.T) {
	maps, bg := locApp("com.example.maps"), locApp("com.example.bg")
	lt := newLocTest(t, maps, bg)
	lt.grant(maps, "granted")
	lt.grant(bg, "granted")
	lt.c.fg = maps
	lt.req(bg, ui.CapLocationGet, nil)
	if r := lt.reply(bg); r.Err != ui.ErrLocationBackground {
		t.Fatalf("background app: %+v", r)
	}
	lt.req(maps, ui.CapLocationGet, nil)
	if _, ok := next(maps); ok || len(lt.c.loc.gets) != 1 || !lt.c.loc.running {
		t.Fatalf("the get did not wait for a fix with the receiver on (gets %d, on %v)", len(lt.c.loc.gets), lt.c.loc.running)
	}
	lt.pump("the receiver on", lt.gnss.on.Load)
	if !lt.c.locating() {
		t.Fatal("no indicator while the get waits")
	}
	lt.fix(beijing)
	r := lt.reply(maps)
	var l ui.Location
	if err := json.Unmarshal(r.Data, &l); err != nil || !r.OK {
		t.Fatalf("%+v %v", r, err)
	}
	if l.Lat != beijing.Lat || l.Lon != beijing.Lon || l.AccuracyM != 4.5 || l.AltitudeM == nil || *l.AltitudeM != 44.5 || l.SpeedMS == nil || l.Bearing != nil || l.Satellites != 8 || l.Approximate {
		t.Fatalf("%s", r.Data)
	}
	if !lt.c.locating() {
		t.Fatal("no indicator right after the fix was handed out")
	}
	lt.c.locTick()
	if !lt.c.loc.running {
		t.Fatal("the receiver went off at once")
	}
	lt.c.loc.idleAt = time.Now().Add(-locLinger)
	lt.c.locTick()
	lt.pump("the receiver off", func() bool { return !lt.gnss.on.Load() })
	if lt.c.loc.running || lt.gnss.runs.Load() != 1 {
		t.Fatalf("running %v, runs %d", lt.c.loc.running, lt.gnss.runs.Load())
	}
	lt.c.loc.handed = time.Now().Add(-locShown)
	if lt.c.locating() {
		t.Fatal("the indicator stays on")
	}
}

// A fix up to max_age_s old is handed out without the receiver; without
// max_age_s only a current one is.
func TestLocationMaxAge(t *testing.T) {
	maps := locApp("com.example.maps")
	lt := newLocTest(t, maps)
	lt.grant(maps, "granted")
	lt.c.fg = maps
	lt.c.loc.last, lt.c.loc.lastAt = beijing, time.Now().Add(-10*time.Second)
	lt.req(maps, ui.CapLocationGet, ui.LocationGetArgs{MaxAgeS: 30})
	if r := lt.reply(maps); !r.OK || lt.c.loc.running {
		t.Fatalf("a 10 s old fix with max_age_s 30: %+v, receiver %v", r, lt.c.loc.running)
	}
	lt.req(maps, ui.CapLocationGet, nil)
	if len(lt.c.loc.gets) != 1 || !lt.c.loc.running {
		t.Fatal("a 10 s old fix was taken for a current one")
	}
	for _, bad := range []ui.LocationGetArgs{{TimeoutS: -1}, {TimeoutS: 121}, {MaxAgeS: -5}} {
		lt.req(maps, ui.CapLocationGet, bad)
		if r := lt.reply(maps); r.OK {
			t.Errorf("%+v accepted", bad)
		}
	}
}

// A get without a fix in its time fails, saying why the receiver gave none.
func TestLocationTimeout(t *testing.T) {
	maps := locApp("com.example.maps")
	lt := newLocTest(t, maps)
	lt.grant(maps, "granted")
	lt.c.fg = maps
	lt.gnss.fail = errors.New("open /dev/gnss0: no such device")
	lt.req(maps, ui.CapLocationGet, ui.LocationGetArgs{TimeoutS: 5})
	lt.pump("the receiver's failure", func() bool { return lt.c.loc.err != "" })
	if lt.c.loc.running || lt.c.loc.retryAt.IsZero() {
		t.Fatal("a failed receiver is not retried later")
	}
	lt.c.locTick()
	if lt.gnss.runs.Load() != 1 {
		t.Fatal("retried before its time")
	}
	lt.c.loc.gets[0].deadline = time.Now()
	lt.c.locTick()
	r := lt.reply(maps)
	if !strings.HasPrefix(r.Err, ui.ErrLocationTimeout) || !strings.Contains(r.Err, "no such device") {
		t.Fatalf("%+v", r)
	}
	lt.gnss.fail = nil
	lt.req(maps, ui.CapLocationGet, nil)
	lt.c.loc.retryAt = time.Now()
	lt.c.locTick()
	lt.pump("the receiver on again", lt.gnss.on.Load)
	lt.fix(beijing)
	if r := lt.reply(maps); !r.OK || lt.c.loc.err != "" || lt.c.loc.backoff != 0 {
		t.Fatalf("%+v err %q", r, lt.c.loc.err)
	}
}

// A background app cannot raise the system's location dialog: it is
// refused before the system would ask the user, who is busy with another
// app (or with nothing at all).
func TestLocationBackgroundAsksNobody(t *testing.T) {
	bg, fg := locApp("com.example.bg"), locApp("com.example.fg")
	lt := newLocTest(t, bg, fg)
	lt.c.fg = fg
	for _, cap := range []string{ui.CapLocationGet, ui.CapLocationWatch} {
		lt.req(bg, cap, nil)
		if r := lt.reply(bg); r.Err != ui.ErrLocationBackground {
			t.Errorf("%s: %+v", cap, r)
		}
	}
	if len(lt.c.overlays) != 0 {
		t.Fatalf("a background app raised %T", lt.c.overlays[0])
	}
	lt.c.fg = bg
	lt.req(bg, ui.CapLocationGet, nil)
	if len(lt.c.overlays) != 1 {
		t.Fatal("the foreground app is not asked about")
	}
}

// The permission is the broker's: denied by the user, or not declared.
func TestLocationPermission(t *testing.T) {
	denied, undeclared := locApp("com.example.denied"), locApp("com.example.none")
	undeclared.M.Permissions = nil
	lt := newLocTest(t, denied, undeclared)
	lt.grant(denied, "denied")
	for _, a := range []*App{denied, undeclared} {
		lt.c.fg = a
		for _, cap := range []string{ui.CapLocationGet, ui.CapLocationWatch} {
			lt.req(a, cap, nil)
			if r := lt.reply(a); r.OK || !strings.Contains(r.Err, "permission") {
				t.Errorf("%s %s: %+v", a.M.ID, cap, r)
			}
		}
	}
	if lt.c.loc.running || len(lt.c.loc.watches) != 0 {
		t.Fatal("the receiver came on for a refused app")
	}
}

// A watch sends fixes while its app is in the foreground; in the
// background it pauses (the receiver goes off) and it goes on when the app
// comes back. location.stop ends it.
func TestLocationWatchPauses(t *testing.T) {
	maps, other := locApp("com.example.maps"), locApp("com.example.other")
	lt := newLocTest(t, maps, other)
	lt.grant(maps, "granted")
	lt.c.setForeground(maps)
	lt.req(maps, ui.CapLocationWatch, ui.LocationWatchArgs{IntervalS: 1})
	if r := lt.reply(maps); !r.OK {
		t.Fatalf("%+v", r)
	}
	lt.pump("the receiver on", lt.gnss.on.Load)
	lt.fix(beijing)
	if got := events(maps, ui.EventLocation); len(got) != 1 || got[0].Lat != beijing.Lat {
		t.Fatalf("events %+v", got)
	}
	if !lt.c.locating() {
		t.Fatal("no indicator during a watch")
	}
	lt.c.setForeground(other)
	lt.fix(location.Fix{Lat: 31.2304, Lon: 121.4737, AccuracyM: 5})
	if got := events(maps, ui.EventLocation); len(got) != 0 {
		t.Fatalf("a background app got %+v", got)
	}
	lt.c.loc.idleAt = time.Now().Add(-locLinger)
	lt.c.locTick()
	lt.pump("the receiver off", func() bool { return !lt.gnss.on.Load() })
	lt.c.loc.handed = time.Now().Add(-locShown)
	if lt.c.locating() || len(lt.c.loc.watches) != 1 {
		t.Fatalf("indicator %v, watches %d", lt.c.locating(), len(lt.c.loc.watches))
	}
	lt.c.loc.lastAt = time.Now()
	lt.c.setForeground(maps)
	if got := events(maps, ui.EventLocation); len(got) != 1 || got[0].Lat != 31.2304 {
		t.Fatalf("no current fix at once on coming back: %+v", got)
	}
	lt.pump("the receiver on again", lt.gnss.on.Load)
	if lt.gnss.runs.Load() != 2 {
		t.Fatalf("%d runs", lt.gnss.runs.Load())
	}
	lt.c.loc.watches[maps].sentAt = time.Now().Add(-time.Second)
	lt.fix(location.Fix{Lat: 22.5431, Lon: 114.0579, AccuracyM: 5})
	if got := events(maps, ui.EventLocation); len(got) != 1 || got[0].Lat != 22.5431 {
		t.Fatalf("after coming back: %+v", got)
	}
	lt.req(maps, ui.CapLocationStop, nil)
	if r := lt.reply(maps); !r.OK || len(lt.c.loc.watches) != 0 {
		t.Fatalf("%+v", r)
	}
	lt.c.locSync()
	if lt.c.loc.idleAt.IsZero() {
		t.Fatal("the receiver is not winding down after the stop")
	}
}

// A watch's events keep its interval and its distance; one that ended with
// its app or its grant gets nothing.
func TestLocationWatchFilters(t *testing.T) {
	maps := locApp("com.example.maps")
	lt := newLocTest(t, maps)
	lt.grant(maps, "granted")
	lt.c.fg = maps
	lt.req(maps, ui.CapLocationWatch, ui.LocationWatchArgs{IntervalS: 1, MinDistanceM: 100})
	lt.reply(maps)
	lt.pump("the receiver on", lt.gnss.on.Load)
	lt.fix(beijing)
	w := lt.c.loc.watches[maps]
	w.sentAt = time.Now().Add(-time.Hour)
	lt.fix(location.Fix{Lat: beijing.Lat + 0.0005, Lon: beijing.Lon, AccuracyM: 5})
	w.sentAt = time.Now()
	lt.fix(location.Fix{Lat: beijing.Lat + 0.01, Lon: beijing.Lon, AccuracyM: 5})
	w.sentAt = time.Now().Add(-time.Hour)
	lt.fix(location.Fix{Lat: beijing.Lat + 0.02, Lon: beijing.Lon, AccuracyM: 5})
	got := events(maps, ui.EventLocation)
	if len(got) != 2 || got[1].Lat != beijing.Lat+0.02 {
		t.Fatalf("55 m away, within the interval, then 2 km away after it: %+v", got)
	}
	lt.grant(maps, "denied")
	w.sentAt = time.Now().Add(-time.Hour)
	lt.fix(location.Fix{Lat: 0, Lon: 0, AccuracyM: 5})
	if got := events(maps, ui.EventLocation); len(got) != 0 {
		t.Fatalf("after the grant was taken back: %+v", got)
	}
	lt.c.locGone(maps)
	if len(lt.c.loc.watches) != 0 {
		t.Fatal("the watch outlived its app")
	}
	for _, bad := range []ui.LocationWatchArgs{{IntervalS: 0.5}, {IntervalS: 3601}, {MinDistanceM: -1}} {
		lt.grant(maps, "granted")
		lt.req(maps, ui.CapLocationWatch, bad)
		if r := lt.reply(maps); r.OK {
			t.Errorf("%+v accepted", bad)
		}
	}
}

// The switch in Settings turns location off for every app: the waiting get
// fails, the watch is told and stays silent, the receiver goes off at
// once, new requests fail; on again, the watch goes on.
func TestLocationSwitch(t *testing.T) {
	maps := locApp("com.example.maps")
	lt := newLocTest(t, maps)
	lt.grant(maps, "granted")
	lt.c.fg = maps
	lt.req(maps, ui.CapLocationWatch, nil)
	lt.reply(maps)
	lt.req(maps, ui.CapLocationGet, nil)
	lt.pump("the receiver on", lt.gnss.on.Load)
	if ok, err := lt.c.locationSetting(ui.SettingArgs{Key: ui.SettingLocation, Value: 0}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if r := lt.reply(maps); r.Err != ui.ErrLocationOff {
		t.Fatalf("the waiting get: %+v", r)
	}
	off := false
	for m, ok := next(maps); ok; m, ok = next(maps) {
		off = off || (m.T == ui.MsgEvent && m.Event == ui.EventLocationOff)
	}
	if !off || lt.c.loc.running || lt.c.locating() {
		t.Fatalf("told %v, receiver %v, indicator %v", off, lt.c.loc.running, lt.c.locating())
	}
	lt.pump("the receiver off", func() bool { return !lt.gnss.on.Load() })
	for _, cap := range []string{ui.CapLocationGet, ui.CapLocationWatch} {
		lt.req(maps, cap, nil)
		if r := lt.reply(maps); r.Err != ui.ErrLocationOff {
			t.Errorf("%s while off: %+v", cap, r)
		}
	}
	lt.c.profile = &Profile{Providers: map[string]string{ui.CapLocationGet: "service:gnss"}}
	lt.req(maps, ui.CapLocationGet, nil)
	if r := lt.reply(maps); r.Err != ui.ErrLocationOff {
		t.Errorf("a provider service got the request while off: %+v", r)
	}
	lt.c.profile = nil
	if !lt.c.settings.LocationOff || lt.c.locationView()["on"] != false {
		t.Fatal("the switch is not kept")
	}
	lt.c.locationSetting(ui.SettingArgs{Key: ui.SettingLocation, Value: 1})
	lt.pump("the receiver on again", lt.gnss.on.Load)
	lt.fix(beijing)
	if got := events(maps, ui.EventLocation); len(got) != 1 {
		t.Fatalf("the watch after location came back: %+v", got)
	}
}

// An app the user gives only its approximate location gets the centre of
// a cell of about 2 km and nothing more; Settings names it.
func TestLocationApproximate(t *testing.T) {
	maps := locApp("com.example.maps")
	lt := newLocTest(t, maps)
	lt.grant(maps, "granted")
	lt.c.fg = maps
	if ok, err := lt.c.locationSetting(ui.SettingArgs{Key: ui.SettingLocationApprox, Value: 1, Text: maps.M.ID}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, err := lt.c.locationSetting(ui.SettingArgs{Key: ui.SettingLocationApprox, Value: 1, Text: "com.example.nobody"}); err == nil {
		t.Fatal("an unknown app was made approximate")
	}
	lt.req(maps, ui.CapLocationGet, nil)
	lt.pump("the receiver on", lt.gnss.on.Load)
	lt.fix(beijing)
	var l ui.Location
	_ = json.Unmarshal(lt.reply(maps).Data, &l)
	if !l.Approximate || l.AccuracyM != approxM || l.AltitudeM != nil || l.SpeedMS != nil || l.Satellites != 0 {
		t.Fatalf("%+v", l)
	}
	if d := distanceM(l.Lat, l.Lon, beijing.Lat, beijing.Lon); d > approxM*0.75 || d < 1 {
		t.Fatalf("%.0f m from the fix", d)
	}
	if v := lt.c.locationView()["approximate"].([]string); len(v) != 1 || v[0] != maps.M.ID {
		t.Fatalf("%v", v)
	}
}

// Settings › Location lists who had the location in the last day, kept
// across a restart of the Shell.
func TestLocationRecent(t *testing.T) {
	maps := locApp("com.example.maps")
	lt := newLocTest(t, maps)
	lt.grant(maps, "granted")
	lt.c.fg = maps
	lt.c.loc.last, lt.c.loc.lastAt = beijing, time.Now()
	lt.req(maps, ui.CapLocationGet, nil)
	lt.reply(maps)
	recent := lt.c.locationView()["recent"].([]locAccess)
	if len(recent) != 1 || recent[0].App != maps.M.ID || recent[0].Name != maps.M.ID {
		t.Fatalf("%+v", recent)
	}
	again := &Core{log: lt.c.log, cfg: lt.c.cfg, apps: lt.c.apps}
	if r := again.locationView()["recent"].([]locAccess); len(r) != 1 || again.loc.grid != lt.c.loc.grid {
		t.Fatalf("after a restart: %+v, grid %v / %v", r, again.loc.grid, lt.c.loc.grid)
	}
	again.clockSkew = locRecent + time.Minute
	if r := again.locationView()["recent"].([]locAccess); len(r) != 0 {
		t.Fatalf("a day later: %+v", r)
	}
}

// The developer machine runs the simulated receiver through the real NMEA
// path; a phone whose SKU names no receiver has none.
func TestLocationReceiverChoice(t *testing.T) {
	for _, c := range []struct {
		sku  SKU
		spec string
	}{
		{SKU{}, "sim"},
		{SKU{ID: "qemu-t9", GNSS: "fifo:gnss.nmea"}, "fifo:/run/phone/gnss.nmea"},
		{SKU{ID: "x", GNSS: "fifo:/var/gnss.nmea"}, "fifo:/var/gnss.nmea"},
		{SKU{ID: "kario-t9-sd662-2g", GNSS: "modemmanager"}, "modemmanager"},
		{SKU{ID: "kario-t9-sd662-2g", GNSS: "nmea:/dev/gnss0"}, "nmea:/dev/gnss0"},
		{SKU{ID: "no-gnss"}, ""},
		{SKU{ID: "bad", GNSS: "gps"}, ""},
	} {
		core := &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: t.TempDir(), RunDir: "/run/phone", SKU: c.sku}}
		core.locInit()
		if core.loc.spec.String() != c.spec || (c.spec == "") != (core.loc.src == nil) {
			t.Errorf("%+v: %q", c.sku, core.loc.spec)
		}
	}
	maps := locApp("com.example.maps")
	core := &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: t.TempDir(), SKU: SKU{ID: "no-gnss"}}, fg: maps, apps: []*App{maps}}
	if err := core.locCheck(maps, maps.M.ID); err == nil || err.Error() != ui.ErrLocationUnavailable {
		t.Fatalf("%v", err)
	}
	lt := newLocTest(t, maps)
	lt.grant(maps, "granted")
	lt.c.fg = maps
	sim := location.NewSim(nil)
	sim.Interval, sim.TTFF = 10*time.Millisecond, 1
	lt.c.loc.src, lt.c.loc.sim = sim, sim
	lt.req(maps, ui.CapLocationGet, nil)
	var l ui.Location
	_ = json.Unmarshal(lt.reply(maps).Data, &l)
	if math.Abs(l.Lat-location.SimLat) > 1e-6 || math.Abs(l.Lon-location.SimLon) > 1e-6 || l.Satellites != 8 || *l.AltitudeM != 44.5 {
		t.Fatalf("%+v", l)
	}
}
