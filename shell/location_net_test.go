package shell

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kario-os/kario/location"
	"github.com/kario-os/kario/ui"
)

// withNet gives the test the fake network location service, the simulated
// Wi-Fi and cell, its answers kept in the state directory.
func (lt *locTest) withNet() *location.FakeProvider {
	fake := &location.FakeProvider{Where: func() (float64, float64) { return 22.5431, 114.0579 }}
	lt.c.loc.net = locNet{loc: &location.Locator{Provider: fake, Path: filepath.Join(lt.c.cfg.StateDir, "netloc.json")}, kind: "fake", host: "fake",
		cells: func(context.Context) ([]location.Cell, error) { return []location.Cell{simCell}, nil }}
	lt.c.wifi = newSimWifi()
	return fake
}

// lookup lets the receiver have gone without a fix long enough and runs
// the tick; it waits for a lookup to end if one started.
func (lt *locTest) lookup() {
	lt.t.Helper()
	lt.c.loc.needSince = time.Now().Add(-locNetAfter)
	lt.c.loc.net.next = time.Time{}
	lt.c.locTick()
	lt.pump("the lookup", func() bool { return !lt.c.loc.net.busy })
}

// While the receiver gives no fix, the network places the phone (by Wi-Fi
// and the cell): a fix marked as the network's, without satellites; once
// the receiver fixes, the network is not asked.
func TestLocationNetworkFallback(t *testing.T) {
	maps := locApp("com.example.maps")
	lt := newLocTest(t, maps)
	fake := lt.withNet()
	lt.grant(maps, "granted")
	lt.c.fg = maps
	lt.req(maps, ui.CapLocationWatch, nil)
	lt.reply(maps)
	lt.pump("the receiver on", lt.gnss.on.Load)
	lt.c.locTick()
	if fake.Calls.Load() != 0 || lt.c.loc.net.busy {
		t.Fatal("the network asked before the receiver had its chance")
	}
	lt.lookup()
	got := events(maps, ui.EventLocation)
	if fake.Calls.Load() != 1 || len(got) != 1 || got[0].Source != ui.LocationSourceNetwork || got[0].Lat != 22.5431 ||
		got[0].AccuracyM != location.FakeWiFiAccuracyM || got[0].Satellites != 0 || got[0].AltitudeM != nil {
		t.Fatalf("%d calls, events %+v", fake.Calls.Load(), got)
	}
	lt.c.locTick()
	if lt.c.loc.net.busy {
		t.Fatal("asked again before locNetEvery")
	}
	lt.c.loc.watches[maps].sentAt = time.Now().Add(-time.Minute)
	lt.fix(beijing)
	if got := events(maps, ui.EventLocation); len(got) != 1 || got[0].Source != ui.LocationSourceGNSS {
		t.Fatalf("the receiver's fix: %+v", got)
	}
	lt.c.loc.net.next = time.Time{}
	lt.c.locTick()
	if lt.c.loc.net.busy || fake.Calls.Load() != 1 {
		t.Fatal("the network asked while the receiver fixes")
	}
	if v := lt.c.locationView()["network"].(map[string]any); v["provider"] != "fake" || v["on"] != true || v["requests"] != 1 {
		t.Fatalf("%v", v)
	}
}

// The network is asked only while location is on, the user lets it be,
// and an app allowed the location needs a fix; turned off, what it
// answered goes from the phone.
func TestLocationNetworkPrivacy(t *testing.T) {
	maps := locApp("com.example.maps")
	lt := newLocTest(t, maps)
	fake := lt.withNet()
	lt.grant(maps, "denied")
	lt.c.fg = maps
	lt.req(maps, ui.CapLocationGet, nil)
	lt.reply(maps)
	lt.lookup()
	if fake.Calls.Load() != 0 {
		t.Fatal("asked for an app denied the location")
	}
	lt.grant(maps, "granted")
	lt.req(maps, ui.CapLocationGet, nil)
	lt.lookup()
	if r := lt.reply(maps); !r.OK || fake.Calls.Load() != 1 {
		t.Fatalf("%+v, %d calls", r, fake.Calls.Load())
	}
	if _, err := os.Stat(lt.c.loc.net.loc.Path); err != nil {
		t.Fatal("no answers kept")
	}
	if ok, err := lt.c.locationSetting(ui.SettingArgs{Key: ui.SettingLocationNetwork, Value: 0}); !ok || err != nil || !lt.c.settings.LocationNetworkOff {
		t.Fatal(ok, err)
	}
	if _, err := os.Stat(lt.c.loc.net.loc.Path); !os.IsNotExist(err) {
		t.Fatal("the answers stay on the phone with the network location off")
	}
	lt.req(maps, ui.CapLocationGet, nil)
	lt.lookup()
	if fake.Calls.Load() != 1 {
		t.Fatal("asked with the network location off")
	}
	lt.c.loc.gets = nil
	lt.c.locationSetting(ui.SettingArgs{Key: ui.SettingLocationNetwork, Value: 1})
	lt.c.locationSetting(ui.SettingArgs{Key: ui.SettingLocation, Value: 0})
	lt.c.locationSetting(ui.SettingArgs{Key: ui.SettingLocation, Value: 1})
	lt.req(maps, ui.CapLocationGet, nil)
	lt.c.locationSetting(ui.SettingArgs{Key: ui.SettingLocation, Value: 0})
	lt.lookup()
	if fake.Calls.Load() != 1 {
		t.Fatal("asked with location off")
	}
	o := observe(context.Background(), lt.c.wifi, lt.c.loc.net.cells)
	lt.c.wifi.(*simWifi).addNetwork("Neighbour_nomap", "", "")
	all := observe(context.Background(), lt.c.wifi, nil)
	if len(o.WiFi) != 3 || len(o.Cells) != 1 || len(all.WiFi) != 4 || len(all.Usable().WiFi) != 3 {
		t.Fatalf("%+v / %+v", o, all.Usable())
	}
}

