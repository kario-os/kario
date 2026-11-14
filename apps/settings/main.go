// Settings is a privileged system app: perm.* and settings.* capabilities
// require the "system" manifest permission. It reviews app permissions and
// edits the system settings the shell keeps (volumes, auto keypad lock).
package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

var stateLabel = map[string]string{"granted": "Allowed", "denied": "Denied", "ask": "Ask", "wifi": "Wi-Fi only"}

type settings struct {
	Settings struct {
		AutoLock      int     `json:"auto_lock"`
		ScreenOff     int     `json:"screen_off"`
		RingVolume    float64 `json:"ring_volume"`
		MediaVolume   float64 `json:"media_volume"`
		NotifyVolume  float64 `json:"notify_volume"`
		DeveloperMode bool    `json:"developer_mode"`
		Silent        bool    `json:"silent"`
		LargeText     bool    `json:"large_text"`
		IMEFuzzy      bool    `json:"ime_fuzzy"`
		WallpaperIdle string  `json:"wallpaper_idle"`
		WallpaperLock string  `json:"wallpaper_lock"`
		// "" leaves them to the system (language.go).
		Language   string `json:"language"`
		Region     string `json:"region"`
		Zone       string `json:"time_zone"`
		ManualTime bool   `json:"manual_time"`
		Clock12    bool   `json:"clock_12h"`
	} `json:"settings"`
	Choices []int `json:"auto_lock_choices"`
	// ScreenOffChoices: seconds without a key before the screen goes off.
	ScreenOffChoices []int `json:"screen_off_choices"`
	// Themes are the names settings.set "theme" takes the index of.
	Themes     []string       `json:"themes"`
	ThemeIndex int            `json:"theme_index"`
	About      map[string]any `json:"about"`
	Unlocked   bool           `json:"device_unlocked"`
	Crashes    []struct {
		Time   time.Time `json:"time"`
		Name   string    `json:"name"`
		Reason string    `json:"reason"`
		Log    []string  `json:"log"`
	} `json:"crashes"`
	// Traffic is absent without the per-app meter (it needs root).
	Traffic []ui.TrafficEntry `json:"traffic"`
	Energy  []ui.EnergyEntry  `json:"energy"`
	Battery batteryInfo       `json:"battery"`
	// IMEI is the baseband's, "" without one.
	IMEI string `json:"imei"`
	// Sim is absent without a baseband.
	Sim *struct {
		State   string `json:"state"`
		PinLock bool   `json:"pin_lock"`
	} `json:"sim"`
	localeInfo
	securityInfo
	usbInfo
	alertsInfo
}

