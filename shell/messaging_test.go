package shell

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/location"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// Deleting messages and the block list are the default SMS app's: a store
// app holding the sms permission is refused before smsd is asked.
func TestOnlyTheDefaultSMSAppDeletesAndBlocks(t *testing.T) {
	other := &App{M: &Manifest{ID: "com.example.sms", Name: "SMS+", Permissions: []string{ui.PermSMS}, installed: true}, running: true}
	fake := &App{M: &Manifest{ID: appMessages, Name: "Messages", Permissions: []string{ui.PermSMS}, installed: true}, running: true}
	c := &Core{log: log.New(io.Discard, "", 0), cfg: Config{StateDir: t.TempDir(), RunDir: t.TempDir()}, apps: []*App{other, fake}}
	b := &broker{c: c}
	for _, a := range []*App{other, fake} {
		for _, m := range []ui.Msg{
			{T: ui.MsgReq, Cap: ui.CapSMSDelete, Args: json.RawMessage(`{"peer":"10086"}`)},
			{T: ui.MsgReq, Cap: ui.CapSMSBlock, Args: json.RawMessage(`{"number":"10086","on":true}`)},
			{T: ui.MsgReq, Cap: ui.CapSMSBlocked},
		} {
			var got error
			b.dispatchSMS(a, m, func(_ any, err error) { got = err })
			if got == nil || got.Error() != tr.T("Only the default SMS app can do this") {
				t.Errorf("%s %s: %v", a.M.ID, m.Cap, got)
			}
		}
	}
	if !c.defaultSMSApp(&App{M: &Manifest{ID: appMessages}}) {
		t.Fatal("the system's Messages is not the default SMS app")
	}
}

// The sms role moves to an app with the sms permission once the user
// agrees; the holder deletes and sends without a confirmation, the
// system's Messages no longer deletes; an app without the permission
// cannot have it, and an app that went away gives it back.
func TestDefaultSMSAppRole(t *testing.T) {
	msgs := &App{M: &Manifest{ID: appMessages, Name: "Messages", Permissions: []string{ui.PermSMS}}, running: true}
	other := &App{M: &Manifest{ID: "com.example.sms", Name: "SMS+", Permissions: []string{ui.PermSMS}, installed: true}, running: true}
	notes := &App{M: &Manifest{ID: "com.example.notes", Name: "Notes", installed: true}}
	c, b := contactsRig(t, msgs, other, notes)
	if !c.defaultSMSApp(msgs) || c.defaultSMSApp(other) {
		t.Fatal("Messages holds the role at first")
	}
	roles := c.rolesView()["sms"].(map[string]any)
	if roles["app"] != appMessages || len(roles["choices"].([]map[string]any)) != 2 {
		t.Fatalf("roles %v", roles)
	}
	var err error
	c.setSMSApp(notes.M.ID, func(e error) { err = e })
	if err == nil {
		t.Fatal("an app without the sms permission became the default")
	}
	c.setSMSApp(other.M.ID, func(e error) { err = e })
	confirm(t, c, "SMS+").onNo()
	if !ui.Cancelled(err) || c.defaultSMSApp(other) {
		t.Fatal("the role moved without the user")
	}
	c.setSMSApp(other.M.ID, func(e error) { err = e })
	confirm(t, c, "SMS+").onYes()
	if err != nil || !c.defaultSMSApp(other) || c.defaultSMSApp(msgs) || c.smsApp() != other {
		t.Fatalf("role: %v", err)
	}
	var got error
	b.dispatchSMS(msgs, ui.Msg{T: ui.MsgReq, Cap: ui.CapSMSDelete, Args: json.RawMessage(`{"peer":"10086"}`)}, func(_ any, e error) { got = e })
	if got == nil || got.Error() != tr.T("Only the default SMS app can do this") {
		t.Fatalf("Messages deletes without the role: %v", got)
	}
	b.dispatchSMS(other, req(ui.CapSMSSend, ui.SMSSendArgs{To: "10086", Text: "hi"}), func(any, error) {})
	if len(c.overlays) != 0 {
		t.Fatal("the default SMS app asked before each message")
	}
	if !c.blockManager(other) || c.blockManager(notes) {
		t.Fatal("the block list is the role's")
	}
	c.apps = []*App{msgs, notes}
	if c.smsApp() != msgs {
		t.Fatal("the role did not come back to Messages")
	}
	c.setSMSApp("", func(e error) { err = e })
	if err != nil || c.settings.SMSApp != "" {
		t.Fatalf("back to Messages: %v %q", err, c.settings.SMSApp)
	}

	// Uninstalled, the holder gives the role back for good: installed
	// again under the same ID, it has to be chosen (and agreed to) again.
	c.apps = []*App{msgs, other, notes}
	c.setSMSApp(other.M.ID, func(e error) { err = e })
	confirm(t, c, "SMS+").onYes()
	if !c.defaultSMSApp(other) {
		t.Fatal("no role")
	}
	c.apps = []*App{msgs, notes}
	c.smsAppRemoved(other.M.ID)
	c.apps = []*App{msgs, other, notes}
	if c.smsApp() != msgs || c.settings.SMSApp != "" {
		t.Fatalf("installed again, %s holds the role without asking", c.smsApp().M.ID)
	}
}

