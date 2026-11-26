package shell

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/switchrecv/oldphone"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// switchServices stand in for phone-smsd and telephonyd: they keep what
// they are asked to import and to delete.
type switchServices struct {
	mu                 sync.Mutex
	sms                []ui.SMS
	calls              []ui.TelLogEntry
	smsGone, callsGone []string
}

func (s *switchServices) serve(t *testing.T, dir string) {
	t.Helper()
	ids := func(n int, prefix string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = prefix + string(rune('a'+i))
		}
		return out
	}
	_, err := wire.ServeHub(filepath.Join(dir, svc.SmsdSock), func(method string, params json.RawMessage) (any, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch method {
		case svc.SMSImport:
			var a svc.SMSImportArgs
			_ = json.Unmarshal(params, &a)
			s.sms = append(s.sms, a.Messages...)
			return svc.SMSImportRes{IDs: ids(len(a.Messages), "i")}, nil
		case svc.SMSDeleteImported:
			var a ui.SMSDeleteArgs
			_ = json.Unmarshal(params, &a)
			s.smsGone = append(s.smsGone, a.IDs...)
			return map[string]int{"deleted": len(a.IDs)}, nil
		}
		return nil, errors.New("unknown method " + method)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = wire.ServeHub(filepath.Join(dir, svc.TelephonydSock), func(method string, params json.RawMessage) (any, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch method {
		case svc.TelLogImport:
			var a struct{ Entries []ui.TelLogEntry }
			_ = json.Unmarshal(params, &a)
			s.calls = append(s.calls, a.Entries...)
			return map[string][]string{"ids": ids(len(a.Entries), "c")}, nil
		case svc.TelLogDelete:
			var a struct{ IDs []string }
			_ = json.Unmarshal(params, &a)
			s.callsGone = append(s.callsGone, a.IDs...)
			return map[string]int{"deleted": len(a.IDs)}, nil
		}
		return nil, errors.New("unknown method " + method)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// got is what the services were asked, at this point.
func (s *switchServices) got() switchServices {
	s.mu.Lock()
	defer s.mu.Unlock()
	return switchServices{sms: slices.Clone(s.sms), calls: slices.Clone(s.calls), smsGone: slices.Clone(s.smsGone), callsGone: slices.Clone(s.callsGone)}
}

type switchTest struct {
	*hotspotTest
	svcs *switchServices
	wifi *simWifi
}

func newSwitchTest(t *testing.T) *switchTest {
	w := newSimWifi()
	st := &switchTest{hotspotTest: newHotspotTest(t, w), svcs: &switchServices{}, wifi: w}
	c := st.c
	c.cfg.RunDir = t.TempDir()
	c.files = openFiles(filepath.Join(c.cfg.StateDir, "files"))
	c.sms.up, c.tel = true, &telStatus{}
	c.broker.clients = map[string]*wire.Client{}
	c.sw.anyPort = true
	st.svcs.serve(t, c.cfg.RunDir)
	// Loads the input method's dictionary, which an import spells names
	// with: phone-shell maps the compiled one, a test builds it in memory,
	// seconds under -race, too long inside a step the test times.
	ime.Pinyin("\u6b27\u9633\u9752")
	return st
}

func (st *switchTest) call(cap string, args any) (ui.SwitchState, error) {
	st.t.Helper()
	st.req(st.app, cap, args)
	r := st.reply(st.app)
	var s ui.SwitchState
	if err := json.Unmarshal(r.Data, &s); r.OK && err != nil {
		st.t.Fatal(err)
	}
	if !r.OK {
		return s, errors.New(r.Err)
	}
	return s, nil
}

// oldPhone runs f as the old phone, off the loop, while the loop runs.
func (st *switchTest) oldPhone(code string, f func(*oldphone.Client) error) {
	st.t.Helper()
	p, err := oldphone.ParsePairing(code)
	if err != nil {
		st.t.Fatal(err)
	}
	cl, err := oldphone.New(p, nil)
	if err != nil {
		st.t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f(cl) }()
	var ferr error
	finished := false
	st.pump("the old phone", func() bool {
		select {
		case ferr = <-done:
			finished = true
		default:
		}
		return finished
	})
	if ferr != nil {
		st.t.Fatal(ferr)
	}
}

const switchVCard = "BEGIN:VCARD\r\nVERSION:3.0\r\nN:\u6b27\u9633;\u9752;;;\r\nFN:\u6b27\u9633\u9752\r\nTEL;TYPE=CELL:+86 136 5555 0101\r\nEND:VCARD\r\n"

// From Settings: the hotspot comes up for it (Wi-Fi is not connected),
// the old phone pairs by the code and sends one of each, the import lands
// in the stores, the hotspot goes off again, and undo takes it all out.
func TestSwitchImportAndUndo(t *testing.T) {
	st := newSwitchTest(t)
	c := st.c
	before := c.contactBook().Len()
	s, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: ui.SwitchKinds})
	if err != nil || s.State != ui.SwitchWaiting || len(s.Kinds) != 6 || !strings.HasPrefix(s.Code, "kario-switch://pair?") {
		t.Fatalf("start: %+v %v", s, err)
	}
	if !c.hotspot.on || s.Network != c.settings.HotspotSSID || s.Passphrase == "" || !strings.HasPrefix(s.Address, "127.0.0.1:") {
		t.Fatalf("hotspot %v, %+v", c.hotspot.on, s)
	}
	if _, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: ui.SwitchKinds}); err == nil {
		t.Fatal("a second start went through")
	}
	if _, err := st.call(ui.CapSwitchUndo, nil); err == nil {
		t.Fatal("undo while receiving went through")
	}
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.Local).Unix()
	photo := []byte("\xff\xd8\xff\xe0 a small jpeg")
	var done oldphone.DoneReply
	code := s.Code
	st.oldPhone(code, func(cl *oldphone.Client) error {
		if _, err := cl.SayHello(oldphone.Device{Brand: "Xiaomi", Model: "Redmi Note 12", OS: "android", OSVersion: "14"}, map[string]int{"contacts": 1}); err != nil {
			return err
		}
		for kind, items := range map[string][]any{
			"contacts": {map[string]string{"vcard": switchVCard}, map[string]string{"vcard": "BEGIN:VCARD\r\nVERSION:3.0\r\nEND:VCARD"}},
			"call_log": {map[string]any{"number": "+86 136-5555-0101", "type": "missed", "at": at, "duration": 0}, map[string]any{"number": "abc", "type": "in", "at": at}},
			"sms":      {map[string]any{"address": "13655550101", "body": "\u5468\u672b\u89c1", "type": "in", "at": at * 1000, "read": false}},
			"calendar": {map[string]string{"ics": "BEGIN:VEVENT\r\nUID:e1\r\nDTSTART;TZID=Asia/Shanghai:20261120T190000\r\nSUMMARY:x\r\nEND:VEVENT"}},
		} {
			if _, _, err := cl.Items(kind, items); err != nil {
				return err
			}
		}
		if err := cl.SendFile("IMG_1", "photo", "IMG_20261110_0001.jpg", "image/jpeg", at, photo); err != nil {
			return err
		}
		done, err = cl.Done()
		return err
	})
	if done.Contacts != 1 || done.CallLog != 1 || done.SMS != 1 || done.Calendar != 1 || done.Photos != 1 || len(done.Failed) != 2 {
		t.Fatalf("done %+v", done)
	}
	st.pump("the session's end", func() bool { return c.sw.sess == nil })
	st.pump("the hotspot off", func() bool { return !c.hotspot.on })
	s, _ = st.call(ui.CapSwitchState, nil)
	if s.State != ui.SwitchIdle || s.Code != "" || s.Last == nil || s.Last.Ended != ui.SwitchEndedDone || s.Last.Device != "Xiaomi Redmi Note 12 · android 14" || len(s.Last.Added) != 5 || s.Last.Failed != 2 {
		t.Fatalf("after: %+v %+v", s, s.Last)
	}
	if x, ok := c.contactBook().ByNumber("13655550101"); !ok || x.Name != "\u6b27\u9633\u9752" || c.contactBook().Len() != before+1 {
		t.Fatalf("contact: %+v %v", x, ok)
	}
	got := st.svcs.got()
	if m := got.sms; len(m) != 1 || m[0].At != at || m[0].Read || m[0].Dir != "in" || m[0].Peer != "13655550101" {
		t.Fatalf("sms: %+v", m)
	}
	if e := got.calls; len(e) != 1 || e[0].Number != "+8613655550101" || e[0].Dir != "in" || e[0].Result != "missed" || e[0].At != at {
		t.Fatalf("calls: %+v", e)
	}
	var pic, cal *fileItem
	for _, f := range c.files.Items {
		switch {
		case f.Name == "IMG_20261110_0001.jpg":
			pic = f
		case strings.HasSuffix(f.Name, ".ics"):
			cal = f
		}
	}
	if pic == nil || cal == nil || pic.Mime != "image/jpeg" || pic.At != at || pic.Size != int64(len(photo)) {
		t.Fatalf("files: %+v", c.files.Items)
	}
	if raw, _ := os.ReadFile(c.files.path(pic)); string(raw) != string(photo) {
		t.Fatal("the photo's bytes")
	}
	ics, _ := os.ReadFile(c.files.path(cal))
	if !strings.HasPrefix(string(ics), "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n") || !strings.Contains(string(ics), "\r\nUID:e1\r\n") || !strings.HasSuffix(string(ics), "END:VEVENT\r\nEND:VCALENDAR\r\n") {
		t.Fatalf("ics: %q", ics)
	}
	if _, err := os.Stat(c.switchStaging()); !os.IsNotExist(err) {
		t.Fatalf("staging left: %v", err)
	}
	raw, _ := os.ReadFile(c.switchPath())
	if strings.Contains(string(raw), "13655550101") || strings.Contains(string(raw), "\u6b27") || !strings.Contains(string(raw), `"contacts":["c`) {
		t.Fatalf("the record holds content or no IDs: %s", raw)
	}
	token := code[strings.Index(code, "&token=")+7:][:43]
	if l := st.log.String(); strings.Contains(l, "13655550101") || strings.Contains(l, "\u5468") || strings.Contains(l, token) || strings.Contains(l, "IMG_") {
		t.Fatalf("content or the code in the log:\n%s", l)
	}
	// A Shell started again still has the import to undo.
	c.sw.loaded, c.sw.last = false, nil
	s, err = st.call(ui.CapSwitchUndo, nil)
	if err != nil || s.Last == nil || !s.Last.Undone {
		t.Fatalf("undo: %+v %v", s, err)
	}
	if _, ok := c.contactBook().ByNumber("13655550101"); ok || c.contactBook().Len() != before {
		t.Fatal("the contact is still there")
	}
	got = st.svcs.got()
	if len(c.files.Items) != 0 || len(got.smsGone) != 1 || got.smsGone[0] != "ia" || len(got.callsGone) != 1 || got.callsGone[0] != "ca" {
		t.Fatalf("left: files %+v, sms %v, calls %v", c.files.Items, got.smsGone, got.callsGone)
	}
	if _, err := st.call(ui.CapSwitchUndo, nil); err == nil {
		t.Fatal("undone twice")
	}
}

