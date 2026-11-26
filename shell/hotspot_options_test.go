package shell

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

func intp(n int) *int { return &n }

// The security, hiding, band and device limit: the simulated access point
// lets devices on as a real one would; a new limit holds without a
// restart, the rest restart the hotspot; what the radio cannot do is
// refused; the choices are kept.
func TestHotspotOptions(t *testing.T) {
	w := newSimWifi()
	ht := newHotspotTest(t, w)
	st := ht.state()
	if st.Security != ui.HotspotWPA2 || st.Band != ui.HotspotBand24 || st.MaxClients != 0 || len(st.Securities) != 3 || !st.CanHide || len(st.Bands) != 2 {
		t.Fatalf("before: %+v", st)
	}
	pass := st.Passphrase
	st, err := ht.set(ui.HotspotArgs{Enabled: on(true), MaxClients: intp(1)})
	if err != nil || !st.Started || st.MaxClients != 1 {
		t.Fatalf("on with one device: %+v %v", st, err)
	}
	if err := w.joinHotspot("tablet", pass); err != nil {
		t.Fatal(err)
	}
	if err := w.joinHotspot("laptop", pass); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("past the limit: %v", err)
	}
	st = ht.state()
	if st.Clients != 1 || len(st.Devices) != 1 || st.Devices[0].Name != "tablet" || st.Devices[0].MAC == "" || st.Devices[0].Since == 0 {
		t.Fatalf("devices: %+v", st.Devices)
	}
	if _, err := ht.set(ui.HotspotArgs{MaxClients: intp(2)}); err != nil {
		t.Fatal(err)
	}
	if err := w.joinHotspot("laptop", pass); err != nil {
		t.Fatalf("under the new limit: %v", err)
	}
	if st := ht.state(); st.Clients != 2 {
		t.Fatalf("the new limit restarted the hotspot: %+v", st.Devices)
	}
	if _, err := ht.set(ui.HotspotArgs{Security: ui.HotspotWPA3}); err != nil {
		t.Fatal(err)
	}
	if st := ht.state(); st.Clients != 0 || st.Security != ui.HotspotWPA3 {
		t.Fatalf("WPA3: %+v", st)
	}
	if err := w.joinAs("old phone", pass, simDevice{wpa2Only: true}); err == nil {
		t.Fatal("a WPA2 device joined WPA3")
	}
	if _, err := ht.set(ui.HotspotArgs{Security: ui.HotspotWPA2WPA3}); err != nil {
		t.Fatal(err)
	}
	if err := w.joinAs("old phone", pass, simDevice{wpa2Only: true}); err != nil {
		t.Fatalf("WPA2 in the transition mode: %v", err)
	}
	if _, err := ht.set(ui.HotspotArgs{Hidden: on(true)}); err != nil || w.hotspotSeen() {
		t.Fatalf("hidden: %v, seen %v", err, w.hotspotSeen())
	}
	if _, err := ht.set(ui.HotspotArgs{Hidden: on(false), Band: ui.HotspotBand5}); err != nil || !w.hotspotSeen() {
		t.Fatalf("shown again: %v", err)
	}
	if err := w.joinAs("old laptop", pass, simDevice{only24: true}); err == nil {
		t.Fatal("a 2.4 GHz device joined 5 GHz")
	}
	for _, bad := range []ui.HotspotArgs{{MaxClients: intp(11)}, {MaxClients: intp(-1)}, {Security: "wep"}, {Band: "6"}} {
		if _, err := ht.set(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(ht.c.cfg.StateDir, "settings.json"))
	var saved sysSettings
	if err := json.Unmarshal(raw, &saved); err != nil || saved.HotspotSecurity != ui.HotspotWPA2WPA3 || saved.HotspotBand != ui.HotspotBand5 || saved.HotspotMax != 2 || saved.HotspotHidden {
		t.Fatalf("saved: %s", raw)
	}
	if _, err := ht.set(ui.HotspotArgs{Enabled: on(false)}); err != nil {
		t.Fatal(err)
	}
	if st := ht.state(); len(st.Devices) != 0 || st.Clients != 0 {
		t.Fatalf("devices while off: %+v", st.Devices)
	}
}

// iwdLike is the simulated radio with iwd's access point: WPA2 only,
// never hidden, 2.4 GHz.
type iwdLike struct{ *simWifi }

func (iwdLike) hotspotCaps() hotspotCaps {
	return hotspotCaps{securities: []string{ui.HotspotWPA2}, bands: []string{ui.HotspotBand24}}
}

