package main

import (
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › VPN: the WireGuard profiles the system keeps (vpn.*). A
// profile comes from a .conf file in Files, picked in the system's list
// (the file and its keys go to the system alone), and may use the phone's
// own key pair, whose public key this page shows for the server's
// administrator. One profile is connected at a time. A profile's page
// makes it always on and blocks connections without it; Apps chooses
// which apps go through the tunnel. The pages refresh while they are open
// (the handshake, the bytes).

var vpnState struct {
	ui.VPNState
	loaded bool
}

// vpnTick starts the pages' refresh once.
var vpnTick sync.Once

func (p *app) loadVPN(then func()) {
	var st ui.VPNState
	p.a.Go(ui.CapVPNList, nil, &st, func(err error) {
		if err == nil {
			vpnState.VPNState, vpnState.loaded = st, true
		}
		if then != nil {
			then()
		}
	})
}

func vpnFind(id string) (ui.VPNProfile, bool) {
	for _, pr := range vpnState.Profiles {
		if pr.ID == id {
			return pr, true
		}
	}
	return ui.VPNProfile{}, false
}

func vpnSummary() string {
	switch {
	case !vpnState.loaded:
		return ""
	case vpnState.Backend == "":
		return tr.T("Not available on this phone")
	}
	for _, pr := range vpnState.Profiles {
		switch pr.State {
		case ui.VPNConnected:
			return tr.T("Connected to “%s”", pr.Name)
		case ui.VPNConnecting:
			return tr.T("Connecting to “%s”…", pr.Name)
		}
	}
	if len(vpnState.Profiles) == 0 {
		return tr.T("Not set up")
	}
	return tr.T("Off · %d profiles", len(vpnState.Profiles))
}

// ago is how long ago a moment was, roughly.
func ago(unix int64) string {
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return tr.T("%d s", max(0, int(d.Seconds())))
	case d < time.Hour:
		return tr.T("%d min", int(d.Minutes()))
	}
	return tr.T("%d h", int(d.Hours()))
}

func handshakeLabel(pr ui.VPNProfile) string {
	if pr.Handshake == 0 {
		return tr.T("No handshake yet")
	}
	return tr.T("Handshake %s ago", ago(pr.Handshake))
}

func vpnOn(pr ui.VPNProfile) bool {
	return pr.State == ui.VPNConnected || pr.State == ui.VPNConnecting
}

func vpnToggle(pr ui.VPNProfile) ui.Action {
	if vpnOn(pr) {
		a := ui.Action{ID: "toggle", Label: tr.T("Disconnect"), Primary: true}
		if pr.ID == vpnState.AlwaysOn {
			a.Confirm = tr.T("“%s” is always on. Disconnecting turns that off, and blocking connections without VPN with it.", pr.Name)
		}
		return a
	}
	return ui.Action{ID: "toggle", Label: tr.T("Connect"), Primary: true}
}

// appsSummary is which apps go through the tunnel, in a few words.
func appsSummary() string {
	n := 0
	for _, a := range vpnState.AppList {
		if a.Chosen {
			n++
		}
	}
	switch vpnState.Apps {
	case ui.VPNAppsOnly:
		return tr.T("Only %d chosen apps", n)
	case ui.VPNAppsExcept:
		return tr.T("All apps but %d", n)
	}
	return tr.T("All apps")
}

func vpnDelete(pr ui.VPNProfile, primary bool) ui.Action {
	return ui.Action{ID: "delete", Label: tr.T("Delete"), Key: "d", Primary: primary, Group: ui.GroupDanger,
		Confirm: tr.T("Delete the VPN “%s”? Its keys go with it.", pr.Name)}
}

func vpnStatus(pr ui.VPNProfile) string {
	switch pr.State {
	case ui.VPNConnected:
		return tr.T("Connected · %s · %s", strings.Join(pr.Address, ", "), handshakeLabel(pr))
	case ui.VPNConnecting:
		return tr.T("Connecting…")
	case ui.VPNDisconnecting:
		return tr.T("Disconnecting…")
	case "":
		if pr.Error != "" {
			return tr.T("Not connected: %s", pr.Error)
		}
	}
	if pr.FullTunnel {
		return tr.T("%s · all traffic", pr.Endpoint)
	}
	return tr.T("%s · some addresses", pr.Endpoint)
}

