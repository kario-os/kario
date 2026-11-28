package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/kario-os/kario/hal"
	"github.com/kario-os/kario/obex/btphone"
	"github.com/kario-os/kario/ui"
	"golang.org/x/sys/unix"
)

// BlueZ (org.bluez): the phone's adapter and its devices. Pairing goes
// through the shell's agent (capability DisplayYesNo): the code BlueZ asks
// to confirm is shown to the user, the code a keyboard is to type is shown
// until the pairing ends (btpasskey.go), the PIN or passkey a device asks
// for is typed on the code screen (btcode.go), and a device nobody asked
// to pair with is refused. The selftest may name a second adapter as the
// device to pair with (on the emulator, btvirt's virtual controllers);
// requests on that adapter's side are its own and are answered yes.

const (
	bluezName    = "org.bluez"
	bluezAdapter = bluezName + ".Adapter1"
	bluezDevice  = bluezName + ".Device1"
	bluezAgent   = dbus.ObjectPath("/org/kario/bluetooth/agent")
)

var errRejected = dbus.NewError(bluezName+".Error.Rejected", nil)

type bluezRadio struct {
	bus     *dbus.Conn
	confirm func(name string, passkey uint32) bool
	// show puts up the code to type on the device named name, entered
	// digits of it typed there, or follows them for the same pairing,
	// until gone closes or hide; back is the user's Back on it. ask has
	// the user type the code the device asks for (pin: a PIN, else a
	// passkey), "" for none; esc: the user refused it.
	show    func(name, code string, entered int, gone <-chan struct{}, back func())
	hide    func()
	ask     func(name string, pin bool, gone <-chan struct{}) (code string, esc bool)
	mu      sync.Mutex
	peer    dbus.ObjectPath // the selftest's other device, an adapter
	pairing *btPairing      // the pairing the user asked for
	// OBEX (btobex.go): serve answers a session on the phone's side,
	// peerBox keeps what the selftest's device was sent; dialing are the
	// devices this side connects a profile to, by path, waiting for
	// BlueZ's socket.
	serve   func(uuid, addr, name string, link io.ReadWriteCloser, mtu int)
	peerBox *obexState
	dialing map[dbus.ObjectPath]chan obexLink
	logf    func(format string, v ...any)
	// masServer: the selftest's old phone has its message server.
	masServer bool
}

type obexLink struct {
	f       *os.File
	mtu     int
	version uint16
}

// btPairing is a pairing the user asked for, while pair waits for it:
// ended closes once BlueZ's Pair has answered, which takes the pairing's
// screens away; back: the user cancelled it on one of them.
type btPairing struct {
	addr  string
	ended chan struct{}
	once  sync.Once
	back  atomic.Bool
}

func (p *btPairing) end() { p.once.Do(func() { close(p.ended) }) }

// begin makes addr the device the user asked to pair with, until finish.
func (r *bluezRadio) begin(addr string) *btPairing {
	p := &btPairing{addr: addr, ended: make(chan struct{})}
	r.mu.Lock()
	r.pairing = p
	r.mu.Unlock()
	return p
}

func (r *bluezRadio) finish(p *btPairing) {
	r.mu.Lock()
	if r.pairing == p {
		r.pairing = nil
	}
	r.mu.Unlock()
	p.end()
}

func (r *bluezRadio) current() *btPairing {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pairing
}

type bluezObjs map[dbus.ObjectPath]map[string]map[string]dbus.Variant

func (r *bluezRadio) load() (bluezObjs, error) {
	var o bluezObjs
	if err := busCall(r.bus.Object(bluezName, "/"), "org.freedesktop.DBus.ObjectManager.GetManagedObjects").Store(&o); err != nil {
		return nil, fmt.Errorf("bluez: %w", err)
	}
	return o, nil
}

func (o bluezObjs) prop(p dbus.ObjectPath, iface, name string) any {
	if v, ok := o[p][iface][name]; ok {
		return v.Value()
	}
	return nil
}