func bytesLabel(n uint64) string {
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

func pct(v float64) string { return fmt.Sprintf("%d%%", int(v*100+0.5)) }

func setLabel(path string) string {
	return map[bool]string{true: tr.T("Photo"), false: tr.T("Default")}[path != ""]
}

func lockLabel(s int) string {
	switch {
	case s == 0:
		return tr.T("Off")
	case s < 60:
		return tr.T("%d s", s)
	}
	return tr.T("%d min", s/60)
}

type app struct {
	a       *sdk.App
	perms   []ui.PermEntry
	s       settings
	visible string
	// apps are the settings other apps declare (App settings).
	apps []ui.AppSettings
	upd  update
	// The radios (radios.go); radioPage: the Wi-Fi or Bluetooth page is open
	// (they refresh while it is).
	wifi      ui.WifiState
	bt        ui.BTState
	radio     ui.RadioState
	radioPage string
	eapMethod int // the enterprise network page's method (eapMethods)
	// pages: the IDs of the pages open above the root that push opened
	// (handed over by OnSave).
	pages []string
}

// update is system.update's answer (the shell's UpdateStatus).
type update struct {
	Supported bool   `json:"supported"`
	Version   string `json:"version"`
	Slot      string `json:"slot"`
	Pending   string `json:"pending"`
	State     string `json:"state"`
	Error     string `json:"error"`
	Held      string `json:"held"`
	Available *struct {
		Version string `json:"version"`
		Size    int64  `json:"size"`
		Notes   string `json:"notes"`
		// Delta: the incremental bundle for the system this phone runs.
		Delta *struct {
			Size int64 `json:"size"`
		} `json:"delta"`
	} `json:"available"`
}

func slotName(s string) string { return tr.T("System slot %s", strings.ToUpper(s)) }

func (p *app) updateScreen() ui.Screen {
	u := p.upd
	s := ui.Screen{ID: "update", Title: tr.T("System update")}
	row := func(id, title, value string) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: id, Title: title, Trailing: value}
	}
	s.Body = append(s.Body, row("version", tr.T("Current version"), u.Version))
	if !u.Supported {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("This device cannot update online: it has no A/B pair of system partitions.")))
		return s
	}
	s.Actions = append(s.Actions, ui.Action{ID: "check", Label: tr.T("Check for updates"), Key: "c"})
	s.Body = append(s.Body, row("slot", tr.T("Running"), slotName(u.Slot)))
	if u.Pending != "" {
		s.Body = append(s.Body, row("pending", tr.T("Next start"), tr.T("%s (newly installed)", slotName(u.Pending))))
	}
	switch u.State {
	case "installing":
		s.Body = append(s.Body, sdk.Styled(ui.StyleAccent, tr.T("Installing… You can keep using the phone meanwhile.")))
	case "installed":
		s.Body = append(s.Body, sdk.Styled(ui.StyleOK, tr.T("Installed; it takes effect after a restart.")))
		s.Actions = append(s.Actions, ui.Action{ID: "reboot", Label: tr.T("Restart"), Key: "r", Confirm: tr.T("Restart into the new system now?")})
	case "failed":
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, tr.T("Not updated: %s", u.Error)))
	}
	if u.Held != "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No update to install for now: %s", u.Held)))
	}
	if a := u.Available; a != nil && u.State != "installing" && u.State != "installed" {
		var delta *int64
		if a.Delta != nil {
			delta = &a.Delta.Size
		}
		sub := a.Notes
		if size := offerSize(a.Size, delta); size != "" {
			sub = strings.TrimPrefix(sub+" · "+size, " · ")
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "available", Title: tr.T("Available: %s", a.Version), Subtitle: sub,
			Actions: []ui.Action{{ID: "install", Label: tr.T("Install"), Primary: true, Confirm: tr.T("Install this system update? It takes effect after a restart; if that fails, the phone goes back to the current system.")}}})
	}
	return s
}

// offerSize is what installing the offer downloads: the incremental bundle
// the shell chose for this system where there is one (delta, its size; 0
// when the server did not say), never the full bundle's size alone then,
// else the full bundle.
func offerSize(full int64, delta *int64) string {
	switch {
	case delta != nil && *delta > 0 && full > 0:
		return tr.T("%s incremental (the full system is %s)", bytesLabel(uint64(*delta)), bytesLabel(uint64(full)))
	case delta != nil && *delta > 0:
		return tr.T("%s incremental", bytesLabel(uint64(*delta)))
	case delta != nil:
		return tr.T("Incremental")
	case full > 0:
		return bytesLabel(uint64(full))
	}
	return ""
}

func (p *app) updateAction(action string) {
	if action == "install" {
		p.upd.State = "installing"
		p.a.Update(p.updateScreen())
	}
	p.a.Go(ui.CapSystemUpdate, ui.UpdateArgs{Action: action}, &p.upd, func(err error) {
		if err != nil {
			p.a.Toast(err.Error())
		}
		p.a.Update(p.updateScreen())
	})
}

