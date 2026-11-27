// Package netfw is the per-app firewall and traffic meter. On the device
// every app runs under a uid of its own (Android's model), so nftables can
// tell its packets apart: one named counter per uid, network class and
// direction, and a drop rule for the apps the user limits to Wi-Fi.
// Replies are matched through the connection's mark, which the outgoing
// rule sets to the uid. Loopback (the system's own services) is not
// counted.
package netfw

import (
	"encoding/binary"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// Table is the nftables table (family inet) the shell owns.
const Table = "phone"

// WifiPrefix: interfaces named wl… (wlan0, wlp2s0) are Wi-Fi; any other
// interface counts as mobile, so an unknown link is treated as metered.
const WifiPrefix = "wl"

type Policy struct {
	UID      uint32
	WifiOnly bool
}

// Usage is bytes at the IP layer, per class and direction.
type Usage struct {
	WifiRx   uint64 `json:"wifi_rx"`
	WifiTx   uint64 `json:"wifi_tx"`
	MobileRx uint64 `json:"mobile_rx"`
	MobileTx uint64 `json:"mobile_tx"`
}

func (u *Usage) Add(o Usage) {
	u.WifiRx += o.WifiRx
	u.WifiTx += o.WifiTx
	u.MobileRx += o.MobileRx
	u.MobileTx += o.MobileTx
}

func (u Usage) Total() uint64 { return u.WifiRx + u.WifiTx + u.MobileRx + u.MobileTx }

type Firewall struct {
	conn  *nftables.Conn
	table *nftables.Table
}

func Open() (*Firewall, error) {
	conn, err := nftables.New()
	if err != nil {
		return nil, err
	}
	return &Firewall{conn: conn, table: &nftables.Table{Family: nftables.TableFamilyINet, Name: Table}}, nil
}

func CounterName(uid uint32, class, dir string) string {
	return fmt.Sprintf("u%d-%s-%s", uid, class, dir)
}

// Tunnel is an interface that carries apps' packets inside packets of its
// own over another link (the VPN): it counts as that link, so "Wi-Fi only"
// lets an app use it only while it rides Wi-Fi. Its own packets come from
// the kernel, not from an app's uid, so no per-app rule holds them up.
type Tunnel struct {
	Name string
	Wifi bool
}

// wifiLinks match the interfaces that count as Wi-Fi.
func wifiLinks(key expr.MetaKey, tunnels []Tunnel) [][]expr.Any {
	out := [][]expr.Any{ifprefix(key, WifiPrefix)}
	for _, t := range tunnels {
		if t.Wifi {
			out = append(out, ifname(key, t.Name))
		}
	}
	return out
}

// Apply replaces the rules in one transaction. Counters are named objects
// outside the chains, so they keep counting across applies and shell
// restarts; only Harvest resets them.
func (f *Firewall) Apply(ps []Policy, tunnels ...Tunnel) error {
	c := f.conn
	c.AddTable(f.table)
	out := c.AddChain(&nftables.Chain{Name: "out", Table: f.table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityFilter})
	in := c.AddChain(&nftables.Chain{Name: "in", Table: f.table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookInput, Priority: nftables.ChainPriorityFilter})
	c.FlushChain(out)
	c.FlushChain(in)
	rule := func(ch *nftables.Chain, parts ...[]expr.Any) {
		var e []expr.Any
		for _, p := range parts {
			e = append(e, p...)
		}
		c.AddRule(&nftables.Rule{Table: f.table, Chain: ch, Exprs: e})
	}
	rule(out, ifname(expr.MetaKeyOIFNAME, "lo"), verdict(expr.VerdictAccept))
	rule(in, ifname(expr.MetaKeyIIFNAME, "lo"), verdict(expr.VerdictAccept))
	for _, p := range ps {
		for _, n := range CounterNames(p.UID) {
			c.AddObj(&nftables.CounterObj{Table: f.table, Name: n})
		}
		for _, wl := range wifiLinks(expr.MetaKeyOIFNAME, tunnels) {
			rule(out, uid(p.UID), wl, mark(p.UID), count(CounterName(p.UID, "wifi", "tx")), verdict(expr.VerdictAccept))
		}
		if p.WifiOnly {
			rule(out, uid(p.UID), verdict(expr.VerdictDrop))
		} else {
			rule(out, uid(p.UID), mark(p.UID), count(CounterName(p.UID, "mobile", "tx")), verdict(expr.VerdictAccept))
		}
		for _, wl := range wifiLinks(expr.MetaKeyIIFNAME, tunnels) {
			rule(in, marked(p.UID), wl, count(CounterName(p.UID, "wifi", "rx")), verdict(expr.VerdictAccept))
		}
		rule(in, marked(p.UID), count(CounterName(p.UID, "mobile", "rx")), verdict(expr.VerdictAccept))
	}
	return c.Flush()
}

// CounterNames are the counters one uid has.
func CounterNames(uid uint32) []string {
	return []string{CounterName(uid, "wifi", "rx"), CounterName(uid, "wifi", "tx"), CounterName(uid, "mobile", "rx"), CounterName(uid, "mobile", "tx")}
}

// Harvest reads and zeroes every counter atomically, so the caller can add
// the result to totals it keeps on disk. No table yet means no traffic.
func (f *Firewall) Harvest() (map[uint32]Usage, error) {
	tables, err := f.conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(tables, func(t *nftables.Table) bool { return t.Name == Table }) {
		return nil, nil
	}
	objs, err := f.conn.ResetObjects(f.table)
	if err != nil {
		return nil, err
	}
	out := map[uint32]Usage{}
	for _, o := range objs {
		co, ok := o.(*nftables.CounterObj)
		if !ok {
			continue
		}
		id, kind, ok := parseCounter(co.Name)
		if !ok {
			continue
		}
		u := out[id]
		switch kind {
		case "wifi-rx":
			u.WifiRx += co.Bytes
		case "wifi-tx":
			u.WifiTx += co.Bytes
		case "mobile-rx":
			u.MobileRx += co.Bytes
		case "mobile-tx":
			u.MobileTx += co.Bytes
		}
		out[id] = u
	}
	return out, nil
}

// parseCounter splits "u10003-wifi-rx" into the uid and "wifi-rx".
func parseCounter(name string) (uint32, string, bool) {
	rest, ok := strings.CutPrefix(name, "u")
	if !ok {
		return 0, "", false
	}
	num, kind, ok := strings.Cut(rest, "-")
	id, err := strconv.ParseUint(num, 10, 32)
	if !ok || err != nil {
		return 0, "", false
	}
	return uint32(id), kind, true
}

func native32(v uint32) []byte {
	b := make([]byte, 4)
	binary.NativeEndian.PutUint32(b, v)
	return b
}

func uid(u uint32) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeySKUID, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: native32(u)},
	}
}