// Stopping ends the session: the code is void, the listener and the
// hotspot it turned on are gone; with Wi-Fi connected no hotspot comes up.
func TestSwitchStopAndWifi(t *testing.T) {
	if !alone(t) {
		return
	}
	st := newSwitchTest(t)
	c := st.c
	s, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: []string{ui.SwitchContacts, "nonsense"}})
	if err != nil || len(s.Kinds) != 1 {
		t.Fatalf("start: %+v %v", s, err)
	}
	addr := s.Address
	s, _ = st.call(ui.CapSwitchStop, nil)
	if s.State != ui.SwitchIdle || s.Code != "" || s.Last != nil {
		t.Fatalf("stopped: %+v", s)
	}
	st.pump("the hotspot off", func() bool { return !c.hotspot.on })
	if conn, err := net.Dial("tcp", addr); err == nil {
		conn.Close()
		t.Fatal("still listening")
	}
	if err := st.wifi.connect("Kario-Office", ui.WifiArgs{Passphrase: "kario1234"}); err != nil {
		t.Fatal(err)
	}
	s, err = st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: []string{ui.SwitchSMS}})
	if err != nil || c.hotspot.on || s.Network != "" || s.Passphrase != "" || !strings.Contains(s.Code, "&token=") || strings.Contains(s.Code, "ssid=") {
		t.Fatalf("on Wi-Fi: %+v %v (hotspot %v)", s, err, c.hotspot.on)
	}
	c.sms.up = false
	st.call(ui.CapSwitchStop, nil)
	if s, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: []string{ui.SwitchSMS}}); err == nil {
		t.Fatalf("messages without phone-smsd: %+v", s)
	}
}

