package shell

import (
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/kario-os/kario/metered"
	"github.com/kario-os/kario/ui"
)

// Metered networks (Settings › Wi-Fi, each network's "Metered"): a Wi-Fi
// network counts as metered when the user says so, or, left automatic,
// when its DHCP server or its addresses say it is a phone's hotspot
// (package metered); mobile data always does. Large downloads the user
// did not start wait for an unmetered network, and stop when the phone
// leaves it: automatic system updates (updateauto.go), and background jobs
// that ask for one (the store's automatic updates, job.schedule's
// unmetered; jobs.go). What detection found is kept for the network and
// the address the phone has there.

const (
	// meterProbeTimeout bounds the DHCP question; most servers answer at
	// once, and one that does not leaves the network automatic.
	meterProbeTimeout = 1500 * time.Millisecond
	// meterStale: jobs look at the network the shell saw this recently,
	// and look again (off the loop) after.
	meterStale = 30 * time.Second
)

// Network kinds the phone is on.
const (
	netWifi   = ui.NetWifi
	netMobile = ui.NetMobile
	netNone   = ui.NetNone
)

// netNow is a look at the network the phone is on.
type netNow struct {
	Kind    string
	SSID    string
	Metered bool
	// Choice: the user's for the Wi-Fi network; Hint: what detection
	// found ("" nothing).
	Choice, Hint string
}

type meterState struct {
	mu    sync.Mutex
	hints map[string]string // by network and address
	now   netNow
	at    time.Time
	busy  bool
	jobs  jobWatch // the running jobs' looks (jobs.go)
}

// meteredChoices is a copy of the user's choices, for off the loop.
func (c *Core) meteredChoices() map[string]string {
	out := make(map[string]string, len(c.settings.WifiMetered))
	for k, v := range c.settings.WifiMetered {
		out[k] = v
	}
	return out
}

// setMetered keeps the user's choice for a network (ui.MeteredAuto drops
// it).
func (c *Core) setMetered(ssid, choice string) error {
	switch choice {
	case ui.MeteredAuto:
		delete(c.settings.WifiMetered, ssid)
	case ui.MeteredYes, ui.MeteredNo:
		if c.settings.WifiMetered == nil {
			c.settings.WifiMetered = map[string]string{}
		}
		c.settings.WifiMetered[ssid] = choice
	default:
		return fmt.Errorf("metered is %s, %s or %s", ui.MeteredAuto, ui.MeteredYes, ui.MeteredNo)
	}
	c.saveSettings()
	c.radios.meter.at = time.Time{}
	c.log.Printf("wifi: %q metered: %s", ssid, choice)
	return nil
}

// meterHint is what detection finds of the network l is on (cached for
// the network and address); it may ask the network's DHCP server, so not
// on the loop.
func (c *Core) meterHint(l wifiLink) string {
	key := l.ssid + "\x00" + l.addr
	m := &c.radios.meter
	m.mu.Lock()
	hint, ok := m.hints[key]
	m.mu.Unlock()
	if ok {
		return hint
	}
	why := ""
	switch ip := net.ParseIP(l.addr); {
	case l.hint != "":
		hint = l.hint
	case ip != nil && metered.HotspotAddress(ip):
		hint = ui.MeteredHintHotspot
	case l.ifname == "" || ip == nil:
	case os.Geteuid() != 0:
		why = "no DHCP question without root"
	default:
		vendor, err := metered.Probe(l.ifname, ip, meterProbeTimeout)
		switch {
		case err != nil:
			why = err.Error()
		case metered.VendorSaysMetered(vendor):
			hint = ui.MeteredHintDHCP
		}
	}
	c.log.Printf("wifi: %q at %s metered: %q %s", l.ssid, l.addr, hint, why)
	m.mu.Lock()
	if m.hints == nil {
		m.hints = map[string]string{}
	}
	m.hints[key] = hint
	m.mu.Unlock()
	return hint
}

// lookNetwork tells then what network the phone is on, looking off the
// loop; jobs read the last look.
func (c *Core) lookNetwork(then func(netNow)) {
	wifi, choices := c.wifi, c.meteredChoices()
	mobile := c.radioState().Data == "connected" && !c.settings.Airplane
	go func() {
		n := netNow{Kind: netNone}
		var l wifiLink
		if wifi != nil {
			l, _ = wifi.link()
		}
		switch {
		case l.ssid != "" && l.addr != "":
			n = netNow{Kind: netWifi, SSID: l.ssid, Choice: choices[l.ssid], Hint: c.meterHint(l)}
			n.Metered = metered.Decide(n.Choice, n.Hint)
		case mobile:
			n = netNow{Kind: netMobile, Metered: true}
		}
		c.Post(func() {
			c.radios.meter.now, c.radios.meter.at = n, c.now()
			then(n)
		})
	}()
}

// netMoved: the phone joined, left or changed its Wi-Fi network (wifi.set,
// wifi.connect, wifi.forget): the jobs waiting for a network look again
// at once, not meterStale after the last look.
func (c *Core) netMoved() { c.radios.meter.at = time.Time{} }

// unmeteredWhy says why the network is not one for large downloads ("" it
// is).
func unmeteredWhy(n netNow) string {
	switch {
	case n.Kind != netWifi:
		return "not on Wi-Fi"
	case n.Metered:
		return fmt.Sprintf("on a metered network (%s)", n.SSID)
	}
	return ""
}

// meteredNets adds the user's choices and, for the network the phone is
// on, whether it counts as metered and why, to wifi.state's networks (off
// the loop).
func (c *Core) meteredNets(st *ui.WifiState, link wifiLink, choices map[string]string) {
	for i := range st.Networks {
		n := &st.Networks[i]
		n.MeteredChoice = choices[n.SSID]
		n.Metered = n.MeteredChoice == ui.MeteredYes
		if n.Connected && link.ssid == n.SSID && link.addr != "" {
			n.MeteredHint = c.meterHint(link)
			n.Metered = metered.Decide(n.MeteredChoice, n.MeteredHint)
		}
	}
}