// A ringing call from a blocked number is declined before it rings or
// shows; the others ring as before.
func TestBlockedCallsDoNotRing(t *testing.T) {
	c, _ := contactsRig(t)
	c.sms.blocks = ui.BlockList{Numbers: []string{"+8613700000001", "1065*"}, Hidden: true}
	for _, number := range []string{"137 0000 0001", "10655551234", ""} {
		call := ui.TelCall{ID: "c-" + number, Number: number, Dir: "in", State: "incoming"}
		if !c.blockCall(call) {
			t.Fatalf("%q rang", number)
		}
		if !c.blockCall(ui.TelCall{ID: call.ID, Number: number, Dir: "in", State: "ended"}) || c.sms.blockedCalls[call.ID] {
			t.Fatalf("%q: the end of a declined call", number)
		}
	}
	if c.blockCall(ui.TelCall{ID: "ok", Number: "13800000001", Dir: "in", State: "incoming"}) {
		t.Fatal("a number not on the list was declined")
	}
	if c.blockCall(ui.TelCall{ID: "sos", Number: "110", Dir: "in", State: "incoming"}) && c.callBlocked("110") != "" {
		t.Fatal("an emergency number is never blocked")
	}
	_ = c.contactBook().Seed([]ui.Contact{{Name: "Li", Number: "13800000002"}})
	c.sms.blocks.Unknown = true
	if c.callBlocked("13900000009") != ui.BlockedUnknown || c.callBlocked("+86 138 0000 0002") != "" {
		t.Fatalf("unknown: %q, a contact: %q", c.callBlocked("13900000009"), c.callBlocked("+86 138 0000 0002"))
	}
}

// A flash message is a dialog that the lock screen does not close.
func TestFlashDialog(t *testing.T) {
	c, _ := contactsRig(t)
	c.smsFlash(ui.SMS{ID: "f1", Peer: "10086", Text: "Balance 3.20"})
	c.smsFlash(ui.SMS{ID: "f1", Peer: "10086", Text: "Balance 3.20"})
	if len(c.overlays) != 1 || c.FlashesShown() != 1 {
		t.Fatalf("%d dialogs, %d shown", len(c.overlays), c.FlashesShown())
	}
	c.closeForLock()
	if len(c.overlays) != 1 {
		t.Fatal("the lock closed a flash message")
	}
	if f := c.overlays[0].(*flashOverlay).frame(c); f.Lines[0] != "Balance 3.20" {
		t.Fatalf("frame %+v", f)
	}
}

// An alert for an area shows inside it, and when the phone does not know
// where it is; outside it, it is only recorded.
func TestAlertArea(t *testing.T) {
	c := &Core{log: log.New(io.Discard, "", 0)}
	al := ui.SMSAlert{ID: "a", Area: []ui.AlertArea{{Points: []ui.GeoPoint{{Lat: 39.9087, Lon: 116.3975}}, RadiusM: 2000}}}
	if w := c.alertWhere(al); w == alertOutside {
		t.Fatal("hidden without a position")
	}
	c.loc.last, c.loc.lastAt = location.Fix{Lat: 39.95, Lon: 116.40, AccuracyM: 20}, time.Now()
	if c.alertWhere(al) != alertOutside {
		t.Fatal("shown 4.6 km away")
	}
	c.loc.last.Lat = 39.91
	if c.alertWhere(al) == alertOutside {
		t.Fatal("hidden inside")
	}
	c.loc.last.Lat, c.loc.lastAt = 39.95, time.Now().Add(-time.Hour)
	if c.alertWhere(al) == alertOutside {
		t.Fatal("an hour-old position decided")
	}
	c.loc.lastAt, c.settings.LocationOff = time.Now(), true
	if c.alertWhere(al) == alertOutside {
		t.Fatal("decided with location off")
	}
	if c.alertWhere(ui.SMSAlert{}) != "" {
		t.Fatal("an alert without an area")
	}
}

