package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Language, Region and Date and time. The shell keeps them
// (settings.get gives the lists, settings.set takes the 1-based index in
// one, 0 for automatic); after a language change it restarts this app, which
// comes back on the pages it had open (OnSave).

type named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type zoneEntry struct {
	ID     string `json:"id"`
	City   string `json:"city"`
	Offset string `json:"offset"`
}

type zoneInForce struct {
	ID   string `json:"id"`
	City string `json:"city"`
	From string `json:"from"`
}

// localeInfo is what settings.get says about them.
type localeInfo struct {
	Languages    []named `json:"languages"`
	Language     string  `json:"language"`
	LanguageAuto string  `json:"language_auto"`
	Regions      []named `json:"regions"`
	Region       string  `json:"region"`
	RegionAuto   struct {
		Code string `json:"code"`
		From string `json:"from"`
	} `json:"region_auto"`
	Zones       []zoneEntry `json:"zones"`
	RegionZones []string    `json:"region_zones"`
	Zone        zoneInForce `json:"time_zone"`
	ZoneAuto    zoneInForce `json:"time_zone_auto"`
	Clock       struct {
		Date     string `json:"date"`
		Time     string `json:"time"`
		DateText string `json:"date_text"`
		TimeText string `json:"time_text"`
		Text     string `json:"text"`
		CanSet   bool   `json:"can_set"`
		NTP      string `json:"ntp"`
	} `json:"clock"`
}

// languageNames say in the language shown which language each is.
var languageNames = map[string]string{"zh-Hans": "Simplified Chinese", "zh-Hant": "Traditional Chinese", "en": "English", "es": "Spanish"}

// zoneAreas are the tz database's areas, which the Time zone page groups
// zones by, and their names.
var zoneAreas = []struct{ ID, Name string }{{"Africa", "Africa"}, {"America", "Americas"}, {"Antarctica", "Antarctica"},
	{"Arctic", "Arctic"}, {"Asia", "Asia"}, {"Atlantic", "Atlantic Ocean"}, {"Australia", "Australia"}, {"Europe", "Europe"},
	{"Indian", "Indian Ocean"}, {"Pacific", "Pacific Ocean"}}

func (p *app) languageName(id string) string {
	for _, l := range p.s.Languages {
		if l.ID == id {
			return l.Name
		}
	}
	return id
}

func (p *app) regionName(code string) string {
	for _, r := range p.s.Regions {
		if r.ID == code {
			return r.Name
		}
	}
	return code
}

func (p *app) zoneOffset(id string) string {
	for _, z := range p.s.Zones {
		if z.ID == id {
			return z.Offset
		}
	}
	return ""
}

func regionFrom(from string) string {
	switch from {
	case "sim":
		return tr.T("from the SIM card")
	case "sku":
		return tr.T("from this phone's model")
	}
	return tr.T("the default")
}

func zoneFrom(from string) string {
	if from == "network" {
		return tr.T("from the mobile network")
	}
	return tr.T("from the region")
}

// localeItems are the root page's entries.
func (p *app) localeItems() []ui.Node {
	open := []ui.Action{sdk.Open(tr.T("Open"))}
	lang := p.languageName(p.s.Language)
	if p.s.Settings.Language == "" {
		lang = tr.T("%s (automatic)", lang)
	}
	region := p.regionName(p.s.Region)
	if p.s.Settings.Region == "" {
		region = tr.T("%s (automatic)", region)
	}
	return []ui.Node{
		{Kind: ui.KindItem, ID: ui.SettingLanguage, Title: tr.T("Language"), Subtitle: lang, Actions: open},
		{Kind: ui.KindItem, ID: ui.SettingRegion, Title: tr.T("Region"), Subtitle: region, Actions: open},
		{Kind: ui.KindItem, ID: "datetime", Title: tr.T("Date and time"), Subtitle: p.s.Clock.Text, Actions: open},
	}
}

func choose() []ui.Action { return []ui.Action{{ID: "choose", Label: tr.T("Use"), Primary: true}} }

