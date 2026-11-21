package shell

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/netfw"
	"github.com/kario-os/kario/ui"
)

// Data usage (Settings › Mobile network › Data usage) and the mobile data
// plan. What each app, the hotspot's devices and the system sent and
// received is added up per day (local dates) and network kind: Wi-Fi,
// mobile data, and mobile data while roaming abroad, which is counted
// apart and not against the plan. The days are kept back to the start of
// the previous billing cycle, and at least dataKeepDays (data-usage.json).
// The counts come from:
//   - a phone with the per-app firewall (traffic.go): its per-uid counters
//     for the apps, and the network interfaces' own counters (Wi-Fi: wl…
//     but the hotspot's access point; mobile: the data links telephonyd
//     reports) for what no app's counter saw, the system line (the push
//     connection, the contacts sync, the Shell's own downloads);
//   - a phone whose kernel lacks the firewall's named counters (nf_tables
//     with stateful objects, Linux 4.10): the interfaces' counters alone,
//     by network kind;
//   - a developer machine (not root): nothing of its own; the selftest
//     charges bytes as if an app had used them (data-charge).
// The hotspot's devices are the hotspot line, by its access point's
// counters (hotspot_usage.go), against the network it shares.
//
// The plan: a monthly allowance, the day each cycle starts, a warning (a
// share of the allowance or an amount of its own) notified once a cycle,
// and optionally a hard limit that holds mobile data off once the
// allowance is used up. Turning mobile data on again (Settings asks first)
// goes past it until the cycle ends or the allowance is raised; a new
// cycle lets mobile data on again by itself. The counters are read every
// dataUsageEvery, so mobile data may go that much past a hard limit. Each
// app may have a limit of its own, which blocks its mobile data alone
// (checkAppLimits, netpolicy.go).

const (
	dataUsageEvery  = 30 * time.Second
	dataKeepDays    = 62
	dataWarnPercent = 80
	// hotspotWarnPercent: the hotspot's warning, of its daily limit.
	hotspotWarnPercent = 80
)

// dataCount is what a line used in a day, in bytes.
type dataCount struct {
	WifiRx   uint64 `json:"wifi_rx,omitempty"`
	WifiTx   uint64 `json:"wifi_tx,omitempty"`
	MobileRx uint64 `json:"mobile_rx,omitempty"`
	MobileTx uint64 `json:"mobile_tx,omitempty"`
	RoamRx   uint64 `json:"roam_rx,omitempty"`
	RoamTx   uint64 `json:"roam_tx,omitempty"`
}

func (d *dataCount) add(o dataCount) {
	d.WifiRx += o.WifiRx
	d.WifiTx += o.WifiTx
	d.MobileRx += o.MobileRx
	d.MobileTx += o.MobileTx
	d.RoamRx += o.RoamRx
	d.RoamTx += o.RoamTx
}

func (d dataCount) mobile() uint64   { return d.MobileRx + d.MobileTx }
func (d dataCount) wifi() uint64     { return d.WifiRx + d.WifiTx }
func (d dataCount) roaming() uint64  { return d.RoamRx + d.RoamTx }
func (d dataCount) sumLines() uint64 { return d.mobile() + d.wifi() + d.roaming() }

type dataUsage struct {
	// Days: date → line (an app's ID, ui.TrafficLine*) → bytes.
	Days map[string]map[string]*dataCount `json:"days"`
	// Warned, Reached: the cycle (its first day) and the level of the
	// last warning and of the last allowance used up notified. Override:
	// the cycle the user turned mobile data on in past the hard limit, at
	// the allowance OverrideLimit.
	Warned        string `json:"warned,omitempty"`
	Reached       string `json:"reached,omitempty"`
	Override      string `json:"override,omitempty"`
	OverrideLimit uint64 `json:"override_limit,omitempty"`
	// AppWarned, AppReached: as Warned and Reached, of each app's own
	// limit (netpolicy.go); over: the apps at theirs this cycle.
	AppWarned  map[string]string `json:"app_warned,omitempty"`
	AppReached map[string]string `json:"app_reached,omitempty"`
	over       map[string]bool
	// held: the hard limit holds mobile data off; dirty: there is more
	// than the file has.
	held, loaded, dirty bool
	// at: the last count; saved: when the file was last written.
	at, saved time.Time
	// links: each interface's counters at the last look (looked: there
	// was one); hotspot: what the hotspot's devices used since the last
	// count, which the interfaces carried as well.
	links   map[string][2]uint64
	looked  bool
	hotspot netfw.Usage
	// kept: what the selftest puts back (data-usage save, restore).
	kept *dataKept
}

