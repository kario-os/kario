package shell

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/netfw"
	"github.com/kario-os/kario/ui"
)

// netCore is dataCore with the store and the browser using the network,
// Settings in front and the screen on.
func netCore(t *testing.T, day string) (*Core, *App, *App) {
	t.Helper()
	c := dataCore(t, day)
	c.broker = &broker{c: c, perms: map[string]map[string]string{}, usage: map[string]map[string]*permUse{}, stats: map[string]*CapStat{},
		path: filepath.Join(c.cfg.StateDir, "shell", "perms.json")}
	store, browser := c.appByID("com.phone.store"), c.appByID("com.phone.browser")
	store.M.Permissions, browser.M.Permissions = []string{ui.PermNet}, []string{ui.PermNet}
	c.fg = c.appByID(appSettings)
	c.powerOverride = &Power{Level: 80}
	return c, store, browser
}

func setApp(t *testing.T, c *Core, key, id string, v float64) {
	t.Helper()
	if ok, err := c.appNetSetting(ui.SettingArgs{Key: key, Value: v, Text: id}); !ok || err != nil {
		t.Fatalf("%s %s %v: %v %v", key, id, v, ok, err)
	}
}

// blocks puts the blocks in force and gives them.
func blocks(c *Core) map[string]string {
	c.netPolicyCheck()
	return c.radios.netBlocks
}

// netEvents are the net.state events queued for the app.
func netEvents(a *App) []ui.NetState {
	var out []ui.NetState
	for {
		m, ok := next(a)
		if !ok {
			return out
		}
		if m.T == ui.MsgEvent && m.Event == ui.EventNetState {
			var st ui.NetState
			_ = json.Unmarshal([]byte(m.Value), &st)
			out = append(out, st)
		}
	}
}

