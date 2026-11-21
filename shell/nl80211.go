package shell

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/kario-os/kario/ui"
	"golang.org/x/sys/unix"
)

// The little of nl80211 (generic netlink) the Shell speaks itself: the
// stations on the hotspot and sending one away over its device limit, the
// channels the radio may start an access point on, and moving the
// emulator's spare radio into the hotspot test device's namespace.

// nl80211 opens a generic netlink socket and finds nl80211's number.
func nl80211() (fd int, family uint16, err error) {
	fd, err = unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_GENERIC)
	if err != nil {
		return -1, 0, err
	}
	if err = unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err == nil {
		family, err = genlFamily(fd, "nl80211")
	}
	if err != nil {
		unix.Close(fd)
		return -1, 0, err
	}
	return fd, family, nil
}

// moveWiphy moves a Wi-Fi radio (by phy index) and its interfaces into the
// network namespace of the thread tid: SET_WIPHY_NETNS, what
// "iw phy <phy> set netns <pid>" sends.
func moveWiphy(phy uint32, tid int) error {
	fd, id, err := nl80211()
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	attrs := append(nlAttr(unix.NL80211_ATTR_WIPHY, binary.NativeEndian.AppendUint32(nil, phy)),
		nlAttr(unix.NL80211_ATTR_PID, binary.NativeEndian.AppendUint32(nil, uint32(tid)))...)
	_, err = genlRequest(fd, id, unix.NL80211_CMD_SET_WIPHY_NETNS, attrs)
	return err
}

// apStation is a station associated with an access point: its address and
// how long it has been connected.
type apStation struct {
	mac       string
	connected time.Duration
}

// apStations are the stations associated with an access point's
// interface (the GET_STATION dump).
func apStations(ifname string) ([]apStation, error) {
	ifc, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, err
	}
	fd, id, err := nl80211()
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	var out []apStation
	err = genlDump(fd, id, unix.NL80211_CMD_GET_STATION, ifindexAttr(ifc.Index), func(a map[uint16][]byte) {
		if st, ok := parseStation(a); ok {
			out = append(out, st)
		}
	})
	return out, err
}

func ifindexAttr(index int) []byte {
	return nlAttr(unix.NL80211_ATTR_IFINDEX, binary.NativeEndian.AppendUint32(nil, uint32(index)))
}

func parseStation(a map[uint16][]byte) (apStation, bool) {
	mac := a[unix.NL80211_ATTR_MAC]
	if len(mac) != 6 {
		return apStation{}, false
	}
	st := apStation{mac: net.HardwareAddr(mac).String()}
	if t := nlAttrs(a[unix.NL80211_ATTR_STA_INFO])[unix.NL80211_STA_INFO_CONNECTED_TIME]; len(t) == 4 {
		st.connected = time.Duration(binary.NativeEndian.Uint32(t)) * time.Second
	}
	return st, true
}

// reasonAPFull is IEEE 802.11's reason code 5: disassociated because the
// access point cannot handle all the stations associated.
const reasonAPFull = 5

// delStation removes a station from an access point, disassociating it
// with reason: the request iwd sends itself, whose event iwd follows (it
// forgets the station and its DHCP lease).
func delStation(ifname, mac string, reason uint16) error {
	ifc, err := net.InterfaceByName(ifname)
	if err != nil {
		return err
	}
	hw, err := net.ParseMAC(mac)
	if err != nil {
		return err
	}
	fd, id, err := nl80211()
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	_, err = genlRequest(fd, id, unix.NL80211_CMD_DEL_STATION, delStationAttrs(ifc.Index, hw, reason))
	return err
}

// delStationAttrs: the interface, the station, a disassociation (subtype
// 10) and the reason.
func delStationAttrs(index int, mac net.HardwareAddr, reason uint16) []byte {
	b := ifindexAttr(index)
	b = append(b, nlAttr(unix.NL80211_ATTR_MAC, mac)...)
	b = append(b, nlAttr(unix.NL80211_ATTR_MGMT_SUBTYPE, []byte{10})...)
	return append(b, nlAttr(unix.NL80211_ATTR_REASON_CODE, binary.NativeEndian.AppendUint16(nil, reason))...)
}

// wifiFreq is a channel a radio knows, in MHz, and whether an access
// point may use it: not disabled, not "no initiating radiation" (5 GHz
// before the country is set) and not radar (DFS).
type wifiFreq struct {
	mhz uint32
	ap  bool
}