// onWifi connects the simulated Wi-Fi to the selftest's test network.
func (st *switchTest) onWifi() {
	st.t.Helper()
	st.wifi.addNetwork("Kario-Test", "kario-5678", "")
	if err := st.wifi.connect("Kario-Test", ui.WifiArgs{Passphrase: "kario-5678"}); err != nil {
		st.t.Fatal(err)
	}
}

// On Wi-Fi the receiver listens there, the code names the network instead
// of a hotspot and no hotspot comes up. Asked for the hotspot while still
// waiting, the session moves there: a new code with the hotspot's name and
// password, the Wi-Fi listener gone. Once the old phone is in, it stays.
func TestSwitchOverWifi(t *testing.T) {
	if !alone(t) {
		return
	}
	st := newSwitchTest(t)
	c := st.c
	st.onWifi()
	kinds := []string{ui.SwitchContacts}
	s, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: kinds})
	if err != nil || s.State != ui.SwitchWaiting || s.Wifi != "Kario-Test" || s.Network != "" || s.Passphrase != "" || c.hotspot.on {
		t.Fatalf("start: %+v %v (hotspot %v)", s, err, c.hotspot.on)
	}
	if p, err := oldphone.ParsePairing(s.Code); err != nil || p.Wifi != "Kario-Test" || p.SSID != "" || !strings.Contains(s.Code, "&wifi=Kario-Test&token=") {
		t.Fatalf("code %+v %v", p, err)
	}
	wifiAddr := s.Address
	s, err = st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: kinds, Hotspot: true})
	if err != nil || s.State != ui.SwitchWaiting || s.Network != c.settings.HotspotSSID || s.Wifi != "" || !c.hotspot.on ||
		!strings.Contains(s.Code, "&ssid=") || strings.Contains(s.Code, "wifi=") {
		t.Fatalf("over to the hotspot: %+v %v (hotspot %v)", s, err, c.hotspot.on)
	}
	if conn, err := net.Dial("tcp", wifiAddr); err == nil && s.Address != wifiAddr {
		conn.Close()
		t.Fatal("the Wi-Fi listener is left")
	}
	st.call(ui.CapSwitchStop, nil)
	st.pump("the hotspot off", func() bool { return !c.hotspot.on })

	st.onWifi()
	s, err = st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: kinds})
	if err != nil || s.Wifi != "Kario-Test" {
		t.Fatalf("again on Wi-Fi: %+v %v", s, err)
	}
	st.oldPhone(s.Code, func(cl *oldphone.Client) error {
		_, err := cl.SayHello(oldphone.Device{Brand: "Apple", Model: "iPhone 12", OS: "ios", OSVersion: "18"}, nil)
		return err
	})
	if _, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: kinds, Hotspot: true}); err == nil || c.hotspot.on || c.sw.wifi.ssid != "Kario-Test" {
		t.Fatalf("a session the old phone reached moved: %v (hotspot %v)", err, c.hotspot.on)
	}
	st.oldPhone(s.Code, func(cl *oldphone.Client) error {
		if _, err := cl.SayHello(oldphone.Device{Brand: "Apple", Model: "iPhone 12", OS: "ios", OSVersion: "18"}, nil); err != nil {
			return err
		}
		if _, _, err := cl.Items("contacts", []any{map[string]string{"vcard": switchVCard}}); err != nil {
			return err
		}
		_, err := cl.Done()
		return err
	})
	st.pump("the session's end", func() bool { return c.sw.sess == nil })
	if s, _ := st.call(ui.CapSwitchState, nil); s.Last == nil || s.Last.Ended != ui.SwitchEndedDone || s.Lost != "" || c.hotspot.on {
		t.Fatalf("after: %+v", s)
	}
}