type dataKept struct {
	ledger  []byte
	hotspot hotspotUsage
}

// dataCycle is a billing cycle: from its first day to the first of the
// next, local midnights.
type dataCycle struct{ start, end time.Time }

// cycleOf is the cycle t is in, cycles starting on day (1–31, 0 the 1st;
// a shorter month's last day stands in).
func cycleOf(t time.Time, day int) dataCycle {
	if day < 1 || day > 31 {
		day = 1
	}
	t = t.In(time.Local)
	start := cycleDay(t.Year(), t.Month(), day)
	if t.Before(start) {
		start = cycleDay(t.Year(), t.Month()-1, day)
	}
	return dataCycle{start, cycleDay(start.Year(), start.Month()+1, day)}
}

func cycleDay(y int, m time.Month, day int) time.Time {
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, time.Local).Day()
	return time.Date(y, m, min(day, last), 0, 0, 0, 0, time.Local)
}

func (c *Core) cycle() dataCycle { return cycleOf(c.now(), c.settings.DataCycleDay) }

func (c *Core) dataPath() string { return filepath.Join(c.cfg.StateDir, "shell", "data-usage.json") }

// dataLedger is the days and the plan's state, read the first time.
func (c *Core) dataLedger() *dataUsage {
	l := &c.radios.data
	if !l.loaded {
		if raw, err := os.ReadFile(c.dataPath()); err == nil {
			_ = json.Unmarshal(raw, l)
		}
		if l.Days == nil {
			l.Days = map[string]map[string]*dataCount{}
		}
		l.loaded = true
		cyc := c.cycle()
		lines := l.lines(cyc.start, cyc.end)
		l.held = c.dataHeld(l, cyc, sumLines(lines).mobile())
		l.over = appsOver(c.settings.AppDataLimit, lines)
	}
	return l
}

// appsOver are the apps at their own limit in a cycle of lines.
func appsOver(limits map[string]uint64, lines map[string]dataCount) map[string]bool {
	out := map[string]bool{}
	for id, limit := range limits {
		if limit > 0 && appUsed(lines[id]) >= limit {
			out[id] = true
		}
	}
	return out
}

// saveData writes what is new at most every dataUsageEvery, unless now,
// dropping the days no longer kept.
func (c *Core) saveData(now bool) {
	l := c.dataLedger()
	if !now && (!l.dirty || time.Since(l.saved) < dataUsageEvery) {
		return
	}
	from := c.dataKeepFrom()
	for d := range l.Days {
		if d < from {
			delete(l.Days, d)
		}
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return
	}
	if err := os.WriteFile(c.dataPath(), raw, 0o600); err != nil {
		c.log.Printf("data usage: %v", err)
		return
	}
	l.saved, l.dirty = time.Now(), false
}

// dataKeepFrom is the first day kept: the previous cycle's first, or
// dataKeepDays ago where that is earlier.
func (c *Core) dataKeepFrom() string {
	prev := cycleOf(c.cycle().start.AddDate(0, 0, -1), c.settings.DataCycleDay).start
	if back := c.now().AddDate(0, 0, -dataKeepDays); back.Before(prev) {
		prev = back
	}
	return prev.Format(time.DateOnly)
}

// lines adds the days from up to to per line.
func (l *dataUsage) lines(from, to time.Time) map[string]dataCount {
	out := map[string]dataCount{}
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		for line, n := range l.Days[d.Format(time.DateOnly)] {
			x := out[line]
			x.add(*n)
			out[line] = x
		}
	}
	return out
}

func sumLines(lines map[string]dataCount) dataCount {
	var t dataCount
	for _, n := range lines {
		t.add(n)
	}
	return t
}

func (c *Core) roamingAbroad() bool { return c.tel != nil && c.tel.Roaming == "international" }

