package vpn

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime"
	"time"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// What the Shell asks the kernel itself: networkd says whether the tunnel
// is configured, the kernel's WireGuard (generic netlink) when the last
// handshake was and what went through; rtnetlink which link a packet to
// the endpoint leaves by, and removes the links and rules a tunnel leaves
// behind (networkd does not remove a netdev whose file is gone).

// Stats is a tunnel as its WireGuard device reports it.
type Stats struct {
	// Handshake: the latest with any peer, zero before the first.
	Handshake time.Time
	Rx, Tx    uint64
	Peers     int
}

const vethInfoPeer = 1 // VETH_INFO_PEER (linux/veth.h)

func dial(family int, ns *os.File) (*netlink.Conn, error) {
	cfg := &netlink.Config{}
	if ns != nil {
		cfg.NetNS = int(ns.Fd())
	}
	return netlink.Dial(family, cfg)
}

// wgFamily is the generic netlink family of the kernel's WireGuard; an
// error when the kernel has none.
func wgFamily(c *netlink.Conn) (uint16, error) {
	ae := netlink.NewAttributeEncoder()
	ae.String(unix.CTRL_ATTR_FAMILY_NAME, unix.WG_GENL_NAME)
	attrs, err := ae.Encode()
	if err != nil {
		return 0, err
	}
	msgs, err := c.Execute(netlink.Message{Header: netlink.Header{Type: unix.GENL_ID_CTRL, Flags: netlink.Request},
		Data: append([]byte{unix.CTRL_CMD_GETFAMILY, 1, 0, 0}, attrs...)})
	if err != nil {
		return 0, fmt.Errorf("the kernel has no WireGuard: %w", err)
	}
	for _, m := range msgs {
		if len(m.Data) < 4 {
			continue
		}
		ad, err := netlink.NewAttributeDecoder(m.Data[4:])
		if err != nil {
			return 0, err
		}
		for ad.Next() {
			if ad.Type() == unix.CTRL_ATTR_FAMILY_ID {
				return ad.Uint16(), nil
			}
		}
	}
	return 0, errors.New("the kernel has no WireGuard")
}

// HasWireGuard: the kernel has WireGuard (built in, or its module loaded).
func HasWireGuard() bool {
	c, err := dial(unix.NETLINK_GENERIC, nil)
	if err != nil {
		return false
	}
	defer c.Close()
	_, err = wgFamily(c)
	return err == nil
}

// DeviceStats asks the kernel's WireGuard about iface in the network
// namespace ns (nil: the Shell's).
func DeviceStats(iface string, ns *os.File) (Stats, error) {
	c, err := dial(unix.NETLINK_GENERIC, ns)
	if err != nil {
		return Stats{}, err
	}
	defer c.Close()
	fam, err := wgFamily(c)
	if err != nil {
		return Stats{}, err
	}
	ae := netlink.NewAttributeEncoder()
	ae.String(unix.WGDEVICE_A_IFNAME, iface)
	attrs, err := ae.Encode()
	if err != nil {
		return Stats{}, err
	}
	msgs, err := c.Execute(netlink.Message{Header: netlink.Header{Type: netlink.HeaderType(fam), Flags: netlink.Request | netlink.Dump},
		Data: append([]byte{unix.WG_CMD_GET_DEVICE, 1, 0, 0}, attrs...)})
	if err != nil {
		return Stats{}, err
	}
	var datas [][]byte
	for _, m := range msgs {
		datas = append(datas, m.Data)
	}
	return parseDevice(datas)
}

