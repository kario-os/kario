package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kario-os/kario/vpn"
)

// The VPN's selftest steps (headless.go): a configuration in Files, the
// status bar's indicator, and on the device a WireGuard server in a
// network namespace of its own with the proof that the phone's traffic
// reaches it through the tunnel.

// vpnSample is a profile as a VPN's administrator sends it: a split
// tunnel (or a full one) to a documentation address (RFC 5737), harmless
// on a device, with keys made now.
func vpnSample(full bool) (string, error) {
	own, err := vpn.GenerateKey()
	if err != nil {
		return "", err
	}
	server, err := vpn.GenerateKey()
	if err != nil {
		return "", err
	}
	allowed := "10.77.0.0/24"
	if full {
		allowed = "0.0.0.0/0"
	}
	return fmt.Sprintf("# The office VPN (selftest)\n[Interface]\nPrivateKey = %s\nAddress = 10.77.0.2/32\n\n[Peer]\nPublicKey = %s\nEndpoint = 192.0.2.77:51820\nAllowedIPs = %s\n",
		own, server.Public(), allowed), nil
}

func (r *Runner) vpnStep(cmd, arg string) error {
	c := r.C
	arg = strings.TrimSpace(arg)
	switch cmd {
	case "vpn-file":
		name, kind, _ := strings.Cut(arg, " ")
		conf, err := vpnSample(kind == "full")
		if err == nil {
			_, err = c.files.add(name, []byte(conf), c.now())
		}
		return err
	case "vpn-drop":
		_, ms := lastNum(arg, 20000)
		if c.vpn.st.AlwaysOn == "" || !c.vpnUp() {
			return errors.New("vpn-drop needs the always-on profile connected")
		}
		since := c.vpn.since
		switch be := c.vpn.backend.(type) {
		case *simVPN:
			_ = be.down()
		case *networkdVPN:
			if err := vpn.DeleteLink(vpn.Iface, nil); err != nil {
				return err
			}
		default:
			return errors.New("no VPN")
		}
		if _, ok := r.waitFor(func(string) bool {
			return c.vpnUp() && c.vpn.since.After(since) && (c.vpn.kind != "networkd" || vpn.LinkExists(vpn.Iface, nil))
		}, ms); !ok {
			return fmt.Errorf("the always-on tunnel did not come back (%s)", firstNonEmpty(c.vpn.state, "down"))
		}
	case "vpn-net":
		rest, ms := lastNum(arg, 8000)
		be, ok := c.vpn.backend.(*simVPN)
		if !ok || strings.TrimSpace(rest) == "" {
			return fmt.Errorf("usage: vpn-net <iface> [ms] (the simulated tunnel)")
		}
		be.mu.Lock()
		be.link = strings.TrimSpace(rest)
		ups := be.ups
		be.mu.Unlock()
		if _, ok := r.waitFor(func(string) bool {
			be.mu.Lock()
			defer be.mu.Unlock()
			return be.ups > ups && c.vpnUp()
		}, ms); !ok {
			return errors.New("the tunnel was not connected again on the new network")
		}
	case "vpn-route":
		rest, ms := lastNum(arg, 8000)
		f := strings.Fields(rest)
		if len(f) != 2 || (f[1] != routeTunnel && f[1] != routeDirect && f[1] != routeBlocked) {
			return fmt.Errorf("usage: vpn-route <app-id> tunnel|direct|blocked [ms]")
		}
		var got string
		var err error
		for end := time.Now().Add(time.Duration(ms) * time.Millisecond * time.Duration(max(1, r.Scale))); ; r.pump(200 * time.Millisecond) {
			if got, err = r.vpnSend(f[0]); err == nil && got == f[1] {
				break
			}
			if time.Now().After(end) {
				return fmt.Errorf("%s's packets go %s (%v), want %s; the policies say %s", f[0], got, err, f[1], c.vpnRoute(f[0], vpnTestDst))
			}
		}
		fmt.Fprintf(r.Out, "PHONE-VPN-ROUTE: %s → %s (%s)\n", f[0], got, firstNonEmpty(c.vpn.kind, "none"))
	case "vpn-lock":
		rest, ms := lastNum(arg, 8000)
		want := strings.TrimSpace(rest) == "on"
		var shell, fw bool
		if _, ok := r.waitFor(func(string) bool {
			shell = c.vpnLockOn()
			fw = shell
			if c.vpn.kind == "networkd" && c.traffic != nil {
				fw, _ = c.traffic.fw.Locked()
			}
			return shell == want && fw == want
		}, ms); !ok {
			return fmt.Errorf("the kill switch holds: %v (in the firewall: %v), want %v", shell, fw, want)
		}
	case "vpn":
		rest, ms := lastNum(arg, 8000)
		want := strings.TrimSpace(rest) == "on"
		if rest = strings.TrimSpace(rest); rest != "on" && rest != "off" {
			return fmt.Errorf("usage: vpn on|off [ms]")
		}
		var why string
		if _, ok := r.waitFor(func(s string) bool {
			bar, _, _ := strings.Cut(s, "\n")
			why = ""
			switch {
			case c.frameStatus().VPN != want:
				why = "the status bar's vpn field is " + strconv.FormatBool(!want)
			case strings.Contains(bar, "VPN") != want:
				why = fmt.Sprintf("the status bar reads %q", bar)
			case !want && c.vpn.kind == "networkd" && vpn.LinkExists(vpn.Iface, nil):
				why = vpn.Iface + " is still there"
			case !want && c.vpn.kind == "networkd":
				rules, _ := vpn.Rules()
				for _, rl := range rules {
					if rl.Priority == vpn.PrioritySuppress || rl.Priority == vpn.PriorityTunnel {
						why = fmt.Sprintf("the policy rule %d is still there", rl.Priority)
					}
				}
			}
			return why == ""
		}, ms); !ok {
			return errors.New(why)
		}
	case "vpn-peer":
		if arg == "off" {
			return c.vpnPeerStop()
		}
		return c.vpnPeerStart(arg)
	case "vpn-check":
		return r.vpnCheck(arg)
	}
	return nil
}

