package shell

import (
	"fmt"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kario-os/kario/ui"
)

// The selftest's steps for the personal hotspot (hotspot.go), listed in
// headless.go: a device joins it (simulated, or on the emulator a real one,
// hotspot_client.go), pings through it, and the firewall's sharing is
// checked.

// hotspotKind is what require-hotspot compares: "sim", "iwd" when the
// emulator's device can be put up as well, else "".
func (c *Core) hotspotKind() string {
	switch w := c.wifi.(type) {
	case *simWifi:
		return "sim"
	case *iwdRadio:
		if w.canHotspotClient() {
			return "iwd"
		}
	}
	return ""
}

// hotspotForwarding is what the hotspot turned forwarding on for (each
// interface's value before); ok is false while an apply runs.
func (c *Core) hotspotForwarding() (map[string]string, bool) {
	if !c.hotspot.mu.TryLock() {
		return nil, false
	}
	defer c.hotspot.mu.Unlock()
	return maps.Clone(c.hotspot.ap.fwd), true
}

func (r *Runner) hotspotStep(cmd, arg string) error {
	c := r.C
	h := &c.hotspot
	switch cmd {
	case "hotspot-client":
		f := strings.Fields(arg)
		if len(f) < 2 {
			return fmt.Errorf("usage: hotspot-client <name> <passphrase> [wpa2] [2.4ghz] [refused]")
		}
		if f[1] == "@" {
			f[1] = c.settings.HotspotPass
		}
		var d simDevice
		refused := false
		for _, flag := range f[2:] {
			switch flag {
			case "wpa2":
				d.wpa2Only = true
			case "2.4ghz":
				d.only24 = true
			case "refused":
				refused = true
			default:
				return fmt.Errorf("hotspot-client: unknown %q", flag)
			}
		}
		switch w := c.wifi.(type) {
		case *simWifi:
			err := w.joinAs(f[0], f[1], d)
			switch {
			case refused && err == nil:
				return fmt.Errorf("%s joined the hotspot", f[0])
			case refused:
				fmt.Fprintf(r.Out, "PHONE-HOTSPOT-CLIENT: %s kept off: %v\n", f[0], err)
				return nil
			}
			return err
		case *iwdRadio:
			if len(f) > 2 {
				return fmt.Errorf("hotspot-client: %v only on the simulated Wi-Fi", f[2:])
			}
			return r.hotspotJoin(w, f[0], c.settings.HotspotSSID, f[1], true)
		}
		return fmt.Errorf("no Wi-Fi")
	case "hotspot-traffic":
		var down, up uint64
		w, ok := c.wifi.(*simWifi)
		if _, err := fmt.Sscanf(arg, "%d %d", &down, &up); err != nil || !ok {
			return fmt.Errorf("usage: hotspot-traffic <down-bytes> <up-bytes> (the simulated Wi-Fi)")
		}
		return w.hotspotTraffic(down, up)
	case "hotspot-seen":
		rest, ms := lastNum(arg, 8000)
		w, ok := c.wifi.(*simWifi)
		want := strings.TrimSpace(rest) == "yes"
		if !ok || (!want && strings.TrimSpace(rest) != "no") {
			return fmt.Errorf("usage: hotspot-seen yes|no [ms] (the simulated Wi-Fi)")
		}
		if _, ok := r.waitFor(func(string) bool { return w.hotspotSeen() == want }, ms); !ok {
			return fmt.Errorf("a device nearby sees the hotspot: %v", !want)
		}
	case "hotspot-usage":
		rest, ms := lastNum(arg, 15000)
		var least uint64
		if _, err := fmt.Sscanf(rest, "%d", &least); err != nil {
			return fmt.Errorf("usage: hotspot-usage <bytes> [ms]")
		}
		hs := c.hotspotRadio()
		var u ui.HotspotUsage
		if _, ok := r.waitFor(func(string) bool {
			if s := sampleCounters(hs); s.ok && h.on {
				c.hotspotCount(s.rx, s.tx, false)
			}
			u = c.hotspotUsageView()
			return u.SessionDown+u.SessionUp >= least && u.TodayDown+u.TodayUp >= least
		}, ms); !ok {
			return fmt.Errorf("the hotspot counted %+v, want %d bytes", u, least)
		}
		fmt.Fprintf(r.Out, "PHONE-HOTSPOT-USAGE: this session %d B down, %d B up; today %d B down, %d B up\n", u.SessionDown, u.SessionUp, u.TodayDown, u.TodayUp)
	case "hotspot-report":
		hs := c.hotspotRadio()
		if hs == nil {
			return fmt.Errorf("no Wi-Fi")
		}
		caps := hs.hotspotCaps()
		devs, err := hs.hotspotDevices()
		fmt.Fprintf(r.Out, "PHONE-HOTSPOT: %s, security %v, hiding %v, bands %v; devices %+v (%v)\n", radioName(c.wifi), caps.securities, caps.hide, caps.bands, devs, err)
		if want := strings.TrimSpace(arg); want != "" && !slices.ContainsFunc(devs, func(d ui.HotspotDevice) bool { return d.Name == want && d.MAC != "" }) {
			return fmt.Errorf("no device named %q on the hotspot", want)
		}
	case "wifi-client":
		f := strings.Fields(arg)
		if len(f) != 3 {
			return fmt.Errorf("usage: wifi-client <name> <ssid> <passphrase>")
		}
		if w, ok := c.wifi.(*iwdRadio); ok {
			return r.hotspotJoin(w, f[0], f[1], f[2], false)
		}
	case "hotspot-client-off", "wifi-client-off":
		if w, ok := c.wifi.(*simWifi); ok {
			w.leaveHotspot(strings.TrimSpace(arg))
			return nil
		}
		cl := h.client
		if cl == nil {
			return nil
		}
		h.client = nil
		cl.end()
		if _, ok := r.waitFor(func(string) bool {
			_, err := os.Stat(filepath.Join("/sys/class/net", cl.iface))
			return err == nil
		}, 10000); !ok {
			return fmt.Errorf("%s did not come back from the device's namespace", cl.iface)
		}
	case "hotspot-ping":
		rest, ms := lastNum(arg, 15000)
		cl := h.client
		if cl == nil {
			return fmt.Errorf("no device on the hotspot (hotspot-client on the emulator)")
		}
		l, _ := cl.lease()
		target := l.router
		if strings.TrimSpace(rest) == "upstream" {
			raw, _ := os.ReadFile(routesFile)
			up, gw := defaultRoute(string(raw), h.iface)
			if gw == nil || gw.IsUnspecified() {
				return fmt.Errorf("the phone has no default gateway (%q)", up)
			}
			target = gw.String()
		} else if strings.TrimSpace(rest) != "phone" {
			return fmt.Errorf("usage: hotspot-ping phone|upstream [ms]")
		}
		var err error
		if _, ok := r.waitFor(func(string) bool { err = cl.ping(target); return err == nil }, ms); !ok {
			return fmt.Errorf("%w\nthe phone's %s: %s\n%s", err, h.iface, phoneSide(h.iface), cl.diag())
		}
		fmt.Fprintf(r.Out, "PHONE-HOTSPOT-PING: %s (%s) answers %s at %s\n", strings.TrimSpace(rest), target, cl.name, l.addr)
	case "hotspot-share":
		rest, ms := lastNum(arg, 8000)
		want := strings.TrimSpace(rest)
		if c.traffic == nil {
			return fmt.Errorf("no firewall")
		}
		var got, target string
		var fwd map[string]string
		if _, ok := r.waitFor(func(string) bool {
			var locked bool
			got, _ = c.traffic.fw.Shared(hotspotShare)
			if fwd, locked = c.hotspotForwarding(); !locked {
				return false
			}
			switch want {
			case "none":
				target = ""
				for i, old := range h.seenFwd {
					if forwarding(i) != old {
						return false
					}
				}
				return got == "" && len(fwd) == 0
			case "default":
				raw, _ := os.ReadFile(routesFile)
				target, _ = defaultRoute(string(raw), h.iface)
			default:
				target = want
			}
			return target != "" && got == target && fwd[h.iface] != "" && fwd[target] != "" && forwarding(h.iface) == "1" && forwarding(target) == "1"
		}, ms); !ok {
			return fmt.Errorf("the firewall shares %q (forwarding before: %v), want %q", got, fwd, target)
		}
		if want != "none" {
			h.seenFwd = fwd
			fmt.Fprintf(r.Out, "PHONE-HOTSPOT-SHARE: %s through %s, the phone's addresses there: %v\n", h.iface, target, ifaceAddrs(h.iface))
		}
	}
	return nil
}

