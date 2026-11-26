package vpn

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// A WG_CMD_GET_DEVICE answer split over two messages, as the kernel sends
// a device with many peers: the latest handshake of any peer, the bytes
// of all of them.
func TestParseDevice(t *testing.T) {
	peer := func(sec int64, rx, tx uint64) func(*netlink.AttributeEncoder) error {
		return func(p *netlink.AttributeEncoder) error {
			ts := make([]byte, 16)
			binary.NativeEndian.PutUint64(ts, uint64(sec))
			p.Bytes(unix.WGPEER_A_PUBLIC_KEY, make([]byte, 32))
			p.Bytes(unix.WGPEER_A_LAST_HANDSHAKE_TIME, ts)
			p.Uint64(unix.WGPEER_A_RX_BYTES, rx)
			p.Uint64(unix.WGPEER_A_TX_BYTES, tx)
			return nil
		}
	}
	msg := func(peers ...func(*netlink.AttributeEncoder) error) []byte {
		b := encode(func(ae *netlink.AttributeEncoder) {
			ae.String(unix.WGDEVICE_A_IFNAME, Iface)
			ae.Nested(unix.WGDEVICE_A_PEERS, func(list *netlink.AttributeEncoder) error {
				for i, p := range peers {
					list.Nested(uint16(i), p)
				}
				return nil
			})
		})
		return append([]byte{unix.WG_CMD_GET_DEVICE, 1, 0, 0}, b...)
	}
	st, err := parseDevice([][]byte{msg(peer(1700000000, 100, 50)), msg(peer(1700000300, 7, 3), peer(0, 1, 1))})
	if err != nil {
		t.Fatal(err)
	}
	if st.Peers != 3 || st.Rx != 108 || st.Tx != 54 || !st.Handshake.Equal(time.Unix(1700000300, 0)) {
		t.Errorf("%+v", st)
	}
	if st, _ := parseDevice([][]byte{msg(peer(0, 0, 0))}); !st.Handshake.IsZero() {
		t.Errorf("no handshake yet reads %v", st.Handshake)
	}
	if _, err := parseDevice([][]byte{{1, 2}}); err == nil {
		t.Error("a short message parsed")
	}
}

// netAdmin: the test may change the network (CAP_NET_ADMIN in its
// namespace): run as root, or as `unshare -rn go test ./vpn`.
func netAdmin(t *testing.T) {
	t.Helper()
	if err := DeleteLink("kvpn-none", nil); err != nil {
		t.Skipf("no CAP_NET_ADMIN here (%v): run under unshare -rn", err)
	}
	if LinkExists("eth0", nil) || LinkExists("wlan0", nil) {
		t.Skip("not in a namespace of the test's own: it changes links and rules")
	}
}

