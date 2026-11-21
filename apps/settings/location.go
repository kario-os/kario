package main

import (
	"strings"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Location: the switch that turns location off for every app,
// the receiver and what assisted it, the network location service and its
// switch, which apps had the location in the last day and which of them
// in the background (the Shell keeps that, settings.get "location"), and
// the apps holding the location permission, each allowed, denied or asked,
// given its precise or only its approximate location, and, if it asks for
// it, the location all the time.

type locationInfo struct {
	On          bool     `json:"on"`
	Receiver    string   `json:"receiver"`
	Using       []string `json:"using"`
	Approximate []string `json:"approximate"`
	Recent      []struct {
		App  string `json:"app"`
		Name string `json:"name"`
		At   int64  `json:"at"`
		BgAt int64  `json:"bg_at"`
	} `json:"recent"`
	Network struct {
		Provider string `json:"provider"`
		Host     string `json:"host"`
		On       bool   `json:"on"`
		Error    string `json:"error"`
	} `json:"network"`
	Assistance *struct {
		SUPL     string `json:"supl"`
		Injected int    `json:"injected"`
		Held     bool   `json:"held"`
		Err      string `json:"error"`
	} `json:"assistance"`
}

// locState is the Shell's answer; tried: asked once for the root page's
// line (as nfcState).
var locState struct {
	locationInfo
	loaded, tried bool
}

type locationReply struct {
	Location locationInfo `json:"location"`
}

func (p *app) loadLocation(then func()) {
	locState.tried = true
	var r locationReply
	p.a.Go(ui.CapSettingsGet, nil, &r, func(err error) {
		if err == nil {
			locState.locationInfo, locState.loaded = r.Location, true
		}
		if then != nil {
			then()
		}
	})
}

func (p *app) locationSummary() string {
	switch {
	case !locState.loaded:
		return ""
	case !locState.On:
		return tr.T("Off for every app")
	}
	return tr.T("On · %d apps used it in the last 24 hours", len(locState.Recent))
}

// locationItem is the page's line on the root page.
func (p *app) locationItem() ui.Node {
	if !locState.tried {
		p.loadLocation(nil)
	}
	return ui.Node{Kind: ui.KindItem, ID: "location", Title: tr.T("Location"), Subtitle: p.locationSummary(), Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
}

func receiverName(kind string) string {
	switch kind {
	case "sim":
		return tr.T("Simulated receiver: this machine has no satellite positioning")
	case "nmea":
		return tr.T("Receiver: the satellite positioning chip")
	case "modemmanager":
		return tr.T("Receiver: the modem's GPS")
	case "fifo":
		return tr.T("Receiver: NMEA from the emulator")
	}
	return tr.T("This phone has no location receiver")
}

func when(ms int64) string {
	t := time.UnixMilli(ms)
	if t.Format(time.DateOnly) == time.Now().Format(time.DateOnly) {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}

// networkItem is the switch of the network location, if the phone has a
// service.
func networkItem(st locationInfo) (ui.Node, bool) {
	n := st.Network
	if n.Provider == "" {
		return ui.Node{}, false
	}
	host := n.Host
	if n.Provider == "fake" {
		host = tr.T("the simulated service")
	}
	sub := tr.T("Asks %s, by nearby Wi-Fi and the mobile cell, while satellites give no fix", host)
	switch {
	case !n.On:
		sub = tr.T("Off: only satellites place the phone")
	case n.Error != "":
		sub += " · " + n.Error
	}
	return ui.Node{Kind: ui.KindItem, ID: "loc_net", Title: tr.T("Wi-Fi and mobile network location"), Subtitle: sub,
		Trailing: onOff(n.On), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}}, true
}

// assistLine says what the modem's receiver was assisted with.
func assistLine(st locationInfo) string {
	a := st.Assistance
	if a == nil {
		return ""
	}
	var parts []string
	if a.SUPL != "" {
		parts = append(parts, tr.T("A-GNSS server %s", a.SUPL))
	}
	if a.Injected > 0 || a.Held {
		parts = append(parts, tr.T("satellite orbits given to the modem"))
	}
	if a.Err != "" {
		parts = append(parts, a.Err)
	}
	if len(parts) == 0 {
		return ""
	}
	return tr.T("Assisted: %s", strings.Join(parts, " · "))
}

// background: the app (permission key) asks for the location all the time,
// and whether it has it.
func (p *app) background(key string) (asks, allowed bool) {
	for _, e := range p.perms {
		if e.App == key && e.Perm == ui.PermLocationBackground {
			return true, e.State == "granted"
		}
	}
	return false, false
}

func (p *app) locationScreen() ui.Screen {
	st := locState
	s := ui.Screen{ID: "location", Title: tr.T("Location")}
	sub := tr.T("Apps you allow may ask where the phone is")
	if !st.On {
		sub = tr.T("Off for every app")
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "loc_on", Title: tr.T("Use location"), Subtitle: sub,
		Trailing: onOff(st.On), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}},
		sdk.Styled(ui.StyleDim, receiverName(st.Receiver)))
	if l := assistLine(st.locationInfo); l != "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, l))
	}
	if n, ok := networkItem(st.locationInfo); ok {
		s.Body = append(s.Body, n)
	}
	names := map[string]string{}
	for _, r := range st.Recent {
		names[r.App] = r.Name
	}
	for _, e := range p.perms {
		if e.Perm == ui.PermLocation {
			names[e.App] = e.Name
		}
	}
	if len(st.Using) > 0 {
		s.Body = append(s.Body, sdk.Section(tr.T("In use now")))
		for _, key := range st.Using {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "use:" + key, Title: names[key], Subtitle: key})
		}
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Recent access (last 24 hours)")))
	for _, r := range st.Recent {
		sub := r.App
		if r.BgAt > 0 {
			sub += " · " + tr.T("in the background at %s", when(r.BgAt))
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "rec:" + r.App, Title: r.Name, Subtitle: sub, Trailing: when(r.At)})
	}
	if len(st.Recent) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No app used the location in the last 24 hours")))
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Apps with the location permission")))
	n := 0
	for _, e := range p.perms {
		if e.Perm != ui.PermLocation {
			continue
		}
		n++
		precision := tr.T("Precise location")
		if contains(st.Approximate, e.App) {
			precision = tr.T("Approximate location")
		}
		state := tr.T(stateLabel[e.State])
		actions := []ui.Action{
			{ID: "granted", Label: tr.T("Allow"), Key: "a"},
			{ID: "denied", Label: tr.T("Deny"), Key: "d"},
			{ID: "ask", Label: tr.T("Ask next time"), Key: "q", Primary: true},
			{ID: "approx", Label: tr.T("Precise or approximate"), Key: "p"},
		}
		if asks, allowed := p.background(e.App); asks {
			if allowed {
				state = tr.T("Allowed all the time")
			} else if e.State == "granted" {
				state = tr.T("Allowed while in use")
			}
			actions = append(actions, ui.Action{ID: "always", Label: tr.T("All the time or only while in use"), Key: "b"})
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "lp:" + e.App, Title: e.Name, Subtitle: e.App + " · " + precision,
			Trailing: state, Actions: actions})
	}
	if n == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No app has asked for the location")))
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("An app given only its approximate location gets an area of about 2 km, not where you are. An app allowed the location all the time has it in the background too; you are reminded when it uses it.")))
	return s
}

