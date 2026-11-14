package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/kario-os/kario/ui"
	"golang.org/x/sys/unix"
)

// BlueZ (org.bluez): the phone's adapter and its devices. Pairing goes
// through the shell's agent (capability DisplayYesNo): the code BlueZ asks
// to confirm is shown to the user, and a device nobody asked to pair with is
// refused. The selftest may name a second adapter as the device to pair
// with (on the emulator, btvirt's virtual controllers); requests on that
// adapter's side are its own and are answered yes.

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
	mu      sync.Mutex
	peer    dbus.ObjectPath // the selftest's other device, an adapter
	pairing string          // the address the user asked to pair with
	// OBEX (btobex.go): serve answers a session on the phone's side,
	// peerBox keeps what the selftest's device was sent; dialing are the
	// devices this side connects a profile to, by path, waiting for
	// BlueZ's socket.
	serve   func(uuid, addr, name string, link io.ReadWriteCloser, mtu int)
	peerBox *obexState
	dialing map[dbus.ObjectPath]chan obexLink
	logf    func(format string, v ...any)
}

type obexLink struct {
	f   *os.File
	mtu int
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

// adapter is the phone's: the first that is not the selftest's peer.
func (r *bluezRadio) adapter(o bluezObjs) dbus.ObjectPath {
	r.mu.Lock()
	peer := r.peer
	r.mu.Unlock()
	for _, a := range o.adapters() {
		if a != peer {
			return a
		}
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
	if err := r.bus.Export(&btAgent{r: r}, bluezAgent, bluezName+".Agent1"); err != nil {
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
	r.mu.Lock()
	r.pairing = addr
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.pairing = ""
		r.mu.Unlock()
	}()
	dev := r.bus.Object(bluezName, p)
	ctx, cancel := context.WithTimeout(context.Background(), pairTimeout+10*time.Second)
	defer cancel()
	if err := dev.CallWithContext(ctx, bluezDevice+".Pair", 0).Err; err != nil {
		var de dbus.Error
		if errors.As(err, &de) && strings.HasSuffix(de.Name, ".AlreadyExists") {
			return nil
		}
		return fmt.Errorf(tr.T("Pairing failed: %w"), err)
	}
	_ = busCall(dev, "org.freedesktop.DBus.Properties.Set", bluezDevice, "Trusted", dbus.MakeVariant(true)).Err
	// A device without a profile the phone speaks stays paired, not connected.
	_ = dev.CallWithContext(ctx, bluezDevice+".Connect", 0).Err
	return nil
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

// btAgent is the pairing agent BlueZ calls.
type btAgent struct{ r *bluezRadio }

// who reports whether a request is the selftest peer's own side, whether
// the user asked to pair with that device, and its name.
func (a *btAgent) who(dev dbus.ObjectPath) (peer, asked bool, name string) {
	o, err := a.r.load()
	if err != nil {
		return false, false, string(dev)
	}
	a.r.mu.Lock()
	defer a.r.mu.Unlock()
	addr, _ := o.prop(dev, bluezDevice, "Address").(string)
	return a.r.peer != "" && o.prop(dev, bluezDevice, "Adapter") == a.r.peer, addr != "" && addr == a.r.pairing, o.name(dev)
}

func (a *btAgent) Release() *dbus.Error                                       { return nil }
func (a *btAgent) Cancel() *dbus.Error                                        { return nil }
func (a *btAgent) DisplayPinCode(dbus.ObjectPath, string) *dbus.Error         { return nil }
func (a *btAgent) DisplayPasskey(dbus.ObjectPath, uint32, uint16) *dbus.Error { return nil }
func (a *btAgent) RequestPinCode(dbus.ObjectPath) (string, *dbus.Error)       { return "", errRejected }
func (a *btAgent) RequestPasskey(dbus.ObjectPath) (uint32, *dbus.Error)       { return 0, errRejected }

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
// (ConnectProfile). Both need a paired device (RequireAuthentication);
// the user's own questions (each file, the phone book per device) are the
// shell's, not the agent's.
var obexProfiles = []struct {
	uuid, name string
	path       dbus.ObjectPath
}{
	{uuidOPP, "Object Push", "/org/kario/bluetooth/opp"},
	{uuidPBAP, "Phone Book Access", "/org/kario/bluetooth/pbap"},
}

func (r *bluezRadio) registerOBEX() error {
	r.dialing = map[dbus.ObjectPath]chan obexLink{}
	m := r.bus.Object(bluezName, "/org/bluez")
	for _, p := range obexProfiles {
		if err := r.bus.Export(&btProfile{r: r, uuid: p.uuid}, p.path, bluezName+".Profile1"); err != nil {
			return err
		}
		opts := map[string]dbus.Variant{"Name": dbus.MakeVariant(p.name), "RequireAuthentication": dbus.MakeVariant(true),
			"RequireAuthorization": dbus.MakeVariant(false), "AutoConnect": dbus.MakeVariant(false)}
		if err := busCall(m, bluezName+".ProfileManager1.RegisterProfile", p.path, p.uuid, opts).Err; err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
		}
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

func (p *btProfile) NewConnection(dev dbus.ObjectPath, fd dbus.UnixFD, _ map[string]dbus.Variant) *dbus.Error {
	n := int(fd)
	mtu := 0
	if t, err := unix.GetsockoptInt(n, unix.SOL_SOCKET, unix.SO_TYPE); err == nil && t == unix.SOCK_SEQPACKET {
		if mtu, err = unix.GetsockoptInt(n, unix.SOL_BLUETOOTH, btSndMTU); err != nil || mtu <= 0 {
			unix.Close(n)
			return dbus.MakeFailedError(fmt.Errorf("L2CAP link without an MTU: %v", err))
		}
	}
	_ = unix.SetNonblock(n, true)
	go p.r.connected(p.uuid, dev, obexLink{f: os.NewFile(uintptr(n), "bluetooth"), mtu: mtu})
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
		r.peerBox.serve(l.f, l.mtu)
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
			return &profileLink{File: l.f, r: r, dev: dev, uuid: uuid}, l.mtu, nil
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
	r    *bluezRadio
	dev  dbus.ObjectPath
	uuid string
	once sync.Once
}

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
	return r.connectProfile(dev, uuid)
}

// peerLink: the selftest's other adapter connects to the phone's service.
func (r *bluezRadio) peerLink(uuid string) (io.ReadWriteCloser, int, error) {
	o, err := r.load()
	if err != nil {
		return nil, 0, err
	}
	r.mu.Lock()
	peer := r.peer
	r.mu.Unlock()
	phone, _ := o.prop(r.adapter(o), bluezAdapter, "Address").(string)
	dev := o.device(peer, phone)
	if peer == "" || dev == "" || r.dialing == nil {
		return nil, 0, fmt.Errorf("the other adapter does not know the phone (%s)", phone)
	}
	return r.connectProfile(dev, uuid)
}
