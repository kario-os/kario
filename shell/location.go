package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kario-os/kario/location"
	"github.com/kario-os/kario/ui"
)

// Location. The receiver the SKU names (sku.json "gnss"; the developer
// machine's is simulated) runs off the loop, and only while an app needs a
// fix: a location.get waiting for one, or a location.watch of the app in
// the foreground. Only the foreground app is served, or one the user
// allowed the location all the time (a second dialog, location.background,
// or Settings) while it is not frozen; a watch pauses while its app may
// not have it and goes on when it may. An app using the location in the
// background is named in the status bar and in a notification that can
// stop that until it is opened again. The switch in Settings › Location
// turns location off for every app. While the receiver gives no fix, the
// product's network location service may place the phone by Wi-Fi and
// the cell (location_net.go). Apps get coordinates only; turning them into
// a place name is the app's (or its server's) business.

const (
	// locShown: the status bar shows location in use this long after a
	// fix was handed out.
	locShown = 3 * time.Second
	// locFresh: a fix this old is current (receivers give one a second).
	locFresh          = 2 * time.Second
	locDefaultTimeout = 30 * time.Second
	locMaxTimeout     = 120 * time.Second
	locMaxAge         = 24 * time.Hour
	locMaxInterval    = time.Hour
	locMaxDistanceM   = 1000e3
	// locLinger keeps the receiver on after the last need, so the next
	// request finds it warm.
	locLinger = 5 * time.Second
	// locRetry is the first wait before a failed receiver is tried again;
	// it doubles up to locRetryMax.
	locRetry    = 5 * time.Second
	locRetryMax = time.Minute
	// locRecent is how far back Settings › Location shows which apps had
	// the location; locSaveEvery spaces the writes of that record.
	locRecent    = 24 * time.Hour
	locSaveEvery = time.Minute
	// approxM is the side of the cells an approximate fix is snapped to;
	// an approximate watch hears of the same cell again only this often
	// (Android's coarse interval).
	approxM        = location.CoarseCellM
	locApproxEvery = 10 * time.Minute
	// An app that used the location in the background is named in a
	// reminder (Settings' notification) at most every locRemindEvery;
	// whether one is due is looked at every locRemindCheck.
	locRemindEvery = 7 * 24 * time.Hour
	locRemindCheck = time.Hour
)

type locShell struct {
	ready bool
	spec  location.Spec
	src   location.Source
	// sim is the simulated receiver: the source itself, or what the
	// selftest feeds a named pipe with (feed); simNow the same for the
	// fake network location service, off the loop.
	sim    *location.Sim
	simNow atomic.Pointer[location.Sim]
	feed   context.CancelFunc

	running bool
	cancel  context.CancelFunc
	gen     int
	started time.Time
	fixes   int
	idleAt  time.Time
	retryAt time.Time
	backoff time.Duration
	err     string
	// needSince: since when someone needs the location (unset: nobody).
	needSince time.Time

	// last is the last fix (the receiver's or the network's); gnssAt when
	// the receiver last gave one.
	last   location.Fix
	lastAt time.Time
	gnssAt time.Time

	gets    []*locGet
	watches map[*App]*locWatch
	handed  time.Time

	recent  map[string]*locAccess
	grid    [2]float64
	coarse  location.Coarse
	savedAt time.Time
	// reminded: when each app was last named in the reminder of
	// background use (unix ms); remindAt: the last look.
	reminded map[string]int64
	remindAt time.Time

	// stopped: the apps (their keys) whose location in the background the
	// user stopped from its notification, until they are in the
	// foreground again; bgShown: the apps that notification is up for;
	// bgHanded: when each app was last handed a fix in the background.
	stopped  map[string]bool
	bgShown  map[string]bool
	bgHanded map[*App]time.Time

	net    locNet
	assist *location.Assist
}

type locGet struct {
	app      *App
	key      string
	deadline time.Time
	timeout  time.Duration
	approx   bool
	reply    func(any, error)
}

type locWatch struct {
	app      *App
	key      string
	interval time.Duration
	minDist  float64
	approx   bool
	sent     *ui.Location
	sentAt   time.Time
}

// locAccess is one app's last use of the location (Settings › Location);
// BgAt its last use in the background.
type locAccess struct {
	App  string `json:"app"`
	Name string `json:"name"`
	At   int64  `json:"at"` // unix ms
	BgAt int64  `json:"bg_at,omitempty"`
}

func (c *Core) locStatePath() string { return filepath.Join(c.cfg.StateDir, "shell", "location.json") }

