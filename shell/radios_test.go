package shell

import (
	"io"
	"strings"
	"testing"

	"github.com/kario-os/kario/obex"
	"github.com/kario-os/kario/ui"
)

// A protected network asks for its passphrase once, a wrong one fails, and
// forgetting it asks again; a device with a screen is paired only when the
// user confirms its code, a headset without one.
func TestSimulatedRadios(t *testing.T) {
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