func (o bluezObjs) adapters() []dbus.ObjectPath {
	var out []dbus.ObjectPath
	for p, ifs := range o {
		if _, ok := ifs[bluezAdapter]; ok {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// adapter is the phone's: the first that is not the selftest's peer, a
// controller of the phone's own before a virtual one.
func (r *bluezRadio) adapter(o bluezObjs) dbus.ObjectPath {
	r.mu.Lock()
	peer := r.peer
	r.mu.Unlock()
	var virtual dbus.ObjectPath
	for _, a := range o.adapters() {
		switch {
		case a == peer:
		case !btVirtual(a):
			return a
		case virtual == "":
			virtual = a
		}
	}
	return virtual
}

// vhciDev is the kernel's maker of virtual controllers (hci_vhci), which
// btvirt opens.
var vhciDev = "/dev/vhci"

// btVirtual: the adapter's controller is a virtual one (hci_vhci's, as
// btvirt's are), which has no device of its own under /sys/devices.
func btVirtual(a dbus.ObjectPath) bool {
	p, err := filepath.EvalSymlinks(filepath.Join(hal.SysRoot, "class", "bluetooth", path.Base(string(a))))
	return err == nil && strings.Contains(p, "/devices/virtual/")
}

// peerOff says why the selftest's device to pair with cannot be among
// these adapters, "" when it can: it is a virtual controller (btvirt's),
// which reaches the other virtual controllers only, never a radio.
func (r *bluezRadio) peerOff(o bluezObjs) string {
	if a := r.adapter(o); a != "" && !btVirtual(a) {
		return fmt.Sprintf("the phone's adapter %s is a controller of its own, which the selftest's device, a virtual controller (btvirt), cannot reach", path.Base(string(a)))
	}
	if _, err := os.Stat(vhciDev); err != nil {
		return fmt.Sprintf("no virtual Bluetooth controllers for the selftest's device (the kernel's hci_vhci: %v)", err)
	}
	return ""
}

func (o bluezObjs) device(adapter dbus.ObjectPath, addr string) dbus.ObjectPath {
	for p, ifs := range o {
		if _, ok := ifs[bluezDevice]; ok && o.prop(p, bluezDevice, "Adapter") == adapter && o.prop(p, bluezDevice, "Address") == addr {
			return p
		}
	}
	return ""
}

func (o bluezObjs) name(p dbus.ObjectPath) string {
	for _, k := range []string{"Alias", "Name", "Address"} {
		if s, _ := o.prop(p, bluezDevice, k).(string); s != "" {
			return s
		}
	}
	return string(p)
}

func (r *bluezRadio) register() error {
	if err := r.bus.Export(&btAgent{r: r, who: r.who, cancel: r.cancelPairing}, bluezAgent, bluezName+".Agent1"); err != nil {
		return err
	}
	m := r.bus.Object(bluezName, "/org/bluez")
	if err := busCall(m, bluezName+".AgentManager1.RegisterAgent", bluezAgent, "DisplayYesNo").Err; err != nil {
		return err
	}
	return busCall(m, bluezName+".AgentManager1.RequestDefaultAgent", bluezAgent).Err
}

func (r *bluezRadio) state() (ui.BTState, error) {
	st := ui.BTState{Backend: "bluez", Devices: []ui.BTDevice{}}
	o, err := r.load()
	if err != nil {
		return st, err
	}
	a := r.adapter(o)
	if a == "" {
		return st, nil
	}
	st.Enabled = o.prop(a, bluezAdapter, "Powered") == true
	st.Discovering = o.prop(a, bluezAdapter, "Discovering") == true
	st.Name, _ = o.prop(a, bluezAdapter, "Alias").(string)
	if !st.Enabled {
		return st, nil
	}
	for p, ifs := range o {
		if _, ok := ifs[bluezDevice]; !ok || o.prop(p, bluezDevice, "Adapter") != a {
			continue
		}
		d := ui.BTDevice{Name: o.name(p)}
		d.Address, _ = o.prop(p, bluezDevice, "Address").(string)
		d.Paired = o.prop(p, bluezDevice, "Paired") == true
		d.Connected = o.prop(p, bluezDevice, "Connected") == true
		if d.Paired || st.Discovering {
			st.Devices = append(st.Devices, d)
		}
	}
	sort.Slice(st.Devices, func(i, j int) bool { return st.Devices[i].Name < st.Devices[j].Name })
	return st, nil
}

func (r *bluezRadio) setEnabled(on bool) error {
	o, err := r.load()
	if err != nil {
		return err
	}
	a := r.adapter(o)
	if a == "" {
		return errors.New(tr.T("No Bluetooth adapter"))
	}
	return busCall(r.bus.Object(bluezName, a), "org.freedesktop.DBus.Properties.Set", bluezAdapter, "Powered", dbus.MakeVariant(on)).Err
}

func (r *bluezRadio) discover(on bool) error {
	o, err := r.load()
	if err != nil {
		return err
	}
	a := r.adapter(o)
	if a == "" {
		return errors.New(tr.T("No Bluetooth adapter"))
	}
	method := "StartDiscovery"
	if !on {
		method = "StopDiscovery"
	}
	err = busCall(r.bus.Object(bluezName, a), bluezAdapter+"."+method).Err
	var de dbus.Error
	if errors.As(err, &de) && (strings.HasSuffix(de.Name, ".InProgress") || strings.HasSuffix(de.Name, ".Failed") && !on) {
		return nil
	}
	return err
}

func (r *bluezRadio) pair(addr string) error {
	o, err := r.load()
	if err != nil {
		return err
	}
	p := o.device(r.adapter(o), addr)
	if p == "" {
		return errors.New(tr.T("Cannot find this device"))
	}
	pairing := r.begin(addr)
	defer r.finish(pairing)
	dev := r.bus.Object(bluezName, p)
	ctx, cancel := context.WithTimeout(context.Background(), pairTimeout+10*time.Second)
	defer cancel()
	err = dev.CallWithContext(ctx, bluezDevice+".Pair", 0).Err
	pairing.end()
	if err != nil {
		return pairErr(pairing, err)
	}
	_ = busCall(dev, "org.freedesktop.DBus.Properties.Set", bluezDevice, "Trusted", dbus.MakeVariant(true)).Err
	// A device without a profile the phone speaks stays paired, not connected.
	_ = dev.CallWithContext(ctx, bluezDevice+".Connect", 0).Err
	return nil
}

// pairErr is what a Pair that failed with err ends in: nothing for a
// device paired already, cancelled (which Settings does not show) when
// the user cancelled it on the phone.
func pairErr(p *btPairing, err error) error {
	var de dbus.Error
	switch {
	case errors.As(err, &de) && strings.HasSuffix(de.Name, ".AlreadyExists"):
		return nil
	case p.back.Load():
		return errors.New(ui.ErrCancelled)
	}
	return fmt.Errorf(tr.T("Pairing failed: %w"), err)
}

func (r *bluezRadio) forget(addr string) error {
	o, err := r.load()
	if err != nil {
		return err
	}
	a := r.adapter(o)
	if p := o.device(a, addr); p != "" {
		return busCall(r.bus.Object(bluezName, a), bluezAdapter+".RemoveDevice", p).Err
	}
	return nil
}

// setPeer makes the last adapter the device to pair with, discoverable as
// name (the selftest on the emulator's virtual controllers).
func (r *bluezRadio) setPeer(name string) error {
	var as []dbus.ObjectPath
	for end := time.Now().Add(10 * time.Second); len(as) < 2; time.Sleep(200 * time.Millisecond) {
		o, err := r.load()
		if err != nil {
			return err
		}
		if off := r.peerOff(o); off != "" {
			return errors.New(off)
		}
		if as = o.adapters(); len(as) < 2 && time.Now().After(end) {
			return fmt.Errorf("%d Bluetooth adapters, need two", len(as))
		}
	}
	peer := as[len(as)-1]
	r.mu.Lock()
	r.peer = peer
	r.mu.Unlock()
	obj := r.bus.Object(bluezName, peer)
	for _, kv := range []struct {
		k string
		v any
	}{{"Powered", true}, {"Alias", name}, {"Pairable", true}, {"DiscoverableTimeout", uint32(0)}, {"Discoverable", true}} {
		if err := busCall(obj, "org.freedesktop.DBus.Properties.Set", bluezAdapter, kv.k, dbus.MakeVariant(kv.v)).Err; err != nil {
			return fmt.Errorf("%s: %w", kv.k, err)
		}
	}
	return nil
}

// btAgent is the pairing agent BlueZ calls, each call on a goroutine of
// its own. who and cancel are the radio's (tests give their own).
type btAgent struct {
	r      *bluezRadio
	who    func(dev dbus.ObjectPath) (peer, asked bool, name string)
	cancel func(dev dbus.ObjectPath)
	mu     sync.Mutex
	// stop closes when BlueZ cancels the request the code screen is up
	// for.
	stop chan struct{}
}

// who reports whether a request is the selftest peer's own side, whether
// the user asked to pair with that device, and its name.
func (r *bluezRadio) who(dev dbus.ObjectPath) (peer, asked bool, name string) {
	o, err := r.load()
	if err != nil {
		return false, false, string(dev)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	addr, _ := o.prop(dev, bluezDevice, "Address").(string)
	return r.peer != "" && o.prop(dev, bluezDevice, "Adapter") == r.peer, addr != "" && r.pairing != nil && addr == r.pairing.addr, o.name(dev)
}

// cancelPairing ends the pairing with dev under way: its Pair answers
// AuthenticationCanceled.
func (r *bluezRadio) cancelPairing(dev dbus.ObjectPath) {
	if err := busCall(r.bus.Object(bluezName, dev), bluezDevice+".CancelPairing").Err; err != nil {
		r.logf("bluetooth: cancelling the pairing with %s: %v", dev, err)
	}
}

// Release and Cancel take the screens of the request away: BlueZ has let
// the agent go, or no longer waits for that request's answer.
func (a *btAgent) Release() *dbus.Error { return a.Cancel() }

func (a *btAgent) Cancel() *dbus.Error {
	a.mu.Lock()
	if a.stop != nil {
		close(a.stop)
		a.stop = nil
	}
	a.mu.Unlock()
	a.r.hide()
	return nil
}

// DisplayPasskey has the user type passkey on the device (a keyboard);
// BlueZ calls it again as the keys are typed there (entered). A legacy
// keyboard gets its PIN the same way (DisplayPinCode), told once.
func (a *btAgent) DisplayPasskey(dev dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	return a.display(dev, fmt.Sprintf("%06d", passkey), int(entered))
}

func (a *btAgent) DisplayPinCode(dev dbus.ObjectPath, pin string) *dbus.Error {
	return a.display(dev, pin, 0)
}

func (a *btAgent) display(dev dbus.ObjectPath, code string, entered int) *dbus.Error {
	peer, asked, name := a.who(dev)
	p := a.r.current()
	switch {
	case peer:
		return nil
	case !asked || p == nil:
		return errRejected
	}
	a.r.show(name, code, entered, p.ended, func() {
		a.r.logf("bluetooth: pairing with %s cancelled on the phone", dev)
		p.back.Store(true)
		a.cancel(dev)
	})
	return nil
}

// RequestPinCode and RequestPasskey have the user type the code the
// device shows or wants (a car kit's PIN, often 0000), as long as the
// pairing waits; none is a refusal.
func (a *btAgent) RequestPinCode(dev dbus.ObjectPath) (string, *dbus.Error) {
	return a.request(dev, true)
}

func (a *btAgent) RequestPasskey(dev dbus.ObjectPath) (uint32, *dbus.Error) {
	code, err := a.request(dev, false)
	if err != nil {
		return 0, err
	}
	n, perr := strconv.ParseUint(code, 10, 32)
	if perr != nil || n > 999999 {
		return 0, errRejected
	}
	return uint32(n), nil
}

func (a *btAgent) request(dev dbus.ObjectPath, pin bool) (string, *dbus.Error) {
	peer, asked, name := a.who(dev)
	p := a.r.current()
	if peer || !asked || p == nil {
		return "", errRejected
	}
	stop := make(chan struct{})
	a.mu.Lock()
	a.stop = stop
	a.mu.Unlock()
	gone := make(chan struct{})
	go func() {
		select {
		case <-stop:
		case <-p.ended:
		}
		close(gone)
	}()
	code, esc := a.r.ask(name, pin, gone)
	a.mu.Lock()
	if a.stop == stop {
		a.stop = nil
	}
	a.mu.Unlock()
	if code == "" {
		a.r.logf("bluetooth: pairing with %s: no code typed (refused on the phone: %v)", dev, esc)
		if esc {
			p.back.Store(true)
		}
		return "", errRejected
	}
	return code, nil
}

func (a *btAgent) RequestConfirmation(dev dbus.ObjectPath, passkey uint32) *dbus.Error {
	peer, _, name := a.who(dev)
	if peer || a.r.confirm(name, passkey) {
		return nil
	}
	return errRejected
}

func (a *btAgent) RequestAuthorization(dev dbus.ObjectPath) *dbus.Error {
	if peer, asked, _ := a.who(dev); peer || asked {
		return nil
	}
	return errRejected
}

func (a *btAgent) AuthorizeService(dev dbus.ObjectPath, _ string) *dbus.Error {
	o, err := a.r.load()
	if peer, asked, _ := a.who(dev); err == nil && (peer || asked || o.prop(dev, bluezDevice, "Trusted") == true) {
		return nil
	}
	return errRejected
}

// The OBEX profiles (btobex.go) are BlueZ external profiles: BlueZ
// publishes their service records, listens (RFCOMM, and L2CAP for GOEP
// 2.0 clients) and hands each connection's socket to NewConnection, for a
// device connecting to the phone or one the phone connected to
// (ConnectProfile, which BlueZ matches against the profile's remote
// service: the phone book's is the device's phone book, which a move
// from an old phone reads, switch_bt.go). All need a paired device
// (RequireAuthentication); the user's own questions (each file, the
// phone book per device) are the shell's, not the agent's.
var obexProfiles = []struct {
	uuid, name string
	path       dbus.ObjectPath
	// role: "" both sides; "server" the record alone, as PBAP's client
	// has no channel and its remote is the phone book's, which the
	// profile above connects to; "client" connecting only, as Kario
	// serves no messages.
	role string
}{
	{uuidOPP, "Object Push", "/org/kario/bluetooth/opp", ""},
	{uuidPBAP, "Phone Book Access", "/org/kario/bluetooth/pbap", ""},
	{uuidPCE, "Phone Book Access Client", "/org/kario/bluetooth/pce", "server"},
	{uuidMAS, "Message Access", "/org/kario/bluetooth/mas", "client"},
}

func (r *bluezRadio) registerOBEX() error {
	r.dialing = map[dbus.ObjectPath]chan obexLink{}
	var errs []error
	for _, p := range obexProfiles {
		if err := r.registerProfile(p.uuid, p.name, p.path, p.role); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *bluezRadio) registerProfile(uuid, name string, path dbus.ObjectPath, role string) error {
	if err := r.bus.Export(&btProfile{r: r, uuid: uuid}, path, bluezName+".Profile1"); err != nil {
		return err
	}
	opts := map[string]dbus.Variant{"Name": dbus.MakeVariant(name), "RequireAuthentication": dbus.MakeVariant(true),
		"RequireAuthorization": dbus.MakeVariant(false), "AutoConnect": dbus.MakeVariant(false)}
	if role != "" {
		opts["Role"] = dbus.MakeVariant(role)
	}
	if err := busCall(r.bus.Object(bluezName, "/org/bluez"), bluezName+".ProfileManager1.RegisterProfile", path, uuid, opts).Err; err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

type btProfile struct {
	r    *bluezRadio
	uuid string
}

func (p *btProfile) Release() *dbus.Error                             { return nil }
func (p *btProfile) RequestDisconnection(dbus.ObjectPath) *dbus.Error { return nil }

// btSndMTU is BT_SNDMTU (linux/bluetooth.h): what an L2CAP socket sends in
// one packet.
const btSndMTU = 12

// NewConnection's properties have, for a connection this side made, the
// Version of the device's profile from its service record.
func (p *btProfile) NewConnection(dev dbus.ObjectPath, fd dbus.UnixFD, props map[string]dbus.Variant) *dbus.Error {
	n := int(fd)
	mtu := 0
	if t, err := unix.GetsockoptInt(n, unix.SOL_SOCKET, unix.SO_TYPE); err == nil && t == unix.SOCK_SEQPACKET {
		if mtu, err = unix.GetsockoptInt(n, unix.SOL_BLUETOOTH, btSndMTU); err != nil || mtu <= 0 {
			unix.Close(n)
			return dbus.MakeFailedError(fmt.Errorf("L2CAP link without an MTU: %v", err))
		}
	}
	var version uint16
	if v, ok := props["Version"]; ok {
		version, _ = v.Value().(uint16)
	}
	_ = unix.SetNonblock(n, true)
	go p.r.connected(p.uuid, dev, obexLink{f: os.NewFile(uintptr(n), "bluetooth"), mtu: mtu, version: version})
	return nil
}

func (r *bluezRadio) connected(uuid string, dev dbus.ObjectPath, l obexLink) {
	r.mu.Lock()
	ch := r.dialing[dev]
	delete(r.dialing, dev)
	peer := r.peer != "" && strings.HasPrefix(string(dev), string(r.peer)+"/")
	r.mu.Unlock()
	switch {
	case ch != nil:
		ch <- l
		return
	case peer:
		r.peerBox.serve(uuid, l.f, l.mtu)
		return
	}
	o, err := r.load()
	if err != nil {
		l.f.Close()
		return
	}
	addr, _ := o.prop(dev, bluezDevice, "Address").(string)
	r.serve(uuid, addr, o.name(dev), l.f, l.mtu)
}

// errNoSocket: ConnectProfile succeeded without handing a socket over,
// which BlueZ does when it still counts the device's profile as connected.
var errNoSocket = errors.New("bluez: connected, but no socket came")

// connectProfile connects a device's service and returns BlueZ's socket.
// Right after a session its link may still be closing (busy) or still be
// counted as connected (no socket); either gets another try for a while.
func (r *bluezRadio) connectProfile(dev dbus.ObjectPath, uuid string) (io.ReadWriteCloser, int, error) {
	end := time.Now().Add(10 * time.Second)
	for {
		l, err := r.connectOnce(dev, uuid)
		if err == nil {
			return &profileLink{File: l.f, r: r, dev: dev, uuid: uuid, version: l.version}, l.mtu, nil
		}
		if !retryConnect(err) || time.Now().After(end) {
			return nil, 0, err
		}
		r.logf("bluetooth: %s to %s: %v; trying again", uuid[4:8], dev, err)
		if errors.Is(err, errNoSocket) {
			if err := r.disconnectProfile(dev, uuid); err != nil {
				return nil, 0, err
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (r *bluezRadio) connectOnce(dev dbus.ObjectPath, uuid string) (obexLink, error) {
	ch := make(chan obexLink, 1)
	r.mu.Lock()
	r.dialing[dev] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.dialing, dev)
		r.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.bus.Object(bluezName, dev).CallWithContext(ctx, bluezDevice+".ConnectProfile", 0, uuid).Err; err != nil {
		return obexLink{}, err
	}
	// BlueZ sends NewConnection before it answers ConnectProfile.
	select {
	case l := <-ch:
		return l, nil
	case <-time.After(5 * time.Second):
		return obexLink{}, errNoSocket
	}
}

func retryConnect(err error) bool {
	var de dbus.Error
	return errors.Is(err, errNoSocket) ||
		errors.As(err, &de) && (de.Name == bluezName+".Error.InProgress" || strings.Contains(de.Error(), "busy"))
}

func (r *bluezRadio) disconnectProfile(dev dbus.ObjectPath, uuid string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.bus.Object(bluezName, dev).CallWithContext(ctx, bluezDevice+".DisconnectProfile", 0, uuid).Err; err != nil {
		return fmt.Errorf("bluez: disconnecting %s: %w", uuid[4:8], err)
	}
	return nil
}

// profileLink is a connection this side made. BlueZ keeps its own copy of
// the socket until the profile is disconnected: closing only ours leaves
// the link up, and the next ConnectProfile to the device gets no socket.
type profileLink struct {
	*os.File
	r       *bluezRadio
	dev     dbus.ObjectPath
	uuid    string
	version uint16
	once    sync.Once
}

func (l *profileLink) peerVersion() uint16 { return l.version }

func (l *profileLink) Close() error {
	err := l.File.Close()
	l.once.Do(func() {
		if derr := l.r.disconnectProfile(l.dev, l.uuid); derr != nil {
			l.r.logf("bluetooth: %v", derr)
			err = errors.Join(err, derr)
		}
	})
	return err
}

func (r *bluezRadio) obexDial(addr, uuid string) (io.ReadWriteCloser, int, error) {
	if r.dialing == nil {
		return nil, 0, errors.New(tr.T("Bluetooth file transfer is not available"))
	}
	o, err := r.load()
	if err != nil {
		return nil, 0, err
	}
	dev := o.device(r.adapter(o), addr)
	if dev == "" || o.prop(dev, bluezDevice, "Paired") != true {
		return nil, 0, errors.New(tr.T("Pair with the device first"))
	}
	// The services the old phone's record lists, read when it paired.
	if uuids, _ := o.prop(dev, bluezDevice, "UUIDs").([]string); (uuid == uuidPBAP || uuid == uuidMAS) && !slices.Contains(uuids, uuid) {
		return nil, 0, errNotOffered
	}
	return r.connectProfile(dev, uuid)
}

// setOldPhone makes the selftest's other adapter an old phone: its side
// serves the phone's phone book and messages, a message server is
// published for it (and, while the selftest runs, for the phone too:
// BlueZ's profiles are for all adapters), and the two are unpaired, so
// pairing again reads the services it has now.
func (r *bluezRadio) setOldPhone(name string, ph *btphone.Phone) error {
	r.peerBox.setPhone(ph)
	if ph.HasMessages() && !r.masServer {
		// BlueZ takes one profile per UUID: the phone's own, connecting
		// only, is registered again for both sides, which publishes the
		// record; connected() tells the two sides apart by the device.
		for _, p := range obexProfiles {
			if p.uuid != uuidMAS {
				continue
			}
			_ = busCall(r.bus.Object(bluezName, "/org/bluez"), bluezName+".ProfileManager1.UnregisterProfile", p.path).Err
			if err := r.registerProfile(p.uuid, p.name, p.path, ""); err != nil {
				return err
			}
		}
		r.masServer = true
	}
	if err := r.setPeer(name); err != nil {
		return err
	}
	o, err := r.load()
	if err != nil {
		return err
	}
	r.mu.Lock()
	peer := r.peer
	r.mu.Unlock()
	phone := r.adapter(o)
	phoneAddr, _ := o.prop(phone, bluezAdapter, "Address").(string)
	peerAddr, _ := o.prop(peer, bluezAdapter, "Address").(string)
	for _, side := range []struct {
		adapter dbus.ObjectPath
		other   string
	}{{phone, peerAddr}, {peer, phoneAddr}} {
		if d := o.device(side.adapter, side.other); d != "" {
			if err := busCall(r.bus.Object(bluezName, side.adapter), bluezAdapter+".RemoveDevice", d).Err; err != nil {
				return err
			}
		}
	}
	return nil
}

// peerLink: the selftest's other adapter connects to the phone's service.
// The phone's adapter has been seen missing from BlueZ's objects for a
// moment on QEMU (its address empty), so it is looked for a few seconds,
// and a failure names what was there.
func (r *bluezRadio) peerLink(uuid string) (io.ReadWriteCloser, int, error) {
	var o bluezObjs
	var peer, dev dbus.ObjectPath
	var phone string
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		var err error
		if o, err = r.load(); err != nil {
			return nil, 0, err
		}
		r.mu.Lock()
		peer = r.peer
		r.mu.Unlock()
		phone, _ = o.prop(r.adapter(o), bluezAdapter, "Address").(string)
		dev = o.device(peer, phone)
		if peer != "" && dev != "" || time.Now().After(end) {
			break
		}
	}
	if peer == "" || dev == "" || r.dialing == nil {
		var known []string
		for p, ifs := range o {
			if _, ok := ifs[bluezDevice]; ok && o.prop(p, bluezDevice, "Adapter") == peer {
				known = append(known, fmt.Sprint(o.prop(p, bluezDevice, "Address")))
			}
		}
		return nil, 0, fmt.Errorf("the other adapter does not know the phone (%s; adapters %v, the other %s, which knows %v)", phone, o.adapters(), peer, known)
	}
	return r.connectProfile(dev, uuid)
}