// addData counts u for the line today; mobile bytes while the phone roams
// abroad count as roaming.
func (c *Core) addData(line string, u netfw.Usage) {
	if u.Total() == 0 {
		return
	}
	l := c.dataLedger()
	day := c.now().Format(time.DateOnly)
	if l.Days[day] == nil {
		l.Days[day] = map[string]*dataCount{}
	}
	d := l.Days[day][line]
	if d == nil {
		d = &dataCount{}
		l.Days[day][line] = d
	}
	l.dirty = true
	d.WifiRx += u.WifiRx
	d.WifiTx += u.WifiTx
	if c.roamingAbroad() {
		d.RoamRx += u.MobileRx
		d.RoamTx += u.MobileTx
	} else {
		d.MobileRx += u.MobileRx
		d.MobileTx += u.MobileTx
	}
}

// hotspotData counts what the hotspot's devices downloaded and uploaded
// against the network the phone shares: mobile data, unless that is a
// Wi-Fi interface.
func (c *Core) hotspotData(down, up uint64) {
	u := netfw.Usage{MobileRx: down, MobileTx: up}
	if strings.HasPrefix(c.hotspot.upstream, netfw.WifiPrefix) {
		u = netfw.Usage{WifiRx: down, WifiTx: up}
	}
	c.addData(ui.TrafficLineHotspot, u)
	c.dataLedger().hotspot.Add(u)
}

// countData adds what the counters moved since the last count to today,
// and looks at the plan.
func (c *Core) countData() {
	var links *netfw.Usage
	apps := c.harvestApps()
	if c.Sandboxed() {
		d := c.linkDelta()
		links = &d
	}
	c.countFrom(apps, links)
}

// countFrom counts what the apps used and, with the interfaces' counts,
// the rest as the system's.
func (c *Core) countFrom(apps map[string]netfw.Usage, links *netfw.Usage) {
	l := c.dataLedger()
	l.at = time.Now()
	for id, u := range apps {
		c.addData(id, u)
	}
	if links != nil {
		rest := *links
		for _, u := range apps {
			rest = less(rest, u)
		}
		c.addData(ui.TrafficLineSystem, less(rest, l.hotspot))
	}
	l.hotspot = netfw.Usage{}
	c.checkDataPlan()
}

// less is u without o, none below zero (the counters are read a moment
// apart).
func less(u, o netfw.Usage) netfw.Usage {
	sub := func(a, b uint64) uint64 {
		if a < b {
			return 0
		}
		return a - b
	}
	return netfw.Usage{WifiRx: sub(u.WifiRx, o.WifiRx), WifiTx: sub(u.WifiTx, o.WifiTx), MobileRx: sub(u.MobileRx, o.MobileRx), MobileTx: sub(u.MobileTx, o.MobileTx)}
}

// netLinks are the network interfaces; linkCounters what one received and
// sent. Variables for the tests.
var (
	netLinks = func() []string {
		ents, _ := os.ReadDir(netClassDir)
		var out []string
		for _, e := range ents {
			out = append(out, e.Name())
		}
		return out
	}
	linkCounters = ifaceCounters
)

// linkDelta is what the Wi-Fi interfaces (but the hotspot's access point)
// and the mobile data links telephonyd reports carried since the last
// look, by kind. An interface there at the first look counts from then,
// one that came later from nothing, and a counter that went down (the
// interface made again) from zero. The access point's interface is
// followed too, so that it counts on from where it was once it is the
// station again.
func (c *Core) linkDelta() netfw.Usage {
	l := c.dataLedger()
	mobile := map[string]bool{}
	if c.tel != nil {
		for _, i := range c.tel.DataIfaces {
			mobile[i] = true
		}
	}
	seen := map[string][2]uint64{}
	var out netfw.Usage
	for _, name := range netLinks() {
		wifi := strings.HasPrefix(name, netfw.WifiPrefix)
		if !wifi && !mobile[name] {
			continue
		}
		rx, tx, err := linkCounters(name)
		if err != nil {
			continue
		}
		seen[name] = [2]uint64{rx, tx}
		last, had := l.links[name]
		if !had && !l.looked || name == c.hotspot.iface {
			continue
		}
		drx, dtx := since(rx, last[0]), since(tx, last[1])
		if wifi {
			out.WifiRx, out.WifiTx = out.WifiRx+drx, out.WifiTx+dtx
		} else {
			out.MobileRx, out.MobileTx = out.MobileRx+drx, out.MobileTx+dtx
		}
	}
	l.links, l.looked = seen, true
	return out
}