// locInit picks the receiver the first time location is asked about.
func (c *Core) locInit() {
	if c.loc.ready {
		return
	}
	c.loc.ready = true
	if c.loc.watches == nil {
		c.loc.watches = map[*App]*locWatch{}
	}
	c.loc.stopped, c.loc.bgShown, c.loc.bgHanded = map[string]bool{}, map[string]bool{}, map[*App]time.Time{}
	spec := location.Spec{Kind: location.KindSim}
	switch g := c.cfg.SKU.GNSS; {
	case g != "":
		s, err := location.ParseSpec(g)
		if err != nil {
			c.log.Printf("location: %v; no receiver", err)
		}
		if s.Kind == location.KindFIFO && !filepath.IsAbs(s.Path) {
			s.Path = filepath.Join(c.cfg.RunDir, s.Path)
		}
		spec = s
	case c.cfg.SKU.ID != "":
		spec = location.Spec{}
	}
	var saved struct {
		Recent   map[string]*locAccess `json:"recent"`
		Grid     [2]float64            `json:"grid"`
		Reminded map[string]int64      `json:"reminded"`
	}
	if raw, err := os.ReadFile(c.locStatePath()); err == nil {
		_ = json.Unmarshal(raw, &saved)
	}
	c.loc.recent, c.loc.grid, c.loc.reminded = saved.Recent, saved.Grid, saved.Reminded
	if c.loc.recent == nil {
		c.loc.recent = map[string]*locAccess{}
	}
	if c.loc.reminded == nil {
		c.loc.reminded = map[string]int64{}
	}
	if c.loc.grid == [2]float64{} {
		c.loc.grid = [2]float64{rand.Float64(), rand.Float64()}
		c.saveLocState()
	}
	c.loc.coarse = location.Coarse{Offset: c.loc.grid}
	c.locNetInit()
	c.locUse(spec)
}

// locUse sets the receiver (an empty spec: none), stopping the one on.
func (c *Core) locUse(spec location.Spec) {
	if c.loc.running {
		c.locStop()
	}
	if c.loc.feed != nil {
		c.loc.feed()
		c.loc.feed = nil
	}
	c.loc.spec, c.loc.src, c.loc.sim = spec, nil, nil
	c.loc.simNow.Store(nil)
	c.loc.err, c.loc.backoff, c.loc.retryAt = "", 0, time.Time{}
	c.loc.lastAt, c.loc.gnssAt = time.Time{}, time.Time{}
	if spec.Kind == "" {
		c.log.Printf("location: no receiver")
		return
	}
	c.loc.src = location.New(spec, time.Now)
	if sim, ok := c.loc.src.(*location.Sim); ok {
		c.loc.sim = sim
	}
	c.loc.simNow.Store(c.loc.sim)
	if mm, ok := c.loc.src.(*location.ModemManager); ok {
		c.locAssist(mm)
	}
	c.log.Printf("location: receiver %s", spec)
}

func (c *Core) saveLocState() {
	raw, _ := json.Marshal(map[string]any{"recent": c.loc.recent, "grid": c.loc.grid, "reminded": c.loc.reminded})
	_ = os.MkdirAll(filepath.Dir(c.locStatePath()), 0o700)
	_ = os.WriteFile(c.locStatePath(), raw, 0o600)
	c.loc.savedAt = time.Now()
}

// locBackground: the app may have the location out of the foreground: the
// user allowed it all the time, for the app itself (a mini-program never
// has it), did not stop it from the notification since the app was last
// in the foreground, and it is not frozen.
func (c *Core) locBackground(a *App, key string) bool {
	return key == a.M.ID && !a.frozen && !c.loc.stopped[key] && c.broker.holds(a, ui.PermLocation) && c.broker.holds(a, ui.PermLocationBackground)
}

// locAvailable: something can place the phone: a receiver, or the network
// location service while the user lets it.
func (c *Core) locAvailable() bool {
	return c.loc.src != nil || (c.loc.net.loc != nil && !c.settings.LocationNetworkOff)
}

// locCheck is why the app cannot have the location now, if it cannot.
func (c *Core) locCheck(a *App, key string) error {
	c.locInit()
	switch {
	case c.settings.LocationOff:
		return errors.New(ui.ErrLocationOff)
	case c.fg != a && !c.locBackground(a, key):
		return errors.New(ui.ErrLocationBackground)
	case !c.locAvailable():
		return errors.New(ui.ErrLocationUnavailable)
	}
	return nil
}