func (p *app) vpnScreen() ui.Screen {
	st := &vpnState
	s := ui.Screen{ID: "vpn", Title: "VPN"}
	if st.loaded && st.Backend == "" {
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("This phone cannot run a VPN: it has no systemd-networkd"))}
		return s
	}
	if st.Blocking {
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, tr.T("Connections without VPN are blocked: until the always-on VPN connects, the apps reach nothing")))
	}
	if len(st.Profiles) > 0 {
		s.Body = append(s.Body, sdk.Section(tr.T("Profiles")))
	}
	for _, pr := range st.Profiles {
		n := ui.Node{Kind: ui.KindItem, ID: "vp:" + pr.ID, Title: pr.Name, Subtitle: vpnStatus(pr), Trailing: onOff(vpnOn(pr)),
			Actions: []ui.Action{vpnToggle(pr), {ID: "details", Label: tr.T("Details"), Key: "i"}, vpnDelete(pr, false)}}
		if pr.ID == st.AlwaysOn {
			n.Badge = tr.T("Always on")
		}
		s.Body = append(s.Body, n)
	}
	if len(st.Profiles) > 0 {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "vpn-apps", Title: tr.T("Apps through the VPN"), Subtitle: appsSummary(),
			Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
	}
	key := ui.Node{Kind: ui.KindItem, ID: "vpn-key", Title: tr.T("This phone's key pair"), Subtitle: tr.T("For a server that wants the phone's public key first"),
		Actions: []ui.Action{{ID: "key", Label: tr.T("Make"), Primary: true}}}
	if st.PendingKey != "" {
		key.Subtitle, key.Actions[0].Label = tr.T("Public key %s…", st.PendingKey[:8]), tr.T("Show")
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Add a VPN")),
		ui.Node{Kind: ui.KindItem, ID: "vpn-import", Title: tr.T("Import a configuration file"), Subtitle: tr.T("A WireGuard .conf in Files, e.g. copied from a computer over USB"),
			Actions: []ui.Action{{ID: "import", Label: tr.T("Import"), Primary: true}}},
		key,
		sdk.Styled(ui.StyleDim, tr.T("WireGuard. While a VPN is connected, the apps' traffic goes through it: all of it when the profile's AllowedIPs take every address (0.0.0.0/0), else what goes to those addresses. “Wi-Fi only” in App permissions follows the network under the tunnel.")),
		sdk.Styled(ui.StyleDim, tr.T("A profile's Details make it always on and can block connections without it.")))
	if st.Backend == "sim" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Simulated: this machine has no systemd-networkd, so no tunnel is really made")))
	}
	return s
}

