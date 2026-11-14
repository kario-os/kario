package shell

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ui"
)

// localeCore is a Core with what the language, region and time code needs;
// the process's language and zone come back afterwards.
func localeCore(t *testing.T) *Core {
	t.Setenv("PHONE_LANG", "")
	loc := time.Local
	t.Cleanup(func() {
		time.Local = loc
		i18n.SetLang(i18n.DefaultLang)
	})
	c := &Core{log: log.New(io.Discard, "", 0), settings: defaultSettings()}
	c.cfg.StateDir = t.TempDir()
	c.sys = &App{M: &Manifest{ID: "system"}, system: true}
	return c
}

func TestRegionData(t *testing.T) {
	for mcc, want := range map[string]string{"334": "MX", "46000": "CN", "466": "TW", "214": "ES", "310": "US", "": "", "999": ""} {
		if got := regionByMCC(mcc); got != want {
			t.Errorf("MCC %q: %q, want %q", mcc, got, want)
		}
	}
	if got := regionName("MX", "es"); got != "M\u00e9xico" {
		t.Errorf("Mexico in Spanish: %q", got)
	}
	if got := zoneCity("America/Mexico_City", "es"); got != "Ciudad de M\u00e9xico" {
		t.Errorf("Mexico City in Spanish: %q", got)
	}
	if got := zoneCity("Asia/Shanghai", "zh-Hant"); got != "\u4e0a\u6d77" {
		t.Errorf("Shanghai in Traditional Chinese: %q", got)
	}
	list := regionList("es")
	pos := map[string]int{}
	for i, r := range list {
		pos[r.Code] = i
	}
	// Alemania, Argentina, ..., Islas Åland folded among the I's, Zambia late.
	if !(pos["DE"] < pos["AR"] && pos["AR"] < pos["ES"] && pos["ES"] < pos["ZM"]) || len(list) < 240 {
		t.Errorf("Spanish order: DE %d AR %d ES %d ZM %d of %d", pos["DE"], pos["AR"], pos["ES"], pos["ZM"], len(list))
	}
	if r := regionByCode("MX"); r == nil || r.Zones[0] != "America/Mexico_City" {
		t.Errorf("Mexico's main zone: %+v", r)
	}
	if r := regionByCode("RU"); r == nil || r.Zones[0] != "Europe/Moscow" {
		t.Errorf("Russia's main zone: %+v", r)
	}
}

// Left to the system the region is the SIM's, then the last SIM's, then the
// SKU's, then the default; the language follows it unless the user chose.
func TestAutoRegionAndLanguage(t *testing.T) {
	c := localeCore(t)
	if r, from := c.autoRegion(); r != "CN" || from != "default" || c.languageInForce() != "zh-Hans" {
		t.Fatalf("no SIM, no SKU: %s %s %s", r, from, c.languageInForce())
	}
	c.cfg.SKU.Region = "tw"
	if r, from := c.autoRegion(); r != "TW" || from != "sku" || c.languageInForce() != "zh-Hant" {
		t.Errorf("SKU region: %s %s %s", r, from, c.languageInForce())
	}
	c.networkSays(netInfo{Sim: "ready", SimMCC: "334", NetMCC: "334"})
	if c.region() != "MX" || c.Language() != "es" || c.settings.SimRegion != "MX" {
		t.Errorf("a SIM from Mexico: %s %s %s", c.region(), c.Language(), c.settings.SimRegion)
	}
	c.networkSays(netInfo{Sim: "absent"})
	if r, from := c.autoRegion(); r != "MX" || from != "sim" {
		t.Errorf("the last SIM's: %s %s", r, from)
	}
	c.settings.Language = "en"
	if c.languageInForce() != "en" {
		t.Error("the user's language")
	}
	c.settings.Language, c.settings.Region = "", "AR"
	if c.region() != "AR" || c.languageInForce() != "es" {
		t.Errorf("the user's region: %s %s", c.region(), c.languageInForce())
	}
	t.Setenv("PHONE_LANG", "en")
	if c.languageInForce() != "en" {
		t.Error("PHONE_LANG stands for the automatic language")
	}
}

func TestAutoZone(t *testing.T) {
	c := localeCore(t)
	zone := func(n netInfo) string {
		c.locale.net, c.locale.netKnown = n, true
		loc, _ := c.autoZone()
		return loc.String()
	}
	for _, x := range []struct {
		n    netInfo
		want string
	}{
		{netInfo{SimMCC: "334", NetMCC: "334", NITZ: &nitz{Offset: -360}}, "America/Mexico_City"},
		{netInfo{SimMCC: "334", NetMCC: "334", NITZ: &nitz{Offset: -300}}, "America/Cancun"},
		{netInfo{SimMCC: "460", NetMCC: "460", NITZ: &nitz{Offset: 480}}, "Asia/Shanghai"},
		{netInfo{SimMCC: "460", NetMCC: "", NITZ: &nitz{Offset: 540}}, "Etc/GMT-9"},
		{netInfo{SimMCC: "460", NetMCC: "214"}, "Europe/Madrid"},
		{netInfo{SimMCC: "460"}, "Asia/Shanghai"},
	} {
		if got := zone(x.n); got != x.want {
			t.Errorf("%+v %+v: %s, want %s", x.n, x.n.NITZ, got, x.want)
		}
	}
	if got := fixedZone(330).String(); got != "UTC+05:30" {
		t.Errorf("a half-hour offset: %s", got)
	}
	c.settings.Zone = "Europe/Lisbon"
	if loc, from := c.zoneInForce(); loc.String() != "Europe/Lisbon" || from != "user" {
		t.Errorf("the user's zone: %s %s", loc, from)
	}
	c.applyZone()
	if time.Local.String() != "Europe/Lisbon" || c.zoneName() != "Europe/Lisbon" {
		t.Errorf("in force: %s %s", time.Local, c.zoneName())
	}
}

