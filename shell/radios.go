package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/kario-os/kario/obex"
	"github.com/kario-os/kario/obex/btphone"
	"github.com/kario-os/kario/ui"
)

// Wi-Fi and Bluetooth (Settings › Wi-Fi, Bluetooth): the shell drives iwd
// and BlueZ on the system bus (radio_iwd.go, radio_bluez.go). A machine
// without them that is not a phone (a developer's: not root) gets simulated
// radios with a few networks and devices, so the pages and the selftest run
// the same; a phone without them has none. Only system apps reach them; a
// network's passphrase goes from Settings to iwd's own store and nowhere
// else, and a Bluetooth pairing is confirmed by the user comparing the code
// the other device shows, in a system dialog.

type wifiRadio interface {
	state() (ui.WifiState, error)
	// link is only the network the station is on, without the scan
	// state starts: the move from an old phone looks every few seconds.
	link() (wifiLink, error)
	setEnabled(on bool) error
	connect(ssid string, cred ui.WifiArgs) error
	forget(ssid string) error
}

// wifiLink is the network the Wi-Fi station is on and the phone's IPv4
// address there; no address while it is on none. ifname is the station's
// interface; hint what a simulated network's DHCP server would say of
// being metered (metered.go asks a real one).
type wifiLink struct{ ssid, addr, ifname, hint string }

type btRadio interface {
	state() (ui.BTState, error)
	setEnabled(on bool) error
	discover(on bool) error
	pair(address string) error
	forget(address string) error
	// obexDial opens a session's link to a paired device's OBEX service
	// (btobex.go) and the largest packet the link carries (0: a stream).
	obexDial(address, uuid string) (io.ReadWriteCloser, int, error)
}

// pairTimeout covers the user comparing the code and the other device.
const pairTimeout = 45 * time.Second

// busCall bounds a call to iwd or BlueZ: a daemon that stops answering must
// not hold Settings up (joining a network and pairing have their own, longer
// limits).
func busCall(o dbus.BusObject, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return o.CallWithContext(ctx, method, 0, args...)
}

func (c *Core) startRadios() {
	var bus *dbus.Conn
	if b, err := dbus.ConnectSystemBus(); err == nil {
		bus = b
	}
	has := func(name string) bool {
		if bus == nil {
			return false
		}
		var ok bool
		return bus.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, name).Store(&ok) == nil && ok
	}
	sim := os.Geteuid() != 0 || os.Getenv("PHONE_SIM_RADIOS") == "1"
	switch {
	case has("net.connman.iwd"):
		c.wifi = &iwdRadio{bus: bus, logf: c.log.Printf}
	case sim:
		c.wifi = newSimWifi()
	}
	switch {
	case useBluez(has("org.bluez"), func() int { return bluezAdapters(bus) }, sim):
		r := &bluezRadio{bus: bus, confirm: c.confirmPairing, show: c.showPairCode, hide: c.hidePairCode, ask: c.askPairCode,
			serve: c.obexServe, peerBox: &c.radios.obex, logf: c.log.Printf}
		if err := r.register(); err != nil {
			c.log.Printf("bluetooth: no pairing agent: %v", err)
		}
		if err := r.registerOBEX(); err != nil {
			c.log.Printf("bluetooth: no file transfer or phone book: %v", err)
		}
		c.bt = r
	case sim:
		// On, as BlueZ powers adapters up by itself (AutoEnable).
		c.bt = &simBT{enabled: true, confirm: c.confirmPairing, show: c.showPairCode, serve: c.obexServe, peerBox: &c.radios.obex}
	}
	c.log.Printf("radios: wifi %s, bluetooth %s", radioName(c.wifi), radioName(c.bt))
	c.airplaneAtBoot()
	c.hotspotAtBoot()
}

// useBluez: BlueZ when it runs, except where the simulated radios are
// allowed and it has no adapter (a developer's machine whose bluetoothd
// found no controller): that would leave Settings and the selftest without
// Bluetooth. A phone takes BlueZ regardless, its adapter may come later.
func useBluez(owned bool, adapters func() int, sim bool) bool {
	return owned && (!sim || adapters() > 0)
}

// bluezAdapters counts the adapters BlueZ has.
func bluezAdapters(bus *dbus.Conn) int {
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := busCall(bus.Object("org.bluez", "/"), "org.freedesktop.DBus.ObjectManager.GetManagedObjects").Store(&objs); err != nil {
		return 0
	}
	n := 0
	for _, ifaces := range objs {
		if _, ok := ifaces["org.bluez.Adapter1"]; ok {
			n++
		}
	}
	return n
}

