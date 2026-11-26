// Package vpn is the phone's VPN: WireGuard configurations in the format
// wg-quick reads, checked strictly before anything of them reaches the
// system (conf.go), the systemd-networkd files that bring such a tunnel up
// (networkd.go) and, on Linux, what the Shell asks the kernel itself over
// netlink: the tunnel's handshake and counters, the link under it, and the
// peer the device's selftest puts up in a network namespace.
package vpn

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kario-os/kario/i18n"
)

//go:embed locale
var localeFS embed.FS

var tr = i18n.New(localeFS)

// Limits of one configuration: far above any real one, low enough that a
// hostile file costs nothing to reject.
const (
	MaxFile      = 64 << 10
	maxLine      = 4096
	maxPeers     = 32
	maxAddresses = 16
	maxDNS       = 16
	maxAllowed   = 256
)

// Key is a WireGuard key: 32 bytes, written in base64.
type Key [32]byte

// ParseKey reads a key as wg genkey writes it.
func ParseKey(s string) (Key, error) {
	var k Key
	raw, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil || len(raw) != len(k) {
		return k, errors.New(tr.T("not a WireGuard key (base64 of 32 bytes)"))
	}
	copy(k[:], raw)
	if k.IsZero() {
		return k, errors.New(tr.T("not a WireGuard key (all zero)"))
	}
	return k, nil
}

func (k Key) String() string { return base64.StdEncoding.EncodeToString(k[:]) }

func (k Key) IsZero() bool { return k == Key{} }

func (k Key) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

func (k *Key) UnmarshalText(b []byte) error {
	v, err := ParseKey(string(b))
	*k = v
	return err
}

// GenerateKey makes a private key (Curve25519, as wg genkey).
func GenerateKey() (Key, error) {
	var k Key
	p, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return k, err
	}
	copy(k[:], p.Bytes())
	return k, nil
}

// Public is the public key of a private one (wg pubkey).
func (k Key) Public() Key {
	var pub Key
	p, err := ecdh.X25519().NewPrivateKey(k[:])
	if err != nil {
		return pub
	}
	copy(pub[:], p.PublicKey().Bytes())
	return pub
}

// Endpoint is where a peer listens: an IP literal or a DNS name, and a port.
type Endpoint struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
}

func (e Endpoint) String() string {
	if a, err := netip.ParseAddr(e.Host); err == nil {
		return netip.AddrPortFrom(a, e.Port).String()
	}
	return e.Host + ":" + strconv.Itoa(int(e.Port))
}

// Addr is the endpoint's IP when it is written as one.
func (e Endpoint) Addr() (netip.Addr, bool) {
	a, err := netip.ParseAddr(e.Host)
	return a, err == nil
}

type Peer struct {
	PublicKey    Key            `json:"public_key"`
	PresharedKey *Key           `json:"preshared_key,omitempty"`
	Endpoint     Endpoint       `json:"endpoint"`
	AllowedIPs   []netip.Prefix `json:"allowed_ips"`
	// Keepalive: seconds between keepalive packets, 0 for none.
	Keepalive int `json:"keepalive,omitempty"`
}

// Config is one tunnel. PrivateKey is zero when the file had none: the
// phone's own key pair (vpn.keygen) completes it.
type Config struct {
	PrivateKey Key            `json:"private_key"`
	Addresses  []netip.Prefix `json:"addresses"`
	DNS        []netip.Addr   `json:"dns,omitempty"`
	// Search: DNS's entries that are names, as wg-quick takes them.
	Search     []string `json:"search,omitempty"`
	MTU        int      `json:"mtu,omitempty"`
	ListenPort int      `json:"listen_port,omitempty"`
	Peers      []Peer   `json:"peers"`
}

// FullTunnel: a peer takes every address of a family (0.0.0.0/0, ::/0),
// which wg-quick routes through a table of its own.
func (c Config) FullTunnel() bool {
	for _, p := range c.Peers {
		for _, a := range p.AllowedIPs {
			if a.Bits() == 0 {
				return true
			}
		}
	}
	return false
}