// dataTick counts every dataUsageEvery (the shell's tick), once
// setupTraffic read the days.
func (c *Core) dataTick() {
	if l := &c.radios.data; !l.loaded || time.Since(l.at) < dataUsageEvery {
		return
	}
	c.countData()
	c.saveData(false)
}

func (s sysSettings) warnPercent() int {
	switch {
	case s.DataWarnOff || s.DataWarn > 0:
		return 0
	case s.DataWarnPercent > 0:
		return s.DataWarnPercent
	}
	return dataWarnPercent
}

// dataWarning is the warning's level in bytes, 0 for none.
func (s sysSettings) dataWarning() uint64 {
	if s.DataWarn > 0 && !s.DataWarnOff {
		return s.DataWarn
	}
	return s.DataLimit * uint64(s.warnPercent()) / 100
}

func (s sysSettings) dataPlan() ui.DataPlan {
	return ui.DataPlan{Limit: s.DataLimit, CycleDay: max(s.DataCycleDay, 1), Warning: s.dataWarning(), WarningPercent: s.warnPercent(), LimitOff: s.DataLimitOff}
}

// dataHeld: the hard limit holds mobile data off, unless the user went
// past it this cycle at an allowance no higher.
func (c *Core) dataHeld(l *dataUsage, cyc dataCycle, used uint64) bool {
	s := c.settings
	return s.DataLimitOff && s.DataLimit > 0 && used >= s.DataLimit && !c.dataPassed(l, cyc)
}

func (c *Core) dataPassed(l *dataUsage, cyc dataCycle) bool {
	return l.Override == cyc.start.Format(time.DateOnly) && c.settings.DataLimit <= l.OverrideLimit
}

// checkDataPlan notifies the warning and the allowance used up, once a
// cycle each (again for a new level), and holds mobile data off while the
// hard limit says so; so for each app's own limit (checkAppLimits).
func (c *Core) checkDataPlan() {
	l, s := c.dataLedger(), c.settings
	cyc := c.cycle()
	lines := l.lines(cyc.start, cyc.end)
	defer c.netPolicyCheck()
	c.checkAppLimits(cyc, lines)
	used := sumLines(lines).mobile()
	start, last := cyc.start.Format(time.DateOnly), monthDay(cyc.end.AddDate(0, 0, -1))
	at := func(level uint64) string { return start + "/" + strconv.FormatUint(level, 10) }
	warn := s.dataWarning()
	if l.Override != "" && (l.Override != start || s.DataLimit > l.OverrideLimit) {
		l.Override, l.OverrideLimit, l.dirty = "", 0, true
	}
	switch {
	case s.DataLimit > 0 && used >= s.DataLimit && l.Reached != at(s.DataLimit):
		l.Reached, l.Warned, l.dirty = at(s.DataLimit), at(warn), true
		c.log.Printf("data plan: %d of the %d bytes of the cycle from %s used", used, s.DataLimit, start)
		if s.DataLimitOff {
			c.dataNotify(tr.T("Mobile data is off"), tr.T("This cycle's %s of mobile data are used up. To go on anyway, turn mobile data on in Settings; the cycle runs until %s.", sizeText(s.DataLimit), last))
		} else {
			c.dataNotify(tr.T("Mobile data plan used up"), tr.T("%s of mobile data used this cycle, your plan's %s; mobile data stays on. The cycle runs until %s.", sizeText(used), sizeText(s.DataLimit), last))
		}
	case warn > 0 && used >= warn && (s.DataLimit == 0 || used < s.DataLimit) && l.Warned != at(warn):
		l.Warned, l.dirty = at(warn), true
		c.log.Printf("data plan: %d bytes used in the cycle from %s, the warning is at %d", used, start, warn)
		c.dataNotify(tr.T("Data warning"), tr.T("%s of mobile data used this cycle; the warning is at %s. The cycle runs until %s.", sizeText(used), sizeText(warn), last))
	}
	if held := c.dataHeld(l, cyc, used); held != l.held {
		wasOff := c.dataOff()
		l.held = held
		c.log.Printf("data plan: mobile data held off by the limit: %v", held)
		c.applyMobileData(wasOff)
		c.saveData(true)
	}
}