// parseDevice reads WG_CMD_GET_DEVICE's answer (one or more messages, each
// a genetlink header and attributes; the peers may be spread over them).
func parseDevice(msgs [][]byte) (Stats, error) {
	var st Stats
	for _, data := range msgs {
		if len(data) < 4 {
			return st, errors.New("short WireGuard message")
		}
		ad, err := netlink.NewAttributeDecoder(data[4:])
		if err != nil {
			return st, err
		}
		for ad.Next() {
			if ad.Type() != unix.WGDEVICE_A_PEERS {
				continue
			}
			ad.Nested(func(peers *netlink.AttributeDecoder) error {
				for peers.Next() {
					st.Peers++
					peers.Nested(func(p *netlink.AttributeDecoder) error {
						for p.Next() {
							switch p.Type() {
							case unix.WGPEER_A_LAST_HANDSHAKE_TIME:
								b := p.Bytes()
								if len(b) != 16 {
									return errors.New("bad handshake time")
								}
								sec, nsec := int64(binary.NativeEndian.Uint64(b)), int64(binary.NativeEndian.Uint64(b[8:]))
								if t := time.Unix(sec, nsec); (sec != 0 || nsec != 0) && t.After(st.Handshake) {
									st.Handshake = t
								}
							case unix.WGPEER_A_RX_BYTES:
								st.Rx += p.Uint64()
							case unix.WGPEER_A_TX_BYTES:
								st.Tx += p.Uint64()
							}
						}
						return nil
					})
				}
				return nil
			})
		}
		if err := ad.Err(); err != nil {
			return st, err
		}
	}
	return st, nil
}

// SetDevice configures a WireGuard device (the selftest's peer): its key
// and port, and the one peer it takes, with that peer's addresses.
func SetDevice(iface string, ns *os.File, private Key, port int, peer Key, allowed []netip.Prefix) error {
	c, err := dial(unix.NETLINK_GENERIC, ns)
	if err != nil {
		return err
	}
	defer c.Close()
	fam, err := wgFamily(c)
	if err != nil {
		return err
	}
	ae := netlink.NewAttributeEncoder()
	ae.String(unix.WGDEVICE_A_IFNAME, iface)
	ae.Bytes(unix.WGDEVICE_A_PRIVATE_KEY, private[:])
	ae.Uint16(unix.WGDEVICE_A_LISTEN_PORT, uint16(port))
	ae.Uint32(unix.WGDEVICE_A_FLAGS, unix.WGDEVICE_F_REPLACE_PEERS)
	ae.Nested(unix.WGDEVICE_A_PEERS, func(peers *netlink.AttributeEncoder) error {
		peers.Nested(0, func(p *netlink.AttributeEncoder) error {
			p.Bytes(unix.WGPEER_A_PUBLIC_KEY, peer[:])
			p.Uint32(unix.WGPEER_A_FLAGS, unix.WGPEER_F_REPLACE_ALLOWEDIPS)
			p.Nested(unix.WGPEER_A_ALLOWEDIPS, func(ips *netlink.AttributeEncoder) error {
				for i, a := range allowed {
					ips.Nested(uint16(i), func(ip *netlink.AttributeEncoder) error {
						fam := uint16(unix.AF_INET)
						if a.Addr().Is6() {
							fam = unix.AF_INET6
						}
						ip.Uint16(unix.WGALLOWEDIP_A_FAMILY, fam)
						ip.Bytes(unix.WGALLOWEDIP_A_IPADDR, a.Addr().AsSlice())
						ip.Uint8(unix.WGALLOWEDIP_A_CIDR_MASK, uint8(a.Bits()))
						return nil
					})
				}
				return nil
			})
			return nil
		})
		return nil
	})
	attrs, err := ae.Encode()
	if err != nil {
		return err
	}
	_, err = c.Execute(netlink.Message{Header: netlink.Header{Type: netlink.HeaderType(fam), Flags: netlink.Request | netlink.Acknowledge},
		Data: append([]byte{unix.WG_CMD_SET_DEVICE, 1, 0, 0}, attrs...)})
	return err
}

// ifinfomsg is struct ifinfomsg with the index and the flags to change.
func ifinfomsg(index int32, flags, change uint32) []byte {
	b := make([]byte, unix.SizeofIfInfomsg)
	binary.NativeEndian.PutUint32(b[4:], uint32(index))
	binary.NativeEndian.PutUint32(b[8:], flags)
	binary.NativeEndian.PutUint32(b[12:], change)
	return b
}

func rtExec(ns *os.File, typ uint16, flags netlink.HeaderFlags, data []byte) ([]netlink.Message, error) {
	c, err := dial(unix.NETLINK_ROUTE, ns)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.Execute(netlink.Message{Header: netlink.Header{Type: netlink.HeaderType(typ), Flags: netlink.Request | flags}, Data: data})
}

func encode(f func(ae *netlink.AttributeEncoder)) []byte {
	ae := netlink.NewAttributeEncoder()
	f(ae)
	b, _ := ae.Encode()
	return b
}