func (p *app) root() ui.Screen {
	st := p.s.Settings
	item := func(id, title, sub string) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: id, Title: title, Subtitle: sub, Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
	}
	toggle := func(id, title, sub string, on bool) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: id, Title: title, Subtitle: sub,
			Trailing: onOff(on), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}}
	}
	s := ui.Screen{ID: "root", Title: tr.T("Settings"), Actions: []ui.Action{{ID: "probe", Label: tr.T("Sandbox check"), Key: "s"}},
		Body: []ui.Node{
			item("perms", tr.T("App permissions"), tr.T("%d runtime grants", len(p.perms))),
			item("apps", tr.T("App settings"), tr.T("Settings apps declare in their manifests, shown by the system")),
			p.airplaneItem(),
			item("wifi", "Wi-Fi", p.wifiSummary()),
			item("bt", tr.T("Bluetooth"), p.btSummary()),
			p.nfcItem(),
			p.usbItem(),
			item("traffic", tr.T("Data usage"), p.trafficSummary()),
			p.batteryItem(),
			item("sound", tr.T("Sound"), tr.T("Ringtone %s · Media %s", pct(st.RingVolume), pct(st.MediaVolume))),
			item(ui.SettingTheme, tr.T("Theme"), p.themeName()),
			item("wallpaper", tr.T("Wallpaper"), tr.T("Idle %s · Lock %s", setLabel(st.WallpaperIdle), setLabel(st.WallpaperLock))),
			item("lock", tr.T("Keypad lock"), tr.T("Auto lock: %s · Screen off: %s", lockLabel(st.AutoLock), lockLabel(screenOff(st.ScreenOff)))),
			p.securityItem(),
			p.alertsItem(),
			toggle(ui.SettingSilent, tr.T("Silent mode"), tr.T("Calls and notifications only vibrate; alarms still ring"), st.Silent),
			toggle(ui.SettingLargeText, tr.T("Large text"), tr.T("The system and every app get one size bigger"), st.LargeText),
			toggle(ui.SettingIMEFuzzy, tr.T("Fuzzy pinyin"), tr.T("z/zh, c/ch, s/sh, n/l, an/ang, en/eng, in/ing count as the same"), st.IMEFuzzy),
			item("probe", tr.T("Sandbox check"), tr.T("Tries one thing after another as a malicious app")),
			{Kind: ui.KindItem, ID: "devmode", Title: tr.T("Developer mode"), Subtitle: tr.T("Trust apps signed by the developer store"),
				Trailing: onOff(st.DeveloperMode),
				Actions:  []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true, Confirm: tr.T("Switch developer mode? Once it is off, apps from the developer store no longer run.")}}},
			{Kind: ui.KindItem, ID: "unlock", Title: tr.T("Unlock device"), Subtitle: tr.T("Run system components you built yourself; erases all app keys"),
				Trailing: map[bool]string{true: tr.T("Unlocked"), false: tr.T("Locked")}[p.s.Unlocked],
				Actions:  []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}},
			item("update", tr.T("System update"), tr.T("Check for and install new versions")),
			item("about", tr.T("About phone"), fmt.Sprint(p.s.About["system"], " ", p.s.About["version"])),
		}}
	for i, x := range s.Body {
		if x.ID == "traffic" {
			s.Body = append(s.Body[:i+1], append(p.localeItems(), s.Body[i+1:]...)...)
			break
		}
	}
	if p.radio.Cellular != "" {
		for i, x := range s.Body {
			if x.ID == "airplane" {
				s.Body = append(s.Body[:i+1], append([]ui.Node{p.mobileDataItem()}, s.Body[i+1:]...)...)
				break
			}
		}
	}
	if sim := p.s.Sim; sim != nil && sim.State == "ready" {
		// The PIN is entered on the system's screen, not here.
		n := ui.Node{Kind: ui.KindItem, ID: "simlock", Title: tr.T("SIM lock"), Subtitle: tr.T("Without the PIN at start-up, the card cannot be used"),
			Trailing: onOff(sim.PinLock), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}}
		if sim.PinLock {
			n.Subtitle = tr.T("The PIN is needed at start-up")
			n.Actions = append(n.Actions, ui.Action{ID: "change", Label: tr.T("Change PIN"), Key: "c"})
		}
		at := len(s.Body)
		for i, x := range s.Body {
			if x.ID == "lock" {
				at = i + 1
			}
		}
		s.Body = append(s.Body[:at], append([]ui.Node{n}, s.Body[at:]...)...)
	}
	return s
}

func (p *app) permsScreen() ui.Screen {
	s := ui.Screen{ID: "perms", Title: tr.T("App permissions"), Actions: []ui.Action{
		{ID: "refresh", Label: tr.T("Refresh"), Key: "r"}, {ID: "probe", Label: tr.T("Sandbox check"), Key: "s"}}}
	for _, e := range p.perms {
		sub := e.App
		if e.Uses > 0 {
			sub += " · " + tr.T("Last used %s · %d times in all", time.UnixMilli(e.LastUsed).Format("01-02 15:04"), e.Uses)
		}
		if e.Reset > 0 {
			sub += " · " + tr.T("Not opened for a long time; taken back on %s", time.UnixMilli(e.Reset).Format("01-02"))
		}
		actions := []ui.Action{
			{ID: "granted", Label: tr.T("Allow"), Key: "a"},
			{ID: "denied", Label: tr.T("Deny"), Key: "d"},
			{ID: "ask", Label: tr.T("Ask next time"), Key: "q", Primary: true},
		}
		switch e.Perm {
		case ui.PermNet:
			actions = []ui.Action{
				{ID: "granted", Label: tr.T("Allow"), Key: "a", Primary: true},
				{ID: "wifi", Label: tr.T("Wi-Fi only"), Key: "w"},
				{ID: "denied", Label: tr.T("Deny"), Key: "d"},
			}
		case ui.PermNotify:
			actions = []ui.Action{
				{ID: "granted", Label: tr.T("Allow"), Key: "a", Primary: true},
				{ID: "denied", Label: tr.T("Turn off"), Key: "d"},
			}
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: e.App + "/" + e.Perm, Title: e.Name + " · " + e.Label,
			Trailing: tr.T(stateLabel[e.State]), Subtitle: sub, Actions: actions})
	}
	if len(p.perms) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No app has asked for runtime permissions")))
	}
	return s
}

