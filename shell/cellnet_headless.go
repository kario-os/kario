package shell

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/kario-os/kario/vpn"
)

// The cellular link's device steps (headless.go). The emulator's simulated
// modem reports a link no device has (simwwan0); a veth pair of that name,
// its other end in a network namespace of its own, stands for the modem's
// network interface, and the steps check what systemd-networkd and
// systemd-resolved made of telephonyd's data_ip.

const cellTestIface, cellTestPeer = "simwwan0", "simwwan0p"

// cellTestNS is the namespace of the veth's other end while it is up.
var cellTestNS *os.File

// canCellNet: the Shell configures the cellular link (root, networkd).
func (c *Core) canCellNet() bool {
	_, err := exec.LookPath("networkctl")
	return err == nil && c.vpn.kind == "networkd"
}

func (r *Runner) cellStep(cmd, arg string) error {
	arg = strings.TrimSpace(arg)
	switch cmd {
	case "cell-veth":
		switch arg {
		case "on":
			if cellTestNS != nil {
				return errors.New("the veth is up already")
			}
			ns, err := vpn.Netns()
			if err != nil {
				return err
			}
			_ = vpn.DeleteLink(cellTestIface, nil)
			if err := vpn.Veth(cellTestIface, cellTestPeer, ns); err != nil {
				ns.Close()
				return fmt.Errorf("veth: %w", err)
			}
			if err := vpn.LinkUp(cellTestPeer, ns); err != nil {
				_ = vpn.DeleteLink(cellTestIface, nil)
				ns.Close()
				return err
			}
			cellTestNS = ns
		case "off":
			err := vpn.DeleteLink(cellTestIface, nil)
			if cellTestNS != nil {
				cellTestNS.Close()
				cellTestNS = nil
			}
			return err
		default:
			return errors.New("usage: cell-veth on|off")
		}
	case "cell-link":
		rest, ms := lastNum(arg, 20000)
		want := strings.TrimSpace(rest)
		var why string
		if _, ok := r.waitFor(func(string) bool {
			why = r.C.cellLinkCheck(want)
			return why == ""
		}, ms); !ok {
			return fmt.Errorf("%s: %s", cellTestIface, why)
		}
	}
	return nil
}

// cellLinkCheck says what of the link is not yet as telephonyd reports it:
// the address want (a prefix) with the MTU, the default route via the
// gateway after Wi-Fi's, and the DNS servers in systemd-resolved for the
// link; with want "none", no IPv4 address and an Unmanaged file.
func (c *Core) cellLinkCheck(want string) string {
	ifi, err := net.InterfaceByName(cellTestIface)
	if err != nil {
		return err.Error()
	}
	addrs, _ := ifi.Addrs()
	var have []string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			have = append(have, n.String())
		}
	}
	if want == "none" {
		raw, _ := os.ReadFile(filepath.Join(networkdDir, cellNetFile(cellTestIface)))
		switch {
		case len(have) > 0:
			return fmt.Sprintf("IPv4 addresses %v", have)
		case !strings.Contains(string(raw), "Unmanaged=yes"):
			return "its file is not Unmanaged"
		}
		return ""
	}
	if !slices.Contains(have, want) {
		return fmt.Sprintf("IPv4 addresses %v, want %s", have, want)
	}
	if c.tel == nil || c.tel.DataIP == nil || c.tel.DataIP.IPv4 == nil {
		return "telephonyd reports no IPv4 link"
	}
	link := c.tel.DataIP
	if ifi.MTU != link.MTU {
		return fmt.Sprintf("MTU %d, want %d", ifi.MTU, link.MTU)
	}
	out, err := exec.Command("ip", "-4", "route", "show", "dev", cellTestIface).Output()
	if err != nil {
		return "ip route: " + err.Error()
	}
	route := false
	for _, l := range strings.Split(string(out), "\n") {
		route = route || strings.HasPrefix(l, "default via "+link.IPv4.Gateway+" ") && strings.Contains(l, " metric "+strconv.Itoa(cellMetric))
	}
	if !route {
		return fmt.Sprintf("routes %q, want the default via %s, metric %d", out, link.IPv4.Gateway, cellMetric)
	}
	dns, err := resolvedDNS(ifi.Index)
	if err != nil {
		return "systemd-resolved: " + err.Error()
	}
	for _, d := range link.IPv4.DNS {
		if a, err := netip.ParseAddr(d); err != nil || !slices.Contains(dns, a) {
			return fmt.Sprintf("systemd-resolved has %v for the link, want %s", dns, d)
		}
	}
	return ""
}

// resolvedDNS is the DNS servers systemd-resolved has for a link.
func resolvedDNS(index int) ([]netip.Addr, error) {
	bus, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	defer bus.Close()
	v, err := bus.Object("org.freedesktop.resolve1", "/org/freedesktop/resolve1").GetProperty("org.freedesktop.resolve1.Manager.DNS")
	if err != nil {
		return nil, err
	}
	var servers []struct {
		Index   int32
		Family  int32
		Address []byte
	}
	if err := v.Store(&servers); err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, s := range servers {
		if a, ok := netip.AddrFromSlice(s.Address); ok && int(s.Index) == index {
			out = append(out, a.Unmap())
		}
	}
	return out, nil
}