// The peer comes up as far as the kernel lets it, and a peer that cannot
// come up leaves nothing behind.
func TestStartTestPeer(t *testing.T) {
	netAdmin(t)
	k, _ := GenerateKey()
	p, err := StartTestPeer(k.Public())
	if HasWireGuard() {
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Close(); err != nil || LinkExists(TestHostVeth, nil) {
			t.Fatalf("close: %v", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "WireGuard interface") {
		t.Fatalf("without WireGuard: %v", err)
	}
	if LinkExists(TestHostVeth, nil) {
		t.Error("the veth pair outlived the failed peer")
	}
}

// The 6T's selftest (39-oneplus-6t.md §16, report 006): a full tunnel up,
// the packets of an app it leaves out carry its mark, skip its table and
// look the main table up, which had no default route (the selftest's
// Shell had taken the mobile data link down): network unreachable. With
// the cellular link's default route (cellnet.go) they leave by it. A veth
// stands for each of the tunnel and the cellular link.
func TestMarkedPacketsNeedTheMainTablesRoute(t *testing.T) {
	netAdmin(t)
	ipCmd, err := exec.LookPath("ip")
	if err != nil {
		t.Skip("no ip(8)")
	}
	ip := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(ipCmd, args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	ns, err := Netns()
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Close()
	for _, l := range []struct{ host, peer, addr string }{{"kvpntun0", "kvpntun1", "10.77.7.2/32"}, {"kvpncell0", "kvpncell1", "100.80.12.34/30"}} {
		if err := Veth(l.host, l.peer, ns); err != nil {
			t.Fatal(err)
		}
		defer DeleteLink(l.host, nil)
		if err := AddAddr(l.host, netip.MustParsePrefix(l.addr), nil); err != nil {
			t.Fatal(err)
		}
		if err := LinkUp(l.host, nil); err != nil {
			t.Fatal(err)
		}
	}
	ip("route", "add", "default", "dev", "kvpntun0", "table", fmt.Sprint(Table))
	ip("rule", "add", "table", "main", "suppress_prefixlength", "0", "priority", fmt.Sprint(PrioritySuppress))
	ip("rule", "add", "not", "fwmark", fmt.Sprint(FwMark), "table", fmt.Sprint(Table), "priority", fmt.Sprint(PriorityTunnel))
	defer DeleteRules(PrioritySuppress, PriorityTunnel)
	dst := netip.MustParseAddr("192.0.2.1")
	if dev, err := RouteDev(dst, 0); err != nil || dev != "kvpntun0" {
		t.Fatalf("unmarked: %q %v, want the tunnel", dev, err)
	}
	if dev, err := RouteDev(dst, FwMark); err == nil || !strings.Contains(err.Error(), "network is unreachable") {
		t.Fatalf("marked, no default route in the main table: %q %v, want network unreachable", dev, err)
	}
	ip("route", "add", "default", "via", "100.80.12.33", "dev", "kvpncell0", "metric", "2048")
	if dev, err := RouteDev(dst, FwMark); err != nil || dev != "kvpncell0" {
		t.Fatalf("marked, with the cellular link's default route: %q %v", dev, err)
	}
	if dev, err := RouteDev(dst, 0); err != nil || dev != "kvpntun0" {
		t.Fatalf("unmarked, with the cellular link's default route: %q %v, want the tunnel", dev, err)
	}
}

// What the device selftest's peer is made of, short of WireGuard: a
// namespace, a veth pair across it, addresses, routes, and the policy
// rules a full tunnel leaves.
func TestNetlinkPlumbing(t *testing.T) {
	netAdmin(t)
	ns, err := Netns()
	if err != nil {
		t.Fatal(err)
	}
	defer ns.Close()
	if err := Veth(TestHostVeth, TestPeerVeth, ns); err != nil {
		t.Fatal(err)
	}
	defer DeleteLink(TestHostVeth, nil)
	if LinkExists(TestPeerVeth, nil) || !LinkExists(TestPeerVeth, ns) {
		t.Fatal("the veth's peer end is not in the namespace")
	}
	for _, s := range []func() error{
		func() error { return AddAddr(TestHostVeth, TestHostAddr, nil) },
		func() error { return LinkUp(TestHostVeth, nil) },
		func() error { return LinkUp("lo", ns) },
		func() error { return AddAddr(TestPeerVeth, TestPeerAddr, ns) },
		func() error { return LinkUp(TestPeerVeth, ns) },
	} {
		if err := s(); err != nil {
			t.Fatal(err)
		}
	}
	if dev, err := RouteDev(TestPeerAddr.Addr(), FwMark); err != nil || dev != TestHostVeth {
		t.Fatalf("route to the peer: %q %v", dev, err)
	}
	var ln net.Listener
	if err := InNetns(ns, func() (err error) {
		ln, err = net.Listen("tcp4", netip.AddrPortFrom(TestPeerAddr.Addr(), EchoPort).String())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Write([]byte("hi"))
			c.Close()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	var conn net.Conn
	for {
		if conn, err = net.DialTimeout("tcp4", ln.Addr().String(), time.Second); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("across the veth pair: %v", err)
	}
	buf := make([]byte, 2)
	if _, err := conn.Read(buf); err != nil || string(buf) != "hi" {
		t.Fatalf("read %q %v", buf, err)
	}
	conn.Close()
	add := func(prio uint32) {
		hdr := []byte{unix.AF_INET, 0, 0, 0, unix.RT_TABLE_COMPAT, 0, 0, unix.FR_ACT_TO_TBL, 0, 0, 0, 0}
		if _, err := rtExec(nil, unix.RTM_NEWRULE, netlink.Acknowledge|netlink.Create|netlink.Excl, append(hdr, encode(func(ae *netlink.AttributeEncoder) {
			ae.Uint32(unix.FRA_PRIORITY, prio)
			ae.Uint32(unix.FRA_TABLE, Table)
		})...)); err != nil {
			t.Fatal(err)
		}
	}
	add(PrioritySuppress)
	add(PriorityTunnel)
	if n, err := DeleteRules(PrioritySuppress, PriorityTunnel); err != nil || n != 2 {
		t.Fatalf("deleted %d rules: %v", n, err)
	}
	rules, _ := Rules()
	for _, r := range rules {
		if r.Priority == PrioritySuppress || r.Priority == PriorityTunnel {
			t.Errorf("rule %+v is still there", r)
		}
	}
	if err := DeleteLink(TestHostVeth, nil); err != nil || LinkExists(TestHostVeth, nil) {
		t.Fatalf("delete: %v", err)
	}
	if err := DeleteLink(TestHostVeth, nil); err != nil {
		t.Errorf("deleting a link that is gone: %v", err)
	}
}
