package netfw

import (
	"bytes"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// A tunnel over Wi-Fi counts as Wi-Fi (an app limited to Wi-Fi may use
// it); over the mobile network it is matched by nothing of Wi-Fi's.
func TestWifiLinks(t *testing.T) {
	names := func(ts ...Tunnel) []string {
		var out []string
		for _, m := range wifiLinks(expr.MetaKeyOIFNAME, ts) {
			out = append(out, string(bytes.TrimRight(m[1].(*expr.Cmp).Data, "\x00")))
		}
		return out
	}
	if got := names(); len(got) != 1 || got[0] != WifiPrefix {
		t.Errorf("no tunnel: %q", got)
	}
	if got := names(Tunnel{Name: "kario-vpn", Wifi: true}); len(got) != 2 || got[1] != "kario-vpn" {
		t.Errorf("over Wi-Fi: %q", got)
	}
	if got := names(Tunnel{Name: "kario-vpn"}); len(got) != 1 {
		t.Errorf("over mobile: %q", got)
	}
	// The whole name: "kario-vpn" must not match "kario-vpn2".
	if m := wifiLinks(expr.MetaKeyIIFNAME, []Tunnel{{Name: "kario-vpn", Wifi: true}})[1]; len(m[1].(*expr.Cmp).Data) != 16 {
		t.Errorf("the tunnel is matched by prefix: %v", m[1])
	}
}

// cmps are the values a rule compares with, in order.
func cmps(r []expr.Any) []string {
	var out []string
	for _, e := range r {
		if c, ok := e.(*expr.Cmp); ok {
			out = append(out, string(c.Data))
		}
	}
	return out
}

// pad is an interface's name as a rule compares it (IFNAMSIZ).
func pad(name string) string { return name + strings.Repeat("\x00", 16-len(name)) }

func last(r []expr.Any) expr.Any { return r[len(r)-1] }

// The kill switch lets out loopback, the tunnel, the nearby links, the
// tunnel's mark, the servers, DHCP and neighbour discovery, and drops the
// rest; uid 0's DNS only while a server's name needs resolving.
func TestLockRules(t *testing.T) {
	rs := lockRules(Lock{Tunnel: "kario-vpn", Mark: 51820, Links: []string{"wlan0"},
		Servers: []netip.AddrPort{netip.MustParseAddrPort("192.0.2.7:51820"), netip.MustParseAddrPort("[2001:db8::7]:443")}})
	if v, ok := last(rs[len(rs)-1]).(*expr.Verdict); !ok || v.Kind != expr.VerdictDrop || len(rs[len(rs)-1]) != 1 {
		t.Fatalf("the last rule: %#v", rs[len(rs)-1])
	}
	for _, r := range rs[:len(rs)-1] {
		if v, ok := last(r).(*expr.Verdict); !ok || v.Kind != expr.VerdictAccept {
			t.Fatalf("an exception that does not accept: %#v", r)
		}
	}
	want := [][]string{
		{pad("lo")}, {pad("kario-vpn")}, {pad("wlan0")}, {string(native32(51820))},
		{string([]byte{unix.NFPROTO_IPV4}), string([]byte{192, 0, 2, 7}), string([]byte{unix.IPPROTO_UDP}), "\xca\x6c"},
		{string([]byte{unix.NFPROTO_IPV6}), string(netip.MustParseAddr("2001:db8::7").AsSlice()), string([]byte{unix.IPPROTO_UDP}), "\x01\xbb"},
		{string([]byte{unix.IPPROTO_UDP}), "\x00\x44", string([]byte{unix.IPPROTO_UDP}), "\x00\x43"},
		{string([]byte{unix.IPPROTO_UDP}), "\x02\x22", string([]byte{unix.IPPROTO_UDP}), "\x02\x23"},
		{string([]byte{unix.IPPROTO_ICMPV6}), "\x85"}, {string([]byte{unix.IPPROTO_ICMPV6}), "\x87"}, {string([]byte{unix.IPPROTO_ICMPV6}), "\x88"},
	}
	if len(rs) != len(want)+1 {
		t.Fatalf("%d rules, want %d", len(rs), len(want)+1)
	}
	for i, w := range want {
		if got := cmps(rs[i]); !slices.Equal(got, w) {
			t.Errorf("rule %d compares %q, want %q", i, got, w)
		}
	}
	// The IPv6 address is read 24 bytes into the header, the IPv4 one 16.
	if p := rs[5][2].(*expr.Payload); p.Offset != 24 || p.Len != 16 {
		t.Errorf("IPv6 destination: %+v", p)
	}
	if p := rs[4][2].(*expr.Payload); p.Offset != 16 || p.Len != 4 {
		t.Errorf("IPv4 destination: %+v", p)
	}
	resolve := lockRules(Lock{Tunnel: "kario-vpn", Resolve: true})
	if n := len(resolve); n != 10 || cmps(resolve[7])[0] != string(native32(0)) || cmps(resolve[8])[1] != string([]byte{unix.IPPROTO_TCP}) || cmps(resolve[8])[2] != "\x00\x35" {
		t.Errorf("resolving: %d rules, %q %q", n, cmps(resolve[7]), cmps(resolve[8]))
	}
	for _, r := range lockRules(Lock{Tunnel: "kario-vpn"}) {
		if slices.Contains(cmps(r), string(native32(0))) {
			t.Error("uid 0's DNS let out without a name to resolve")
		}
	}
}

// Only: the chosen uids pass unmarked and everything else gets the mark;
// except: just the chosen uids get it.
func TestAppRules(t *testing.T) {
	only := appRules(Apps{Mark: 51820, Only: true, UIDs: []uint32{10003, 10007}})
	if len(only) != 3 || cmps(only[0])[0] != string(native32(10003)) || last(only[0]).(*expr.Verdict).Kind != expr.VerdictAccept {
		t.Fatalf("only: %#v", only)
	}
	if m, ok := last(only[2]).(*expr.Meta); !ok || m.Key != expr.MetaKeyMARK || !m.SourceRegister || len(only[2]) != 2 {
		t.Fatalf("only, the rest: %#v", only[2])
	}
	except := appRules(Apps{Mark: 51820, UIDs: []uint32{10003}})
	if len(except) != 1 || cmps(except[0])[0] != string(native32(10003)) {
		t.Fatalf("except: %#v", except)
	}
	if imm := except[0][2].(*expr.Immediate); !bytes.Equal(imm.Data, native32(51820)) {
		t.Fatalf("except marks %v", imm.Data)
	}
	if len(appRules(Apps{Mark: 51820})) != 0 {
		t.Error("nobody chosen to keep out, yet a rule")
	}
	nat := appNATRules(Apps{Mark: 51820, Tunnel: "kario-vpn", Addrs: []netip.Addr{netip.MustParseAddr("10.77.7.2"), netip.MustParseAddr("fd00::2")}})
	if len(nat) != 2 {
		t.Fatalf("nat: %d rules", len(nat))
	}
	if got := cmps(nat[0]); !slices.Equal(got, []string{string(native32(51820)), pad("kario-vpn"), string([]byte{unix.NFPROTO_IPV4}), string([]byte{10, 77, 7, 2})}) {
		t.Errorf("nat v4 compares %q", got)
	}
	if c := nat[0][3].(*expr.Cmp); c.Op != expr.CmpOpNeq {
		t.Error("the tunnel's own packets masqueraded")
	}
	if p := nat[1][6].(*expr.Payload); p.Offset != 8 || p.Len != 16 {
		t.Errorf("IPv6 source: %+v", p)
	}
	if _, ok := last(nat[1]).(*expr.Masq); !ok {
		t.Error("not masqueraded")
	}
}

// The cable's link: its services accept anything, a draining port only
// what belongs to connections conntrack has seen both ways, counted; the
// rest is dropped. Without a port draining no rule asks conntrack.
func TestLinkRules(t *testing.T) {
	rs := linkRules(Link{Name: "usb0", UDP: []uint16{67}, TCP: []uint16{80}, Drain: []uint16{22}})
	want := [][]string{
		{pad("usb0"), string([]byte{unix.IPPROTO_UDP}), "\x00\x43"},
		{pad("usb0"), string([]byte{unix.IPPROTO_TCP}), "\x00\x50"},
		{pad("usb0"), string([]byte{unix.IPPROTO_TCP}), "\x00\x16", string(native32(0))},
		{pad("usb0")},
	}
	if len(rs) != len(want) {
		t.Fatalf("%d rules, want %d", len(rs), len(want))
	}
	for i, w := range want {
		if got := cmps(rs[i]); !slices.Equal(got, w) {
			t.Errorf("rule %d compares %q, want %q", i, got, w)
		}
	}
	for i, r := range rs[:3] {
		if v, ok := last(r).(*expr.Verdict); !ok || v.Kind != expr.VerdictAccept {
			t.Errorf("rule %d does not accept: %#v", i, r)
		}
	}
	if v, ok := last(rs[3]).(*expr.Verdict); !ok || v.Kind != expr.VerdictDrop {
		t.Errorf("the last rule does not drop: %#v", rs[3])
	}
	var state *expr.Bitwise
	counted := false
	for _, e := range rs[2] {
		switch x := e.(type) {
		case *expr.Ct:
			if x.Key != expr.CtKeySTATE {
				t.Errorf("the drain reads ct key %d", x.Key)
			}
		case *expr.Bitwise:
			state = x
		case *expr.Counter:
			counted = true
		}
	}
	if state == nil || !bytes.Equal(state.Mask, native32(expr.CtStateBitESTABLISHED|expr.CtStateBitRELATED)) {
		t.Errorf("the drain lets in more than open connections: %#v", state)
	}
	if !counted {
		t.Error("the drain rule counts nothing")
	}
	if c, ok := rs[2][len(rs[2])-3].(*expr.Cmp); !ok || c.Op != expr.CmpOpNeq {
		t.Errorf("the state test: %#v", rs[2][len(rs[2])-3])
	}
	for _, r := range linkRules(Link{Name: "usb0", UDP: []uint16{67}, TCP: []uint16{80, 22}}) {
		for _, e := range r {
			if _, ok := e.(*expr.Ct); ok {
				t.Fatalf("a rule asks conntrack without a port draining: %#v", r)
			}
		}
	}
}

func TestCounterNames(t *testing.T) {
	for _, n := range CounterNames(10003) {
		id, kind, ok := parseCounter(n)
		if !ok || id != 10003 || (kind != "wifi-rx" && kind != "wifi-tx" && kind != "mobile-rx" && kind != "mobile-tx") {
			t.Errorf("%s: %d %q %v", n, id, kind, ok)
		}
	}
	for _, bad := range []string{"x10003-wifi-rx", "u-wifi-rx", "u99999999999-wifi-rx", "u10003"} {
		if _, _, ok := parseCounter(bad); ok {
			t.Errorf("%s accepted", bad)
		}
	}
}
