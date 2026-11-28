// Package metered tells whether a Wi-Fi network is metered: paid for by
// the byte, as a phone's hotspot is. The shell lets system updates, the
// store's automatic updates and apps' background jobs that ask for it wait
// for an unmetered network unless the user starts them; mobile data always
// counts as metered.
//
// The hints, as Android takes them:
//   - DHCP: an Android phone's hotspot sends "ANDROID_METERED" in the
//     vendor-specific information (option 43). iwd, the phone's DHCP
//     client, does not pass it on, so Probe asks the server again with a
//     DHCPREQUEST for the address the phone has (RFC 2131's renewal, as
//     the DHCP client itself sends one) and reads the option off its
//     answer.
//   - The hotspot's addresses: an iPhone's personal hotspot hands out
//     172.20.10.0/28; older Android phones' hotspots and Kario's own
//     192.168.43.0/24.
//
// The user's choice for the network (metered, not metered) comes before
// either.
package metered

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"

	"github.com/kario-os/kario/ui"
)

// VendorMetered is what an Android hotspot's DHCP server puts in option 43.
const VendorMetered = "ANDROID_METERED"

var hotspotNets = []*net.IPNet{
	{IP: net.IPv4(172, 20, 10, 0).To4(), Mask: net.CIDRMask(28, 32)},
	{IP: net.IPv4(192, 168, 43, 0).To4(), Mask: net.CIDRMask(24, 32)},
}

// HotspotAddress: addr is one a phone's hotspot hands out.
func HotspotAddress(addr net.IP) bool {
	for _, n := range hotspotNets {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}

// Decide is whether a network counts as metered: the user's choice
// (ui.MeteredYes, ui.MeteredNo), else what was detected (a hint,
// ui.MeteredHint*, "" for none).
func Decide(choice, hint string) bool {
	switch choice {
	case ui.MeteredYes:
		return true
	case ui.MeteredNo:
		return false
	}
	return hint != ""
}

const (
	bootRequest  = 1
	bootReply    = 2
	dhcpRequest  = 3
	dhcpAck      = 5
	optPad       = 0
	optSubnet    = 1
	optRouter    = 3
	optDNS       = 6
	optVendor    = 43
	optType      = 53
	optParams    = 55
	optClientID  = 61
	optEnd       = 255
	bootpLen     = 236
	cookie       = 0x63825363
	minReplySize = bootpLen + 4
)

// Request is a DHCPREQUEST for the address addr already holds (ciaddr,
// with neither a requested address nor a server's identifier: a renewal),
// from the interface with hardware address mac, asking for the
// vendor-specific information.
func Request(mac net.HardwareAddr, addr net.IP, xid uint32) ([]byte, error) {
	ip := addr.To4()
	if len(mac) != 6 || ip == nil {
		return nil, errors.New("metered: an Ethernet address and an IPv4 address")
	}
	p := make([]byte, bootpLen, bootpLen+32)
	p[0], p[1], p[2] = bootRequest, 1, 6
	binary.BigEndian.PutUint32(p[4:], xid)
	copy(p[12:], ip)
	copy(p[28:], mac)
	p = binary.BigEndian.AppendUint32(p, cookie)
	p = append(p, optType, 1, dhcpRequest)
	p = append(append(p, optClientID, 7, 1), mac...)
	p = append(p, optParams, 4, optSubnet, optRouter, optDNS, optVendor)
	return append(p, optEnd), nil
}

// ParseReply reads the server's answer to the request with xid: whether it
// is an acknowledgement, and its vendor-specific information.
func ParseReply(p []byte, xid uint32) (vendor []byte, ack bool, err error) {
	if len(p) < minReplySize || p[0] != bootReply || binary.BigEndian.Uint32(p[4:]) != xid || binary.BigEndian.Uint32(p[bootpLen:]) != cookie {
		return nil, false, errors.New("metered: not the answer")
	}
	for o := p[bootpLen+4:]; len(o) > 0; {
		code := o[0]
		if code == optEnd {
			break
		}
		if code == optPad {
			o = o[1:]
			continue
		}
		if len(o) < 2 || len(o) < 2+int(o[1]) {
			return nil, false, errors.New("metered: options cut short")
		}
		val := o[2 : 2+int(o[1])]
		switch code {
		case optType:
			ack = len(val) == 1 && val[0] == dhcpAck
		case optVendor:
			vendor = append(vendor, val...)
		}
		o = o[2+len(val):]
	}
	return vendor, ack, nil
}

// VendorSaysMetered: the vendor-specific information carries Android's
// hint.
func VendorSaysMetered(vendor []byte) bool {
	return bytes.Contains(vendor, []byte(VendorMetered))
}

// udpPacket is an IPv4 datagram (UDP without a checksum, which IPv4
// allows).
func udpPacket(src, dst net.IP, sport, dport uint16, data []byte) []byte {
	p := make([]byte, 28, 28+len(data))
	p[0], p[8], p[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(p[2:], uint16(28+len(data)))
	copy(p[12:], src)
	copy(p[16:], dst)
	var sum uint32
	for i := 0; i < 20; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(p[i:]))
	}
	for sum > 0xFFFF {
		sum = sum&0xFFFF + sum>>16
	}
	binary.BigEndian.PutUint16(p[10:], ^uint16(sum))
	binary.BigEndian.PutUint16(p[20:], sport)
	binary.BigEndian.PutUint16(p[22:], dport)
	binary.BigEndian.PutUint16(p[24:], uint16(8+len(data)))
	return append(p, data...)
}

// udpTo is the data of an IPv4 UDP datagram to port.
func udpTo(p []byte, port uint16) ([]byte, bool) {
	if len(p) < 20 || p[0]>>4 != 4 || p[9] != 17 {
		return nil, false
	}
	ihl := int(p[0]&0xF) * 4
	total := int(binary.BigEndian.Uint16(p[2:]))
	if ihl < 20 || total > len(p) || total < ihl+8 || binary.BigEndian.Uint16(p[ihl+2:]) != port {
		return nil, false
	}
	return p[ihl+8 : total], true
}