func (p *app) trafficSummary() string {
	if p.s.Traffic == nil {
		return tr.T("Needs per-app metering on the device")
	}
	var wifi, mobile uint64
	for _, t := range p.s.Traffic {
		wifi += t.WifiRx + t.WifiTx
		mobile += t.MobileRx + t.MobileTx
	}
	return tr.T("Wi-Fi %s · Mobile %s", bytesLabel(wifi), bytesLabel(mobile))
}

func (p *app) trafficScreen() ui.Screen {
	s := ui.Screen{ID: "traffic", Title: tr.T("Data usage"), Actions: []ui.Action{{ID: "refresh", Label: tr.T("Refresh"), Key: "r"}}}
	if p.s.Traffic == nil {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Counting data per app needs a uid for each app (the sandbox on the device); a development machine has none")))
		return s
	}
	for _, t := range p.s.Traffic {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "t:" + t.App, Title: t.Name,
			Trailing: bytesLabel(t.WifiRx + t.WifiTx + t.MobileRx + t.MobileTx),
			Subtitle: tr.T("Wi-Fi ↓%s ↑%s · Mobile ↓%s ↑%s", bytesLabel(t.WifiRx), bytesLabel(t.WifiTx), bytesLabel(t.MobileRx), bytesLabel(t.MobileTx))})
	}
	if len(p.s.Traffic) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No app has used the network yet")))
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Bytes at the IP layer; traffic between local services (loopback) does not count. Interfaces whose names start with wl count as Wi-Fi, all others as mobile. In “App permissions” an app can be set to Wi-Fi only")))
	return s
}

func (p *app) soundScreen() ui.Screen {
	st := p.s.Settings
	s := ui.Screen{ID: "sound", Title: tr.T("Sound")}
	for _, v := range []struct {
		key, label string
		val        float64
	}{{ui.SettingRingVolume, tr.T("Ringtone and alarms"), st.RingVolume}, {ui.SettingMediaVolume, tr.T("Media"), st.MediaVolume}, {ui.SettingNotifyVolume, tr.T("Notification sounds"), st.NotifyVolume}} {
		s.Body = append(s.Body,
			ui.Node{Kind: ui.KindItem, ID: v.key, Title: v.label, Trailing: pct(v.val), Actions: []ui.Action{
				{ID: "up", Label: tr.T("Louder"), Primary: true, Key: "right"}, {ID: "down", Label: tr.T("Quieter"), Key: "left"}}},
			sdk.Progress("", v.val, ""))
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("← quieter · → louder, in steps of 10%")))
	return s
}

func (p *app) themeName() string {
	if i := p.s.ThemeIndex; i < len(p.s.Themes) {
		return p.s.Themes[i]
	}
	return ""
}

func (p *app) themeScreen() ui.Screen {
	s := ui.Screen{ID: ui.SettingTheme, Title: tr.T("Theme")}
	for i, name := range p.s.Themes {
		n := ui.Node{Kind: ui.KindItem, ID: fmt.Sprint("theme:", i), Title: name, Actions: []ui.Action{{ID: "choose", Label: tr.T("Use"), Primary: true}}}
		if i == p.s.ThemeIndex {
			n.Badge = tr.T("Current")
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("The system and every app change colour together; web pages and photos stay as they are")))
	return s
}

