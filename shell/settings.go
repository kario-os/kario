package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// System settings, kept by the shell and edited by the Settings app through
// settings.get / settings.set (system apps only).

type sysSettings struct {
	AutoLock int `json:"auto_lock"` // seconds idle before the keypad locks, 0 = off
	// ScreenOff: seconds without a key before the screen goes off (0: the
	// default, screen.go).
	ScreenOff    int     `json:"screen_off,omitempty"`
	RingVolume   float64 `json:"ring_volume"`
	MediaVolume  float64 `json:"media_volume"`
	NotifyVolume float64 `json:"notify_volume"`
	// DeveloperMode trusts the development store (StateDir/trust): packages
	// signed by it install and run only while it is on.
	DeveloperMode bool `json:"developer_mode"`
	// Silent: ringtones and notification sounds vibrate instead.
	Silent bool `json:"silent"`
	// IMEFuzzy: fuzzy pinyin (z/zh, c/ch, s/sh, n/l, an/ang, en/eng, in/ing).
	IMEFuzzy bool `json:"ime_fuzzy"`
	// Theme names the colours both renderers use ("" is dark).
	Theme string `json:"theme"`
	// LargeText: the pixel renderer draws everything a step bigger
	// (largeTextZoom); the text renderer keeps its cells.
	LargeText bool `json:"large_text"`
	// The status bar (frame.go): HidePercent leaves the battery's icon
	// alone; NetSpeed shows how fast data goes right now (netspeed.go).
	HidePercent bool `json:"hide_battery_percent"`
	NetSpeed    bool `json:"net_speed"`
	// Brightness: the backlight in percent, 0 for the default (screen.go).
	Brightness int `json:"brightness,omitempty"`
	// AutoBrightness: the backlight follows the light sensor
	// (autobright.go).
	AutoBrightness bool `json:"auto_brightness"`
	// Motion: Settings › Animations, "" standard, "reduced", "off" (motion.go).
	Motion string `json:"motion"`
	// RefreshRate: Settings › Refresh rate, "" automatic, ui.RefreshStandard
	// or ui.RefreshHigh (display.go).
	RefreshRate string `json:"refresh_rate,omitempty"`
	// The standby pictures (wallpaper.go): unlocked and with the keypad
	// locked; "" is none. Each is a copy, kept apart from the photos.
	WallpaperIdle string `json:"wallpaper_idle,omitempty"`
	WallpaperLock string `json:"wallpaper_lock,omitempty"`
	// Language (a tag), Region (ISO 3166) and Zone (IANA): "" leaves them to
	// the system (language.go, region.go, clock.go). ManualTime: the user
	// sets the clock instead of network time; Clock12: the 12-hour clock.
	Language   string `json:"language,omitempty"`
	Region     string `json:"region,omitempty"`
	Zone       string `json:"time_zone,omitempty"`
	ManualTime bool   `json:"manual_time,omitempty"`
	Clock12    bool   `json:"clock_12h,omitempty"`
	// SimRegion and NetZone: the last SIM's country and the last zone the
	// network gave, for when telephonyd has not said (boot) or no SIM is in.
	SimRegion string `json:"sim_region,omitempty"`
	NetZone   string `json:"net_zone,omitempty"`
	// Airplane mode (airplane.go): the user's Wi-Fi and Bluetooth switches
	// it restores when it ends, and the radios the user turned on in
	// airplane mode, which it then leaves on (Android and iOS remember
	// that). MobileDataOff: the mobile data switch; DataRoaming: the data
	// roaming switch (off by default).
	Airplane      bool `json:"airplane,omitempty"`
	AirplaneWifi  bool `json:"airplane_wifi,omitempty"`
	AirplaneBT    bool `json:"airplane_bt,omitempty"`
	KeepWifi      bool `json:"airplane_keep_wifi,omitempty"`
	KeepBT        bool `json:"airplane_keep_bt,omitempty"`
	MobileDataOff bool `json:"mobile_data_off,omitempty"`
	DataRoaming   bool `json:"data_roaming,omitempty"`
	// BTPhonebook: paired devices (by address) allowed or refused the
	// contacts and the call log (btobex.go).
	BTPhonebook map[string]bool `json:"bt_phonebook,omitempty"`
	// WifiMetered: the user's metered choice for Wi-Fi networks by name
	// (ui.MeteredYes, ui.MeteredNo); the others are automatic (metered.go).
	WifiMetered map[string]string `json:"wifi_metered,omitempty"`
	// Alerts: emergency alert levels the user switched on or off
	// (messaging.go); the others keep their default.
	Alerts map[string]bool `json:"alerts,omitempty"`
	// SMSApp: the app the user made the default SMS app ("" the system's
	// Messages, smsrole.go).
	SMSApp string `json:"sms_app,omitempty"`
	// USBMode: what a trusted computer gets (usbmtp.go): ui.USBModeMTP,
	// or "" for network sharing.
	USBMode string `json:"usb_mode,omitempty"`
	// AutoSystemUpdate: system updates download and install by themselves
	// (updateauto.go); off unless the product's profile turns it on.
	AutoSystemUpdate bool `json:"auto_system_update,omitempty"`
	// LocationOff: the user turned location off for every app;
	// LocationApprox: the apps (permission keys) given only their
	// approximate location (location.go); LocationNetworkOff: the network
	// location service is never asked (location_net.go).
	LocationOff        bool            `json:"location_off,omitempty"`
	LocationApprox     map[string]bool `json:"location_approximate,omitempty"`
	LocationNetworkOff bool            `json:"location_network_off,omitempty"`
	// The personal hotspot (hotspot.go): its switch, network name and WPA2
	// passphrase (made up the first time; shown in Settings, never logged),
	// and HotspotWifiOff: Wi-Fi was off when it came on, and stays off
	// when it goes off.
	Hotspot        bool   `json:"hotspot,omitempty"`
	HotspotSSID    string `json:"hotspot_ssid,omitempty"`
	HotspotPass    string `json:"hotspot_passphrase,omitempty"`
	HotspotWifiOff bool   `json:"hotspot_wifi_off,omitempty"`
	// HotspotSecurity ("" WPA2), HotspotHidden, HotspotBand ("" 2.4 GHz)
	// and HotspotMax (devices at once, 0 no limit): ui.HotspotArgs's.
	HotspotSecurity string `json:"hotspot_security,omitempty"`
	HotspotHidden   bool   `json:"hotspot_hidden,omitempty"`
	HotspotBand     string `json:"hotspot_band,omitempty"`
	HotspotMax      int    `json:"hotspot_max_clients,omitempty"`
	// HotspotDayLimit: what the hotspot's devices may use in a day, in
	// bytes, 0 no limit (hotspot_usage.go).
	HotspotDayLimit uint64 `json:"hotspot_daily_limit,omitempty"`
	// The mobile data plan (datausage.go): the monthly allowance in bytes
	// (0 none) and the day its cycles start (0 the 1st); the warning, an
	// amount of its own or else DataWarnPercent of the allowance (0:
	// dataWarnPercent), or none (DataWarnOff); DataLimitOff: the allowance
	// used up holds mobile data off.
	DataLimit       uint64 `json:"data_limit,omitempty"`
	DataCycleDay    int    `json:"data_cycle_day,omitempty"`
	DataWarn        uint64 `json:"data_warning,omitempty"`
	DataWarnPercent int    `json:"data_warning_percent,omitempty"`
	DataWarnOff     bool   `json:"data_warning_off,omitempty"`
	DataLimitOff    bool   `json:"data_limit_off,omitempty"`
	// The apps' own (netpolicy.go): background data off, Data Saver and
	// the apps exempt from it, and each app's mobile data a cycle, in
	// bytes.
	BgDataOff      map[string]bool   `json:"background_data_off,omitempty"`
	DataSaver      bool              `json:"data_saver,omitempty"`
	DataSaverAllow map[string]bool   `json:"data_saver_allow,omitempty"`
	AppDataLimit   map[string]uint64 `json:"app_data_limit,omitempty"`
	// A11y: Settings › Accessibility (a11y.go).
	A11y a11ySettings `json:"accessibility"`
}

