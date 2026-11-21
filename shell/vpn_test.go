package shell

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/vpn"
)

// fakeVPN is a backend whose answers the test sets.
type fakeVPN struct {
	mu      sync.Mutex
	alive   bool
	upErr   error
	downErr error
	stuck   bool // down leaves the tunnel up
	ups     []string
	owns    []bool
	downs   int
	release chan struct{}
	link    string // the network under the tunnel ("": wlan0)
}

func (f *fakeVPN) up(c vpn.Config, own bool) error {
	f.mu.Lock()
	release := f.release
	f.mu.Unlock()
	if release != nil {
		<-release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ups = append(f.ups, c.Peers[0].Endpoint.String())
	f.owns = append(f.owns, own)
	if f.upErr != nil {
		return f.upErr
	}
	f.alive = true
	return nil
}

func (f *fakeVPN) down() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downs++
	if !f.stuck {
		f.alive = false
	}
	return f.downErr
}

func (f *fakeVPN) status() (bool, vpn.Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive, vpn.Stats{Rx: 10, Tx: 20, Handshake: time.Unix(1700000000, 0)}, nil
}

func (f *fakeVPN) via(vpn.Config) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return firstNonEmpty(f.link, "wlan0")
}

func (f *fakeVPN) set(do func(*fakeVPN)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	do(f)
}

func (f *fakeVPN) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ups)
}

func vpnCore(t *testing.T, be vpnBackend) *Core {
	dir := t.TempDir()
	c := &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: dir}, ev: make(chan func(), 64), files: openFiles(filepath.Join(dir, "files"))}
	_ = os.MkdirAll(filepath.Join(dir, "shell"), 0o700)
	c.vpn = vpnShell{backend: be, kind: "fake", path: filepath.Join(dir, "shell", "vpn.json"), errs: map[string]string{}}
	return c
}

// call runs a capability on the loop and waits for its answer.
func vpnCall(t *testing.T, c *Core, cap string, id string) (any, error) {
	t.Helper()
	type res struct {
		out any
		err error
	}
	done := make(chan res, 1)
	raw, _ := json.Marshal(ui.VPNArgs{ID: id})
	c.dispatchVPN(ui.Msg{Cap: cap, Args: raw}, func(out any, err error) { done <- res{out, err} })
	for end := time.After(5 * time.Second); ; {
		select {
		case r := <-done:
			return r.out, r.err
		case f := <-c.ev:
			f()
		case <-end:
			t.Fatalf("%s %s: no answer", cap, id)
		}
	}
}

