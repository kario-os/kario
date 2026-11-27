package shell

import (
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// The names of the devices on the hotspot. iwd's DHCP server keeps its
// leases to itself, so the Shell reads the DHCP traffic on the access
// point's interface (a packet socket, root): the host name a device asks
// with (option 12, what Android's hotspot shows) and the address the
// server acknowledges, by the device's hardware address. A filter in the
// kernel hands it DHCP alone, not the devices' other traffic.

type dhcpLease struct{ name, addr string }

type dhcpLeases struct {
	mu    sync.Mutex
	byMAC map[string]dhcpLease
}

func (l *dhcpLeases) get(mac string) dhcpLease {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byMAC[mac]
}

func (l *dhcpLeases) note(mac, name, addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byMAC == nil {
		l.byMAC = map[string]dhcpLease{}
	}
	e := l.byMAC[mac]
	if name != "" {
		e.name = name
	}
	if addr != "" {
		e.addr = addr
	}
	l.byMAC[mac] = e
}

// dhcpFilter passes unfragmented IPv4 UDP to port 67 or 68 (the packet as
// a SOCK_DGRAM socket gets it, from its IP header).
var dhcpFilter = []bpf.Instruction{
	bpf.LoadAbsolute{Off: 9, Size: 1},
	bpf.JumpIf{Cond: bpf.JumpNotEqual, Val: unix.IPPROTO_UDP, SkipTrue: 7},
	bpf.LoadAbsolute{Off: 6, Size: 2},
	bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x1fff, SkipTrue: 5},
	bpf.LoadMemShift{Off: 0},
	bpf.LoadIndirect{Off: 2, Size: 2},
	bpf.JumpIf{Cond: bpf.JumpEqual, Val: 67, SkipTrue: 1},
	bpf.JumpIf{Cond: bpf.JumpNotEqual, Val: 68, SkipTrue: 1},
	bpf.RetConstant{Val: 0xffff},
	bpf.RetConstant{Val: 0},
}

// snoopDHCP notes the DHCP traffic on iface into leases until stop closes.
func snoopDHCP(iface string, leases *dhcpLeases, stop <-chan struct{}) error {
	ifc, err := net.InterfaceByName(iface)
	if err != nil {
		return err
	}
	raw, err := bpf.Assemble(dhcpFilter)
	if err != nil {
		return err
	}
	prog := make([]unix.SockFilter, len(raw))
	for i, r := range raw {
		prog[i] = unix.SockFilter{Code: r.Op, Jt: r.Jt, Jf: r.Jf, K: r.K}
	}
	proto := int(htons(unix.ETH_P_IP))
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, proto)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err := unix.SetsockoptSockFprog(fd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, &unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}); err != nil {
		return err
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: uint16(proto), Ifindex: ifc.Index}); err != nil {
		return err
	}
	tv := unix.NsecToTimeval(time.Second.Nanoseconds())
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	buf := make([]byte, 2048)
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if mac, name, addr, ok := parseDHCP(buf[:n]); ok {
			leases.note(mac, name, addr)
		}
	}
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// DHCP's fields (RFC 2131, 2132).
const (
	bootpLen      = 236
	dhcpCookie    = 0x63825363
	dhcpOptHost   = 12
	dhcpOptType   = 53
	dhcpOptEnd    = 255
	dhcpOptPad    = 0
	dhcpTypeAck   = 5
	bootpRequest  = 1
	bootpReply    = 2
	maxDeviceName = 64
)

// parseDHCP reads an IPv4 packet: a client's request gives its host name,
// the server's acknowledgement the address it gives; both name the
// client's hardware address.
func parseDHCP(p []byte) (mac, name, addr string, ok bool) {
	if len(p) < 20 || p[0]>>4 != 4 || p[9] != unix.IPPROTO_UDP {
		return "", "", "", false
	}
	ihl := int(p[0]&0xf) * 4
	if ihl < 20 || len(p) < ihl+8+bootpLen+4 {
		return "", "", "", false
	}
	b := p[ihl+8:]
	if b[1] != 1 || b[2] != 6 || binary.BigEndian.Uint32(b[bootpLen:]) != dhcpCookie {
		return "", "", "", false
	}
	mac = net.HardwareAddr(b[28:34]).String()
	var host string
	var typ byte
	for o := b[bootpLen+4:]; len(o) > 0 && o[0] != dhcpOptEnd; {
		if o[0] == dhcpOptPad {
			o = o[1:]
			continue
		}
		if len(o) < 2 || len(o) < 2+int(o[1]) {
			break
		}
		val := o[2 : 2+int(o[1])]
		switch o[0] {
		case dhcpOptHost:
			host = string(val)
		case dhcpOptType:
			if len(val) == 1 {
				typ = val[0]
			}
		}
		o = o[2+len(val):]
	}
	switch {
	case b[0] == bootpRequest:
		return mac, deviceName(host), "", deviceName(host) != ""
	case b[0] == bootpReply && typ == dhcpTypeAck:
		return mac, "", net.IP(b[16:20]).String(), true
	}
	return "", "", "", false
}

// deviceName is a host name fit to show: valid UTF-8, no control
// characters, at most maxDeviceName bytes.
func deviceName(s string) string {
	s = strings.TrimRight(s, "\x00")
	if !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl) {
		return ""
	}
	for len(s) > maxDeviceName {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return strings.TrimSpace(s)
}