// themes are Settings › Theme's choices, in order (settings.set "theme" takes
// the index); Name is translated where it is shown.
var themes = []struct{ ID, Name string }{{"", "Dark"}, {"light", "Light"}, {"retro", "Retro green"}, {"amber", "Amber"}}

var autoLockChoices = []int{0, 15, 30, 60}

func defaultSettings() sysSettings {
	return sysSettings{RingVolume: 0.9, MediaVolume: 0.7, NotifyVolume: 0.6, A11y: defaultA11y()}
}

func (c *Core) settingsPath() string { return filepath.Join(c.cfg.StateDir, "settings.json") }

func (c *Core) loadSettings() {
	c.settings = defaultSettings()
	if c.profile != nil && len(c.profile.Settings) > 0 {
		if err := json.Unmarshal(c.profile.Settings, &c.settings); err != nil {
			c.log.Printf("profile settings: %v", err)
		}
	}
	if raw, err := os.ReadFile(c.settingsPath()); err == nil {
		_ = json.Unmarshal(raw, &c.settings)
	}
	textBold = c.settings.A11y.BoldText
	applyTheme(c.settings.Theme, c.settings.A11y.HighContrast)
}

func (c *Core) saveSettings() {
	raw, _ := json.MarshalIndent(c.settings, "", "  ")
	_ = os.WriteFile(c.settingsPath(), raw, 0o600)
}

