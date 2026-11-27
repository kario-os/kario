package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/kario-os/kario/netfw"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/vpn"
)

// The VPN (Settings › VPN): WireGuard profiles from a wg-quick file the
// user picks in Files (copied there from a computer over USB, say), or
// completed with a key pair the phone makes, whose public half Settings
// shows for the server's administrator. One profile is up at a time. On
// the device systemd-networkd runs the tunnel (vpn.NetworkdFiles under
// /run/systemd/network, the kernel's WireGuard); a developer machine (not
// root, or PHONE_SIM_VPN=1) gets a simulated tunnel; a phone without
// networkd has no VPN. The private keys stay in the Shell's state
// (vpn.json, 0600) and, while a tunnel is up, in the key files networkd
// reads (0640, group systemd-network); Settings sees public keys only, and
// the log names profiles and files, never keys. Always-on, the kill switch
// and per-app routing are vpn_policy.go's.

const networkdDir = "/run/systemd/network"

type vpnBackend interface {
	// up brings c's tunnel up in place of any other; ownTable: per-app
	// routing, which a split tunnel's routes need a table of their own for
	// (vpn.NetworkdFiles).
	up(c vpn.Config, ownTable bool) error
	down() error
	// status: whether the tunnel is up, and what went through it.
	status() (bool, vpn.Stats, error)
	// via is the interface the tunnel's own packets leave by ("" when it
	// cannot tell).
	via(c vpn.Config) string
	// leftover: a tunnel, or what makes one (networkd's files, the policy
	// rules), is there.
	leftover() bool
}

type vpnProfile struct {
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	Conf  vpn.Config `json:"conf"`
	Added int64      `json:"added"`
}

type vpnStore struct {
	Seq      int           `json:"seq"`
	Profiles []*vpnProfile `json:"profiles"`
	// Pending is the key pair vpn.keygen made, for the next file without
	// a PrivateKey.
	Pending *vpn.Key `json:"pending,omitempty"`
	// Active: the profile up, or coming up, when the store was written.
	Active string `json:"active,omitempty"`
	// AlwaysOn: the profile kept connected; KillSwitch: nothing leaves
	// outside its tunnel (with AlwaysOn only). Apps: which apps the tunnel
	// takes (ui.VPNApps*), Chosen the apps. Servers: the addresses each
	// profile's servers had when its tunnel last came up with the kill
	// switch on (a name's, resolved then), which the switch lets through.
	AlwaysOn   string                      `json:"always_on,omitempty"`
	KillSwitch bool                        `json:"kill_switch,omitempty"`
	Apps       string                      `json:"apps,omitempty"`
	Chosen     []string                    `json:"chosen,omitempty"`
	Servers    map[string][]netip.AddrPort `json:"servers,omitempty"`
}

type vpnShell struct {
	backend vpnBackend
	kind    string // networkd | sim | ""
	none    string // why there is no backend (kind "")
	path    string
	st      vpnStore
	// state is st.Active's: ui.VPNConnecting, ui.VPNConnected,
	// ui.VPNDisconnecting ("" with no profile active).
	state string
	since time.Time
	errs  map[string]string
	stats vpn.Stats
	// via: the link the tunnel rides, viaSeen once looked up; wifi: it is
	// Wi-Fi, so the firewall counts the tunnel as Wi-Fi.
	via     string
	viaSeen bool
	wifi    bool
	stop    chan struct{}
	// ownTable: the tunnel up has its routes in a table of its own.
	ownTable bool
	// retry: the always-on profile's next attempt, after attempt failures
	// in a row (vpn_policy.go).
	retry   *time.Timer
	attempt int
	// peer is the device selftest's server (vpn_headless.go).
	peer *vpn.TestPeer
}

