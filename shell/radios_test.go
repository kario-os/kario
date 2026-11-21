package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/metered"
	"github.com/kario-os/kario/obex"
	"github.com/kario-os/kario/ui"
)

// A protected network asks for its passphrase once, a wrong one fails, and
// forgetting it asks again; a device with a screen is paired only when the
// user confirms its code, a headset without one.
func TestSimulatedRadios(t *testing.T) {
	t.Parallel()
	w := newSimWifi()
	if err := w.connect("Kario-Office", ui.WifiArgs{}); err == nil || err.Error() != ui.ErrNeedPassphrase {
		t.Fatalf("no passphrase: %v", err)
	}
	if err := w.connect("Kario-Office", ui.WifiArgs{Passphrase: "wrong-pass"}); err == nil {
		t.Fatal("a wrong passphrase connected")
	}
	if err := w.connect("Kario-Office", ui.WifiArgs{Passphrase: "kario1234"}); err != nil {
		t.Fatal(err)
	}
	st, _ := w.state()
	if st.State != "connected" || st.Address == "" || !st.Networks[0].Connected || !st.Networks[0].Known {
		t.Fatalf("after connecting: %+v", st)
	}
	_ = w.forget("Kario-Office")
	if err := w.connect("Kario-Office", ui.WifiArgs{}); err == nil || err.Error() != ui.ErrNeedPassphrase {
		t.Fatalf("forgotten network: %v", err)
	}

	var asked []uint32
	answer := false
	b := &simBT{enabled: true, confirm: func(_ string, code uint32) bool { asked = append(asked, code); return answer }}
	b.addPeer("Kario earbuds", false)
	b.addPeer("Car kit", true)
	_ = b.discover(true)
	st2, _ := b.state()
	if len(st2.Devices) != 2 {
		t.Fatalf("found %+v", st2.Devices)
	}
	if err := b.pair(st2.Devices[0].Address); err != nil || len(asked) != 0 {
		t.Fatalf("headset: %v, asked %v", err, asked)
	}
	if err := b.pair(st2.Devices[1].Address); err == nil || len(asked) != 1 {
		t.Fatalf("refused code: %v, asked %v", err, asked)
	}
	answer = true
	if err := b.pair(st2.Devices[1].Address); err != nil {
		t.Fatal(err)
	}
	_ = b.discover(false)
	if st3, _ := b.state(); len(st3.Devices) != 2 || !st3.Devices[1].Paired {
		t.Fatalf("paired devices: %+v", st3.Devices)
	}
}

func TestEnterpriseWifi(t *testing.T) {
	t.Parallel()
	w := newSimWifi()
	w.addEnterprise("Kario-Corp", "alice@corp.kario.test", "corp-pass", "radius.kario.test")
	ok := ui.WifiArgs{Identity: "alice@corp.kario.test", Passphrase: "corp-pass", Domain: "radius.kario.test"}
	for _, c := range []struct {
		cred ui.WifiArgs
		why  string
	}{
		{ui.WifiArgs{}, ui.ErrNeedCredentials},
		{ui.WifiArgs{Identity: ok.Identity, Passphrase: ok.Passphrase}, ui.ErrNeedCredentials},
		{ui.WifiArgs{Identity: ok.Identity, Passphrase: ok.Passphrase, Domain: "not a domain"}, tr.T("“%s” is not a domain name", "not a domain")},
		{ui.WifiArgs{Identity: ok.Identity, Passphrase: "x\nEAP-Method=MD5", Domain: ok.Domain}, tr.T("Line breaks are not allowed")},
		{ui.WifiArgs{Identity: ok.Identity, Passphrase: ok.Passphrase, Domain: ok.Domain, EAP: "md5"}, tr.T("Unknown EAP method %s", "md5")},
		{ui.WifiArgs{Identity: ok.Identity, Passphrase: ok.Passphrase, Domain: "evil.example"}, tr.T("Connection failed: the server's certificate is not for %s", "evil.example")},
		{ui.WifiArgs{Identity: ok.Identity, Passphrase: "wrong", Domain: ok.Domain}, tr.T("Connection failed: wrong user name or password")},
	} {
		if err := w.connect("Kario-Corp", c.cred); err == nil || err.Error() != c.why {
			t.Errorf("%+v: %v, want %q", c.cred, err, c.why)
		}
	}
	if err := w.connect("Kario-Corp", ok); err != nil {
		t.Fatal(err)
	}
	if err := w.connect("Kario-Corp", ui.WifiArgs{}); err != nil {
		t.Errorf("a known enterprise network asks again: %v", err)
	}
}