// wiphyFreqs are the channels of the radio phy (the split GET_WIPHY dump:
// the bands come over several messages).
func wiphyFreqs(phy uint32) ([]wifiFreq, error) {
	fd, id, err := nl80211()
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	attrs := append(nlAttr(unix.NL80211_ATTR_WIPHY, binary.NativeEndian.AppendUint32(nil, phy)), nlAttr(unix.NL80211_ATTR_SPLIT_WIPHY_DUMP, nil)...)
	var out []wifiFreq
	err = genlDump(fd, id, unix.NL80211_CMD_GET_WIPHY, attrs, func(a map[uint16][]byte) { out = append(out, parseFreqs(a)...) })
	return out, err
}

func parseFreqs(a map[uint16][]byte) []wifiFreq {
	var out []wifiFreq
	for _, band := range nlList(a[unix.NL80211_ATTR_WIPHY_BANDS]) {
		for _, f := range nlList(nlAttrs(band)[unix.NL80211_BAND_ATTR_FREQS]) {
			fa := nlAttrs(f)
			mhz := fa[unix.NL80211_FREQUENCY_ATTR_FREQ]
			if len(mhz) != 4 {
				continue
			}
			_, off := fa[unix.NL80211_FREQUENCY_ATTR_DISABLED]
			_, noIR := fa[unix.NL80211_FREQUENCY_ATTR_NO_IR]
			_, radar := fa[unix.NL80211_FREQUENCY_ATTR_RADAR]
			out = append(out, wifiFreq{mhz: binary.NativeEndian.Uint32(mhz), ap: !off && !noIR && !radar})
		}
	}
	return out
}

// apChannels5 are the 5 GHz channels a hotspot tries, in order: UNII-1
// (indoors in most countries, no radar), then UNII-3 (China's 5.8 GHz).
var apChannels5 = []int{36, 40, 44, 48, 149, 153, 157, 161}

// apChannel5 is the first of apChannels5 an access point may use, 0 for
// none.
func apChannel5(freqs []wifiFreq) int {
	for _, ch := range apChannels5 {
		for _, f := range freqs {
			if f.ap && f.mhz == uint32(5000+5*ch) {
				return ch
			}
		}
	}
	return 0
}

// apBands are the bands an access point may use: 2.4 GHz where channel 6
// (iwd's choice there) is allowed, 5 GHz where apChannel5 finds one.
func apBands(freqs []wifiFreq) []string {
	var out []string
	for _, f := range freqs {
		if f.ap && f.mhz == 2437 {
			out = append(out, ui.HotspotBand24)
			break
		}
	}
	if apChannel5(freqs) != 0 {
		out = append(out, ui.HotspotBand5)
	}
	return out
}

// newStations calls each with a station that associated with the access
// point on ifindex (nl80211's "mlme" events), until stop closes.
func newStations(ifindex int, stop <-chan struct{}, each func(mac string)) error {
	fd, id, err := nl80211()
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	group, err := genlGroup(fd, "nl80211", "mlme")
	if err != nil {
		return err
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_NETLINK, unix.NETLINK_ADD_MEMBERSHIP, int(group)); err != nil {
		return err
	}
	tv := unix.NsecToTimeval(time.Second.Nanoseconds())
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	buf := make([]byte, 1<<16)
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
		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			continue
		}
		for _, m := range msgs {
			if mac, ok := newStationEvent(m, id, ifindex); ok {
				each(mac)
			}
		}
	}
}

func newStationEvent(m syscall.NetlinkMessage, family uint16, ifindex int) (string, bool) {
	if m.Header.Type != family || len(m.Data) < unix.GENL_HDRLEN || m.Data[0] != unix.NL80211_CMD_NEW_STATION {
		return "", false
	}
	a := nlAttrs(m.Data[unix.GENL_HDRLEN:])
	idx, mac := a[unix.NL80211_ATTR_IFINDEX], a[unix.NL80211_ATTR_MAC]
	if len(idx) != 4 || int(binary.NativeEndian.Uint32(idx)) != ifindex || len(mac) != 6 {
		return "", false
	}
	return net.HardwareAddr(mac).String(), true
}

// genlGroup is the number of a generic netlink family's multicast group.
func genlGroup(fd int, family, group string) (uint32, error) {
	attrs, err := genlRequest(fd, unix.GENL_ID_CTRL, unix.CTRL_CMD_GETFAMILY, nlAttr(unix.CTRL_ATTR_FAMILY_NAME, append([]byte(family), 0)))
	if err != nil {
		return 0, err
	}
	for _, g := range nlList(attrs[unix.CTRL_ATTR_MCAST_GROUPS]) {
		ga := nlAttrs(g)
		if id := ga[unix.CTRL_ATTR_MCAST_GRP_ID]; strings.TrimRight(string(ga[unix.CTRL_ATTR_MCAST_GRP_NAME]), "\x00") == group && len(id) == 4 {
			return binary.NativeEndian.Uint32(id), nil
		}
	}
	return 0, fmt.Errorf("generic netlink family %s has no group %s", family, group)
}

