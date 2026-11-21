package shell

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// The simulated modem's link (telephonyd's sim_link).
func simCellLink() cellLink {
	return cellLink{Iface: "simwwan0", MTU: 1400,
		IPv4: &cellIP{Method: "static", Address: "100.80.12.34", Prefix: 30, Gateway: "100.80.12.33", DNS: []string{"100.80.0.53", "100.80.0.54"}},
		IPv6: &cellIP{Method: "static", Address: "2001:db8:4600:12::34", Prefix: 64, Gateway: "fe80::1", DNS: []string{"2001:db8:4600::53"}}}
}

// networkd's file for each kind of addressing ModemManager reports.
func TestCellNetwork(t *testing.T) {
	for _, tc := range []struct {
		name string
		link cellLink
		want string
	}{
		{"static, both families", simCellLink(), `[Match]
Name=simwwan0

[Link]
RequiredForOnline=no
MTUBytes=1400

[Network]
LinkLocalAddressing=ipv6
IPv6AcceptRA=yes
Address=100.80.12.34/30
Address=2001:db8:4600:12::34/64
DNS=100.80.0.53
DNS=100.80.0.54
DNS=2001:db8:4600::53

[Route]
Gateway=100.80.12.33
GatewayOnLink=yes
Metric=2048

[Route]
Gateway=fe80::1
GatewayOnLink=yes
Metric=2048

[IPv6AcceptRA]
RouteMetric=2048
`},
		{"IPv4 without a gateway, IPv6 by a link-local address", cellLink{Iface: "wwan0", MTU: 1500,
			IPv4: &cellIP{Method: "static", Address: "10.71.32.5", Prefix: 0, DNS: []string{"211.136.17.107"}},
			IPv6: &cellIP{Method: "static", Address: "fe80::1a2b:3c4d:5e6f:7081", Prefix: 64}}, `[Match]
Name=wwan0

[Link]
RequiredForOnline=no
MTUBytes=1500

[Network]
LinkLocalAddressing=ipv6
IPv6AcceptRA=yes
Address=10.71.32.5/32
DNS=211.136.17.107

[Route]
Destination=0.0.0.0/0
Metric=2048

[IPv6AcceptRA]
RouteMetric=2048
Token=static:::1a2b:3c4d:5e6f:7081
`},
		{"DHCP, IPv4 only, an MTU IPv6 could not use", cellLink{Iface: "wwan1", MTU: 1200, IPv4: &cellIP{Method: "dhcp"}, IPv6: &cellIP{}}, `[Match]
Name=wwan1

[Link]
RequiredForOnline=no
MTUBytes=1200

[Network]
LinkLocalAddressing=no
IPv6AcceptRA=no
DHCP=ipv4

[DHCPv4]
RouteMetric=2048
`},
		{"router advertisements alone", cellLink{Iface: "rmnet_data0", MTU: 1000, IPv6: &cellIP{Method: "dhcp", DNS: []string{"2001:db8::53"}}}, `[Match]
Name=rmnet_data0

[Link]
RequiredForOnline=no

[Network]
LinkLocalAddressing=ipv6
IPv6AcceptRA=yes
DNS=2001:db8::53

[IPv6AcceptRA]
RouteMetric=2048
`},
	} {
		got, err := cellNetwork(tc.link)
		if err != nil || got != tc.want {
			t.Errorf("%s: %v\n%s\nwant\n%s", tc.name, err, got, tc.want)
		}
	}
}

// What telephonyd passes on reaches networkd only as values parsed again:
// a name, an address or a server that is not one is refused, and so is a
// link nothing can configure.
func TestCellNetworkRefuses(t *testing.T) {
	ok := simCellLink()
	for name, mod := range map[string]func(l *cellLink){
		"a newline in the name":      func(l *cellLink) { l.Iface = "wwan0\n[Network]" },
		"a long name":                func(l *cellLink) { l.Iface = "wwan0123456789ab" },
		"a dot name":                 func(l *cellLink) { l.Iface = ".." },
		"no name":                    func(l *cellLink) { l.Iface = "" },
		"an IPv6 address for IPv4":   func(l *cellLink) { l.IPv4.Address = "2001:db8::1" },
		"an IPv4 address for IPv6":   func(l *cellLink) { l.IPv6.Address = "10.0.0.1" },
		"a mapped address":           func(l *cellLink) { l.IPv6.Address = "::ffff:10.0.0.1" },
		"a zone":                     func(l *cellLink) { l.IPv6.Gateway = "fe80::1%wwan0" },
		"text as an address":         func(l *cellLink) { l.IPv4.Address = "10.0.0.1\nDNS=1.1.1.1" },
		"text as a server":           func(l *cellLink) { l.IPv4.DNS = []string{"8.8.8.8 9.9.9.9"} },
		"an unspecified gateway":     func(l *cellLink) { l.IPv4.Gateway = "0.0.0.0" },
		"PPP":                        func(l *cellLink) { l.IPv4.Method = "ppp" },
		"nothing to configure yet":   func(l *cellLink) { l.IPv4, l.IPv6 = nil, &cellIP{Method: ""} },
		"a static IPv4 with nothing": func(l *cellLink) { l.IPv4 = &cellIP{Method: "static"} },
	} {
		l := ok
		l.IPv4, l.IPv6 = new(cellIP), new(cellIP)
		*l.IPv4, *l.IPv6 = *ok.IPv4, *ok.IPv6
		mod(&l)
		if conf, err := cellNetwork(l); err == nil {
			t.Errorf("%s: accepted:\n%s", name, conf)
		}
	}
}