// Background data off: the app's mobile data is blocked unless it is on
// the screen (in front, the screen on, the keypad unlocked) or a
// background mode keeps it running, frozen not. Data Saver blocks every
// app's but those exempt, and an app's own switch wins over its
// exemption; Wi-Fi only is a block of its own. The firewall holds the
// blocked apps' uids, their web views' too, to Wi-Fi.
func TestBackgroundData(t *testing.T) {
	c, store, _ := netCore(t, "2026-11-20")
	if b := blocks(c); len(b) != 0 {
		t.Fatalf("nothing set: %v", b)
	}
	setApp(t, c, ui.SettingAppBackgroundData, "com.phone.store", 0)
	if b := blocks(c); len(b) != 1 || b["com.phone.store"] != ui.NetBlockedBackground {
		t.Fatalf("background data off: %v", b)
	}
	c.fg = store
	if b := blocks(c); len(b) != 0 {
		t.Fatalf("on the screen: %v", b)
	}
	c.screen.off = true
	if b := blocks(c); b["com.phone.store"] != ui.NetBlockedBackground {
		t.Fatalf("the screen off: %v", b)
	}
	c.screen.off, c.keylock = false, true
	if b := blocks(c); b["com.phone.store"] != ui.NetBlockedBackground {
		t.Fatalf("the keypad locked: %v", b)
	}
	c.keylock, c.fg = false, nil
	store.M.Background, store.M.Permissions = []string{ui.PermVoIP}, []string{ui.PermNet, ui.PermVoIP}
	if b := blocks(c); len(b) != 0 {
		t.Fatalf("kept running by a background mode: %v", b)
	}
	store.frozen = true
	if b := blocks(c); b["com.phone.store"] != ui.NetBlockedBackground {
		t.Fatalf("frozen: %v", b)
	}
	store.M.Background, store.M.Permissions, store.frozen = nil, []string{ui.PermNet}, false

	setApp(t, c, ui.SettingDataSaver, "", 1)
	if b := blocks(c); len(b) != 2 || b["com.phone.browser"] != ui.NetBlockedBackground {
		t.Fatalf("Data Saver: %v", b)
	}
	setApp(t, c, ui.SettingAppDataUnrestricted, "com.phone.browser", 1)
	setApp(t, c, ui.SettingAppDataUnrestricted, "com.phone.store", 1)
	if b := blocks(c); len(b) != 1 || b["com.phone.store"] != ui.NetBlockedBackground {
		t.Fatalf("unrestricted, one with background data off: %v", b)
	}
	c.broker.perms["com.phone.browser"] = map[string]string{ui.PermNet: "wifi"}
	if b := blocks(c); b["com.phone.browser"] != ui.NetBlockedWifiOnly {
		t.Fatalf("Wi-Fi only: %v", b)
	}

	uids, _ := json.Marshal(map[string]uint32{"com.phone.store": 10003, "com.phone.store/web": 10004, "com.phone.browser": 10005, "com.phone.gone": 10009})
	if err := os.WriteFile(filepath.Join(c.cfg.StateDir, "shell", "uids.json"), uids, 0o600); err != nil {
		t.Fatal(err)
	}
	want := []netfw.Policy{{UID: 10003, WifiOnly: true}, {UID: 10004, WifiOnly: true}, {UID: 10005, WifiOnly: true}}
	if ps := c.netPolicies(); !slices.Equal(ps, want) {
		t.Fatalf("the firewall's rules: %+v", ps)
	}
	delete(c.broker.perms, "com.phone.browser")
	c.fg = store
	want = []netfw.Policy{{UID: 10003}, {UID: 10004}, {UID: 10005}}
	if ps := c.netPolicies(); !slices.Equal(ps, want) {
		t.Fatalf("the store in front, the browser exempt: %+v", ps)
	}

	c.fg = c.appByID(appSettings)
	v := c.dataUsageView()
	if !v.DataSaver || len(v.Apps) != 2 || v.Apps[0].ID != "com.phone.browser" || !v.Apps[0].Unrestricted || v.Apps[0].Blocked != "" ||
		!v.Apps[1].BackgroundOff || v.Apps[1].Blocked != ui.NetBlockedBackground {
		t.Fatalf("Settings' view: %+v", v.Apps)
	}
	raw, _ := os.ReadFile(c.settingsPath())
	var saved sysSettings
	if json.Unmarshal(raw, &saved) != nil || !saved.DataSaver || !saved.BgDataOff["com.phone.store"] || !saved.DataSaverAllow["com.phone.browser"] {
		t.Fatalf("not kept: %s", raw)
	}
	c.forgetData("com.phone.store")
	if c.settings.BgDataOff != nil || c.settings.DataSaverAllow["com.phone.store"] {
		t.Fatalf("an uninstalled app's settings kept: %+v", c.settings)
	}
}

// The app settings take an app that uses the network and, for the limit,
// a whole number of bytes.
func TestAppNetSettingChecks(t *testing.T) {
	c, _, _ := netCore(t, "2026-11-20")
	for _, bad := range []ui.SettingArgs{
		{Key: ui.SettingAppBackgroundData, Text: "com.phone.nope"},
		{Key: ui.SettingAppDataUnrestricted, Value: 1, Text: appSettings},
		{Key: ui.SettingAppDataLimit, Value: -1, Text: "com.phone.store"},
		{Key: ui.SettingAppDataLimit, Value: 1.5, Text: "com.phone.store"},
	} {
		if ok, err := c.appNetSetting(bad); !ok || err == nil {
			t.Errorf("%+v taken: %v %v", bad, ok, err)
		}
	}
	if ok, _ := c.appNetSetting(ui.SettingArgs{Key: ui.SettingDataLimit, Value: 1}); ok {
		t.Error("the plan's key taken as an app's")
	}
	setApp(t, c, ui.SettingAppDataLimit, "com.phone.store", 5<<20)
	setApp(t, c, ui.SettingAppDataLimit, "com.phone.store", 0)
	if len(c.settings.AppDataLimit) != 0 {
		t.Fatalf("no limit kept: %v", c.settings.AppDataLimit)
	}
}