// applyVolumes hands the volumes to audiod (when it comes up, and on change).
func (c *Core) applyVolumes() {
	s := c.settings
	go func() {
		for cat, v := range map[string]float64{audio.Ring: s.RingVolume, audio.Alarm: s.RingVolume, audio.Media: s.MediaVolume, audio.Notify: s.NotifyVolume} {
			_ = c.audioRPC("volume", map[string]any{"category": cat, "level": v}, nil)
		}
	}()
	if a := s.A11y; a.Mono || a.Balance != 0 {
		c.applyChannels()
	}
}

// about is what Settings → About phone shows.
func (c *Core) about() map[string]any {
	kernel := kernelRelease()
	dev := c.screenDev
	screen := tr.T("Text %d×%d characters", c.cfg.W, c.cfg.H)
	if dev.Width > 0 {
		screen = fmt.Sprintf("%s · %d×%d", tr.T(dev.Name), dev.Width, dev.Height)
	}
	// The hardware keypad, not the input method (scripts type on QWERTY).
	keypad := map[string]string{"t9": tr.T("12-key"), "qwerty": tr.T("QWERTY"), "full": tr.T("Wide QWERTY")}[dev.Keypad]
	if keypad == "" {
		keypad = map[bool]string{true: tr.T("12-key"), false: tr.T("QWERTY")}[c.ime.Keypad() == "t9"]
	}
	out := map[string]any{"system": Brand, "version": shownVersion(), "proto": ui.Proto, "screen": screen,
		"keypad": keypad, "kernel": kernel, "apps": len(c.apps), "uptime": time.Since(c.started).Round(time.Second).String()}
	if c.renderer != nil {
		out["renderer"] = c.renderer.String()
	}
	if c.profile != nil && c.profile.Product != "" {
		out["product"] = c.profile.Product
	}
	return out
}

// SystemVersion is shown in About phone; a release sets it when it links
// (-X github.com/kario-os/kario/shell.SystemVersion=<version>, PHONE_VERSION in
// the phone-fw recipe). A development build's is not a release version
// (releaseVersion) and is shown translated.
var SystemVersion = "0.6 (prototype)"

// shownVersion is SystemVersion as the user reads it.
func shownVersion() string { return tr.T(SystemVersion) }

