package netfw

import (
	"encoding/binary"
	"errors"
	"net/netip"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// The VPN's part of the table: the kill switch (Lock) and the apps routed
// around the tunnel (Apps). Both go by the tunnel's firewall mark, which
// the policy rules of a WireGuard tunnel already honour (vpn.FwMark):
// a packet carrying it keeps out of the tunnel's routing table.

// Lock keeps the phone's traffic in the VPN's tunnel (Android's "block
// connections without VPN"): whatever leaves by another interface is
// dropped, the tunnel down or up, except loopback, the tunnel itself,
// packets carrying Mark (the tunnel's own, and the apps Apps keeps out of
// it), UDP to the VPN's Servers, DHCP and IPv6 neighbour discovery (the
// link under the tunnel stays configured) and Links, interfaces that lead
// only to devices nearby (the hotspot's, the USB cable's). Resolve lets
// uid 0 ask DNS servers itself, for a server known by its name only; the
// apps and systemd-resolved stay blocked. Its chain runs before the
// per-app rules, as a cut's, and only adds drops.
type Lock struct {
	Tunnel  string
	Mark    uint32
	Servers []netip.AddrPort
	Links   []string
	Resolve bool
}

// LockChain, AppsChain and AppsNATChain name the VPN's chains.
const (
	LockChain    = "vpn-lock"
	AppsChain    = "vpn-apps"
	AppsNATChain = "vpn-apps-nat"
)

// ApplyLock replaces the kill switch's rules in one transaction; nil lifts
// it.
func (f *Firewall) ApplyLock(l *Lock) error {
	if l == nil {
		return f.dropChain(LockChain)
	}
	c := f.conn
	c.AddTable(f.table)
	ch := c.AddChain(&nftables.Chain{Name: LockChain, Table: f.table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityRef(*nftables.ChainPriorityFilter - 10)})
	c.FlushChain(ch)
	for _, r := range lockRules(*l) {
		c.AddRule(&nftables.Rule{Table: f.table, Chain: ch, Exprs: r})
	}
	return c.Flush()
}

func lockRules(l Lock) [][]expr.Any {
	var out [][]expr.Any
	rule := func(parts ...[]expr.Any) {
		var e []expr.Any
		for _, p := range parts {
			e = append(e, p...)
		}
		out = append(out, e)
	}
	accept := verdict(expr.VerdictAccept)
	rule(ifname(expr.MetaKeyOIFNAME, "lo"), accept)
	rule(ifname(expr.MetaKeyOIFNAME, l.Tunnel), accept)
	for _, i := range l.Links {
		rule(ifname(expr.MetaKeyOIFNAME, i), accept)
	}
	if l.Mark != 0 {
		rule(hasMark(l.Mark), accept)
	}
	for _, s := range l.Servers {
		rule(daddr(s.Addr()), port(unix.IPPROTO_UDP, s.Port()), accept)
	}
	rule(sport(unix.IPPROTO_UDP, 68), port(unix.IPPROTO_UDP, 67), accept)
	rule(sport(unix.IPPROTO_UDP, 546), port(unix.IPPROTO_UDP, 547), accept)
	for _, t := range []byte{133, 135, 136} {
		rule(icmpv6(t), accept)
	}
	if l.Resolve {
		rule(uid(0), port(unix.IPPROTO_UDP, 53), accept)
		rule(uid(0), port(unix.IPPROTO_TCP, 53), accept)
	}
	rule(verdict(expr.VerdictDrop))
	return out
}

// Apps sends only the chosen apps through the tunnel (Only), or all but
// them: their sockets' packets (by uid), or every other packet, get Mark
// on the way out, and the kernel routes them again past the tunnel. A
// socket picked its source address when it connected, by the route
// without the mark: the tunnel's (one of Addrs). Such a packet leaving
// by another interface than Tunnel is masqueraded as that interface's
// address, so its replies find their way back.
type Apps struct {
	Mark   uint32
	Only   bool
	UIDs   []uint32
	Tunnel string
	Addrs  []netip.Addr
}

// ApplyApps replaces the rules in one transaction; nil lifts them.
func (f *Firewall) ApplyApps(a *Apps) error {
	if a == nil {
		return errors.Join(f.dropChain(AppsChain), f.dropChain(AppsNATChain))
	}
	c := f.conn
	c.AddTable(f.table)
	ch := c.AddChain(&nftables.Chain{Name: AppsChain, Table: f.table, Type: nftables.ChainTypeRoute,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityMangle})
	nat := c.AddChain(&nftables.Chain{Name: AppsNATChain, Table: f.table, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource})
	c.FlushChain(ch)
	c.FlushChain(nat)
	for _, r := range appRules(*a) {
		c.AddRule(&nftables.Rule{Table: f.table, Chain: ch, Exprs: r})
	}
	for _, r := range appNATRules(*a) {
		c.AddRule(&nftables.Rule{Table: f.table, Chain: nat, Exprs: r})
	}
	return c.Flush()
}

// appNATRules masquerade the marked packets from a tunnel address that
// leave by another interface.
func appNATRules(a Apps) [][]expr.Any {
	var out [][]expr.Any
	for _, ad := range a.Addrs {
		r := append(hasMark(a.Mark), notIfname(expr.MetaKeyOIFNAME, a.Tunnel)...)
		r = append(r, saddr(ad)...)
		out = append(out, append(r, &expr.Masq{}))
	}
	return out
}

func appRules(a Apps) [][]expr.Any {
	var out [][]expr.Any
	for _, u := range a.UIDs {
		if a.Only {
			out = append(out, append(uid(u), verdict(expr.VerdictAccept)...))
		} else {
			out = append(out, append(uid(u), setMark(a.Mark)...))
		}
	}
	if a.Only {
		out = append(out, setMark(a.Mark))
	}
	return out
}

// Locked reports whether the kill switch is in the table, for the
// selftest on the device.
func (f *Firewall) Locked() (bool, error) {
	ch, err := f.chain(LockChain)
	return ch != nil, err
}

func (f *Firewall) dropChain(name string) error {
	ch, err := f.chain(name)
	if err != nil || ch == nil {
		return err
	}
	f.conn.DelChain(ch)
	return f.conn.Flush()
}

// notIfname matches any interface but name.
func notIfname(key expr.MetaKey, name string) []expr.Any {
	m := ifname(key, name)
	m[1].(*expr.Cmp).Op = expr.CmpOpNeq
	return m
}

func hasMark(m uint32) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: native32(m)},
	}
}

func setMark(m uint32) []expr.Any {
	return []expr.Any{
		&expr.Immediate{Register: 1, Data: native32(m)},
		&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: true, Register: 1},
	}
}

// daddr matches packets to one address, of either family.
func daddr(a netip.Addr) []expr.Any { return addrAt(a, 16, 24) }

// saddr matches packets from one address, of either family.
func saddr(a netip.Addr) []expr.Any { return addrAt(a, 12, 8) }

// addrAt matches an address at its offset in the IPv4 or IPv6 header.
func addrAt(a netip.Addr, off4, off6 uint32) []expr.Any {
	proto, off := byte(unix.NFPROTO_IPV4), off4
	if a.Is6() && !a.Is4In6() {
		proto, off = unix.NFPROTO_IPV6, off6
	}
	ip := a.Unmap().AsSlice()
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: off, Len: uint32(len(ip))},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ip},
	}
}

// sport matches a transport protocol and source port.
func sport(proto byte, p uint16) []expr.Any {
	b := binary.BigEndian.AppendUint16(nil, p)
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 0, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: b},
	}
}

// icmpv6 matches an ICMPv6 message of type t.
func icmpv6(t byte) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_ICMPV6}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 0, Len: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{t}},
	}
}