func (v *vpnShell) profile(id string) *vpnProfile {
	for _, p := range v.st.Profiles {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (v *vpnShell) busy() bool { return v.state == ui.VPNConnecting || v.state == ui.VPNDisconnecting }

func (v *vpnShell) save() error {
	raw, err := json.Marshal(v.st)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(v.path), ".vpn-*")
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), v.path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

func (c *Core) startVPN() {
	v := &c.vpn
	v.path, v.errs = filepath.Join(c.cfg.StateDir, "shell", "vpn.json"), map[string]string{}
	if raw, err := os.ReadFile(v.path); err == nil {
		if err := json.Unmarshal(raw, &v.st); err != nil {
			c.log.Printf("vpn: %s: %v", v.path, err)
		}
	}
	switch {
	case os.Geteuid() != 0 || os.Getenv("PHONE_SIM_VPN") == "1":
		v.kind, v.backend = "sim", &simVPN{}
	default:
		if n, err := newNetworkdVPN(); err == nil {
			v.kind, v.backend = "networkd", n
		} else {
			v.none = err.Error()
		}
	}
	if v.kind == "" {
		c.log.Printf("vpn: none (%s), %d profiles", v.none, len(v.st.Profiles))
	} else {
		c.log.Printf("vpn: %s, %d profiles", v.kind, len(v.st.Profiles))
	}
	c.resumeVPN()
}

// resumeVPN goes on from the Shell before this one: the tunnel it left up
// for the profile active, taken down where no profile is.
func (c *Core) resumeVPN() {
	v := &c.vpn
	if v.profile(v.st.AlwaysOn) == nil {
		v.st.AlwaysOn, v.st.KillSwitch = "", false
	}
	// The kill switch holds from the start, before any tunnel is up.
	c.vpnFirewall()
	p := v.profile(v.st.Active)
	if p == nil || v.backend == nil {
		v.st.Active = ""
		c.vpnClearLeftover()
		c.vpnKeep()
		return
	}
	// A tunnel a previous Shell put up lives on: its files are in /run.
	be := v.backend
	go func() {
		up, _, _ := be.status()
		c.Post(func() {
			if v.st.Active != p.ID || v.state != "" {
				return
			}
			if !up {
				v.st.Active = ""
				_ = v.save()
				c.vpnKeep()
				return
			}
			v.state, v.since, v.ownTable = ui.VPNConnected, c.now(), v.st.Apps != ui.VPNAppsAll
			c.log.Printf("vpn: “%s” is still up", p.Name)
			c.vpnWatch(p.Conf)
			c.vpnFirewall()
		})
	}()
}

// vpnClearLeftover takes down a tunnel no profile is up for: a Shell put
// it up and went without taking it down (a selftest case that failed, the
// phone starting over from the state before it), and a full tunnel's
// policy rules would keep sending everything into it.
func (c *Core) vpnClearLeftover() {
	be := c.vpn.backend
	if be == nil || !be.leftover() {
		return
	}
	if err := be.down(); err != nil {
		c.log.Printf("vpn: the tunnel no profile is up for: %v", err)
		return
	}
	c.log.Printf("vpn: took down the tunnel no profile is up for (a Shell before this one left it)")
}

// vpnUp: a tunnel is up (the status bar's indicator).
func (c *Core) vpnUp() bool { return c.vpn.state == ui.VPNConnected }

// vpnTunnels is what the firewall counts the tunnel as (traffic.go).
func (c *Core) vpnTunnels() []netfw.Tunnel {
	if c.vpn.kind != "networkd" || c.vpn.state != ui.VPNConnected {
		return nil
	}
	return []netfw.Tunnel{{Name: vpn.Iface, Wifi: c.vpn.wifi}}
}

func (c *Core) vpnView(p *vpnProfile) ui.VPNProfile {
	v, conf := &c.vpn, p.Conf
	out := ui.VPNProfile{ID: p.ID, Name: p.Name, Error: v.errs[p.ID], Peers: len(conf.Peers), FullTunnel: conf.FullTunnel(),
		PublicKey: conf.PrivateKey.Public().String(), Address: []string{}, AllowedIPs: []string{}}
	for _, a := range conf.Addresses {
		out.Address = append(out.Address, a.String())
	}
	for _, a := range conf.DNS {
		out.DNS = append(out.DNS, a.String())
	}
	out.DNS = append(out.DNS, conf.Search...)
	for i, peer := range conf.Peers {
		if i == 0 {
			out.Endpoint = peer.Endpoint.String()
		}
		for _, a := range peer.AllowedIPs {
			out.AllowedIPs = append(out.AllowedIPs, a.String())
		}
	}
	if v.st.Active == p.ID {
		out.State = v.state
	}
	if out.State == ui.VPNConnected {
		out.Since, out.RxBytes, out.TxBytes = v.since.Unix(), v.stats.Rx, v.stats.Tx
		if !v.stats.Handshake.IsZero() {
			out.Handshake = v.stats.Handshake.Unix()
		}
	}
	return out
}

func (c *Core) vpnState() ui.VPNState {
	v := &c.vpn
	st := ui.VPNState{Backend: v.kind, Profiles: []ui.VPNProfile{}}
	for _, p := range v.st.Profiles {
		st.Profiles = append(st.Profiles, c.vpnView(p))
	}
	if v.st.Pending != nil {
		st.PendingKey = v.st.Pending.Public().String()
	}
	st.AlwaysOn, st.KillSwitch, st.Blocking, st.Apps = v.st.AlwaysOn, v.st.KillSwitch, c.vpnBlocking(), v.st.Apps
	st.AppList = c.vpnAppList()
	return st
}

func noVPN() error {
	return errors.New(tr.T("This phone cannot run a VPN: it has no systemd-networkd"))
}

// dispatchVPN serves vpn.*; the backend's work runs off the loop.
func (c *Core) dispatchVPN(m ui.Msg, reply func(any, error)) {
	var args ui.VPNArgs
	_ = json.Unmarshal(m.Args, &args)
	v := &c.vpn
	if v.backend == nil && m.Cap != ui.CapVPNList {
		reply(nil, noVPN())
		return
	}
	switch m.Cap {
	case ui.CapVPNList:
		c.vpnList(reply)
	case ui.CapVPNImport:
		c.vpnImport(reply)
	case ui.CapVPNKeygen:
		k, err := vpn.GenerateKey()
		if err == nil {
			v.st.Pending = &k
			err = v.save()
		}
		if err != nil {
			reply(nil, err)
			return
		}
		c.log.Printf("vpn: made a key pair")
		reply(map[string]string{"public_key": k.Public().String()}, nil)
	case ui.CapVPNConnect:
		if on := v.profile(v.st.AlwaysOn); on != nil && on.ID != args.ID {
			reply(nil, errors.New(tr.T("“%s” is always on: turn that off first", on.Name)))
			return
		}
		c.vpnConnect(args.ID, reply)
	case ui.CapVPNDisconnect:
		c.vpnAlwaysOff(args.ID)
		c.vpnDisconnect(args.ID, func(err error) { reply(c.vpnState(), err) })
	case ui.CapVPNSet:
		c.vpnSet(args, reply)
	case ui.CapVPNDelete:
		p := v.profile(args.ID)
		if p == nil {
			reply(nil, errors.New(tr.T("No such VPN profile")))
			return
		}
		c.vpnAlwaysOff(p.ID)
		c.vpnDisconnect(p.ID, func(err error) {
			if err != nil {
				reply(nil, err)
				return
			}
			for i, x := range v.st.Profiles {
				if x == p {
					v.st.Profiles = append(v.st.Profiles[:i], v.st.Profiles[i+1:]...)
				}
			}
			delete(v.errs, p.ID)
			delete(v.st.Servers, p.ID)
			c.log.Printf("vpn: deleted “%s”", p.Name)
			reply(c.vpnState(), v.save())
		})
	}
}

func (c *Core) vpnList(reply func(any, error)) {
	v := &c.vpn
	if v.state != ui.VPNConnected {
		reply(c.vpnState(), nil)
		return
	}
	be, id := v.backend, v.st.Active
	go func() {
		up, st, err := be.status()
		c.Post(func() {
			if err == nil && up && v.st.Active == id {
				v.stats = st
			}
			reply(c.vpnState(), nil)
		})
	}()
}

func (c *Core) vpnConnect(id string, reply func(any, error)) {
	v := &c.vpn
	switch p := v.profile(id); {
	case p == nil:
		reply(nil, errors.New(tr.T("No such VPN profile")))
		return
	case v.busy():
		reply(nil, errors.New(tr.T("The VPN is still connecting or disconnecting")))
		return
	case v.st.Active == id && v.state == ui.VPNConnected:
		reply(c.vpnState(), nil)
		return
	}
	c.vpnBringUp(id, func(err error) {
		if err != nil {
			reply(nil, err)
			return
		}
		reply(c.vpnState(), nil)
	})
}

// vpnBringUp brings id's tunnel up in place of any other (or again);
// done runs on the loop. With the kill switch on, the servers' names are
// resolved here and networkd gets addresses, which the switch then lets
// through.
func (c *Core) vpnBringUp(id string, done func(error)) {
	v := &c.vpn
	p := v.profile(id)
	c.vpnStopWatch()
	v.st.Active, v.state, v.stats = id, ui.VPNConnecting, vpn.Stats{}
	delete(v.errs, id)
	_ = v.save()
	be, conf, own := v.backend, p.Conf, v.st.Apps != ui.VPNAppsAll
	resolve := v.kind == "networkd" && c.vpnLockOn()
	go func() {
		var servers []netip.AddrPort
		var err error
		if resolve {
			conf, servers, err = vpnResolve(conf, lockedLookup)
		}
		if err == nil {
			err = be.up(conf, own)
		}
		c.Post(func() {
			if v.st.Active != id {
				return
			}
			if err != nil {
				v.st.Active, v.state = "", ""
				v.errs[id] = err.Error()
				_ = v.save()
				c.log.Printf("vpn: “%s” did not connect: %v", p.Name, err)
				c.vpnFirewall()
				done(err)
				return
			}
			if resolve {
				if v.st.Servers == nil {
					v.st.Servers = map[string][]netip.AddrPort{}
				}
				v.st.Servers[id] = servers
				_ = v.save()
			}
			v.state, v.since, v.ownTable, v.attempt = ui.VPNConnected, c.now(), own, 0
			kind := map[bool]string{true: "full tunnel", false: "split tunnel"}[conf.FullTunnel()]
			c.log.Printf("vpn: connected “%s” (%s, %s, endpoint %s, apps %s)", p.Name, v.kind, kind, conf.Peers[0].Endpoint, firstNonEmpty(v.st.Apps, "all"))
			c.vpnWatch(p.Conf)
			c.vpnFirewall()
			done(nil)
		})
	}()
}

// vpnDisconnect takes id's tunnel down, if it is the one up; done runs on
// the loop.
func (c *Core) vpnDisconnect(id string, done func(error)) {
	v := &c.vpn
	switch {
	case v.st.Active != id || v.state == "":
		done(nil)
		return
	case v.busy():
		done(errors.New(tr.T("The VPN is still connecting or disconnecting")))
		return
	}
	c.vpnStopWatch()
	v.state = ui.VPNDisconnecting
	be := v.backend
	go func() {
		err := be.down()
		up, _, _ := be.status()
		c.Post(func() {
			name := ""
			if p := v.profile(id); p != nil {
				name = p.Name
			}
			if up {
				v.state = ui.VPNConnected
				c.log.Printf("vpn: “%s” is still up: %v", name, err)
				if p := v.profile(id); p != nil {
					c.vpnWatch(p.Conf)
				}
				done(errors.Join(errors.New(tr.T("The tunnel is still up")), err))
				return
			}
			v.st.Active, v.state = "", ""
			_ = v.save()
			if err != nil {
				c.log.Printf("vpn: disconnecting “%s”: %v", name, err)
			}
			c.log.Printf("vpn: disconnected “%s”", name)
			c.vpnFirewall()
			done(nil)
		})
	}()
}

// vpnWatchEvery is how often vpnWatch looks.
var vpnWatchEvery = 5 * time.Second

// vpnWatch follows the tunnel while it is up: the link under it (the
// firewall counts the tunnel as that), and whether it is still there. An
// always-on tunnel comes up again when it went down or the link under it
// changed (another network, or one again after none: a server's name may
// now mean another address).
func (c *Core) vpnWatch(conf vpn.Config) {
	v := &c.vpn
	c.vpnStopWatch()
	stop := make(chan struct{})
	v.stop = stop
	be, id := v.backend, v.st.Active
	check := func() {
		up, st, err := be.status()
		via := be.via(conf)
		c.Post(func() {
			if v.stop != stop {
				return
			}
			if err == nil && !up {
				c.vpnStopWatch()
				v.st.Active, v.state = "", ""
				v.errs[id] = tr.T("The tunnel went down")
				_ = v.save()
				c.log.Printf("vpn: the tunnel went down")
				c.vpnFirewall()
				c.vpnKeep()
				return
			}
			if err == nil {
				v.stats = st
			}
			seen := v.viaSeen
			v.viaSeen = true
			if via == v.via {
				return
			}
			v.via, v.wifi = via, strings.HasPrefix(via, netfw.WifiPrefix)
			c.log.Printf("vpn: the tunnel rides %s", firstNonEmpty(via, "an unknown link"))
			c.vpnFirewall()
			if seen && via != "" && v.st.AlwaysOn == id && !v.busy() {
				c.log.Printf("vpn: the network changed; connecting again")
				c.vpnAgain(id)
			}
		})
	}
	every := vpnWatchEvery
	go func() {
		check()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				check()
			}
		}
	}()
}