// nlList is a nested list's entries, in order (their types are indexes).
func nlList(b []byte) [][]byte {
	var out [][]byte
	for len(b) >= unix.SizeofNlAttr {
		l := int(binary.NativeEndian.Uint16(b))
		if l < unix.SizeofNlAttr || l > len(b) {
			break
		}
		out = append(out, b[unix.SizeofNlAttr:l])
		b = b[min((l+3)&^3, len(b)):]
	}
	return out
}

// genlFamily is the number of a generic netlink family.
func genlFamily(fd int, name string) (uint16, error) {
	attrs, err := genlRequest(fd, unix.GENL_ID_CTRL, unix.CTRL_CMD_GETFAMILY, nlAttr(unix.CTRL_ATTR_FAMILY_NAME, append([]byte(name), 0)))
	if err != nil {
		return 0, fmt.Errorf("generic netlink family %s: %w", name, err)
	}
	id := attrs[unix.CTRL_ATTR_FAMILY_ID]
	if len(id) < 2 {
		return 0, fmt.Errorf("generic netlink family %s: no number", name)
	}
	return binary.NativeEndian.Uint16(id), nil
}

// genlRequest sends one generic netlink request, asking for an
// acknowledgement, and returns the attributes of the answer (none for a
// bare acknowledgement).
func genlRequest(fd int, family uint16, cmd uint8, attrs []byte) (map[uint16][]byte, error) {
	var out map[uint16][]byte
	err := genlExchange(fd, family, cmd, unix.NLM_F_ACK, attrs, func(a map[uint16][]byte) { out = a })
	return out, err
}

// genlDump sends a dump request; each answer's attributes go to each.
func genlDump(fd int, family uint16, cmd uint8, attrs []byte, each func(map[uint16][]byte)) error {
	return genlExchange(fd, family, cmd, unix.NLM_F_DUMP, attrs, each)
}

func genlExchange(fd int, family uint16, cmd uint8, flags uint16, attrs []byte, each func(map[uint16][]byte)) error {
	msg := make([]byte, unix.SizeofNlMsghdr+unix.GENL_HDRLEN, unix.SizeofNlMsghdr+unix.GENL_HDRLEN+len(attrs))
	msg = append(msg, attrs...)
	binary.NativeEndian.PutUint32(msg[0:], uint32(len(msg)))
	binary.NativeEndian.PutUint16(msg[4:], family)
	binary.NativeEndian.PutUint16(msg[6:], unix.NLM_F_REQUEST|flags)
	binary.NativeEndian.PutUint32(msg[8:], 1)
	msg[unix.SizeofNlMsghdr], msg[unix.SizeofNlMsghdr+1] = cmd, 1
	if err := unix.Sendto(fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	buf := make([]byte, 1<<16)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return err
		}
		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return err
		}
		for _, m := range msgs {
			switch m.Header.Type {
			case unix.NLMSG_ERROR:
				if len(m.Data) < 4 {
					return errors.New("short netlink error")
				}
				if code := int32(binary.NativeEndian.Uint32(m.Data)); code != 0 {
					return syscall.Errno(-code)
				}
				return nil
			case unix.NLMSG_DONE:
				return nil
			case family:
				if len(m.Data) >= unix.GENL_HDRLEN {
					each(nlAttrs(m.Data[unix.GENL_HDRLEN:]))
				}
			}
		}
	}
}

func nlAttr(typ uint16, data []byte) []byte {
	l := unix.SizeofNlAttr + len(data)
	b := make([]byte, (l+3)&^3)
	binary.NativeEndian.PutUint16(b, uint16(l))
	binary.NativeEndian.PutUint16(b[2:], typ)
	copy(b[unix.SizeofNlAttr:], data)
	return b
}

func nlAttrs(b []byte) map[uint16][]byte {
	out := map[uint16][]byte{}
	for len(b) >= unix.SizeofNlAttr {
		l := int(binary.NativeEndian.Uint16(b))
		if l < unix.SizeofNlAttr || l > len(b) {
			break
		}
		out[binary.NativeEndian.Uint16(b[2:])&^(unix.NLA_F_NESTED|unix.NLA_F_NET_BYTEORDER)] = b[unix.SizeofNlAttr:l]
		b = b[min((l+3)&^3, len(b)):]
	}
	return out
}
