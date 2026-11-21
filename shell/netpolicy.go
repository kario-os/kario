package shell

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"syscall"

	"github.com/kario-os/kario/ui"
)

// Background data, Data Saver and each app's own mobile data limit
// (Settings › Data usage › an app; Android's "Background data",
// "Unrestricted data" and Data Saver). An app whose background data is off
// (its own switch, or Data Saver on and the app not exempt) may use mobile
// data only while it is on the screen (in front, the screen on, the keypad
// unlocked) or a background mode the system grants keeps it running (a
// call, its music playing, location all the time), as Android lets a
// foreground service: frozen, woken for a job or by push data, its mobile
// data is blocked; Wi-Fi is not. An app's own limit warns at
// ui.AppDataWarnPercent of it and then blocks its mobile data, on the
// screen too, until the billing cycle ends or the limit rises
// (datausage.go counts, every dataUsageEvery). A block is the per-app
// firewall's Wi-Fi-only rule for the app's uid (traffic.go), put in force
// when the blocks change: its connections over mobile data fail at once
// with EPERM. Without the firewall (a kernel lacking nftables' named
// counters, Linux 4.10; a developer machine) the blocks are the Shell's
// policy alone: the app is told (net.state), its jobs that need a network
// wait for Wi-Fi, the selftest's net-send answers as the policy says; its
// own connections are not stopped.

// bgRestricted: the app's background data is off, by its own switch or
// Data Saver.
func (s sysSettings) bgRestricted(id string) bool {
	return s.BgDataOff[id] || s.DataSaver && !s.DataSaverAllow[id]
}

// bgBlock is why the app's mobile data is blocked while it is in the
// background ("" it is not).
func (c *Core) bgBlock(id string) string {
	switch {
	case c.netState(id) == "wifi":
		return ui.NetBlockedWifiOnly
	case c.radios.data.over[id]:
		return ui.NetBlockedLimit
	case c.settings.bgRestricted(id):
		return ui.NetBlockedBackground
	}
	return ""
}

// mobileBlock is why the app's mobile data is blocked now ("" it is not).
func (c *Core) mobileBlock(a *App) string {
	why := c.bgBlock(a.M.ID)
	if why == ui.NetBlockedBackground && c.netFront(a) {
		return ""
	}
	return why
}

// netFront: the app counts as in front for its background data.
func (c *Core) netFront(a *App) bool {
	if a == c.onScreen() {
		return true
	}
	return a.running && !a.frozen && (c.inCall(a) || c.runsInBackground(a))
}

// netBlocks is why each app's mobile data is blocked now, the blocked
// ones alone.
func (c *Core) netBlocks() map[string]string {
	out := map[string]string{}
	for _, a := range c.apps {
		if !a.M.Has(ui.PermNet) {
			continue
		}
		if why := c.mobileBlock(a); why != "" {
			out[a.M.ID] = why
		}
	}
	return out
}

// netPolicyCheck puts the blocks in force once they changed: the
// firewall's rules, and every app whose block changed told, if it runs
// and is not frozen (net.state). Called from the tick, and where what
// they follow changes (the app in front, the screen, the counts, the
// settings).
func (c *Core) netPolicyCheck() {
	now, was := c.netBlocks(), c.radios.netBlocks
	if maps.Equal(now, was) {
		return
	}
	c.radios.netBlocks = now
	ids := map[string]bool{}
	for id := range now {
		ids[id] = true
	}
	for id := range was {
		ids[id] = true
	}
	var told []*App
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		if now[id] == was[id] {
			continue
		}
		c.log.Printf("net: %s mobile data blocked: %s", id, firstNonEmpty(now[id], "no"))
		if a := c.appByID(id); a != nil && a.running && !a.frozen {
			told = append(told, a)
		}
	}
	if err := c.applyAppRules(); err != nil {
		c.log.Printf("traffic: %v", err)
	}
	if len(told) == 0 {
		return
	}
	c.lookNetwork(func(n netNow) {
		for _, a := range told {
			if a.running && !a.frozen {
				raw, _ := json.Marshal(c.netStateOf(a, n))
				c.deliver(a, ui.Msg{T: ui.MsgEvent, Event: ui.EventNetState, Value: string(raw)})
			}
		}
	})
}

// netStateOf is what net.state tells the app, with n the network the
// phone is on.
func (c *Core) netStateOf(a *App, n netNow) ui.NetState {
	id, s := a.M.ID, c.settings
	st := ui.NetState{Network: firstNonEmpty(n.Kind, ui.NetNone), Metered: n.Metered, BackgroundData: !s.bgRestricted(id),
		DataSaver: s.DataSaver, Blocked: c.mobileBlock(a), Limit: s.AppDataLimit[id]}
	if st.Limit > 0 {
		cyc := c.cycle()
		st.Used = appUsed(c.dataLedger().lines(cyc.start, cyc.end)[id])
	}
	return st
}

