package shell

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/user"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/hal/usbgadget"
	"github.com/kario-os/kario/netfw"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/vpn"
)

// The VPN's policies (Settings › VPN, vpn.set), as Android's: one profile
// may be always on, connected at the start and again after it went down,
// after a failure (2 s, 5 s, 15 s, 30 s, then every minute) and when the
// network under it changes; with the kill switch on ("block connections
// without VPN", always-on only) nothing leaves outside its tunnel, up or
// down, but what the system needs (netfw.Lock: the servers, DHCP, IPv6
// neighbour discovery, the hotspot's and the USB cable's links). Per-app
// routing sends only the chosen apps through the tunnel, or all but them
// (netfw.Apps, by uid; their web views with them); the apps kept out pass
// the kill switch, as Android exempts the apps a VPN excludes. In "only"
// mode systemd-resolved goes through the tunnel as a chosen app does, so
// the profile's DNS servers answer every app's names. A disconnect, or
// deleting the profile, turns always-on off. On a developer machine the
// policies are kept and shown, and vpnRoute says what the device would do;
// only the device's tunnel (networkd) touches the firewall.

// vpnRetryDelays are the waits between an always-on profile's attempts.
var vpnRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute}

// vpnFirewall brings the firewall to the VPN's state: applyNetPolicy
// applies the kill switch and per-app routing after the per-app rules.
func (c *Core) vpnFirewall() {
	if err := c.applyNetPolicy(); err != nil {
		c.log.Printf("traffic: %v", err)
	}
}

// applyVPNRules puts the kill switch and per-app routing into the
// firewall, or lifts them (the device's tunnel only).
func (c *Core) applyVPNRules() error {
	if c.traffic == nil || c.vpn.kind != "networkd" {
		return nil
	}
	var lock *netfw.Lock
	if c.vpnLockOn() {
		l := c.vpnLock()
		lock = &l
	}
	var apps *netfw.Apps
	if a, ok := c.vpnAppsRule(); ok {
		apps = &a
	}
	return errors.Join(c.traffic.fw.ApplyLock(lock), c.traffic.fw.ApplyApps(apps))
}

// vpnLockOn: the kill switch holds (an always-on profile, and the switch).
func (c *Core) vpnLockOn() bool {
	v := &c.vpn
	return v.backend != nil && v.st.KillSwitch && v.profile(v.st.AlwaysOn) != nil
}

// vpnBlocking: the kill switch holds and the always-on tunnel is not up,
// so the apps it takes reach nothing.
func (c *Core) vpnBlocking() bool {
	v := &c.vpn
	return c.vpnLockOn() && (v.st.Active != v.st.AlwaysOn || v.state != ui.VPNConnected)
}

// vpnLock is the kill switch for the always-on profile: its servers given
// as addresses, the addresses its named ones had when it last came up,
// and DNS for the Shell itself while a name is among them.
func (c *Core) vpnLock() netfw.Lock {
	v := &c.vpn
	p := v.profile(v.st.AlwaysOn)
	l := netfw.Lock{Tunnel: vpn.Iface, Mark: vpn.FwMark, Links: c.vpnLinks()}
	for _, peer := range p.Conf.Peers {
		if a, ok := peer.Endpoint.Addr(); ok {
			l.Servers = append(l.Servers, netip.AddrPortFrom(a, peer.Endpoint.Port))
		} else {
			l.Resolve = true
		}
	}
	for _, s := range v.st.Servers[p.ID] {
		if !slices.Contains(l.Servers, s) {
			l.Servers = append(l.Servers, s)
		}
	}
	return l
}

// vpnLinks are the interfaces that lead only to devices nearby: the USB
// cable's, and the hotspot's while it is on (otherwise the same radio is
// the Wi-Fi station, a way out).
func (c *Core) vpnLinks() []string {
	out := []string{usbgadget.Interface(c.usb.cfg)}
	if c.hotspot.on && c.hotspot.iface != "" {
		out = append(out, c.hotspot.iface)
	}
	return out
}