func (c *Core) settingsGet() map[string]any {
	recent := c.Crashes()[max(0, len(c.Crashes())-10):]
	names, cur := make([]string, len(themes)), 0
	for i, t := range themes {
		names[i] = tr.T(t.Name)
		if t.ID == c.settings.Theme {
			cur = i
		}
	}
	out := map[string]any{"settings": c.settings, "auto_lock_choices": autoLockChoices, "screen_off_choices": screenOffChoices, "about": c.about(), "crashes": recent,
		ui.SettingDeviceUnlocked: c.unlocked(), "themes": names, "theme_index": cur}
	for k, v := range c.localeSettings() {
		out[k] = v
	}
	out["data_usage"] = c.dataUsageView()
	pw := c.power()
	out["energy"], out["battery"] = c.energyView(), map[string]any{"level": pw.Level, "charging": pw.Charging, "simulated": pw.Simulated}
	if c.tel != nil {
		out["sim"] = map[string]any{"state": c.tel.Sim, "pin_lock": c.tel.PinLock}
		// Not in about(): that goes into compatibility reports.
		if c.tel.IMEI != "" {
			out["imei"] = c.tel.IMEI
		}
	}
	out["security"] = c.securityInfo()
	out["reset"] = c.resetView()
	out["alerts"] = c.alertsView()
	out["roles"] = c.rolesView()
	out["blocking"] = c.sms.blocks
	out["screen_time"], out["minor"] = c.screenTimeView(), c.minorView()
	out["usb"] = c.usbView()
	out[ui.SettingLocation] = c.locationView()
	out["display"] = c.displayView()
	out["backlight"] = map[string]any{"brightness": c.brightness(), "simulated": c.screen.blSim, "level": c.level(),
		"lux": math.Round(max(c.screen.lux, 0)), "sensor_simulated": c.screen.alsSim}
	return out
}