// An app that runs hears when its block changes (net.state), with the
// network the phone is on; a frozen one does not. net.state answers the
// same, to apps with the net permission.
func TestNetStateEvents(t *testing.T) {
	c, store, _ := netCore(t, "2026-11-20")
	c.tel = &telStatus{DataState: "connected"}
	setApp(t, c, ui.SettingAppBackgroundData, "com.phone.store", 0)
	var ev []ui.NetState
	pumpUntil(t, c, func() bool { ev = append(ev, netEvents(store)...); return len(ev) > 0 })
	if len(ev) != 1 || ev[0] != (ui.NetState{Network: ui.NetMobile, Metered: true, Blocked: ui.NetBlockedBackground}) {
		t.Fatalf("blocked: %+v", ev)
	}
	c.fg = store
	c.netPolicyCheck()
	ev = nil
	pumpUntil(t, c, func() bool { ev = append(ev, netEvents(store)...); return len(ev) > 0 })
	if len(ev) != 1 || ev[0].Blocked != "" || ev[0].BackgroundData {
		t.Fatalf("in front: %+v", ev)
	}
	c.fg, store.frozen = nil, true
	c.netPolicyCheck()
	time.Sleep(20 * time.Millisecond)
	for len(c.ev) > 0 {
		(<-c.ev)()
	}
	if ev := netEvents(store); len(ev) != 0 || c.radios.netBlocks["com.phone.store"] == "" {
		t.Fatalf("frozen, told %+v", ev)
	}

	store.frozen = false
	c.broker.handle(store, ui.Msg{T: ui.MsgReq, Req: 7, Cap: ui.CapNetState})
	var res ui.Msg
	pumpUntil(t, c, func() bool {
		m, ok := next(store)
		if ok && m.T == ui.MsgRes {
			res = m
		}
		return res.T != ""
	})
	var st ui.NetState
	if raw, _ := json.Marshal(res.Data); res.Err != "" || json.Unmarshal(raw, &st) != nil || st.Network != ui.NetMobile || st.Blocked != ui.NetBlockedBackground {
		t.Fatalf("net.state: %+v %+v", res, st)
	}
	settings := c.appByID(appSettings)
	c.broker.handle(settings, ui.Msg{T: ui.MsgReq, Req: 8, Cap: ui.CapNetState})
	if m, ok := next(settings); !ok || m.Err == "" {
		t.Fatalf("without the net permission: %+v", m)
	}
}