func radioName(r any) string {
	switch r.(type) {
	case *iwdRadio:
		return "iwd"
	case *bluezRadio:
		return "bluez"
	case *simWifi, *simBT:
		return "sim"
	}
	return "none"
}

// confirmPairing asks the user, from any goroutine, whether the code on the
// other device is the one shown; it blocks until they answer or time is up.
func (c *Core) confirmPairing(name string, passkey uint32) bool {
	answer := make(chan bool, 1)
	var o *confirmOverlay
	c.Post(func() {
		o = &confirmOverlay{text: tr.T("Bluetooth pairing with “%s”\nCode %06d\nIs it the same as on the other device?", name, passkey),
			onYes: func() { answer <- true }, onNo: func() { answer <- false }, trust: true, secret: true}
		c.pushOverlay(o)
	})
	select {
	case ok := <-answer:
		return ok
	case <-time.After(pairTimeout):
		c.Post(func() { c.popOverlay(o) })
		return false
	}
}

// inOrder runs one radio's changes one after another, as they came: off
// the loop iwd and BlueZ would take Bluetooth on and the search Settings
// asks for next in either order. On the loop.
type inOrder struct{ last chan struct{} }

func (q *inOrder) run(f func()) {
	prev, done := q.last, make(chan struct{})
	q.last = done
	go func() {
		if prev != nil {
			<-prev
		}
		f()
		close(done)
	}()
}

// The search for Bluetooth devices (bt.set's discover) is the app's that
// asked for it, which has to be in front with the screen on: it stops
// when that app leaves the foreground, is frozen or ends, and when the
// screen goes off, whatever the app does. Android's Settings searches
// only while its page to pair a device is in front; a search left on has
// the radio inquiring about every 16 seconds.

// btSearch takes an app's discover, on the loop.
func (c *Core) btSearch(a *App, on bool) error {
	switch {
	case !on:
		if c.radios.search == a {
			c.radios.search = nil
		}
	case a != c.fg || c.screen.off:
		return errors.New(tr.T("Only the app in front can search for Bluetooth devices"))
	default:
		if c.radios.search != a {
			c.log.Printf("bluetooth: %s searches for devices", a.M.ID)
		}
		c.radios.search = a
	}
	return nil
}

// btSearchEnd stops the search of a, if it has one (why: what became of
// it), after the changes already asked for.
func (c *Core) btSearchEnd(a *App, why string) {
	if a == nil || c.radios.search != a {
		return
	}
	c.radios.search = nil
	blue := c.bt
	if blue == nil {
		return
	}
	c.log.Printf("bluetooth: %s's search stopped: %s", a.M.ID, why)
	c.btOrder.run(func() {
		if err := blue.discover(false); err != nil {
			c.Post(func() { c.log.Printf("bluetooth: stopping the search: %v", err) })
		}
	})
}