// hotspotJoin puts the emulator's device on the network ssid, waits for
// its address, and reports the lease: on the hotspot, whose lease must
// name a router (the way out), or (router false) on the Wi-Fi network the
// phone is on, which the device only needs to share with the phone.
func (r *Runner) hotspotJoin(w *iwdRadio, name, ssid, pass string, router bool) error {
	h := &r.C.hotspot
	if h.client != nil {
		return fmt.Errorf("a device is on a network already (hotspot-client-off first)")
	}
	cl, err := w.startHotspotClient(name, ssid, pass)
	if err != nil {
		return err
	}
	h.client = cl
	if _, ok := r.waitFor(func(string) bool { return cl.joined() }, 30000); !ok {
		return fmt.Errorf("wpa_supplicant did not join %q (state %q):\n%s", ssid, cl.wpaState(), cl.logText("wpa.log"))
	}
	if err := cl.dhcp(); err != nil {
		return err
	}
	var l hotspotLease
	if _, ok := r.waitFor(func(string) bool {
		var got bool
		l, got = cl.lease()
		return got
	}, 30000); !ok {
		return fmt.Errorf("no address from %q's DHCP server:\n%s\nscript:\n%s\nlease: %q", ssid, cl.logText("udhcpc.log"), cl.logText("udhcpc.sh.log"), cl.logText("udhcpc.sh.lease"))
	}
	if router && l.router == "" {
		return fmt.Errorf("the hotspot's DHCP server gave %s no router, so it has no way out (lease %q)", l.addr, cl.logText("udhcpc.sh.lease"))
	}
	tag := map[bool]string{true: "PHONE-HOTSPOT-CLIENT", false: "PHONE-WIFI-CLIENT"}[router]
	fmt.Fprintf(r.Out, "%s: %s joined %q on %s: address %s, router %s, DNS %v\n", tag, name, ssid, cl.iface, l.addr, l.router, l.dns)
	return nil
}

// phoneSide is the phone's side of the hotspot, for a failure.
func phoneSide(iface string) string {
	addrs := ifaceAddrs(iface)
	arp, _ := os.ReadFile("/proc/net/arp")
	all, err := exec.Command("ip", "-4", "addr").CombinedOutput()
	iwd, err2 := exec.Command("journalctl", "-b", "--no-pager", "-o", "short-monotonic", "-u", "iwd", "-n", "40").CombinedOutput()
	return fmt.Sprintf("addresses %v\nARP:\n%s\nip -4 addr (%v):\n%s\niwd's log (%v):\n%s", addrs, arp, err, all, err2, iwd)
}

func ifaceAddrs(iface string) []string {
	ifc, err := net.InterfaceByName(iface)
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	as, _ := ifc.Addrs()
	for _, a := range as {
		out = append(out, a.String())
	}
	return append(out, ifc.Flags.String())
}
