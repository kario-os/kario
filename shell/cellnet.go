package shell

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/kario-os/kario/vpn"
)

// The cellular data link's interface. telephonyd reports the bearer's
// addressing (data_ip: static, or DHCP for IPv4 and router advertisements
// for IPv6, as ModemManager's IpConfig method says); on the device the
// Shell hands it to systemd-networkd as a runtime .network for the link,
// as for USB sharing and the VPN, and networkd gives the link's DNS
// servers to systemd-resolved. The link's routes give way to Wi-Fi's and
// a wired link's. When the link goes (mobile data off, abroad with data
// roaming off, the bearer down) its file turns Unmanaged and its addresses
// go: networkd leaves an unmanaged link as it was, and an Unmanaged file
// keeps a catch-all .network (QEMU's ip=dhcp) off it. A developer machine
// (not root, no systemd-networkd) configures nothing.
//
// The MMS access point's link (telephonyd's mms_link, up beside the data
// link while phone-smsd sends or fetches a message, when the operator
// keeps MMS on an APN of its own) gets a file of its own: its address,
// and a routing table of its own that a policy rule gives what leaves
// from that address. smsd's MMS connections leave from it; nothing else
// does, and neither its routes nor its DNS servers reach the main table
// or systemd-resolved.

// cellMetric: after iwd's routes and networkd's DHCP default (1024).
const cellMetric = 2048

// The MMS link's routing table, and its rule's priority: before the VPN's
// rules, so MMS leaves over its access point with a tunnel up.
const (
	cellMMSTable    = 2049
	cellMMSPriority = vpn.PrioritySuppress - 1
)

// cellLink is telephonyd's data_ip.
type cellLink struct {
	Iface string  `json:"iface"`
	MTU   int     `json:"mtu"`
	IPv4  *cellIP `json:"ipv4"`
	IPv6  *cellIP `json:"ipv6"`
}

type cellIP struct {
	Method  string   `json:"method"`
	Address string   `json:"address"`
	Prefix  int      `json:"prefix"`
	Gateway string   `json:"gateway"`
	DNS     []string `json:"dns"`
}

const cellNetPrefix = "10-kario-cell-"

func cellNetFile(iface string) string { return cellNetPrefix + iface + ".network" }