// dispatchRadios serves wifi.* and bt.* off the loop: iwd and BlueZ calls
// take seconds (a connection, a pairing). The states answer at once; the
// changes keep their order.
func (b *broker) dispatchRadios(a *App, m ui.Msg, reply func(any, error)) {
	c := b.c
	var w ui.WifiArgs
	var bt ui.BTArgs
	_ = json.Unmarshal(m.Args, &w)
	_ = json.Unmarshal(m.Args, &bt)
	if m.Cap == ui.CapWifiSet && w.Enabled != nil && *w.Enabled && c.settings.Hotspot {
		c.wifiOverHotspot(reply)
		return
	}
	if m.Cap == ui.CapBTSet && bt.Discover != nil {
		if err := c.btSearch(a, *bt.Discover); err != nil {
			reply(nil, err)
			return
		}
	}
	wifi, blue := c.wifi, c.bt
	switch {
	case m.Cap == ui.CapWifiSet && w.Enabled != nil:
		c.airplaneSwitch(m.Cap, *w.Enabled)
	case m.Cap == ui.CapBTSet && bt.Enabled != nil:
		c.airplaneSwitch(m.Cap, *bt.Enabled)
	case m.Cap == ui.CapBTSet && bt.Phonebook != nil && bt.Address != "":
		if c.settings.BTPhonebook == nil {
			c.settings.BTPhonebook = map[string]bool{}
		}
		c.settings.BTPhonebook[bt.Address] = *bt.Phonebook
		c.saveSettings()
	case m.Cap == ui.CapBTForget && c.settings.BTPhonebook != nil:
		delete(c.settings.BTPhonebook, bt.Address)
		c.saveSettings()
	case m.Cap == ui.CapWifiSet && w.Metered != "":
		if w.SSID == "" {
			reply(nil, errors.New("wifi.set's metered needs the network's ssid"))
			return
		}
		if err := c.setMetered(w.SSID, w.Metered); err != nil {
			reply(nil, err)
			return
		}
	case m.Cap == ui.CapWifiForget && c.settings.WifiMetered[w.SSID] != "":
		_ = c.setMetered(w.SSID, ui.MeteredAuto)
	}
	book := maps.Clone(c.settings.BTPhonebook)
	choices := c.meteredChoices()
	work := func() {
		var out any
		var err error
		switch {
		case m.Cap == ui.CapWifiState && wifi == nil:
			out = ui.WifiState{State: "off", Networks: []ui.WifiNet{}}
		case m.Cap == ui.CapBTState && blue == nil:
			out = ui.BTState{Devices: []ui.BTDevice{}}
		case wifi == nil && (m.Cap == ui.CapWifiSet || m.Cap == ui.CapWifiConnect || m.Cap == ui.CapWifiForget):
			err = errors.New(tr.T("This device has no Wi-Fi"))
		case blue == nil && (m.Cap == ui.CapBTSet || m.Cap == ui.CapBTPair || m.Cap == ui.CapBTForget):
			err = errors.New(tr.T("This device has no Bluetooth"))
		case m.Cap == ui.CapWifiState:
			var st ui.WifiState
			if st, err = wifi.state(); err == nil {
				l, _ := wifi.link()
				c.meteredNets(&st, l, choices)
			}
			out = st
		case m.Cap == ui.CapWifiSet && w.Enabled != nil:
			err = wifi.setEnabled(*w.Enabled)
		case m.Cap == ui.CapWifiConnect:
			if err = wifi.connect(w.SSID, w); err == nil {
				// What detection finds is ready by the time something asks.
				go func() {
					if l, lerr := wifi.link(); lerr == nil && l.addr != "" {
						c.meterHint(l)
					}
				}()
			}
		case m.Cap == ui.CapWifiForget:
			err = wifi.forget(w.SSID)
		case m.Cap == ui.CapBTState:
			var st ui.BTState
			st, err = blue.state()
			for i, d := range st.Devices {
				if v, ok := book[d.Address]; ok && d.Paired {
					st.Devices[i].Phonebook = map[bool]string{true: "allowed", false: "denied"}[v]
				}
			}
			out = st
		case m.Cap == ui.CapBTSet:
			if bt.Enabled != nil {
				err = blue.setEnabled(*bt.Enabled)
			}
			if err == nil && bt.Discover != nil {
				err = blue.discover(*bt.Discover)
			}
		case m.Cap == ui.CapBTPair:
			err = blue.pair(bt.Address)
		case m.Cap == ui.CapBTForget:
			err = blue.forget(bt.Address)
		}
		if err != nil && m.Cap != ui.CapWifiState && m.Cap != ui.CapBTState {
			c.log.Printf("radios: %s: %v", m.Cap, err)
		}
		if out == nil && err == nil {
			out = map[string]bool{"ok": true}
		}
		c.Post(func() {
			if m.Cap == ui.CapWifiSet || m.Cap == ui.CapWifiConnect || m.Cap == ui.CapWifiForget {
				c.netMoved()
			}
			reply(out, err)
		})
	}
	switch m.Cap {
	case ui.CapWifiState, ui.CapBTState:
		go work()
	case ui.CapWifiSet, ui.CapWifiConnect, ui.CapWifiForget:
		c.wifiOrder.run(work)
	default:
		c.btOrder.run(work)
	}
}

// barsOf turns a signal in dBm into the status bar's bars.
func barsOf(dbm int) int {
	switch {
	case dbm >= -60:
		return 4
	case dbm >= -67:
		return 3
	case dbm >= -75:
		return 2
	case dbm >= -85:
		return 1
	}
	return 0
}

// The simulated radios: what a developer machine shows.

type simNet struct {
	ui.WifiNet
	pass             string
	identity, domain string // 802.1X
	// meteredHint: what its DHCP server says of being metered (a phone's
	// hotspot sends Android's hint, metered.go).
	meteredHint string
}