func TestEmergencyAlertLevels(t *testing.T) {
	c := &Core{log: log.New(io.Discard, "", 0)}
	if !c.alertOn(ui.AlertPresidential) || !c.alertOn(ui.AlertExtreme) || c.alertOn(ui.AlertTest) {
		t.Fatal("defaults: the highest level and extreme on, tests off")
	}
	if c.setAlert("", 0) == nil {
		t.Fatal("the highest level has no switch")
	}
	for _, k := range []string{ui.SettingAlertExtreme, ui.SettingAlertSevere, ui.SettingAlertAmber} {
		if err := c.setAlert(k, 0); err != nil {
			t.Fatal(err)
		}
	}
	_ = c.setAlert(ui.SettingAlertTest, 1)
	c.settings.Alerts[ui.AlertPresidential] = false
	if !c.alertOn(ui.AlertPresidential) || c.alertOn(ui.AlertExtreme) || c.alertOn(ui.AlertAmber) || !c.alertOn(ui.AlertTest) {
		t.Fatalf("after switching: %v", c.settings.Alerts)
	}
	view := c.alertsView()
	if len(view) != 5 || view[0]["locked"] != true || view[0]["on"] != true || view[1]["on"] != false {
		t.Fatalf("Settings' view: %v", view)
	}
}

func TestCopyCodeFromNotification(t *testing.T) {
	msgs := &App{M: &Manifest{ID: appMessages, Name: "Messages"}}
	c := &Core{log: log.New(io.Discard, "", 0)}
	n := &notif{app: msgs, n: ui.Notification{ID: "sms:10690000"}, at: time.Now()}
	if !c.smsNotifAction(n, smsCopyCode+"482913") || c.clipText() != "482913" {
		t.Fatalf("clipboard: %q", c.clipText())
	}
	if c.smsNotifAction(&notif{app: &App{M: &Manifest{ID: "com.example.x"}}}, smsCopyCode+"1") || c.smsNotifAction(n, "reply") {
		t.Fatal("not a code to copy")
	}
}

// fakeSmsdProc is a phone-smsd process that answers every call with an
// empty result, and can die (its socket and connections go) like a
// killed one.
type fakeSmsdProc struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func startFakeSmsd(t *testing.T, sock string) *fakeSmsdProc {
	t.Helper()
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeSmsdProc{ln: ln}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.conns = append(p.conns, nc)
			p.mu.Unlock()
			go func() {
				conn := wire.New(nc)
				for {
					var r wire.RPC
					if conn.Recv(&r) != nil {
						return
					}
					_ = conn.Send(wire.RPC{ID: r.ID, OK: true, Result: json.RawMessage(`{}`)})
				}
			}()
		}
	}()
	return p
}

func (p *fakeSmsdProc) kill() {
	p.ln.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, nc := range p.conns {
		nc.Close()
	}
}

// An SMS app that asked for its conversations while phone-smsd was being
// restarted got an error and shows an empty list; once the shell is
// subscribed to the new phone-smsd, the running SMS apps hear "sms" and
// ask again.
func TestSMSAppsHearSmsdBack(t *testing.T) {
	msgs := &App{M: &Manifest{ID: appMessages, Name: "Messages", Permissions: []string{ui.PermSMS}}, out: make(chan outbound, 64), running: true}
	c, b := contactsRig(t, msgs)
	c.cfg.RunDir = t.TempDir()
	c.ev = make(chan func(), 256)
	b.clients = map[string]*wire.Client{}
	sock := filepath.Join(c.cfg.RunDir, svc.SmsdSock)
	smsd := startFakeSmsd(t, sock)
	t.Cleanup(func() {
		c.stopping.Store(true)
		smsd.kill()
	})
	runUntil := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for !cond() {
			select {
			case f := <-c.ev:
				f()
			case <-deadline:
				t.Fatalf("no %s", what)
			}
		}
	}
	heard := func() bool {
		for {
			select {
			case o := <-msgs.out:
				if o.m.T == ui.MsgEvent && o.m.Event == ui.EventSMS {
					return true
				}
			default:
				return false
			}
		}
	}
	c.startSMS()
	runUntil("first connection", func() bool { return c.sms.up })
	heard()

	smsd.kill()
	runUntil("lost connection", func() bool { return !c.sms.up })
	if heard() {
		t.Fatal("the app heard sms while phone-smsd was away")
	}
	smsd = startFakeSmsd(t, sock)
	runUntil("new connection", func() bool { return c.sms.up })
	runUntil("sms event after the restart", heard)
}