// vpnAppsRule is per-app routing while a tunnel is up or the kill switch
// holds; false for every app through the tunnel.
func (c *Core) vpnAppsRule() (netfw.Apps, bool) {
	v := &c.vpn
	if v.st.Apps == ui.VPNAppsAll || (v.st.Active == "" && !c.vpnLockOn()) {
		return netfw.Apps{}, false
	}
	a := netfw.Apps{Mark: vpn.FwMark, Only: v.st.Apps == ui.VPNAppsOnly, Tunnel: vpn.Iface}
	if p := v.profile(firstNonEmpty(v.st.Active, v.st.AlwaysOn)); p != nil {
		for _, ad := range p.Conf.Addresses {
			a.Addrs = append(a.Addrs, ad.Addr())
		}
	}
	for id, uid := range c.appUIDs() {
		if slices.Contains(v.st.Chosen, strings.TrimSuffix(id, "/web")) {
			a.UIDs = append(a.UIDs, uid)
		}
	}
	if a.Only {
		if u, err := user.Lookup("systemd-resolve"); err == nil {
			if n, err := strconv.ParseUint(u.Uid, 10, 32); err == nil {
				a.UIDs = append(a.UIDs, uint32(n))
			}
		}
	}
	slices.Sort(a.UIDs)
	return a, true
}

