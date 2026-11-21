package main

import (
	"strings"
	"testing"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ui"
)

// Always-on and blocking on the profile's page (blocking only for the
// always-on one), the warning while it blocks, and the apps' page.
func TestVPNPolicyPages(t *testing.T) {
	defer func(saved ui.VPNState, loaded bool) { vpnState.VPNState, vpnState.loaded = saved, loaded }(vpnState.VPNState, vpnState.loaded)
	i18n.SetLang("en")
	var p app
	vpnState.loaded = true
	vpnState.VPNState = ui.VPNState{Backend: "sim", AlwaysOn: "v1", KillSwitch: true, Blocking: true, Apps: ui.VPNAppsExcept,
		Profiles: []ui.VPNProfile{{ID: "v1", Name: "office", Endpoint: "192.0.2.77:51820", FullTunnel: true, State: ui.VPNConnecting}, {ID: "v2", Name: "home", Endpoint: "192.0.2.78:51820"}},
		AppList:  []ui.VPNApp{{ID: "com.phone.browser", Name: "Browser", Chosen: true}, {ID: "com.phone.store", Name: "Store"}}}
	s := p.vpnScreen()
	if !strings.Contains(texts(s), "Connections without VPN are blocked") {
		t.Fatalf("no warning: %q", texts(s))
	}
	if node(s, "vp:v1").Badge != "Always on" || node(s, "vp:v2").Badge != "" {
		t.Fatalf("badges: %+v", s.Body)
	}
	if n := node(s, "vpn-apps"); n.Subtitle != "All apps but 1" {
		t.Fatalf("apps: %+v", n)
	}
	if a := vpnToggle(vpnState.Profiles[0]); a.Label != "Disconnect" || !strings.Contains(a.Confirm, "always on") {
		t.Fatalf("disconnecting the always-on profile: %+v", a)
	}
	office := p.vpnProfileScreen("v1")
	if node(office, "always").Trailing != "On" || node(office, "kill").Trailing != "On" {
		t.Fatalf("office: %+v", office.Body)
	}
	home := p.vpnProfileScreen("v2")
	if node(home, "always").Trailing != "Off" || node(home, "kill").ID != "" {
		t.Fatalf("home: %+v", home.Body)
	}
	apps := p.vpnAppsScreen()
	if node(apps, "apps:except").Badge != "Current" || node(apps, "apps:all").Badge != "" || node(apps, "app:com.phone.browser").Trailing != "On" || node(apps, "app:com.phone.store").Trailing != "Off" {
		t.Fatalf("apps page: %+v", apps.Body)
	}
	vpnState.Apps, vpnState.Blocking = ui.VPNAppsAll, false
	if apps := p.vpnAppsScreen(); node(apps, "app:com.phone.store").ID != "" || node(apps, "apps:all").Badge != "Current" {
		t.Fatalf("all apps: %+v", apps.Body)
	}
	if strings.Contains(texts(p.vpnScreen()), "blocked") || appsSummary() != "All apps" {
		t.Fatal("all apps, not blocking")
	}
}