func TestEAPConfig(t *testing.T) {
	peap := eapConfig(ui.WifiArgs{Identity: "alice@corp.kario.test", Passphrase: "corp-pass", Domain: "radius.kario.test"}, systemCAs)
	for _, want := range []string{"EAP-Method=PEAP\n", "EAP-Identity=anonymous@corp.kario.test\n", "EAP-PEAP-CACert=" + systemCAs + "\n",
		"EAP-PEAP-ServerDomainMask=radius.kario.test\n", "EAP-PEAP-Phase2-Method=MSCHAPV2\n", "EAP-PEAP-Phase2-Identity=alice@corp.kario.test\n",
		"EAP-PEAP-Phase2-Password=corp-pass\n"} {
		if !strings.Contains(peap, want) {
			t.Errorf("PEAP file lacks %q:\n%s", want, peap)
		}
	}
	ttls := eapConfig(ui.WifiArgs{Identity: "bob", Passphrase: "p", Domain: "radius.kario.test", EAP: "ttls"}, systemCAs)
	for _, want := range []string{"EAP-Method=TTLS\n", "EAP-Identity=anonymous\n", "EAP-TTLS-Phase2-Method=Tunneled-MSCHAPv2\n"} {
		if !strings.Contains(ttls, want) {
			t.Errorf("TTLS file lacks %q:\n%s", want, ttls)
		}
	}
	pap := eapConfig(ui.WifiArgs{Identity: "bob", Passphrase: "p", Domain: "radius.kario.test", EAP: "ttls-pap"}, "/run/phone/eap-test/ca.pem")
	for _, want := range []string{"EAP-Method=TTLS\n", "EAP-TTLS-Phase2-Method=Tunneled-PAP\n", "EAP-TTLS-CACert=/run/phone/eap-test/ca.pem\n"} {
		if !strings.Contains(pap, want) {
			t.Errorf("TTLS/PAP file lacks %q:\n%s", want, pap)
		}
	}
}

// Airplane mode turns the radios off but those the user turned on in it
// before, and its end restores the user's switches.
func TestAirplaneRadios(t *testing.T) {
	w, b := newSimWifi(), &simBT{enabled: true}
	wasW, wasB := airplaneRadios(w, b, true, sysSettings{KeepBT: true})
	if ws, _ := w.state(); !wasW || !wasB || ws.Enabled || !b.enabled {
		t.Fatalf("airplane on: was %v %v, wifi %v, bt %v", wasW, wasB, ws.Enabled, b.enabled)
	}
	airplaneRadios(w, b, false, sysSettings{AirplaneWifi: true, AirplaneBT: false})
	if ws, _ := w.state(); !ws.Enabled || b.enabled {
		t.Fatalf("airplane off: wifi %v, bt %v", ws.Enabled, b.enabled)
	}
}

