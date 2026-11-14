package shell

import (
	"encoding/json"
	"io"
	"log"
	"testing"
	"time"

	"github.com/kario-os/kario/ui"
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
	if !defaultSMSApp(&App{M: &Manifest{ID: appMessages}}) {
		t.Fatal("the system's Messages is not the default SMS app")
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