// vpnTestDst is where sendAs sends (TEST-NET-1).
var vpnTestDst = netip.MustParseAddr("192.0.2.1")

// directRoute is the interface sendAs's packets leave by when they carry
// the tunnel's mark, as those of an app the VPN leaves out do.
var directRoute = func() (string, error) { return vpn.RouteDev(vpnTestDst, vpn.FwMark) }

// directRouteOff says why the device's packets outside the tunnel have no
// way out, "" when they have. An app the VPN leaves out sends by the
// phone's own network, and so do the packets the kill switch refuses
// (EPERM) before it does: without Wi-Fi or mobile data the kernel turns
// both down at once (network unreachable), and neither can be told.
func (c *Core) directRouteOff() string {
	if c.vpn.kind != "networkd" || c.traffic == nil {
		return ""
	}
	if _, err := directRoute(); err != nil {
		return fmt.Sprintf("no route for the packets to %s outside the tunnel (no Wi-Fi or mobile data): %v", vpnTestDst, err)
	}
	return ""
}

// vpnSend is where the app's packets to vpnTestDst go now: on the device
// three datagrams sent as its uid, which the firewall refuses (EPERM) or
// the tunnel's counter takes in, or neither; elsewhere what the policies
// say.
func (r *Runner) vpnSend(app string) (string, error) {
	c := r.C
	if c.vpn.kind != "networkd" || c.traffic == nil {
		return c.vpnRoute(app, vpnTestDst), nil
	}
	_, st0, _ := c.vpn.backend.status()
	err := c.sendAs(app)
	switch {
	case errors.Is(err, syscall.EPERM):
		return routeBlocked, nil
	case err != nil:
		return "", err
	}
	if _, ok := r.waitFor(func(string) bool {
		up, st, _ := c.vpn.backend.status()
		return up && st.Tx >= st0.Tx+300
	}, 1500); ok {
		return routeTunnel, nil
	}
	return routeDirect, nil
}

// canVPNPeer: the device's tunnel, through networkd (the peer then needs
// the kernel's WireGuard; without it vpn-peer fails and says so).
func (c *Core) canVPNPeer() bool { return c.vpn.kind == "networkd" }

func (c *Core) vpnPeerStart(name string) error {
	v := &c.vpn
	switch {
	case v.kind != "networkd":
		return errors.New("the test peer needs the networkd backend")
	case v.st.Pending == nil:
		return errors.New("no key pair waiting: make it in Settings › VPN first")
	}
	if v.peer != nil {
		v.peer.Close()
	}
	p, err := vpn.StartTestPeer(v.st.Pending.Public())
	if err != nil {
		return err
	}
	v.peer = p
	c.log.Printf("vpn: the test peer is up in a namespace of its own, behind %s", vpn.TestHostVeth)
	_, err = c.files.add(name, []byte(p.Conf()), c.now())
	return err
}

func (c *Core) vpnPeerStop() error {
	v := &c.vpn
	if v.peer == nil {
		return nil
	}
	err := v.peer.Close()
	v.peer = nil
	return err
}

// vpnWhile runs f off the loop, which keeps running meanwhile.
func (r *Runner) vpnWhile(f func() error) error {
	done := make(chan error, 1)
	go func() { done <- f() }()
	for {
		select {
		case err := <-done:
			return err
		default:
			r.pump(20 * time.Millisecond)
		}
	}
}