func (p *app) wallpaperScreen() ui.Screen {
	st := p.s.Settings
	s := ui.Screen{ID: "wallpaper", Title: tr.T("Wallpaper")}
	for _, w := range []struct {
		id, title, sub, path string
	}{{"wp:1", tr.T("Idle screen"), tr.T("The idle screen while unlocked"), st.WallpaperIdle}, {"wp:2", tr.T("Lock screen"), tr.T("While the keypad is locked; without one, the idle wallpaper"), st.WallpaperLock}} {
		n := ui.Node{Kind: ui.KindItem, ID: w.id, Title: w.title, Subtitle: w.sub, Trailing: setLabel(w.path)}
		if w.path != "" {
			n.Actions = []ui.Action{{ID: "clear", Label: tr.T("Restore default"), Primary: true}}
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("While viewing a photo in “Gallery”, press Options and choose “Set as wallpaper”")))
	return s
}

func (p *app) lockScreen() ui.Screen {
	s := ui.Screen{ID: "lock", Title: tr.T("Keypad lock")}
	s.Body = append(s.Body, sdk.Section(tr.T("Auto lock")))
	for _, ch := range p.s.Choices {
		n := ui.Node{Kind: ui.KindItem, ID: fmt.Sprint("lock:", ch), Title: lockLabel(ch), Actions: []ui.Action{{ID: "choose", Label: tr.T("Choose"), Primary: true}}}
		if ch == p.s.Settings.AutoLock {
			n.Badge = tr.T("Current")
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("The keypad locks when the idle screen has had no key press for this long; to lock it any time, press the left soft key and then * on the idle screen")))
	s.Body = append(s.Body, sdk.Section(tr.T("Screen off")))
	for _, ch := range p.s.ScreenOffChoices {
		n := ui.Node{Kind: ui.KindItem, ID: fmt.Sprint("screen:", ch), Title: lockLabel(ch), Actions: []ui.Action{{ID: "choose", Label: tr.T("Choose"), Primary: true}}}
		if ch == screenOff(p.s.Settings.ScreenOff) {
			n.Badge = tr.T("Current")
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("With no key pressed for this long the screen goes off; any key lights it again. New messages do not light it: the notification light blinks slowly instead")))
	return s
}

// screenOff is the screen timeout in force (0 is the shell's default).
func screenOff(s int) int {
	if s <= 0 {
		return 30
	}
	return s
}

func (p *app) aboutScreen() ui.Screen {
	a := p.s.About
	row := func(label, key string) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: key, Title: label, Trailing: fmt.Sprint(a[key])}
	}
	s := ui.Screen{ID: "about", Title: tr.T("About phone"), Body: []ui.Node{
		sdk.Styled(ui.StyleBig, fmt.Sprint(a["system"])),
		row(tr.T("Version"), "version"), row(tr.T("UI protocol"), "proto"), row(tr.T("Screen"), "screen"), row(tr.T("Keypad"), "keypad"),
		row(tr.T("Kernel"), "kernel"), row(tr.T("Installed apps"), "apps"), row(tr.T("Uptime"), "uptime"),
		{Kind: ui.KindItem, ID: "crashes", Title: tr.T("App crashes"), Trailing: tr.T("%d records", len(p.s.Crashes)),
			Actions: []ui.Action{{ID: ui.ActionOpen, Label: tr.T("View"), Primary: true}}},
	}}
	if _, ok := a["product"]; ok {
		s.Body = append(s.Body[:2], append([]ui.Node{row(tr.T("Product"), "product")}, s.Body[2:]...)...)
	}
	if p.s.IMEI != "" {
		s.Body = append(s.Body[:len(s.Body)-1], ui.Node{Kind: ui.KindItem, ID: "imei", Title: "IMEI", Trailing: p.s.IMEI}, s.Body[len(s.Body)-1])
	}
	return s
}

func (p *app) appsScreen() ui.Screen {
	s := ui.Screen{ID: "appsettings", Title: tr.T("App settings")}
	for _, x := range p.apps {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "a:" + x.App, Icon: x.Icon, Title: x.Name,
			Subtitle: tr.T("%d settings", len(x.Settings)), Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
	}
	if len(s.Body) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No app declares settings")))
	}
	return s
}

