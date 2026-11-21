package shell

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
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

// cellMetric: after iwd's routes and networkd's DHCP default (1024).
const cellMetric = 2048

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

// cellNet configures the link off the loop, always to the newest state.
type cellNet struct {
	dir    string // networkd's runtime directory, "" for none
	run    func(name string, args ...string) error
	exists func(iface string) bool
	log    func(format string, args ...any)

	mu   sync.Mutex
	want *cellLink
	kick chan struct{}
	// up: the interfaces configured, by name, with their files' text (the
	// worker's own).
	up map[string]string
}

// newCellNet: networkd says whether systemd-networkd runs and the Shell
// may write its files (the VPN's finding: root, networkd on the bus).
func newCellNet(logf func(string, ...any), networkd bool) *cellNet {
	n := &cellNet{log: logf, kick: make(chan struct{}, 1), up: map[string]string{}, run: func(name string, args ...string) error {
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
func (n *cellNet) adopt() {
	names, _ := filepath.Glob(filepath.Join(n.dir, cellNetFile("*")))
	for _, name := range names {
		raw, err := os.ReadFile(name)
		iface := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(name), cellNetPrefix), ".network")
		if err == nil && !strings.Contains(string(raw), "Unmanaged=yes") && cellIfaceName.MatchString(iface) {
			n.up[iface] = string(raw)
		}
	}
}

// set asks for the link's configuration (nil: none).
func (n *cellNet) set(want *cellLink) {
	if n.dir == "" {
		return
	}
	n.mu.Lock()
	same := reflect.DeepEqual(n.want, want)
	n.want = want
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
		want := n.want
		n.mu.Unlock()
		n.apply(want)
	}
}

func (n *cellNet) apply(want *cellLink) {
	iface, conf := "", ""
	if want != nil {
		var err error
		if conf, err = cellNetwork(*want); err != nil {
			n.log("mobile data: %s: not configured: %v", want.Iface, err)
		} else {
			iface = want.Iface
		}
	}
	for name := range n.up {
		if name != iface {
			n.down(name)
		}
	}
	if iface == "" || n.up[iface] == conf {
		return
	}
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
		n = newCellNet(c.log.Printf, c.vpn.kind == "networkd")
		c.radios.cell = n
	}
	var want *cellLink
	if c.tel != nil && c.tel.DataState == "connected" && !c.dataOff() && c.tel.DataIP != nil {
		l := *c.tel.DataIP
		want = &l
	}
	n.set(want)
}