func (p *app) vpnProfileScreen(id string) ui.Screen {
	pr, ok := vpnFind(id)
	if !ok {
		return ui.Screen{ID: "vpnp:" + id, Title: "VPN", Body: []ui.Node{sdk.Styled(ui.StyleDim, tr.T("This VPN has been deleted"))}}
	}
	row := func(rid, title, value string) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: rid, Title: title, Subtitle: value}
	}
	s := ui.Screen{ID: "vpnp:" + id, Title: pr.Name}
	state := map[string]string{ui.VPNConnected: tr.T("Connected"), ui.VPNConnecting: tr.T("Connecting…"), ui.VPNDisconnecting: tr.T("Disconnecting…")}[pr.State]
	if state == "" {
		state = tr.T("Not connected")
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "status", Title: tr.T("Status"), Trailing: onOff(vpnOn(pr)), Subtitle: state,
		Actions: []ui.Action{vpnToggle(pr)}})
	if pr.State == "" && pr.Error != "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, pr.Error))
	}
	always := pr.ID == vpnState.AlwaysOn
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "always", Title: tr.T("Always-on VPN"), Trailing: onOff(always),
		Subtitle: tr.T("Connected all the time: again after a network change, a failure or a restart"),
		Actions:  []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}})
	if always {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "kill", Title: tr.T("Block connections without VPN"), Trailing: onOff(vpnState.KillSwitch),
			Subtitle: tr.T("The apps reach the network only through this VPN; while it is down they reach nothing"),
			Actions:  []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}})
	}
	s.Body = append(s.Body, row("address", tr.T("Address"), strings.Join(pr.Address, ", ")))
	if len(pr.DNS) > 0 {
		s.Body = append(s.Body, row("dns", "DNS", strings.Join(pr.DNS, ", ")))
	}
	server := pr.Endpoint
	if pr.Peers > 1 {
		server = tr.T("%s and %d more", pr.Endpoint, pr.Peers-1)
	}
	routes := strings.Join(pr.AllowedIPs, ", ")
	if pr.FullTunnel {
		routes = tr.T("All traffic (%s)", routes)
	}
	s.Body = append(s.Body, row("endpoint", tr.T("Server"), server), row("routes", tr.T("Through the tunnel"), routes))
	if pr.State == ui.VPNConnected {
		s.Body = append(s.Body, row("handshake", tr.T("Last handshake"), handshakeLabel(pr)),
			row("bytes", tr.T("Received · sent"), bytesLabel(pr.RxBytes)+" · "+bytesLabel(pr.TxBytes)))
	}
	s.Body = append(s.Body,
		ui.Node{Kind: ui.KindItem, ID: "pub", Title: tr.T("This phone's public key"), Subtitle: pr.PublicKey, Actions: []ui.Action{{ID: "copy", Label: tr.T("Copy"), Key: "c"}}},
		ui.Node{Kind: ui.KindItem, ID: "delete", Title: tr.T("Delete this VPN"), Actions: []ui.Action{vpnDelete(pr, true)}})
	return s
}

// vpnAppsScreen chooses which apps go through the tunnel: all of them,
// only the chosen ones, or all but them, and the apps.
func (p *app) vpnAppsScreen() ui.Screen {
	s := ui.Screen{ID: "vpnapps", Title: tr.T("Apps through the VPN")}
	for _, m := range []struct{ id, title, sub string }{
		{ui.VPNAppsAll, tr.T("All apps"), tr.T("And the system")},
		{ui.VPNAppsOnly, tr.T("Only the chosen apps"), tr.T("The others and the system go past the VPN")},
		{ui.VPNAppsExcept, tr.T("All but the chosen apps"), tr.T("The chosen apps go past the VPN")},
	} {
		n := ui.Node{Kind: ui.KindItem, ID: "apps:" + firstOf(m.id, "all"), Title: m.title, Subtitle: m.sub,
			Actions: []ui.Action{{ID: "choose", Label: tr.T("Use"), Primary: true}}}
		if m.id == vpnState.Apps {
			n.Badge = tr.T("Current")
		}
		s.Body = append(s.Body, n)
	}
	if vpnState.Apps != ui.VPNAppsAll {
		s.Body = append(s.Body, sdk.Section(tr.T("Apps")))
		for _, a := range vpnState.AppList {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "app:" + a.ID, Title: a.Name, Trailing: onOff(a.Chosen),
				Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}})
		}
		if len(vpnState.AppList) == 0 {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No app uses the network")))
		}
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("The VPN's DNS servers answer every app's names. Apps that go past the VPN still reach the network while connections without VPN are blocked.")))
	}
	return s
}

func (p *app) vpnKeyScreen() ui.Screen {
	s := ui.Screen{ID: "vpnkey", Title: tr.T("This phone's key pair")}
	s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("Give this public key to whoever runs the VPN server, then import the file they send back: a file without a PrivateKey uses this key pair. The private key never leaves the system."))}
	if k := vpnState.PendingKey; k != "" {
		s.Body = append(s.Body, sdk.Section(tr.T("Public key")), sdk.Text(k),
			ui.Node{Kind: ui.KindItem, ID: "copy", Title: tr.T("Copy the public key"), Actions: []ui.Action{{ID: "copy", Label: tr.T("Copy"), Primary: true}}})
	} else {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("A profile uses the last key pair: make a new one for the next server")))
	}
	renew := ui.Action{ID: "new", Label: tr.T("Make"), Primary: true}
	if vpnState.PendingKey != "" {
		renew.Confirm = tr.T("Make a new key pair? The one shown now is forgotten.")
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "new", Title: tr.T("Make a new key pair"), Actions: []ui.Action{renew}})
	return s
}