func (p *app) appScreen(id string) ui.Screen {
	for _, x := range p.apps {
		if x.App != id {
			continue
		}
		s := ui.Screen{ID: "aps:" + id, Title: x.Name}
		for _, st := range x.Settings {
			n := ui.Node{Kind: ui.KindItem, ID: "k:" + st.Key, Title: st.Title}
			switch st.Type {
			case "toggle":
				on, _ := st.Value.(bool)
				n.Trailing = onOff(on)
				n.Actions = []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}
			case "number":
				n.Trailing = fmt.Sprint(st.Value)
				n.Actions = []ui.Action{{ID: "inc", Label: tr.T("Increase"), Primary: true, Key: "+"}, {ID: "dec", Label: tr.T("Decrease"), Key: "-"}}
			case "text":
				n.Trailing = fmt.Sprint(st.Value)
				n.Actions = []ui.Action{{ID: "edit", Label: tr.T("Edit"), Primary: true}}
			default:
				n.Trailing = fmt.Sprint(st.Value)
				for i, ch := range st.Choices {
					if ch == st.Value && i < len(st.Labels) {
						n.Trailing = st.Labels[i]
					}
				}
				n.Actions = []ui.Action{{ID: "next", Label: tr.T("Next option"), Primary: true}}
			}
			s.Body = append(s.Body, n)
		}
		return s
	}
	return ui.Screen{ID: "aps:" + id, Title: tr.T("App settings"), Body: []ui.Node{sdk.Styled(ui.StyleDim, tr.T("This app has been uninstalled"))}}
}

func (p *app) setAppSetting(id, key string, v any) {
	p.a.Go(ui.CapSettingsAppSet, ui.AppSettingArgs{App: id, Key: key, Value: v}, nil, func(err error) {
		if err != nil {
			p.a.Toast(err.Error())
		}
		p.a.Go(ui.CapSettingsApps, nil, &p.apps, func(error) { p.a.Update(p.appScreen(id)) })
	})
}

func (p *app) setting(id, key string) (ui.AppSetting, bool) {
	for _, x := range p.apps {
		for _, st := range x.Settings {
			if x.App == id && st.Key == key {
				return st, true
			}
		}
	}
	return ui.AppSetting{}, false
}

// changeAppSetting flips a toggle, moves a choice on, steps a number or
// opens a text for editing, through the system.
func (p *app) changeAppSetting(id, key, action string) {
	st, ok := p.setting(id, key)
	if !ok {
		return
	}
	switch st.Type {
	case "number":
		v, _ := st.Value.(float64)
		step := st.Step
		if step == 0 {
			step = 1
		}
		if action == "dec" {
			step = -step
		}
		p.setAppSetting(id, key, max(st.Min, min(st.Max, v+step)))
		return
	case "text":
		p.a.Push(ui.Screen{ID: "apt:" + id + ":" + key, Title: st.Title, Body: []ui.Node{
			{Kind: ui.KindInput, ID: "v", Value: fmt.Sprint(st.Value), Placeholder: st.Title, Submit: "save"},
		}})
		return
	}
	p.changeChoice(id, key)
}

func (p *app) changeChoice(id, key string) {
	for _, x := range p.apps {
		for _, st := range x.Settings {
			if x.App != id || st.Key != key {
				continue
			}
			var v any
			if st.Type == "toggle" {
				on, _ := st.Value.(bool)
				v = !on
			} else {
				i := 0
				for j, ch := range st.Choices {
					if ch == st.Value {
						i = j
					}
				}
				v = st.Choices[(i+1)%len(st.Choices)]
			}
			p.setAppSetting(id, key, v)
		}
	}
}

func (p *app) crashScreen() ui.Screen {
	s := ui.Screen{ID: "crashes", Title: tr.T("App crashes")}
	for i := len(p.s.Crashes) - 1; i >= 0; i-- {
		r := p.s.Crashes[i]
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: fmt.Sprint("c", i), Title: r.Name,
			Subtitle: r.Reason, Trailing: r.Time.Local().Format("01-02 15:04"),
			Actions: []ui.Action{{ID: ui.SettingCrashSend, Label: tr.T("Send to the developer"), Key: "s"}}})
	}
	if len(s.Body) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No records")))
	}
	return s
}

func (p *app) load(show func(ui.Screen), next func() ui.Screen) {
	p.a.Go(ui.CapPermList, nil, &p.perms, func(err error) {
		if err != nil {
			p.a.Toast(err.Error())
		}
		p.a.Go(ui.CapSettingsGet, nil, &p.s, func(err error) {
			if err != nil {
				p.a.Toast(err.Error())
			}
			// The shell answers radio.state itself, so the airplane and mobile
			// data rows are right from the first paint: a row added later
			// moves the ones under it while the user is picking. The radio
			// daemons may be slow to answer; the rest does not wait.
			p.a.Go(ui.CapRadioState, nil, &p.radio, func(error) {
				show(next())
				p.loadRadios(p.refresh)
			})
		})
	})
}