// A phone without a receiver has the location by the network alone; with
// the network location off too, it has none.
func TestLocationNetworkOnly(t *testing.T) {
	maps := locApp("com.example.maps")
	lt := newLocTest(t, maps)
	lt.withNet()
	lt.c.loc.src = nil
	lt.grant(maps, "granted")
	lt.c.fg = maps
	lt.req(maps, ui.CapLocationGet, nil)
	if lt.c.loc.running {
		t.Fatal("a receiver that is not there turned on")
	}
	lt.lookup()
	if l := lt.located(maps); l.Source != ui.LocationSourceNetwork || l.Lat != 22.5431 {
		t.Fatalf("%+v", l)
	}
	lt.c.locSetNetwork(false)
	lt.req(maps, ui.CapLocationGet, nil)
	if r := lt.reply(maps); r.Err != ui.ErrLocationUnavailable {
		t.Fatalf("%+v", r)
	}
}

// The product's profile names the A-GNSS server and the network location
// service; the key is read from the owner's file. A malformed one is
// none; a simulated phone has the fake service unless told otherwise.
func TestLocationProfile(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "geo.key")
	os.WriteFile(key, []byte("owner-key\n"), 0o600)
	write := func(body string) *Profile {
		t.Helper()
		layers := filepath.Join(t.TempDir(), "profile.d")
		os.MkdirAll(layers, 0o700)
		os.WriteFile(filepath.Join(layers, "20-product.json"), []byte(body), 0o600)
		p, err := LoadProfile(layers)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	core := func(p *Profile, sku SKU) *Core {
		c := &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: t.TempDir(), SKU: sku}, profile: p, ev: make(chan func(), 8)}
		c.locInit()
		return c
	}
	phone := SKU{ID: "kario-t9-sd662-2g", Modem: "modemmanager", GNSS: "modemmanager"}
	c := core(write(`{"location": {"supl_server": "supl.example.net:7275", "network": {"provider": "http", "url": "https://geo.example.net/v1/geolocate", "key_file": "`+key+`", "max_per_hour": 20}}}`), phone)
	hp, ok := c.loc.net.loc.Provider.(*location.HTTPProvider)
	if !ok || hp.Key != "owner-key" || hp.URL != "https://geo.example.net/v1/geolocate" || c.loc.net.host != "geo.example.net" || c.loc.net.loc.MaxPerHour != 20 || c.loc.net.cells == nil {
		t.Fatalf("%+v %+v", c.loc.net, c.loc.net.loc)
	}
	mm, ok := c.loc.src.(*location.ModemManager)
	if !ok || mm.SUPL != "supl.example.net:7275" || mm.Assistance == nil || mm.Assisted == nil {
		t.Fatalf("%+v", c.loc.src)
	}
	mm.Assisted(location.Assist{Modem: "/m/0", SUPL: mm.SUPL, AGPS: "msb", Injected: 42})
	(<-c.ev)()
	if a, _ := c.locationView()["assistance"].(*location.Assist); a == nil || a.Injected != 42 {
		t.Fatalf("%+v", c.locationView()["assistance"])
	}
	c = core(write(`{"location": {"supl_server": "supl.example.net", "no_assistance_data": true, "network": {"provider": "http", "url": "http://geo.example.net/v1/geolocate"}}}`), phone)
	if mm := c.loc.src.(*location.ModemManager); mm.SUPL != "" || mm.Assistance != nil || c.loc.net.loc != nil {
		t.Fatalf("SUPL %q, assistance %v, network %+v", mm.SUPL, mm.Assistance != nil, c.loc.net)
	}
	c = core(write(`{"location": {"network": {"provider": "http", "url": "https://geo.example.net/", "key_file": "`+filepath.Join(dir, "missing")+`"}}}`), phone)
	if c.loc.net.loc != nil {
		t.Fatal("a service without its key")
	}
	t.Setenv("KARIO_TEST_GEO_KEY", "from-env")
	c = core(write(`{"location": {"network": {"provider": "http", "url": "http://127.0.0.1:8080/v1/geolocate", "key_env": "KARIO_TEST_GEO_KEY"}}}`), phone)
	if hp, ok := c.loc.net.loc.Provider.(*location.HTTPProvider); !ok || hp.Key != "from-env" {
		t.Fatalf("%+v", c.loc.net)
	}
	for _, x := range []struct {
		p    *Profile
		sku  SKU
		kind string
	}{
		{nil, SKU{}, "fake"},
		{nil, SKU{ID: "qemu-t9", Modem: "sim", GNSS: "fifo:gnss.nmea"}, "fake"},
		{nil, phone, ""},
		{write(`{"location": {"network": {"provider": "none"}}}`), SKU{}, ""},
		{write(`{"location": {"network": {"provider": "carrier-pigeon"}}}`), SKU{}, ""},
	} {
		if c := core(x.p, x.sku); c.loc.net.kind != x.kind || (x.kind == "") != (c.loc.net.loc == nil) {
			t.Errorf("%+v: %q", x.sku, c.loc.net.kind)
		}
	}
}
