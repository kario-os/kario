package metered

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

// Probe asks the DHCP server of the network on ifname, where the phone has
// addr, for its vendor-specific information (Request), and returns it.
// It sends and listens on a packet socket (root, CAP_NET_RAW): the DHCP
// client may hold port 68, and the answer goes to the phone's address (a
// renewal's) or to everyone; the client ignores it, the transaction being
// another's.
func Probe(ifname string, addr net.IP, timeout time.Duration) ([]byte, error) {
	ifc, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, err
	}
	var x [4]byte
	_, _ = rand.Read(x[:])
	xid := binary.BigEndian.Uint32(x[:])
	dhcp, err := Request(ifc.HardwareAddr, addr, xid)
	if err != nil {
		return nil, err
	}
	proto := htons(unix.ETH_P_IP)
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, int(proto))
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: proto, Ifindex: ifc.Index}); err != nil {
		return nil, err
	}
	to := &unix.SockaddrLinklayer{Protocol: proto, Ifindex: ifc.Index, Halen: 6, Addr: [8]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}}
	if err := unix.Sendto(fd, udpPacket(addr.To4(), net.IPv4bcast.To4(), 68, 67, dhcp), 0, to); err != nil {
		return nil, err
	}
	end := time.Now().Add(timeout)
	buf := make([]byte, 2048)
	for {
		left := time.Until(end)
		if left <= 0 {
			return nil, errors.New("metered: no answer from a DHCP server")
		}
		tv := unix.NsecToTimeval(left.Nanoseconds())
		_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
				continue
			}
			return nil, err
		}
		payload, ok := udpTo(buf[:n], 68)
		if !ok {
			continue
		}
		vendor, ack, err := ParseReply(payload, xid)
		if err != nil {
			continue
		}
		if !ack {
			return nil, errors.New("metered: the DHCP server did not acknowledge")
		}
		return vendor, nil
	}
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }
