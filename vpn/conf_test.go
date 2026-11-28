package vpn

import (
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/kario-os/kario/i18n"
)

func TestMain(m *testing.M) {
	i18n.SetLang("en")
	os.Exit(m.Run())
}

const (
	testPriv = "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="
	testPub  = "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg="
	testPSK  = "FpCyhws9cxwWoV4xELtfJvjJN+zQVRPISllRWgeopVE="
)

const sample = `[Interface]
PrivateKey = ` + testPriv + `
Address = 10.77.0.2/24
DNS = 10.77.0.1

[Peer]
PublicKey = ` + testPub + `
Endpoint = vpn.example.com:51820
AllowedIPs = 10.77.0.0/24, 192.168.10.0/24
`

func TestParseSample(t *testing.T) {
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if c.PrivateKey.String() != testPriv || len(c.Peers) != 1 || c.Peers[0].PublicKey.String() != testPub {
		t.Fatalf("keys: %+v", c)
	}
	if got := c.Addresses[0].String(); got != "10.77.0.2/24" {
		t.Errorf("address %s", got)
	}
	if got := c.Peers[0].Endpoint.String(); got != "vpn.example.com:51820" {
		t.Errorf("endpoint %s", got)
	}
	if c.FullTunnel() || len(c.DNS) != 1 || c.DNS[0].String() != "10.77.0.1" {
		t.Errorf("tunnel %v, dns %v", c.FullTunnel(), c.DNS)
	}
}

// Files as people write them: comments, CRLF, case, spacing, a byte order
// mark, repeated list keys, several peers, IPv6, bare addresses.
func TestParseVariants(t *testing.T) {
	raw := "\ufeff# office\r\n[interface]\r\nprivatekey=" + testPriv + "  # mine\r\n" +
		"Address = 10.0.0.2/32\r\nAddress = fd00::2/128, 10.0.1.2\r\nDNS = 1.1.1.1, corp.example, 2606:4700::1111\r\nMTU = 1380\r\nListenPort = 40000\r\n\r\n" +
		"[PEER]\r\nPublicKey = " + testPub + "\r\nPresharedKey = " + testPSK + "\r\nEndpoint = [2001:db8::1]:51820\r\n" +
		"AllowedIPs = 0.0.0.0/0\r\nAllowedIPs = ::/0\r\nPersistentKeepalive = 25\r\n" +
		"[Peer]\nPublicKey = " + testPSK + "\nEndpoint = 198.51.100.4:443\nAllowedIPs = 10.9.0.7/16\nPersistentKeepalive = off\n"
	c, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.2/32", "fd00::2/128", "10.0.1.2/32"}
	for i, a := range c.Addresses {
		if a.String() != want[i] {
			t.Errorf("address %d: %s, want %s", i, a, want[i])
		}
	}
	if len(c.DNS) != 2 || len(c.Search) != 1 || c.Search[0] != "corp.example" {
		t.Errorf("dns %v search %v", c.DNS, c.Search)
	}
	if c.MTU != 1380 || c.ListenPort != 40000 || !c.FullTunnel() || len(c.Peers) != 2 {
		t.Errorf("%+v", c)
	}
	p := c.Peers[0]
	if p.PresharedKey == nil || p.PresharedKey.String() != testPSK || p.Keepalive != 25 || p.Endpoint.String() != "[2001:db8::1]:51820" {
		t.Errorf("peer 1: %+v", p)
	}
	// AllowedIPs are networks: the host bits go, as wg does.
	if got := c.Peers[1].AllowedIPs[0].String(); got != "10.9.0.0/16" || c.Peers[1].Keepalive != 0 {
		t.Errorf("peer 2: %s, keepalive %d", got, c.Peers[1].Keepalive)
	}
	if v4, v6 := c.Families(); !v4 || !v6 {
		t.Errorf("families %v %v", v4, v6)
	}
}

// A file without a PrivateKey parses: the phone's own key pair completes it.
func TestParseWithoutPrivateKey(t *testing.T) {
	c, err := Parse([]byte(strings.Replace(sample, "PrivateKey = "+testPriv+"\n", "", 1)))
	if err != nil || !c.PrivateKey.IsZero() {
		t.Fatalf("%v %v", err, c.PrivateKey)
	}
}