func (c *Core) vpnStopWatch() {
	if c.vpn.stop != nil {
		close(c.vpn.stop)
		c.vpn.stop = nil
	}
	c.vpn.via, c.vpn.viaSeen, c.vpn.wifi = "", false, false
}

// vpnImport lists the configuration files in Files for the user to pick
// one; the file goes to the system only, Settings gets the profile.
func (c *Core) vpnImport(reply func(any, error)) {
	var es []menuEntry
	for i := len(c.files.Items) - 1; i >= 0; i-- {
		f := c.files.Items[i]
		if !matchesTypes(f, []string{"conf"}) {
			continue
		}
		es = append(es, menuEntry{group: tr.T("Files"), label: f.Name + " · " + sizeLabel(f.Size), find: f.Name, run: func() {
			p, err := c.vpnImportFile(f)
			if err != nil {
				reply(nil, err)
				return
			}
			reply(c.vpnView(p), nil)
		}})
	}
	if len(es) == 0 {
		reply(nil, errors.New(tr.T("No WireGuard configuration (.conf) in Files: copy one from a computer over USB first")))
		return
	}
	c.pushOverlay(&menuOverlay{title: tr.T("Import a VPN configuration"), entries: es,
		onCancel: func() { reply(nil, errors.New(ui.ErrCancelled)) }})
}