// locRefused refuses location.get and location.watch before the user is
// asked for the permission, and before a provider service (profile.go)
// gets them: a background app raises no system dialog, and has the
// location only if the user allowed it all the time. location.background
// is the foreground app's own (never a mini-program's), and only the
// second step: the location while in use comes first (Android 11 ignores
// a request for both at once).
func (c *Core) locRefused(a *App, cap, key string) error {
	switch {
	case cap == ui.CapLocationBackground && (key != a.M.ID || c.fg != a):
		return errors.New(ui.ErrLocationBackground)
	case cap == ui.CapLocationBackground && !c.broker.holds(a, ui.PermLocation):
		return errors.New(ui.ErrLocationWhileInUse)
	case cap != ui.CapLocationGet && cap != ui.CapLocationWatch:
		return nil
	case c.settings.LocationOff:
		return errors.New(ui.ErrLocationOff)
	case c.fg != a && !c.locBackground(a, key):
		return errors.New(ui.ErrLocationBackground)
	}
	return nil
}

// locApproxOnly: a location request that asks for the approximate
// location only (the dialog then offers only that).
func locApproxOnly(m ui.Msg) bool {
	if m.Cap != ui.CapLocationGet && m.Cap != ui.CapLocationWatch {
		return false
	}
	var args struct {
		Approximate bool `json:"approximate"`
	}
	_ = json.Unmarshal(m.Args, &args)
	return args.Approximate
}

func secs(v float64) time.Duration { return time.Duration(v * float64(time.Second)) }

func (c *Core) locationGet(a *App, key string, args ui.LocationGetArgs, reply func(any, error)) {
	if err := c.locCheck(a, key); err != nil {
		reply(nil, err)
		return
	}
	timeout := locDefaultTimeout
	switch t := secs(args.TimeoutS); {
	case args.TimeoutS < 0 || t > locMaxTimeout || math.IsNaN(args.TimeoutS):
		reply(nil, fmt.Errorf("timeout_s must be up to %d", int(locMaxTimeout/time.Second)))
		return
	case t > 0:
		timeout = t
	}
	maxAge := locFresh
	if args.MaxAgeS < 0 || math.IsNaN(args.MaxAgeS) {
		reply(nil, errors.New("max_age_s cannot be negative"))
		return
	} else if args.MaxAgeS > 0 {
		maxAge = max(locFresh, min(locMaxAge, secs(args.MaxAgeS)))
	}
	if !c.loc.lastAt.IsZero() && time.Since(c.loc.lastAt) <= maxAge {
		reply(c.locHand(a, key, args.Approximate), nil)
		return
	}
	c.loc.gets = append(c.loc.gets, &locGet{app: a, key: key, timeout: timeout, deadline: time.Now().Add(timeout), approx: args.Approximate, reply: reply})
	c.log.Printf("%s: location: waiting for a fix (up to %s)", a.M.ID, timeout)
	c.locSync()
}

func (c *Core) locationWatch(a *App, key string, args ui.LocationWatchArgs, reply func(any, error)) {
	if err := c.locCheck(a, key); err != nil {
		reply(nil, err)
		return
	}
	interval := time.Second
	switch t := secs(args.IntervalS); {
	case args.IntervalS < 0 || math.IsNaN(args.IntervalS) || (args.IntervalS > 0 && t < time.Second) || t > locMaxInterval:
		reply(nil, fmt.Errorf("interval_s must be 1 to %d", int(locMaxInterval/time.Second)))
		return
	case t > 0:
		interval = t
	}
	if args.MinDistanceM < 0 || args.MinDistanceM > locMaxDistanceM || math.IsNaN(args.MinDistanceM) {
		reply(nil, fmt.Errorf("min_distance_m must be 0 to %d", int(locMaxDistanceM)))
		return
	}
	w := &locWatch{app: a, key: key, interval: interval, minDist: args.MinDistanceM, approx: args.Approximate}
	c.loc.watches[a] = w
	c.log.Printf("%s: location: watching (every %s, %.0f m)", a.M.ID, interval, w.minDist)
	reply(map[string]bool{"watching": true}, nil)
	c.locCatchUp(w)
	c.locSync()
}

// locationBackground answers location.background once the user allowed the
// location all the time (in its dialog now, or before).
func (c *Core) locationBackground(a *App) any {
	c.locInit()
	c.log.Printf("%s: location allowed all the time", a.M.ID)
	c.locSync()
	return map[string]bool{"background": true} // const-check: ok (the result's field, not the lifecycle state)
}

