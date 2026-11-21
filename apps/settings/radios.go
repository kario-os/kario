package main

import (
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Wi-Fi and Bluetooth: the radios the system drives (wifi.*,
// bt.*). A new protected network asks for its passphrase on a page of its
// own; pairing is confirmed in the system's dialog with the code the other
// device shows. A saved network is metered automatically (the system
// detects a phone's hotspot) or as the user says; on a metered one large
// background downloads wait.

var bars = []string{"▁", "▂", "▄", "▆", "█"}

func (p *app) loadRadios(then func()) {
	p.radioSeq++
	seq, gen, idle := p.radioSeq, p.radioGen, p.radioBusy == 0
	var wifi ui.WifiState
	var bt ui.BTState
	var radio ui.RadioState
	p.a.Go(ui.CapWifiState, nil, &wifi, func(werr error) {
		p.a.Go(ui.CapBTState, nil, &bt, func(berr error) {
			p.a.Go(ui.CapRadioState, nil, &radio, func(rerr error) {
				if idle && gen == p.radioGen && seq > p.radioShown {
					p.radioShown = seq
					if werr == nil {
						p.wifi = wifi
					}
					if berr == nil {
						p.bt = bt
					}
					if rerr == nil {
						p.radio = radio
					}
				}
				p.pairSearch()
				p.loadHotspot(then)
			})
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
	case p.radio.LimitHeld:
		sub = tr.T("Off: this cycle's data limit is used up")
	case p.radio.MobileData && p.radio.Data == "roaming":
		sub = tr.T("Off while roaming abroad: data roaming is off")
	case p.radio.MobileData && p.radio.Data == "connected":
		sub = strings.Join(slices.DeleteFunc([]string{tr.T("Connected"), p.radio.APN, p.radio.Address}, func(s string) bool { return s == "" }), " · ")
	}
	toggle := ui.Action{ID: "toggle", Label: tr.T("Switch"), Primary: true}
	if p.radio.LimitHeld {
		toggle.Confirm = tr.T("Turn mobile data on anyway? This cycle's data limit is used up; it stays on until the cycle ends or the limit is raised.")
	}
	return ui.Node{Kind: ui.KindItem, ID: "mobiledata", Title: tr.T("Mobile data"), Subtitle: sub, Trailing: onOff(p.radio.MobileData),
		Actions: []ui.Action{toggle}}
}

// mobileTick refreshes the radios' state every 2 seconds while the Mobile
// network page or the APN page is open: roaming and the data link change
// by themselves.
var mobileTick sync.Once

func (p *app) watchMobile() {
	if p.a == nil {
		return
	}
	mobileTick.Do(func() {
		go func() {
			for range time.Tick(2 * time.Second) {
				p.a.Post(func() {
					if slices.Contains(p.pages, groupPrefix+"mobile") || slices.Contains(p.pages, "apn") {
						p.a.Go(ui.CapRadioState, nil, &p.radio, func(error) { p.updateMenu() })
					}
				})
			}
		}()
	})
}

// Data roaming: off by default; turning it on warns of the charges first.
// National roaming (another network of the SIM's country) needs no switch.
func (p *app) roamingItem() ui.Node {
	p.watchMobile()
	sub := tr.T("Mobile data on other countries' networks")
	switch {
	case p.radio.Roaming == "international" && p.radio.DataRoaming:
		sub = tr.T("Roaming abroad now · mobile data on")
	case p.radio.Roaming == "international":
		sub = tr.T("Roaming abroad now · mobile data off")
	case p.radio.Roaming == "national":
		sub = tr.T("National roaming now: mobile data works as at home")
	}
	toggle := ui.Action{ID: "toggle", Label: tr.T("Switch"), Primary: true}
	if !p.radio.DataRoaming {
		toggle.Confirm = tr.T("Turn on data roaming? Abroad, apps then use mobile data over other carriers' networks, and roaming charges can be high.")
	}
	return ui.Node{Kind: ui.KindItem, ID: "roaming", Title: tr.T("Data roaming"), Subtitle: sub, Trailing: onOff(p.radio.DataRoaming), Actions: []ui.Action{toggle}}
}

// eapMethods are the enterprise page's choices: WifiArgs.EAP and the label.
var eapMethods = []struct{ id, label, inner string }{
	{"peap", "PEAP", "MSCHAPv2"}, {"ttls-pap", "TTLS", "PAP"}, {"ttls", "TTLS", "MSCHAPv2"},
}

func (p *app) wifiSummary() string {
	switch {
	case p.wifi.Backend == "":
		return tr.T("No Wi-Fi")
	case !p.wifi.Enabled && hsState.Enabled:
		return tr.T("Off while the personal hotspot is on")
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
		note := meteredNote(n)
		switch {
		case n.Connected:
			sub = tr.T("Connected")
			if note != "" {
				sub += " · " + note
			} else if w.Address != "" {
				sub += " · " + w.Address
			}
		case n.Known:
			sub = tr.T("Saved · %s", sub)
			if note != "" {
				sub += " · " + note
			}
		}
		node := ui.Node{Kind: ui.KindItem, ID: "net:" + n.SSID, Title: n.SSID, Subtitle: sub, Trailing: bars[min(max(n.Signal, 0), 4)]}
		if !n.Connected {
			node.Actions = append(node.Actions, ui.Action{ID: "join", Label: tr.T("Connect"), Primary: true})
		}
		if n.Known || n.Connected {
			node.Actions = append(node.Actions, ui.Action{ID: "metered", Label: tr.T(meteredLabel[n.MeteredChoice]), Key: "m"})
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

// meteredLabel is the metered action's label by the network's choice, and
// meteredNext the choice it switches to.
var (
	meteredLabel = map[string]string{"": "Metered: automatic", ui.MeteredYes: "Metered: yes", ui.MeteredNo: "Metered: no"}
	meteredNext  = map[string]string{"": ui.MeteredYes, ui.MeteredYes: ui.MeteredNo, ui.MeteredNo: ui.MeteredAuto}
)

// meteredNote says whether a network counts as metered, and why.
func meteredNote(n ui.WifiNet) string {
	switch {
	case n.MeteredChoice == ui.MeteredNo:
		return tr.T("Not metered (your choice)")
	case !n.Metered:
		return ""
	case n.MeteredChoice == ui.MeteredYes:
		return tr.T("Metered (your choice)")
	case n.MeteredHint == ui.MeteredHintDHCP:
		return tr.T("Metered: a phone's hotspot")
	}
	return tr.T("Metered: looks like a hotspot")
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
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "pairnew", Title: tr.T("Pair new device"), Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
	var paired []ui.Node
	for _, d := range b.Devices {
		if !d.Paired {
			continue
		}
		n := ui.Node{Kind: ui.KindItem, ID: "dev:" + d.Address, Title: d.Name}
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
	}
	if len(paired) > 0 {
		s.Body = append(append(s.Body, sdk.Section(tr.T("Paired devices"))), paired...)
	}
	return s
}

// Settings › Bluetooth › Pair new device searches for devices the way
// Android's page to pair one does (BluetoothDevicePairingDetailBase): while
// it is on top in the foreground, not while the device picked is being
// paired; a pairing that fails searches again, and one that succeeds
// closes the page. Opening it turns Bluetooth on if it is off. A system
// dialog or menu over the page (the palette, the pairing code) leaves the
// search on, as Android's dialogs do, or the devices found would vanish
// under it; the system stops the search when the screen goes off (and
// when the app leaves the front), so the keys coming back ask again.

// btPair is the page's state: open (until it is popped), the address
// being paired, the search last asked for, the app in the foreground.
type btPair struct {
	open      bool
	pairing   string
	searching bool
	front     bool
}

const btPairPage = "btpair"

func (p *app) btPairScreen() ui.Screen {
	b := p.bt
	s := ui.Screen{ID: btPairPage, Title: tr.T("Pair new device")}
	switch {
	case !b.Enabled:
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("Turning Bluetooth on…"))}
		return s
	case p.pair.pairing != "":
		s.Body = []ui.Node{sdk.Styled(ui.StyleAccent, tr.T("Pairing with “%s”…", p.btName(p.pair.pairing)))}
	case p.pair.searching || b.Discovering:
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("Searching for nearby devices…"))}
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Nearby devices")))
	near := 0
	for _, d := range b.Devices {
		if d.Paired {
			continue
		}
		n := ui.Node{Kind: ui.KindItem, ID: "dev:" + d.Address, Title: d.Name, Subtitle: d.Address}
		if p.pair.pairing == "" {
			n.Actions = []ui.Action{{ID: "pair", Label: tr.T("Pair"), Primary: true}}
		}
		s.Body = append(s.Body, n)
		near++
	}
	if near == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No devices found yet")))
	}
	return s
}

func (p *app) btName(addr string) string {
	for _, d := range p.bt.Devices {
		if d.Address == addr {
			return d.Name
		}
	}
	return addr
}

// openPair opens Pair new device and starts searching.
func (p *app) openPair() {
	p.pair.open = true
	p.a.Push(p.btPairScreen())
	if !p.bt.Enabled && p.pair.front {
		on := true
		p.bt.Enabled, p.pair.searching = true, true
		p.a.Go(ui.CapBTSet, ui.BTArgs{Enabled: &on, Discover: &on}, nil, p.searched(true))
		return
	}
	p.pairSearch()
}

// pairSearch asks for the search the page wants now, when that changed.
func (p *app) pairSearch() {
	want := p.pair.open && p.a.Top() == btPairPage && p.pair.front && p.pair.pairing == "" && p.bt.Enabled
	if want == p.pair.searching {
		return
	}
	p.pair.searching = want
	p.a.Go(ui.CapBTSet, ui.BTArgs{Discover: &want}, nil, p.searched(want))
}

// searched: a search that did not start is asked for again at the next
// refresh; neither way is the user told (the page shows what the system
// does).
func (p *app) searched(on bool) func(error) {
	return func(err error) {
		if err != nil {
			log.Printf("bluetooth search %v: %v", on, err)
			if on && p.pair.searching {
				p.pair.searching = false
			}
		}
		p.loadRadios(p.refresh)
	}
}

// btFront, btFocus and btPopped follow the app's turn: the foreground,
// the keys back from a system dialog or the screen off, the pages popped.
func (p *app) btFront(on bool) {
	p.pair.front = on
	p.pairSearch()
}

func (p *app) btFocus(lost bool) {
	if !lost {
		p.pair.searching = false
		p.pairSearch()
	}
}

func (p *app) btPopped(id string) {
	if id == btPairPage {
		p.pair.open = false
	}
	p.pairSearch()
}

// pairDevice pairs the device picked, the search stopped meanwhile.
func (p *app) pairDevice(addr string) {
	p.pair.pairing = addr
	p.pairSearch()
	p.a.Update(p.btPairScreen())
	p.a.Go(ui.CapBTPair, ui.BTArgs{Address: addr}, nil, func(err error) {
		p.pair.pairing = ""
		if err == nil && p.pair.open && p.a.Top() == btPairPage {
			p.pair.open = false
			p.a.Pop()
		}
		if err != nil && !ui.Cancelled(err) {
			p.a.Toast(err.Error())
		}
		p.pairSearch()
		p.loadRadios(p.refresh)
	})
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
	switched := func(err error) {
		p.radioBusy--
		after(err)
	}
	// The switches are kept at once, as the volumes are: a second press
	// before the system's answer switches back.
	switch {
	case onMenu(e.Screen) && e.Node == "airplane":
		on := !p.radio.Airplane
		p.radio.Airplane = on
		a.Go(ui.CapRadioSet, ui.RadioArgs{Airplane: &on}, &p.radio, after)
	case onMenu(e.Screen) && e.Node == "mobiledata":
		on := !p.radio.MobileData
		p.radio.MobileData = on
		a.Go(ui.CapRadioSet, ui.RadioArgs{MobileData: &on}, &p.radio, after)
	case onMenu(e.Screen) && e.Node == "roaming":
		on := !p.radio.DataRoaming
		p.radio.DataRoaming = on
		a.Go(ui.CapRadioSet, ui.RadioArgs{DataRoaming: &on}, &p.radio, after)
	case onMenu(e.Screen) && (e.Node == "wifi" || e.Node == "bt"):
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
		p.wifi.Enabled = on
		p.radioGen++
		p.radioBusy++
		a.Update(p.wifiScreen())
		a.Go(ui.CapWifiSet, ui.WifiArgs{Enabled: &on}, nil, switched)
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
	case e.Screen == "wifi" && e.Action == "metered":
		ssid := strings.TrimPrefix(e.Node, "net:")
		choice := ""
		for _, n := range p.wifi.Networks {
			if n.SSID == ssid {
				choice = n.MeteredChoice
			}
		}
		a.Go(ui.CapWifiSet, ui.WifiArgs{SSID: ssid, Metered: meteredNext[choice]}, nil, after)
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
		p.bt.Enabled = on
		p.radioGen++
		p.radioBusy++
		a.Update(p.btScreen())
		a.Go(ui.CapBTSet, ui.BTArgs{Enabled: &on}, nil, switched)
	case e.Screen == "bt" && e.Node == "pairnew":
		p.openPair()
	case e.Screen == btPairPage && e.Action == "pair" && p.pair.pairing == "":
		p.pairDevice(strings.TrimPrefix(e.Node, "dev:"))
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