// vpnAppList is the apps that use the network, for Settings to choose.
func (c *Core) vpnAppList() []ui.VPNApp {
	out := []ui.VPNApp{}
	for _, a := range c.apps {
		if a.M.Has(ui.PermNet) {
			out = append(out, ui.VPNApp{ID: a.M.ID, Name: a.M.Name, Chosen: slices.Contains(c.vpn.st.Chosen, a.M.ID)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Where vpnRoute says a packet goes.
const (
	routeTunnel  = "tunnel"
	routeDirect  = "direct"
	routeBlocked = "blocked"
)

// vpnRoute is where a packet of app's to dst goes now: into the tunnel,
// outside it, or nowhere (the kill switch), as the device's policy rules
// and firewall decide it.
func (c *Core) vpnRoute(app string, dst netip.Addr) string {
	v := &c.vpn
	taken := v.st.Apps == ui.VPNAppsAll || (v.st.Apps == ui.VPNAppsOnly) == slices.Contains(v.st.Chosen, app)
	routed := false
	if p := v.profile(v.st.Active); p != nil && v.state == ui.VPNConnected {
		for _, peer := range p.Conf.Peers {
			routed = routed || slices.ContainsFunc(peer.AllowedIPs, func(a netip.Prefix) bool { return a.Contains(dst) })
		}
	}
	switch {
	case taken && routed:
		return routeTunnel
	case taken && c.vpnLockOn():
		return routeBlocked
	}
	return routeDirect
}

func (c *Core) vpnSet(args ui.VPNArgs, reply func(any, error)) {
	v := &c.vpn
	splitBefore, reconnect := v.st.Apps != ui.VPNAppsAll, false
	switch {
	case args.AlwaysOn != nil && *args.AlwaysOn:
		p := v.profile(args.ID)
		if p == nil {
			reply(nil, errors.New(tr.T("No such VPN profile")))
			return
		}
		if v.st.AlwaysOn != p.ID {
			c.vpnCancelRetry()
		}
		v.st.AlwaysOn = p.ID
		c.log.Printf("vpn: “%s” always on", p.Name)
	case args.AlwaysOn != nil:
		c.vpnAlwaysOff(v.st.AlwaysOn)
	case args.KillSwitch != nil:
		p := v.profile(v.st.AlwaysOn)
		if *args.KillSwitch && p == nil {
			reply(nil, errors.New(tr.T("Make a VPN always on first: the block keeps the apps in its tunnel")))
			return
		}
		v.st.KillSwitch = *args.KillSwitch
		c.log.Printf("vpn: kill switch %v", v.st.KillSwitch)
		// A server known by name only gets through once the Shell has
		// resolved it, which a new connection does.
		if l := c.vpnLock(); v.st.KillSwitch && l.Resolve && v.kind == "networkd" && v.st.Active == p.ID && v.state == ui.VPNConnected {
			reconnect = true
		}
	case args.Apps != nil:
		mode := *args.Apps
		if mode != ui.VPNAppsAll && mode != ui.VPNAppsOnly && mode != ui.VPNAppsExcept {
			reply(nil, fmt.Errorf("vpn.set: apps is %q, %q or %q", ui.VPNAppsAll, ui.VPNAppsOnly, ui.VPNAppsExcept))
			return
		}
		v.st.Apps = mode
		c.log.Printf("vpn: apps %s", firstNonEmpty(mode, "all"))
	case args.App != "" && args.Chosen != nil:
		if a := c.appByID(args.App); a == nil || !a.M.Has(ui.PermNet) {
			reply(nil, fmt.Errorf("vpn.set: no app %s that uses the network", args.App))
			return
		}
		v.st.Chosen = slices.DeleteFunc(v.st.Chosen, func(id string) bool { return id == args.App })
		if *args.Chosen {
			v.st.Chosen = append(v.st.Chosen, args.App)
			slices.Sort(v.st.Chosen)
		}
		c.log.Printf("vpn: %s chosen %v", args.App, *args.Chosen)
	default:
		reply(nil, errors.New("vpn.set takes always_on (with id), kill_switch, apps, or app with chosen"))
		return
	}
	if err := v.save(); err != nil {
		reply(nil, err)
		return
	}
	c.vpnFirewall()
	// A split tunnel's routes move into a table of their own, or back,
	// only with the tunnel made again.
	if p := v.profile(v.st.Active); p != nil && splitBefore != (v.st.Apps != ui.VPNAppsAll) && v.state == ui.VPNConnected &&
		!p.Conf.FullTunnel() && v.ownTable != (v.st.Apps != ui.VPNAppsAll) {
		reconnect = true
	}
	if reconnect && !v.busy() {
		c.vpnAgain(v.st.Active)
	}
	c.vpnKeep()
	reply(c.vpnState(), nil)
}

// vpnAlwaysOff ends always-on (and the kill switch with it) when id is
// the always-on profile: the user disconnected or deleted it.
func (c *Core) vpnAlwaysOff(id string) {
	v := &c.vpn
	if id == "" || v.st.AlwaysOn != id {
		return
	}
	c.vpnCancelRetry()
	v.st.AlwaysOn, v.st.KillSwitch = "", false
	_ = v.save()
	c.log.Printf("vpn: always-on off")
	c.vpnFirewall()
}

// vpnKeep connects the always-on profile unless it is up, coming up or
// waiting for its next attempt.
func (c *Core) vpnKeep() {
	v := &c.vpn
	p := v.profile(v.st.AlwaysOn)
	if p == nil || v.backend == nil || v.busy() || v.retry != nil || (v.st.Active == p.ID && v.state == ui.VPNConnected) {
		return
	}
	c.vpnAgain(p.ID)
}

// vpnAgain brings id's tunnel up, and on failure waits for the next try.
func (c *Core) vpnAgain(id string) {
	c.vpnBringUp(id, func(err error) {
		if err != nil {
			c.vpnRetryLater()
		}
	})
}

func (c *Core) vpnRetryLater() {
	v := &c.vpn
	if v.profile(v.st.AlwaysOn) == nil || v.retry != nil {
		return
	}
	d := vpnRetryDelays[min(v.attempt, len(vpnRetryDelays)-1)]
	v.attempt++
	c.log.Printf("vpn: always on: trying again in %v", d)
	var t *time.Timer
	t = time.AfterFunc(d, func() {
		c.Post(func() {
			if v.retry == t {
				v.retry = nil
				c.vpnKeep()
			}
		})
	})
	v.retry = t
}

func (c *Core) vpnCancelRetry() {
	v := &c.vpn
	if v.retry != nil {
		v.retry.Stop()
		v.retry = nil
	}
	v.attempt = 0
}

// vpnResolve gives the servers c names by a name the address lookup finds
// for it (the first), so that networkd has nothing to resolve, and lists
// every server's address and port.
func vpnResolve(c vpn.Config, lookup func(context.Context, string) ([]netip.Addr, error)) (vpn.Config, []netip.AddrPort, error) {
	out := c
	out.Peers = slices.Clone(c.Peers)
	var servers []netip.AddrPort
	for i, p := range out.Peers {
		a, ok := p.Endpoint.Addr()
		if !ok {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			as, err := lookup(ctx, p.Endpoint.Host)
			cancel()
			if err == nil && len(as) == 0 {
				err = errors.New(tr.T("no address"))
			}
			if err != nil {
				return c, nil, errors.New(tr.T("Cannot find the VPN server %s: %v", p.Endpoint.Host, err))
			}
			a = as[0].Unmap()
			out.Peers[i].Endpoint.Host = a.String()
		}
		servers = append(servers, netip.AddrPortFrom(a, p.Endpoint.Port))
	}
	return out, servers, nil
}

// lockedLookup resolves a VPN server's name past the kill switch: the
// Shell (uid 0, which the switch lets ask DNS servers) asks the servers
// systemd-resolved would itself, whose own queries the switch blocks.
func lockedLookup(ctx context.Context, host string) ([]netip.Addr, error) {
	servers := upstreamDNS()
	if len(servers) == 0 {
		return nil, errors.New(tr.T("no DNS server: the phone has no network"))
	}
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		var errs []error
		for _, s := range servers {
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(s, "53"))
			if err == nil {
				return conn, nil
			}
			errs = append(errs, err)
		}
		return nil, errors.Join(errs...)
	}}
	return r.LookupNetIP(ctx, "ip", host)
}
