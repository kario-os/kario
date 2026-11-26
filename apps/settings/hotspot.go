package main

import (
	"slices"
	"strings"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Personal hotspot (hotspot.state, hotspot.set): the switch, the
// network's name and its password, shown as phones show it and changed on
// a page of their own, the security, hiding the name and the band where
// the phone's access point can, the device limit, what the devices used
// and how much they may use in a day, and the devices connected (a page
// of their own), which the pages refresh while they are open.

// hotspotLimits are the daily limits the page steps through, 0 for none.
var hotspotLimits = []uint64{0, 500 << 20, 1 << 30, 2 << 30, 5 << 30, 10 << 30}

// hsState is the Shell's answer; tried: asked once for the root page's
// line (as nfcState); polling: the page's refresh is running.
var hsState struct {
	ui.HotspotState
	loaded, tried, polling bool
}

func (p *app) loadHotspot(then func()) {
	hsState.tried = true
	var st ui.HotspotState
	p.a.Go(ui.CapHotspotState, nil, &st, func(err error) {
		if err == nil {
			hsState.HotspotState, hsState.loaded = st, true
		}
		if then != nil {
			then()
		}
	})
}

func (p *app) hotspotSummary() string {
	st := hsState
	switch {
	case !st.loaded:
		return ""
	case st.Backend == "":
		return tr.T("No Wi-Fi")
	case !st.Enabled:
		return tr.T("Off")
	}
	return tr.T("On · %s", st.SSID)
}

// hotspotItem is the page's line on the root page.
func (p *app) hotspotItem() ui.Node {
	if !hsState.tried {
		p.loadHotspot(nil)
	}
	return ui.Node{Kind: ui.KindItem, ID: "hotspot", Title: tr.T("Personal hotspot"), Subtitle: p.hotspotSummary(), Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
}

func (p *app) hotspotScreen() ui.Screen {
	st := hsState
	s := ui.Screen{ID: "hotspot", Title: tr.T("Personal hotspot")}
	if st.loaded && st.Backend == "" {
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("This device has no Wi-Fi"))}
		return s
	}
	sub := tr.T("Shares the phone's connection with other devices over Wi-Fi")
	switch {
	case st.Enabled && !st.Started:
		sub = tr.T("Turning on…")
	case st.Enabled:
		sub = tr.T("Connected devices: %d", st.Clients)
	}
	edit := []ui.Action{{ID: "edit", Label: tr.T("Change"), Primary: true}}
	next := []ui.Action{{ID: "next", Label: tr.T("Next option"), Primary: true, Key: "right"}, {ID: "prev", Label: tr.T("Previous"), Key: "left"}}
	toggle := ui.Action{ID: "toggle", Label: tr.T("Switch"), Primary: true}
	if !st.Enabled && st.Usage.LimitReached {
		toggle.Confirm = tr.T("Turn the hotspot on anyway? The devices used today's limit; it stays on for the rest of the day.")
	}
	s.Body = []ui.Node{
		{Kind: ui.KindItem, ID: "hs_on", Title: tr.T("Personal hotspot"), Subtitle: sub, Trailing: onOff(st.Enabled), Actions: []ui.Action{toggle}},
		{Kind: ui.KindItem, ID: "hs_name", Title: tr.T("Network name"), Trailing: st.SSID, Actions: edit},
		{Kind: ui.KindItem, ID: "hs_pass", Title: tr.T("Password"), Subtitle: securityLabel(st.Security), Trailing: st.Passphrase, Actions: edit},
	}
	if len(st.Securities) > 1 {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "hs_security", Title: tr.T("Security"), Subtitle: securityNote(st.Security),
			Trailing: securityLabel(st.Security), Actions: next})
	}
	if st.CanHide {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "hs_hidden", Title: tr.T("Hide the network"), Subtitle: tr.T("Not in other devices' Wi-Fi lists: they join by typing its name"),
			Trailing: onOff(st.Hidden), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}})
	}
	if len(st.Bands) > 1 {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "hs_band", Title: tr.T("Band"), Subtitle: bandNote(st.Band),
			Trailing: bandLabel(st.Band), Actions: next})
	}
	s.Body = append(s.Body,
		ui.Node{Kind: ui.KindItem, ID: "hs_max", Title: tr.T("Maximum devices"), Subtitle: tr.T("One more is sent away; the devices already on stay"),
			Trailing: maxLabel(st.MaxClients), Actions: []ui.Action{{ID: "more", Label: tr.T("More"), Primary: true, Key: "right"}, {ID: "fewer", Label: tr.T("Fewer"), Key: "left"}}},
		ui.Node{Kind: ui.KindItem, ID: "hs_devices", Title: tr.T("Connected devices"), Subtitle: devicesSummary(st.HotspotState), Actions: []ui.Action{sdk.Open(tr.T("Open"))}},
		ui.Node{Kind: ui.KindItem, ID: "hs_usage", Title: tr.T("Data used"), Trailing: bytesLabel(st.Usage.SessionDown + st.Usage.SessionUp),
			Subtitle: tr.T("This session ↓%s ↑%s · Today ↓%s ↑%s", bytesLabel(st.Usage.SessionDown), bytesLabel(st.Usage.SessionUp), bytesLabel(st.Usage.TodayDown), bytesLabel(st.Usage.TodayUp))},
		ui.Node{Kind: ui.KindItem, ID: "hs_limit", Title: tr.T("Daily limit for devices"), Subtitle: dayLimitNote(st.HotspotState), Trailing: dayLimitLabel(st.DailyLimit),
			Actions: []ui.Action{{ID: "more", Label: tr.T("More"), Primary: true, Key: "right"}, {ID: "fewer", Label: tr.T("Less"), Key: "left"}}})
	if len(st.Securities) <= 1 && !st.CanHide && st.Backend != "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("This phone's hotspot offers WPA2 only and cannot hide its name (iwd's access point)")))
	}
	switch {
	case st.Backend == "sim":
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Simulated hotspot: this machine shares no connection")))
	case st.Started && st.Upstream == "":
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Nothing to share yet: devices that join reach only the phone until mobile data connects")))
	case st.Started:
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Devices that join use the phone's connection (%s)", st.Upstream)))
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("While the hotspot is on, Wi-Fi is off: the phone has one radio. Turning Wi-Fi on turns the hotspot off.")))
	return s
}