// dataNotify tells the user, in Settings' name, of the plan.
func (c *Core) dataNotify(title, body string) {
	if s := c.appByID(healthApp); s != nil {
		c.addNotif(s, ui.Notification{ID: ui.NotifDataUsage, Title: title, Body: body})
	}
}

// checkAppLimits notifies an app's own limit neared and used up, once a
// cycle each (again for a new limit), and keeps the apps at theirs, whose
// mobile data netPolicyCheck then blocks.
func (c *Core) checkAppLimits(cyc dataCycle, lines map[string]dataCount) {
	l := c.dataLedger()
	l.over = appsOver(c.settings.AppDataLimit, lines)
	start, last := cyc.start.Format(time.DateOnly), monthDay(cyc.end.AddDate(0, 0, -1))
	for _, id := range slices.Sorted(maps.Keys(c.settings.AppDataLimit)) {
		limit, a := c.settings.AppDataLimit[id], c.appByID(id)
		if a == nil || limit == 0 {
			continue
		}
		used, at := appUsed(lines[id]), start+"/"+strconv.FormatUint(limit, 10)
		switch {
		case used >= limit && l.AppReached[id] != at:
			l.AppReached, l.AppWarned = setLevel(l.AppReached, id, at), setLevel(l.AppWarned, id, at)
			l.dirty = true
			c.log.Printf("data usage: %s used its %d bytes of the cycle from %s: its mobile data is blocked", id, limit, start)
			c.appDataNotify(a, tr.T("“%s” used up its mobile data limit", a.M.Name),
				tr.T("Its %s for this cycle are used: until %s, or until you raise the limit in Settings, it gets Wi-Fi only.", sizeText(limit), last))
		case used < limit && used >= limit*ui.AppDataWarnPercent/100 && l.AppWarned[id] != at:
			l.AppWarned, l.dirty = setLevel(l.AppWarned, id, at), true
			c.log.Printf("data usage: %s used %d of its %d bytes of the cycle from %s", id, used, limit, start)
			c.appDataNotify(a, tr.T("“%s” is near its mobile data limit", a.M.Name),
				tr.T("%s of its %s used this cycle; at the limit its mobile data is blocked. The cycle runs until %s.", sizeText(used), sizeText(limit), last))
		}
	}
}

func setLevel(m map[string]string, id, at string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	m[id] = at
	return m
}

// appDataNotify tells the user, in Settings' name, of an app's own limit;
// opening it shows the app's page.
func (c *Core) appDataNotify(a *App, title, body string) {
	if s := c.appByID(healthApp); s != nil {
		c.addNotif(s, ui.Notification{ID: ui.NotifAppDataLimit + ":" + a.M.ID, Title: title, Body: body})
	}
}

// passDataLimit is the user turning mobile data on while the hard limit
// holds it off: it stays on until the cycle ends or the allowance rises.
func (c *Core) passDataLimit() {
	l, cyc := c.dataLedger(), c.cycle()
	l.Override, l.OverrideLimit, l.held = cyc.start.Format(time.DateOnly), c.settings.DataLimit, false
	c.log.Printf("data plan: mobile data on past the limit until %s", cyc.end.Format(time.DateOnly))
	c.saveData(true)
}

