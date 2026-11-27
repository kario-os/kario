package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// The page: this cycle's mobile data against the plan, the lines by mobile
// data with the system's own named, Wi-Fi apart by its own order, roaming
// abroad apart, the hard limit's state and where the counts come from.
func TestDataUsagePage(t *testing.T) {
	i18n.SetLang("en")
	var p app
	p.s.Data = ui.TrafficUsage{Source: ui.TrafficSourceApps, Start: "2026-11-05", End: "2026-12-05", Mobile: 600 << 20, Wifi: 3 << 30, Roaming: 2 << 20,
		MobileToday: 10 << 20, PrevMobile: 1 << 30, Plan: ui.DataPlan{Limit: 1 << 30, CycleDay: 5, Warning: 800 << 20, WarningPercent: 80, LimitOff: true},
		Lines: []ui.TrafficLine{
			{ID: "com.phone.store", Name: "Store", MobileRx: 400 << 20, MobileTx: 10 << 20, WifiRx: 1 << 30},
			{ID: ui.TrafficLineHotspot, MobileRx: 150 << 20, MobileTx: 30 << 20},
			{ID: "com.phone.browser", Name: "Browser", MobileRx: 10 << 20, RoamRx: 2 << 20, WifiRx: 2 << 30},
			{ID: ui.TrafficLineSystem, WifiTx: 1024},
		}}
	s := p.trafficScreen()
	if s.Body[0].Text != "Mobile data 600.0 MB of 1.00 GB" || s.Body[1].Fraction < 0.58 || s.Body[1].Fraction > 0.59 {
		t.Fatalf("the head: %+v %+v", s.Body[0], s.Body[1])
	}
	if !strings.Contains(texts(s), "11/5 to 12/4 · today 10.0 MB") || !strings.Contains(texts(s), "Roaming abroad this cycle: 2.0 MB") ||
		!strings.Contains(texts(s), "Last cycle: mobile data 1.00 GB") || !strings.Contains(texts(s), "Counted per app by the firewall") {
		t.Fatalf("the notes: %q", texts(s))
	}
	if n := node(s, "plan"); n.Subtitle != "1.00 GB a month · from day 5 · warning at 800.0 MB · mobile data off at the limit" {
		t.Fatalf("the plan's line: %+v", n)
	}
	var mobile, wifi []string
	for _, n := range s.Body {
		switch {
		case strings.HasPrefix(n.ID, "m:"):
			mobile = append(mobile, n.Title+" "+n.Trailing)
		case strings.HasPrefix(n.ID, "w:"):
			wifi = append(wifi, n.Title+" "+n.Trailing)
		}
	}
	if strings.Join(mobile, ", ") != "Store 410.0 MB, Hotspot devices 180.0 MB, Browser 12.0 MB" {
		t.Fatalf("mobile data by app: %q", mobile)
	}
	if strings.Join(wifi, ", ") != "Browser 2.00 GB, Store 1.00 GB, System and other 1.0 KB" {
		t.Fatalf("Wi-Fi: %q", wifi)
	}
	if n := node(s, "m:com.phone.browser"); n.Subtitle != "↓10.0 MB ↑0 B · abroad 2.0 MB" {
		t.Fatalf("roaming on the app's line: %+v", n)
	}
	if got := p.trafficSummary(); got != "Mobile 600.0 MB of 1.00 GB this cycle · Wi-Fi 3.00 GB" {
		t.Fatalf("the summary: %q", got)
	}

	p.s.Data.Held = true
	if !strings.Contains(texts(p.trafficScreen()), "Mobile data is off: this cycle's limit is used up") {
		t.Fatalf("held: %q", texts(p.trafficScreen()))
	}
	p.s.Data.Held, p.s.Data.Override = false, true
	if !strings.Contains(texts(p.trafficScreen()), "stays on past the limit until 12/4") {
		t.Fatalf("past the limit: %q", texts(p.trafficScreen()))
	}

	p.s.Data = ui.TrafficUsage{Source: ui.TrafficSourceLinks, Start: "2026-11-01", End: "2026-12-01", Plan: ui.DataPlan{CycleDay: 1},
		Lines: []ui.TrafficLine{{ID: ui.TrafficLineSystem, MobileRx: 5}}}
	s = p.trafficScreen()
	if s.Body[0].Text != "Mobile data 0 B" || node(s, "m:"+ui.TrafficLineSystem).Title != "Apps and the system" ||
		!strings.Contains(texts(s), "no per-app counters") || !strings.Contains(texts(s), "Nothing used Wi-Fi this cycle") {
		t.Fatalf("by network only: %q %+v", texts(s), s.Body)
	}
	if n := node(s, "plan"); n.Subtitle != "No monthly limit · from day 1" {
		t.Fatalf("no plan: %+v", n)
	}
	p.s.Data = ui.TrafficUsage{Source: ui.TrafficSourceSim}
	if s := p.trafficScreen(); !strings.Contains(texts(s), "Simulated") || !strings.Contains(texts(s), "Nothing used mobile data this cycle") {
		t.Fatalf("simulated: %q", texts(s))
	}
}