// vpnImportFile makes a profile of a file in Files, named after it; one of
// the same name is replaced (the server's administrator sent a new file),
// keeping its key pair when the new file has none.
func (c *Core) vpnImportFile(f *fileItem) (*vpnProfile, error) {
	v := &c.vpn
	if f.Size > vpn.MaxFile {
		return nil, errors.New(tr.T("“%s” is not a WireGuard configuration: it is larger than %d KB", f.Name, vpn.MaxFile>>10))
	}
	raw, err := os.ReadFile(c.files.path(f))
	if err != nil {
		return nil, err
	}
	conf, err := vpn.Parse(raw)
	if err != nil {
		return nil, errors.New(tr.T("Cannot import “%s”: %v", f.Name, err))
	}
	name := strings.TrimSpace(strings.TrimSuffix(f.Name, filepath.Ext(f.Name)))
	if r := []rune(name); len(r) > 32 {
		name = string(r[:32])
	}
	name = firstNonEmpty(name, "VPN")
	var p *vpnProfile
	for _, x := range v.st.Profiles {
		if x.Name == name {
			p = x
		}
	}
	if p != nil && v.st.Active == p.ID && v.state != "" {
		return nil, errors.New(tr.T("Disconnect “%s” first: it is in use", name))
	}
	pending := false
	switch {
	case !conf.PrivateKey.IsZero():
	case p != nil:
		conf.PrivateKey = p.Conf.PrivateKey
	case v.st.Pending != nil:
		conf.PrivateKey, pending = *v.st.Pending, true
	default:
		return nil, errors.New(ui.ErrVPNNeedsKey)
	}
	self := conf.PrivateKey.Public()
	for _, peer := range conf.Peers {
		if peer.PublicKey == self {
			return nil, errors.New(tr.T("Cannot import “%s”: the peer's PublicKey is this phone's own key", f.Name))
		}
	}
	if p == nil {
		v.st.Seq++
		p = &vpnProfile{ID: "v" + strconv.Itoa(v.st.Seq), Name: name}
		v.st.Profiles = append(v.st.Profiles, p)
	}
	p.Conf, p.Added = conf, c.now().Unix()
	if pending {
		v.st.Pending = nil
	}
	delete(v.errs, p.ID)
	if err := v.save(); err != nil {
		return nil, err
	}
	c.log.Printf("vpn: imported “%s” from file %s (%d peers, full tunnel %v, own key pair %v)", name, f.ID, len(conf.Peers), conf.FullTunnel(), pending)
	return p, nil
}