var cellIfaceName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,14}$`)

// cellFam is one family of the link as networkd gets it: static (addr and
// gateway) or dhcp; for IPv6 with only a link-local address, router
// advertisements with that address's interface identifier (token).
type cellFam struct {
	method  string
	addr    netip.Prefix
	gateway netip.Addr
	token   netip.Addr
	dns     []netip.Addr
}

func cellFamily(ip *cellIP, v6 bool) (cellFam, error) {
	var f cellFam
	if ip == nil {
		return f, nil
	}
	switch ip.Method {
	case "static", "dhcp":
		f.method = ip.Method
	case "ppp":
		return f, errors.New("a PPP link")
	default:
		return f, nil
	}
	same := func(a netip.Addr) bool { return a.Is6() == v6 && !a.Is4In6() && a.Zone() == "" }
	for _, d := range ip.DNS {
		a, err := netip.ParseAddr(d)
		if err != nil || a.Zone() != "" || a.IsUnspecified() {
			return f, fmt.Errorf("DNS server %q", d)
		}
		f.dns = append(f.dns, a)
	}
	if f.method != "static" {
		return f, nil
	}
	a, err := netip.ParseAddr(ip.Address)
	if err != nil || !same(a) || a.IsUnspecified() {
		return f, fmt.Errorf("address %q", ip.Address)
	}
	bits := ip.Prefix
	if bits <= 0 || bits > a.BitLen() {
		bits = a.BitLen()
	}
	if v6 && a.IsLinkLocalUnicast() {
		f.method, f.token = "dhcp", a
	} else {
		f.addr = netip.PrefixFrom(a, bits)
	}
	if ip.Gateway != "" {
		g, err := netip.ParseAddr(ip.Gateway)
		if err != nil || !same(g) || g.IsUnspecified() {
			return f, fmt.Errorf("gateway %q", ip.Gateway)
		}
		f.gateway = g
	}
	return f, nil
}

// cellNetwork is networkd's file for the link. Every value written is one
// parsed and written back in its own form: nothing telephonyd passes on
// reaches the file as text but the interface's name, which is checked.
func cellNetwork(l cellLink) (string, error) {
	if !cellIfaceName.MatchString(l.Iface) {
		return "", fmt.Errorf("interface %q", l.Iface)
	}
	v4, err := cellFamily(l.IPv4, false)
	if err != nil {
		return "", fmt.Errorf("IPv4: %w", err)
	}
	v6, err := cellFamily(l.IPv6, true)
	if err != nil {
		return "", fmt.Errorf("IPv6: %w", err)
	}
	if v4.method == "" && v6.method == "" {
		return "", errors.New("no address yet")
	}
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("[Match]")
	w("Name=%s", l.Iface)
	w("")
	w("[Link]")
	w("RequiredForOnline=no")
	if l.MTU >= 576 && l.MTU <= 9216 && (v6.method == "" || l.MTU >= 1280) {
		w("MTUBytes=%d", l.MTU)
	}
	w("")
	w("[Network]")
	if v6.method != "" {
		w("LinkLocalAddressing=ipv6")
		w("IPv6AcceptRA=yes")
	} else {
		w("LinkLocalAddressing=no")
		w("IPv6AcceptRA=no")
	}
	if v4.method == "dhcp" {
		w("DHCP=ipv4")
	}
	for _, f := range []cellFam{v4, v6} {
		if f.addr.IsValid() {
			w("Address=%s", f.addr)
		}
	}
	for _, f := range []cellFam{v4, v6} {
		for _, d := range f.dns {
			w("DNS=%s", d)
		}
	}
	for _, f := range []cellFam{v4, v6} {
		switch {
		case f.gateway.IsValid():
			w("")
			w("[Route]")
			w("Gateway=%s", f.gateway)
			w("GatewayOnLink=yes")
			w("Metric=%d", cellMetric)
		case f.addr.IsValid() && f.addr.Addr().Is4():
			// A point-to-point link without a gateway: the default route
			// is the link itself.
			w("")
			w("[Route]")
			w("Destination=0.0.0.0/0")
			w("Metric=%d", cellMetric)
		}
	}
	if v4.method == "dhcp" {
		w("")
		w("[DHCPv4]")
		w("RouteMetric=%d", cellMetric)
	}
	if v6.method != "" {
		w("")
		w("[IPv6AcceptRA]")
		w("RouteMetric=%d", cellMetric)
		if v6.token.IsValid() {
			id := v6.token.As16()
			clear(id[:8])
			if iid := netip.AddrFrom16(id); !iid.IsUnspecified() {
				w("Token=static:%s", iid)
			}
		}
	}
	return b.String(), nil
}

// cellMMSNetwork is networkd's file for the MMS access point's link. The
// policy rule matches its address, so it needs a static IPv4 one (a QMI
// modem's); IPv6 stays off.
func cellMMSNetwork(l cellLink) (string, error) {
	if !cellIfaceName.MatchString(l.Iface) {
		return "", fmt.Errorf("interface %q", l.Iface)
	}
	v4, err := cellFamily(l.IPv4, false)
	if err != nil {
		return "", fmt.Errorf("IPv4: %w", err)
	}
	if !v4.addr.IsValid() {
		return "", errors.New("no static IPv4 address")
	}
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("[Match]")
	w("Name=%s", l.Iface)
	w("")
	w("[Link]")
	w("RequiredForOnline=no")
	if l.MTU >= 576 && l.MTU <= 9216 {
		w("MTUBytes=%d", l.MTU)
	}
	w("")
	w("[Network]")
	w("LinkLocalAddressing=no")
	w("IPv6AcceptRA=no")
	w("DNSDefaultRoute=no")
	w("Address=%s", v4.addr)
	w("")
	w("[Route]")
	if v4.gateway.IsValid() {
		w("Gateway=%s", v4.gateway)
		w("GatewayOnLink=yes")
	} else {
		w("Destination=0.0.0.0/0")
	}
	w("Table=%d", cellMMSTable)
	w("")
	w("[RoutingPolicyRule]")
	w("From=%s/32", v4.addr.Addr())
	w("Table=%d", cellMMSTable)
	w("Priority=%d", cellMMSPriority)
	return b.String(), nil
}

// cellNet configures the links off the loop, always to the newest state.
type cellNet struct {
	dir    string // networkd's runtime directory, "" for none
	run    func(name string, args ...string) error
	exists func(iface string) bool
	log    func(format string, args ...any)
	// sim: the modem is telephonyd's simulated one (the selftest's, on a
	// phone too, whose own Shell configured the real modem's link).
	sim bool

	mu        sync.Mutex
	want, mms *cellLink
	kick      chan struct{}
	// up: the interfaces configured, by name, with their files' text (the
	// worker's own).
	up map[string]string
}

// newCellNet: networkd says whether systemd-networkd runs and the Shell
// may write its files (the VPN's finding: root, networkd on the bus); sim
// that the modem is the simulated one.
func newCellNet(logf func(string, ...any), networkd, sim bool) *cellNet {
	n := &cellNet{log: logf, sim: sim, kick: make(chan struct{}, 1), up: map[string]string{}, run: func(name string, args ...string) error {
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}, exists: func(iface string) bool {
		_, err := net.InterfaceByName(iface)
		return err == nil
	}}
	if _, err := exec.LookPath("networkctl"); err != nil || !networkd {
		return n
	}
	n.dir = networkdDir
	n.adopt()
	go n.work()
	return n
}

// adopt takes over what a previous Shell configured: its files are in /run.
// With the simulated modem only the simulated link's: on a phone the
// selftest's Shell would otherwise take the real modem's link down (its
// address, its default route) for as long as it runs.
func (n *cellNet) adopt() {
	names, _ := filepath.Glob(filepath.Join(n.dir, cellNetFile("*")))
	for _, name := range names {
		raw, err := os.ReadFile(name)
		iface := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(name), cellNetPrefix), ".network")
		if n.sim && iface != cellTestIface {
			continue
		}
		if err == nil && !strings.Contains(string(raw), "Unmanaged=yes") && cellIfaceName.MatchString(iface) {
			n.up[iface] = string(raw)
		}
	}
}

// set asks for the data link's and the MMS link's configuration (nil:
// none).
func (n *cellNet) set(want, mms *cellLink) {
	if n.dir == "" {
		return
	}
	n.mu.Lock()
	same := reflect.DeepEqual(n.want, want) && reflect.DeepEqual(n.mms, mms)
	n.want, n.mms = want, mms
	n.mu.Unlock()
	if same {
		return
	}
	select {
	case n.kick <- struct{}{}:
	default:
	}
}

func (n *cellNet) work() {
	for range n.kick {
		n.mu.Lock()
		want, mms := n.want, n.mms
		n.mu.Unlock()
		n.apply(want, mms)
	}
}

func (n *cellNet) apply(want, mms *cellLink) {
	confs := map[string]string{}
	if want != nil {
		if conf, err := cellNetwork(*want); err != nil {
			n.log("mobile data: %s: not configured: %v", want.Iface, err)
		} else {
			confs[want.Iface] = conf
		}
	}
	if mms != nil && (want == nil || mms.Iface != want.Iface) {
		if conf, err := cellMMSNetwork(*mms); err != nil {
			n.log("MMS: %s: not configured: %v", mms.Iface, err)
		} else {
			confs[mms.Iface] = conf
		}
	}
	for name := range n.up {
		if _, ok := confs[name]; !ok {
			n.down(name)
		}
	}
	for _, iface := range slices.Sorted(maps.Keys(confs)) {
		if conf := confs[iface]; n.up[iface] != conf {
			n.configure(iface, conf)
		}
	}
}

func (n *cellNet) configure(iface, conf string) {
	if err := n.write(iface, conf); err != nil {
		n.log("mobile data: %s: %v", iface, err)
		return
	}
	n.up[iface] = conf
	err := n.run("networkctl", "reload")
	if err == nil && n.exists(iface) {
		err = n.run("networkctl", "reconfigure", iface)
	}
	if err != nil {
		n.log("mobile data: %v", err)
	}
	n.log("mobile data: systemd-networkd configures %s", iface)
}

// down takes the link's configuration away; its addresses and routes go
// with it.
func (n *cellNet) down(iface string) {
	delete(n.up, iface)
	if err := n.write(iface, unmanagedConf("Name", iface)); err != nil {
		n.log("mobile data: %s: %v", iface, err)
	}
	err := n.run("networkctl", "reload")
	if n.exists(iface) {
		err = errors.Join(err, n.run("ip", "addr", "flush", "dev", iface), n.run("ip", "link", "set", iface, "down"))
	}
	if err != nil {
		n.log("mobile data: %s down: %v", iface, err)
	}
	n.log("mobile data: %s is down", iface)
}

func (n *cellNet) write(iface, conf string) error {
	if err := os.MkdirAll(n.dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(n.dir, cellNetFile(iface))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(conf), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// applyCellLink follows telephonyd's data link (loop goroutine).
func (c *Core) applyCellLink() {
	n := c.radios.cell
	if n == nil {
		n = newCellNet(c.log.Printf, c.vpn.kind == "networkd", c.tel != nil && c.tel.Backend == "sim")
		c.radios.cell = n
	}
	var want, mms *cellLink
	if c.tel != nil && !c.dataOff() {
		if c.tel.DataState == "connected" && c.tel.DataIP != nil {
			l := *c.tel.DataIP
			want = &l
		}
		if c.tel.MMSLink != nil {
			l := *c.tel.MMSLink
			mms = &l
		}
	}
	n.set(want, mms)
}