// An app's own limit: a notice in Settings' name naming the app at
// ui.AppDataWarnPercent, once; used up (roaming abroad counts), another,
// and its mobile data is blocked, on the screen too, while the others'
// is not. Raising the limit lifts the block, the same limit again blocks
// without telling again; a restart keeps the block, a new cycle lifts it.
func TestAppDataLimit(t *testing.T) {
	defer i18n.SetLang(i18n.Lang())
	i18n.SetLang(i18n.English)
	c, _, browser := netCore(t, "2026-11-20")
	notifs := func() []ui.Notification { return dataNotifs(c, ui.NotifAppDataLimit+":com.phone.browser") }
	setApp(t, c, ui.SettingAppDataLimit, "com.phone.browser", 1000)
	c.addData("com.phone.browser", mobile(700, 0))
	c.addData("com.phone.store", mobile(5000, 0))
	c.checkDataPlan()
	if len(c.notifs) != 0 || len(c.radios.netBlocks) != 0 {
		t.Fatalf("below the notice: %+v %v", c.notifs, c.radios.netBlocks)
	}
	c.addData("com.phone.browser", netfw.Usage{MobileRx: 100, WifiRx: 5000})
	c.checkDataPlan()
	if n := notifs(); len(n) != 1 || n[0].Title != "“Browser” is near its mobile data limit" || !strings.Contains(n[0].Body, "800 B of its 1000 B") {
		t.Fatalf("the notice: %+v", n)
	}
	c.notifs = nil
	c.checkDataPlan()
	if len(c.notifs) != 0 {
		t.Fatal("told twice")
	}
	c.tel = &telStatus{Roaming: "international"}
	c.addData("com.phone.browser", mobile(250, 0))
	c.tel = nil
	c.checkDataPlan()
	if n := notifs(); len(n) != 1 || n[0].Title != "“Browser” used up its mobile data limit" || !strings.Contains(n[0].Body, "until 11/30") {
		t.Fatalf("used up: %+v", n)
	}
	c.fg = browser
	if b := blocks(c); len(b) != 1 || b["com.phone.browser"] != ui.NetBlockedLimit {
		t.Fatalf("on the screen at its limit: %v", b)
	}
	if st := c.netStateOf(browser, netNow{Kind: netWifi}); st.Limit != 1000 || st.Used != 1050 || st.Blocked != ui.NetBlockedLimit || !st.BackgroundData {
		t.Fatalf("net.state: %+v", st)
	}
	if v := c.dataUsageView(); v.Apps[0].ID != "com.phone.browser" || v.Apps[0].Used != 1050 || v.Apps[0].Limit != 1000 || v.Apps[0].Blocked != ui.NetBlockedLimit {
		t.Fatalf("Settings' view: %+v", v.Apps)
	}
	setApp(t, c, ui.SettingAppDataLimit, "com.phone.browser", 2000)
	if b := c.radios.netBlocks; len(b) != 0 {
		t.Fatalf("the limit raised: %v", b)
	}
	c.notifs = nil
	setApp(t, c, ui.SettingAppDataLimit, "com.phone.browser", 1000)
	if b := c.radios.netBlocks; b["com.phone.browser"] != ui.NetBlockedLimit || len(c.notifs) != 0 {
		t.Fatalf("the same limit again: %v %+v", b, c.notifs)
	}

	c.saveData(true)
	again, _, againBrowser := netCore(t, "2026-11-20")
	again.cfg, again.settings = c.cfg, c.settings
	again.dataLedger()
	if again.mobileBlock(againBrowser) != ui.NetBlockedLimit {
		t.Fatal("not blocked after a restart")
	}
	c.clockSkew += 20 * 24 * time.Hour
	c.checkDataPlan()
	if b := c.radios.netBlocks; len(b) != 0 {
		t.Fatalf("a new cycle: %v", b)
	}
}

// A job that asks for a network waits while there is none, and on mobile
// data while its app's mobile data is blocked in the background; it runs
// on Wi-Fi, a metered one too, and the other app's on mobile data.
// Running, it is stopped (blocked) once the phone moves to mobile data,
// and runs again on Wi-Fi.
func TestJobsWaitForWifi(t *testing.T) {
	c, store, browser := netCore(t, "2026-11-20")
	c.tel = &telStatus{DataState: "connected"}
	setApp(t, c, ui.SettingAppBackgroundData, "com.phone.browser", 0)
	past := c.now().Add(-2 * time.Hour)
	c.jobs = []*job{{App: "com.phone.browser", ID: "sync", Every: 60, Network: true, Next: past},
		{App: "com.phone.store", ID: "sync", Every: 60, Network: true, Next: past}}
	m := &c.radios.meter
	// run gives the jobs the network n, as a look found it; the looks of
	// the jobs that run see the radios.
	run := func(n netNow) {
		t.Helper()
		m.now, m.at = n, c.now()
		c.runJobs()
		pumpUntil(t, c, func() bool { return !m.jobs.looking })
	}
	run(netNow{Kind: netNone})
	if b, s := c.JobHeld("com.phone.browser", "sync"), c.JobHeld("com.phone.store", "sync"); b != "no network" || s != "no network" {
		t.Fatalf("offline: %q %q", b, s)
	}
	run(netNow{Kind: netMobile, Metered: true})
	if runs, _ := c.JobRuns("com.phone.browser", "sync"); runs != 0 || !strings.Contains(c.JobHeld("com.phone.browser", "sync"), "blocked") {
		t.Fatalf("on mobile data, blocked: %d runs, %q", runs, c.JobHeld("com.phone.browser", "sync"))
	}
	if runs, _ := c.JobRuns("com.phone.store", "sync"); runs != 1 || c.JobHeld("com.phone.store", "sync") != "" {
		t.Fatal("the store's job did not run on mobile data")
	}
	w := newSimWifi()
	w.addNetwork("Pixel-Hotspot", "", ui.MeteredHintDHCP)
	if err := w.connect("Pixel-Hotspot", ui.WifiArgs{}); err != nil {
		t.Fatal(err)
	}
	c.wifi = w
	m.jobs.at = time.Time{}
	run(netNow{Kind: netWifi, SSID: "Pixel-Hotspot", Metered: true})
	if runs, _ := c.JobRuns("com.phone.browser", "sync"); runs != 1 || c.JobHeld("com.phone.browser", "sync") != "" {
		t.Fatalf("on Wi-Fi: %d runs", runs)
	}
	if ev := jobEvents(browser); len(ev) != 1 || ev[0] != "job sync" {
		t.Fatalf("the browser got %q", ev)
	}
	if ev := jobEvents(store); len(ev) != 1 || ev[0] != "job sync" {
		t.Fatalf("the store got %q", ev)
	}

	w.setEnabled(false)
	m.jobs.at = time.Time{}
	c.runJobs()
	pumpUntil(t, c, func() bool { return !m.jobs.looking })
	if ev := jobEvents(browser); len(ev) != 1 || ev[0] != `job.stop {"id":"sync","reason":"blocked"}` {
		t.Fatalf("moved to mobile data: the browser got %q", ev)
	}
	if ev := jobEvents(store); len(ev) != 0 {
		t.Fatalf("the store's job stopped: %q", ev)
	}
	c.jobs[0].stopAt = time.Now().Add(-jobStopGrace)
	c.runJobs()
	w.setEnabled(true)
	_ = w.connect("Pixel-Hotspot", ui.WifiArgs{})
	run(netNow{Kind: netWifi, SSID: "Pixel-Hotspot", Metered: true})
	if runs, _ := c.JobRuns("com.phone.browser", "sync"); runs != 2 || c.JobStops("com.phone.browser", "sync") != 1 {
		t.Fatalf("back on Wi-Fi: %d runs", runs)
	}
	if err := c.scheduleJob(browser, ui.JobArgs{ID: "later", EveryMin: 30, Network: true}); err != nil || !c.jobs[len(c.jobs)-1].Network {
		t.Fatalf("the job forgot it needs a network: %v", err)
	}
}