// DeleteLink removes the interface; one that is not there is no error.
func DeleteLink(name string, ns *os.File) error {
	_, err := rtExec(ns, unix.RTM_DELLINK, netlink.Acknowledge,
		append(ifinfomsg(0, 0, 0), encode(func(ae *netlink.AttributeEncoder) { ae.String(unix.IFLA_IFNAME, name) })...))
	if errors.Is(err, unix.ENODEV) {
		return nil
	}
	return err
}

// LinkExists: the interface is there in ns.
func LinkExists(name string, ns *os.File) bool {
	_, err := linkIndex(name, ns)
	return err == nil
}

func linkIndex(name string, ns *os.File) (int32, error) {
	msgs, err := rtExec(ns, unix.RTM_GETLINK, 0,
		append(ifinfomsg(0, 0, 0), encode(func(ae *netlink.AttributeEncoder) { ae.String(unix.IFLA_IFNAME, name) })...))
	if err != nil {
		return 0, err
	}
	for _, m := range msgs {
		if len(m.Data) >= unix.SizeofIfInfomsg {
			return int32(binary.NativeEndian.Uint32(m.Data[4:])), nil
		}
	}
	return 0, fmt.Errorf("no link %s", name)
}

// RouteDev is the interface the kernel sends a packet to dst over, the
// packet carrying mark (0: none).
func RouteDev(dst netip.Addr, mark uint32) (string, error) {
	fam := byte(unix.AF_INET)
	if dst.Is6() {
		fam = unix.AF_INET6
	}
	rt := []byte{fam, byte(dst.BitLen()), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	msgs, err := rtExec(nil, unix.RTM_GETROUTE, 0, append(rt, encode(func(ae *netlink.AttributeEncoder) {
		ae.Bytes(unix.RTA_DST, dst.AsSlice())
		if mark != 0 {
			ae.Uint32(unix.RTA_MARK, mark)
		}
	})...))
	if err != nil {
		return "", err
	}
	for _, m := range msgs {
		if len(m.Data) < len(rt) {
			continue
		}
		ad, err := netlink.NewAttributeDecoder(m.Data[len(rt):])
		if err != nil {
			return "", err
		}
		for ad.Next() {
			if ad.Type() == unix.RTA_OIF {
				ifi, err := net.InterfaceByIndex(int(ad.Uint32()))
				if err != nil {
					return "", err
				}
				return ifi.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no route to %s", dst)
}

// Rule is a routing policy rule as the kernel lists it.
type Rule struct {
	Family   byte
	Priority uint32
	Table    uint32
}

// Rules lists the policy rules of both families.
func Rules() ([]Rule, error) {
	msgs, err := rtExec(nil, unix.RTM_GETRULE, netlink.Dump, make([]byte, 12))
	if err != nil {
		return nil, err
	}
	var out []Rule
	for _, m := range msgs {
		if len(m.Data) < 12 {
			continue
		}
		r := Rule{Family: m.Data[0], Table: uint32(m.Data[4])}
		ad, err := netlink.NewAttributeDecoder(m.Data[12:])
		if err != nil {
			return nil, err
		}
		for ad.Next() {
			switch ad.Type() {
			case unix.FRA_PRIORITY:
				r.Priority = ad.Uint32()
			case unix.FRA_TABLE:
				r.Table = ad.Uint32()
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// DeleteRules removes the policy rules with these priorities and reports
// how many there were.
func DeleteRules(prios ...uint32) (int, error) {
	rules, err := Rules()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rules {
		for _, p := range prios {
			if r.Priority != p {
				continue
			}
			// The kernel deletes the first rule matching what is given.
			hdr := []byte{r.Family, 0, 0, 0, unix.RT_TABLE_COMPAT, 0, 0, 0, 0, 0, 0, 0}
			if r.Table < 256 {
				hdr[4] = byte(r.Table)
			}
			_, err := rtExec(nil, unix.RTM_DELRULE, netlink.Acknowledge, append(hdr, encode(func(ae *netlink.AttributeEncoder) {
				ae.Uint32(unix.FRA_PRIORITY, r.Priority)
				ae.Uint32(unix.FRA_TABLE, r.Table)
			})...))
			if err != nil && !errors.Is(err, unix.ENOENT) {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// Netns makes a network namespace; it lives while the file is open.
func Netns() (*os.File, error) {
	var ns *os.File
	err := onThread(func(self *os.File) error {
		if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
			return err
		}
		f, err := os.Open("/proc/thread-self/ns/net")
		ns = f
		return err
	})
	return ns, err
}

// InNetns runs f on a thread in the namespace ns: what it opens (sockets,
// listeners) stays in ns.
func InNetns(ns *os.File, f func() error) error {
	return onThread(func(*os.File) error {
		if err := unix.Setns(int(ns.Fd()), unix.CLONE_NEWNET); err != nil {
			return err
		}
		return f()
	})
}

// onThread runs f on a thread of its own and puts the thread back into the
// Shell's namespace; a thread that cannot go back ends with its goroutine
// instead of running other code.
func onThread(f func(self *os.File) error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		self, err := os.Open("/proc/thread-self/ns/net")
		if err != nil {
			runtime.UnlockOSThread()
			done <- err
			return
		}
		defer self.Close()
		ferr := f(self)
		if err := unix.Setns(int(self.Fd()), unix.CLONE_NEWNET); err != nil {
			done <- err
			return
		}
		runtime.UnlockOSThread()
		done <- ferr
	}()
	return <-done
}

// Veth makes a pair of linked Ethernet interfaces, host here and peer in
// the namespace ns.
func Veth(host, peer string, ns *os.File) error {
	info := encode(func(ae *netlink.AttributeEncoder) {
		ae.String(unix.IFLA_IFNAME, peer)
		ae.Uint32(unix.IFLA_NET_NS_FD, uint32(ns.Fd()))
	})
	_, err := rtExec(nil, unix.RTM_NEWLINK, netlink.Acknowledge|netlink.Create|netlink.Excl, append(ifinfomsg(0, 0, 0), encode(func(ae *netlink.AttributeEncoder) {
		ae.String(unix.IFLA_IFNAME, host)
		ae.Nested(unix.IFLA_LINKINFO, func(li *netlink.AttributeEncoder) error {
			li.String(unix.IFLA_INFO_KIND, "veth")
			li.Nested(unix.IFLA_INFO_DATA, func(d *netlink.AttributeEncoder) error {
				d.Bytes(vethInfoPeer, append(ifinfomsg(0, 0, 0), info...))
				return nil
			})
			return nil
		})
	})...))
	return err
}

// WireGuardLink makes a WireGuard interface in ns (nil: here).
func WireGuardLink(name string, ns *os.File) error {
	_, err := rtExec(ns, unix.RTM_NEWLINK, netlink.Acknowledge|netlink.Create|netlink.Excl, append(ifinfomsg(0, 0, 0), encode(func(ae *netlink.AttributeEncoder) {
		ae.String(unix.IFLA_IFNAME, name)
		ae.Nested(unix.IFLA_LINKINFO, func(li *netlink.AttributeEncoder) error {
			li.String(unix.IFLA_INFO_KIND, "wireguard")
			return nil
		})
	})...))
	return err
}

// LinkUp sets the interface up.
func LinkUp(name string, ns *os.File) error {
	_, err := rtExec(ns, unix.RTM_NEWLINK, netlink.Acknowledge,
		append(ifinfomsg(0, unix.IFF_UP, unix.IFF_UP), encode(func(ae *netlink.AttributeEncoder) { ae.String(unix.IFLA_IFNAME, name) })...))
	return err
}

// AddAddr gives the interface an address with its prefix.
func AddAddr(name string, p netip.Prefix, ns *os.File) error {
	idx, err := linkIndex(name, ns)
	if err != nil {
		return err
	}
	fam := byte(unix.AF_INET)
	if p.Addr().Is6() {
		fam = unix.AF_INET6
	}
	msg := make([]byte, unix.SizeofIfAddrmsg)
	msg[0], msg[1] = fam, byte(p.Bits())
	binary.NativeEndian.PutUint32(msg[4:], uint32(idx))
	_, err = rtExec(ns, unix.RTM_NEWADDR, netlink.Acknowledge|netlink.Create|netlink.Excl, append(msg, encode(func(ae *netlink.AttributeEncoder) {
		ae.Bytes(unix.IFA_LOCAL, p.Addr().AsSlice())
		ae.Bytes(unix.IFA_ADDRESS, p.Addr().AsSlice())
	})...))
	return err
}
