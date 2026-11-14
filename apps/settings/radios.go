package main

import (
	"strings"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Wi-Fi and Bluetooth: the radios the system drives (wifi.*,
// bt.*). A new protected network asks for its passphrase on a page of its
// own; pairing is confirmed in the system's dialog with the code the other
// device shows.

var bars = []string{"▁", "▂", "▄", "▆", "█"}

func (p *app) loadRadios(then func()) {
	p.a.Go(ui.CapWifiState, nil, &p.wifi, func(error) {
		p.a.Go(ui.CapBTState, nil, &p.bt, func(error) {
			p.a.Go(ui.CapRadioState, nil, &p.radio, func(error) { then() })
		})
	})
}

// Airplane mode and mobile data: switches on the root page, as on a
// feature phone; the power key's menu has airplane mode too.
func (p *app) airplaneItem() ui.Node {
	return ui.Node{Kind: ui.KindItem, ID: "airplane", Title: tr.T("Airplane mode"), Trailing: onOff(p.radio.Airplane),
		Subtitle: tr.T("Turns off the mobile network, Wi-Fi and Bluetooth; emergency calls still go through"),
		Actions:  []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}}
}

func (p *app) mobileDataItem() ui.Node {
	sub := tr.T("Internet over the mobile network")
	switch {
	case p.radio.Airplane:
		sub = tr.T("Off while airplane mode is on")
	case p.radio.MobileData && p.radio.Data == "connected":
		sub = tr.T("Connected")
	}
	return ui.Node{Kind: ui.KindItem, ID: "mobiledata", Title: tr.T("Mobile data"), Subtitle: sub, Trailing: onOff(p.radio.MobileData),
		Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}}
}

// eapMethods are the enterprise page's choices: WifiArgs.EAP and the label.
var eapMethods = []struct{ id, label, inner string }{
	{"peap", "PEAP", "MSCHAPv2"}, {"ttls-pap", "TTLS", "PAP"}, {"ttls", "TTLS", "MSCHAPv2"},
}

func (p *app) wifiSummary() string {
	switch {
	case p.wifi.Backend == "":
		return tr.T("No Wi-Fi")
	case !p.wifi.Enabled:
		return tr.T("Off")
	}
	for _, n := range p.wifi.Networks {
		if n.Connected {
			return tr.T("Connected to “%s”", n.SSID)
		}
	}
	return tr.T("On · not connected")
}

func (p *app) btSummary() string {
	switch {
	case p.bt.Backend == "":
		return tr.T("No Bluetooth")
	case !p.bt.Enabled:
		return tr.T("Off")
	}
	for _, d := range p.bt.Devices {
		if d.Connected {
			return tr.T("Connected to “%s”", d.Name)
		}
	}
	return tr.T("On")
}

func onOff(on bool) string { return map[bool]string{true: tr.T("On"), false: tr.T("Off")}[on] }

func (p *app) wifiScreen() ui.Screen {
	w := p.wifi
	s := ui.Screen{ID: "wifi", Title: "Wi-Fi"}
	if w.Backend == "" {
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("This device has no Wi-Fi"))}
		return s
	}
	s.Body = []ui.Node{{Kind: ui.KindItem, ID: "power", Title: "Wi-Fi", Trailing: onOff(w.Enabled),
		Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}}}
	if !w.Enabled {
		return s
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Networks")))
	for _, n := range w.Networks {
		sub := map[string]string{"open": tr.T("Unsecured"), "psk": tr.T("Secured"), "8021x": tr.T("Enterprise network")}[n.Security]
		switch {
		case n.Connected:
			sub = tr.T("Connected")
			if w.Address != "" {
				sub += " · " + w.Address
			}
		case n.Known:
			sub = tr.T("Saved · %s", sub)
		}
		node := ui.Node{Kind: ui.KindItem, ID: "net:" + n.SSID, Title: n.SSID, Subtitle: sub, Trailing: bars[min(max(n.Signal, 0), 4)]}
		if !n.Connected {
			node.Actions = append(node.Actions, ui.Action{ID: "join", Label: tr.T("Connect"), Primary: true})
		}
		if n.Known {
			node.Actions = append(node.Actions, ui.Action{ID: "forget", Label: tr.T("Forget network"), Key: "d", Group: ui.GroupDanger})
		}
		s.Body = append(s.Body, node)
	}
	if len(w.Networks) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Searching for networks…")))
	}
	return s
}

func passScreen(ssid string) ui.Screen {
	return ui.Screen{ID: "wifipass:" + ssid, Title: tr.T("Connect to “%s”", ssid), Body: []ui.Node{
		sdk.Styled(ui.StyleDim, tr.T("Enter the network's password. Only the system keeps it, to reconnect on its own.")),
		{Kind: ui.KindInput, ID: "pass", Placeholder: tr.T("Password"), Secret: true, Submit: "join"},
	}}
}

// eapScreen joins an enterprise (802.1X) network: the user's name and
// password, and the domain the server's certificate must name (as Android
// asks for it: without it any server could collect the password).
func (p *app) eapScreen(ssid string) ui.Screen {
	m := eapMethods[p.eapMethod%len(eapMethods)]
	method := m.label + " · " + m.inner
	return ui.Screen{ID: "wifieap:" + ssid, Title: tr.T("Connect to “%s”", ssid), Body: []ui.Node{
		sdk.Styled(ui.StyleDim, tr.T("An enterprise network: your user name and password, and the domain of its server (your IT department gives it, e.g. radius.example.com). Only the system keeps them.")),
		{Kind: ui.KindInput, ID: "identity", Placeholder: tr.T("User name"), Keyboard: "email"},
		{Kind: ui.KindInput, ID: "pass", Placeholder: tr.T("Password"), Secret: true},
		{Kind: ui.KindInput, ID: "domain", Placeholder: tr.T("Server domain"), Keyboard: "email"},
		{Kind: ui.KindItem, ID: "eap", Title: tr.T("Method"), Trailing: method, Subtitle: tr.T("The server's certificate is checked against the system's"),
			Actions: []ui.Action{{ID: "cycle", Label: tr.T("Change"), Primary: true}}},
		sdk.Button("join", tr.T("Connect"), "join"),
	}}
}

func (p *app) btScreen() ui.Screen {
	b := p.bt
	s := ui.Screen{ID: "bt", Title: tr.T("Bluetooth")}
	if b.Backend == "" {
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("This device has no Bluetooth"))}
		return s
	}
	s.Body = []ui.Node{{Kind: ui.KindItem, ID: "power", Title: tr.T("Bluetooth"), Trailing: onOff(b.Enabled), Subtitle: tr.T("Device name: %s", b.Name),
		Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}}}
	if !b.Enabled {
		return s
	}
	scan := ui.Node{Kind: ui.KindItem, ID: "scan", Title: tr.T("Search for devices"), Actions: []ui.Action{{ID: "scan", Label: tr.T("Search"), Primary: true}}}
	if b.Discovering {
		scan.Title, scan.Subtitle, scan.Actions[0].Label = tr.T("Stop searching"), tr.T("Searching for nearby devices…"), tr.T("Stop")
	}
	s.Body = append(s.Body, scan)
	var paired, near []ui.Node
	for _, d := range b.Devices {
		n := ui.Node{Kind: ui.KindItem, ID: "dev:" + d.Address, Title: d.Name, Subtitle: d.Address}
		if d.Paired {
			n.Subtitle = map[bool]string{true: tr.T("Connected"), false: tr.T("Paired")}[d.Connected]
			book := ui.Action{ID: "phonebook", Label: tr.T("Share contacts and call log"), Key: "c"}
			switch d.Phonebook {
			case "allowed":
				n.Subtitle += " · " + tr.T("Reads your contacts")
				book.Label = tr.T("Stop sharing contacts")
			case "denied":
				n.Subtitle += " · " + tr.T("No access to contacts")
			}
			n.Actions = []ui.Action{{ID: "send", Label: tr.T("Send a file"), Primary: true}, book,
				{ID: "forget", Label: tr.T("Unpair"), Key: "d", Group: ui.GroupDanger}}
			paired = append(paired, n)
		} else {
			n.Actions = []ui.Action{{ID: "pair", Label: tr.T("Pair"), Primary: true}}
			near = append(near, n)
		}
	}
	if len(paired) > 0 {
		s.Body = append(append(s.Body, sdk.Section(tr.T("Paired devices"))), paired...)
	}
	if b.Discovering {
		s.Body = append(append(s.Body, sdk.Section(tr.T("Nearby devices"))), near...)
		if len(near) == 0 {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No devices found yet")))
		}
	}
	return s
}

// radioAction handles what the Wi-Fi and Bluetooth pages do; false for the
// rest.
func (p *app) radioAction(e sdk.ActionEvent) bool {
	a := p.a
	after := func(err error) {
		if err != nil && !ui.Cancelled(err) {
			a.Toast(err.Error())
		}
		p.loadRadios(p.refresh)
	}
	switch {
	case e.Screen == "root" && e.Node == "airplane":
		on := !p.radio.Airplane
		a.Go(ui.CapRadioSet, ui.RadioArgs{Airplane: &on}, &p.radio, after)
	case e.Screen == "root" && e.Node == "mobiledata":
		on := !p.radio.MobileData
		a.Go(ui.CapRadioSet, ui.RadioArgs{MobileData: &on}, &p.radio, after)
	case e.Screen == "root" && (e.Node == "wifi" || e.Node == "bt"):
		p.radioPage = e.Node
		p.loadRadios(func() {
			if e.Node == "wifi" {
				a.Push(p.wifiScreen())
			} else {
				a.Push(p.btScreen())
			}
		})
	case e.Screen == "wifi" && e.Node == "power":
		on := !p.wifi.Enabled
		a.Go(ui.CapWifiSet, ui.WifiArgs{Enabled: &on}, nil, after)
	case e.Screen == "wifi" && e.Action == "join":
		ssid := strings.TrimPrefix(e.Node, "net:")
		a.Toast(tr.T("Connecting to “%s”", ssid))
		a.Go(ui.CapWifiConnect, ui.WifiArgs{SSID: ssid}, nil, func(err error) {
			if err != nil && strings.HasSuffix(err.Error(), ui.ErrNeedPassphrase) {
				a.Push(passScreen(ssid))
				return
			}
			if err != nil && strings.HasSuffix(err.Error(), ui.ErrNeedCredentials) {
				p.eapMethod = 0
				a.Push(p.eapScreen(ssid))
				return
			}
			after(err)
		})
	case e.Screen == "wifi" && e.Action == "forget":
		a.Go(ui.CapWifiForget, ui.WifiArgs{SSID: strings.TrimPrefix(e.Node, "net:")}, nil, after)
	case strings.HasPrefix(e.Screen, "wifipass:") && e.Action == "join":
		ssid := strings.TrimPrefix(e.Screen, "wifipass:")
		a.Toast(tr.T("Connecting to “%s”", ssid))
		a.Go(ui.CapWifiConnect, ui.WifiArgs{SSID: ssid, Passphrase: e.Value}, nil, func(err error) {
			if err == nil {
				a.Pop()
			}
			after(err)
		})
	case strings.HasPrefix(e.Screen, "wifieap:") && e.Action == "cycle":
		p.eapMethod = (p.eapMethod + 1) % len(eapMethods)
		a.Update(p.eapScreen(strings.TrimPrefix(e.Screen, "wifieap:")))
	case strings.HasPrefix(e.Screen, "wifieap:") && e.Action == "join":
		ssid := strings.TrimPrefix(e.Screen, "wifieap:")
		eap := eapMethods[p.eapMethod%len(eapMethods)].id
		a.Toast(tr.T("Connecting to “%s”", ssid))
		a.Go(ui.CapWifiConnect, ui.WifiArgs{SSID: ssid, Identity: strings.TrimSpace(e.Fields["identity"]), Passphrase: e.Fields["pass"],
			Domain: strings.TrimSpace(e.Fields["domain"]), EAP: eap}, nil, func(err error) {
			if err == nil {
				a.Pop()
			}
			after(err)
		})
	case e.Screen == "bt" && e.Node == "power":
		on := !p.bt.Enabled
		a.Go(ui.CapBTSet, ui.BTArgs{Enabled: &on}, nil, after)
	case e.Screen == "bt" && e.Node == "scan":
		on := !p.bt.Discovering
		a.Go(ui.CapBTSet, ui.BTArgs{Discover: &on}, nil, after)
	case e.Screen == "bt" && e.Action == "pair":
		a.Go(ui.CapBTPair, ui.BTArgs{Address: strings.TrimPrefix(e.Node, "dev:")}, nil, after)
	case e.Screen == "bt" && e.Action == "forget":
		a.Go(ui.CapBTForget, ui.BTArgs{Address: strings.TrimPrefix(e.Node, "dev:")}, nil, after)
	case e.Screen == "bt" && e.Action == "send":
		// The system lists the files and sends the one the user picks.
		a.Go(ui.CapBTSend, ui.BTArgs{Address: strings.TrimPrefix(e.Node, "dev:")}, nil, after)
	case e.Screen == "bt" && e.Action == "phonebook":
		addr := strings.TrimPrefix(e.Node, "dev:")
		allow := true
		for _, d := range p.bt.Devices {
			if d.Address == addr {
				allow = d.Phonebook != "allowed"
			}
		}
		a.Go(ui.CapBTSet, ui.BTArgs{Address: addr, Phonebook: &allow}, nil, after)
	default:
		return false
	}
	return true
}
