package shell

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/kario-os/kario/ui"
	"golang.org/x/sys/unix"
)

func TestHotspotChecks(t *testing.T) {
	for _, ok := range []string{"Kario-3F7A", "\u5c0f\u7c73\u7684\u70ed\u70b9", "a", strings.Repeat("x", 32)} {
		if err := checkHotspotSSID(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "  ", strings.Repeat("x", 33), strings.Repeat("\u70ed", 11), "a/b", "a\nb", "a\x00b", "\xff"} {
		if checkHotspotSSID(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"12345678", strings.Repeat("a", 63), "with space~!"} {
		if err := checkHotspotPass(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"1234567", strings.Repeat("a", 64), strings.Repeat("\u5bc6\u7801", 4), "tab\tinside", "line\nbreak"} {
		if checkHotspotPass(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for range 20 {
		if p := randomPassphrase(); len(p) != 12 || checkHotspotPass(p) != nil || strings.ContainsAny(p, "0O1lI") {
			t.Fatalf("passphrase %q", p)
		}
	}
	if s := defaultHotspotSSID("Kario"); len(s) != len("Kario-3F7A") || !strings.HasPrefix(s, "Kario-") || checkHotspotSSID(s) != nil {
		t.Errorf("default name %q", s)
	}
	if s := defaultHotspotSSID("\u8bfa\u57fa\u4e9a \u529f\u80fd\u673a \u952e\u76d8\u624b\u673a"); len(s) > 32 || !strings.HasPrefix(s, "\u8bfa\u57fa\u4e9a-\u529f\u80fd\u673a-") || checkHotspotSSID(s) != nil {
		t.Errorf("long brand: %q", s)
	}
}

// IEEE 802.11i-2004, annex H.4.
func TestWPAPSK(t *testing.T) {
	psk, err := wpaPSK("IEEE", "password")
	if err != nil || hex.EncodeToString(psk) != "f42c6fc52df0ebef9ebb4b90b38a5f902e83fe1b135a70e23aed762e9710a12e" {
		t.Fatalf("%x %v", psk, err)
	}
	_, ap, _ := net.ParseCIDR("192.168.43.1/24")
	ap.IP = net.IPv4(192, 168, 43, 1)
	if p := apProfile(psk, ap, []string{"10.0.2.3", "8.8.8.8"}, 0); !strings.Contains(p, "PreSharedKey=f42c6fc5") || !strings.Contains(p, "[IPv4]\nAddress=192.168.43.1\nGateway=192.168.43.1\nNetmask=255.255.255.0\nDNSList=10.0.2.3,8.8.8.8\n") || strings.Contains(p, "password") {
		t.Fatalf("profile:\n%s", p)
	}
	if p := apProfile(psk, ap, nil, 0); !strings.Contains(p, "Gateway=192.168.43.1\n") || strings.Contains(p, "DNSList") {
		t.Fatalf("profile without DNS servers:\n%s", p)
	}
	if p := apProfile(psk, nil, nil, 0); strings.Contains(p, "IPv4") || strings.Contains(p, "General") {
		t.Fatalf("profile without an address:\n%s", p)
	}
	if p := apProfile(psk, ap, nil, 36); !strings.HasPrefix(p, "[General]\nChannel=36\n\n[Security]\n") {
		t.Fatalf("profile on 5 GHz:\n%s", p)
	}
}

// iwd's DHCP server sends a router only when the profile names one, and
// it can only name one with a fixed address: the phone picks a subnet no
// network it is on uses.
func TestAPAddress(t *testing.T) {
	cidrs := func(s ...string) []*net.IPNet {
		var out []*net.IPNet
		for _, c := range s {
			_, n, err := net.ParseCIDR(c)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		return out
	}
	for _, c := range []struct {
		used []*net.IPNet
		want string
	}{
		{nil, "192.168.43.1/24"},
		{cidrs("10.0.2.0/24", "100.64.0.0/10"), "192.168.43.1/24"},
		{cidrs("192.168.43.0/24"), "192.168.44.1/24"},
		{cidrs("192.168.43.128/25", "192.168.44.7/32"), "192.168.45.1/24"},
		{cidrs("192.168.0.0/16"), "172.20.43.1/24"},
		{cidrs("192.168.0.0/16", "172.16.0.0/12"), ""},
	} {
		got := ""
		if n := apAddress(c.used); n != nil {
			got = n.String()
		}
		if got != c.want {
			t.Errorf("apAddress(%v) = %q, want %q", c.used, got, c.want)
		}
	}
}

const testRoutes = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
wlan0	00000000	0132A8C0	0003	0	0	300	00000000	0	0	0
eth0	00000000	0202000A	0003	0	0	1024	00000000	0	0	0
eth0	0002000A	00000000	0001	0	0	1024	00FFFFFF	0	0	0
wwan0	00000000	00000000	0002	0	0	10	00000000	0	0	0
`

func TestDefaultRoute(t *testing.T) {
	if up, gw := defaultRoute(testRoutes, ""); up != "wlan0" || gw.String() != "192.168.50.1" {
		t.Errorf("%s via %v", up, gw)
	}
	if up, gw := defaultRoute(testRoutes, "wlan0"); up != "eth0" || gw.String() != "10.0.2.2" {
		t.Errorf("leaving out the hotspot: %s via %v", up, gw)
	}
	if up, _ := defaultRoute("Iface\tDestination\n", ""); up != "" {
		t.Errorf("no routes: %q", up)
	}
}

func TestResolvers(t *testing.T) {
	got := resolvers("# resolved\nnameserver 10.0.2.3\nnameserver 127.0.0.53\nnameserver fe80::1\nnameserver 0.0.0.0\nsearch lan\nnameserver 8.8.4.4\n")
	if strings.Join(got, ",") != "10.0.2.3,8.8.4.4" {
		t.Fatalf("%v", got)
	}
}

// The phone's device as a station is the first one, and none while it is
// the hotspot: the emulator's second device (the selftest's) never stands
// in for it.
func TestIwdPhoneDevice(t *testing.T) {
	dev := func(name, mode string) map[string]map[string]dbus.Variant {
		return map[string]map[string]dbus.Variant{iwdDevice: {"Name": dbus.MakeVariant(name), "Mode": dbus.MakeVariant(mode)}}
	}
	o := iwdObjs{"/phy1/2": dev("wlan1", "station"), "/phy0/1": dev("wlan0", "ap")}
	if o.phone() != "/phy0/1" || o.device() != "" {
		t.Fatalf("hotspot on: phone %q, station %q", o.phone(), o.device())
	}
	o["/phy0/1"] = dev("wlan0", "station")
	o["/phy1/2"] = dev("wlan1", "ap")
	if o.device() != "/phy0/1" {
		t.Fatalf("station: %q", o.device())
	}
}

// fakeAP is the simulated radio with a network interface, for the sharing.
type fakeAP struct{ *simWifi }

func (f fakeAP) startHotspot(cfg hotspotConfig) (string, *net.IPNet, error) {
	_, _, _ = f.simWifi.startHotspot(cfg)
	_, n, _ := net.ParseCIDR("192.168.43.1/28")
	return "wlan9", n, nil
}

type shareCall struct {
	in, out string
	src     string
}

type fakeSharer struct {
	mu    sync.Mutex
	calls []shareCall
}

func (f *fakeSharer) ApplyShare(_, in, out string, src *net.IPNet) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := shareCall{in: in, out: out}
	if src != nil {
		c.src = src.String()
	}
	f.calls = append(f.calls, c)
	return nil
}

func (f *fakeSharer) last() (shareCall, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return shareCall{}, 0
	}
	return f.calls[len(f.calls)-1], len(f.calls)
}

type hotspotTest struct {
	*locTest
	app *App
	log *bytes.Buffer
}

func newHotspotTest(t *testing.T, wifi wifiRadio) *hotspotTest {
	settings := &App{M: &Manifest{ID: "com.phone.settings", Name: "Settings", Permissions: []string{ui.PermSystem}}, running: true, ready: true, out: make(chan outbound, 64)}
	lt := newLocTest(t, settings)
	var buf bytes.Buffer
	lt.c.log = log.New(&buf, "", 0)
	lt.c.wifi = wifi
	lt.c.settings = defaultSettings()
	return &hotspotTest{locTest: lt, app: settings, log: &buf}
}

func (ht *hotspotTest) set(args ui.HotspotArgs) (ui.HotspotState, error) {
	ht.t.Helper()
	ht.req(ht.app, ui.CapHotspotSet, args)
	return ht.answer()
}

func (ht *hotspotTest) state() ui.HotspotState {
	ht.t.Helper()
	ht.req(ht.app, ui.CapHotspotState, nil)
	st, err := ht.answer()
	if err != nil {
		ht.t.Fatal(err)
	}
	return st
}

func (ht *hotspotTest) answer() (ui.HotspotState, error) {
	ht.t.Helper()
	r := ht.reply(ht.app)
	if !r.OK {
		return ui.HotspotState{}, errors.New(r.Err)
	}
	var st ui.HotspotState
	if err := json.Unmarshal(r.Data, &st); err != nil {
		ht.t.Fatal(err)
	}
	return st, nil
}

func on(v bool) *bool { return &v }

// The switch brings the access point up with a made-up name and passphrase,
// the station off; a device joins only with the passphrase, a new one
// restarts the hotspot; off brings Wi-Fi back. The passphrase is kept in
// the settings and never logged.
func TestHotspotSwitch(t *testing.T) {
	w := newSimWifi()
	ht := newHotspotTest(t, w)
	st := ht.state()
	if st.Backend != "sim" || st.Enabled || st.Started || !strings.HasPrefix(st.SSID, "Kario-") || len(st.Passphrase) != 12 {
		t.Fatalf("before: %+v", st)
	}
	pass := st.Passphrase
	st, err := ht.set(ui.HotspotArgs{Enabled: on(true)})
	if err != nil || !st.Enabled || !st.Started || !ht.c.hotspot.on || st.Clients != 0 {
		t.Fatalf("on: %+v %v", st, err)
	}
	if ws, _ := w.state(); ws.Enabled {
		t.Fatal("the station stayed on")
	}
	if w.joinHotspot("tablet", "wrong-pass") == nil {
		t.Fatal("a wrong passphrase joined")
	}
	if err := w.joinHotspot("tablet", pass); err != nil {
		t.Fatal(err)
	}
	if st := ht.state(); st.Clients != 1 {
		t.Fatalf("clients: %+v", st)
	}
	if _, err := ht.set(ui.HotspotArgs{Passphrase: "short"}); err == nil {
		t.Fatal("a short passphrase was taken")
	}
	st, err = ht.set(ui.HotspotArgs{Passphrase: "kario-hotspot-9"})
	if err != nil || st.Passphrase != "kario-hotspot-9" || st.Clients != 0 || !st.Started {
		t.Fatalf("new passphrase: %+v %v", st, err)
	}
	if w.joinHotspot("tablet", pass) == nil {
		t.Fatal("the old passphrase still joins")
	}
	st, err = ht.set(ui.HotspotArgs{Enabled: on(false)})
	if err != nil || st.Enabled || st.Started || ht.c.hotspot.on {
		t.Fatalf("off: %+v %v", st, err)
	}
	if ws, _ := w.state(); !ws.Enabled {
		t.Fatal("Wi-Fi did not come back")
	}
	raw, _ := os.ReadFile(filepath.Join(ht.c.cfg.StateDir, "settings.json"))
	var saved sysSettings
	if err := json.Unmarshal(raw, &saved); err != nil || saved.HotspotPass != "kario-hotspot-9" || saved.HotspotSSID != st.SSID || saved.Hotspot {
		t.Fatalf("saved: %s", raw)
	}
	if strings.Contains(ht.log.String(), pass) || strings.Contains(ht.log.String(), "kario-hotspot-9") {
		t.Fatalf("the passphrase is in the log:\n%s", ht.log)
	}
}

// The diagnostic dump leaves the passphrase out.
func TestHotspotPassNotDumped(t *testing.T) {
	ht := newHotspotTest(t, newSimWifi())
	ht.c.settings.HotspotPass = "kario-hotspot-7"
	raw, err := json.Marshal(ht.c.Dump())
	if err != nil || strings.Contains(string(raw), "kario-hotspot-7") || !strings.Contains(string(raw), `"auto_lock"`) {
		t.Fatalf("%v: %s", err, raw)
	}
}

// Wi-Fi off when the hotspot came on stays off when it goes off; turning
// Wi-Fi on ends the hotspot; airplane mode ends it and keeps it from
// coming on.
func TestHotspotAndOtherSwitches(t *testing.T) {
	w := newSimWifi()
	_ = w.setEnabled(false)
	ht := newHotspotTest(t, w)
	if _, err := ht.set(ui.HotspotArgs{Enabled: on(true)}); err != nil {
		t.Fatal(err)
	}
	ht.pump("Wi-Fi's switch recorded", func() bool { return ht.c.settings.HotspotWifiOff })
	if _, err := ht.set(ui.HotspotArgs{Enabled: on(false)}); err != nil {
		t.Fatal(err)
	}
	if ws, _ := w.state(); ws.Enabled {
		t.Fatal("Wi-Fi came on with the hotspot off")
	}

	if _, err := ht.set(ui.HotspotArgs{Enabled: on(true)}); err != nil {
		t.Fatal(err)
	}
	ht.req(ht.app, ui.CapWifiSet, ui.WifiArgs{Enabled: on(true)})
	if r := ht.reply(ht.app); !r.OK {
		t.Fatalf("Wi-Fi on: %+v", r)
	}
	if ws, _ := w.state(); !ws.Enabled || ht.c.settings.Hotspot || ht.c.hotspot.on {
		t.Fatalf("Wi-Fi on over the hotspot: wifi %+v, hotspot %v/%v", ws, ht.c.settings.Hotspot, ht.c.hotspot.on)
	}

	if _, err := ht.set(ui.HotspotArgs{Enabled: on(true)}); err != nil {
		t.Fatal(err)
	}
	done := false
	ht.c.setAirplane(true, func(err error) { done = err == nil })
	ht.pump("airplane mode", func() bool { return done })
	if ht.c.settings.Hotspot || ht.c.hotspot.on {
		t.Fatal("the hotspot stayed on in airplane mode")
	}
	if ws, _ := w.state(); ws.Enabled {
		t.Fatal("Wi-Fi stayed on in airplane mode")
	}
	if _, err := ht.set(ui.HotspotArgs{Enabled: on(true)}); err == nil || ht.c.settings.Hotspot {
		t.Fatalf("on in airplane mode: %v", err)
	}
}

// The NAT and forwarding follow the default route and are taken back,
// forwarding as it was, when the hotspot goes off.
func TestHotspotSharing(t *testing.T) {
	dir := t.TempDir()
	forwardingDir, routesFile = filepath.Join(dir, "conf"), filepath.Join(dir, "route")
	resolvFiles = []string{filepath.Join(dir, "resolv.conf")}
	t.Cleanup(func() {
		forwardingDir, routesFile = "/proc/sys/net/ipv4/conf", "/proc/net/route"
		resolvFiles = []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"}
	})
	for _, i := range []string{"wlan9", "eth8", "eth9"} {
		_ = os.MkdirAll(filepath.Join(forwardingDir, i), 0o755)
		_ = os.WriteFile(filepath.Join(forwardingDir, i, "forwarding"), []byte("0\n"), 0o644)
	}
	route := func(iface string) {
		_ = os.WriteFile(routesFile, []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n"+
			iface+"\t00000000\t0202000A\t0003\t0\t0\t100\t00000000\n"), 0o644)
	}
	route("eth9")
	fw := &fakeSharer{}
	ht := newHotspotTest(t, fakeAP{newSimWifi()})
	ht.c.hotspot.fw = fw
	st, err := ht.set(ui.HotspotArgs{Enabled: on(true)})
	if err != nil || !st.Started || st.Upstream != "eth9" {
		t.Fatalf("on: %+v %v", st, err)
	}
	if c, _ := fw.last(); c != (shareCall{"wlan9", "eth9", "192.168.43.0/28"}) || forwarding("wlan9") != "1" || forwarding("eth9") != "1" {
		t.Fatalf("shared %+v, forwarding %s %s", c, forwarding("wlan9"), forwarding("eth9"))
	}
	_, n := fw.last()
	ht.c.syncHotspot(false, nil)
	time.Sleep(50 * time.Millisecond)
	if _, again := fw.last(); again != n {
		t.Fatal("the same route was shared again")
	}
	route("eth8")
	done := false
	ht.c.syncHotspot(false, func(error) { done = true })
	ht.pump("the new route", func() bool { return done })
	if c, _ := fw.last(); c.out != "eth8" || forwarding("eth9") != "0" || forwarding("eth8") != "1" || ht.c.hotspot.upstream != "eth8" {
		t.Fatalf("after the route moved: %+v, forwarding eth9 %s eth8 %s", c, forwarding("eth9"), forwarding("eth8"))
	}
	if _, err := ht.set(ui.HotspotArgs{Enabled: on(false)}); err != nil {
		t.Fatal(err)
	}
	if c, _ := fw.last(); c.in != "" || forwarding("wlan9") != "0" || forwarding("eth8") != "0" {
		t.Fatalf("off: %+v, forwarding %s %s", c, forwarding("wlan9"), forwarding("eth8"))
	}
}

func TestParseLease(t *testing.T) {
	l, ok := parseLease("192.168.43.5 192.168.43.1\n10.0.2.3 8.8.8.8\n")
	if !ok || l.addr != "192.168.43.5" || l.router != "192.168.43.1" || strings.Join(l.dns, ",") != "10.0.2.3,8.8.8.8" {
		t.Fatalf("%+v %v", l, ok)
	}
	if _, ok := parseLease(""); ok {
		t.Fatal("an empty lease")
	}
	if l, ok := parseLease("192.168.43.6 \n\n"); !ok || l.addr != "192.168.43.6" || l.router != "" {
		t.Fatalf("a lease without a router: %+v %v", l, ok)
	}
}

// The generic netlink request the emulator's device uses to move its
// radio: the controller knows itself, and an unknown family is ENOENT.
func TestGenericNetlink(t *testing.T) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_GENERIC)
	if err != nil {
		t.Skip(err)
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		t.Skip(err)
	}
	if id, err := genlFamily(fd, "nlctrl"); err != nil || id != unix.GENL_ID_CTRL {
		t.Fatalf("nlctrl: %d %v", id, err)
	}
	if _, err := genlFamily(fd, "no-such-family"); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("unknown family: %v", err)
	}
	attrs := nlAttrs(append(nlAttr(1, []byte{7, 0}), nlAttr(2, []byte("abc\x00"))...))
	if string(attrs[2]) != "abc\x00" || len(attrs[1]) != 2 {
		t.Fatalf("attributes: %q", attrs)
	}
}