func securityLabel(sec string) string {
	switch sec {
	case ui.HotspotWPA3:
		return "WPA3"
	case ui.HotspotWPA2WPA3:
		return "WPA2/WPA3"
	}
	return "WPA2"
}

func securityNote(sec string) string {
	switch sec {
	case ui.HotspotWPA3:
		return tr.T("The safest; older devices cannot join")
	case ui.HotspotWPA2WPA3:
		return tr.T("WPA3 for the devices that have it, WPA2 for the others")
	}
	return tr.T("Every device can join")
}

func bandLabel(band string) string { return tr.T("%s GHz", band) }

func bandNote(band string) string {
	if band == ui.HotspotBand5 {
		return tr.T("Faster; devices without 5 GHz do not see it")
	}
	return tr.T("Reaches further; every device can join")
}

func maxLabel(n int) string {
	if n == 0 {
		return tr.T("No limit")
	}
	return tr.T("%d devices", n)
}

func dayLimitLabel(n uint64) string {
	if n == 0 {
		return tr.T("No daily limit")
	}
	return tr.T("%s a day", bytesLabel(n))
}

func dayLimitNote(st ui.HotspotState) string {
	switch {
	case st.DailyLimit == 0:
		return tr.T("The devices may use any amount")
	case st.Usage.LimitReached:
		return tr.T("Today's limit is used up")
	}
	return tr.T("A warning near it; at it the hotspot turns off")
}

func devicesSummary(st ui.HotspotState) string {
	switch {
	case !st.Started:
		return tr.T("The hotspot is off")
	case st.MaxClients > 0:
		return tr.T("%d of at most %d", st.Clients, st.MaxClients)
	}
	return tr.T("%d connected", st.Clients)
}

// cycle is the value after (or before) cur in vals.
func cycle(vals []string, cur string, back bool) string {
	i := slices.Index(vals, cur)
	if back {
		return vals[(max(i, 0)+len(vals)-1)%len(vals)]
	}
	return vals[(i+1)%len(vals)]
}

// hotspotDevicesScreen lists the devices on the hotspot: the name each
// gave (or none), its Wi-Fi address, its address and since when.
func (p *app) hotspotDevicesScreen() ui.Screen {
	st := hsState
	s := ui.Screen{ID: "hotspotdevices", Title: tr.T("Connected devices")}
	for _, d := range st.Devices {
		sub := tr.T("%s · since %s", d.MAC, time.Unix(d.Since, 0).Format("15:04"))
		if d.Address != "" {
			sub += " · " + d.Address
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "hd:" + d.MAC, Title: firstOf(d.Name, tr.T("Unnamed device")), Subtitle: sub})
	}
	if len(st.Devices) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No device is connected")))
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("The name is the one a device gives when it asks for an address; some give none")))
	return s
}

// hotspotEdit is the page that changes the name ("hotspotname") or the
// password ("hotspotpass").
func hotspotEdit(id string) ui.Screen {
	s := ui.Screen{ID: id, Title: tr.T("Network name"), Actions: []ui.Action{{ID: "save", Label: tr.T("Save")}}}
	in := ui.Node{Kind: ui.KindInput, ID: "v", Value: hsState.SSID, Placeholder: tr.T("Network name"), Submit: "save"}
	note := tr.T("What other devices see in their Wi-Fi list; up to 32 bytes (a Chinese character takes 3)")
	if id == "hotspotpass" {
		s.Title, note = tr.T("Password"), tr.T("8 to 63 letters, digits or symbols. Devices that joined with the old password must join again.")
		in.Value, in.Placeholder, in.Keyboard = hsState.Passphrase, tr.T("Password"), "email"
	}
	s.Body = []ui.Node{sdk.Styled(ui.StyleDim, note), in}
	return s
}