// Dates and times in each language, in the zone in force.
func TestDateAndClock(t *testing.T) {
	c := localeCore(t)
	time.Local = time.UTC
	at := time.Date(2026, 11, 12, 21, 41, 0, 0, time.UTC)
	for lang, want := range map[string][2]string{
		"zh-Hans": {"11\u670812\u65e5 \u661f\u671f\u56db", "\u4e0b\u53489:41"},
		"zh-Hant": {"11\u670812\u65e5 \u661f\u671f\u56db", "\u4e0b\u53489:41"},
		"en":      {"Thursday, November 12", "9:41 PM"},
		"es":      {"jueves, 12 de noviembre", "9:41 p. m."},
	} {
		i18n.SetLang(lang)
		c.settings.Clock12 = true
		if d, h := dateText(at), c.clockText(at); d != want[0] || h != want[1] {
			t.Errorf("%s: %q %q, want %q %q", lang, d, h, want[0], want[1])
		}
		c.settings.Clock12 = false
		if h := c.clockText(at); h != "21:41" {
			t.Errorf("%s 24-hour: %q", lang, h)
		}
	}
	time.Local, _ = time.LoadLocation("America/Mexico_City")
	if h := c.clockText(at); h != "15:41" {
		t.Errorf("in Mexico City: %q", h)
	}
}

func TestManifestLocalize(t *testing.T) {
	m := &Manifest{Name: "\u5929\u6c14", Desc: "\u9884\u62a5", Permissions: []string{"location"},
		Purposes: map[string]string{"location": "\u5b9a\u4f4d"},
		Settings: []ui.AppSetting{{Key: "units", Title: "\u5355\u4f4d", Type: "choice", Choices: []string{"c", "f"}}},
		Commands: []AppCommand{{ID: "today", Label: "\u4eca\u5929"}},
		Intents:  []IntentDecl{{Action: "pay", Label: "\u4ed8\u6b3e"}},
		Locales: map[string]i18n.AppText{
			"en": {Name: "Weather", Description: "Forecasts", Purposes: map[string]string{"location": "Your city's weather"},
				Settings: map[string]i18n.SettingText{"units": {Title: "Units", Choices: []string{"Celsius", "Fahrenheit"}}},
				Commands: map[string]string{"today": "Today"}, Intents: map[string]string{"pay": "Pay"}},
			"es": {Name: "Clima", Settings: map[string]i18n.SettingText{"units": {Title: "Unidades"}}},
		}}
	if err := i18n.CheckAppText(m.Lang, m.Locales, m.textRules()); err != nil {
		t.Fatal(err)
	}
	m.localize("es")
	if m.Name != "Clima" || m.Desc != "Forecasts" || m.Settings[0].Title != "Unidades" || m.Purposes["location"] != "Your city's weather" ||
		m.Commands[0].Label != "Today" || m.Intents[0].Label != "Pay" {
		t.Errorf("Spanish, English where it has none: %+v", m)
	}
	if got := strings.Join(m.ChoiceLabels("units", "es"), " "); got != "Celsius Fahrenheit" || m.Settings[0].Choices[0] != "c" {
		t.Errorf("choice labels %q, values %v", got, m.Settings[0].Choices)
	}
	m.localize("zh-Hant")
	if m.Name != "\u5929\u6c14" || m.Settings[0].Title != "\u5355\u4f4d" || m.ChoiceLabels("units", "zh-Hant") != nil {
		t.Errorf("back to the manifest's own: %+v", m)
	}
}

// The system image's manifests: valid locales, and every app's name in each
// language offered (Snake and Music are translated in their own pass).
func TestSystemManifestLocales(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join("..", "apps", "*", "manifest.json"))
	if len(paths) < 10 {
		t.Fatalf("%d manifests", len(paths))
	}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		m := &Manifest{}
		if err := json.Unmarshal(raw, m); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if err := i18n.CheckAppText(m.Lang, m.Locales, m.textRules()); err != nil {
			t.Errorf("%s: %v", p, err)
		}
		for _, l := range []string{"zh-Hant", "en", "es"} {
			if m.Locales[l].Name == "" || m.Locales[l].Description == "" {
				t.Errorf("%s: no %s name or description", p, l)
			}
		}
	}
}

