// Settings is a privileged system app: perm.* and settings.* capabilities
// require the "system" manifest permission. It reviews app permissions and
// edits the system settings the shell keeps (volumes, auto keypad lock).
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"slices"
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
		HidePercent   bool    `json:"hide_battery_percent"`
		NetSpeed      bool    `json:"net_speed"`
		Brightness    int     `json:"brightness"`
		AutoBright    bool    `json:"auto_brightness"`
		Motion        string  `json:"motion"`
		WallpaperIdle string  `json:"wallpaper_idle"`
		WallpaperLock string  `json:"wallpaper_lock"`
		// "" leaves them to the system (language.go).
		Language   string `json:"language"`
		Region     string `json:"region"`
		Zone       string `json:"time_zone"`
		ManualTime bool   `json:"manual_time"`
		Clock12    bool   `json:"clock_12h"`
		// AutoSystemUpdate: system updates install by themselves.
		AutoSystemUpdate bool `json:"auto_system_update"`
		// A11y: Settings › Accessibility (a11y.go).
		A11y a11yInfo `json:"accessibility"`
	} `json:"settings"`
	// Sound: the sound mode in force (ui.SoundRing, ...) and whether the
	// phone's sound switch sets it (then it is shown, not chosen).
	Sound struct {
		Mode   string `json:"mode"`
		Switch bool   `json:"switch"`
	} `json:"sound"`
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
	Backlight struct {
		Brightness int  `json:"brightness"`
		Simulated  bool `json:"simulated"`
		// Level is the backlight now (the automatic one's), Lux the light
		// sensor's reading; SensorSimulated: the machine has no sensor.
		Level           int     `json:"level"`
		Lux             float64 `json:"lux"`
		SensorSimulated bool    `json:"sensor_simulated"`
	} `json:"backlight"`
	Data    ui.TrafficUsage  `json:"data_usage"`
	Energy  []ui.EnergyEntry `json:"energy"`
	Battery batteryInfo      `json:"battery"`
	// IMEI is the baseband's, "" without one.
	IMEI string `json:"imei"`
	// PowerEndsCall: the power key hangs up a call; absent where the
	// board has no power key of its own (the end key hangs up).
	PowerEndsCall *bool `json:"power_ends_call"`
	// Sim is absent without a baseband.
	Sim *struct {
		State   string `json:"state"`
		PinLock bool   `json:"pin_lock"`
	} `json:"sim"`
	localeInfo
	securityInfo
	usbInfo
	alertsInfo
	rolesInfo
	blockInfo
	wellbeingInfo
	resetInfo
	displayInfo
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
	sets    []ui.SettingArgs // the changes not answered yet (set)
	visible string
	// started: the start's first page is up; afterStart is what waits for
	// it (whenStarted).
	started    bool
	afterStart []func()
	// apps are the settings other apps declare (App settings).
	apps []ui.AppSettings
	upd  update
	// The radios (radios.go); radioPage: the Wi-Fi or Bluetooth page is open
	// (they refresh while it is).
	wifi      ui.WifiState
	bt        ui.BTState
	radio     ui.RadioState
	radioPage string
	// radioGen counts the switches pressed, radioBusy those the system has
	// not answered: a refresh asked for before the latest press or while
	// one is unanswered is not shown, nor one older than the refresh shown
	// (radioSeq numbers them; their answers come in any order).
	radioGen, radioBusy  int
	radioSeq, radioShown int
	eapMethod            int    // the enterprise network page's method (eapMethods)
	pair                 btPair // Bluetooth › Pair new device (radios.go)
	// pages: the IDs of the pages open above the root that push opened
	// (handed over by OnSave).
	pages []string
	// updatePage: System update is open (it refreshes while installing);
	// updGen counts its actions, updActing is the one waiting for its
	// answer and updPolling a refresh that is (pollUpdate).
	updatePage bool
	updGen     int
	updActing  string
	updPolling bool
	// keepLock: the reset page's choice to keep the anti-theft lock.
	keepLock bool
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
	// Progress of the install: RAUC's percent, an incremental bundle's
	// operations; Retry: the download stopped and is tried again.
	Progress *struct {
		Percent int `json:"percent"`
		Done    int `json:"done"`
		Total   int `json:"total"`
		Resumed int `json:"resumed"`
	} `json:"progress"`
	Retry *struct {
		Attempt int    `json:"attempt"`
		Of      int    `json:"of"`
		At      string `json:"at"`
	} `json:"retry"`
	Tonight bool `json:"tonight"`
	// Paused: why an automatic install that stopped part way waits.
	Paused    string `json:"paused"`
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
	// A page action, not a row: the offer stays the page's only focusable
	// row, so Install is what OK and the palette reach.
	s.Actions = append(s.Actions, ui.Action{ID: ui.SettingAutoSystemUpdate, Label: tr.T("Switch automatic updates"), Key: "a"})
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "auto", Title: tr.T("Automatic updates"),
		Subtitle: tr.T("Downloads and installs on Wi-Fi, charging or with the battery at least half full; then asks to restart"),
		Trailing: onOff(p.s.Settings.AutoSystemUpdate)})
	switch u.State {
	case "installing":
		s.Body = append(s.Body, sdk.Styled(ui.StyleAccent, tr.T("Installing… You can keep using the phone meanwhile.")))
		if line := installProgress(u); line != "" {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, line))
		}
	case "installed":
		s.Body = append(s.Body, sdk.Styled(ui.StyleOK, tr.T("Installed; it takes effect after a restart.")))
		if u.Tonight {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("It restarts tonight while the phone is not in use.")))
		}
		s.Actions = append(s.Actions, ui.Action{ID: "reboot", Label: tr.T("Restart"), Key: "r", Confirm: tr.T("Restart into the new system now?")})
	case "failed":
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, tr.T("Not updated: %s", u.Error)))
	}
	if u.Paused != "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, u.Paused))
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