// Families are the address families the peers' AllowedIPs have.
func (c Config) Families() (v4, v6 bool) {
	for _, p := range c.Peers {
		for _, a := range p.AllowedIPs {
			v4, v6 = v4 || a.Addr().Is4(), v6 || a.Addr().Is6()
		}
	}
	return v4, v6
}

// Error is a configuration's fault, at a line of the file (0: the file as
// a whole).
type Error struct {
	Line int
	Msg  string
}

func (e *Error) Error() string {
	if e.Line == 0 {
		return e.Msg
	}
	return tr.T("Line %d: %s", e.Line, e.Msg)
}

// hostname is a DNS name as an endpoint or a search domain: letters,
// digits and hyphens in labels of up to 63, separated by dots.
var hostname = regexp.MustCompile(`^(?i:[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.(?i:[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?))*\.?$`)

func validHostname(s string) bool { return len(s) <= 253 && hostname.MatchString(s) }

// unsupported are wg-quick's keys the phone does not do: scripts it would
// run, and its own routing and saving.
var unsupported = map[string]bool{"preup": true, "postup": true, "predown": true, "postdown": true, "table": true, "fwmark": true, "saveconfig": true}

// Parse reads a configuration in wg-quick's format and checks every value:
// keys are base64 of 32 bytes, addresses and AllowedIPs CIDRs, the endpoint
// host:port; what the phone cannot do (scripts, wg-quick's own routing
// keys) and keys it does not know are refused, not ignored.
func Parse(raw []byte) (Config, error) {
	var c Config
	if len(raw) > MaxFile {
		return c, &Error{Msg: tr.T("The file is larger than %d KB", MaxFile>>10)}
	}
	if !utf8.Valid(raw) {
		return c, &Error{Msg: tr.T("The file is not text")}
	}
	raw = bytes.TrimPrefix(raw, []byte("\ufeff"))
	type section struct {
		name string
		line int
	}
	var cur *section
	var iface *section
	var peers []int
	seen := map[string]bool{}
	c.Peers = make([]Peer, 0, maxPeers)
	var peer *Peer
	fail := func(line int, format string, args ...any) (Config, error) {
		return Config{}, &Error{Line: line, Msg: tr.T(format, args...)}
	}
	for n, line := range strings.Split(string(raw), "\n") {
		n++
		line = strings.TrimSuffix(line, "\r")
		if len(line) > maxLine {
			return fail(n, "the line is too long")
		}
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		for _, r := range line {
			if (r < 0x20 && r != '\t') || r == 0x7f {
				return fail(n, "control characters are not allowed")
			}
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			switch strings.ToLower(line) {
			case "[interface]":
				if iface != nil {
					return fail(n, "a second [Interface] (the first is on line %d)", iface.line)
				}
				iface = &section{"interface", n}
				cur, peer = iface, nil
			case "[peer]":
				if len(c.Peers) == maxPeers {
					return fail(n, "more than %d peers", maxPeers)
				}
				c.Peers = append(c.Peers, Peer{})
				peers = append(peers, n)
				cur, peer = &section{"peer", n}, &c.Peers[len(c.Peers)-1]
			default:
				return fail(n, "unknown section %s (only [Interface] and [Peer])", line)
			}
			seen = map[string]bool{}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !ok || key == "" {
			return fail(n, "expected Key = Value")
		}
		if cur == nil {
			return fail(n, "%s is outside [Interface] and [Peer]", key)
		}
		lower := strings.ToLower(key)
		if unsupported[lower] {
			return fail(n, "%s is not supported: the phone runs no scripts and does its own routing", key)
		}
		once := lower == "privatekey" || lower == "mtu" || lower == "listenport" || lower == "publickey" ||
			lower == "presharedkey" || lower == "endpoint" || lower == "persistentkeepalive"
		if once && seen[lower] {
			return fail(n, "%s is given twice", key)
		}
		seen[lower] = true
		if val == "" {
			return fail(n, "%s has no value", key)
		}
		var err error
		switch {
		case cur.name == "interface" && lower == "privatekey":
			c.PrivateKey, err = ParseKey(val)
		case cur.name == "interface" && lower == "address":
			err = eachItem(val, func(s string) error {
				p, err := parseAddress(s)
				switch {
				case err == nil && (p.Addr().IsUnspecified() || p.Addr().IsMulticast() || p.Addr().IsLoopback()):
					err = errors.New(tr.T("%s cannot be this side's address", p.Addr()))
				case err == nil && len(c.Addresses) == maxAddresses:
					err = errors.New(tr.T("more than %d addresses", maxAddresses))
				}
				c.Addresses = append(c.Addresses, p)
				return err
			})
		case cur.name == "interface" && lower == "dns":
			err = eachItem(val, func(s string) error {
				if len(c.DNS)+len(c.Search) == maxDNS {
					return errors.New(tr.T("more than %d DNS entries", maxDNS))
				}
				if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" && !a.IsUnspecified() && !a.IsMulticast() {
					c.DNS = append(c.DNS, a)
					return nil
				}
				if !validHostname(s) || strings.Trim(s, "0123456789.") == "" {
					return errors.New(tr.T("“%s” is neither a DNS server's address nor a search domain", s))
				}
				c.Search = append(c.Search, strings.TrimSuffix(s, "."))
				return nil
			})
		case cur.name == "interface" && lower == "mtu":
			c.MTU, err = number(val, 576, 9000)
		case cur.name == "interface" && lower == "listenport":
			c.ListenPort, err = number(val, 1, 65535)
		case cur.name == "peer" && lower == "publickey":
			peer.PublicKey, err = ParseKey(val)
		case cur.name == "peer" && lower == "presharedkey":
			var k Key
			k, err = ParseKey(val)
			peer.PresharedKey = &k
		case cur.name == "peer" && lower == "endpoint":
			peer.Endpoint, err = parseEndpoint(val)
		case cur.name == "peer" && lower == "allowedips":
			err = eachItem(val, func(s string) error {
				p, err := parseAddress(s)
				if err == nil && len(peer.AllowedIPs) == maxAllowed {
					err = errors.New(tr.T("more than %d AllowedIPs", maxAllowed))
				}
				peer.AllowedIPs = append(peer.AllowedIPs, p.Masked())
				return err
			})
		case cur.name == "peer" && lower == "persistentkeepalive":
			if strings.EqualFold(val, "off") {
				peer.Keepalive = 0
			} else {
				peer.Keepalive, err = number(val, 0, 65535)
			}
		default:
			return fail(n, "unknown key %s in [%s]", key, map[string]string{"interface": "Interface", "peer": "Peer"}[cur.name])
		}
		if err != nil {
			return fail(n, "%s: %v", key, err)
		}
	}
	if iface == nil {
		return fail(0, "The file has no [Interface] section: it is not a WireGuard configuration")
	}
	if len(c.Addresses) == 0 {
		return fail(iface.line, "[Interface] has no Address")
	}
	if len(c.Peers) == 0 {
		return fail(0, "The file has no [Peer] section")
	}
	_, ip6 := c.Families()
	for _, a := range c.Addresses {
		ip6 = ip6 || a.Addr().Is6()
	}
	if c.MTU != 0 && c.MTU < 1280 && ip6 {
		return fail(iface.line, "IPv6 needs an MTU of at least 1280")
	}
	self := c.PrivateKey.Public()
	keys := map[Key]int{}
	routes := map[netip.Prefix]int{}
	for i, p := range c.Peers {
		line := peers[i]
		switch {
		case p.PublicKey.IsZero():
			return fail(line, "[Peer] has no PublicKey")
		case p.Endpoint.Host == "":
			return fail(line, "[Peer] has no Endpoint: the phone has to know where the server is")
		case len(p.AllowedIPs) == 0:
			return fail(line, "[Peer] has no AllowedIPs: nothing would go through the tunnel")
		case !c.PrivateKey.IsZero() && p.PublicKey == self:
			return fail(line, "the peer's PublicKey is this side's own key")
		}
		if first, ok := keys[p.PublicKey]; ok {
			return fail(line, "the same PublicKey as the peer on line %d", first)
		}
		keys[p.PublicKey] = line
		for _, a := range p.AllowedIPs {
			if first, ok := routes[a]; ok {
				return fail(line, "AllowedIPs %s is already the peer's on line %d", a, first)
			}
			routes[a] = line
		}
	}
	if !c.FullTunnel() {
		// Without a table of its own the route to such an endpoint would
		// lead into the tunnel itself.
		for i, p := range c.Peers {
			ep, ok := p.Endpoint.Addr()
			if !ok {
				continue
			}
			for _, q := range c.Peers {
				for _, a := range q.AllowedIPs {
					if a.Contains(ep) {
						return fail(peers[i], "the Endpoint %s lies inside AllowedIPs %s: its packets would go into the tunnel itself", ep, a)
					}
				}
			}
		}
	}
	return c, nil
}