// ifname matches a whole interface name (IFNAMSIZ, zero padded).
func ifname(key expr.MetaKey, name string) []expr.Any {
	b := make([]byte, 16)
	copy(b, name)
	return []expr.Any{&expr.Meta{Key: key, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: b}}
}

// ifprefix compares only the prefix's bytes, which is how nft's "wl*" works.
func ifprefix(key expr.MetaKey, prefix string) []expr.Any {
	return []expr.Any{&expr.Meta{Key: key, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(prefix)}}
}

func mark(u uint32) []expr.Any {
	return []expr.Any{
		&expr.Immediate{Register: 1, Data: native32(u)},
		&expr.Ct{Key: expr.CtKeyMARK, Register: 1, SourceRegister: true},
	}
}

func marked(u uint32) []expr.Any {
	return []expr.Any{
		&expr.Ct{Key: expr.CtKeyMARK, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: native32(u)},
	}
}

// count names the counter object (NFT_OBJECT_COUNTER).
func count(name string) []expr.Any { return []expr.Any{&expr.Objref{Type: 1, Name: name}} }

func verdict(k expr.VerdictKind) []expr.Any { return []expr.Any{&expr.Verdict{Kind: k}} }

// Link guards a network interface that reaches one other machine (the USB
// link to a computer): only the listed services are reachable over it, and
// every other packet coming in through it is dropped. Its own chain runs
// before the per-app rules, so a drop here is final; no ports close the
// link altogether. Drain are TCP ports closed to new connections whose
// open ones (by conntrack) still come in, so the sessions of a service
// being shut can end; their rule counts what it lets in (LinkDrained).
type Link struct {
	Name  string
	UDP   []uint16
	TCP   []uint16
	Drain []uint16
}