type simWifi struct {
	mu      sync.Mutex
	enabled bool
	nets    []*simNet
	ap      *simAP // the personal hotspot while it is on (hotspot.go)
	// addr is the phone's address on the network it is on (tests change
	// it as a new DHCP lease would).
	addr string
}

func newSimWifi() *simWifi {
	return &simWifi{enabled: true, addr: "192.168.50.23", nets: []*simNet{
		{WifiNet: ui.WifiNet{SSID: "Kario-Office", Security: "psk", Signal: 4}, pass: "kario1234"},
		{WifiNet: ui.WifiNet{SSID: "Kario-Guest", Security: "open", Signal: 3}},
		{WifiNet: ui.WifiNet{SSID: "CMCC-8F2A", Security: "psk", Signal: 1}, pass: "unknown!"},
	}}
}

// addEnterprise puts an 802.1X access point in range whose server takes
// one user and has a certificate for domain (selftest).
func (s *simWifi) addEnterprise(ssid, identity, pass, domain string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nets = append([]*simNet{{WifiNet: ui.WifiNet{SSID: ssid, Security: "8021x", Signal: 4}, pass: pass, identity: identity, domain: domain}}, s.nets...)
}

// checkEnterprise: what joining a new 802.1X network needs, checked before
// anything is written or sent.
func checkEnterprise(c ui.WifiArgs) error {
	switch {
	case c.Identity == "" || c.Passphrase == "" || c.Domain == "":
		return errors.New(ui.ErrNeedCredentials)
	case strings.ContainsAny(c.Identity+c.Passphrase+c.Domain, "\n\r"):
		return errors.New(tr.T("Line breaks are not allowed"))
	case !domainName.MatchString(c.Domain):
		return errors.New(tr.T("“%s” is not a domain name", c.Domain))
	case c.EAP != "" && c.EAP != "peap" && c.EAP != "ttls" && c.EAP != "ttls-pap":
		return errors.New(tr.T("Unknown EAP method %s", c.EAP))
	}
	return nil
}