func eachItem(val string, f func(string) error) error {
	for _, s := range strings.Split(val, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			return errors.New(tr.T("an empty entry in the list"))
		}
		if err := f(s); err != nil {
			return err
		}
	}
	return nil
}

// parseAddress reads a CIDR (10.0.0.2/24) or a bare address, which is a
// prefix of its full length as wg takes it.
func parseAddress(s string) (netip.Prefix, error) {
	var p netip.Prefix
	var err error
	if strings.Contains(s, "/") {
		p, err = netip.ParsePrefix(s)
	} else {
		var a netip.Addr
		if a, err = netip.ParseAddr(s); err == nil {
			p = netip.PrefixFrom(a, a.BitLen())
		}
	}
	if err != nil || p.Addr().Zone() != "" || p.Addr().Is4In6() {
		return netip.Prefix{}, errors.New(tr.T("“%s” is not an address with a prefix length (CIDR, e.g. 10.0.0.2/32)", s))
	}
	return p, nil
}

// parseEndpoint reads host:port; an IPv6 address goes in brackets.
func parseEndpoint(s string) (Endpoint, error) {
	bad := errors.New(tr.T("“%s” is not host:port (e.g. vpn.example.com:51820, [2001:db8::1]:51820)", s))
	var host, port string
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]:")
		if end < 0 {
			return Endpoint{}, bad
		}
		host, port = s[1:end], s[end+2:]
		if a, err := netip.ParseAddr(host); err != nil || !a.Is6() || a.Zone() != "" || a.Is4In6() {
			return Endpoint{}, bad
		}
	} else {
		i := strings.LastIndexByte(s, ':')
		if i < 0 {
			return Endpoint{}, bad
		}
		host, port = s[:i], s[i+1:]
		if strings.Contains(host, ":") {
			return Endpoint{}, errors.New(tr.T("“%s”: an IPv6 address goes in brackets, e.g. [2001:db8::1]:51820", s))
		}
		if a, err := netip.ParseAddr(host); err == nil {
			host = a.String()
		} else if !validHostname(host) || strings.Trim(host, "0123456789.") == "" {
			return Endpoint{}, bad
		} else {
			host = strings.ToLower(strings.TrimSuffix(host, "."))
		}
	}
	if a, err := netip.ParseAddr(host); err == nil && (a.IsUnspecified() || a.IsMulticast()) {
		return Endpoint{}, bad
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil || p == 0 {
		return Endpoint{}, bad
	}
	return Endpoint{Host: host, Port: uint16(p)}, nil
}

func number(s string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi || strings.HasPrefix(s, "+") {
		return 0, errors.New(tr.T("“%s” is not a number from %d to %d", s, lo, hi))
	}
	return n, nil
}
