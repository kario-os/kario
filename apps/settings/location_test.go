package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kario-os/kario/ui"
)

func hasAction(n ui.Node, id string) bool {
	for _, a := range n.Actions {
		if a.ID == id {
			return true
		}
	}
	return false
}

// Settings › Location shows the network location's switch with the
// service it sends to, what assisted the receiver, which app uses the
// location in the background now, background use in the recent access,
// and for an app that asks for it the location all the time; App
// permissions shows each grant's precision and all the time.
func TestLocationRows(t *testing.T) {
	saved := locState
	defer func() { locState = saved }()
	p := &app{perms: []ui.PermEntry{
		{App: "com.phone.weather", Name: "Weather", Perm: ui.PermLocation, State: "granted", Approximate: true},
		{App: "com.phone.weather", Name: "Weather", Perm: ui.PermLocationBackground, State: "granted"},
		{App: "com.example.maps", Name: "Maps", Perm: ui.PermLocation, State: "granted"},
	}}
	err := json.Unmarshal([]byte(`{"on": true, "receiver": "modemmanager", "approximate": ["com.phone.weather"],
		"using": ["com.example.maps", "com.phone.weather"], "using_background": ["com.phone.weather"],
		"recent": [{"app": "com.phone.weather", "name": "Weather", "at": 1795000000000, "bg_at": 1795000000000}, {"app": "com.example.maps", "name": "Maps", "at": 1795000000000}],
		"network": {"provider": "http", "host": "geo.example.net", "on": true},
		"assistance": {"modem": "/m/0", "supl": "supl.example.net:7275", "injected": 30000}}`), &locState.locationInfo)
	if err != nil {
		t.Fatal(err)
	}
	s := p.locationScreen()
	if n := node(s, "loc_net"); !strings.Contains(n.Subtitle, "geo.example.net") || !hasAction(n, "toggle") {
		t.Fatalf("the network location's switch: %+v", n)
	}
	weather, maps := node(s, "lp:com.phone.weather"), node(s, "lp:com.example.maps")
	if weather.Trailing != tr.T("Allowed all the time") || !hasAction(weather, "always") || !strings.Contains(weather.Subtitle, tr.T("Approximate location")) {
		t.Fatalf("%+v", weather)
	}
	if hasAction(maps, "always") || strings.Contains(maps.Subtitle, tr.T("Approximate location")) {
		t.Fatalf("an app that does not ask for all the time: %+v", maps)
	}
	if r := node(s, "rec:com.phone.weather"); !strings.Contains(r.Subtitle, strings.TrimSpace(tr.T("in the background at %s", ""))) {
		t.Fatalf("background use: %+v", r)
	}
	if bg, fg := node(s, "use:com.phone.weather"), node(s, "use:com.example.maps"); !strings.Contains(bg.Subtitle, tr.T("in the background")) || strings.Contains(fg.Subtitle, tr.T("in the background")) {
		t.Fatalf("in use now: %+v, %+v", bg, fg)
	}
	if !strings.Contains(texts(s), "supl.example.net:7275") {
		t.Fatalf("no A-GNSS line:\n%s", texts(s))
	}
	locState.Network.On = false
	if n := node(p.locationScreen(), "loc_net"); n.Subtitle != tr.T("Off: only satellites place the phone") {
		t.Fatalf("%+v", n)
	}
	locState.Network.Provider = ""
	if n := node(p.locationScreen(), "loc_net"); n.ID != "" {
		t.Fatal("a switch for a service the phone does not have")
	}

	sub, actions := locationPermRow(p.perms[0], "com.phone.weather", nil)
	if !strings.Contains(sub, tr.T("Approximate location")) || len(actions) != 1 || actions[0].ID != "approx" {
		t.Fatalf("%q %+v", sub, actions)
	}
	if _, actions := locationPermRow(p.perms[1], "com.phone.weather", nil); len(actions) != 2 || actions[0].ID != "granted" || actions[1].ID != "denied" {
		t.Fatalf("%+v", actions)
	}
}