// appUsed is what an app's own limit counts: mobile data at home and
// abroad.
func appUsed(n dataCount) uint64 { return n.mobile() + n.roaming() }

// netStateCap answers net.state once the network is looked at.
func (c *Core) netStateCap(a *App, reply func(any, error)) {
	c.lookNetwork(func(n netNow) { reply(c.netStateOf(a, n), nil) })
}

// appNetSetting is settings.set for Data Saver and an app's own data
// settings (ui.SettingDataSaver, ui.SettingApp*); false for other keys.
func (c *Core) appNetSetting(args ui.SettingArgs) (bool, error) {
	s := &c.settings
	switch args.Key {
	case ui.SettingDataSaver:
		s.DataSaver = args.Value != 0
		c.log.Printf("data saver %v", s.DataSaver)
	case ui.SettingAppBackgroundData, ui.SettingAppDataUnrestricted, ui.SettingAppDataLimit:
		a := c.appByID(args.Text)
		if a == nil || !a.M.Has(ui.PermNet) {
			return true, fmt.Errorf("%s: %q is no app that uses the network", args.Key, args.Text)
		}
		id, v := a.M.ID, args.Value
		switch args.Key {
		case ui.SettingAppBackgroundData:
			s.BgDataOff = setFlag(s.BgDataOff, id, v == 0)
		case ui.SettingAppDataUnrestricted:
			s.DataSaverAllow = setFlag(s.DataSaverAllow, id, v != 0)
		default:
			if v < 0 || v != math.Trunc(v) || v > 1<<53 {
				return true, fmt.Errorf("%s takes a whole number of bytes, 0 for none", args.Key)
			}
			delete(s.AppDataLimit, id)
			if v > 0 {
				if s.AppDataLimit == nil {
					s.AppDataLimit = map[string]uint64{}
				}
				s.AppDataLimit[id] = uint64(v)
			}
		}
		c.log.Printf("data usage: %s background data %v, unrestricted %v, limit %d", id, !s.BgDataOff[id], s.DataSaverAllow[id], s.AppDataLimit[id])
	default:
		return false, nil
	}
	c.saveSettings()
	c.checkDataPlan()
	return true, nil
}

// setFlag sets or clears a flag of a settings map, nil once empty.
func setFlag(m map[string]bool, id string, on bool) map[string]bool {
	if !on {
		delete(m, id)
		if len(m) == 0 {
			return nil
		}
		return m
	}
	if m == nil {
		m = map[string]bool{}
	}
	m[id] = true
	return m
}

// forgetAppNet drops an uninstalled app's own data settings.
func (c *Core) forgetAppNet(id string) {
	s := &c.settings
	if !s.BgDataOff[id] && !s.DataSaverAllow[id] && s.AppDataLimit[id] == 0 {
		return
	}
	s.BgDataOff, s.DataSaverAllow = setFlag(s.BgDataOff, id, false), setFlag(s.DataSaverAllow, id, false)
	delete(s.AppDataLimit, id)
	c.saveSettings()
}

// trafficApps are the apps with the network permission as Settings › Data
// usage shows them, with this cycle's lines.
func (c *Core) trafficApps(lines map[string]dataCount) []ui.TrafficApp {
	s, out := c.settings, []ui.TrafficApp{}
	for _, a := range c.apps {
		if !a.M.Has(ui.PermNet) {
			continue
		}
		id := a.M.ID
		out = append(out, ui.TrafficApp{ID: id, Name: a.M.Name, BackgroundOff: s.BgDataOff[id], Unrestricted: s.DataSaverAllow[id],
			WifiOnly: c.netState(id) == "wifi", Limit: s.AppDataLimit[id], Used: appUsed(lines[id]), Blocked: c.mobileBlock(a)})
	}
	slices.SortFunc(out, func(x, y ui.TrafficApp) int {
		if x.Name != y.Name {
			return strings.Compare(x.Name, y.Name)
		}
		return strings.Compare(x.ID, y.ID)
	})
	return out
}

// onWifiNow: the phone has a Wi-Fi network with an address (the radio's
// own answer, the simulated one's on a developer machine).
func (c *Core) onWifiNow() bool {
	if c.wifi == nil {
		return false
	}
	l, err := c.wifi.link()
	return err == nil && l.ssid != "" && l.addr != ""
}

// policySend is what the per-app firewall would answer an app's packet
// where there is none (a developer machine, an old kernel): EPERM while
// its mobile data is blocked and the phone is not on Wi-Fi. Selftest.
func (c *Core) policySend(id string) error {
	a := c.appByID(id)
	if a == nil || !c.netAllowed(a) {
		return fmt.Errorf("%s has no network", id)
	}
	if c.radios.netBlocks[id] != "" && !c.onWifiNow() {
		return syscall.EPERM
	}
	return nil
}