func (c *Core) locationStop(a *App) any {
	if _, ok := c.loc.watches[a]; ok {
		delete(c.loc.watches, a)
		c.log.Printf("%s: location: watch stopped", a.M.ID)
		c.locSync()
	}
	return map[string]bool{"watching": false}
}

// locGranted: the grant behind a request still holds (the user may take
// it back in Settings while an app watches).
func (c *Core) locGranted(a *App, key string) bool {
	if key == a.M.ID {
		return c.broker.holds(a, ui.PermLocation)
	}
	return a.M.Has(ui.PermLocation) && c.broker.perms[key][ui.PermLocation] == "granted"
}

// locActive: a watch that gets fixes now.
func (c *Core) locActive(w *locWatch) bool {
	return !c.settings.LocationOff && w.app.running && c.locGranted(w.app, w.key) && (c.fg == w.app || c.locBackground(w.app, w.key))
}

// locKeepsRunning: the app declares the location background mode, may
// have the location all the time and watches it: it is not frozen.
func (c *Core) locKeepsRunning(a *App) bool {
	w := c.loc.watches[a]
	return w != nil && !c.settings.LocationOff && w.key == a.M.ID && !c.loc.stopped[w.key] &&
		c.broker.holds(a, ui.PermLocation) && c.broker.holds(a, ui.PermLocationBackground)
}

func (c *Core) locNeeded() bool {
	if c.settings.LocationOff || !c.locAvailable() {
		return false
	}
	if len(c.loc.gets) > 0 || c.familyLocating() {
		return true
	}
	for _, w := range c.loc.watches {
		if c.locActive(w) {
			return true
		}
	}
	return false
}

// locSync turns the receiver on when someone needs it, off locLinger after
// the last (at once when location is switched off).
func (c *Core) locSync() {
	need := c.locNeeded()
	switch {
	case need && c.loc.needSince.IsZero():
		c.loc.needSince = time.Now()
	case !need:
		c.loc.needSince = time.Time{}
	}
	switch {
	case need && !c.loc.running && c.loc.src != nil && !time.Now().Before(c.loc.retryAt):
		c.locStart()
	case need:
		c.loc.idleAt = time.Time{}
	case c.loc.running && c.settings.LocationOff:
		c.locStop()
	case c.loc.running && c.loc.idleAt.IsZero():
		c.loc.idleAt = time.Now()
	case c.loc.running && time.Since(c.loc.idleAt) >= locLinger:
		c.locStop()
	}
	c.locBgNotify()
}

func (c *Core) locStart() {
	c.loc.gen++
	gen, src := c.loc.gen, c.loc.src
	ctx, cancel := context.WithCancel(context.Background())
	c.loc.running, c.loc.cancel, c.loc.started, c.loc.fixes, c.loc.idleAt = true, cancel, time.Now(), 0, time.Time{}
	c.log.Printf("location: %s on", c.loc.spec)
	go func() {
		err := src.Run(ctx, func(f location.Fix) { c.Post(func() { c.locFix(gen, f) }) })
		c.Post(func() { c.locEnded(gen, err) })
	}()
}

func (c *Core) locStop() {
	if !c.loc.running {
		return
	}
	c.loc.cancel()
	c.loc.running, c.loc.cancel, c.loc.idleAt = false, nil, time.Time{}
	c.loc.gen++
	c.log.Printf("location: %s off (%d fixes in %s)", c.loc.spec, c.loc.fixes, time.Since(c.loc.started).Round(time.Second))
}

// locEnded: the receiver stopped by itself; it is tried again later while
// it is needed.
func (c *Core) locEnded(gen int, err error) {
	if gen != c.loc.gen {
		return
	}
	c.loc.running, c.loc.cancel = false, nil
	if err == nil {
		err = errors.New("the receiver stopped")
	}
	c.loc.err = err.Error()
	c.loc.backoff = min(locRetryMax, max(locRetry, 2*c.loc.backoff))
	c.loc.retryAt = time.Now().Add(c.loc.backoff)
	c.log.Printf("location: %s failed: %v; again in %s", c.loc.spec, err, c.loc.backoff)
}

func (c *Core) locFix(gen int, f location.Fix) {
	if gen != c.loc.gen {
		return
	}
	if c.loc.fixes == 0 {
		c.log.Printf("location: first fix after %s: %.5f, %.5f ±%.1f m, %d satellites", time.Since(c.loc.started).Round(100*time.Millisecond), f.Lat, f.Lon, f.AccuracyM, f.Satellites)
	}
	c.loc.fixes++
	c.loc.gnssAt, c.loc.err, c.loc.backoff = time.Now(), "", 0
	c.locHandOut(f)
}

