package vpn

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

// TestPeer is the device selftest's VPN server: a WireGuard interface in a
// network namespace of its own, reached over a veth pair, so a packet to
// its tunnel address can only get there through the tunnel. It answers TCP
// on EchoPort with what it receives, and DNS queries for TestName.
type TestPeer struct {
	ns     *os.File
	key    Key
	echo   net.Listener
	dns    net.PacketConn
	Phone  Key // the phone's public key, the one peer it accepts
	closed bool
}

// The test network: the veth pair's link, the tunnel's addresses.
const (
	TestHostVeth = "vethkvpn0"
	TestPeerVeth = "vethkvpn1"
	TestPeerIf   = "kvpnpeer"
	TestPort     = 51820
	EchoPort     = 7007
	TestName     = "peer.kario-vpn.test"
)

var (
	TestHostAddr   = netip.MustParsePrefix("192.168.251.1/24")
	TestPeerAddr   = netip.MustParsePrefix("192.168.251.2/24")
	TestTunnelPeer = netip.MustParsePrefix("10.77.7.1/24")
	TestTunnelSelf = netip.MustParsePrefix("10.77.7.2/32")
)

// StartTestPeer puts the peer up; phone is the public key it lets in.
func StartTestPeer(phone Key) (*TestPeer, error) {
	key, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	ns, err := Netns()
	if err != nil {
		return nil, fmt.Errorf("network namespace: %w", err)
	}
	p := &TestPeer{ns: ns, key: key, Phone: phone}
	steps := []struct {
		what string
		do   func() error
	}{
		{"veth pair", func() error { _ = DeleteLink(TestHostVeth, nil); return Veth(TestHostVeth, TestPeerVeth, ns) }},
		{"address of " + TestHostVeth, func() error { return AddAddr(TestHostVeth, TestHostAddr, nil) }},
		{TestHostVeth + " up", func() error { return LinkUp(TestHostVeth, nil) }},
		{"lo up", func() error { return LinkUp("lo", ns) }},
		{"address of " + TestPeerVeth, func() error { return AddAddr(TestPeerVeth, TestPeerAddr, ns) }},
		{TestPeerVeth + " up", func() error { return LinkUp(TestPeerVeth, ns) }},
		{"WireGuard interface", func() error { return WireGuardLink(TestPeerIf, ns) }},
		{"WireGuard keys", func() error {
			return SetDevice(TestPeerIf, ns, key, TestPort, phone, []netip.Prefix{TestTunnelSelf.Masked()})
		}},
		{"tunnel address", func() error { return AddAddr(TestPeerIf, TestTunnelPeer, ns) }},
		{TestPeerIf + " up", func() error { return LinkUp(TestPeerIf, ns) }},
		{"echo and DNS servers", func() error {
			return InNetns(ns, func() error {
				var err error
				if p.echo, err = net.Listen("tcp4", netip.AddrPortFrom(TestTunnelPeer.Addr(), EchoPort).String()); err != nil {
					return err
				}
				p.dns, err = net.ListenPacket("udp4", netip.AddrPortFrom(TestTunnelPeer.Addr(), 53).String())
				return err
			})
		}},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			p.Close()
			return nil, fmt.Errorf("test peer: %s: %w", s.what, err)
		}
	}
	go p.serveEcho()
	go p.serveDNS()
	return p, nil
}

// Conf is the phone's profile for the peer, in wg-quick's format: a full
// tunnel, the peer's DNS server, and no PrivateKey (the phone's own key
// pair completes it).
func (p *TestPeer) Conf() string {
	return fmt.Sprintf("# The device selftest's peer (network namespace, veth %s)\n[Interface]\nAddress = %s\nDNS = %s\n\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = 0.0.0.0/0\nPersistentKeepalive = 5\n",
		TestHostVeth, TestTunnelSelf, TestTunnelPeer.Addr(), p.key.Public(), netip.AddrPortFrom(TestPeerAddr.Addr(), TestPort))
}

// Stats is the peer's side of the tunnel.
func (p *TestPeer) Stats() (Stats, error) { return DeviceStats(TestPeerIf, p.ns) }

func (p *TestPeer) serveEcho() {
	for {
		conn, err := p.echo.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			_, _ = io.Copy(conn, io.LimitReader(conn, 1<<16))
		}()
	}
}

// serveDNS answers TestName's A record with the peer's tunnel address,
// its other types and its parent domains with no records (a resolver may
// ask them first), other names with NXDOMAIN.
func (p *TestPeer) serveDNS() {
	buf := make([]byte, 1500)
	for {
		n, from, err := p.dns.ReadFrom(buf)
		if err != nil {
			return
		}
		var ps dnsmessage.Parser
		h, err := ps.Start(buf[:n])
		if err != nil {
			continue
		}
		q, err := ps.Question()
		if err != nil {
			continue
		}
		res := dnsmessage.Message{Header: dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true,
			RecursionDesired: h.RecursionDesired, RecursionAvailable: true}, Questions: []dnsmessage.Question{q}}
		name := strings.ToLower(strings.TrimSuffix(q.Name.String(), "."))
		switch {
		case name != TestName && !strings.HasSuffix(TestName, "."+name):
			res.RCode = dnsmessage.RCodeNameError
		case name == TestName && q.Type == dnsmessage.TypeA:
			res.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
				Body: &dnsmessage.AResource{A: TestTunnelPeer.Addr().As4()}}}
		}
		if out, err := res.Pack(); err == nil {
			_, _ = p.dns.WriteTo(out, from)
		}
	}
}

// Close takes the peer down: its namespace goes, and the veth pair with it.
func (p *TestPeer) Close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	if p.echo != nil {
		p.echo.Close()
	}
	if p.dns != nil {
		p.dns.Close()
	}
	err := DeleteLink(TestHostVeth, nil)
	return errors.Join(err, p.ns.Close())
}