func (p *app) hotspotUpdate() {
	p.a.Update(p.hotspotScreen())
	p.a.Update(p.hotspotDevicesScreen())
	p.updateMenu()
}

// setHotspot sends the change; done runs once the system took it.
func (p *app) setHotspot(args ui.HotspotArgs, done func()) {
	var st ui.HotspotState
	p.a.Go(ui.CapHotspotSet, args, &st, func(err error) {
		if err != nil {
			if !ui.Cancelled(err) {
				p.a.Toast(err.Error())
			}
			p.loadHotspot(p.hotspotUpdate)
			return
		}
		hsState.HotspotState, hsState.loaded = st, true
		if done != nil {
			done()
		}
		p.hotspotUpdate()
	})
}

// pollHotspot refreshes the page every 2 seconds while it is open.
func (p *app) pollHotspot() {
	if hsState.polling {
		return
	}
	hsState.polling = true
	go func() {
		for range time.Tick(2 * time.Second) {
			p.a.Post(func() {
				if slices.Contains(p.pages, "hotspot") {
					p.loadHotspot(func() {
						p.a.Update(p.hotspotScreen())
						p.a.Update(p.hotspotDevicesScreen())
					})
				}
			})
		}
	}()
}

// restoreHotspot is the page opened again after a restart.
func (p *app) restoreHotspot() ui.Screen {
	p.loadHotspot(p.hotspotUpdate)
	p.pollHotspot()
	return p.hotspotScreen()
}

// hotspotAction handles the hotspot's pages; false for the rest.
func (p *app) hotspotAction(e sdk.ActionEvent) bool {
	switch {
	case onMenu(e.Screen) && e.Node == "hotspot":
		p.loadHotspot(func() {
			p.push(p.hotspotScreen())
			p.pollHotspot()
		})
	case e.Screen == "hotspot" && e.Node == "hs_on":
		// Kept at once, as the volumes are: a second press before the
		// system's answer switches back.
		on := !hsState.Enabled
		hsState.Enabled = on
		p.a.Update(p.hotspotScreen())
		p.setHotspot(ui.HotspotArgs{Enabled: &on}, nil)
	case e.Screen == "hotspot" && e.Node == "hs_security" && len(hsState.Securities) > 1:
		p.setHotspot(ui.HotspotArgs{Security: cycle(hsState.Securities, hsState.Security, e.Action == "prev")}, nil)
	case e.Screen == "hotspot" && e.Node == "hs_band" && len(hsState.Bands) > 1:
		p.setHotspot(ui.HotspotArgs{Band: cycle(hsState.Bands, hsState.Band, e.Action == "prev")}, nil)
	case e.Screen == "hotspot" && e.Node == "hs_hidden":
		hidden := !hsState.Hidden
		p.setHotspot(ui.HotspotArgs{Hidden: &hidden}, nil)
	case e.Screen == "hotspot" && e.Node == "hs_max":
		n := hsState.MaxClients + 1
		if e.Action == "fewer" {
			n = hsState.MaxClients - 1
		}
		// No limit sits on both ends: past the most, and before one.
		n = (n + ui.HotspotMaxClients + 1) % (ui.HotspotMaxClients + 1)
		hsState.MaxClients = n
		p.a.Update(p.hotspotScreen())
		p.setHotspot(ui.HotspotArgs{MaxClients: &n}, nil)
	case e.Screen == "hotspot" && e.Node == "hs_limit":
		i, step := slices.Index(hotspotLimits, hsState.DailyLimit), 1
		if e.Action == "fewer" {
			step = len(hotspotLimits) - 1
		}
		if i < 0 {
			i = 0
		}
		v := hotspotLimits[(i+step)%len(hotspotLimits)]
		hsState.DailyLimit = v
		p.a.Update(p.hotspotScreen())
		p.setHotspot(ui.HotspotArgs{DailyLimit: &v}, nil)
	case e.Screen == "hotspot" && e.Node == "hs_devices":
		p.push(p.hotspotDevicesScreen())
	case e.Screen == "hotspot" && e.Node == "hs_name":
		p.push(hotspotEdit("hotspotname"))
	case e.Screen == "hotspot" && e.Node == "hs_pass":
		p.push(hotspotEdit("hotspotpass"))
	case (e.Screen == "hotspotname" || e.Screen == "hotspotpass") && e.Action == "save":
		v := firstOf(e.Fields["v"], e.Value)
		args := ui.HotspotArgs{SSID: v}
		if e.Screen == "hotspotpass" {
			args = ui.HotspotArgs{Passphrase: v}
		}
		switch {
		case strings.TrimSpace(v) == "" && e.Screen == "hotspotname":
			p.a.Toast(tr.T("Enter a network name"))
		case v == "":
			p.a.Toast(tr.T("Enter a password"))
		default:
			p.setHotspot(args, p.a.Pop)
		}
	default:
		return false
	}
	return true
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