// The simulated devices: a paired one takes the files sent to it and can
// send one and read the phone book, which the phone answers.
func TestSimulatedOBEX(t *testing.T) {
	box := &obexState{}
	var served []string
	b := &simBT{enabled: true, peerBox: box, serve: func(uuid, addr, name string, link io.ReadWriteCloser, mtu int) {
		served = append(served, uuid+" "+name)
		link.Close()
	}}
	b.addPeer("Car kit", false)
	addr := b.devices[0].Address
	if _, _, err := b.obexDial(addr, uuidOPP); err == nil {
		t.Fatal("an unpaired device took a connection")
	}
	if _, _, err := b.peerLink(uuidOPP); err == nil {
		t.Fatal("an unpaired device connected")
	}
	if err := b.pair(addr); err != nil {
		t.Fatal(err)
	}
	if err := obexPut(b, addr, "note.txt", "text/plain", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	box.mu.Lock()
	got := string(box.got["note.txt"])
	box.mu.Unlock()
	if got != "hello" {
		t.Errorf("the device got %q", got)
	}
	link, _, err := b.peerLink(uuidPBAP)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = link.Read(make([]byte, 1))
	if len(served) != 1 || served[0] != uuidPBAP+" Car kit" {
		t.Errorf("served %v", served)
	}
	if _, _, err := b.peerLink(uuidPBAP); err == nil {
		t.Error("a second connection to the phone book while the first is open")
	}
	link.Close()
	if _, _, err := b.peerLink(uuidPBAP); err != nil {
		t.Errorf("after the first closed: %v", err)
	}
}

// The selftest's other device closes each session before it reports, so
// its next push may follow at once.
func TestPeerSessionsBackToBack(t *testing.T) {
	box := &obexState{}
	b := &simBT{enabled: true, peerBox: &obexState{}, serve: func(_, _, _ string, link io.ReadWriteCloser, mtu int) {
		defer link.Close()
		_ = (&obex.Server{Inbox: peerInbox{box}}).Serve(link, mtu)
	}}
	b.addPeer("Kario headset", false)
	if err := b.pair(b.devices[0].Address); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"notes.txt", "spam.txt"} {
		if _, err := peerSession(b, false, name, "hello"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	box.mu.Lock()
	defer box.mu.Unlock()
	if box.got["notes.txt"] == nil || box.got["spam.txt"] == nil {
		t.Errorf("the phone got %v", box.got)
	}
}

// A developer's machine running a bluetoothd that found no controller keeps
// the simulated Bluetooth; one with an adapter, and a phone, take BlueZ.
func TestBluezWithoutAnAdapter(t *testing.T) {
	n := func(k int) func() int { return func() int { return k } }
	for _, c := range []struct {
		owned    bool
		adapters int
		sim, use bool
	}{
		{true, 0, true, false},
		{true, 1, true, true},
		{true, 0, false, true},
		{false, 1, true, false},
	} {
		if got := useBluez(c.owned, n(c.adapters), c.sim); got != c.use {
			t.Errorf("BlueZ running %v, %d adapters, simulation allowed %v: use BlueZ %v, want %v", c.owned, c.adapters, c.sim, got, c.use)
		}
	}
}

// The selftest's iwd access point has a fixed address that no rule counts
// as a phone's hotspot (iwd's own pick from 192.168.0.0/16 landed on
// Android's 192.168.43.0/24 now and then, and the network looked metered),
// and gives its clients no router, so their traffic stays where it was.
func TestTestAPIsNotMetered(t *testing.T) {
	p := testAPProfile([]byte{1, 2, 3})
	ip, ipnet, err := net.ParseCIDR(testAPNet)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "\nAddress="+ip.String()+"\n") || !strings.Contains(p, "\nNetmask="+net.IP(ipnet.Mask).String()+"\n") {
		t.Errorf("the profile has no fixed address:\n%s", p)
	}
	if strings.Contains(p, "Gateway=") {
		t.Errorf("the profile gives its clients a router:\n%s", p)
	}
	for _, a := range []net.IP{ip, net.IPv4(ip[12], ip[13], ip[14], 100)} {
		if metered.HotspotAddress(a) {
			t.Errorf("%s counts as a phone hotspot's address", a)
		}
	}
	if hs := apAddress(nil); ipnet.Contains(hs.IP) {
		t.Errorf("the hotspot's first address %s is on the test access point's network", hs.IP)
	}
}

// slowBT takes its time coming on and refuses a search until it has.
type slowBT struct {
	mu    sync.Mutex
	on    bool
	calls []string
	ready chan struct{}
}

func (b *slowBT) state() (ui.BTState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return ui.BTState{Backend: "test", Enabled: b.on, Devices: []ui.BTDevice{}}, nil
}

func (b *slowBT) setEnabled(on bool) error {
	<-b.ready
	b.mu.Lock()
	defer b.mu.Unlock()
	b.on = on
	b.calls = append(b.calls, fmt.Sprint("power ", on))
	return nil
}

func (b *slowBT) discover(on bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.on {
		return errors.New("not ready")
	}
	b.calls = append(b.calls, fmt.Sprint("discover ", on))
	return nil
}

func (b *slowBT) pair(string) error   { return nil }
func (b *slowBT) forget(string) error { return nil }
func (b *slowBT) obexDial(string, string) (io.ReadWriteCloser, int, error) {
	return nil, 0, errors.New("no OBEX")
}

// Settings' Bluetooth on and the search pressed right after reach BlueZ in
// that order; the state still answers while they wait.
func TestRadioChangesInOrder(t *testing.T) {
	bt := &slowBT{ready: make(chan struct{})}
	ht := newHotspotTest(t, newSimWifi())
	ht.c.bt, ht.c.fg = bt, ht.app
	ht.req(ht.app, ui.CapBTSet, ui.BTArgs{Enabled: on(true)})
	ht.req(ht.app, ui.CapBTSet, ui.BTArgs{Discover: on(true)})
	ht.req(ht.app, ui.CapBTState, nil)
	var st ui.BTState
	if r := ht.reply(ht.app); !r.OK || json.Unmarshal(r.Data, &st) != nil || st.Backend != "test" || st.Enabled {
		t.Fatalf("the state before Bluetooth came on: %+v", r)
	}
	close(bt.ready)
	for range 2 {
		if r := ht.reply(ht.app); !r.OK {
			t.Fatalf("a change failed: %s", r.Err)
		}
	}
	bt.mu.Lock()
	defer bt.mu.Unlock()
	if !slices.Equal(bt.calls, []string{"power true", "discover true"}) {
		t.Fatalf("calls: %q", bt.calls)
	}
}

// searchBT only searches; stops counts the times it was told to stop.
type searchBT struct {
	mu          sync.Mutex
	discovering bool
	stops       int
}

func (b *searchBT) state() (ui.BTState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return ui.BTState{Backend: "test", Enabled: true, Discovering: b.discovering, Devices: []ui.BTDevice{}}, nil
}

func (b *searchBT) discover(on bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.discovering = on
	if !on {
		b.stops++
	}
	return nil
}

func (b *searchBT) searching() (bool, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.discovering, b.stops
}

func (b *searchBT) setEnabled(bool) error { return nil }
func (b *searchBT) pair(string) error     { return nil }
func (b *searchBT) forget(string) error   { return nil }
func (b *searchBT) obexDial(string, string) (io.ReadWriteCloser, int, error) {
	return nil, 0, errors.New("no OBEX")
}

// A search for devices is the app's in front: asked for from behind or
// with the screen off it is refused, and it stops when the app leaves
// the foreground, is frozen or ends, and when the screen goes off,
// without the app asking.
func TestBTSearchEndsWithTheApp(t *testing.T) {
	c := loopCore(t, t.TempDir())
	bt := &searchBT{}
	a := &App{M: &Manifest{ID: "com.phone.settings", Name: "Settings"}, running: true, ready: true}
	_ = c.onLoop(func() error {
		c.bt, c.cg, c.cfg.FreezeAfter = bt, nil, time.Millisecond
		c.apps = append(c.apps, a)
		return nil
	})
	search := func(on bool) error {
		got := make(chan error, 1)
		raw, _ := json.Marshal(ui.BTArgs{Discover: &on})
		c.Post(func() {
			c.broker.dispatchRadios(a, ui.Msg{T: ui.MsgReq, Cap: ui.CapBTSet, Args: raw}, func(_ any, err error) { got <- err })
		})
		return <-got
	}
	wait := func(what string, want bool) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if on, _ := bt.searching(); on == want {
				return
			}
			if time.Now().After(end) {
				t.Fatalf("%s: searching, want %v", what, want)
			}
		}
	}
	start := func() {
		t.Helper()
		_ = c.onLoop(func() error { c.setForeground(a); return nil })
		if err := search(true); err != nil {
			t.Fatalf("the app in front: %v", err)
		}
		wait("the app in front asked", true)
	}

	if err := search(true); err == nil {
		t.Fatal("a search asked for from behind was taken")
	}
	start()
	_ = c.onLoop(func() error { c.setForeground(nil); return nil })
	wait("the app left the foreground", false)

	start()
	_ = c.onLoop(func() error { c.screenOff("test"); return nil })
	wait("the screen went off", false)
	if err := search(true); err == nil {
		t.Fatal("a search asked for with the screen off was taken")
	}
	_ = c.onLoop(func() error { c.Wake("test"); return nil })

	start()
	_ = c.onLoop(func() error { a.killed = true; c.appExited(a, nil); return nil })
	wait("the app ended", false)

	sleep := exec.Command("sleep", "30")
	sleep.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-sleep.Process.Pid, syscall.SIGKILL); _ = sleep.Wait() })
	_ = c.onLoop(func() error { a.running, a.ready = true, true; return nil })
	start()
	// A search the app kept in the background (none the API lets start)
	// ends when the app is frozen.
	_ = c.onLoop(func() error {
		c.fg, a.proc, a.bgSince = nil, sleep.Process, time.Now().Add(-time.Minute)
		return nil
	})
	wait("the app was frozen", false)

	_ = c.onLoop(func() error { c.thaw(a); return nil })
	start()
	if err := search(false); err != nil {
		t.Fatal(err)
	}
	wait("the app stopped it", false)
	_, stops := bt.searching()
	_ = c.onLoop(func() error { c.setForeground(nil); return nil })
	time.Sleep(200 * time.Millisecond)
	if _, now := bt.searching(); now != stops {
		t.Errorf("a search the app stopped was stopped again when it left (%d stops, then %d)", stops, now)
	}
}