func (p *app) refresh() {
	for _, s := range []ui.Screen{p.root(), p.permsScreen(), p.trafficScreen(), p.batteryScreen(), p.soundScreen(), p.themeScreen(), p.wallpaperScreen(),
		p.lockScreen(), p.aboutScreen(), p.crashScreen(), p.wifiScreen(), p.btScreen()} {
		p.a.Update(s)
	}
	p.refreshLocale()
}

// screenByID builds a page of the app's own (not the locale pages) by its
// ID, for one restored after a restart.
func (p *app) screenByID(id string) (ui.Screen, bool) {
	switch id {
	case "perms":
		return p.permsScreen(), true
	case "traffic":
		return p.trafficScreen(), true
	case "battery":
		return p.batteryScreen(), true
	case "sound":
		return p.soundScreen(), true
	case ui.SettingTheme:
		return p.themeScreen(), true
	case "wallpaper":
		return p.wallpaperScreen(), true
	case "lock":
		return p.lockScreen(), true
	case "about":
		return p.aboutScreen(), true
	case "crashes":
		return p.crashScreen(), true
	case "alerts":
		return p.alertsScreen(), true
	}
	return ui.Screen{}, false
}

func (p *app) set(key string, v float64) {
	p.a.Go(ui.CapSettingsSet, ui.SettingArgs{Key: key, Value: v}, &p.s, func(err error) {
		if err != nil && !ui.Cancelled(err) {
			p.a.Toast(err.Error())
		}
		p.refresh()
	})
}

func clamp(v float64) float64 { return max(0, min(1, float64(int(v*10+0.5))/10)) }