// installProgress is how far the install is: the download being tried
// again, an incremental bundle's operations (and where it resumed), or
// RAUC's percent.
func installProgress(u update) string {
	switch p, r := u.Progress, u.Retry; {
	case r != nil:
		return tr.T("The download stopped; trying again at %s (%d of %d)", r.At, r.Attempt, r.Of)
	case p == nil:
		return ""
	case p.Total > 0 && p.Resumed > 0:
		return tr.T("Operation %d of %d (resumed at %d)", p.Done, p.Total, p.Resumed)
	case p.Total > 0:
		return tr.T("Operation %d of %d", p.Done, p.Total)
	case p.Percent > 0:
		return tr.T("%d%% done", p.Percent)
	}
	return ""
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

// pollUpdate refreshes System update while it installs. The install's
// own answer comes when it ends, and calls run side by side: an answer
// to a refresh sent before the latest action, or one saying the install
// has not begun while its call waits (the system answered the refresh
// first), would show the state from before it and stop the refreshing.
func (p *app) pollUpdate() {
	if !p.updatePage || p.upd.State != "installing" || p.updPolling {
		return
	}
	p.updPolling = true
	gen := p.updGen
	var u update
	p.a.Go(ui.CapSystemUpdate, ui.UpdateArgs{Action: ui.UpdateStatus}, &u, func(err error) {
		p.updPolling = false
		if err != nil || gen != p.updGen || (p.updActing == ui.UpdateInstall && u.State != "installing") {
			return
		}
		p.upd = u
		p.a.Update(p.updateScreen())
	})
}

func (p *app) updateAction(action string) {
	if action == "install" {
		p.upd.State = "installing"
		p.a.Update(p.updateScreen())
	}
	p.updGen++
	p.updActing = action
	var u update
	p.a.Go(ui.CapSystemUpdate, ui.UpdateArgs{Action: action}, &u, func(err error) {
		p.updActing = ""
		if err != nil {
			p.a.Toast(err.Error())
		} else {
			p.upd = u
		}
		p.a.Update(p.updateScreen())
	})
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
		case ui.PermLocation, ui.PermLocationBackground:
			sub, actions = locationPermRow(e, sub, actions)
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: e.App + "/" + e.Perm, Title: e.Name + " · " + e.Label,
			Trailing: tr.T(stateLabel[e.State]), Subtitle: sub, Actions: actions})
	}
	if len(p.perms) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No app has asked for runtime permissions")))
	}
	return s
}