// The worker writes the link's file and has networkd configure it; when the
// link goes, the file turns Unmanaged and the addresses go; a file a
// previous Shell left is taken over.
func TestCellNetWorker(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var ran []string
	var logs bytes.Buffer
	n := &cellNet{dir: dir, up: map[string]string{}, kick: make(chan struct{}, 1), log: log.New(&logs, "", 0).Printf,
		run: func(name string, args ...string) error {
			mu.Lock()
			defer mu.Unlock()
			ran = append(ran, name+" "+strings.Join(args, " "))
			return nil
		}, exists: func(iface string) bool { return iface == "simwwan0" }}
	took := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := ran
		ran = nil
		return out
	}
	l := simCellLink()
	n.apply(&l)
	raw, err := os.ReadFile(filepath.Join(dir, "10-kario-cell-simwwan0.network"))
	want, _ := cellNetwork(l)
	if err != nil || string(raw) != want {
		t.Fatalf("file: %v\n%s", err, raw)
	}
	if got := strings.Join(took(), "; "); got != "networkctl reload; networkctl reconfigure simwwan0" {
		t.Fatalf("ran %q", got)
	}
	n.apply(&l)
	if got := took(); len(got) != 0 {
		t.Fatalf("the same link again ran %q", got)
	}
	n.apply(nil)
	raw, _ = os.ReadFile(filepath.Join(dir, "10-kario-cell-simwwan0.network"))
	if string(raw) != unmanagedConf("Name", "simwwan0") {
		t.Fatalf("after the link went: %s", raw)
	}
	if got := strings.Join(took(), "; "); got != "networkctl reload; ip addr flush dev simwwan0; ip link set simwwan0 down" {
		t.Fatalf("down ran %q", got)
	}
	// Another interface: the old one goes first; one that is not there
	// is not reconfigured.
	n.apply(&l)
	took()
	other := l
	other.Iface = "wwan0"
	n.apply(&other)
	if got := strings.Join(took(), "; "); got != "networkctl reload; ip addr flush dev simwwan0; ip link set simwwan0 down; networkctl reload" {
		t.Fatalf("moving ran %q", got)
	}
	bad := l
	bad.IPv4 = &cellIP{Method: "ppp"}
	n.apply(&bad)
	if !strings.Contains(logs.String(), "not configured: IPv4: a PPP link") {
		t.Fatalf("log: %s", logs.String())
	}

	again := &cellNet{dir: dir, up: map[string]string{}}
	_ = os.WriteFile(filepath.Join(dir, "10-kario-cell-wwan1.network"), []byte(want), 0o644)
	again.adopt()
	if len(again.up) != 1 || again.up["wwan1"] != want {
		t.Fatalf("adopted %v", again.up)
	}
}

// The status bar shows R while the phone roams abroad, beside the network
// type when the data link is up; national roaming shows none. Settings
// sees the switches, the roaming, the APN and the link's address.
func TestRoamingStatus(t *testing.T) {
	c := &Core{settings: defaultSettings()}
	c.tel = &telStatus{Registered: true, Signal: 3, RAT: "4G", Radio: true, Data: true, DataState: "connected", DataAPN: "cmnet",
		DataIP: &cellLink{IPv4: &cellIP{Address: "100.80.12.34"}, IPv6: &cellIP{Address: "2001:db8::1"}}}
	if got := c.telRight(); got != "4G ▂▄▆" {
		t.Fatalf("at home: %q", got)
	}
	c.tel.Roaming = "national"
	if got := c.telRight(); got != "4G ▂▄▆" {
		t.Fatalf("national roaming: %q", got)
	}
	c.tel.Roaming = "international"
	if got := c.telRight(); got != "4G R ▂▄▆" {
		t.Fatalf("abroad, data roaming on: %q", got)
	}
	st := c.radioState()
	if st.Roaming != "international" || st.APN != "cmnet" || st.Address != "100.80.12.34" || st.DataRoaming {
		t.Fatalf("radio state %+v", st)
	}
	c.tel.DataState = "roaming"
	if got := c.telRight(); got != "R ▂▄▆" {
		t.Fatalf("abroad, data roaming off: %q", got)
	}
	if st := c.radioState(); st.Data != "roaming" || st.Address != "" {
		t.Fatalf("radio state %+v", st)
	}
	c.settings.DataRoaming = true
	if !c.radioState().DataRoaming {
		t.Fatal("the switch")
	}
}

