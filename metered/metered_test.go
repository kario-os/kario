package metered

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"

	"github.com/kario-os/kario/ui"
)

// reply is a DHCPACK (or another type) as a server sends it, with the
// vendor-specific information given.
func reply(xid uint32, typ byte, vendor string) []byte {
	p := make([]byte, bootpLen)
	p[0] = bootReply
	binary.BigEndian.PutUint32(p[4:], xid)
	p = binary.BigEndian.AppendUint32(p, cookie)
	p = append(p, optType, 1, typ, optPad, optRouter, 4, 192, 168, 43, 1)
	if vendor != "" {
		p = append(append(p, optVendor, byte(len(vendor))), vendor...)
	}
	return append(p, optEnd, 0, 0)
}

func TestRequest(t *testing.T) {
	mac := net.HardwareAddr{2, 0x4b, 0x41, 0x52, 0x49, 1}
	p, err := Request(mac, net.IPv4(192, 168, 43, 23), 0xCAFE)
	if err != nil {
		t.Fatal(err)
	}
	if p[0] != bootRequest || binary.BigEndian.Uint32(p[4:]) != 0xCAFE || !net.IP(p[12:16]).Equal(net.IPv4(192, 168, 43, 23)) ||
		!bytes.Equal(p[28:34], mac) || binary.BigEndian.Uint32(p[bootpLen:]) != cookie {
		t.Fatalf("header %x", p[:40])
	}
	opts := p[bootpLen+4:]
	for _, want := range [][]byte{{optType, 1, dhcpRequest}, append([]byte{optClientID, 7, 1}, mac...), {optParams, 4, optSubnet, optRouter, optDNS, optVendor}} {
		if !bytes.Contains(opts, want) {
			t.Fatalf("no option %v in %v", want, opts)
		}
	}
	if opts[len(opts)-1] != optEnd {
		t.Fatal("no end option")
	}
	// A renewal: no requested address (50) nor server identifier (54).
	for _, code := range []byte{50, 54} {
		for o := opts; len(o) > 1 && o[0] != optEnd; o = o[2+int(o[1]):] {
			if o[0] == code {
				t.Fatalf("option %d in a renewal", code)
			}
		}
	}
	if _, err := Request(mac[:4], net.IPv4(1, 2, 3, 4), 1); err == nil {
		t.Fatal("a short hardware address")
	}
	if _, err := Request(mac, net.ParseIP("fe80::1"), 1); err == nil {
		t.Fatal("an IPv6 address")
	}
}

func TestParseReply(t *testing.T) {
	v, ack, err := ParseReply(reply(7, dhcpAck, VendorMetered), 7)
	if err != nil || !ack || !VendorSaysMetered(v) {
		t.Fatalf("%q %v %v", v, ack, err)
	}
	if v, ack, err := ParseReply(reply(7, dhcpAck, ""), 7); err != nil || !ack || VendorSaysMetered(v) {
		t.Fatalf("no vendor information: %q %v %v", v, ack, err)
	}
	if _, ack, err := ParseReply(reply(7, 6, VendorMetered), 7); err != nil || ack {
		t.Fatalf("a DHCPNAK read as an acknowledgement: %v", err)
	}
	if _, _, err := ParseReply(reply(8, dhcpAck, VendorMetered), 7); err == nil {
		t.Fatal("another transaction's answer")
	}
	cut := reply(7, dhcpAck, VendorMetered)
	if _, _, err := ParseReply(cut[:len(cut)-8], 7); err == nil {
		t.Fatal("an option cut short")
	}
	req, _ := Request(net.HardwareAddr{1, 2, 3, 4, 5, 6}, net.IPv4(10, 0, 0, 2), 7)
	if _, _, err := ParseReply(req, 7); err == nil {
		t.Fatal("a request read as an answer")
	}
	for i := 0; i < minReplySize+20; i++ {
		_, _, _ = ParseReply(reply(7, dhcpAck, VendorMetered)[:i], 7)
	}
}

func TestDecide(t *testing.T) {
	for _, c := range []struct {
		choice, hint string
		want         bool
	}{{ui.MeteredAuto, "", false}, {ui.MeteredAuto, ui.MeteredHintDHCP, true}, {"", ui.MeteredHintHotspot, true}, {ui.MeteredYes, "", true},
		{ui.MeteredNo, ui.MeteredHintDHCP, false}, {ui.MeteredNo, "", false}} {
		if got := Decide(c.choice, c.hint); got != c.want {
			t.Errorf("Decide(%q, %q) = %v", c.choice, c.hint, got)
		}
	}
}

func TestHotspotAddress(t *testing.T) {
	for addr, want := range map[string]bool{"172.20.10.2": true, "172.20.10.14": true, "172.20.10.16": false, "192.168.43.77": true,
		"192.168.1.10": false, "10.0.2.15": false, "192.168.44.2": false} {
		if got := HotspotAddress(net.ParseIP(addr)); got != want {
			t.Errorf("%s: %v", addr, got)
		}
	}
}

// The datagram a probe sends is what the packet socket reads back.
func TestUDP(t *testing.T) {
	data := []byte("dhcp")
	p := udpPacket(net.IPv4(192, 168, 43, 23).To4(), net.IPv4bcast.To4(), 68, 67, data)
	var sum uint32
	for i := 0; i < 20; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(p[i:]))
	}
	for sum > 0xFFFF {
		sum = sum&0xFFFF + sum>>16
	}
	if sum != 0xFFFF {
		t.Fatalf("header checksum: %x", sum)
	}
	if got, ok := udpTo(p, 67); !ok || !bytes.Equal(got, data) {
		t.Fatalf("%q %v", got, ok)
	}
	if _, ok := udpTo(p, 68); ok {
		t.Fatal("to another port")
	}
	if _, ok := udpTo(p[:25], 67); ok {
		t.Fatal("a cut datagram")
	}
}
