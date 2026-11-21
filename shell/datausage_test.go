package shell

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/netfw"
	"github.com/kario-os/kario/ui"
)

// dataCore is a shell with Settings, the store and the browser, its clock
// at noon on the given day.
func dataCore(t *testing.T, day string) *Core {
	t.Helper()
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "shell"), 0o700)
	app := func(id, name string) *App {
		return &App{M: &Manifest{ID: id, Name: name}, running: true, ready: true, out: make(chan outbound, 64)}
	}
	c := &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: dir}, ev: make(chan func(), 64),
		apps: []*App{app(appSettings, "Settings"), app("com.phone.store", "Store"), app("com.phone.browser", "Browser")}}
	c.settings = defaultSettings()
	noLinks(t)
	at, err := time.ParseInLocation(time.DateOnly, day, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	c.clockSkew = at.Add(12 * time.Hour).Sub(time.Now())
	return c
}

// noLinks: the machine's own interfaces are not read (tests run as root
// would count them).
func noLinks(t *testing.T) {
	old := netLinks
	netLinks = func() []string { return nil }
	t.Cleanup(func() { netLinks = old })
}

func mobile(rx, tx uint64) netfw.Usage { return netfw.Usage{MobileRx: rx, MobileTx: tx} }

// dataNotifs are the plan's notifications in Settings' name, newest first.
func dataNotifs(c *Core, id string) []ui.Notification {
	var out []ui.Notification
	for _, n := range c.notifs {
		if n.app.M.ID == appSettings && n.n.ID == id {
			out = append(out, n.n)
		}
	}
	return out
}

// Cycles start on the day set, a short month's last day standing in, and
// run to the same day of the next month.
func TestDataCycle(t *testing.T) {
	day := func(s string) time.Time {
		d, _ := time.ParseInLocation(time.DateOnly, s, time.Local)
		return d.Add(15 * time.Hour)
	}
	for _, tc := range []struct {
		at         string
		day        int
		start, end string
	}{
		{"2026-11-20", 5, "2026-11-05", "2026-12-05"},
		{"2026-11-03", 5, "2026-10-05", "2026-11-05"},
		{"2026-11-05", 5, "2026-11-05", "2026-12-05"},
		{"2026-01-10", 15, "2025-12-15", "2026-01-15"},
		{"2026-12-20", 15, "2026-12-15", "2027-01-15"},
		{"2026-02-28", 31, "2026-02-28", "2026-03-31"},
		{"2026-02-27", 31, "2026-01-31", "2026-02-28"},
		{"2028-02-29", 30, "2028-02-29", "2028-03-30"},
		{"2026-11-20", 0, "2026-11-01", "2026-12-01"},
	} {
		got := cycleOf(day(tc.at), tc.day)
		if s, e := got.start.Format(time.DateOnly), got.end.Format(time.DateOnly); s != tc.start || e != tc.end {
			t.Errorf("%s, day %d: %s to %s, want %s to %s", tc.at, tc.day, s, e, tc.start, tc.end)
		}
	}
}

// What the lines used adds up per day and kind: mobile abroad as roaming,
// the hotspot's devices against the network shared, an uninstalled app's
// bytes on the removed apps' line; the page sorts by mobile data, lists
// this cycle and names the previous cycle's total; the file keeps the
// previous cycle and drops what is older.
func TestDataUsageLines(t *testing.T) {
	c := dataCore(t, "2026-11-20")
	c.settings.DataCycleDay = 5
	c.addData("com.phone.browser", netfw.Usage{MobileRx: 3000, MobileTx: 100, WifiRx: 50})
	c.addData("com.phone.store", netfw.Usage{MobileRx: 5000, WifiRx: 9000, WifiTx: 10})
	c.hotspotData(700, 300)
	c.tel = &telStatus{Roaming: "international"}
	c.addData("com.phone.browser", mobile(400, 0))
	c.tel = nil
	c.hotspot.upstream = "wlan1"
	c.hotspotData(10, 0)
	c.hotspot.upstream = ""

	v := c.dataUsageView()
	if v.Source != ui.TrafficSourceSim || v.Start != "2026-11-05" || v.End != "2026-12-05" {
		t.Fatalf("source and cycle: %+v", v)
	}
	if v.Mobile != 9100 || v.Wifi != 9070 || v.Roaming != 400 || v.MobileToday != 9100 {
		t.Fatalf("totals: mobile %d, Wi-Fi %d, roaming %d, today %d", v.Mobile, v.Wifi, v.Roaming, v.MobileToday)
	}
	if len(v.Lines) != 3 || v.Lines[0].ID != "com.phone.store" || v.Lines[0].Name != "Store" || v.Lines[1].ID != "com.phone.browser" ||
		v.Lines[1].RoamRx != 400 || v.Lines[2].ID != ui.TrafficLineHotspot || v.Lines[2].MobileRx != 700 || v.Lines[2].WifiRx != 10 {
		t.Fatalf("lines: %+v", v.Lines)
	}

	// The previous cycle, and a day before it the file no longer keeps.
	c.clockSkew -= 20 * 24 * time.Hour
	c.addData("com.phone.store", mobile(111, 0))
	c.clockSkew -= 70 * 24 * time.Hour
	c.addData("com.phone.store", mobile(1, 0))
	c.clockSkew += 90 * 24 * time.Hour
	c.saveData(true)
	if v := c.dataUsageView(); v.PrevMobile != 111 || v.Mobile != 9100 {
		t.Fatalf("the previous cycle: %d, this one %d", v.PrevMobile, v.Mobile)
	}
	if len(c.radios.data.Days) != 2 {
		t.Fatalf("days kept: %v", c.radios.data.Days)
	}

	c.forgetData("com.phone.store")
	again := dataCore(t, "2026-11-20")
	again.cfg, again.settings = c.cfg, c.settings
	v = again.dataUsageView()
	for _, l := range v.Lines {
		if l.ID == "com.phone.store" || l.ID == ui.TrafficLineRemoved && l.MobileRx != 5000 {
			t.Fatalf("after the store went: %+v", v.Lines)
		}
	}
	if v.Mobile != 9100 || v.PrevMobile != 111 {
		t.Fatalf("read back: %+v", v)
	}
}

