package main

import (
	"fmt"
	"strings"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// The top of Settings lists groups in the order phones put them; a group of
// several rows is a page of its own ("g." IDs), a group of one is that row.
// A row's ID and its handling are the same on whichever page shows it.

type group struct {
	id, title string
	rows      []string
}

const groupPrefix = "g."

func groups() []group {
	return []group{
		{groupPrefix + "device", tr.T("My device"), []string{"about", "update", "switch"}},
		{"", "", []string{"accounts"}},
		{"", "", []string{"wifi"}},
		{"", "", []string{"bt"}},
		{groupPrefix + "mobile", tr.T("Mobile network"), []string{"airplane", "mobiledata", "roaming", "nationalroaming", "apn", "simlock", "traffic"}},
		{"", "", []string{"hotspot"}},
		{"", "", []string{"vpn"}},
		{groupPrefix + "connect", tr.T("More connections"), []string{"nfc", "usb"}},
		{"", "", []string{"wallpaper"}},
		{"", "", []string{"lock"}},
		{groupPrefix + "status", tr.T("Notifications and status bar"), []string{ui.SettingNetSpeed, ui.SettingBatteryPercent, "alerts"}},
		{groupPrefix + "display", tr.T("Display and brightness"), []string{ui.SettingBrightness, ui.SettingAutoBrightness, ui.SettingRefreshRate, ui.SettingMotion, ui.SettingTheme, ui.SettingLargeText}},
		{"", "", []string{"sound"}},
		{"", "", []string{"security"}},
		{groupPrefix + "privacy", tr.T("Privacy and security"), []string{"perms", "location", "blocking", "probe"}},
		{groupPrefix + "apps", tr.T("Apps"), []string{"apps", "defaults"}},
		{"", "", []string{"battery"}},
		{groupPrefix + "wellbeing", tr.T("Digital wellbeing"), []string{"screentime", "minor"}},
		{a11yGroup, tr.T("Accessibility"), a11yRows()},
		{groupPrefix + "more", tr.T("More settings"), []string{ui.SettingLanguage, ui.SettingRegion, "datetime", ui.SettingIMEFuzzy, "devmode", "unlock", "reset"}},
		{"", "", []string{"crashes"}},
	}
}

func (p *app) brightness() int {
	if b := p.s.Settings.Brightness; b > 0 {
		return b
	}
	return p.s.Backlight.Brightness
}

// onMenu: the page lists rows of the top (the root or a group's page).
func onMenu(screen string) bool {
	return screen == "root" || strings.HasPrefix(screen, groupPrefix)
}

func (p *app) root() ui.Screen {
	s := ui.Screen{ID: "root", Title: tr.T("Settings"), Actions: []ui.Action{{ID: "probe", Label: tr.T("Sandbox check"), Key: "s"}}}
	for _, g := range groups() {
		if g.id == "" {
			if n, ok := p.row(g.rows[0]); ok {
				s.Body = append(s.Body, n)
			}
			continue
		}
		var titles []string
		for _, id := range g.rows {
			if n, ok := p.row(id); ok {
				titles = append(titles, n.Title)
			}
		}
		sub := strings.Join(titles, " · ")
		switch g.id {
		case groupPrefix + "device":
			sub = fmt.Sprint(p.s.About["system"], " ", p.s.About["version"])
		case groupPrefix + "display":
			sub = p.themeName() + " · " + tr.T("Brightness %s", pct(float64(p.brightness())/100))
		case a11yGroup:
			sub = p.a11ySummary()
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: g.id, Title: g.title, Subtitle: sub, Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
	}
	return s
}

func (p *app) groupScreen(id string) (ui.Screen, bool) {
	for _, g := range groups() {
		if g.id != id || id == "" {
			continue
		}
		s := ui.Screen{ID: g.id, Title: g.title}
		if id == a11yGroup {
			return p.a11yPage(s), true
		}
		for _, r := range g.rows {
			if n, ok := p.row(r); ok {
				s.Body = append(s.Body, n)
			}
		}
		return s, true
	}
	return ui.Screen{}, false
}

// openGroups are the group pages open now: refreshing the others would
// only cost time (an app slow to refresh is late to hand over its state).
func (p *app) openGroups() []ui.Screen {
	var out []ui.Screen
	for _, id := range p.pages {
		if s, ok := p.groupScreen(id); ok {
			out = append(out, s)
		}
	}
	return out
}

// updateMenu shows a row's new state wherever it is listed.
func (p *app) updateMenu() {
	p.a.Update(p.root())
	for _, s := range p.openGroups() {
		p.a.Update(s)
	}
}

// groupOf is the group page a row is on, "" for a row of the root.
func groupOf(row string) string {
	for _, g := range groups() {
		for _, r := range g.rows {
			if r == row {
				return g.id
			}
		}
	}
	return ""
}

// show is a deep link (the palette's Settings commands): the row of that ID
// focused where it lives, on the root or on its group's page.
func (p *app) show(row string) {
	root := p.root()
	p.pages, p.radioPage, p.updatePage = nil, "", false
	// The reset drops the pages without OnPopped.
	defer p.btPopped(btPairPage)
	if row == ui.SettingSilent {
		root.Focus = "sound"
		p.a.Reset(root)
		s := p.soundScreen()
		s.Focus = row
		p.push(s)
		return
	}
	g := groupOf(row)
	if g == "" {
		root.Focus = row
	}
	p.a.Reset(root)
	if s, ok := p.groupScreen(g); ok {
		if _, here := p.row(row); here {
			s.Focus = row
		}
		p.push(s)
	}
}

func (p *app) row(id string) (ui.Node, bool) {
	st := p.s.Settings
	item := func(title, sub string) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: id, Title: title, Subtitle: sub, Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
	}
	toggle := func(title, sub string, on bool) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: id, Title: title, Subtitle: sub, Role: ui.RoleSwitch, Checked: on,
			Trailing: onOff(on), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}}
	}
	if n, ok := p.a11yRow(id); ok {
		return n, true
	}
	switch id {
	case "about":
		return item(tr.T("About phone"), fmt.Sprint(p.s.About["system"], " ", p.s.About["version"])), true
	case "update":
		return item(tr.T("System update"), tr.T("Check for and install new versions")), true
	case "switch":
		return p.switchItem(), true
	case "accounts":
		return p.accountsItem(), true
	case "wifi":
		return item("Wi-Fi", p.wifiSummary()), true
	case "bt":
		return item(tr.T("Bluetooth"), p.btSummary()), true
	case "airplane":
		return p.airplaneItem(), true
	case "mobiledata":
		return p.mobileDataItem(), p.radio.Cellular != ""
	case "roaming":
		return p.roamingItem(), p.radio.Cellular != ""
	case "nationalroaming":
		return p.nationalRoamingItem(), p.radio.Cellular != ""
	case "apn":
		return p.apnItem(), p.radio.Cellular != ""
	case "simlock":
		return p.simLockItem()
	case "traffic":
		return item(tr.T("Data usage"), p.trafficSummary()), true
	case "hotspot":
		return p.hotspotItem(), true
	case "vpn":
		return item("VPN", vpnSummary()), true
	case "nfc":
		return p.nfcItem(), true
	case "usb":
		return p.usbItem(), true
	case "wallpaper":
		return item(tr.T("Wallpaper"), tr.T("Idle %s · Lock %s", setLabel(st.WallpaperIdle), setLabel(st.WallpaperLock))), true
	case "lock":
		return item(tr.T("Keypad lock"), tr.T("Auto lock: %s · Screen off: %s", lockLabel(st.AutoLock), lockLabel(screenOff(st.ScreenOff)))), true
	case ui.SettingNetSpeed:
		return toggle(tr.T("Show network speed"), tr.T("The bytes per second the phone sends and receives, at the top right"), st.NetSpeed), true
	case ui.SettingBatteryPercent:
		return toggle(tr.T("Show battery percentage"), tr.T("Beside the battery in the status bar"), !st.HidePercent), true
	case "alerts":
		return p.alertsItem(), true
	case ui.SettingBrightness:
		sub := tr.T("← dimmer · → brighter, in steps of 10%")
		switch {
		case p.s.Backlight.Simulated:
			sub = tr.T("Simulated: this machine has no backlight")
		case st.AutoBright:
			sub = tr.T("The level in an ordinary room; darker in the dark, brighter outdoors")
		}
		return ui.Node{Kind: ui.KindItem, ID: id, Title: tr.T("Brightness"), Subtitle: sub, Trailing: pct(float64(p.brightness()) / 100),
			Actions: []ui.Action{{ID: "up", Label: tr.T("Brighter"), Primary: true, Key: "right"}, {ID: "down", Label: tr.T("Dimmer"), Key: "left"}}}, true
	case ui.SettingAutoBrightness:
		sub := tr.T("Follows the light around the phone")
		switch {
		case st.AutoBright && p.s.Backlight.SensorSimulated:
			sub = tr.T("Simulated light sensor: this machine has none")
		case st.AutoBright:
			sub = tr.T("%d lux now, backlight %s", int(p.s.Backlight.Lux), pct(float64(p.s.Backlight.Level)/100))
		}
		return toggle(tr.T("Auto brightness"), sub, st.AutoBright), true
	case ui.SettingRefreshRate:
		return p.refreshItem()
	case ui.SettingMotion:
		return ui.Node{Kind: ui.KindItem, ID: id, Title: tr.T("Animations"), Subtitle: motionHint(st.Motion), Trailing: motionName(st.Motion),
			Actions: []ui.Action{{ID: "next", Label: tr.T("Next"), Primary: true, Key: "right"}, {ID: "prev", Label: tr.T("Previous"), Key: "left"}}}, true
	case ui.SettingTheme:
		return item(tr.T("Theme"), p.themeName()), true
	case ui.SettingLargeText:
		return toggle(tr.T("Large text"), tr.T("The system and every app get one size bigger"), st.LargeText), true
	case "sound":
		return item(tr.T("Sound and vibration"), tr.T("Ringtone %s · Media %s", pct(st.RingVolume), pct(st.MediaVolume))), true
	case "security":
		return p.securityItem(), true
	case "perms":
		return item(tr.T("App permissions"), tr.T("%d runtime grants", len(p.perms))), true
	case "location":
		return p.locationItem(), true
	case "probe":
		return item(tr.T("Sandbox check"), tr.T("Tries one thing after another as a malicious app")), true
	case "apps":
		return item(tr.T("App settings"), tr.T("Settings apps declare in their manifests, shown by the system")), true
	case "defaults":
		return p.defaultsItem(), true
	case "blocking":
		return p.blockingItem(), true
	case "battery":
		return p.batteryItem(), true
	case "screentime", "minor":
		return p.wellbeingRow(id), true
	case ui.SettingLanguage, ui.SettingRegion, "datetime":
		for _, n := range p.localeItems() {
			if n.ID == id {
				return n, true
			}
		}
	case ui.SettingIMEFuzzy:
		return toggle(tr.T("Fuzzy pinyin"), tr.T("z/zh, c/ch, s/sh, n/l, an/ang, en/eng, in/ing count as the same"), st.IMEFuzzy), true
	case "devmode":
		return ui.Node{Kind: ui.KindItem, ID: id, Title: tr.T("Developer mode"), Subtitle: tr.T("Trust apps signed by the developer store"),
			Trailing: onOff(st.DeveloperMode),
			Actions:  []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true, Confirm: tr.T("Switch developer mode? Once it is off, apps from the developer store no longer run.")}}}, true
	case "unlock":
		return ui.Node{Kind: ui.KindItem, ID: id, Title: tr.T("Unlock device"), Subtitle: tr.T("Run system components you built yourself; erases all app keys"),
			Trailing: map[bool]string{true: tr.T("Unlocked"), false: tr.T("Locked")}[p.s.Unlocked],
			Actions:  []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}}, true
	case "crashes":
		return item(tr.T("App crashes"), tr.T("%d records", len(p.s.Crashes))), true
	case "reset":
		return item(tr.T("Factory reset"), tr.T("Erase everything on this phone")), true
	}
	return ui.Node{}, false
}

func (p *app) simLockItem() (ui.Node, bool) {
	sim := p.s.Sim
	if sim == nil || sim.State != "ready" {
		return ui.Node{}, false
	}
	// The PIN is entered on the system's screen, not here.
	n := ui.Node{Kind: ui.KindItem, ID: "simlock", Title: tr.T("SIM lock"), Subtitle: tr.T("Without the PIN at start-up, the card cannot be used"),
		Trailing: onOff(sim.PinLock), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}}
	if sim.PinLock {
		n.Subtitle = tr.T("The PIN is needed at start-up")
		n.Actions = append(n.Actions, ui.Action{ID: "change", Label: tr.T("Change PIN"), Key: "c"})
	}
	return n, true
}

func motionName(m string) string {
	switch m {
	case ui.MotionReduced:
		return tr.T("Reduced")
	case ui.MotionOff:
		return tr.T("Off")
	}
	return tr.T("Standard")
}

func motionHint(m string) string {
	switch m {
	case ui.MotionReduced:
		return tr.T("No page or menu transitions; the focus and scrolling still move")
	case ui.MotionOff:
		return tr.T("Nothing on the screen moves")
	}
	return tr.T("Pages slide in, menus rise, the focus glides; less on a low battery")
}