// When the Wi-Fi a session listens on goes (another network, another
// address, disconnected) or the hotspot is turned off, the session ends
// with the listener, saying why, and Settings is told which network went.
// The old code kept such a session waiting until it timed out.
func TestSwitchNetworkGone(t *testing.T) {
	if !alone(t) {
		return
	}
	defer func(d time.Duration) { switchWatchEvery = d }(switchWatchEvery)
	switchWatchEvery = 20 * time.Millisecond
	was := wifiLink{ssid: "Kario-Test", addr: "192.168.50.23"}
	for _, tc := range []struct {
		name   string
		change func(*simWifi)
		now    wifiLink
	}{
		{"moved", func(w *simWifi) { _ = w.connect("Kario-Office", ui.WifiArgs{Passphrase: "kario1234"}) }, wifiLink{ssid: "Kario-Office", addr: was.addr}},
		{"address", func(w *simWifi) { w.mu.Lock(); w.addr = "192.168.50.99"; w.mu.Unlock() }, wifiLink{ssid: was.ssid, addr: "192.168.50.99"}},
		{"off", func(w *simWifi) { _ = w.setEnabled(false) }, wifiLink{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newSwitchTest(t)
			c := st.c
			st.onWifi()
			s, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: []string{ui.SwitchContacts}})
			if err != nil || s.Wifi != "Kario-Test" {
				t.Fatalf("start: %+v %v", s, err)
			}
			st.oldPhone(s.Code, func(cl *oldphone.Client) error {
				if _, err := cl.SayHello(oldphone.Device{Brand: "Xiaomi"}, nil); err != nil {
					return err
				}
				_, _, err := cl.Items("contacts", []any{map[string]string{"vcard": switchVCard}})
				return err
			})
			tc.change(st.wifi)
			st.pump("the session's end", func() bool { return c.sw.sess == nil })
			got, _ := st.call(ui.CapSwitchState, nil)
			want := switchWifiGone(was, tc.now)
			if got.State != ui.SwitchIdle || got.Lost != ui.SwitchLostWifi || got.Error != want || !strings.Contains(want, "Kario-Test") || got.Last == nil || got.Last.Ended != ui.SwitchEndedNetwork {
				t.Fatalf("after: %+v %+v", got, got.Last)
			}
			if conn, err := net.Dial("tcp", s.Address); err == nil {
				conn.Close()
				t.Fatal("still listening")
			}
			if s, _ := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: []string{ui.SwitchContacts}, Hotspot: true}); s.Lost != "" || s.Error != "" || !c.hotspot.on {
				t.Fatalf("again over the hotspot: %+v", s)
			}
			st.call(ui.CapSwitchStop, nil)
		})
	}
	st := newSwitchTest(t)
	c := st.c
	if _, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: []string{ui.SwitchContacts}}); err != nil || !c.hotspot.on {
		t.Fatalf("start: %v", err)
	}
	if _, err := st.set(ui.HotspotArgs{Enabled: on(false)}); err != nil {
		t.Fatal(err)
	}
	st.pump("the session's end", func() bool { return c.sw.sess == nil })
	if got, _ := st.call(ui.CapSwitchState, nil); got.Lost != ui.SwitchLostHotspot || got.Error != tr.T("Receiving stopped: the hotspot was turned off") {
		t.Fatalf("hotspot off: %+v", got)
	}
}