// locHandOut makes a fix the last one and gives it to whoever waits.
func (c *Core) locHandOut(f location.Fix) {
	c.loc.last, c.loc.lastAt = f, time.Now()
	gets := c.loc.gets
	c.loc.gets = nil
	for _, g := range gets {
		g.reply(c.locHand(g.app, g.key, g.approx), nil)
	}
	for _, w := range c.loc.watches {
		if c.locActive(w) {
			c.locDeliver(w)
		}
	}
	c.familyFixed()
	c.locSync()
}

// locDeliver sends a watch the last fix, if its interval has passed and it
// moved far enough since the last one sent (an approximate watch: to
// another cell, or locApproxEvery passed).
func (c *Core) locDeliver(w *locWatch) {
	l := c.locFor(w.app, w.key, w.approx)
	if w.sent != nil && (time.Since(w.sentAt) < w.interval*9/10 || distanceM(w.sent.Lat, w.sent.Lon, l.Lat, l.Lon) < w.minDist) {
		return
	}
	if w.sent != nil && l.Approximate && w.sent.Lat == l.Lat && w.sent.Lon == l.Lon && time.Since(w.sentAt) < locApproxEvery {
		return
	}
	raw, _ := json.Marshal(l)
	w.app.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventLocation, Value: string(raw)})
	w.sent, w.sentAt = &l, time.Now()
	c.locAccessed(w.app, w.key)
}

// locCatchUp gives a watch that starts or goes on a current fix at once.
func (c *Core) locCatchUp(w *locWatch) {
	if c.locActive(w) && !c.loc.lastAt.IsZero() && time.Since(c.loc.lastAt) <= locFresh {
		w.sent = nil
		c.locDeliver(w)
	}
}

// locHand is the last fix as the app may have it, and marks it handed out.
func (c *Core) locHand(a *App, key string, approx bool) ui.Location {
	l := c.locFor(a, key, approx)
	c.locAccessed(a, key)
	c.log.Printf("%s: location handed out (±%.0f m%s%s)", a.M.ID, l.AccuracyM, map[bool]string{true: ", approximate"}[l.Approximate],
		map[bool]string{true: ", in the background"}[c.fg != a])
	return l
}

// locFor is the last fix as the app may have it: only the centre of its
// cell, and nothing more, when the user allowed it only the approximate
// location or it asked for no more (approx).
func (c *Core) locFor(a *App, key string, approx bool) ui.Location {
	f := c.loc.last
	l := ui.Location{Lat: f.Lat, Lon: f.Lon, AccuracyM: math.Round(f.AccuracyM*10) / 10, At: c.now().Add(-time.Since(c.loc.lastAt)).Unix(), Source: ui.LocationSourceGNSS}
	if f.Network {
		l.Source = ui.LocationSourceNetwork
	}
	if approx || c.settings.LocationApprox[key] {
		l.Lat, l.Lon = c.loc.coarse.Snap(f.Lat, f.Lon)
		l.AccuracyM, l.Approximate = math.Max(approxM, l.AccuracyM), true
		return l
	}
	if f.HasAltitude {
		l.AltitudeM = &f.AltitudeM
	}
	if f.HasSpeed {
		l.SpeedMS = &f.SpeedMS
	}
	if f.HasBearing {
		l.Bearing = &f.Bearing
	}
	l.Satellites = f.Satellites
	return l
}

// distanceM is the great-circle distance between two positions.
func distanceM(lat1, lon1, lat2, lon2 float64) float64 {
	return location.Distance(lat1, lon1, lat2, lon2)
}

func (c *Core) locAccessed(a *App, key string) {
	c.loc.handed = time.Now()
	if c.loc.recent == nil {
		c.loc.recent = map[string]*locAccess{}
	}
	name := a.M.Name
	if origin, ok := strings.CutPrefix(key, a.M.ID+"/"); ok {
		name = tr.T("%s “%s”", a.M.Name, origin)
	}
	r, known := c.loc.recent[key]
	if !known {
		r = &locAccess{App: key}
		c.loc.recent[key] = r
	}
	now := c.now().UnixMilli()
	firstBg := c.fg != a && r.BgAt <= c.loc.reminded[key]
	r.Name, r.At = name, now
	if c.fg != a {
		r.BgAt = now
		c.loc.bgHanded[a] = time.Now()
	}
	if !known || firstBg || time.Since(c.loc.savedAt) >= locSaveEvery {
		c.saveLocState()
	}
}