func TestHotspotOptionsRefused(t *testing.T) {
	ht := newHotspotTest(t, iwdLike{newSimWifi()})
	// Before the radio was asked what it can do, the defaults hold.
	if _, err := ht.set(ui.HotspotArgs{Security: ui.HotspotWPA2, Band: ui.HotspotBand24}); err != nil {
		t.Fatalf("the defaults before the radio's answer: %v", err)
	}
	st := ht.state()
	if len(st.Securities) != 1 || st.CanHide || len(st.Bands) != 1 {
		t.Fatalf("%+v", st)
	}
	for _, bad := range []ui.HotspotArgs{{Security: ui.HotspotWPA3}, {Security: ui.HotspotWPA2WPA3}, {Hidden: on(true)}, {Band: ui.HotspotBand5}} {
		if _, err := ht.set(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if _, err := ht.set(ui.HotspotArgs{Hidden: on(false), MaxClients: intp(3)}); err != nil {
		t.Fatal(err)
	}
}

// The usage counts the access point's counters for the session and the
// day: across a restart of the access point (counters from zero), past
// its end (the last session stays), into a new session, and into a new
// day; the days are kept in the Shell's state.
func TestHotspotUsage(t *testing.T) {
	w := newSimWifi()
	ht := newHotspotTest(t, w)
	pass := ht.state().Passphrase
	if _, err := ht.set(ui.HotspotArgs{Enabled: on(true)}); err != nil {
		t.Fatal(err)
	}
	if err := w.joinHotspot("tablet", pass); err != nil {
		t.Fatal(err)
	}
	_ = w.hotspotTraffic(1000, 200)
	if u := ht.state().Usage; u != (ui.HotspotUsage{SessionDown: 1000, SessionUp: 200, TodayDown: 1000, TodayUp: 200}) {
		t.Fatalf("first: %+v", u)
	}
	_ = w.hotspotTraffic(500, 0)
	if u := ht.state().Usage; u.SessionDown != 1500 || u.TodayDown != 1500 {
		t.Fatalf("more: %+v", u)
	}
	_ = w.hotspotTraffic(7, 3)
	if _, err := ht.set(ui.HotspotArgs{Passphrase: "kario-hotspot-5"}); err != nil {
		t.Fatal(err)
	}
	if err := w.joinHotspot("tablet", "kario-hotspot-5"); err != nil {
		t.Fatal(err)
	}
	_ = w.hotspotTraffic(100, 10)
	if u := ht.state().Usage; u.SessionDown != 1607 || u.SessionUp != 213 {
		t.Fatalf("after the restart: %+v", u)
	}
	_ = w.hotspotTraffic(3, 1)
	if _, err := ht.set(ui.HotspotArgs{Enabled: on(false)}); err != nil {
		t.Fatal(err)
	}
	if u := ht.state().Usage; u != (ui.HotspotUsage{SessionDown: 1610, SessionUp: 214, TodayDown: 1610, TodayUp: 214}) {
		t.Fatalf("off: %+v", u)
	}
	if _, err := ht.set(ui.HotspotArgs{Enabled: on(true)}); err != nil {
		t.Fatal(err)
	}
	if u := ht.state().Usage; u.SessionDown != 0 || u.TodayDown != 1610 {
		t.Fatalf("a new session: %+v", u)
	}
	ht.c.clockSkew = 24 * time.Hour
	if err := w.joinHotspot("tablet", "kario-hotspot-5"); err != nil {
		t.Fatal(err)
	}
	_ = w.hotspotTraffic(9, 9)
	if u := ht.state().Usage; u != (ui.HotspotUsage{SessionDown: 9, SessionUp: 9, TodayDown: 9, TodayUp: 9}) {
		t.Fatalf("the next day: %+v", u)
	}
	if _, err := ht.set(ui.HotspotArgs{Enabled: on(false)}); err != nil {
		t.Fatal(err)
	}
	again := &Core{cfg: ht.c.cfg, clockSkew: ht.c.clockSkew}
	again.loadHotspotUsage()
	if u := again.hotspotUsageView(); u.TodayDown != 9 || len(again.hotspot.usage.Days) != 2 {
		t.Fatalf("loaded: %+v %v", u, again.hotspot.usage.Days)
	}
	if since(5, 10) != 5 || since(10, 4) != 6 {
		t.Error("a counter that went down")
	}
}

// The devices to send away: the newest past the limit, only of those
// that just associated.
func TestOverLimit(t *testing.T) {
	sts := []apStation{{"a", time.Hour}, {"b", time.Second}, {"c", 0}, {"d", time.Minute}}
	if got := overLimit(sts, 3, apFresh); len(got) != 1 || got[0] != "c" {
		t.Errorf("one too many: %v", got)
	}
	if got := overLimit(sts, 2, apFresh); len(got) != 2 || got[0] != "c" || got[1] != "b" {
		t.Errorf("two too many: %v", got)
	}
	if got := overLimit(sts, 1, apFresh); len(got) != 2 {
		t.Errorf("the devices already on stay: %v", got)
	}
	if overLimit(sts, 0, apFresh) != nil || overLimit(sts, 4, apFresh) != nil {
		t.Error("sent away without a limit, or under it")
	}
}

func be32(v uint32) []byte { return binary.NativeEndian.AppendUint32(nil, v) }

// What nl80211 says of stations, channels and new stations, and what the
// Shell sends to send one away.
func TestNL80211Messages(t *testing.T) {
	mac := []byte{0x02, 0x4b, 0x41, 0x00, 0x00, 0x07}
	st, ok := parseStation(nlAttrs(append(nlAttr(unix.NL80211_ATTR_MAC, mac), nlAttr(unix.NL80211_ATTR_STA_INFO, nlAttr(unix.NL80211_STA_INFO_CONNECTED_TIME, be32(42)))...)))
	if !ok || st.mac != "02:4b:41:00:00:07" || st.connected != 42*time.Second {
		t.Fatalf("station: %+v %v", st, ok)
	}
	a := nlAttrs(delStationAttrs(7, mac, reasonAPFull))
	if binary.NativeEndian.Uint32(a[unix.NL80211_ATTR_IFINDEX]) != 7 || string(a[unix.NL80211_ATTR_MAC]) != string(mac) ||
		a[unix.NL80211_ATTR_MGMT_SUBTYPE][0] != 10 || binary.NativeEndian.Uint16(a[unix.NL80211_ATTR_REASON_CODE]) != 5 {
		t.Fatalf("del station: %v", a)
	}
	freq := func(i uint16, mhz uint32, flags ...uint16) []byte {
		b := nlAttr(unix.NL80211_FREQUENCY_ATTR_FREQ, be32(mhz))
		for _, f := range flags {
			b = append(b, nlAttr(f, nil)...)
		}
		return nlAttr(i, b)
	}
	band := func(i uint16, freqs ...[]byte) []byte {
		var b []byte
		for _, f := range freqs {
			b = append(b, f...)
		}
		return nlAttr(i, nlAttr(unix.NL80211_BAND_ATTR_FREQS, b))
	}
	world := parseFreqs(nlAttrs(nlAttr(unix.NL80211_ATTR_WIPHY_BANDS, append(
		band(unix.NL80211_BAND_2GHZ, freq(0, 2412), freq(1, 2437), freq(2, 2484, unix.NL80211_FREQUENCY_ATTR_DISABLED)),
		band(unix.NL80211_BAND_5GHZ, freq(0, 5180, unix.NL80211_FREQUENCY_ATTR_NO_IR), freq(1, 5260, unix.NL80211_FREQUENCY_ATTR_RADAR))...))))
	if len(world) != 5 || !world[1].ap || world[2].ap || world[3].ap {
		t.Fatalf("frequencies: %+v", world)
	}
	if b := apBands(world); len(b) != 1 || b[0] != ui.HotspotBand24 || apChannel5(world) != 0 {
		t.Fatalf("world roaming: %v", b)
	}
	china := parseFreqs(nlAttrs(nlAttr(unix.NL80211_ATTR_WIPHY_BANDS, append(band(unix.NL80211_BAND_2GHZ, freq(0, 2437)),
		band(unix.NL80211_BAND_5GHZ, freq(0, 5180, unix.NL80211_FREQUENCY_ATTR_NO_IR), freq(1, 5745))...))))
	if b := apBands(china); len(b) != 2 || apChannel5(china) != 149 {
		t.Fatalf("5.8 GHz only: %v, channel %d", b, apChannel5(china))
	}
	ev := func(cmd byte, ifindex uint32) syscall.NetlinkMessage {
		data := append([]byte{cmd, 1, 0, 0}, append(nlAttr(unix.NL80211_ATTR_IFINDEX, be32(ifindex)), nlAttr(unix.NL80211_ATTR_MAC, mac)...)...)
		return syscall.NetlinkMessage{Header: syscall.NlMsghdr{Type: 30}, Data: data}
	}
	if m, ok := newStationEvent(ev(unix.NL80211_CMD_NEW_STATION, 7), 30, 7); !ok || m != "02:4b:41:00:00:07" {
		t.Fatalf("new station: %q %v", m, ok)
	}
	if _, ok := newStationEvent(ev(unix.NL80211_CMD_DEL_STATION, 7), 30, 7); ok {
		t.Error("a station's leaving read as its coming")
	}
	if _, ok := newStationEvent(ev(unix.NL80211_CMD_NEW_STATION, 8), 30, 7); ok {
		t.Error("another interface's station")
	}
}

// dhcpPacket is an IPv4 UDP datagram carrying a BOOTP message of op from
// mac, with yiaddr and the options.
func dhcpPacket(op byte, mac net.HardwareAddr, yiaddr net.IP, opts ...[]byte) []byte {
	b := make([]byte, bootpLen)
	b[0], b[1], b[2] = op, 1, 6
	copy(b[16:], yiaddr.To4())
	copy(b[28:], mac)
	b = binary.BigEndian.AppendUint32(b, dhcpCookie)
	for _, o := range opts {
		b = append(b, o...)
	}
	b = append(b, dhcpOptEnd)
	sport, dport := uint16(68), uint16(67)
	if op == bootpReply {
		sport, dport = 67, 68
	}
	p := make([]byte, 28)
	p[0], p[9] = 0x45, unix.IPPROTO_UDP
	binary.BigEndian.PutUint16(p[2:], uint16(28+len(b)))
	binary.BigEndian.PutUint16(p[20:], sport)
	binary.BigEndian.PutUint16(p[22:], dport)
	return append(p, b...)
}

// A request names the device, an acknowledgement gives its address;
// other messages and names unfit to show give nothing.
func TestParseDHCP(t *testing.T) {
	mac := net.HardwareAddr{2, 0x4b, 0x41, 0, 0, 9}
	req := dhcpPacket(bootpRequest, mac, nil, []byte{dhcpOptType, 1, 3}, append([]byte{dhcpOptHost, 6}, "Pixel7"...))
	if m, name, addr, ok := parseDHCP(req); !ok || m != mac.String() || name != "Pixel7" || addr != "" {
		t.Fatalf("request: %s %q %q %v", m, name, addr, ok)
	}
	ack := dhcpPacket(bootpReply, mac, net.IPv4(192, 168, 43, 23), []byte{dhcpOptType, 1, dhcpTypeAck})
	if m, name, addr, ok := parseDHCP(ack); !ok || m != mac.String() || name != "" || addr != "192.168.43.23" {
		t.Fatalf("ack: %s %q %q %v", m, name, addr, ok)
	}
	if _, _, _, ok := parseDHCP(dhcpPacket(bootpReply, mac, net.IPv4(192, 168, 43, 23), []byte{dhcpOptType, 1, 2})); ok {
		t.Error("an offer read as the address given")
	}
	if _, _, _, ok := parseDHCP(dhcpPacket(bootpRequest, mac, nil, []byte{dhcpOptType, 1, 3})); ok {
		t.Error("a request without a name")
	}
	if _, name, _, _ := parseDHCP(dhcpPacket(bootpRequest, mac, nil, append([]byte{dhcpOptHost, 7}, "a\nb\x1bc"...))); name != "" {
		t.Errorf("a name with control characters: %q", name)
	}
	if _, _, _, ok := parseDHCP(req[:100]); ok {
		t.Error("a short packet")
	}
	if n := deviceName("\u5e73\u677f" + strings.Repeat("x", 70)); len(n) > maxDeviceName || !strings.HasPrefix(n, "\u5e73\u677f") {
		t.Errorf("a long name: %q", n)
	}
	var l dhcpLeases
	l.note(mac.String(), "Pixel7", "")
	l.note(mac.String(), "", "192.168.43.23")
	if got := l.get(mac.String()); got != (dhcpLease{"Pixel7", "192.168.43.23"}) {
		t.Fatalf("lease: %+v", got)
	}
}

// The kernel's filter passes DHCP both ways and nothing else.
func TestDHCPFilter(t *testing.T) {
	vm, err := bpf.NewVM(dhcpFilter)
	if err != nil {
		t.Fatal(err)
	}
	mac := net.HardwareAddr{2, 0x4b, 0x41, 0, 0, 9}
	pass := func(p []byte) bool {
		n, err := vm.Run(p)
		if err != nil {
			t.Fatal(err)
		}
		return n > 0
	}
	if !pass(dhcpPacket(bootpRequest, mac, nil)) || !pass(dhcpPacket(bootpReply, mac, nil)) {
		t.Error("DHCP dropped")
	}
	dns := dhcpPacket(bootpRequest, mac, nil)
	binary.BigEndian.PutUint16(dns[22:], 53)
	tcp := dhcpPacket(bootpRequest, mac, nil)
	tcp[9] = unix.IPPROTO_TCP
	frag := dhcpPacket(bootpRequest, mac, nil)
	binary.BigEndian.PutUint16(frag[6:], 0x0010)
	for name, p := range map[string][]byte{"DNS": dns, "TCP": tcp, "a fragment": frag} {
		if pass(p) {
			t.Errorf("%s passed", name)
		}
	}
}