// One look at the Wi-Fi: down once may be a roam, down twice (or no
// answer) ends the session, as do another network or another address.
func TestSwitchLook(t *testing.T) {
	on := wifiLink{ssid: "Kario-Test", addr: "192.168.50.23"}
	for _, tc := range []struct {
		now      wifiLink
		err      error
		down     int
		gone     bool
		downThen int
	}{
		{on, nil, 0, false, 0},
		{on, nil, 1, false, 0},
		{wifiLink{}, nil, 0, false, 1},
		{wifiLink{ssid: "Kario-Test"}, nil, 0, false, 1},
		{wifiLink{}, nil, 1, true, 2},
		{wifiLink{}, errors.New("iwd: gone"), 1, true, 2},
		{wifiLink{ssid: "Kario-Office", addr: "10.0.0.2"}, nil, 0, true, 0},
		{wifiLink{ssid: "Kario-Test", addr: "192.168.50.99"}, nil, 0, true, 0},
	} {
		if gone, down := switchLook(on, tc.now, tc.err, tc.down); gone != tc.gone || down != tc.downThen {
			t.Errorf("%+v %v after %d down: gone %v, %d down; want %v, %d", tc.now, tc.err, tc.down, gone, down, tc.gone, tc.downThen)
		}
	}
}

// A Shell that ended while a session had the hotspot on leaves it to the
// next one to turn off; a hotspot the user turned on stays.
func TestSwitchAtBoot(t *testing.T) {
	st := newSwitchTest(t)
	c := st.c
	if _, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: []string{ui.SwitchContacts}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.switchHotspotMark()); err != nil || !c.settings.Hotspot {
		t.Fatalf("mark %v, hotspot %v", err, c.settings.Hotspot)
	}
	_ = os.MkdirAll(c.switchStaging(), 0o700)
	c.switchAtBoot()
	if c.settings.Hotspot {
		t.Fatal("the session's hotspot stays on after a restart")
	}
	if _, err := os.Stat(c.switchStaging()); !os.IsNotExist(err) {
		t.Fatal("staging left")
	}
	c.settings.Hotspot = true
	c.switchAtBoot()
	if !c.settings.Hotspot {
		t.Fatal("the user's hotspot went off")
	}
	st.call(ui.CapSwitchStop, nil)
}