// The file is written when there is something new, at most every
// dataUsageEvery, and at once when asked: a phone that sits idle does not
// write every half minute.
func TestDataUsageSaves(t *testing.T) {
	c := dataCore(t, "2026-11-20")
	written := func() bool {
		_, err := os.Stat(c.dataPath())
		_ = os.Remove(c.dataPath())
		return err == nil
	}
	c.addData("com.phone.store", mobile(1, 0))
	if c.saveData(false); !written() {
		t.Fatal("something new not written")
	}
	c.radios.data.saved = time.Time{}
	if c.dataTick(); written() {
		t.Fatal("written with nothing new")
	}
	c.addData("com.phone.store", mobile(1, 0))
	if c.saveData(false); !written() {
		t.Fatal("something new a while later not written")
	}
	c.addData("com.phone.store", mobile(1, 0))
	if c.saveData(false); written() {
		t.Fatal("written again within the half minute")
	}
	if c.saveData(true); !written() {
		t.Fatal("not written when asked")
	}
}

// On a phone the interfaces' counters bring in what no app's counter saw:
// what Wi-Fi (but the hotspot's access point) and the mobile data links
// carried, less the apps and the hotspot's devices, as the system's line.
// An interface there at the first look counts from then, a later one from
// nothing, one made again from zero; the access point's interface counts
// on from where it was once it is the station again.
func TestDataSystemLine(t *testing.T) {
	c := dataCore(t, "2026-11-20")
	c.tel = &telStatus{DataIfaces: []string{"wwan0"}}
	counters := map[string][2]uint64{"lo": {5, 5}, "wlan0": {1000, 100}, "wwan0": {2000, 200}, "usb0": {7, 7}}
	defer func(l func() []string, n func(string) (uint64, uint64, error)) { netLinks, linkCounters = l, n }(netLinks, linkCounters)
	netLinks = func() []string { return []string{"lo", "wlan0", "wwan0", "usb0", "wwan1"} }
	linkCounters = func(name string) (uint64, uint64, error) {
		n, ok := counters[name]
		if !ok {
			return 0, 0, errors.New("no such interface")
		}
		return n[0], n[1], nil
	}
	if d := c.linkDelta(); d.Total() != 0 {
		t.Fatalf("the first look: %+v", d)
	}
	counters["wlan0"], counters["wwan0"], counters["usb0"] = [2]uint64{1500, 150}, [2]uint64{2600, 260}, [2]uint64{900, 900}
	c.hotspotData(100, 10)
	links := c.linkDelta()
	if links != (netfw.Usage{WifiRx: 500, WifiTx: 50, MobileRx: 600, MobileTx: 60}) {
		t.Fatalf("the delta: %+v", links)
	}
	c.countFrom(map[string]netfw.Usage{"com.phone.store": {MobileRx: 300, MobileTx: 20, WifiRx: 600}}, &links)
	sys := c.radios.data.Days["2026-11-20"][ui.TrafficLineSystem]
	if sys == nil || *sys != (dataCount{WifiRx: 0, WifiTx: 50, MobileRx: 200, MobileTx: 30}) {
		t.Fatalf("the system's line: %+v", sys)
	}

	// A new mobile link counts from nothing, a link made again from zero.
	c.tel.DataIfaces = []string{"wwan0", "wwan1"}
	counters["wwan1"], counters["wwan0"] = [2]uint64{40, 4}, [2]uint64{10, 1}
	if d := c.linkDelta(); d != (netfw.Usage{MobileRx: 50, MobileTx: 5}) {
		t.Fatalf("new and remade links: %+v", d)
	}
	// The hotspot's access point is followed, not counted.
	c.hotspot.iface = "wlan0"
	counters["wlan0"] = [2]uint64{9000, 900}
	if d := c.linkDelta(); d.Total() != 0 {
		t.Fatalf("the access point counted: %+v", d)
	}
	c.hotspot.iface = ""
	counters["wlan0"] = [2]uint64{9100, 910}
	if d := c.linkDelta(); d != (netfw.Usage{WifiRx: 100, WifiTx: 10}) {
		t.Fatalf("the station again: %+v", d)
	}
}