// locRemind names, in a notification from Settings, the apps that used the
// location in the background since they were last named, if that was at
// least locRemindEvery ago (Android 10's reminder).
func (c *Core) locRemind() {
	now := c.now()
	if ago(now, c.loc.remindAt) < locRemindCheck {
		return
	}
	c.loc.remindAt = now
	var keys []string
	for key, r := range c.loc.recent {
		last := c.loc.reminded[key]
		if r.BgAt > last && now.UnixMilli()-last >= locRemindEvery.Milliseconds() && c.appByID(key) != nil {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return
	}
	sort.Strings(keys)
	var names []string
	for _, key := range keys {
		names = append(names, tr.T("“%s”", c.loc.recent[key].Name))
		c.loc.reminded[key] = now.UnixMilli()
	}
	c.saveLocState()
	c.log.Printf("location: reminding of background use by %s", strings.Join(keys, ", "))
	if s := c.appByID(healthApp); s != nil {
		c.addNotif(s, ui.Notification{ID: ui.NotifBgLocation, Title: tr.T("Apps used your location in the background"),
			Body: tr.T("%s used your location while you were not using it; Settings › Location can change that", strings.Join(names, ""))})
	}
}

// locForeground follows the foreground: a get the app that left was
// waiting with fails, its watch pauses, unless it may have the location
// all the time; the watch of the one that came back goes on, and its
// location in the background, if the user stopped it, may go on again.
func (c *Core) locForeground() {
	if !c.loc.ready {
		return
	}
	if c.fg != nil && c.loc.stopped[c.fg.M.ID] {
		delete(c.loc.stopped, c.fg.M.ID)
		c.log.Printf("%s: in the foreground; its location in the background may go on", c.fg.M.ID)
	}
	kept := c.loc.gets[:0]
	for _, g := range c.loc.gets {
		if g.app == c.fg || c.locBackground(g.app, g.key) {
			kept = append(kept, g)
			continue
		}
		g.reply(nil, errors.New(ui.ErrLocationBackground))
	}
	c.loc.gets = kept
	if w := c.loc.watches[c.fg]; w != nil {
		c.locCatchUp(w)
	}
	c.locSync()
}

// locGone forgets an app that ended.
func (c *Core) locGone(a *App) {
	if !c.loc.ready {
		return
	}
	delete(c.loc.watches, a)
	delete(c.loc.bgHanded, a)
	kept := c.loc.gets[:0]
	for _, g := range c.loc.gets {
		if g.app != a {
			kept = append(kept, g)
		}
	}
	c.loc.gets = kept
	c.locSync()
}

// locTick fails the gets past their time, turns the receiver on or off,
// asks the network while it gives no fix, and reminds of background use.
func (c *Core) locTick() {
	if !c.loc.ready {
		return
	}
	now := time.Now()
	kept := c.loc.gets[:0]
	for _, g := range c.loc.gets {
		if now.Before(g.deadline) {
			kept = append(kept, g)
			continue
		}
		why := ui.ErrLocationTimeout
		if c.loc.err != "" {
			why += " (" + c.loc.err + ")"
		}
		c.log.Printf("%s: location: no fix in %s", g.app.M.ID, g.timeout)
		g.reply(nil, errors.New(why))
	}
	c.loc.gets = kept
	c.locSync()
	c.locNetTick()
	c.locRemind()
}

// locating: an app has a watch getting fixes, a get waits for one, or a
// fix was handed out in the last locShown (the status bar's indicator).
func (c *Core) locating() bool {
	if !c.loc.handed.IsZero() && time.Since(c.loc.handed) < locShown {
		return true
	}
	if c.settings.LocationOff {
		return false
	}
	if len(c.loc.gets) > 0 {
		return true
	}
	for _, w := range c.loc.watches {
		if c.locActive(w) {
			return true
		}
	}
	return false
}

// locBgUsers are the apps using the location out of the foreground now
// (the status bar names them): a watch getting fixes, a get waiting for
// one, or a fix handed out in the last locShown.
func (c *Core) locBgUsers() []*App {
	if !c.loc.ready {
		return nil
	}
	seen := map[*App]bool{}
	var out []*App
	use := func(a *App) {
		if a != c.fg && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for a, at := range c.loc.bgHanded {
		if time.Since(at) < locShown {
			use(a)
		}
	}
	if !c.settings.LocationOff {
		for _, g := range c.loc.gets {
			use(g.app)
		}
		for _, w := range c.loc.watches {
			if c.locActive(w) {
				use(w.app)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].M.ID < out[j].M.ID })
	return out
}

// locBgNotify keeps a notification up, in Settings' name, for each app
// whose watch gets fixes in the background, naming it and offering to stop
// that (Android's foreground service notification, iOS's blue status
// bar). It comes once each time such a use begins, without a banner, a
// sound or the light (it says what goes on, it is not news), and goes when
// the use ends; one the user cleared stays away until the next.
func (c *Core) locBgNotify() {
	now := map[string]*App{}
	for _, w := range c.loc.watches {
		if w.app != c.fg && c.locActive(w) {
			now[w.key] = w.app
		}
	}
	s := c.appByID(healthApp)
	changed := false
	for key := range c.loc.bgShown {
		if now[key] == nil {
			delete(c.loc.bgShown, key)
			changed = c.locDropNotif(s, ui.NotifBgLocationUse+":"+key) || changed
		}
	}
	for key, a := range now {
		if c.loc.bgShown[key] || s == nil {
			continue
		}
		c.loc.bgShown[key] = true
		c.log.Printf("%s: using the location in the background; notified", key)
		c.locDropNotif(s, ui.NotifBgLocationUse+":"+key)
		c.notifs = append([]*notif{{app: s, at: time.Now(), seen: true, n: ui.Notification{ID: ui.NotifBgLocationUse + ":" + key,
			Title:   tr.T("“%s” is using your location in the background", a.M.Name),
			Body:    tr.T("Stop: not in the background again until you open it"),
			Actions: []ui.Action{{ID: "stop", Label: tr.T("Stop"), Key: "s"}}}}}, c.notifs...)
		c.trimNotifs(s)
		changed = true
	}
	if changed && c.sys != nil {
		c.refreshSystemTop()
	}
}

// locDropNotif takes the app's notification out of the centre, if it is
// there.
func (c *Core) locDropNotif(a *App, id string) bool {
	for i, x := range c.notifs {
		if x.app == a && x.n.ID == id {
			c.notifs = append(c.notifs[:i], c.notifs[i+1:]...)
			return true
		}
	}
	return false
}

// locNotifAction takes the actions of the notification of a use in the
// background (sendNotifAction); false for the other notifications.
func (c *Core) locNotifAction(n *notif, action string) bool {
	key, ok := strings.CutPrefix(n.n.ID, ui.NotifBgLocationUse+":")
	if !ok || n.app.M.ID != healthApp {
		return false
	}
	if action == "stop" {
		c.locStopBackground(key)
	}
	return true
}

// locStopBackground stops an app's location in the background, as the user
// asked in its notification: its watch stays, silent, its get waiting
// fails, and it has the location again only once it is in the foreground
// (or the user allows it all the time again in Settings). Not kept across
// a restart of the Shell.
func (c *Core) locStopBackground(key string) {
	a := c.appByID(key)
	if a == nil || a == c.fg || !c.loc.ready {
		return
	}
	c.loc.stopped[key] = true
	c.log.Printf("%s: location in the background stopped by the user", key)
	kept := c.loc.gets[:0]
	for _, g := range c.loc.gets {
		if g.app != a {
			kept = append(kept, g)
			continue
		}
		g.reply(nil, errors.New(ui.ErrLocationBackground))
	}
	c.loc.gets = kept
	delete(c.loc.bgHanded, a)
	if c.loc.watches[a] != nil {
		a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventLocationStopped})
	}
	c.locSync()
}