// The tunnel through systemd-networkd and the kernel's WireGuard.
type networkdVPN struct {
	bus *dbus.Conn
	dir string
	// wait: how long networkd may take to set the tunnel up.
	wait time.Duration
}

const networkdBus = "org.freedesktop.network1"

func newNetworkdVPN() (*networkdVPN, error) {
	bus, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("no system bus: %w", err)
	}
	// networkd may not be up yet when the Shell starts; a call starts it.
	var running bool
	var names []string
	_ = bus.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, networkdBus).Store(&running)
	_ = bus.BusObject().Call("org.freedesktop.DBus.ListActivatableNames", 0).Store(&names)
	if !running && !slices.Contains(names, networkdBus) {
		bus.Close()
		return nil, fmt.Errorf("no systemd-networkd on the system bus (%s neither owned nor activatable)", networkdBus)
	}
	return &networkdVPN{bus: bus, dir: networkdDir, wait: 45 * time.Second}, nil
}

func (n *networkdVPN) manager() dbus.BusObject {
	return n.bus.Object(networkdBus, "/org/freedesktop/network1")
}

func (n *networkdVPN) reload() error {
	return busCall(n.manager(), networkdBus+".Manager.Reload").Err
}

// linkState is networkd's AdministrativeState of the link ("" while it is
// not there): configuring, configured, failed…
func (n *networkdVPN) linkState(name string) string {
	var index int32
	var path dbus.ObjectPath
	if busCall(n.manager(), networkdBus+".Manager.GetLinkByName", name).Store(&index, &path) != nil {
		return ""
	}
	prop, err := n.bus.Object(networkdBus, path).GetProperty(networkdBus + ".Link.AdministrativeState")
	if err != nil {
		return ""
	}
	s, _ := prop.Value().(string)
	return s
}