// The plan: the warning is notified once a cycle (again at a new level);
// the allowance used up tells, and with the hard limit holds mobile data
// off until the user turns it on, which holds until the allowance rises;
// a new cycle lets mobile data on by itself. Roaming abroad does not count.
func TestDataPlan(t *testing.T) {
	defer i18n.SetLang(i18n.Lang())
	i18n.SetLang(i18n.English)
	c := dataCore(t, "2026-11-20")
	set := func(key string, v float64) {
		t.Helper()
		if err := c.settingsSet(key, v); err != nil {
			t.Fatal(err)
		}
	}
	set(ui.SettingDataCycleDay, 5)
	set(ui.SettingDataLimit, 1000)
	if p := c.settings.dataPlan(); p.Warning != 800 || p.WarningPercent != 80 || p.CycleDay != 5 {
		t.Fatalf("the default warning: %+v", p)
	}
	c.addData("com.phone.store", mobile(700, 0))
	c.checkDataPlan()
	if n := dataNotifs(c, ui.NotifDataUsage); len(n) != 0 {
		t.Fatalf("below the warning: %+v", n)
	}
	c.addData("com.phone.store", mobile(150, 0))
	c.checkDataPlan()
	if n := dataNotifs(c, ui.NotifDataUsage); len(n) != 1 || n[0].Title != "Data warning" || c.radios.data.Warned != "2026-11-05/800" {
		t.Fatalf("the warning: %+v %q", n, c.radios.data.Warned)
	}
	c.notifs = nil
	c.checkDataPlan()
	if len(c.notifs) != 0 {
		t.Fatal("warned twice in a cycle")
	}
	c.tel = &telStatus{Roaming: "international"}
	c.addData("com.phone.browser", mobile(5000, 0))
	c.checkDataPlan()
	c.tel = nil
	if len(c.notifs) != 0 {
		t.Fatal("roaming counted against the plan")
	}
	c.addData("com.phone.store", mobile(200, 0))
	c.checkDataPlan()
	if n := dataNotifs(c, ui.NotifDataUsage); len(n) != 1 || n[0].Title != "Mobile data plan used up" || c.dataOff() {
		t.Fatalf("used up, no hard limit: %+v off %v", n, c.dataOff())
	}

	set(ui.SettingDataLimitOff, 1)
	if !c.dataOff() || c.settings.MobileDataOff {
		t.Fatal("the hard limit set past the allowance does not hold mobile data off")
	}
	if st := c.radioState(); st.MobileData || !st.LimitHeld {
		t.Fatalf("radio.state: %+v", st)
	}
	c.setMobileData(true)
	if c.dataOff() || c.radioState().LimitHeld || c.radios.data.Override != "2026-11-05" {
		t.Fatalf("turned on past the limit: off %v %+v", c.dataOff(), c.radios.data)
	}
	c.addData("com.phone.store", mobile(500, 0))
	c.checkDataPlan()
	if c.dataOff() {
		t.Fatal("held off again in the same cycle")
	}
	set(ui.SettingDataLimit, 1200)
	if !c.dataOff() {
		t.Fatal("a higher allowance still used up does not hold mobile data off")
	}
	if n := dataNotifs(c, ui.NotifDataUsage); len(n) != 1 || n[0].Title != "Mobile data is off" {
		t.Fatalf("the hard limit's notice: %+v", n)
	}
	set(ui.SettingDataLimit, 5000)
	if c.dataOff() {
		t.Fatal("an allowance not used up holds mobile data off")
	}
	set(ui.SettingDataLimit, 1000)
	if !c.dataOff() {
		t.Fatal("back at the allowance used up")
	}
	// Read back after a restart: still held.
	c.saveData(true)
	again := dataCore(t, "2026-11-20")
	again.cfg, again.settings = c.cfg, c.settings
	if !again.dataOff() {
		t.Fatal("not held after a restart")
	}
	c.clockSkew += 20 * 24 * time.Hour
	c.checkDataPlan()
	if c.dataOff() {
		t.Fatal("a new cycle does not let mobile data on again")
	}
	set(ui.SettingDataLimitOff, 0)
}

