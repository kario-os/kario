package main

import (
	"strings"
	"testing"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ui"
)

func node(s ui.Screen, id string) ui.Node {
	for _, n := range s.Body {
		if n.ID == id {
			return n
		}
	}
	return ui.Node{}
}

func texts(s ui.Screen) string {
	var b strings.Builder
	for _, n := range s.Body {
		b.WriteString(n.Text + "\n")
	}
	return b.String()
}

// The page shows the switch, the name and the password as the Shell
// reports them, the devices while it is on and what it shares; Wi-Fi's
// line says why it is off.
func TestHotspotPage(t *testing.T) {
	defer func(saved ui.HotspotState, loaded bool) { hsState.HotspotState, hsState.loaded = saved, loaded }(hsState.HotspotState, hsState.loaded)
	i18n.SetLang("en")
	var p app
	hsState.HotspotState = ui.HotspotState{Backend: "iwd", SSID: "Kario-1A2B", Passphrase: "abcdefgh23"}
	hsState.loaded = true
	s := p.hotspotScreen()
	if n := node(s, "hs_on"); n.Trailing != "Off" || n.Subtitle != "Shares the phone's connection with other devices over Wi-Fi" {
		t.Fatalf("off: %+v", n)
	}
	if node(s, "hs_name").Trailing != "Kario-1A2B" || node(s, "hs_pass").Trailing != "abcdefgh23" {
		t.Fatalf("name and password: %+v", s.Body)
	}
	if p.hotspotSummary() != "Off" {
		t.Fatalf("summary %q", p.hotspotSummary())
	}

	hsState.Enabled = true
	if n := node(p.hotspotScreen(), "hs_on"); n.Subtitle != "Turning on…" {
		t.Fatalf("switched, not started: %+v", n)
	}
	hsState.Started, hsState.Clients, hsState.Upstream = true, 2, "wwan0"
	s = p.hotspotScreen()
	if n := node(s, "hs_on"); n.Trailing != "On" || n.Subtitle != "Connected devices: 2" {
		t.Fatalf("on: %+v", n)
	}
	if !strings.Contains(texts(s), "use the phone's connection (wwan0)") || p.hotspotSummary() != "On · Kario-1A2B" {
		t.Fatalf("sharing: %q, summary %q", texts(s), p.hotspotSummary())
	}
	hsState.Upstream = ""
	if !strings.Contains(texts(p.hotspotScreen()), "Nothing to share yet") {
		t.Fatalf("nothing to share: %q", texts(p.hotspotScreen()))
	}
	p.wifi = ui.WifiState{Backend: "iwd"}
	if got := p.wifiSummary(); got != "Off while the personal hotspot is on" {
		t.Fatalf("Wi-Fi's line: %q", got)
	}

	hsState.HotspotState = ui.HotspotState{}
	if s := p.hotspotScreen(); len(s.Body) != 1 || !strings.Contains(texts(s), "no Wi-Fi") {
		t.Fatalf("no Wi-Fi: %+v", s.Body)
	}
	if pass := hotspotEdit("hotspotpass"); pass.Title != "Password" || node(pass, "v").Submit != "save" || node(pass, "v").Keyboard != "email" {
		t.Fatalf("password page: %+v", pass)
	}
}

// The options the access point has, the device limit, what the devices
// used and who they are; a phone whose access point cannot do more says
// so instead of offering it.
func TestHotspotOptionsPage(t *testing.T) {
	defer func(saved ui.HotspotState, loaded bool) { hsState.HotspotState, hsState.loaded = saved, loaded }(hsState.HotspotState, hsState.loaded)
	i18n.SetLang("en")
	var p app
	hsState.loaded = true
	hsState.HotspotState = ui.HotspotState{Backend: "sim", Enabled: true, Started: true, SSID: "Kario-1A2B", Passphrase: "abcdefgh23",
		Security: ui.HotspotWPA2WPA3, Band: ui.HotspotBand5, MaxClients: 3, Clients: 2,
		Securities: []string{ui.HotspotWPA2, ui.HotspotWPA2WPA3, ui.HotspotWPA3}, CanHide: true, Bands: []string{ui.HotspotBand24, ui.HotspotBand5},
		Devices: []ui.HotspotDevice{{MAC: "02:4b:41:52:48:01", Name: "Pixel 7", Address: "192.168.43.2", Since: 1700000000}, {MAC: "02:4b:41:52:48:02", Since: 1700000060}},
		Usage:   ui.HotspotUsage{SessionDown: 2048, SessionUp: 1024, TodayDown: 4096, TodayUp: 1024}}
	s := p.hotspotScreen()
	if n := node(s, "hs_security"); n.Trailing != "WPA2/WPA3" || node(s, "hs_pass").Subtitle != "WPA2/WPA3" {
		t.Fatalf("security: %+v", n)
	}
	if node(s, "hs_hidden").Trailing != "Off" || node(s, "hs_band").Trailing != "5 GHz" || node(s, "hs_max").Trailing != "3 devices" {
		t.Fatalf("options: %+v", s.Body)
	}
	if n := node(s, "hs_devices"); n.Subtitle != "2 of at most 3" {
		t.Fatalf("devices: %+v", n)
	}
	if n := node(s, "hs_usage"); n.Trailing != "3.0 KB" || n.Subtitle != "This session ↓2.0 KB ↑1.0 KB · Today ↓4.0 KB ↑1.0 KB" {
		t.Fatalf("usage: %+v", n)
	}
	d := p.hotspotDevicesScreen()
	if n := node(d, "hd:02:4b:41:52:48:01"); n.Title != "Pixel 7" || !strings.HasPrefix(n.Subtitle, "02:4b:41:52:48:01 · since ") || !strings.HasSuffix(n.Subtitle, " · 192.168.43.2") {
		t.Fatalf("a named device: %+v", n)
	}
	if n := node(d, "hd:02:4b:41:52:48:02"); n.Title != "Unnamed device" {
		t.Fatalf("a device without a name: %+v", n)
	}
	if cycle(hsState.Securities, ui.HotspotWPA3, false) != ui.HotspotWPA2 || cycle(hsState.Securities, ui.HotspotWPA2, true) != ui.HotspotWPA3 {
		t.Fatal("cycling the security")
	}
	hsState.HotspotState = ui.HotspotState{Backend: "iwd", SSID: "Kario-1A2B", Security: ui.HotspotWPA2, Band: ui.HotspotBand24,
		Securities: []string{ui.HotspotWPA2}, Bands: []string{ui.HotspotBand24}}
	s = p.hotspotScreen()
	for _, id := range []string{"hs_security", "hs_hidden", "hs_band"} {
		if node(s, id).ID != "" {
			t.Errorf("%s offered by an access point that cannot", id)
		}
	}
	if !strings.Contains(texts(s), "WPA2 only and cannot hide its name") || node(s, "hs_max").Trailing != "No limit" || node(s, "hs_devices").Subtitle != "The hotspot is off" {
		t.Fatalf("iwd: %q %+v", texts(s), s.Body)
	}
	if len(p.hotspotDevicesScreen().Body) != 2 {
		t.Fatal("devices while off")
	}
}
