package vpn

import (
	"fmt"
	"path/filepath"
	"strings"
)

// The tunnel as systemd-networkd sets it up (systemd.netdev(5),
// systemd.network(5)): a WireGuard netdev and its network, written under
// /run/systemd/network while it is up. A full tunnel is wg-quick's: the
// routes to AllowedIPs go into a table of their own, which everything not
// carrying the tunnel's firewall mark looks up; the tunnel's own UDP
// packets carry it, so they reach the endpoint over the link under the
// tunnel, and the main table is still asked first for everything but its
// default route (the local network stays reachable). A split tunnel adds
// AllowedIPs to the main table, or with per-app routing to the tunnel's
// table under the same rule. The DNS servers go to systemd-resolved for
// the tunnel's link and answer every name while it is up, as wg-quick's
// do.

const (
	// Iface is the tunnel's network interface, whichever profile is up.
	Iface = "kario-vpn"
	// Table and FwMark are wg-quick's defaults (its listen port, 51820).
	Table  = 51820
	FwMark = 51820
	// The policy rules' priorities: the main table without default routes
	// first, then the tunnel's table, both before the main table's rule
	// (32766).
	PrioritySuppress = 30000
	PriorityTunnel   = 30001
)

// File is one of networkd's files; Secret ones hold keys (readable by
// systemd-network only).
type File struct {
	Name   string
	Data   string
	Secret bool
}

// NetworkdFiles are the files that bring c up, in dir. c must come from
// Parse with its PrivateKey set: every value written is one Parse checked
// and wrote back in its own form, so nothing of the file's text reaches
// networkd's. ownTable puts a split tunnel's routes into the tunnel's
// table as well, which packets carrying FwMark keep out of (a lookup
// there that finds no route goes on to the main table): per-app routing
// steers by that mark, which a full tunnel honours anyway.
func NetworkdFiles(c Config, dir string, ownTable bool) []File {
	path := func(name string) string { return filepath.Join(dir, name) }
	keyFile := Iface + ".key"
	files := []File{{Name: keyFile, Data: c.PrivateKey.String() + "\n", Secret: true}}
	var nd strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&nd, format+"\n", args...) }
	w("[NetDev]")
	w("Name=%s", Iface)
	w("Kind=wireguard")
	if c.MTU != 0 {
		w("MTUBytes=%d", c.MTU)
	}
	w("")
	w("[WireGuard]")
	w("PrivateKeyFile=%s", path(keyFile))
	if c.ListenPort != 0 {
		w("ListenPort=%d", c.ListenPort)
	}
	full := c.FullTunnel()
	if full || ownTable {
		w("FirewallMark=%d", FwMark)
		w("RouteTable=%d", Table)
	} else {
		w("RouteTable=main")
	}
	for i, p := range c.Peers {
		w("")
		w("[WireGuardPeer]")
		w("PublicKey=%s", p.PublicKey)
		if p.PresharedKey != nil {
			name := fmt.Sprintf("%s-%d.psk", Iface, i+1)
			files = append(files, File{Name: name, Data: p.PresharedKey.String() + "\n", Secret: true})
			w("PresharedKeyFile=%s", path(name))
		}
		w("Endpoint=%s", p.Endpoint)
		for _, a := range p.AllowedIPs {
			w("AllowedIPs=%s", a)
		}
		if p.Keepalive != 0 {
			w("PersistentKeepalive=%d", p.Keepalive)
		}
	}
	files = append(files, File{Name: "50-" + Iface + ".netdev", Data: nd.String()})

	var nw strings.Builder
	w = func(format string, args ...any) { fmt.Fprintf(&nw, format+"\n", args...) }
	w("[Match]")
	w("Name=%s", Iface)
	w("")
	w("[Link]")
	w("RequiredForOnline=no")
	w("")
	w("[Network]")
	for _, a := range c.Addresses {
		w("Address=%s", a)
	}
	for _, a := range c.DNS {
		w("DNS=%s", a)
	}
	if len(c.DNS) > 0 {
		w("Domains=%s", strings.Join(append([]string{"~."}, c.Search...), " "))
		w("DNSDefaultRoute=yes")
	} else if len(c.Search) > 0 {
		w("Domains=%s", strings.Join(c.Search, " "))
	}
	w("LinkLocalAddressing=no")
	w("IPv6AcceptRA=no")
	if full || ownTable {
		family := "both"
		switch v4, v6 := c.Families(); {
		case !v6:
			family = "ipv4"
		case !v4:
			family = "ipv6"
		}
		if full {
			w("")
			w("[RoutingPolicyRule]")
			w("Table=main")
			w("SuppressPrefixLength=0")
			w("Priority=%d", PrioritySuppress)
			w("Family=%s", family)
		}
		w("")
		w("[RoutingPolicyRule]")
		w("FirewallMark=%d", FwMark)
		w("InvertRule=yes")
		w("Table=%d", Table)
		w("Priority=%d", PriorityTunnel)
		w("Family=%s", family)
	}
	return append(files, File{Name: "50-" + Iface + ".network", Data: nw.String()})
}