func vpnFile(t *testing.T, c *Core, name, conf string) *fileItem {
	t.Helper()
	f, err := c.files.add(name, []byte(conf), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

const vpnPeerKey = "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg="

func vpnConf(withKey bool, endpoint string) string {
	key := ""
	if withKey {
		k, _ := vpn.GenerateKey()
		key = "PrivateKey = " + k.String() + "\n"
	}
	return "[Interface]\n" + key + "Address = 10.8.0.2/32\n[Peer]\nPublicKey = " + vpnPeerKey + "\nEndpoint = " + endpoint + "\nAllowedIPs = 0.0.0.0/0\n"
}

func TestVPNImport(t *testing.T) {
	c := vpnCore(t, &fakeVPN{})
	v := &c.vpn
	p, err := c.vpnImportFile(vpnFile(t, c, "office.conf", vpnConf(true, "198.51.100.1:51820")))
	if err != nil || p.Name != "office" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := c.vpnImportFile(vpnFile(t, c, "bad.conf", "[Interface]\nPostUp = rm -rf /\n")); err == nil || !strings.Contains(err.Error(), "PostUp") {
		t.Errorf("a script was not refused: %v", err)
	}
	// A file without a key needs the phone's own key pair, which it then
	// uses up.
	nokey := vpnFile(t, c, "home.conf", vpnConf(false, "198.51.100.2:51820"))
	if _, err := c.vpnImportFile(nokey); err == nil || err.Error() != ui.ErrVPNNeedsKey {
		t.Fatalf("no key pair waiting: %v", err)
	}
	out, err := vpnCall(t, c, ui.CapVPNKeygen, "")
	if err != nil {
		t.Fatal(err)
	}
	pub := out.(map[string]string)["public_key"]
	home, err := c.vpnImportFile(nokey)
	if err != nil || c.vpnView(home).PublicKey != pub || v.st.Pending != nil {
		t.Fatalf("home: %v, key %s, want %s, pending %v", err, c.vpnView(home).PublicKey, pub, v.st.Pending)
	}
	// The server's new file for the same profile keeps its key pair.
	again, err := c.vpnImportFile(vpnFile(t, c, "home.conf", vpnConf(false, "198.51.100.3:51820")))
	if err != nil || again != home || c.vpnView(again).PublicKey != pub || c.vpnView(again).Endpoint != "198.51.100.3:51820" {
		t.Fatalf("reimport: %v %+v", err, c.vpnView(again))
	}
	if len(v.st.Profiles) != 2 {
		t.Errorf("%d profiles", len(v.st.Profiles))
	}
	// The private keys are in the Shell's file (0600) and nowhere Settings
	// looks.
	fi, err := os.Stat(v.path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("store: %v %v", fi, err)
	}
	stored, _ := os.ReadFile(v.path)
	view, _ := json.Marshal(c.vpnState())
	for _, x := range v.st.Profiles {
		k := x.Conf.PrivateKey.String()
		if !strings.Contains(string(stored), k) || strings.Contains(string(view), k) {
			t.Errorf("private key in the store %v, in vpn.list %v", strings.Contains(string(stored), k), strings.Contains(string(view), k))
		}
	}
}

func TestVPNStates(t *testing.T) {
	be := &fakeVPN{}
	c := vpnCore(t, be)
	v := &c.vpn
	a, _ := c.vpnImportFile(vpnFile(t, c, "a.conf", vpnConf(true, "198.51.100.1:51820")))
	b, _ := c.vpnImportFile(vpnFile(t, c, "b.conf", vpnConf(true, "198.51.100.2:51820")))

	be.release = make(chan struct{})
	done := make(chan error, 1)
	c.vpnConnect(a.ID, func(_ any, err error) { done <- err })
	if v.state != ui.VPNConnecting || c.vpnUp() {
		t.Fatalf("connecting: %q", v.state)
	}
	if _, err := vpnCall(t, c, ui.CapVPNConnect, b.ID); err == nil {
		t.Error("a second connection while the first comes up")
	}
	close(be.release)
	be.set(func(f *fakeVPN) { f.release = nil })
	for v.state == ui.VPNConnecting {
		(<-c.ev)()
	}
	if err := <-done; err != nil || !c.vpnUp() || c.vpnView(a).State != ui.VPNConnected {
		t.Fatalf("connected: %v %q", err, v.state)
	}
	// One at a time: b's tunnel takes a's place.
	if _, err := vpnCall(t, c, ui.CapVPNConnect, b.ID); err != nil || v.st.Active != b.ID || c.vpnView(a).State != "" {
		t.Fatalf("b: %v, active %s", err, v.st.Active)
	}
	if got := strings.Join(be.ups, " "); got != "198.51.100.1:51820 198.51.100.2:51820" {
		t.Errorf("up %s", got)
	}
	out, _ := vpnCall(t, c, ui.CapVPNList, "")
	if st := out.(ui.VPNState); st.Profiles[1].RxBytes != 10 || st.Profiles[1].Handshake != 1700000000 {
		t.Errorf("list: %+v", st.Profiles[1])
	}
	if _, err := vpnCall(t, c, ui.CapVPNDisconnect, b.ID); err != nil || c.vpnUp() || v.st.Active != "" || be.alive {
		t.Fatalf("disconnect: %v, %q", err, v.state)
	}
	// A failed connection says why and leaves nothing up.
	be.set(func(f *fakeVPN) { f.upErr = errors.New("no WireGuard") })
	if _, err := vpnCall(t, c, ui.CapVPNConnect, a.ID); err == nil || c.vpnView(a).Error != "no WireGuard" || v.st.Active != "" {
		t.Fatalf("failed: %v %+v", err, c.vpnView(a))
	}
	be.set(func(f *fakeVPN) { f.upErr = nil })
	// A tunnel that does not come down stays shown as up.
	if _, err := vpnCall(t, c, ui.CapVPNConnect, a.ID); err != nil {
		t.Fatal(err)
	}
	be.set(func(f *fakeVPN) { f.stuck = true })
	if _, err := vpnCall(t, c, ui.CapVPNDisconnect, a.ID); err == nil || !c.vpnUp() {
		t.Fatalf("stuck tunnel: %v %q", err, v.state)
	}
	be.set(func(f *fakeVPN) { f.stuck = false })
	// One that goes down by itself is noticed.
	be.set(func(f *fakeVPN) { f.alive = false })
	c.vpnWatch(a.Conf)
	for end := time.After(5 * time.Second); c.vpnUp(); {
		select {
		case f := <-c.ev:
			f()
		case <-end:
			t.Fatal("the dead tunnel still shows as up")
		}
	}
	if c.vpnView(a).Error == "" {
		t.Error("no reason given")
	}
	// Deleting the profile up takes its tunnel down first.
	if _, err := vpnCall(t, c, ui.CapVPNConnect, b.ID); err != nil {
		t.Fatal(err)
	}
	downs := be.downs
	if _, err := vpnCall(t, c, ui.CapVPNDelete, b.ID); err != nil || be.downs != downs+1 || v.profile(b.ID) != nil || c.vpnUp() {
		t.Fatalf("delete: %v, downs %d", err, be.downs-downs)
	}
	if _, err := vpnCall(t, c, ui.CapVPNConnect, b.ID); err == nil {
		t.Error("connected a deleted profile")
	}
}

// The firewall counts the tunnel by the link under it, on the device only.
func TestVPNTunnels(t *testing.T) {
	c := vpnCore(t, &fakeVPN{})
	c.vpn.kind = "networkd"
	a, _ := c.vpnImportFile(vpnFile(t, c, "a.conf", vpnConf(true, "198.51.100.1:51820")))
	if len(c.vpnTunnels()) != 0 {
		t.Fatal("a tunnel before connecting")
	}
	if _, err := vpnCall(t, c, ui.CapVPNConnect, a.ID); err != nil {
		t.Fatal(err)
	}
	for end := time.After(5 * time.Second); c.vpn.via == ""; {
		select {
		case f := <-c.ev:
			f()
		case <-end:
			t.Fatal("the link under the tunnel was not looked up")
		}
	}
	if ts := c.vpnTunnels(); len(ts) != 1 || ts[0].Name != vpn.Iface || !ts[0].Wifi {
		t.Fatalf("%+v", ts)
	}
	if _, err := vpnCall(t, c, ui.CapVPNDisconnect, a.ID); err != nil || len(c.vpnTunnels()) != 0 {
		t.Fatalf("after: %v %+v", err, c.vpnTunnels())
	}
}

// A restarted Shell finds the tunnel its predecessor left up, or forgets it.
func TestVPNRestart(t *testing.T) {
	c := vpnCore(t, &fakeVPN{})
	a, _ := c.vpnImportFile(vpnFile(t, c, "a.conf", vpnConf(true, "198.51.100.1:51820")))
	if _, err := vpnCall(t, c, ui.CapVPNConnect, a.ID); err != nil {
		t.Fatal(err)
	}
	again := &Core{log: c.log, cfg: c.cfg, ev: make(chan func(), 8)}
	again.startVPN()
	if again.vpn.kind != "sim" {
		t.Skip("not a developer machine")
	}
	for again.vpn.st.Active != "" {
		(<-again.ev)()
	}
	if len(again.vpn.st.Profiles) != 1 || again.vpnUp() {
		t.Errorf("%+v", again.vpn.st)
	}
}

// vpnSetCall runs vpn.set on the loop and waits for its answer.
func vpnSetCall(t *testing.T, c *Core, args ui.VPNArgs) (ui.VPNState, error) {
	t.Helper()
	type res struct {
		out any
		err error
	}
	done := make(chan res, 1)
	raw, _ := json.Marshal(args)
	c.dispatchVPN(ui.Msg{Cap: ui.CapVPNSet, Args: raw}, func(out any, err error) { done <- res{out, err} })
	for end := time.After(5 * time.Second); ; {
		select {
		case r := <-done:
			st, _ := r.out.(ui.VPNState)
			return st, r.err
		case f := <-c.ev:
			f()
		case <-end:
			t.Fatalf("vpn.set %+v: no answer", args)
		}
	}
}

// vpnPump runs the loop until ok holds.
func vpnPump(t *testing.T, c *Core, what string, ok func() bool) {
	t.Helper()
	for end := time.After(5 * time.Second); !ok(); {
		select {
		case f := <-c.ev:
			f()
		case <-time.After(10 * time.Millisecond):
		case <-end:
			t.Fatalf("%s: not in 5 s", what)
		}
	}
}

func fastVPN(t *testing.T) {
	retry, watch := vpnRetryDelays, vpnWatchEvery
	vpnRetryDelays, vpnWatchEvery = []time.Duration{20 * time.Millisecond}, 20*time.Millisecond
	t.Cleanup(func() { vpnRetryDelays, vpnWatchEvery = retry, watch })
}

// An always-on profile connects at once, again after its tunnel went
// down, after failures (retried) and when the network under it changes;
// no other profile connects meanwhile; disconnecting it turns always-on
// off and it stays down.
func TestVPNAlwaysOn(t *testing.T) {
	fastVPN(t)
	be := &fakeVPN{}
	c := vpnCore(t, be)
	v := &c.vpn
	a, _ := c.vpnImportFile(vpnFile(t, c, "a.conf", vpnConf(true, "198.51.100.1:51820")))
	b, _ := c.vpnImportFile(vpnFile(t, c, "b.conf", vpnConf(true, "198.51.100.2:51820")))
	if _, err := vpnSetCall(t, c, ui.VPNArgs{ID: "nope", AlwaysOn: on(true)}); err == nil {
		t.Fatal("always-on for no profile")
	}
	st, err := vpnSetCall(t, c, ui.VPNArgs{ID: a.ID, AlwaysOn: on(true)})
	if err != nil || st.AlwaysOn != a.ID {
		t.Fatalf("always on: %+v %v", st, err)
	}
	vpnPump(t, c, "connected", func() bool { return c.vpnUp() && v.st.Active == a.ID })
	if _, err := vpnCall(t, c, ui.CapVPNConnect, b.ID); err == nil || v.st.Active != a.ID {
		t.Fatalf("another profile over the always-on one: %v", err)
	}
	be.set(func(f *fakeVPN) { f.alive = false })
	vpnPump(t, c, "up again after going down", func() bool { return be.count() == 2 && c.vpnUp() })
	be.set(func(f *fakeVPN) { f.alive, f.upErr = false, errors.New("no network") })
	vpnPump(t, c, "retried", func() bool { return be.count() >= 4 })
	be.set(func(f *fakeVPN) { f.upErr = nil })
	vpnPump(t, c, "up after the failures, on wlan0", func() bool { return c.vpnUp() && v.attempt == 0 && v.via == "wlan0" })
	n := be.count()
	be.set(func(f *fakeVPN) { f.link = "wwan0" })
	vpnPump(t, c, "up again on the new network", func() bool { return be.count() == n+1 && c.vpnUp() })
	if _, err := vpnCall(t, c, ui.CapVPNDisconnect, a.ID); err != nil || c.vpnUp() || v.st.AlwaysOn != "" {
		t.Fatalf("disconnect: %v, up %v, always on %q", err, c.vpnUp(), v.st.AlwaysOn)
	}
	n = be.count()
	time.Sleep(100 * time.Millisecond)
	for len(c.ev) > 0 {
		(<-c.ev)()
	}
	if be.count() != n || c.vpnUp() {
		t.Fatal("the disconnected profile came back")
	}
	raw, _ := os.ReadFile(v.path)
	if strings.Contains(string(raw), "always_on") {
		t.Fatalf("always-on still saved: %s", raw)
	}
}

// The kill switch needs always-on; it holds the always-on tunnel's
// servers (a name's once resolved) and the nearby links, blocks while the
// tunnel is down, and goes with always-on.
func TestVPNKillSwitch(t *testing.T) {
	fastVPN(t)
	be := &fakeVPN{}
	c := vpnCore(t, be)
	v := &c.vpn
	a, _ := c.vpnImportFile(vpnFile(t, c, "a.conf", vpnConf(true, "198.51.100.1:51820")))
	if _, err := vpnSetCall(t, c, ui.VPNArgs{KillSwitch: on(true)}); err == nil || v.st.KillSwitch {
		t.Fatal("a kill switch without always-on")
	}
	if _, err := vpnSetCall(t, c, ui.VPNArgs{ID: a.ID, AlwaysOn: on(true)}); err != nil {
		t.Fatal(err)
	}
	st, err := vpnSetCall(t, c, ui.VPNArgs{KillSwitch: on(true)})
	if err != nil || !st.KillSwitch || !c.vpnLockOn() {
		t.Fatalf("kill switch: %+v %v", st, err)
	}
	vpnPump(t, c, "connected", func() bool { return c.vpnUp() && !c.vpnBlocking() })
	l := c.vpnLock()
	if l.Tunnel != vpn.Iface || l.Mark != vpn.FwMark || l.Resolve || len(l.Servers) != 1 || l.Servers[0].String() != "198.51.100.1:51820" || len(l.Links) != 1 {
		t.Fatalf("lock: %+v", l)
	}
	c.hotspot.on, c.hotspot.iface = true, "wlan0"
	if l := c.vpnLock(); len(l.Links) != 2 || l.Links[1] != "wlan0" {
		t.Fatalf("the hotspot's link: %+v", l.Links)
	}
	c.hotspot.on = false
	be.set(func(f *fakeVPN) { f.alive, f.upErr = false, errors.New("no network") })
	vpnPump(t, c, "blocking while down", func() bool { return c.vpnBlocking() })
	if st := c.vpnState(); !st.Blocking {
		t.Fatalf("state while down: %+v", st)
	}
	be.set(func(f *fakeVPN) { f.upErr = nil })
	vpnPump(t, c, "up again", func() bool { return !c.vpnBlocking() })
	// A server known by name: its addresses once it was resolved.
	p := v.profile(a.ID)
	p.Conf.Peers[0].Endpoint = vpn.Endpoint{Host: "vpn.example.com", Port: 51820}
	if l := c.vpnLock(); !l.Resolve || len(l.Servers) != 0 {
		t.Fatalf("named server: %+v", l)
	}
	v.st.Servers = map[string][]netip.AddrPort{a.ID: {netip.MustParseAddrPort("203.0.113.9:51820")}}
	if l := c.vpnLock(); !l.Resolve || len(l.Servers) != 1 || l.Servers[0].String() != "203.0.113.9:51820" {
		t.Fatalf("named server, resolved: %+v", l)
	}
	if _, err := vpnSetCall(t, c, ui.VPNArgs{AlwaysOn: on(false)}); err != nil || c.vpnLockOn() || v.st.KillSwitch {
		t.Fatalf("always-on off: %v, lock %v", err, c.vpnLockOn())
	}
}

// Where an app's packets go: per-app modes, the tunnel's addresses, and
// the kill switch while it is down (the apps kept out pass).
func TestVPNRoute(t *testing.T) {
	fastVPN(t)
	be := &fakeVPN{}
	c := vpnCore(t, be)
	v := &c.vpn
	c.apps = []*App{{M: &Manifest{ID: "com.example.x", Name: "X", Permissions: []string{ui.PermNet}}},
		{M: &Manifest{ID: "com.example.y", Name: "Y", Permissions: []string{ui.PermNet}}},
		{M: &Manifest{ID: "com.example.calc", Name: "Calc"}}}
	full, _ := c.vpnImportFile(vpnFile(t, c, "full.conf", vpnConf(true, "198.51.100.1:51820")))
	dst := netip.MustParseAddr("192.0.2.1")
	route := func(app string) string { return c.vpnRoute(app, dst) }
	if route("com.example.x") != routeDirect {
		t.Fatal("into a tunnel that is not up")
	}
	if _, err := vpnCall(t, c, ui.CapVPNConnect, full.ID); err != nil {
		t.Fatal(err)
	}
	if route("com.example.x") != routeTunnel || route("com.example.y") != routeTunnel {
		t.Fatal("all apps: not in the tunnel")
	}
	if _, err := vpnSetCall(t, c, ui.VPNArgs{App: "com.example.calc", Chosen: on(true)}); err == nil {
		t.Fatal("chose an app without the network")
	}
	st, err := vpnSetCall(t, c, ui.VPNArgs{App: "com.example.x", Chosen: on(true)})
	if err != nil || len(st.AppList) != 2 || st.AppList[0].ID != "com.example.x" || !st.AppList[0].Chosen || st.AppList[1].Chosen {
		t.Fatalf("chosen: %+v %v", st.AppList, err)
	}
	if _, err := vpnSetCall(t, c, ui.VPNArgs{Apps: new(string)}); err != nil {
		t.Fatal(err)
	}
	bad := "some"
	if _, err := vpnSetCall(t, c, ui.VPNArgs{Apps: &bad}); err == nil {
		t.Fatal("an unknown mode")
	}
	except := ui.VPNAppsExcept
	if _, err := vpnSetCall(t, c, ui.VPNArgs{Apps: &except}); err != nil {
		t.Fatal(err)
	}
	if route("com.example.x") != routeDirect || route("com.example.y") != routeTunnel {
		t.Fatalf("except x: x %s, y %s", route("com.example.x"), route("com.example.y"))
	}
	only := ui.VPNAppsOnly
	if _, err := vpnSetCall(t, c, ui.VPNArgs{Apps: &only}); err != nil {
		t.Fatal(err)
	}
	if route("com.example.x") != routeTunnel || route("com.example.y") != routeDirect {
		t.Fatalf("only x: x %s, y %s", route("com.example.x"), route("com.example.y"))
	}
	if r := c.vpnRoute("com.example.x", netip.MustParseAddr("2001:db8::1")); r != routeDirect {
		t.Fatalf("an IPv6 address outside AllowedIPs: %s", r)
	}
	if _, err := vpnSetCall(t, c, ui.VPNArgs{ID: full.ID, AlwaysOn: on(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := vpnSetCall(t, c, ui.VPNArgs{KillSwitch: on(true)}); err != nil {
		t.Fatal(err)
	}
	be.set(func(f *fakeVPN) { f.alive, f.upErr = false, errors.New("no network") })
	vpnPump(t, c, "down", func() bool { return v.state != ui.VPNConnected })
	if route("com.example.x") != routeBlocked || route("com.example.y") != routeDirect {
		t.Fatalf("only x, blocked: x %s, y %s", route("com.example.x"), route("com.example.y"))
	}
	if _, err := vpnSetCall(t, c, ui.VPNArgs{Apps: new(string)}); err != nil {
		t.Fatal(err)
	}
	if route("com.example.y") != routeBlocked {
		t.Fatalf("all apps, blocked: y %s", route("com.example.y"))
	}
}

// Per-app routing puts a split tunnel's routes into a table of their own:
// a tunnel up without it is made again; a full tunnel stays.
func TestVPNOwnTable(t *testing.T) {
	fastVPN(t)
	be := &fakeVPN{}
	c := vpnCore(t, be)
	split, _ := c.vpnImportFile(vpnFile(t, c, "split.conf", strings.Replace(vpnConf(true, "198.51.100.1:51820"), "0.0.0.0/0", "10.8.0.0/24", 1)))
	full, _ := c.vpnImportFile(vpnFile(t, c, "full.conf", vpnConf(true, "198.51.100.2:51820")))
	if _, err := vpnCall(t, c, ui.CapVPNConnect, split.ID); err != nil {
		t.Fatal(err)
	}
	except := ui.VPNAppsExcept
	if _, err := vpnSetCall(t, c, ui.VPNArgs{Apps: &except}); err != nil {
		t.Fatal(err)
	}
	vpnPump(t, c, "the split tunnel again", func() bool { return be.count() == 2 && c.vpnUp() })
	if be.owns[0] || !be.owns[1] || !c.vpn.ownTable {
		t.Fatalf("own tables: %v", be.owns)
	}
	if _, err := vpnCall(t, c, ui.CapVPNConnect, full.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := vpnSetCall(t, c, ui.VPNArgs{Apps: new(string)}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	for len(c.ev) > 0 {
		(<-c.ev)()
	}
	if be.count() != 3 {
		t.Fatalf("the full tunnel was made again: %v", be.ups)
	}
}

// A server's name becomes the address the lookup finds, and every server
// is listed; a name that does not resolve is an error.
func TestVPNResolve(t *testing.T) {
	lookup := func(_ context.Context, host string) ([]netip.Addr, error) {
		if host == "vpn.example.com" {
			return []netip.Addr{netip.MustParseAddr("::ffff:203.0.113.9")}, nil
		}
		return nil, errors.New("no such host")
	}
	conf, err := vpn.Parse([]byte(vpnConf(true, "vpn.example.com:51820")))
	if err != nil {
		t.Fatal(err)
	}
	k, _ := vpn.GenerateKey()
	conf.Peers = append(conf.Peers, vpn.Peer{PublicKey: k.Public(), Endpoint: vpn.Endpoint{Host: "2001:db8::5", Port: 443}, AllowedIPs: []netip.Prefix{netip.MustParsePrefix("::/0")}})
	out, servers, err := vpnResolve(conf, lookup)
	if err != nil || out.Peers[0].Endpoint.String() != "203.0.113.9:51820" || conf.Peers[0].Endpoint.Host != "vpn.example.com" ||
		len(servers) != 2 || servers[0].String() != "203.0.113.9:51820" || servers[1].String() != "[2001:db8::5]:443" {
		t.Fatalf("%v %+v %v", err, out.Peers[0].Endpoint, servers)
	}
	conf.Peers[0].Endpoint.Host = "gone.example.com"
	if _, _, err := vpnResolve(conf, lookup); err == nil || !strings.Contains(err.Error(), "gone.example.com") {
		t.Fatalf("an unknown name: %v", err)
	}
}

// A restarted Shell connects the always-on profile by itself.
func TestVPNAlwaysOnRestart(t *testing.T) {
	c := vpnCore(t, &fakeVPN{})
	a, _ := c.vpnImportFile(vpnFile(t, c, "a.conf", vpnConf(true, "198.51.100.1:51820")))
	c.vpn.st.AlwaysOn = a.ID
	if err := c.vpn.save(); err != nil {
		t.Fatal(err)
	}
	again := &Core{log: c.log, cfg: c.cfg, ev: make(chan func(), 64)}
	again.startVPN()
	if again.vpn.kind != "sim" {
		t.Skip("not a developer machine")
	}
	vpnPump(t, again, "the always-on profile after the restart", func() bool { return again.vpnUp() && again.vpn.st.Active == a.ID })
}