// locationPermRow is a location grant's row on Settings › App permissions:
// its precision (first: a narrow screen cuts the end of the line), and all
// the time given or not.
func locationPermRow(e ui.PermEntry, sub string, actions []ui.Action) (string, []ui.Action) {
	switch e.Perm {
	case ui.PermLocation:
		precision := tr.T("Precise location")
		if e.Approximate {
			precision = tr.T("Approximate location")
		}
		return precision + " · " + sub, append(actions, ui.Action{ID: "approx", Label: tr.T("Precise or approximate"), Key: "p"})
	case ui.PermLocationBackground:
		return sub, []ui.Action{
			{ID: "granted", Label: tr.T("Allow all the time"), Key: "a"},
			{ID: "denied", Label: tr.T("Only while in use"), Key: "d", Primary: true},
		}
	}
	return sub, actions
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (p *app) locationSet(args ui.SettingArgs) {
	var r locationReply
	p.a.Go(ui.CapSettingsSet, args, &r, func(err error) {
		if err != nil {
			p.a.Toast(err.Error())
		} else {
			locState.locationInfo, locState.loaded = r.Location, true
		}
		p.a.Update(p.locationScreen())
		p.updateMenu()
	})
}

// openLocation shows the page with the current grants and record.
func (p *app) openLocation() {
	p.a.Go(ui.CapPermList, nil, &p.perms, func(error) {
		p.loadLocation(func() { p.push(p.locationScreen()) })
	})
}

// permChanged reloads the grants and shows them on both pages.
func (p *app) permChanged(err error) {
	if err != nil {
		p.a.Toast(err.Error())
	}
	p.a.Go(ui.CapPermList, nil, &p.perms, func(error) {
		p.a.Update(p.locationScreen())
		p.a.Update(p.permsScreen())
	})
}

// locationAction handles the location page, and the precision on App
// permissions; false for the rest.
func (p *app) locationAction(e sdk.ActionEvent) bool {
	switch {
	case onMenu(e.Screen) && e.Node == "location":
		p.openLocation()
	case e.Screen == "location" && e.Node == "loc_on":
		p.locationSet(ui.SettingArgs{Key: ui.SettingLocation, Value: onOffValue(locState.On)})
	case e.Screen == "location" && e.Node == "loc_net":
		p.locationSet(ui.SettingArgs{Key: ui.SettingLocationNetwork, Value: onOffValue(locState.Network.On)})
	case e.Screen == "location" && strings.HasPrefix(e.Node, "lp:") && e.Action == "approx":
		key := strings.TrimPrefix(e.Node, "lp:")
		p.locationSet(ui.SettingArgs{Key: ui.SettingLocationApprox, Text: key, Value: onOffValue(contains(locState.Approximate, key))})
	case e.Screen == "location" && strings.HasPrefix(e.Node, "lp:") && e.Action == "always":
		key := strings.TrimPrefix(e.Node, "lp:")
		state := "granted"
		if _, allowed := p.background(key); allowed {
			state = "denied"
		}
		p.a.Go(ui.CapPermSet, ui.PermSetArgs{App: key, Perm: ui.PermLocationBackground, State: state}, nil, p.permChanged)
	case e.Screen == "location" && strings.HasPrefix(e.Node, "lp:"):
		p.a.Go(ui.CapPermSet, ui.PermSetArgs{App: strings.TrimPrefix(e.Node, "lp:"), Perm: ui.PermLocation, State: e.Action}, nil, p.permChanged)
	case e.Screen == "perms" && e.Action == "approx" && strings.HasSuffix(e.Node, "/"+ui.PermLocation):
		key := strings.TrimSuffix(e.Node, "/"+ui.PermLocation)
		approx := false
		for _, pe := range p.perms {
			approx = approx || (pe.App == key && pe.Perm == ui.PermLocation && pe.Approximate)
		}
		var r locationReply
		p.a.Go(ui.CapSettingsSet, ui.SettingArgs{Key: ui.SettingLocationApprox, Text: key, Value: onOffValue(approx)}, &r, func(err error) {
			if err == nil {
				locState.locationInfo, locState.loaded = r.Location, true
			}
			p.permChanged(err)
		})
	default:
		return false
	}
	return true
}