// vpnScreenByID builds one of these pages again (a restored process).
func (p *app) vpnScreenByID(id string) (ui.Screen, bool) {
	switch {
	case id == "vpn":
		return p.vpnScreen(), true
	case id == "vpnkey":
		return p.vpnKeyScreen(), true
	case id == "vpnapps":
		return p.vpnAppsScreen(), true
	case strings.HasPrefix(id, "vpnp:"):
		return p.vpnProfileScreen(strings.TrimPrefix(id, "vpnp:")), true
	}
	return ui.Screen{}, false
}

func (p *app) refreshVPN() {
	p.a.Update(p.vpnScreen())
	p.a.Update(p.vpnKeyScreen())
	p.a.Update(p.vpnAppsScreen())
	for _, id := range p.pages {
		if strings.HasPrefix(id, "vpnp:") {
			p.a.Update(p.vpnProfileScreen(strings.TrimPrefix(id, "vpnp:")))
		}
	}
	p.updateMenu()
}

func (p *app) vpnOpen() bool {
	for _, id := range p.pages {
		if id == "vpn" || id == "vpnapps" || strings.HasPrefix(id, "vpnp:") {
			return true
		}
	}
	return false
}

func (p *app) openVPN() {
	vpnTick.Do(func() {
		go func() {
			for range time.Tick(2 * time.Second) {
				p.a.Post(func() {
					if p.vpnOpen() {
						p.loadVPN(p.refreshVPN)
					}
				})
			}
		}()
	})
	p.loadVPN(func() { p.push(p.vpnScreen()) })
}

func (p *app) vpnKeygen(then func()) {
	var r struct {
		PublicKey string `json:"public_key"`
	}
	p.a.Go(ui.CapVPNKeygen, nil, &r, func(err error) {
		if err != nil {
			p.a.Toast(err.Error())
		}
		p.loadVPN(func() {
			p.refreshVPN()
			if err == nil && then != nil {
				then()
			}
		})
	})
}

func (p *app) vpnImport() {
	var pr ui.VPNProfile
	p.a.Go(ui.CapVPNImport, nil, &pr, func(err error) {
		switch {
		case err != nil && strings.HasSuffix(err.Error(), ui.ErrVPNNeedsKey):
			p.a.Toast(tr.T("The file has no private key: give the server this phone's public key, then import the file it sends"))
			p.vpnKeygen(func() { p.push(p.vpnKeyScreen()) })
			return
		case err != nil && !ui.Cancelled(err):
			p.a.Toast(err.Error())
		case err == nil:
			p.a.Toast(tr.T("Imported “%s”", pr.Name))
		}
		p.loadVPN(p.refreshVPN)
	})
}

// vpnSwitch connects or disconnects a profile; the switch shows the new
// state at once, before the system's answer.
func (p *app) vpnSwitch(id string) {
	pr, ok := vpnFind(id)
	if !ok {
		return
	}
	capName, next := ui.CapVPNConnect, ui.VPNConnecting
	if vpnOn(pr) {
		capName, next = ui.CapVPNDisconnect, ui.VPNDisconnecting
	}
	for i := range vpnState.Profiles {
		if vpnState.Profiles[i].ID == id {
			vpnState.Profiles[i].State, vpnState.Profiles[i].Error = next, ""
		}
	}
	p.refreshVPN()
	p.a.Go(capName, ui.VPNArgs{ID: id}, nil, func(err error) {
		if err != nil && !ui.Cancelled(err) {
			p.a.Toast(err.Error())
		}
		p.loadVPN(p.refreshVPN)
	})
}