func main() {
	a, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	p := &app{a: a}
	a.OnStart = func(string) {
		var saved savedPages
		restored := a.Restore(&saved)
		p.load(func(s ui.Screen) {
			a.Reset(s)
			if restored {
				p.restorePages(saved)
			}
		}, p.root)
	}
	a.OnSave = func() any { return savedPages{Pages: p.pages} }
	a.OnNotif = func(id, _ string) {
		if id == ui.NotifUnusedPerms {
			p.load(a.Push, p.permsScreen)
		}
	}
	a.OnLifecycle = func(state string) {
		if state == ui.StateForeground {
			p.load(func(ui.Screen) { p.refresh() }, p.root)
		}
	}
	a.OnPopped = func(id string) {
		p.popped(id)
		if id == "wifi" || id == "bt" {
			p.radioPage = ""
		}
	}
	go func() {
		for range time.Tick(2 * time.Second) {
			a.Post(func() {
				if p.radioPage != "" {
					p.loadRadios(p.refresh)
				}
			})
		}
	}()
	a.OnAction = func(e sdk.ActionEvent) {
		if p.radioAction(e) || p.localeAction(e) || p.batteryAction(e) || p.securityAction(e) || p.nfcAction(e) || p.usbAction(e) || p.alertsAction(e) {
			return
		}
		switch {
		case e.Action == "probe" || (e.Screen == "root" && e.Node == "probe"):
			a.Push(probe(a))
		case e.Screen == "root" && (e.Node == ui.SettingSilent || e.Node == ui.SettingIMEFuzzy || e.Node == ui.SettingLargeText):
			on := map[string]bool{ui.SettingSilent: p.s.Settings.Silent, ui.SettingIMEFuzzy: p.s.Settings.IMEFuzzy,
				ui.SettingLargeText: p.s.Settings.LargeText}[e.Node]
			p.set(e.Node, map[bool]float64{true: 0, false: 1}[on])
		case e.Screen == "root" && e.Node == "unlock":
			// The system asks for confirmation itself.
			p.set(ui.SettingDeviceUnlocked, map[bool]float64{true: 0, false: 1}[p.s.Unlocked])
		case e.Screen == "root" && e.Node == "simlock" && p.s.Sim != nil:
			if e.Action == "change" {
				p.set(ui.SettingSimPinChange, 0)
			} else {
				p.set(ui.SettingSimPinLock, map[bool]float64{true: 0, false: 1}[p.s.Sim.PinLock])
			}
		case e.Screen == "root" && e.Node == "devmode":
			p.set(ui.SettingDeveloperMode, map[bool]float64{true: 0, false: 1}[p.s.Settings.DeveloperMode])
		case e.Screen == "root":
			switch e.Node {
			case "perms":
				// Grants change while Settings sits in the background: show
				// the current ones.
				a.Go(ui.CapPermList, nil, &p.perms, func(err error) {
					if err != nil {
						a.Toast(err.Error())
					}
					p.push(p.permsScreen())
				})
			case "traffic":
				p.push(p.trafficScreen())
			case "apps":
				a.Go(ui.CapSettingsApps, nil, &p.apps, func(error) { a.Push(p.appsScreen()) })
			case "sound":
				p.push(p.soundScreen())
			case ui.SettingTheme:
				p.push(p.themeScreen())
			case "wallpaper":
				p.push(p.wallpaperScreen())
			case "lock":
				p.push(p.lockScreen())
			case "about":
				p.push(p.aboutScreen())
			case "update":
				a.Go(ui.CapSystemUpdate, ui.UpdateArgs{Action: ui.UpdateStatus}, &p.upd, func(err error) {
					if err != nil {
						a.Toast(err.Error())
					}
					a.Push(p.updateScreen())
				})
			}
		case e.Screen == "update" && (e.Action == "check" || e.Action == "install" || e.Action == "reboot"):
			p.updateAction(e.Action)
		case e.Screen == "about" && e.Node == "crashes":
			p.push(p.crashScreen())
		case e.Screen == "appsettings" && strings.HasPrefix(e.Node, "a:"):
			a.Push(p.appScreen(strings.TrimPrefix(e.Node, "a:")))
		case strings.HasPrefix(e.Screen, "aps:") && strings.HasPrefix(e.Node, "k:"):
			p.changeAppSetting(strings.TrimPrefix(e.Screen, "aps:"), strings.TrimPrefix(e.Node, "k:"), e.Action)
		case strings.HasPrefix(e.Screen, "apt:") && e.Action == "save":
			id, key, _ := strings.Cut(strings.TrimPrefix(e.Screen, "apt:"), ":")
			a.Pop()
			p.setAppSetting(id, key, e.Fields["v"])
		case e.Screen == "crashes" && e.Action == ui.SettingCrashSend:
			// The system asks before anything leaves the device.
			var i int
			if _, err := fmt.Sscanf(e.Node, "c%d", &i); err == nil {
				p.set(ui.SettingCrashSend, float64(i))
			}
		case e.Action == "refresh":
			p.load(func(ui.Screen) { p.refresh() }, p.root)
		case e.Screen == "sound":
			st := p.s.Settings
			cur := map[string]float64{ui.SettingRingVolume: st.RingVolume, ui.SettingMediaVolume: st.MediaVolume, ui.SettingNotifyVolume: st.NotifyVolume}[e.Node]
			step := 0.1
			if e.Action == "down" {
				step = -0.1
			}
			// Kept at once: the next press, before the system's answer, steps
			// on from this one.
			v := clamp(cur + step)
			switch e.Node {
			case ui.SettingRingVolume:
				p.s.Settings.RingVolume = v
			case ui.SettingMediaVolume:
				p.s.Settings.MediaVolume = v
			case ui.SettingNotifyVolume:
				p.s.Settings.NotifyVolume = v
			}
			p.set(e.Node, v)
		case e.Screen == ui.SettingTheme && e.Action == "choose":
			var i int
			fmt.Sscanf(e.Node, "theme:%d", &i)
			p.set(ui.SettingTheme, float64(i))
		case e.Screen == "wallpaper" && e.Action == "clear":
			var i int
			fmt.Sscanf(e.Node, "wp:%d", &i)
			p.set(ui.SettingWallpaperClear, float64(i))
		case e.Screen == "lock" && e.Action == "choose" && strings.HasPrefix(e.Node, "screen:"):
			var v int
			fmt.Sscanf(e.Node, "screen:%d", &v)
			p.set(ui.SettingScreenOff, float64(v))
		case e.Screen == "lock" && e.Action == "choose":
			var v int
			fmt.Sscanf(e.Node, "lock:%d", &v)
			p.set(ui.SettingAutoLock, float64(v))
		case e.Screen == "perms":
			for _, pe := range p.perms {
				if pe.App+"/"+pe.Perm == e.Node {
					a.Go(ui.CapPermSet, ui.PermSetArgs{App: pe.App, Perm: pe.Perm, State: e.Action}, nil, func(err error) {
						if err != nil {
							a.Toast(err.Error())
						}
						p.load(func(ui.Screen) { p.refresh() }, p.root)
					})
				}
			}
		}
	}
	if err := a.Run(); err != nil {
		log.Print(err)
	}
}