func (p *app) soundScreen() ui.Screen {
	st := p.s.Settings
	s := ui.Screen{ID: "sound", Title: tr.T("Sound and vibration")}
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
	if p.s.Sound.Switch {
		return p.soundSwitchRows(s)
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: ui.SettingSilent, Title: tr.T("Silent mode"), Subtitle: tr.T("Calls and notifications only vibrate; alarms still ring"),
		Trailing: onOff(st.Silent), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}})
	return s
}

// soundSwitchRows: on a phone with a sound switch the mode is where the
// switch is, shown here with what it does; no choice to make.
func (p *app) soundSwitchRows(s ui.Screen) ui.Screen {
	name, sub := tr.T("Ring"), tr.T("Calls and notifications ring")
	switch p.s.Sound.Mode {
	case ui.SoundVibrate:
		name, sub = tr.T("Vibrate"), tr.T("Calls and notifications only vibrate; alarms still ring")
	case ui.SoundSilent:
		name, sub = tr.T("Silent"), tr.T("Calls and notifications neither ring nor vibrate; alarms still ring")
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: ui.SettingSilent, Title: tr.T("Sound mode"), Subtitle: sub, Trailing: name},
		sdk.Styled(ui.StyleDim, tr.T("Set by the sound switch on the side of the phone")))
	return s
}

// soundChanged takes the sound mode the system says the switch moved to
// (an EventSettings of key "sound").
func (p *app) soundChanged(value string) {
	var ev struct{ Key, Value string }
	if json.Unmarshal([]byte(value), &ev) != nil || ev.Key != "sound" {
		return
	}
	p.s.Sound.Mode = ev.Value
	p.a.Update(p.soundScreen())
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
				p.loadRadios(func() { p.loadVPN(func() { p.loadAccounts(p.refresh) }) })
			})
		})
	})
}

// startShown puts up the start's first page (the root, the pages open
// before a restart, or the page the start asked for), then what waited for it.
func (p *app) startShown(root ui.Screen, arg string, restored bool, saved savedPages) {
	if arg != "" {
		p.show(arg)
	} else {
		p.a.Reset(root)
		if restored {
			p.restorePages(saved)
		}
	}
	p.started = true
	for _, f := range p.afterStart {
		f()
	}
	p.afterStart = nil
}

// whenStarted runs f once the start's first page is up. A notification
// that opened the app arrives during its start: the start's reset, answered
// after the notification's pages, would take them away.
func (p *app) whenStarted(f func()) {
	if p.started {
		f()
		return
	}
	p.afterStart = append(p.afterStart, f)
}

// openNotif shows the page of a notification of Settings' that was opened.
func (p *app) openNotif(id string) {
	if id == ui.NotifUnusedPerms {
		p.load(p.a.Push, p.permsScreen)
	}
	if id == ui.NotifBgLocation || strings.HasPrefix(id, ui.NotifBgLocationUse+":") {
		p.openLocation()
	}
	if id == ui.NotifDataUsage {
		p.load(p.push, p.trafficScreen)
	}
	if app, ok := strings.CutPrefix(id, ui.NotifAppDataLimit+":"); ok {
		p.load(func(s ui.Screen) { p.push(p.trafficScreen()); p.push(s) }, func() ui.Screen { return p.appDataScreen(app) })
	}
	if id == ui.NotifHotspotLimit {
		p.loadHotspot(func() { p.push(p.hotspotScreen()); p.pollHotspot() })
	}
	if id == ui.NotifDataRoaming {
		p.load(p.push, p.mobilePage)
	}
}

func (p *app) refresh() {
	for _, s := range append(p.openGroups(), p.root(), p.permsScreen(), p.trafficScreen(), p.batteryScreen(), p.soundScreen(), p.themeScreen(), p.wallpaperScreen(),
		p.lockScreen(), p.aboutScreen(), p.crashScreen(), p.wifiScreen(), p.btScreen(), p.btPairScreen(), p.updateScreen(), p.resetScreen()) {
		p.a.Update(s)
	}
	p.refreshLocale()
	p.refreshWellbeing()
	p.refreshData()
}