// The plan's settings take whole numbers in range; an amount of its own
// replaces the share as the warning, a share the amount, 0 none.
func TestDataPlanSettings(t *testing.T) {
	c := dataCore(t, "2026-11-20")
	for _, bad := range []struct {
		key string
		v   float64
	}{{ui.SettingDataCycleDay, 0}, {ui.SettingDataCycleDay, 32}, {ui.SettingDataWarningPercent, 101}, {ui.SettingDataLimit, -1}, {ui.SettingDataLimit, 1.5}} {
		if err := c.settingsSet(bad.key, bad.v); err == nil {
			t.Errorf("%s %v taken", bad.key, bad.v)
		}
	}
	_ = c.settingsSet(ui.SettingDataLimit, 10<<30)
	_ = c.settingsSet(ui.SettingDataWarning, 3<<30)
	if p := c.settings.dataPlan(); p.Warning != 3<<30 || p.WarningPercent != 0 {
		t.Fatalf("an amount: %+v", p)
	}
	_ = c.settingsSet(ui.SettingDataWarningPercent, 50)
	if p := c.settings.dataPlan(); p.Warning != 5<<30 || p.WarningPercent != 50 {
		t.Fatalf("a share: %+v", p)
	}
	_ = c.settingsSet(ui.SettingDataWarningPercent, 0)
	if p := c.settings.dataPlan(); p.Warning != 0 || p.WarningPercent != 0 {
		t.Fatalf("none: %+v", p)
	}
	_ = c.settingsSet(ui.SettingDataWarning, 1<<30)
	if p := c.settings.dataPlan(); p.Warning != 1<<30 {
		t.Fatalf("an amount after none: %+v", p)
	}
	if v := c.dataUsageView(); v.Plan.Limit != 10<<30 || v.Plan.Warning != 1<<30 {
		t.Fatalf("the page: %+v", v.Plan)
	}
}

// The hotspot's daily limit: a warning near it, the hotspot off at it
// (each once a day), on again the same day it stays on; the bytes are the
// hotspot's line in data usage.
func TestHotspotDailyLimit(t *testing.T) {
	defer i18n.SetLang(i18n.Lang())
	i18n.SetLang(i18n.English)
	w := newSimWifi()
	ht := newHotspotTest(t, w)
	noLinks(t)
	_ = os.MkdirAll(filepath.Join(ht.c.cfg.StateDir, "shell"), 0o700)
	pass := ht.state().Passphrase
	if _, err := ht.set(ui.HotspotArgs{Enabled: on(true), DailyLimit: u64(1000)}); err != nil {
		t.Fatal(err)
	}
	if err := w.joinHotspot("tablet", pass); err != nil {
		t.Fatal(err)
	}
	notifs := func() []ui.Notification { return dataNotifs(ht.c, ui.NotifHotspotLimit) }
	_ = w.hotspotTraffic(700, 0)
	if st := ht.state(); len(notifs()) != 0 || st.DailyLimit != 1000 || st.Usage.LimitReached {
		t.Fatalf("below the warning: %+v %+v", notifs(), st)
	}
	_ = w.hotspotTraffic(100, 50)
	ht.state()
	ht.state()
	if n := notifs(); len(n) != 1 || n[0].Title != "Hotspot data warning" {
		t.Fatalf("the warning: %+v", n)
	}
	_ = w.hotspotTraffic(150, 0)
	ht.state()
	ht.pump("the hotspot off", func() bool { return !ht.c.hotspot.on })
	if n := notifs(); len(n) != 1 || n[0].Title != "Personal hotspot is off" || ht.c.settings.Hotspot || !ht.state().Usage.LimitReached {
		t.Fatalf("at the limit: %+v on %v", n, ht.c.settings.Hotspot)
	}
	if _, err := ht.set(ui.HotspotArgs{Enabled: on(true)}); err != nil {
		t.Fatal(err)
	}
	_ = w.joinHotspot("tablet", pass)
	_ = w.hotspotTraffic(500, 0)
	ht.state()
	if !ht.c.settings.Hotspot || len(notifs()) != 1 {
		t.Fatal("turned on again the same day, it went off again")
	}
	if v := ht.c.dataUsageView(); len(v.Lines) != 1 || v.Lines[0].ID != ui.TrafficLineHotspot || v.Lines[0].MobileRx != 1450 || v.Lines[0].MobileTx != 50 {
		t.Fatalf("data usage: %+v", v.Lines)
	}
	if _, err := ht.set(ui.HotspotArgs{Enabled: on(false), DailyLimit: u64(0)}); err != nil {
		t.Fatal(err)
	}
}

func u64(v uint64) *uint64 { return &v }