// Joining Wi-Fi in Settings makes the jobs waiting for a network look at
// it again at once, not meterStale after the last look.
func TestWifiJoinLooksAgain(t *testing.T) {
	c, _, _ := netCore(t, "2026-11-20")
	c.wifi = newSimWifi()
	m := &c.radios.meter
	m.now, m.at = netNow{Kind: netMobile, Metered: true}, c.now()
	raw, _ := json.Marshal(ui.WifiArgs{SSID: "Kario-Guest"})
	var err error
	done := false
	settings := &App{M: &Manifest{ID: "com.phone.settings", Name: "Settings"}, running: true, ready: true}
	c.broker.dispatchRadios(settings, ui.Msg{T: ui.MsgReq, Cap: ui.CapWifiConnect, Args: raw}, func(_ any, e error) { err, done = e, true })
	pumpUntil(t, c, func() bool { return done })
	if err != nil || !m.at.IsZero() {
		t.Fatalf("joined (%v), the last look stands: %v", err, m.at)
	}
}

// Without the firewall the selftest's sends answer as the policy says:
// blocked while the app's mobile data is and the phone is not on Wi-Fi.
func TestPolicySend(t *testing.T) {
	c, _, _ := netCore(t, "2026-11-20")
	setApp(t, c, ui.SettingAppBackgroundData, "com.phone.store", 0)
	if err := c.policySend("com.phone.store"); err == nil {
		t.Fatal("blocked, sent")
	}
	if err := c.policySend("com.phone.browser"); err != nil {
		t.Fatal(err)
	}
	w := newSimWifi()
	if err := w.connect("Kario-Guest", ui.WifiArgs{}); err != nil {
		t.Fatal(err)
	}
	c.wifi = w
	if err := c.policySend("com.phone.store"); err != nil {
		t.Fatalf("on Wi-Fi: %v", err)
	}
	if c.policySend(appSettings) == nil {
		t.Fatal("an app without the network sent")
	}
}