// ApplyLink replaces the link's rules in one transaction.
func (f *Firewall) ApplyLink(l Link) error {
	c := f.conn
	c.AddTable(f.table)
	ch := c.AddChain(&nftables.Chain{Name: LinkChain(l.Name), Table: f.table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookInput, Priority: nftables.ChainPriorityRef(*nftables.ChainPriorityFilter - 10)})
	c.FlushChain(ch)
	for _, r := range linkRules(l) {
		c.AddRule(&nftables.Rule{Table: f.table, Chain: ch, Exprs: r})
	}
	return c.Flush()
}

// LinkChain names a link's chain.
func LinkChain(name string) string { return "link-" + name }

// linkRules: the services, the open connections of the ports draining,
// and a drop for the rest. Matching a connection's state takes conntrack
// (nft_ct), which the per-app rules' connection marks need as well.
func linkRules(l Link) [][]expr.Any {
	var out [][]expr.Any
	rule := func(parts ...[]expr.Any) {
		var e []expr.Any
		for _, p := range parts {
			e = append(e, p...)
		}
		out = append(out, e)
	}
	in := ifname(expr.MetaKeyIIFNAME, l.Name)
	for _, p := range l.UDP {
		rule(in, port(unix.IPPROTO_UDP, p), verdict(expr.VerdictAccept))
	}
	for _, p := range l.TCP {
		rule(in, port(unix.IPPROTO_TCP, p), verdict(expr.VerdictAccept))
	}
	for _, p := range l.Drain {
		rule(in, port(unix.IPPROTO_TCP, p), replies(), []expr.Any{&expr.Counter{}}, verdict(expr.VerdictAccept))
	}
	rule(in, verdict(expr.VerdictDrop))
	return out
}

// LinkDrained counts the packets the link's draining ports let in since
// its rules were last applied.
func (f *Firewall) LinkDrained(name string) (uint64, error) {
	rules, err := f.conn.GetRules(f.table, &nftables.Chain{Name: LinkChain(name), Table: f.table})
	if err != nil {
		return 0, err
	}
	var n uint64
	for _, r := range rules {
		for _, e := range r.Exprs {
			if c, ok := e.(*expr.Counter); ok {
				n += c.Packets
			}
		}
	}
	return n, nil
}

// port matches a transport protocol and destination port.
func port(proto byte, p uint16) []expr.Any {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, p)
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: b},
	}
}

// ApplyCut closes the named interfaces altogether, both ways and for
// forwarded traffic too: the cellular data link while mobile data is off.
// The modem disconnects its bearer as well; the rule keeps a link that
// comes back by itself (or that the modem never dropped) carrying nothing.
// Its chains run before the per-app rules, as a Link's; no interfaces
// lift it.
func (f *Firewall) ApplyCut(name string, ifaces []string) error {
	c := f.conn
	c.AddTable(f.table)
	prio := nftables.ChainPriorityRef(*nftables.ChainPriorityFilter - 10)
	for _, h := range []struct {
		hook *nftables.ChainHook
		keys []expr.MetaKey
	}{
		{nftables.ChainHookInput, []expr.MetaKey{expr.MetaKeyIIFNAME}},
		{nftables.ChainHookOutput, []expr.MetaKey{expr.MetaKeyOIFNAME}},
		{nftables.ChainHookForward, []expr.MetaKey{expr.MetaKeyIIFNAME, expr.MetaKeyOIFNAME}},
	} {
		ch := c.AddChain(&nftables.Chain{Name: CutChain(name, h.hook), Table: f.table, Type: nftables.ChainTypeFilter, Hooknum: h.hook, Priority: prio})
		c.FlushChain(ch)
		for _, i := range ifaces {
			for _, k := range h.keys {
				c.AddRule(&nftables.Rule{Table: f.table, Chain: ch, Exprs: append(ifname(k, i), verdict(expr.VerdictDrop)...)})
			}
		}
	}
	return c.Flush()
}

// CutChain names a cut's chain on one hook.
func CutChain(name string, hook *nftables.ChainHook) string {
	return map[nftables.ChainHook]string{*nftables.ChainHookInput: "cut-in-", *nftables.ChainHookOutput: "cut-out-", *nftables.ChainHookForward: "cut-fwd-"}[*hook] + name
}