func (c *Core) settingsSet(key string, v float64) error {
	vol := func(p *float64) error {
		if v < 0 || v > 1 {
			return errors.New("volume must be 0–1")
		}
		*p = v
		c.applyVolumes()
		return nil
	}
	var err error
	switch key {
	case ui.SettingAutoLock:
		err = errors.New("auto_lock must be one of the choices")
		for _, ch := range autoLockChoices {
			if int(v) == ch {
				c.settings.AutoLock, err = ch, nil
			}
		}
	case ui.SettingScreenOff:
		err = errors.New("screen_off must be one of the choices")
		for _, ch := range screenOffChoices {
			if int(v) == ch {
				c.settings.ScreenOff, err = ch, nil
			}
		}
	case ui.SettingRingVolume:
		err = vol(&c.settings.RingVolume)
	case ui.SettingMediaVolume:
		err = vol(&c.settings.MediaVolume)
	case ui.SettingNotifyVolume:
		err = vol(&c.settings.NotifyVolume)
	case ui.SettingIMEFuzzy:
		c.settings.IMEFuzzy = v != 0
		c.ime.SetFuzzy(c.settings.IMEFuzzy)
	case ui.SettingSilent:
		c.settings.Silent = v != 0
		if c.settings.Silent && c.ringing() {
			c.silenceRing()
		}
	case ui.SettingDeviceUnlocked:
		c.confirmUnlock(v != 0)
	case ui.SettingCrashSend:
		// v indexes the recent records settingsGet lists.
		c.confirmCrashSend(int(v))
	case ui.SettingLargeText:
		c.settings.LargeText = v != 0
	case ui.SettingBatteryPercent:
		c.settings.HidePercent = v == 0
	case ui.SettingNetSpeed:
		c.settings.NetSpeed = v != 0
		c.speed = netSpeed{}
	case ui.SettingBrightness:
		c.settings.Brightness = min(max(int(v), ui.MinBrightness), 100)
		c.resetAutoLevel()
		c.applyBrightness()
	case ui.SettingMotion:
		err = c.setMotion(v)
	case ui.SettingRefreshRate:
		err = c.setRefreshRate(v)
	case ui.SettingAutoBrightness:
		c.settings.AutoBrightness = v != 0
		c.resetAutoLevel()
		c.applyBrightness()
	case ui.SettingTheme:
		if i := int(v); v >= 0 && i < len(themes) && float64(i) == v {
			c.settings.Theme = themes[i].ID
			applyTheme(c.settings.Theme, c.settings.A11y.HighContrast)
		} else {
			err = errors.New("no such theme")
		}
	case ui.SettingWallpaperClear:
		// 1: the standby picture, 2: the lock screen's, 3: both.
		c.clearWallpaper(int(v))
	case ui.SettingDeveloperMode:
		c.settings.DeveloperMode = v != 0
		// Every installed app is checked again against the roots now trusted.
		for _, a := range c.apps {
			a.verified = false
		}
		c.usbDeveloperMode()
		c.log.Printf("developer mode %v", c.settings.DeveloperMode)
	case ui.SettingLanguage, ui.SettingRegion, ui.SettingTimeZone, ui.SettingAutoTime, ui.SettingTime, ui.SettingClock24:
		err = c.setLocale(key, v)
	case ui.SettingAlertExtreme, ui.SettingAlertSevere, ui.SettingAlertAmber, ui.SettingAlertTest:
		err = c.setAlert(key, v)
	case ui.SettingUSBMode:
		err = c.setUSBMode(v)
	case ui.SettingAutoSystemUpdate:
		c.settings.AutoSystemUpdate = v != 0
		c.log.Printf("automatic system updates %v", c.settings.AutoSystemUpdate)
	case ui.SettingDataLimit, ui.SettingDataCycleDay, ui.SettingDataWarning, ui.SettingDataWarningPercent, ui.SettingDataLimitOff:
		err = c.setDataPlan(key, v)
	case ui.SettingHighContrast, ui.SettingBoldText, ui.SettingColorInversion, ui.SettingColorCorrection, ui.SettingMagnification,
		ui.SettingScreenReader, ui.SettingReaderOnDemand, ui.SettingKeyRepeatDelay, ui.SettingKeyRepeatRate, ui.SettingStickyKeys,
		ui.SettingSlowKeys, ui.SettingLongPress, ui.SettingKeyFeedback, ui.SettingMonoAudio, ui.SettingAudioBalance, ui.SettingA11yShortcut:
		err = c.setA11y(key, v)
	default:
		err = fmt.Errorf("unknown setting %q", key)
	}
	if err == nil {
		c.saveSettings()
	}
	return err
}

// autoLock locks the keypad after the idle screen has sat untouched for the
// chosen time (Nokia's automatic keyguard).
func (c *Core) autoLock() {
	s := c.settings.AutoLock
	if s <= 0 || c.keylock || c.fg != nil || len(c.overlays) > 0 || c.anyCall() {
		return
	}
	if top := c.sys.top(); top == nil || top.S.ID != "sys.idle" {
		return
	}
	if c.now().Sub(c.lastKey) >= time.Duration(s)*time.Second {
		c.keylock = true
		c.log.Printf("keypad locked after %ds idle", s)
	}
}

// unlocked: the owner unlocked the device (keystore.SetUnlocked).
func (c *Core) unlocked() bool {
	_, err := os.Stat(keystore.UnlockedPath(c.cfg.StateDir))
	return err == nil
}

// confirmUnlock asks the owner, in a system dialog, before unlocking or
// locking the device again: both erase every app key in the keystore.
func (c *Core) confirmUnlock(on bool) {
	if on == c.unlocked() {
		return
	}
	text := tr.T("Unlock the device?\nEvery app's keys are erased; then system components you built yourself (such as a renderer) can be swapped in, device attestation reports “unlocked”, and payment apps may refuse service.")
	done := tr.T("Device unlocked")
	if !on {
		text = tr.T("Lock it again?\nEvery app's keys are erased once more, and the swapped-in system components are no longer used.")
		done = tr.T("Device locked again")
	}
	c.pushOverlay(&confirmOverlay{text: text, trust: true, onYes: func() {
		c.broker.call(svc.Keystored, svc.KeySetUnlocked, map[string]bool{"on": on}, nil, func(err error) {
			if err != nil {
				c.showToast(tr.T("Failed: %v", err))
				return
			}
			c.log.Printf("device unlocked=%v by the owner", on)
			c.pushKeyChanged()
			c.showToast(done)
		})
	}})
}