func (p *app) languageScreen() ui.Screen {
	s := ui.Screen{ID: ui.SettingLanguage, Title: tr.T("Language")}
	auto := ui.Node{Kind: ui.KindItem, ID: "lang:0", Title: tr.T("Automatic"),
		Subtitle: tr.T("%s, the language of %s", p.languageName(p.s.LanguageAuto), p.regionName(p.s.Region)), Actions: choose()}
	if p.s.Settings.Language == "" {
		auto.Badge = tr.T("Current")
	}
	s.Body = append(s.Body, auto)
	for i, l := range p.s.Languages {
		n := ui.Node{Kind: ui.KindItem, ID: fmt.Sprint("lang:", i+1), Title: l.Name, Subtitle: tr.T(languageNames[l.ID]), Actions: choose()}
		if p.s.Settings.Language == l.ID {
			n.Badge = tr.T("Current")
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Apps restart in the new language and come back where they were")))
	return s
}

func (p *app) regionScreen() ui.Screen {
	s := ui.Screen{ID: ui.SettingRegion, Title: tr.T("Region")}
	auto := ui.Node{Kind: ui.KindItem, ID: "region:0", Title: tr.T("Automatic"),
		Subtitle: tr.T("%s, %s", p.regionName(p.s.RegionAuto.Code), regionFrom(p.s.RegionAuto.From)), Actions: choose()}
	if p.s.Settings.Region == "" {
		auto.Badge = tr.T("Current")
	}
	s.Body = append(s.Body, auto, sdk.Styled(ui.StyleDim, tr.T("The region picks the automatic language and, when the network does not say, the time zone")))
	for i, r := range p.s.Regions {
		n := ui.Node{Kind: ui.KindItem, ID: fmt.Sprint("region:", i+1), Title: r.Name, Trailing: r.ID, Actions: choose()}
		if p.s.Settings.Region == r.ID {
			n.Badge = tr.T("Current")
		}
		s.Body = append(s.Body, n)
	}
	return s
}

func (p *app) dateTimeScreen() ui.Screen {
	st := p.s.Settings
	s := ui.Screen{ID: "datetime", Title: tr.T("Date and time"), Body: []ui.Node{sdk.Styled(ui.StyleBig, p.s.Clock.TimeText), sdk.Text(p.s.Clock.DateText)}}
	auto := ui.Node{Kind: ui.KindItem, ID: ui.SettingAutoTime, Title: tr.T("Set time automatically"), Trailing: onOff(!st.ManualTime),
		Subtitle: tr.T("Network time when online, else the mobile network's"), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}}
	if !p.s.Clock.CanSet {
		auto.Subtitle = tr.T("Only the phone itself sets its clock; this development machine keeps its own")
	}
	zone := p.s.Zone.City + " · " + p.zoneOffset(p.s.Zone.ID)
	if st.Zone == "" {
		zone = tr.T("%s (automatic, %s)", zone, zoneFrom(p.s.Zone.From))
	}
	s.Body = append(s.Body, auto,
		ui.Node{Kind: ui.KindItem, ID: "zone", Title: tr.T("Time zone"), Subtitle: zone, Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
	if st.ManualTime && p.s.Clock.CanSet {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "settime", Title: tr.T("Set date and time"),
			Subtitle: p.s.Clock.Date + " " + p.s.Clock.Time, Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: ui.SettingClock24, Title: tr.T("24-hour clock"), Trailing: onOff(!st.Clock12),
		Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}})
	return s
}

func (p *app) zoneIndex(id string) int {
	for i, z := range p.s.Zones {
		if z.ID == id {
			return i + 1
		}
	}
	return 0
}

func (p *app) zoneNode(i int) ui.Node {
	z := p.s.Zones[i-1]
	n := ui.Node{Kind: ui.KindItem, ID: fmt.Sprint("zone:", i), Title: z.City, Subtitle: z.ID, Trailing: z.Offset, Actions: choose()}
	if p.s.Settings.Zone == z.ID {
		n.Badge = tr.T("Current")
	}
	return n
}

func (p *app) zoneScreen() ui.Screen {
	s := ui.Screen{ID: "zone", Title: tr.T("Time zone")}
	auto := ui.Node{Kind: ui.KindItem, ID: "zone:0", Title: tr.T("Automatic"),
		Subtitle: tr.T("%s, %s", p.s.ZoneAuto.City, zoneFrom(p.s.ZoneAuto.From)), Actions: choose()}
	if p.s.Settings.Zone == "" {
		auto.Badge = tr.T("Current")
	}
	s.Body = append(s.Body, auto, sdk.Section(p.regionName(p.s.Region)))
	for _, id := range p.s.RegionZones {
		if i := p.zoneIndex(id); i > 0 {
			s.Body = append(s.Body, p.zoneNode(i))
		}
	}
	s.Body = append(s.Body, sdk.Section(tr.T("All time zones")))
	for _, a := range zoneAreas {
		n := 0
		for _, z := range p.s.Zones {
			if strings.HasPrefix(z.ID, a.ID+"/") {
				n++
			}
		}
		if n > 0 {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "area:" + a.ID, Title: tr.T(a.Name), Trailing: fmt.Sprint(n),
				Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
		}
	}
	return s
}

func (p *app) areaScreen(area string) ui.Screen {
	title := area
	for _, a := range zoneAreas {
		if a.ID == area {
			title = tr.T(a.Name)
		}
	}
	s := ui.Screen{ID: "zones:" + area, Title: title}
	for i, z := range p.s.Zones {
		if strings.HasPrefix(z.ID, area+"/") {
			s.Body = append(s.Body, p.zoneNode(i+1))
		}
	}
	return s
}

func (p *app) setTimeScreen() ui.Screen {
	return ui.Screen{ID: "settime", Title: tr.T("Set date and time"), Body: []ui.Node{
		{Kind: ui.KindInput, ID: "date", Value: p.s.Clock.Date, Placeholder: tr.T("Date (YYYY-MM-DD)")},
		{Kind: ui.KindInput, ID: "time", Value: p.s.Clock.Time, Placeholder: tr.T("Time (HH:MM)"), Submit: "save"},
		sdk.Button("save", tr.T("Set"), "save"),
	}}
}

// localeScreen builds a page of these by its ID (restoring, refreshing).
func (p *app) localeScreen(id string) (ui.Screen, bool) {
	switch {
	case id == ui.SettingLanguage:
		return p.languageScreen(), true
	case id == ui.SettingRegion:
		return p.regionScreen(), true
	case id == "datetime":
		return p.dateTimeScreen(), true
	case id == "zone":
		return p.zoneScreen(), true
	case strings.HasPrefix(id, "zones:"):
		return p.areaScreen(strings.TrimPrefix(id, "zones:")), true
	}
	return ui.Screen{}, false
}

// push opens a page that the app can build again by its ID: it is among the
// pages OnSave hands over.
func (p *app) push(s ui.Screen) {
	p.pages = append(p.pages, s.ID)
	p.a.Push(s)
}

func (p *app) popped(id string) {
	if n := len(p.pages); n > 0 && p.pages[n-1] == id {
		p.pages = p.pages[:n-1]
	}
}

// savedPages is what OnSave hands over: the pages open above the root.
type savedPages struct {
	Pages []string `json:"pages"`
}

// restorePages opens again the pages the process the system ended had open.
func (p *app) restorePages(saved savedPages) {
	for _, id := range saved.Pages {
		s, ok := p.localeScreen(id)
		if !ok {
			s, ok = p.screenByID(id)
		}
		if !ok {
			return
		}
		p.push(s)
	}
}

// localeAction handles these pages' actions; false for the rest.
func (p *app) localeAction(e sdk.ActionEvent) bool {
	index := func(prefix string) int {
		var i int
		fmt.Sscanf(strings.TrimPrefix(e.Node, prefix), "%d", &i)
		return i
	}
	switch {
	case e.Screen == "root" && (e.Node == ui.SettingLanguage || e.Node == ui.SettingRegion || e.Node == "datetime"):
		s, _ := p.localeScreen(e.Node)
		p.push(s)
	case e.Screen == ui.SettingLanguage && e.Action == "choose":
		p.set(ui.SettingLanguage, float64(index("lang:")))
	case e.Screen == ui.SettingRegion && e.Action == "choose":
		p.set(ui.SettingRegion, float64(index("region:")))
	case e.Screen == "datetime" && e.Node == ui.SettingAutoTime:
		p.set(ui.SettingAutoTime, map[bool]float64{true: 1, false: 0}[p.s.Settings.ManualTime])
	case e.Screen == "datetime" && e.Node == ui.SettingClock24:
		p.set(ui.SettingClock24, map[bool]float64{true: 1, false: 0}[p.s.Settings.Clock12])
	case e.Screen == "datetime" && e.Node == "zone":
		p.push(p.zoneScreen())
	case e.Screen == "datetime" && e.Node == "settime":
		p.a.Push(p.setTimeScreen())
	case (e.Screen == "zone" || strings.HasPrefix(e.Screen, "zones:")) && e.Action == "choose":
		p.set(ui.SettingTimeZone, float64(index("zone:")))
	case e.Screen == "zone" && strings.HasPrefix(e.Node, "area:"):
		p.push(p.areaScreen(strings.TrimPrefix(e.Node, "area:")))
	case e.Screen == "settime" && e.Action == "save":
		t, err := time.Parse("2006-01-02 15:04", strings.TrimSpace(e.Fields["date"])+" "+strings.TrimSpace(e.Fields["time"]))
		if err != nil {
			p.a.Toast(tr.T("Enter the date as 2026-01-31 and the time as 08:30"))
			return true
		}
		p.a.Pop()
		p.set(ui.SettingTime, float64(t.Unix()))
	default:
		return false
	}
	return true
}

// refreshLocale redraws the open pages of these.
func (p *app) refreshLocale() {
	for _, id := range p.pages {
		if s, ok := p.localeScreen(id); ok {
			p.a.Update(s)
		}
	}
}
