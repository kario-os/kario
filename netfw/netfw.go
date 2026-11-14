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

// Apply replaces the rules in one transaction. Counters are named objects
// outside the chains, so they keep counting across applies and shell
// restarts; only Harvest resets them.
func (f *Firewall) Apply(ps []Policy) error {
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
		rule(out, uid(p.UID), ifprefix(expr.MetaKeyOIFNAME, WifiPrefix), mark(p.UID), count(CounterName(p.UID, "wifi", "tx")), verdict(expr.VerdictAccept))
		if p.WifiOnly {
			rule(out, uid(p.UID), verdict(expr.VerdictDrop))
		} else {
			rule(out, uid(p.UID), mark(p.UID), count(CounterName(p.UID, "mobile", "tx")), verdict(expr.VerdictAccept))
		}
		rule(in, marked(p.UID), ifprefix(expr.MetaKeyIIFNAME, WifiPrefix), count(CounterName(p.UID, "wifi", "rx")), verdict(expr.VerdictAccept))
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
// link altogether.
type Link struct {
	Name string
	UDP  []uint16
	TCP  []uint16
}

// ApplyLink replaces the link's rules in one transaction.
func (f *Firewall) ApplyLink(l Link) error {
	c := f.conn
	c.AddTable(f.table)
	ch := c.AddChain(&nftables.Chain{Name: "link-" + l.Name, Table: f.table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookInput, Priority: nftables.ChainPriorityRef(*nftables.ChainPriorityFilter - 10)})
	c.FlushChain(ch)
	rule := func(parts ...[]expr.Any) {
		var e []expr.Any
		for _, p := range parts {
			e = append(e, p...)
		}
		c.AddRule(&nftables.Rule{Table: f.table, Chain: ch, Exprs: e})
	}
	in := ifname(expr.MetaKeyIIFNAME, l.Name)
	for _, p := range l.UDP {
		rule(in, port(unix.IPPROTO_UDP, p), verdict(expr.VerdictAccept))
	}
	for _, p := range l.TCP {
		rule(in, port(unix.IPPROTO_TCP, p), verdict(expr.VerdictAccept))
	}
	rule(in, verdict(expr.VerdictDrop))
	return c.Flush()
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