// locationSetting is settings.set for Settings › Location; false for the
// other keys.
func (c *Core) locationSetting(args ui.SettingArgs) (bool, error) {
	switch args.Key {
	case ui.SettingLocation:
		c.locInit()
		off := args.Value == 0
		if off == c.settings.LocationOff {
			return true, nil
		}
		c.settings.LocationOff = off
		c.saveSettings()
		c.log.Printf("location turned %s by the user", map[bool]string{true: "off", false: "on"}[off])
		if off {
			for _, g := range c.loc.gets {
				g.reply(nil, errors.New(ui.ErrLocationOff))
			}
			c.loc.gets = nil
			for a := range c.loc.watches {
				a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventLocationOff})
			}
		} else {
			for _, w := range c.loc.watches {
				c.locCatchUp(w)
			}
		}
		c.locSync()
		return true, nil
	case ui.SettingLocationApprox:
		key := args.Text
		id, _, _ := strings.Cut(key, "/")
		if a := c.appByID(id); a == nil || !a.M.Has(ui.PermLocation) {
			return true, fmt.Errorf("%q does not use the location", key)
		}
		c.locSetApprox(key, args.Value != 0)
		return true, nil
	case ui.SettingLocationNetwork:
		c.locInit()
		return true, c.locSetNetwork(args.Value != 0)
	}
	return false, nil
}