// Settings' apn.* go to telephonyd as they are (system apps only), the
// data roaming switch is kept and passed on, and the link telephonyd
// reports is what networkd is asked for.
func TestAPNAndRoamingSwitch(t *testing.T) {
	settings := &App{M: &Manifest{ID: "com.phone.settings", Name: "Settings", Permissions: []string{ui.PermSystem}}, running: true, ready: true, out: make(chan outbound, 64)}
	other := &App{M: &Manifest{ID: "com.example.notes", Name: "Notes"}, running: true, ready: true, out: make(chan outbound, 64)}
	lt := newLocTest(t, settings, other)
	lt.c.settings = defaultSettings()
	run, err := os.MkdirTemp("", "apn-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(run)
	lt.c.cfg.RunDir = run
	lt.c.broker.clients = map[string]*wire.Client{}

	lt.req(settings, ui.CapAPNList, nil)
	if r := lt.reply(settings); r.OK {
		t.Fatal("apn.list without telephonyd")
	}
	var mu sync.Mutex
	var asked []string
	if _, err := wire.ServeHub(filepath.Join(run, svc.TelephonydSock), func(method string, params json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, method+" "+string(params))
		return ui.APNState{MCC: "460", MNC: "00", Carrier: "China Mobile", Selected: "cmnet", APNs: []ui.APN{{ID: "cmnet", Name: "CMNET", APN: "cmnet", Types: []string{"default"}}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	lt.c.tel = &telStatus{Registered: true, Radio: true, Data: true}
	lt.req(settings, ui.CapAPNList, nil)
	r := lt.reply(settings)
	var st ui.APNState
	if !r.OK || json.Unmarshal(r.Data, &st) != nil || st.Selected != "cmnet" || len(st.APNs) != 1 {
		t.Fatalf("apn.list: %+v %s", r, r.Data)
	}
	pass := "secret"
	lt.req(settings, ui.CapAPNSet, ui.APNArgs{Op: ui.APNSave, APN: &ui.APN{Name: "Office", APN: "corp.example", Types: []string{"default"}, IPType: "ipv4", Auth: "chap", User: "me", Password: &pass}})
	if r := lt.reply(settings); !r.OK {
		t.Fatalf("apn.set: %s", r.Err)
	}
	lt.req(other, ui.CapAPNList, nil)
	if r := lt.reply(other); r.OK {
		t.Fatal("an app without the system permission saw the APNs")
	}
	mu.Lock()
	if len(asked) != 2 || asked[0] != "apn.list null" || !strings.HasPrefix(asked[1], `apn.set {"op":"save","apn":{"name":"Office","apn":"corp.example"`) {
		t.Fatalf("telephonyd was asked %q", asked)
	}
	mu.Unlock()

	lt.req(settings, ui.CapRadioSet, ui.RadioArgs{DataRoaming: on(true)})
	r = lt.reply(settings)
	var radio ui.RadioState
	if !r.OK || json.Unmarshal(r.Data, &radio) != nil || !radio.DataRoaming || !lt.c.settings.DataRoaming {
		t.Fatalf("radio.set data_roaming: %+v %s", r, r.Data)
	}
	lt.pump("roaming.set", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(asked) == 3 && asked[2] == `roaming.set {"on":true}`
	})

	n := &cellNet{dir: "x", kick: make(chan struct{}, 1), up: map[string]string{}}
	lt.c.radios.cell = n
	lt.c.tel = &telStatus{DataState: "connected", DataIP: &cellLink{Iface: "wwan0"}}
	lt.c.applyCellLink()
	if n.want == nil || n.want.Iface != "wwan0" {
		t.Fatalf("wanted %+v", n.want)
	}
	lt.c.tel.DataState = "roaming"
	lt.c.applyCellLink()
	if n.want != nil {
		t.Fatalf("roaming abroad, still wanted %+v", n.want)
	}
	lt.c.tel.DataState, lt.c.settings.MobileDataOff = "connected", true
	lt.c.applyCellLink()
	if n.want != nil {
		t.Fatalf("mobile data off, still wanted %+v", n.want)
	}
}