// vpnCheck proves the tunnel to the test peer: a handshake; the tunnel's
// own packets leave by the veth (the endpoint is outside the tunnel),
// everything else goes in; a TCP echo reaches the peer's tunnel address,
// which only the tunnel leads to; a name resolves through the peer's DNS
// server (systemd-resolved asks the tunnel's link); and, given an app,
// what the app sends goes into the tunnel past the per-app firewall.
func (r *Runner) vpnCheck(app string) error {
	c := r.C
	peer := c.vpn.peer
	switch {
	case peer == nil:
		return errors.New("no test peer (vpn-peer)")
	case !c.vpnUp():
		return errors.New("the VPN is not connected")
	}
	scale := time.Duration(max(1, r.Scale))
	// A first packet for the peer starts the handshake.
	if conn, err := net.Dial("udp4", netip.AddrPortFrom(vpn.TestTunnelPeer.Addr(), 9).String()); err == nil {
		_, _ = conn.Write([]byte("kario"))
		conn.Close()
	}
	var st vpn.Stats
	var err error
	if _, ok := r.waitFor(func(string) bool {
		st, err = vpn.DeviceStats(vpn.Iface, nil)
		return err == nil && !st.Handshake.IsZero()
	}, 15000); !ok {
		return fmt.Errorf("no handshake with the peer: %+v %v", st, err)
	}
	if dev, err := vpn.RouteDev(vpn.TestPeerAddr.Addr(), vpn.FwMark); err != nil || dev != vpn.TestHostVeth {
		return fmt.Errorf("the tunnel's own packets leave by %q (%v), not %s", dev, err, vpn.TestHostVeth)
	}
	outside := netip.MustParseAddr("198.51.100.7")
	if dev, err := vpn.RouteDev(outside, 0); err != nil || dev != vpn.Iface {
		return fmt.Errorf("%s goes by %q (%v), not into the tunnel", outside, dev, err)
	}
	target := netip.AddrPortFrom(vpn.TestTunnelPeer.Addr(), vpn.EchoPort).String()
	var resolved []netip.Addr
	err = r.vpnWhile(func() error {
		conn, err := net.DialTimeout("tcp4", target, 5*time.Second*scale)
		if err != nil {
			return fmt.Errorf("the peer's tunnel address: %w", err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second * scale))
		msg := "kario-vpn-echo\n"
		if _, err := io.WriteString(conn, msg); err != nil {
			return err
		}
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(conn, got); err != nil || string(got) != msg {
			return fmt.Errorf("the echo answered %q (%v)", got, err)
		}
		// resolved takes the tunnel's servers from networkd a moment after
		// the link is up; until then the name is not known.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second*scale)
		defer cancel()
		for {
			if resolved, err = net.DefaultResolver.LookupNetIP(ctx, "ip4", vpn.TestName); err == nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("DNS through the tunnel: %w", err)
			case <-time.After(200 * time.Millisecond):
			}
		}
	})
	if err != nil {
		return err
	}
	if len(resolved) == 0 || resolved[0].Unmap() != vpn.TestTunnelPeer.Addr() {
		return fmt.Errorf("%s resolved to %v, not the peer's %s", vpn.TestName, resolved, vpn.TestTunnelPeer.Addr())
	}
	sent := ""
	if app != "" && c.traffic != nil {
		_, st0, _ := c.vpn.backend.status()
		if err := c.sendAs(app); err != nil {
			return fmt.Errorf("%s could not send through the tunnel: %v", app, err)
		}
		// The tunnel counts a packet once it has encrypted and sent it.
		var st1 vpn.Stats
		if _, ok := r.waitFor(func(string) bool { _, st1, _ = c.vpn.backend.status(); return st1.Tx >= st0.Tx+300 }, 5000); !ok {
			return fmt.Errorf("%s sent 300 bytes, the tunnel took %d", app, st1.Tx-st0.Tx)
		}
		sent = fmt.Sprintf("; %s's datagrams went in (%d B)", app, st1.Tx-st0.Tx)
	}
	ps, _ := peer.Stats()
	_, st, _ = c.vpn.backend.status()
	fmt.Fprintf(r.Out, "PHONE-VPN: handshake %s ago; tunnel rx %d B, tx %d B; the peer got %d B; echo to %s and %s → %s through %s; the endpoint by %s%s\n",
		time.Since(st.Handshake).Round(time.Second), st.Rx, st.Tx, ps.Rx, target, vpn.TestName, resolved[0], vpn.Iface, vpn.TestHostVeth, sent)
	return nil
}