// The plan's page, its warning's labels, and amounts read as typed.
func TestDataPlanPage(t *testing.T) {
	i18n.SetLang("en")
	var p app
	p.s.Data.Plan = ui.DataPlan{Limit: 10 << 30, CycleDay: 15, Warning: 8 << 30, WarningPercent: 80}
	s := p.planScreen()
	if node(s, "dp_limit").Trailing != "10.00 GB" || node(s, "dp_day").Trailing != "Day 15" || node(s, "dp_warn").Trailing != "80% · 8.00 GB" || node(s, "dp_off").Trailing != "Off" {
		t.Fatalf("the page: %+v", s.Body)
	}
	for _, tc := range []struct {
		plan ui.DataPlan
		want string
	}{
		{ui.DataPlan{WarningPercent: 75}, "75% of the limit"},
		{ui.DataPlan{Limit: 1 << 30, Warning: 500 << 20}, "500.0 MB"},
		{ui.DataPlan{Limit: 1 << 30}, "Off"},
	} {
		if got := warnLabel(tc.plan); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.plan, got, tc.want)
		}
	}
	for in, want := range map[string]uint64{"10": 10 << 30, "0.5": 512 << 20, "500 MB": 500 << 20, "2g": 2 << 30, "1,5 GB": 1536 << 20, "": 0, "0": 0} {
		if got, ok := parseAmount(in); !ok || got != want {
			t.Errorf("%q: %d %v, want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"ten", "-1", "1e20", "5 KB"} {
		if _, ok := parseAmount(bad); ok {
			t.Errorf("%q taken", bad)
		}
	}
	for _, n := range []uint64{500 << 20, 10 << 30, 1536 << 20} {
		if got, _ := parseAmount(amountText(n)); got != n {
			t.Errorf("%d reads back as %d (%q)", n, got, amountText(n))
		}
	}
	if a := amountPage("dpwarn", 0); node(a, "v").Value != "" || node(a, "v").Keyboard != ui.KeyboardNumber || a.Title != "Data warning" {
		t.Fatalf("the warning's amount page: %+v", a)
	}
}

// The plan's choices go to the system as settings: the cycle's day wraps,
// the warning steps through its shares (from an amount of its own as from
// none), the hard limit switches, an amount typed is saved in bytes and
// one that is none is refused.
func TestDataPlanActions(t *testing.T) {
	p, _, reqs := fakeShell(t)
	ready := make(chan struct{})
	p.a.Post(func() {
		p.s.Data.Plan = ui.DataPlan{Limit: 1 << 30, CycleDay: 31, Warning: 300 << 20}
		close(ready)
	})
	<-ready
	for _, c := range []struct {
		e    sdk.ActionEvent
		key  string
		want float64
	}{
		{sdk.ActionEvent{Screen: "dataplan", Node: "dp_day", Action: "next"}, ui.SettingDataCycleDay, 1},
		{sdk.ActionEvent{Screen: "dataplan", Node: "dp_day", Action: "prev"}, ui.SettingDataCycleDay, 31},
		{sdk.ActionEvent{Screen: "dataplan", Node: "dp_warn", Action: "prev"}, ui.SettingDataWarningPercent, 90},
		{sdk.ActionEvent{Screen: "dataplan", Node: "dp_warn", Action: "prev"}, ui.SettingDataWarningPercent, 80},
		{sdk.ActionEvent{Screen: "dataplan", Node: "dp_warn", Action: "next"}, ui.SettingDataWarningPercent, 90},
		{sdk.ActionEvent{Screen: "dataplan", Node: "dp_warn", Action: "next"}, ui.SettingDataWarningPercent, 0},
		{sdk.ActionEvent{Screen: "dataplan", Node: "dp_off", Action: "toggle"}, ui.SettingDataLimitOff, 1},
		{sdk.ActionEvent{Screen: "dplimit", Action: "save", Fields: map[string]string{"v": "500 MB"}}, ui.SettingDataLimit, 500 << 20},
		{sdk.ActionEvent{Screen: "dpwarn", Action: "save", Fields: map[string]string{"v": "0.25"}}, ui.SettingDataWarning, 256 << 20},
	} {
		p.a.Post(func() { p.action(c.e) })
		select {
		case m := <-reqs:
			var args ui.SettingArgs
			if err := json.Unmarshal(m.Args, &args); err != nil || m.Cap != ui.CapSettingsSet || args.Key != c.key || args.Value != c.want {
				t.Fatalf("%+v: %s %s, want %s %v", c.e, m.Cap, m.Args, c.key, c.want)
			}
			p.a.Post(func() { p.sets = nil })
		case <-time.After(2 * time.Second):
			t.Fatalf("%+v: nothing sent", c.e)
		}
	}
	p.a.Post(func() {
		p.action(sdk.ActionEvent{Screen: "dplimit", Action: "save", Fields: map[string]string{"v": "lots"}})
	})
	select {
	case m := <-reqs:
		t.Fatalf("an amount that is none sent: %s %s", m.Cap, m.Args)
	case <-time.After(300 * time.Millisecond):
	}

	// Opening the page reads the counts afresh.
	p.a.Post(func() {
		p.action(sdk.ActionEvent{Screen: groupPrefix + "mobile", Node: "traffic", Action: ui.ActionOpen})
	})
	select {
	case m := <-reqs:
		if m.Cap != ui.CapSettingsGet {
			t.Fatalf("opening the page asked for %s", m.Cap)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("opening the page read nothing")
	}
}

// A page's own actions come with the focused row: the refresh key on the
// page with the plan's row focused refreshes, it does not open the plan,
// and the top's sandbox check with the row focused is not an open.
func TestDataPageKeysAreNotOpens(t *testing.T) {
	var p app
	for _, e := range []sdk.ActionEvent{
		{Screen: "traffic", Node: "plan", Action: "refresh"},
		{Screen: "root", Node: "traffic", Action: "probe"},
		{Screen: groupPrefix + "mobile", Node: "traffic", Action: "refresh"},
	} {
		if p.dataAction(e) {
			t.Errorf("%+v taken as the data usage pages' own", e)
		}
	}
}

// appData is a cycle with the store at its own limit, the browser's
// background data off and the chat app with nothing of its own.
func appData() ui.TrafficUsage {
	return ui.TrafficUsage{Source: ui.TrafficSourceApps, Start: "2026-11-05", End: "2026-12-05", Mobile: 100 << 20, Plan: ui.DataPlan{CycleDay: 5},
		Lines: []ui.TrafficLine{{ID: "com.phone.store", Name: "Store", MobileRx: 90 << 20, MobileTx: 10 << 20, WifiRx: 1 << 30},
			{ID: "com.phone.browser", Name: "Browser", WifiRx: 5 << 20}},
		Apps: []ui.TrafficApp{{ID: "com.phone.browser", Name: "Browser", BackgroundOff: true, Blocked: ui.NetBlockedBackground},
			{ID: "com.phone.chat", Name: "Chat"},
			{ID: "com.phone.store", Name: "Store", Limit: 100 << 20, Used: 100 << 20, Blocked: ui.NetBlockedLimit}}}
}

// The apps' own rules on the data usage page: an app's line says them
// and opens its page, an app held back shows though it used no mobile
// data; Data Saver's page lists every app that uses the network.
func TestAppDataRows(t *testing.T) {
	i18n.SetLang("en")
	var p app
	p.s.Data = appData()
	s := p.trafficScreen()
	if n := node(s, "m:com.phone.store"); n.Subtitle != "↓90.0 MB ↑10.0 MB · limit used up" || len(n.Actions) != 1 || n.Actions[0].ID != ui.ActionOpen {
		t.Fatalf("the store's line: %+v", n)
	}
	if n := node(s, "m:com.phone.browser"); n.Trailing != "0 B" || n.Subtitle != "background data off" {
		t.Fatalf("the browser held back: %+v", n)
	}
	if n := node(s, "m:com.phone.chat"); n.ID != "" {
		t.Fatalf("an app with nothing of its own and no mobile data: %+v", n)
	}
	if n := node(s, "w:com.phone.browser"); len(n.Actions) != 1 {
		t.Fatalf("the browser's Wi-Fi line opens nothing: %+v", n)
	}
	if n := node(s, "datasaver"); n.Subtitle != "Off" {
		t.Fatalf("Data Saver's line: %+v", n)
	}
	p.s.Data.DataSaver = true
	p.s.Data.Apps[1].Unrestricted = true
	if n := node(p.trafficScreen(), "datasaver"); n.Subtitle != "On · 1 with unrestricted data" {
		t.Fatalf("Data Saver on: %+v", n)
	}
	s = p.dataSaverScreen()
	if node(s, "ds_on").Trailing != "On" || node(s, "da:com.phone.chat").Subtitle != "unrestricted" ||
		node(s, "da:com.phone.store").Subtitle != "limit used up · held back by Data Saver" || node(s, "da:com.phone.store").Trailing != "100.0 MB" {
		t.Fatalf("Data Saver's page: %+v", s.Body)
	}
	p.s.Data.Apps = nil
	if !strings.Contains(texts(p.dataSaverScreen()), "No app uses the network") {
		t.Fatal("no app")
	}
}

// An app's page: what it used, its switches and its limit, why its mobile
// data is blocked, and how the blocks hold where the counts come from.
func TestAppDataPage(t *testing.T) {
	i18n.SetLang("en")
	var p app
	p.s.Data = appData()
	s := p.appDataScreen("com.phone.store")
	if s.ID != "appdata:com.phone.store" || s.Title != "Store" || s.Body[0].Text != "Mobile data 100.0 MB this cycle" || s.Body[1].Fraction != 1 {
		t.Fatalf("the head: %+v", s.Body[:2])
	}
	if !strings.Contains(texts(s), "Its mobile data is blocked: this cycle's limit is used up, until 12/4") || !strings.Contains(texts(s), "Blocked connections fail at once") {
		t.Fatalf("the notes: %q", texts(s))
	}
	if node(s, "ad_bg").Trailing != "On" || node(s, "ad_free").Trailing != "Off" || node(s, "ad_limit").Trailing != "100.0 MB" ||
		node(s, "ad_limit").Subtitle != "A notice at 80%, then its mobile data is blocked until the cycle ends or the limit is raised" {
		t.Fatalf("the rows: %+v", s.Body)
	}
	s = p.appDataScreen("com.phone.browser")
	if n := node(s, "ad_bg"); n.Trailing != "Off" || !strings.HasPrefix(n.Subtitle, "Off: mobile data only while the app is on the screen") ||
		node(s, "ad_free").Subtitle != "Has no effect while its background data is off" || node(s, "ad_limit").Trailing != "Not set" {
		t.Fatalf("background data off: %+v", s.Body)
	}
	if !strings.Contains(texts(s), "Mobile data only while it is on the screen") {
		t.Fatalf("the note: %q", texts(s))
	}
	p.s.Data.Apps[0].WifiOnly = true
	if !strings.Contains(texts(p.appDataScreen("com.phone.browser")), "Wi-Fi only in App permissions") {
		t.Fatal("Wi-Fi only")
	}
	p.s.Data.Source = ui.TrafficSourceLinks
	s = p.appDataScreen("com.phone.chat")
	if n := node(s, "ad_limit"); n.Actions != nil || !strings.Contains(n.Subtitle, "no per-app counters") || !strings.Contains(texts(s), "its connections go through") {
		t.Fatalf("an old kernel: %+v %q", n, texts(s))
	}
	p.s.Data.Source = ui.TrafficSourceSim
	if !strings.Contains(texts(p.appDataScreen("com.phone.chat")), "Simulated") {
		t.Fatal("a developer machine")
	}
	if a := amountPage("adlimit:com.phone.chat", 0); a.Title != "Mobile data limit" || node(a, "v").Value != "" {
		t.Fatalf("the limit's amount page: %+v", a)
	}
	if s, ok := p.dataScreenByID("appdata:com.phone.chat"); !ok || s.ID != "appdata:com.phone.chat" {
		t.Fatal("not restored")
	}
}

// The apps' pages send their switches and limit as the app's settings,
// Data Saver's switch as a setting of its own; the lines open the pages.
func TestAppDataActions(t *testing.T) {
	p, _, reqs := fakeShell(t)
	ready := make(chan struct{})
	p.a.Post(func() { p.s.Data = appData(); close(ready) })
	<-ready
	for _, c := range []struct {
		e    sdk.ActionEvent
		want ui.SettingArgs
	}{
		{sdk.ActionEvent{Screen: "appdata:com.phone.store", Node: "ad_bg", Action: "toggle"}, ui.SettingArgs{Key: ui.SettingAppBackgroundData, Value: 0, Text: "com.phone.store"}},
		{sdk.ActionEvent{Screen: "appdata:com.phone.store", Node: "ad_bg", Action: "toggle"}, ui.SettingArgs{Key: ui.SettingAppBackgroundData, Value: 1, Text: "com.phone.store"}},
		{sdk.ActionEvent{Screen: "appdata:com.phone.chat", Node: "ad_free", Action: "toggle"}, ui.SettingArgs{Key: ui.SettingAppDataUnrestricted, Value: 1, Text: "com.phone.chat"}},
		{sdk.ActionEvent{Screen: "datasaver", Node: "ds_on", Action: "toggle"}, ui.SettingArgs{Key: ui.SettingDataSaver, Value: 1}},
		{sdk.ActionEvent{Screen: "adlimit:com.phone.chat", Action: "save", Fields: map[string]string{"v": "200 MB"}}, ui.SettingArgs{Key: ui.SettingAppDataLimit, Value: 200 << 20, Text: "com.phone.chat"}},
		{sdk.ActionEvent{Screen: "adlimit:com.phone.chat", Action: "save", Fields: map[string]string{"v": ""}}, ui.SettingArgs{Key: ui.SettingAppDataLimit, Value: 0, Text: "com.phone.chat"}},
	} {
		p.a.Post(func() { p.action(c.e) })
		select {
		case m := <-reqs:
			var args ui.SettingArgs
			if err := json.Unmarshal(m.Args, &args); err != nil || m.Cap != ui.CapSettingsSet || args != c.want {
				t.Fatalf("%+v: %s %s, want %+v", c.e, m.Cap, m.Args, c.want)
			}
			p.a.Post(func() { p.sets = nil })
		case <-time.After(2 * time.Second):
			t.Fatalf("%+v: nothing sent", c.e)
		}
	}
	pages := make(chan []string)
	for _, e := range []sdk.ActionEvent{
		{Screen: "traffic", Node: "m:com.phone.store", Action: ui.ActionOpen},
		{Screen: "traffic", Node: "w:com.phone.browser", Action: ui.ActionOpen},
		{Screen: "traffic", Node: "datasaver", Action: ui.ActionOpen},
		{Screen: "datasaver", Node: "da:com.phone.chat", Action: ui.ActionOpen},
		{Screen: "traffic", Node: "m:" + ui.TrafficLineHotspot, Action: ui.ActionOpen},
	} {
		p.a.Post(func() { p.action(e); pages <- slices.Clone(p.pages) })
	}
	var got []string
	for range 5 {
		got = <-pages
	}
	if want := []string{"appdata:com.phone.store", "appdata:com.phone.browser", "datasaver", "appdata:com.phone.chat"}; !slices.Equal(got, want) {
		t.Fatalf("pages opened: %q, want %q", got, want)
	}
	select {
	case m := <-reqs:
		t.Fatalf("opening a page sent %s", m.Cap)
	case <-time.After(200 * time.Millisecond):
	}
}

// Mobile data held off by the plan says so, and turning it on asks first;
// the hotspot's daily limit is a choice of its own, and once the devices
// used it turning the hotspot on asks first.
func TestDataLimitRows(t *testing.T) {
	defer func(saved ui.HotspotState, loaded bool) { hsState.HotspotState, hsState.loaded = saved, loaded }(hsState.HotspotState, hsState.loaded)
	i18n.SetLang("en")
	var p app
	p.radio = ui.RadioState{Cellular: "on", LimitHeld: true}
	if n := p.mobileDataItem(); n.Subtitle != "Off: this cycle's data limit is used up" || n.Trailing != "Off" || !strings.HasPrefix(n.Actions[0].Confirm, "Turn mobile data on anyway?") {
		t.Fatalf("held: %+v", n)
	}
	p.radio.LimitHeld, p.radio.MobileData = false, true
	if n := p.mobileDataItem(); n.Actions[0].Confirm != "" {
		t.Fatalf("not held: %+v", n)
	}

	hsState.loaded = true
	hsState.HotspotState = ui.HotspotState{Backend: "sim", SSID: "Kario-1A2B", DailyLimit: 1 << 30, Usage: ui.HotspotUsage{TodayDown: 1 << 30, LimitReached: true}}
	s := p.hotspotScreen()
	if n := node(s, "hs_limit"); n.Trailing != "1.00 GB a day" || n.Subtitle != "Today's limit is used up" {
		t.Fatalf("the limit's row: %+v", n)
	}
	if n := node(s, "hs_on"); !strings.HasPrefix(n.Actions[0].Confirm, "Turn the hotspot on anyway?") {
		t.Fatalf("the switch: %+v", n)
	}
	hsState.DailyLimit, hsState.Usage = 0, ui.HotspotUsage{}
	s = p.hotspotScreen()
	if n := node(s, "hs_limit"); n.Trailing != "No daily limit" || n.Subtitle != "The devices may use any amount" || node(s, "hs_on").Actions[0].Confirm != "" {
		t.Fatalf("no limit: %+v", s.Body)
	}
}