// screenByID builds a page of the app's own (not the locale pages) by its
// ID, for one restored after a restart.
func (p *app) screenByID(id string) (ui.Screen, bool) {
	switch id {
	case "perms":
		return p.permsScreen(), true
	case "traffic":
		return p.trafficScreen(), true
	case "dataplan":
		return p.planScreen(), true
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
	case "defaults":
		return p.defaultsScreen(), true
	case "blocking":
		return p.blockingScreen(), true
	case "location":
		return p.locationScreen(), true
	case "hotspot":
		return p.restoreHotspot(), true
	case "hotspotdevices":
		return p.hotspotDevicesScreen(), true
	case "switch":
		return p.restoreSwitch(), true
	case "reset":
		return p.resetScreen(), true
	}
	if s, ok := p.vpnScreenByID(id); ok {
		return s, true
	}
	if s, ok := p.apnScreenByID(id); ok {
		return s, true
	}
	if s, ok := p.accountsScreenByID(id); ok {
		return s, true
	}
	if s, ok := p.wellbeingScreenByID(id); ok {
		return s, true
	}
	if s, ok := p.dataScreenByID(id); ok {
		return s, true
	}
	return p.groupScreen(id)
}

// set sends one change at a time, as it is pressed: calls run side by
// side, so the system could take two quick presses in the wrong order,
// and the answer to the older one would undo the newer one kept at once.
// Only the last answer is shown.
func (p *app) set(key string, v float64) {
	p.sets = append(p.sets, ui.SettingArgs{Key: key, Value: v})
	if len(p.sets) == 1 {
		p.sendSet()
	}
}