// files are the tunnel's files in networkd's directory.
func (n *networkdVPN) files() []string {
	var out []string
	for _, pattern := range []string{vpn.Iface + ".key", vpn.Iface + "-*.psk", "50-" + vpn.Iface + ".netdev", "50-" + vpn.Iface + ".network"} {
		names, _ := filepath.Glob(filepath.Join(n.dir, pattern))
		out = append(out, names...)
	}
	return out
}

func (n *networkdVPN) removeFiles() {
	for _, name := range n.files() {
		os.Remove(name)
	}
}

func (n *networkdVPN) leftover() bool {
	if vpn.LinkExists(vpn.Iface, nil) || len(n.files()) > 0 {
		return true
	}
	rules, _ := vpn.Rules()
	for _, r := range rules {
		if r.Priority == vpn.PrioritySuppress || r.Priority == vpn.PriorityTunnel {
			return true
		}
	}
	return false
}

func (n *networkdVPN) up(c vpn.Config, ownTable bool) error {
	// networkd does not change a netdev that exists: the one up goes first.
	_ = n.down()
	if err := os.MkdirAll(n.dir, 0o755); err != nil {
		return err
	}
	gid := -1
	if g, err := user.LookupGroup("systemd-network"); err == nil {
		gid, _ = strconv.Atoi(g.Gid)
	}
	for _, f := range vpn.NetworkdFiles(c, n.dir, ownTable) {
		if err := writeNetworkd(filepath.Join(n.dir, f.Name), f, gid); err != nil {
			n.removeFiles()
			return err
		}
	}
	if err := n.reload(); err != nil {
		n.removeFiles()
		return err
	}
	for end := time.Now().Add(n.wait); ; time.Sleep(200 * time.Millisecond) {
		switch n.linkState(vpn.Iface) {
		case "configured":
			return nil
		case "failed":
			_ = n.down()
			return errors.New(tr.T("systemd-networkd could not set the tunnel up"))
		}
		if time.Now().After(end) {
			why := tr.T("The tunnel did not come up in time")
			if !vpn.HasWireGuard() {
				why = tr.T("The tunnel did not come up: the kernel has no WireGuard")
			}
			_ = n.down()
			return errors.New(why)
		}
	}
}