func TestParseRejects(t *testing.T) {
	peer := "[Peer]\nPublicKey = " + testPub + "\nEndpoint = 203.0.113.1:51820\nAllowedIPs = 10.0.0.0/8\n"
	iface := "[Interface]\nPrivateKey = " + testPriv + "\nAddress = 10.0.0.2/32\n"
	for _, c := range []struct{ name, in, want string }{
		{"empty", "", "no [Interface]"},
		{"not wireguard", "<html>", "Key = Value"},
		{"binary", "\xff\xfe[Interface]", "not text"},
		{"nul", iface + "DNS = 1.1.1.1\x00\n" + peer, "Line 4: control"},
		{"escape", iface + "\x1b[2J\n" + peer, "Line 4: control"},
		{"no peer", iface, "no [Peer]"},
		{"no address", "[Interface]\nPrivateKey = " + testPriv + "\n" + peer, "Line 1: [Interface] has no Address"},
		{"two interfaces", iface + iface + peer, "Line 4: a second [Interface] (the first is on line 1)"},
		{"unknown section", iface + "[Server]\n", "unknown section [Server]"},
		{"outside", "Address = 10.0.0.2/32\n" + iface + peer, "Line 1: Address is outside"},
		{"short key", strings.Replace(iface, testPriv, "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBm==", 1) + peer, "Line 2: PrivateKey: not a WireGuard key"},
		{"long key", strings.Replace(iface, testPriv, testPriv[:43]+"kA=", 1) + peer, "not a WireGuard key"},
		{"hex key", strings.Replace(iface, testPriv, strings.Repeat("ab", 32), 1) + peer, "not a WireGuard key"},
		{"unpadded key", strings.Replace(iface, testPriv, strings.TrimSuffix(testPriv, "="), 1) + peer, "not a WireGuard key"},
		{"zero key", strings.Replace(iface, testPriv, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", 1) + peer, "all zero"},
		{"key with space", strings.Replace(iface, testPriv, testPriv[:10]+" "+testPriv[10:], 1) + peer, "not a WireGuard key"},
		{"twice", iface + "PrivateKey = " + testPriv + "\n" + peer, "Line 4: PrivateKey is given twice"},
		{"empty value", iface + "DNS =\n" + peer, "DNS has no value"},
		{"no equals", iface + "MTU 1420\n" + peer, "Line 4: expected Key = Value"},
		{"bad cidr", strings.Replace(iface, "10.0.0.2/32", "10.0.0.2/33", 1) + peer, "Line 3: Address: “10.0.0.2/33” is not an address"},
		{"bad address", strings.Replace(iface, "10.0.0.2/32", "10.0.0.256", 1) + peer, "is not an address"},
		{"zone", strings.Replace(iface, "10.0.0.2/32", "fe80::1%eth0/64", 1) + peer, "is not an address"},
		{"unspecified address", strings.Replace(iface, "10.0.0.2/32", "0.0.0.0/0", 1) + peer, "cannot be this side's address"},
		{"empty list item", strings.Replace(iface, "10.0.0.2/32", "10.0.0.2/32,,10.0.0.3/32", 1) + peer, "empty entry"},
		{"bad dns", iface + "DNS = 1.2.3\n" + peer, "neither a DNS server"},
		{"dns injection", iface + "DNS = 1.1.1.1\\nDNS=6.6.6.6\n" + peer, "neither a DNS server"},
		{"mtu", iface + "MTU = 70000\n" + peer, "is not a number from 576 to 9000"},
		{"mtu v6", strings.Replace(iface, "10.0.0.2/32", "fd00::2/128", 1) + "MTU = 1000\n" + peer, "IPv6 needs an MTU"},
		{"port", iface + "ListenPort = 0\n" + peer, "from 1 to 65535"},
		{"plus port", iface + "ListenPort = +5\n" + peer, "not a number"},
		{"postup", iface + "PostUp = iptables -A FORWARD -i %i -j ACCEPT\n" + peer, "Line 4: PostUp is not supported"},
		{"table", iface + "Table = off\n" + peer, "Table is not supported"},
		{"fwmark", iface + "FwMark = 0x1234\n" + peer, "FwMark is not supported"},
		{"unknown key", iface + "Foo = bar\n" + peer, "Line 4: unknown key Foo in [Interface]"},
		{"peer key in interface", iface + "Endpoint = 1.2.3.4:5\n" + peer, "unknown key Endpoint in [Interface]"},
		{"no public key", iface + "[Peer]\nEndpoint = 203.0.113.1:51820\nAllowedIPs = 10.0.0.0/8\n", "Line 4: [Peer] has no PublicKey"},
		{"no endpoint", iface + "[Peer]\nPublicKey = " + testPub + "\nAllowedIPs = 10.0.0.0/8\n", "no Endpoint"},
		{"no allowed", iface + "[Peer]\nPublicKey = " + testPub + "\nEndpoint = 203.0.113.1:51820\n", "no AllowedIPs"},
		{"endpoint no port", strings.Replace(iface+peer, "203.0.113.1:51820", "203.0.113.1", 1), "is not host:port"},
		{"endpoint port 0", strings.Replace(iface+peer, ":51820", ":0", 1), "is not host:port"},
		{"endpoint port big", strings.Replace(iface+peer, ":51820", ":65536", 1), "is not host:port"},
		{"endpoint v6 bare", strings.Replace(iface+peer, "203.0.113.1:51820", "2001:db8::1:51820", 1), "goes in brackets"},
		{"endpoint bad host", strings.Replace(iface+peer, "203.0.113.1:51820", "vpn_example.com:51820", 1), "is not host:port"},
		{"endpoint space", strings.Replace(iface+peer, "203.0.113.1:51820", "vpn example.com:51820", 1), "is not host:port"},
		{"endpoint numbers", strings.Replace(iface+peer, "203.0.113.1:51820", "1.2.3:51820", 1), "is not host:port"},
		{"endpoint unspecified", strings.Replace(iface+peer, "203.0.113.1:51820", "0.0.0.0:51820", 1), "is not host:port"},
		{"endpoint v6 in v4 brackets", strings.Replace(iface+peer, "203.0.113.1:51820", "[203.0.113.1]:51820", 1), "is not host:port"},
		{"endpoint twice", iface + peer + "Endpoint = 203.0.113.2:51820\n", "Endpoint is given twice"},
		{"keepalive", iface + peer + "PersistentKeepalive = -1\n", "not a number from 0 to 65535"},
		{"own key", iface + strings.Replace(peer, testPub, mustKey(testPriv).Public().String(), 1), "this side's own key"},
		{"same peer twice", iface + peer + strings.Replace(peer, "10.0.0.0/8", "172.16.0.0/12", 1), "Line 8: the same PublicKey as the peer on line 4"},
		{"same route twice", iface + peer + strings.Replace(peer, testPub, testPSK, 1), "AllowedIPs 10.0.0.0/8 is already the peer's on line 4"},
		{"endpoint in tunnel", strings.Replace(iface+peer, "203.0.113.1", "10.1.2.3", 1), "lies inside AllowedIPs 10.0.0.0/8"},
		{"too big", iface + strings.Repeat("# padding padding padding padding padding\n", 2000) + peer, "larger than 64 KB"},
		{"long line", iface + "# " + strings.Repeat("x", 5000) + "\n" + peer, "too long"},
		{"too many peers", iface + strings.Repeat("[Peer]\n", 33), "more than 32 peers"},
	} {
		_, err := Parse([]byte(c.in))
		if err == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %q, want %q in it", c.name, err, c.want)
		}
		if strings.Contains(err.Error(), testPriv) || strings.Contains(err.Error(), testPSK) {
			t.Errorf("%s: the error shows a key: %q", c.name, err)
		}
	}
}

func mustKey(s string) Key {
	k, err := ParseKey(s)
	if err != nil {
		panic(err)
	}
	return k
}

// The public key of a known private key (RFC 7748 §6.1: Alice's pair).
func TestKeys(t *testing.T) {
	priv := mustKey("dwdtCnMYpX08FsFyUbJmRd9ML4frwJkqsXf7pR25LCo=")
	if got := priv.Public().String(); got != "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo=" {
		t.Errorf("public key %s", got)
	}
	a, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GenerateKey()
	if a == b || a.IsZero() || a.Public() == a {
		t.Errorf("generated %s %s", a, b)
	}
	if _, err := ParseKey(a.String()); err != nil {
		t.Error(err)
	}
	var k Key
	if err := k.UnmarshalText([]byte("not a key")); err == nil {
		t.Error("bad text key accepted")
	}
}

func TestEndpointAddr(t *testing.T) {
	e, err := parseEndpoint("VPN.Example.COM.:51820")
	if err != nil || e.Host != "vpn.example.com" || e.String() != "vpn.example.com:51820" {
		t.Fatalf("%+v %v", e, err)
	}
	if _, ok := e.Addr(); ok {
		t.Error("a name has an address")
	}
	e, _ = parseEndpoint("198.51.100.4:443")
	if a, ok := e.Addr(); !ok || a != netip.MustParseAddr("198.51.100.4") {
		t.Errorf("%v %v", a, ok)
	}
	for port, want := range map[string]uint16{"1": 1, "65535": 65535, "0443": 443} {
		if e, err := parseEndpoint("[2001:db8::1]:" + port); err != nil || e.Port != want {
			t.Errorf("port %s: %+v %v", port, e, err)
		}
	}
	for _, port := range []string{"", "0", "+443", "-1", " 443", "65536", "4294967297", "18446744073709551617", "0x1bb", "4_43"} {
		if e, err := parseEndpoint("198.51.100.4:" + port); err == nil {
			t.Errorf("port %q: %+v", port, e)
		}
	}
}