var domainName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$`)

// addNetwork puts an access point in range (selftest), whose DHCP server
// gives meteredHint ("" none); one already in range under that name stays
// as it is.
func (s *simWifi) addNetwork(ssid, pass, meteredHint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.find(ssid) != nil {
		return
	}
	sec := "psk"
	if pass == "" {
		sec = "open"
	}
	s.nets = append([]*simNet{{WifiNet: ui.WifiNet{SSID: ssid, Security: sec, Signal: 4}, pass: pass, meteredHint: meteredHint}}, s.nets...)
}

func (s *simWifi) state() (ui.WifiState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := ui.WifiState{Backend: "sim", Enabled: s.enabled, State: "off", Networks: []ui.WifiNet{}}
	if !s.enabled {
		return st, nil
	}
	st.State = "disconnected"
	for _, n := range s.nets {
		st.Networks = append(st.Networks, n.WifiNet)
		if n.Connected {
			st.State, st.Address = "connected", s.addr
		}
	}
	sort.SliceStable(st.Networks, func(i, j int) bool { return st.Networks[i].Signal > st.Networks[j].Signal })
	return st, nil
}

func (s *simWifi) link() (wifiLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.nets {
		if s.enabled && n.Connected {
			return wifiLink{ssid: n.SSID, addr: s.addr, hint: n.meteredHint}, nil
		}
	}
	return wifiLink{}, nil
}

func (s *simWifi) setEnabled(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = on
	for _, n := range s.nets {
		n.Connected = n.Connected && on
	}
	return nil
}

func (s *simWifi) find(ssid string) *simNet {
	for _, n := range s.nets {
		if n.SSID == ssid {
			return n
		}
	}
	return nil
}

func (s *simWifi) connect(ssid string, cred ui.WifiArgs) error {
	time.Sleep(300 * time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.find(ssid)
	pass := cred.Passphrase
	switch {
	case !s.enabled:
		return errors.New(tr.T("Wi-Fi is off"))
	case n == nil:
		return errors.New(tr.T("Out of range"))
	case n.Security == "psk" && !n.Known && pass == "":
		return errors.New(ui.ErrNeedPassphrase)
	case n.Security == "psk" && !n.Known && pass != n.pass:
		return errors.New(tr.T("Connection failed: wrong passphrase"))
	case n.Security == "8021x" && !n.Known:
		if err := checkEnterprise(cred); err != nil {
			return err
		}
		// What a RADIUS server and its certificate would decide.
		switch {
		case cred.Domain != n.domain:
			return errors.New(tr.T("Connection failed: the server's certificate is not for %s", cred.Domain))
		case cred.Identity != n.identity || pass != n.pass:
			return errors.New(tr.T("Connection failed: wrong user name or password"))
		}
	}
	for _, x := range s.nets {
		x.Connected = false
	}
	n.Connected, n.Known = true, true
	return nil
}

func (s *simWifi) forget(ssid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.find(ssid); n != nil {
		n.Known, n.Connected = false, false
	}
	return nil
}

type simBT struct {
	mu          sync.Mutex
	enabled     bool
	discovering bool
	devices     []*ui.BTDevice
	added       int // devices ever added: their addresses stay unique when one goes
	// withScreen: devices that show a code to compare (a phone, a car);
	// the others pair as a headset does, without one.
	withScreen map[string]bool
	confirm    func(name string, passkey uint32) bool
	// keyboards: devices whose user types the code the phone shows
	// (bluezRadio's show), then Enter (selftest's bt-keyboard); keys is
	// what is typed on the one pairing: the digits so far, or -1 Enter.
	keyboards map[string]bool
	keys      chan int
	show      func(name, code string, entered int, gone <-chan struct{}, back func())
	// serve answers a device's OBEX session on the phone's side; peerBox
	// keeps what the devices were sent (btobex.go).
	serve   func(uuid, addr, name string, link io.ReadWriteCloser, mtu int)
	peerBox *obexState
	// open: the device-and-profile connections in use. BlueZ too has one
	// per device and profile, until the side that made it closes it.
	open map[string]bool
	// phones: the old phones to move from, by address (selftest); they
	// serve their phone book and messages.
	phones map[string]*btphone.Phone
}

// connect pairs a connection's two ends for a device's profile.
func (s *simBT) connect(addr, uuid string) (dialer io.ReadWriteCloser, answerer net.Conn, err error) {
	key := addr + " " + uuid
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open[key] {
		return nil, nil, errors.New("the device's last connection to this service is still open")
	}
	if s.open == nil {
		s.open = map[string]bool{}
	}
	s.open[key] = true
	a, b := net.Pipe()
	return &simLink{Conn: a, done: func() {
		s.mu.Lock()
		delete(s.open, key)
		s.mu.Unlock()
	}}, b, nil
}

type simLink struct {
	net.Conn
	once    sync.Once
	done    func()
	version uint16
}

func (l *simLink) peerVersion() uint16 { return l.version }

func (l *simLink) Close() error {
	err := l.Conn.Close()
	l.once.Do(l.done)
	return err
}

// obexDial reaches a paired simulated device's inbox, which takes every
// file, or an old phone's phone book and messages (PBAP 1.2).
func (s *simBT) obexDial(addr, uuid string) (io.ReadWriteCloser, int, error) {
	s.mu.Lock()
	paired := false
	for _, d := range s.devices {
		paired = paired || (d.Address == addr && d.Paired)
	}
	on, ph := s.enabled, s.phones[addr]
	s.mu.Unlock()
	switch {
	case !on:
		return nil, 0, errors.New(tr.T("Bluetooth is off"))
	case !paired:
		return nil, 0, errors.New(tr.T("Pair with the device first"))
	case uuid == uuidOPP:
	case ph != nil && (uuid == uuidPBAP || uuid == uuidMAS && ph.HasMessages()):
		phone, dev, err := s.connect(addr, uuid)
		if err != nil {
			return nil, 0, err
		}
		if uuid == uuidPBAP {
			phone.(*simLink).version = obex.PBAP12
		}
		go func() {
			defer dev.Close()
			_ = ph.Server().Serve(dev, 0)
		}()
		return phone, 0, nil
	default:
		return nil, 0, errNotOffered
	}
	phone, dev, err := s.connect(addr, uuid)
	if err != nil {
		return nil, 0, err
	}
	go s.peerBox.serve(uuid, dev, 0)
	return phone, 0, nil
}

// addOldPhone puts an old phone to move from nearby (selftest): it shows
// a pairing code and serves ph.
func (s *simBT) addOldPhone(name string, ph *btphone.Phone) {
	s.addPeer(name, true)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phones == nil {
		s.phones = map[string]*btphone.Phone{}
	}
	s.phones[s.devices[len(s.devices)-1].Address] = ph
}

// peerLink: the first paired simulated device opens a session to the
// phone's service (the selftest's push and phone book).
func (s *simBT) peerLink(uuid string) (io.ReadWriteCloser, int, error) {
	s.mu.Lock()
	var addr, name string
	for _, d := range s.devices {
		if d.Paired && addr == "" {
			addr, name = d.Address, d.Name
		}
	}
	on := s.enabled
	s.mu.Unlock()
	if !on || addr == "" {
		return nil, 0, errors.New("no paired simulated device in range")
	}
	dev, phone, err := s.connect("peer "+addr, uuid)
	if err != nil {
		return nil, 0, err
	}
	go s.serve(uuid, addr, name, phone, 0)
	return dev, 0, nil
}

// addPeer makes a device discoverable nearby (selftest); screen: it shows a
// pairing code the user compares.
func (s *simBT) addPeer(name string, screen bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.added++
	addr := fmt.Sprintf("02:4B:41:52:49:%02X", s.added)
	s.devices = append(s.devices, &ui.BTDevice{Address: addr, Name: name})
	if screen {
		if s.withScreen == nil {
			s.withScreen = map[string]bool{}
		}
		s.withScreen[addr] = true
	}
}

func (s *simBT) state() (ui.BTState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := ui.BTState{Backend: "sim", Enabled: s.enabled, Discovering: s.discovering, Name: tr.T("Kario phone"), Devices: []ui.BTDevice{}}
	if s.enabled {
		for _, d := range s.devices {
			if d.Paired || s.discovering {
				st.Devices = append(st.Devices, *d)
			}
		}
	}
	return st, nil
}

func (s *simBT) setEnabled(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled, s.discovering = on, s.discovering && on
	return nil
}

func (s *simBT) discover(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return errors.New(tr.T("Bluetooth is off"))
	}
	s.discovering = on
	return nil
}

func (s *simBT) pair(addr string) error {
	s.mu.Lock()
	var d *ui.BTDevice
	for _, x := range s.devices {
		if x.Address == addr {
			d = x
		}
	}
	keyboard := s.keyboards[addr]
	s.mu.Unlock()
	if d == nil {
		return errors.New(tr.T("Cannot find this device"))
	}
	if keyboard {
		if err := s.typeCode(d.Name); err != nil {
			return err
		}
	} else if s.withScreen[addr] && !s.confirm(d.Name, 482913) {
		return errors.New(tr.T("Pairing cancelled"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d.Paired, d.Connected = true, true
	return nil
}

// addKeyboard makes a keyboard discoverable nearby (selftest), which pairs
// by its user typing the code the phone shows.
func (s *simBT) addKeyboard(name string) {
	s.addPeer(name, false)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keyboards == nil {
		s.keyboards, s.keys = map[string]bool{}, make(chan int)
	}
	s.keyboards[s.devices[len(s.devices)-1].Address] = true
}

// typeCode is a keyboard's pairing: the code shown, its digits typed
// there as keys says, until Enter; Back on the phone cancels it.
func (s *simBT) typeCode(name string) error {
	const code = "482913"
	ended, back := make(chan struct{}), make(chan struct{}, 1)
	defer close(ended)
	cancel := func() {
		select {
		case back <- struct{}{}:
		default:
		}
	}
	s.show(name, code, 0, ended, cancel)
	for {
		select {
		case n := <-s.keys:
			if n < 0 {
				return nil
			}
			s.show(name, code, n, ended, cancel)
		case <-back:
			return errors.New(ui.ErrCancelled)
		case <-time.After(pairTimeout):
			return fmt.Errorf(tr.T("Pairing failed: %w"), context.DeadlineExceeded)
		}
	}
}

// removePeer takes the devices of that name out of range (selftest), but
// not a paired one.
func (s *simBT) removePeer(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.devices[:0]
	for _, d := range s.devices {
		if d.Name != name || d.Paired {
			kept = append(kept, d)
			continue
		}
		delete(s.withScreen, d.Address)
		delete(s.keyboards, d.Address)
		delete(s.phones, d.Address)
	}
	if len(kept) == len(s.devices) {
		return fmt.Errorf("no device %q nearby that is not paired", name)
	}
	clear(s.devices[len(kept):])
	s.devices = kept
	return nil
}

// typeOnKeyboard is what the user types on the keyboard being paired: n
// digits of the code so far, or Enter (-1).
func (s *simBT) typeOnKeyboard(n int) error {
	select {
	case s.keys <- n:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("no keyboard is pairing")
	}
}

func (s *simBT) forget(addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.Address == addr {
			d.Paired, d.Connected = false, false
		}
	}
	return nil
}