// locSetApprox gives the app (a permission key) only its approximate
// location, or its precise one again.
func (c *Core) locSetApprox(key string, approx bool) {
	if c.settings.LocationApprox[key] == approx {
		return
	}
	if c.settings.LocationApprox == nil {
		c.settings.LocationApprox = map[string]bool{}
	}
	if approx {
		c.settings.LocationApprox[key] = true
	} else {
		delete(c.settings.LocationApprox, key)
	}
	c.saveSettings()
}

// locPermCheck refuses a perm.set the location's grants cannot take: all
// the time is for an app that declares it, not a mini-program, and is
// allowed or not (it is asked again once the location is).
func (c *Core) locPermCheck(args ui.PermSetArgs) error {
	if args.Perm != ui.PermLocationBackground {
		return nil
	}
	a := c.appByID(args.App)
	switch {
	case strings.Contains(args.App, "/"):
		return errors.New(tr.T("A mini-program cannot use the location in the background"))
	case a == nil || !a.M.Has(ui.PermLocationBackground):
		return errors.New(tr.T("“%s” does not ask for the location in the background", args.App))
	case args.State != "granted" && args.State != "denied":
		return fmt.Errorf("%s cannot be %q", args.Perm, args.State)
	}
	return nil
}

// locPermSet follows a perm.set of the location's grants: all the time
// allows the location as well, as Android's "Allow all the time" does;
// the location denied (or to be asked again) takes all the time back,
// and asked again, the precision too.
func (c *Core) locPermSet(key, perm, state string) {
	grants := c.broker.perms[key]
	switch {
	case perm == ui.PermLocationBackground && state == "granted":
		grants[ui.PermLocation] = "granted"
		c.clearReset(key, ui.PermLocation)
		delete(c.loc.stopped, key)
	case perm == ui.PermLocation && state != "granted":
		delete(grants, ui.PermLocationBackground)
		if state == "ask" {
			c.locSetApprox(key, false)
		}
	default:
		return
	}
	c.broker.save()
	if c.loc.ready {
		c.locSync()
	}
}

// locEntry is a location grant as Settings › App permissions lists it:
// the precision, and all the time "denied" until given, whether it was
// asked for yet or not.
func (c *Core) locEntry(e *ui.PermEntry) {
	switch e.Perm {
	case ui.PermLocation:
		e.Approximate = c.settings.LocationApprox[e.App]
	case ui.PermLocationBackground:
		if e.State == "ask" {
			e.State = "denied"
		}
	}
}

// locationView is settings.get's "location": the switch, the receiver,
// who uses the location now (and which of them in the background) and who
// had it in the last locRecent (and in the background), the apps given
// only their approximate location, the network location service and what
// the modem's receiver was assisted with.
func (c *Core) locationView() map[string]any {
	c.locInit()
	cutoff := c.now().Add(-locRecent).UnixMilli()
	recent := []locAccess{}
	for key, r := range c.loc.recent {
		id, _, _ := strings.Cut(key, "/")
		if r.At < cutoff || c.appByID(id) == nil {
			continue
		}
		x := *r
		if x.BgAt < cutoff {
			x.BgAt = 0
		}
		recent = append(recent, x)
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].At > recent[j].At })
	using := []string{}
	for _, w := range c.loc.watches {
		if c.locActive(w) {
			using = append(using, w.key)
		}
	}
	for _, g := range c.loc.gets {
		using = append(using, g.key)
	}
	sort.Strings(using)
	background := []string{}
	for _, a := range c.locBgUsers() {
		background = append(background, a.M.ID)
	}
	approx := []string{}
	for k := range c.settings.LocationApprox {
		if id, _, _ := strings.Cut(k, "/"); c.appByID(id) == nil {
			delete(c.settings.LocationApprox, k)
			continue
		}
		approx = append(approx, k)
	}
	sort.Strings(approx)
	out := map[string]any{"on": !c.settings.LocationOff, "receiver": c.loc.spec.Kind, "on_now": c.loc.running,
		"error": c.loc.err, "using": using, "using_background": background, "recent": recent, "approximate": approx, "network": c.locNetView()}
	if c.loc.assist != nil {
		out["assistance"] = c.loc.assist
	}
	return out
}