// writeNetworkd writes one of networkd's files; a key is readable by the
// systemd-network group alone from the moment it exists.
func writeNetworkd(path string, f vpn.File, gid int) error {
	os.Remove(path)
	mode := os.FileMode(0o644)
	if f.Secret {
		mode = 0o640
	}
	h, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if f.Secret && gid >= 0 {
		err = h.Chown(0, gid)
	}
	if err == nil {
		_, err = h.WriteString(f.Data)
	}
	if cerr := h.Close(); err == nil {
		err = cerr
	}
	return err
}

func (n *networkdVPN) down() error {
	n.removeFiles()
	err := n.reload()
	err = errors.Join(err, vpn.DeleteLink(vpn.Iface, nil))
	_, rerr := vpn.DeleteRules(vpn.PrioritySuppress, vpn.PriorityTunnel)
	return errors.Join(err, rerr)
}

func (n *networkdVPN) status() (bool, vpn.Stats, error) {
	if !vpn.LinkExists(vpn.Iface, nil) {
		return false, vpn.Stats{}, nil
	}
	st, _ := vpn.DeviceStats(vpn.Iface, nil)
	for name, count := range map[string]*uint64{"rx_bytes": &st.Rx, "tx_bytes": &st.Tx} {
		if raw, err := os.ReadFile(filepath.Join("/sys/class/net", vpn.Iface, "statistics", name)); err == nil {
			*count, _ = strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		}
	}
	return true, st, nil
}

func (n *networkdVPN) via(c vpn.Config) string {
	ep := c.Peers[0].Endpoint
	addr, ok := ep.Addr()
	if !ok {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		as, err := net.DefaultResolver.LookupNetIP(ctx, "ip", ep.Host)
		if err != nil || len(as) == 0 {
			return ""
		}
		addr = as[0].Unmap()
	}
	mark := uint32(0)
	if c.FullTunnel() {
		mark = vpn.FwMark
	}
	dev, err := vpn.RouteDev(addr, mark)
	if err != nil {
		return ""
	}
	return dev
}

// simVPN is a developer machine's tunnel: up at once, a handshake a second
// later, and some traffic. The selftest moves it to another network
// (link) or takes it away (vpn_headless.go); ups counts its connections.
type simVPN struct {
	mu    sync.Mutex
	on    bool
	since time.Time
	link  string
	ups   int
}

func (s *simVPN) up(vpn.Config, bool) error {
	time.Sleep(300 * time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.on, s.since = true, time.Now()
	s.ups++
	return nil
}

func (s *simVPN) down() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.on = false
	return nil
}

func (s *simVPN) status() (bool, vpn.Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.on {
		return false, vpn.Stats{}, nil
	}
	el := time.Since(s.since)
	st := vpn.Stats{Peers: 1, Rx: 148 + uint64(el.Milliseconds())*3, Tx: 148 + uint64(el.Milliseconds())}
	if el >= time.Second {
		st.Handshake = s.since.Add(time.Second)
	}
	return true, st, nil
}

func (s *simVPN) via(vpn.Config) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.link
}

func (s *simVPN) leftover() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on
}