func (p *app) sendSet() {
	var st settings
	p.a.Go(ui.CapSettingsSet, p.sets[0], &st, func(err error) {
		if err != nil && !ui.Cancelled(err) {
			p.a.Toast(err.Error())
		}
		if p.sets = p.sets[1:]; len(p.sets) > 0 {
			p.sendSet()
			return
		}
		if err == nil {
			p.s = st
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
	a.OnStart = func(arg string) {
		var saved savedPages
		restored := a.Restore(&saved)
		p.load(func(s ui.Screen) { p.startShown(s, arg, restored, saved) }, p.root)
	}
	a.OnEvent = func(name, value string) {
		// At once, so the keys the system holds behind the link land on
		// its page. No reload: coming to the front reloads already, and a
		// reload's answer arriving after a change the user just made would
		// show the value from before it.
		if name == ui.EventOpen {
			p.show(value)
		}
		if name == ui.EventFocus {
			p.btFocus(value == ui.FocusLost)
		}
		if name == ui.EventSettings {
			p.soundChanged(value)
		}
	}
	a.OnSave = func() any { return savedPages{Pages: p.pages} }
	a.OnNotif = func(id, _ string) {
		p.whenStarted(func() { p.openNotif(id) })
	}
	a.OnLifecycle = func(state string) {
		if state == ui.StateForeground {
			p.load(func(ui.Screen) { p.refresh() }, p.root)
		}
		p.btFront(state == ui.StateForeground)
	}
	a.OnPopped = p.onPopped
	go func() {
		for range time.Tick(2 * time.Second) {
			a.Post(func() {
				if p.radioPage != "" {
					p.loadRadios(p.refresh)
				}
			})
		}
	}()
	go func() {
		for range time.Tick(time.Second) {
			a.Post(p.pollUpdate)
		}
	}()
	a.OnAction = p.action
	if err := a.Run(); err != nil {
		log.Print(err)
	}
}

func (p *app) onPopped(id string) {
	p.popped(id)
	if id == "wifi" || id == "bt" {
		p.radioPage = ""
	}
	if id == "update" {
		p.updatePage = false
	}
	p.btPopped(id)
}

// action handles a press on one of the app's pages.
func (p *app) action(e sdk.ActionEvent) {
	a := p.a
	if p.radioAction(e) || p.localeAction(e) || p.batteryAction(e) || p.securityAction(e) || p.nfcAction(e) || p.usbAction(e) || p.alertsAction(e) || p.locationAction(e) || p.hotspotAction(e) || p.switchAction(e) || p.vpnAction(e) || p.apnAction(e) || p.accountsAction(e) ||
		p.defaultsAction(e) || p.blockingAction(e) || p.wellbeingAction(e) || p.dataAction(e) || p.resetAction(e) || p.refreshAction(e) || p.a11yAction(e) {
		return
	}
	st := p.s.Settings
	switch {
	case onMenu(e.Screen) && strings.HasPrefix(e.Node, groupPrefix) && e.Action == ui.ActionOpen:
		if s, ok := p.groupScreen(e.Node); ok {
			p.push(s)
		}
	case e.Action == "probe" || (onMenu(e.Screen) && e.Node == "probe"):
		a.Push(probe(a))
	case e.Node == ui.SettingSilent && p.s.Sound.Switch:
		// The switch sets it: nothing to choose.
	case (onMenu(e.Screen) || e.Screen == "sound") && (e.Node == ui.SettingSilent || e.Node == ui.SettingIMEFuzzy ||
		e.Node == ui.SettingLargeText || e.Node == ui.SettingNetSpeed || e.Node == ui.SettingBatteryPercent ||
		e.Node == ui.SettingAutoBrightness):
		s := &p.s.Settings
		flag := map[string]*bool{ui.SettingSilent: &s.Silent, ui.SettingIMEFuzzy: &s.IMEFuzzy, ui.SettingLargeText: &s.LargeText,
			ui.SettingNetSpeed: &s.NetSpeed, ui.SettingBatteryPercent: &s.HidePercent, ui.SettingAutoBrightness: &s.AutoBright}[e.Node]
		// Kept at once, as the volumes are: a second press before the
		// system's answer switches back.
		*flag = !*flag
		on := *flag
		if e.Node == ui.SettingBatteryPercent {
			// The setting is the percentage shown; the field hides it.
			on = !on
		}
		p.set(e.Node, map[bool]float64{true: 1, false: 0}[on])
	case onMenu(e.Screen) && e.Node == "unlock":
		// The system asks for confirmation itself.
		p.set(ui.SettingDeviceUnlocked, map[bool]float64{true: 0, false: 1}[p.s.Unlocked])
	case onMenu(e.Screen) && e.Node == "simlock" && p.s.Sim != nil:
		if e.Action == "change" {
			p.set(ui.SettingSimPinChange, 0)
		} else {
			p.set(ui.SettingSimPinLock, map[bool]float64{true: 0, false: 1}[p.s.Sim.PinLock])
		}
	case onMenu(e.Screen) && e.Node == "devmode":
		p.set(ui.SettingDeveloperMode, map[bool]float64{true: 0, false: 1}[p.s.Settings.DeveloperMode])
	case onMenu(e.Screen) && e.Node == ui.SettingMotion && (e.Action == "next" || e.Action == "prev"):
		i := slices.Index(ui.MotionLevels, p.s.Settings.Motion)
		if e.Action == "next" {
			i = (i + 1) % len(ui.MotionLevels)
		} else {
			i = (i + len(ui.MotionLevels) - 1) % len(ui.MotionLevels)
		}
		// Kept at once, as the volumes are.
		p.s.Settings.Motion = ui.MotionLevels[i]
		p.set(ui.SettingMotion, float64(i))
	case onMenu(e.Screen) && e.Node == ui.SettingBrightness && (e.Action == "up" || e.Action == "down"):
		step := 10
		if e.Action == "down" {
			step = -10
		}
		// Kept at once, as the volumes are.
		v := min(100, max(ui.MinBrightness, p.brightness()+step))
		p.s.Settings.Brightness, p.s.Backlight.Brightness = v, v
		p.set(ui.SettingBrightness, float64(v))
	case onMenu(e.Screen):
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
		case "crashes":
			p.push(p.crashScreen())
		case "update":
			a.Go(ui.CapSystemUpdate, ui.UpdateArgs{Action: ui.UpdateStatus}, &p.upd, func(err error) {
				if err != nil {
					a.Toast(err.Error())
				}
				p.updatePage = true
				a.Push(p.updateScreen())
			})
		}
	case e.Screen == "update" && (e.Action == "check" || e.Action == "install" || e.Action == "reboot"):
		p.updateAction(e.Action)
	case e.Screen == "update" && e.Action == ui.SettingAutoSystemUpdate:
		p.set(ui.SettingAutoSystemUpdate, map[bool]float64{true: 0, false: 1}[p.s.Settings.AutoSystemUpdate])
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