// RendererOverride is a renderer the owner put in the state directory of an
// unlocked device (their own build, e.g. the GPLv3 phone-gui), or "".
func RendererOverride(stateDir string) string {
	if _, err := os.Stat(keystore.UnlockedPath(stateDir)); err != nil {
		return ""
	}
	p := filepath.Join(stateDir, "renderer")
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
		return p
	}
	return ""
}

// localeSettings is what Settings › Language, Region and Date and time
// show: the lists settings.set takes the 1-based index in, what is in
// force and what the system would choose.
func (c *Core) localeSettings() map[string]any {
	lang := c.locale.lang
	type named struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var langs []named
	for _, l := range i18n.Languages() {
		langs = append(langs, named{l.ID, l.Name})
	}
	var regions []named
	for _, r := range regionList(lang) {
		regions = append(regions, named{r.Code, localName(r.Names, lang, r.Code)})
	}
	autoRegion, regionFrom := c.autoRegion()
	region := c.region()
	var regionZones []string
	if r := regionByCode(region); r != nil {
		regionZones = r.Zones
	}
	now := time.Now()
	zone, zoneFrom := c.zoneInForce()
	auto, autoFrom := c.autoZone()
	return map[string]any{
		"languages": langs, ui.SettingLanguage: lang, "language_auto": c.autoLanguage(),
		"regions": regions, ui.SettingRegion: region, "region_auto": map[string]string{"code": autoRegion, "from": regionFrom},
		"zones": zoneList(lang, now), "region_zones": regionZones,
		ui.SettingTimeZone: map[string]string{"id": zone.String(), "city": zoneCity(zone.String(), lang), "from": zoneFrom},
		"time_zone_auto":   map[string]string{"id": auto.String(), "city": zoneCity(auto.String(), lang), "from": autoFrom},
		"clock": map[string]any{"date": now.Format(time.DateOnly), "time": now.Format("15:04"), "date_text": dateText(now),
			"time_text": c.clockText(now), "text": dateText(now) + " " + c.clockText(now), "can_set": canSetClock(), "ntp": c.locale.timeSync},
	}
}

// setLocale is settings.set for the language, the region and the time.
func (c *Core) setLocale(key string, v float64) error {
	i := int(v)
	if float64(i) != v || i < 0 {
		return fmt.Errorf("%s takes an index", key)
	}
	switch key {
	case ui.SettingLanguage:
		langs := i18n.Languages()
		if i > len(langs) {
			return errors.New("no such language")
		}
		c.settings.Language = ""
		if i > 0 {
			c.settings.Language = langs[i-1].ID
		}
	case ui.SettingRegion:
		list := regionList(c.locale.lang)
		if i > len(list) {
			return errors.New("no such region")
		}
		c.settings.Region = ""
		if i > 0 {
			c.settings.Region = list[i-1].Code
		}
	case ui.SettingTimeZone:
		zones := zoneList(c.locale.lang, time.Now())
		if i > len(zones) {
			return errors.New("no such time zone")
		}
		c.settings.Zone = ""
		if i > 0 {
			c.settings.Zone = zones[i-1].ID
		}
	case ui.SettingAutoTime:
		c.settings.ManualTime = v == 0
		c.applyTimeSync()
	case ui.SettingClock24:
		c.settings.Clock12 = v == 0
		c.refreshSystemScreens()
	case ui.SettingTime:
		return c.setManualTime(v)
	}
	c.applyLocale()
	return nil
}