// setDataPlan is settings.set for the plan (ui.SettingData*).
func (c *Core) setDataPlan(key string, v float64) error {
	s := &c.settings
	if v < 0 || v != math.Trunc(v) || v > 1<<53 {
		return fmt.Errorf("%s takes a whole number, 0 or more", key)
	}
	switch key {
	case ui.SettingDataLimit:
		s.DataLimit = uint64(v)
	case ui.SettingDataCycleDay:
		if v < 1 || v > 31 {
			return fmt.Errorf("%s is a day of the month, 1 to 31", key)
		}
		s.DataCycleDay = int(v)
	case ui.SettingDataWarning:
		s.DataWarn = uint64(v)
		s.DataWarnOff = s.DataWarnOff && v == 0
	case ui.SettingDataWarningPercent:
		if v > 100 {
			return fmt.Errorf("%s is 1 to 100, or 0 for none", key)
		}
		s.DataWarnPercent, s.DataWarnOff, s.DataWarn = int(v), v == 0, 0
	case ui.SettingDataLimitOff:
		s.DataLimitOff = v != 0
	}
	p := s.dataPlan()
	c.log.Printf("data plan: %d bytes a month from day %d, warning at %d, mobile data off at the limit: %v", p.Limit, p.CycleDay, p.Warning, p.LimitOff)
	c.checkDataPlan()
	return nil
}

// forgetData puts an uninstalled app's bytes on the removed apps' line,
// and drops its own data settings.
func (c *Core) forgetData(id string) {
	c.forgetAppNet(id)
	l := &c.radios.data
	if !l.loaded {
		return
	}
	delete(l.AppWarned, id)
	delete(l.AppReached, id)
	delete(l.over, id)
	for _, day := range l.Days {
		n := day[id]
		if n == nil {
			continue
		}
		delete(day, id)
		if day[ui.TrafficLineRemoved] == nil {
			day[ui.TrafficLineRemoved] = &dataCount{}
		}
		day[ui.TrafficLineRemoved].add(*n)
	}
	c.saveData(true)
}

func (c *Core) dataSource() string {
	switch {
	case c.traffic != nil:
		return ui.TrafficSourceApps
	case c.Sandboxed():
		return ui.TrafficSourceLinks
	}
	return ui.TrafficSourceSim
}

// dataUsageView is Settings › Data usage, with the counters read now.
func (c *Core) dataUsageView() ui.TrafficUsage {
	c.countData()
	c.saveData(false)
	l, s := c.dataLedger(), c.settings
	cyc, now := c.cycle(), c.now().In(time.Local)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	prev := cycleOf(cyc.start.AddDate(0, 0, -1), s.DataCycleDay)
	lines := l.lines(cyc.start, cyc.end)
	v := ui.TrafficUsage{Source: c.dataSource(), Start: cyc.start.Format(time.DateOnly), End: cyc.end.Format(time.DateOnly),
		MobileToday: sumLines(l.lines(day, day.AddDate(0, 0, 1))).mobile(), PrevMobile: sumLines(l.lines(prev.start, prev.end)).mobile(),
		Lines: []ui.TrafficLine{}, Plan: s.dataPlan(), Held: l.held, Override: c.dataPassed(l, cyc), Apps: c.trafficApps(lines), DataSaver: s.DataSaver}
	for id, n := range lines {
		if n.sumLines() == 0 {
			continue
		}
		line := ui.TrafficLine{ID: id, MobileRx: n.MobileRx, MobileTx: n.MobileTx, WifiRx: n.WifiRx, WifiTx: n.WifiTx, RoamRx: n.RoamRx, RoamTx: n.RoamTx}
		if a := c.appByID(id); a != nil {
			line.Name = a.M.Name
		}
		v.Lines = append(v.Lines, line)
		v.Mobile, v.Wifi, v.Roaming = v.Mobile+n.mobile(), v.Wifi+n.wifi(), v.Roaming+n.roaming()
	}
	sort.Slice(v.Lines, func(i, j int) bool {
		a, b := v.Lines[i], v.Lines[j]
		ta, tb := a.MobileRx+a.MobileTx+a.RoamRx+a.RoamTx, b.MobileRx+b.MobileTx+b.RoamRx+b.RoamTx
		if ta != tb {
			return ta > tb
		}
		if wa, wb := a.WifiRx+a.WifiTx, b.WifiRx+b.WifiTx; wa != wb {
			return wa > wb
		}
		return a.ID < b.ID
	})
	return v
}

// sizeText is a byte count as Settings writes it.
func sizeText(n uint64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
}

// monthDay is a date by its month and day ("11/5").
func monthDay(t time.Time) string {
	return strings.NewReplacer("{m}", strconv.Itoa(int(t.Month())), "{day}", strconv.Itoa(t.Day())).Replace(tr.T("{m}/{day}"))
}