// settings.set takes the lists' 1-based indexes; 0 leaves it to the system.
func TestLocaleSettings(t *testing.T) {
	c := localeCore(t)
	c.applyLocale()
	if c.Language() != "zh-Hans" {
		t.Fatalf("boot language %s", c.Language())
	}
	if err := c.setLocale(ui.SettingLanguage, 4); err != nil || c.settings.Language != "es" || i18n.Lang() != "es" || c.sys.M.Name != "Sistema" {
		t.Fatalf("Spanish: %v %q %s %q", err, c.settings.Language, i18n.Lang(), c.sys.M.Name)
	}
	got := c.localeSettings()
	regions := got["regions"]
	raw, _ := json.Marshal(regions)
	var list []struct{ ID, Name string }
	_ = json.Unmarshal(raw, &list)
	mx := 0
	for i, r := range list {
		if r.ID == "MX" {
			mx = i + 1
		}
	}
	if err := c.setLocale(ui.SettingRegion, float64(mx)); err != nil || c.region() != "MX" {
		t.Fatalf("region %d: %v %s", mx, err, c.region())
	}
	if loc, _ := c.zoneInForce(); loc.String() != "America/Mexico_City" {
		t.Errorf("Mexico's zone: %s", loc)
	}
	if err := c.setLocale(ui.SettingLanguage, 0); err != nil || c.Language() != "es" {
		t.Errorf("automatic in Mexico: %v %s", err, c.Language())
	}
	if err := c.setLocale(ui.SettingRegion, 0); err != nil || c.Language() != "zh-Hans" {
		t.Errorf("automatic again: %v %s", err, c.Language())
	}
	for _, bad := range []struct {
		key string
		v   float64
	}{{ui.SettingLanguage, 5}, {ui.SettingRegion, 1e6}, {ui.SettingTimeZone, 1.5}, {ui.SettingLanguage, -1}} {
		if err := c.setLocale(bad.key, bad.v); err == nil {
			t.Errorf("%s=%v accepted", bad.key, bad.v)
		}
	}
	if err := c.setLocale(ui.SettingTime, float64(time.Date(2026, 1, 31, 8, 30, 0, 0, time.UTC).Unix())); err == nil {
		t.Error("the time set while automatic")
	}
	if err := c.settingsSet(ui.SettingClock24, 0); err != nil || !c.settings.Clock12 {
		t.Error("12-hour clock")
	}
	if saved, _ := os.ReadFile(c.settingsPath()); !strings.Contains(string(saved), `"clock_12h": true`) {
		t.Errorf("not saved: %s", saved)
	}
}

// The clock follows the network's time while NTP has not, and the user's
// once automatic time is off; the wall clock the user typed is in the zone
// in force.
func TestSetClock(t *testing.T) {
	c := localeCore(t)
	can, synced, set := canSetClock, ntpSynced, setSystemClock
	t.Cleanup(func() { canSetClock, ntpSynced, setSystemClock = can, synced, set })
	var got []time.Time
	canSetClock, ntpSynced = func() bool { return true }, func() bool { return false }
	setSystemClock = func(at time.Time, _ string) error { got = append(got, at); return nil }
	now := time.Now().Unix()
	c.syncFromNITZ(&nitz{Offset: 480, Time: now + 3600, Received: now})
	c.syncFromNITZ(&nitz{Offset: 480, Time: now, Received: now})
	if len(got) != 1 || got[0].Unix() < now+3599 {
		t.Fatalf("NITZ an hour ahead sets the clock once: %v", got)
	}
	ntpSynced = func() bool { return true }
	c.syncFromNITZ(&nitz{Time: now + 3600, Received: now})
	if len(got) != 1 {
		t.Fatal("NITZ over NTP")
	}
	c.settings.Zone = "America/Mexico_City"
	wall := float64(time.Date(2026, 1, 31, 8, 30, 0, 0, time.UTC).Unix())
	if err := c.setLocale(ui.SettingTime, wall); err == nil {
		t.Fatal("set by hand while automatic")
	}
	if err := c.setLocale(ui.SettingAutoTime, 0); err != nil || !c.settings.ManualTime {
		t.Fatal("automatic time off")
	}
	c.syncFromNITZ(&nitz{Time: now + 3600, Received: now})
	if err := c.setLocale(ui.SettingTime, wall); err != nil || len(got) != 2 || !got[1].Equal(time.Date(2026, 1, 31, 14, 30, 0, 0, time.UTC)) {
		t.Fatalf("08:30 in Mexico City is 14:30 UTC: %v %v", err, got)
	}
	if err := c.setLocale(ui.SettingTime, float64(time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC).Unix())); err == nil {
		t.Error("a date before 2020")
	}
}

// An app's title that copies a system dialog's, in any offered language, is
// marked as the app's; an ordinary one is not.
func TestReservedTitle(t *testing.T) {
	for _, l := range i18n.Languages() {
		if title := tr.In(l.ID).T("System security check"); !reservedTitle(title) {
			t.Errorf("%s: %q passes as an app's own title", l.ID, title)
		}
	}
	for _, title := range []string{"Notes", "Calculator", tr.In("es").T("Settings")} {
		if reservedTitle(title) {
			t.Errorf("%q is taken for a system title", title)
		}
	}
}