// ApplyShare shares the connection of out with the devices on in (the
// personal hotspot): what they send from the subnet src leaves out
// masqueraded as the phone, and of what comes back through out only the
// replies reach them; they reach nothing else through the phone. Its
// accepts do not lift a cut of out (mobile data off): a drop in any chain
// is final. Without in, out or src the sharing is lifted and its chains
// go.
func (f *Firewall) ApplyShare(name, in, out string, src *net.IPNet) error {
	c := f.conn
	if in == "" || out == "" || src == nil || src.IP.To4() == nil {
		for _, n := range []string{shareFwd(name), shareNAT(name)} {
			ch, err := f.chain(n)
			if err != nil {
				return err
			}
			if ch != nil {
				c.DelChain(ch)
			}
		}
		return c.Flush()
	}
	c.AddTable(f.table)
	fwd := c.AddChain(&nftables.Chain{Name: shareFwd(name), Table: f.table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter})
	nat := c.AddChain(&nftables.Chain{Name: shareNAT(name), Table: f.table, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource})
	c.FlushChain(fwd)
	c.FlushChain(nat)
	rule := func(ch *nftables.Chain, parts ...[]expr.Any) {
		var e []expr.Any
		for _, p := range parts {
			e = append(e, p...)
		}
		c.AddRule(&nftables.Rule{Table: f.table, Chain: ch, Exprs: e})
	}
	iin, iout := ifname(expr.MetaKeyIIFNAME, in), ifname(expr.MetaKeyOIFNAME, out)
	rule(fwd, iin, iout, verdict(expr.VerdictAccept))
	rule(fwd, ifname(expr.MetaKeyIIFNAME, out), ifname(expr.MetaKeyOIFNAME, in), replies(), verdict(expr.VerdictAccept))
	rule(fwd, iin, verdict(expr.VerdictDrop))
	rule(fwd, ifname(expr.MetaKeyOIFNAME, in), verdict(expr.VerdictDrop))
	rule(nat, iout, saddr4(src), []expr.Any{&expr.Masq{}})
	return c.Flush()
}

func shareFwd(name string) string { return "share-fwd-" + name }
func shareNAT(name string) string { return "share-nat-" + name }

// chain is the table's chain of that name, nil when there is none.
func (f *Firewall) chain(name string) (*nftables.Chain, error) {
	chains, err := f.conn.ListChainsOfTableFamily(nftables.TableFamilyINet)
	if err != nil {
		return nil, err
	}
	for _, ch := range chains {
		if ch.Table.Name == Table && ch.Name == name {
			return ch, nil
		}
	}
	return nil, nil
}

// replies matches packets of connections already seen both ways (and
// those they bring along, such as ICMP errors).
func replies() []expr.Any {
	return []expr.Any{
		&expr.Ct{Key: expr.CtKeySTATE, Register: 1},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4,
			Mask: native32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED), Xor: native32(0)},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: native32(0)},
	}
}

// saddr4 matches IPv4 packets from the subnet n.
func saddr4(n *net.IPNet) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte(net.IP(n.Mask).To4()), Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(n.IP.Mask(n.Mask).To4())},
	}
}

// Shared reports the interface a sharing masquerades out of ("" when it
// is lifted), for the selftest on the device.
func (f *Firewall) Shared(name string) (string, error) {
	ch, err := f.chain(shareNAT(name))
	if ch == nil || err != nil {
		return "", err
	}
	rules, err := f.conn.GetRules(f.table, ch)
	if err != nil {
		return "", err
	}
	for _, r := range rules {
		for i, e := range r.Exprs {
			if m, ok := e.(*expr.Meta); ok && m.Key == expr.MetaKeyOIFNAME && i+1 < len(r.Exprs) {
				if cmp, ok := r.Exprs[i+1].(*expr.Cmp); ok {
					return strings.TrimRight(string(cmp.Data), "\x00"), nil
				}
			}
		}
	}
	return "", nil
}

// Cut reports the interfaces a cut drops on output (what ApplyCut last
// set), for the selftest on the device.
func (f *Firewall) Cut(name string) ([]string, error) {
	rules, err := f.conn.GetRules(f.table, &nftables.Chain{Name: CutChain(name, nftables.ChainHookOutput), Table: f.table})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rules {
		for _, e := range r.Exprs {
			if cmp, ok := e.(*expr.Cmp); ok {
				out = append(out, strings.TrimRight(string(cmp.Data), "\x00"))
			}
		}
	}
	return out, nil
}