// The receiver takes connections from its own network only.
func TestSwitchListener(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, other, _ := net.ParseCIDR("10.0.0.0/8")
	l := switchListener{Listener: ln, allow: other}
	defer l.Close()
	got := make(chan net.Conn, 1)
	go func() {
		conn, err := l.Accept()
		if err == nil {
			got <- conn
		}
	}()
	conn, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("a connection from outside the network was kept")
	}
	select {
	case <-got:
		t.Fatal("handed over")
	default:
	}
	if n := subnetOf(net.ParseIP("127.0.0.1")); n == nil || n.String() != "127.0.0.0/8" {
		t.Fatalf("loopback's network: %v", n)
	}
}

// Files from an old phone may be larger than what Bluetooth sends from
// memory: they are not offered there.
func TestSwitchBigFilesStayOffBluetooth(t *testing.T) {
	st := newSwitchTest(t)
	c := st.c
	small, _ := c.files.add("small.txt", []byte("x"), c.now())
	big, _ := c.files.add("big.mp4", []byte("y"), c.now())
	big.Size = maxFileBytes + 1
	c.pickToSend(st.app, &simBT{}, ui.BTDevice{Name: "car", Address: "00:00:00:00:00:01"}, func(any, error) {})
	m, ok := c.overlays[len(c.overlays)-1].(*menuOverlay)
	if !ok || len(m.entries) != 1 || !strings.Contains(m.entries[0].label, small.Name) {
		t.Fatalf("offered %+v", m)
	}
}