// vpnSet changes always-on, blocking or the apps; the pages show the
// system's answer.
func (p *app) vpnSet(args ui.VPNArgs) {
	var st ui.VPNState
	p.a.Go(ui.CapVPNSet, args, &st, func(err error) {
		if err != nil {
			p.a.Toast(err.Error())
			p.loadVPN(p.refreshVPN)
			return
		}
		vpnState.VPNState, vpnState.loaded = st, true
		p.refreshVPN()
	})
}

func (p *app) vpnCopy(key string) {
	p.a.Go(ui.CapClipboardSet, ui.ClipArgs{Text: key}, nil, func(err error) {
		if err != nil {
			p.a.Toast(err.Error())
			return
		}
		p.a.Toast(tr.T("Copied the public key"))
	})
}

// vpnAction handles the VPN pages; false for the rest.
func (p *app) vpnAction(e sdk.ActionEvent) bool {
	id := strings.TrimPrefix(e.Node, "vp:")
	if strings.HasPrefix(e.Screen, "vpnp:") {
		id = strings.TrimPrefix(e.Screen, "vpnp:")
	}
	switch {
	case onMenu(e.Screen) && e.Node == "vpn":
		p.openVPN()
	case e.Screen == "vpn" && e.Node == "vpn-import":
		p.vpnImport()
	case e.Screen == "vpn" && e.Node == "vpn-key":
		if vpnState.PendingKey != "" {
			p.push(p.vpnKeyScreen())
		} else {
			p.vpnKeygen(func() { p.push(p.vpnKeyScreen()) })
		}
	case strings.HasPrefix(e.Screen, "vpnp:") && e.Node == "always":
		on := id != vpnState.AlwaysOn
		p.vpnSet(ui.VPNArgs{ID: id, AlwaysOn: &on})
	case strings.HasPrefix(e.Screen, "vpnp:") && e.Node == "kill":
		on := !vpnState.KillSwitch
		p.vpnSet(ui.VPNArgs{KillSwitch: &on})
	case e.Screen == "vpn" && e.Node == "vpn-apps":
		p.push(p.vpnAppsScreen())
	case e.Screen == "vpnapps" && strings.HasPrefix(e.Node, "apps:"):
		mode := strings.TrimPrefix(e.Node, "apps:")
		if mode == "all" {
			mode = ui.VPNAppsAll
		}
		p.vpnSet(ui.VPNArgs{Apps: &mode})
	case e.Screen == "vpnapps" && strings.HasPrefix(e.Node, "app:"):
		app := strings.TrimPrefix(e.Node, "app:")
		chosen := true
		for _, a := range vpnState.AppList {
			if a.ID == app {
				chosen = !a.Chosen
			}
		}
		p.vpnSet(ui.VPNArgs{App: app, Chosen: &chosen})
	case e.Screen == "vpnkey" && e.Action == "copy":
		p.vpnCopy(vpnState.PendingKey)
	case e.Screen == "vpnkey" && e.Action == "new":
		p.vpnKeygen(nil)
	case (e.Screen == "vpn" || strings.HasPrefix(e.Screen, "vpnp:")) && e.Action == "toggle":
		p.vpnSwitch(id)
	case e.Screen == "vpn" && e.Action == "details":
		p.push(p.vpnProfileScreen(id))
	case strings.HasPrefix(e.Screen, "vpnp:") && e.Action == "copy":
		if pr, ok := vpnFind(id); ok {
			p.vpnCopy(pr.PublicKey)
		}
	case (e.Screen == "vpn" || strings.HasPrefix(e.Screen, "vpnp:")) && e.Action == "delete":
		name := ""
		if pr, ok := vpnFind(id); ok {
			name = pr.Name
		}
		p.a.Go(ui.CapVPNDelete, ui.VPNArgs{ID: id}, nil, func(err error) {
			if err != nil {
				p.a.Toast(err.Error())
			} else {
				p.a.Toast(tr.T("Deleted the VPN “%s”", name))
				if strings.HasPrefix(e.Screen, "vpnp:") {
					p.a.Pop()
				}
			}
			p.loadVPN(p.refreshVPN)
		})
	default:
		return false
	}
	return true
}
