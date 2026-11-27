package main

import (
	"strings"
	"testing"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ui"
)

// Over Bluetooth: the idle page leads there, the device page lists the
// paired ones (or says to pair), and a move under way shows no code but
// the old phone's question, then each kind's count and why not all came.
func TestSwitchBluetoothPages(t *testing.T) {
	defer func(saved ui.SwitchState) { swState.SwitchState = saved }(swState.SwitchState)
	i18n.SetLang("en")
	var p app
	swState.SwitchState = ui.SwitchState{State: ui.SwitchIdle}
	s := p.switchScreen()
	if node(s, "sw_bt").ID == "" || !strings.Contains(texts(s), "Nothing to install on the old phone") {
		t.Fatalf("idle: %+v", s.Body)
	}

	p.bt = ui.BTState{Backend: "sim", Enabled: true, Devices: []ui.BTDevice{{Name: "Kario headset", Address: "02:00:00:00:00:01"}}}
	if pick := p.btPickScreen(); node(pick, "swbt:02:00:00:00:00:01").ID != "" || !strings.Contains(texts(pick), "pair the old phone") || node(pick, "sw_bt_pair").ID == "" {
		t.Fatalf("nothing paired: %+v", pick.Body)
	}
	p.bt.Devices = append(p.bt.Devices, ui.BTDevice{Name: "Redmi Note 9", Address: "02:00:00:00:00:02", Paired: true})
	if n := node(p.btPickScreen(), "swbt:02:00:00:00:00:02"); n.Title != "Redmi Note 9" || len(n.Actions) != 1 || !n.Actions[0].Primary {
		t.Fatalf("paired: %+v", n)
	}

	swState.SwitchState = ui.SwitchState{State: ui.SwitchWaiting, Bluetooth: "Redmi Note 9", Progress: []ui.SwitchProgress{{Kind: ui.SwitchContacts}}}
	s = p.switchScreen()
	if !strings.Contains(texts(s), "Allow it on “Redmi Note 9”") || strings.Contains(texts(s), "Scan this code") || len(s.Actions) != 1 || s.Actions[0].ID != "sw_stop" {
		t.Fatalf("waiting: %q %+v", texts(s), s.Actions)
	}
	swState.SwitchState = ui.SwitchState{State: ui.SwitchReceiving, Bluetooth: "Redmi Note 9", Progress: []ui.SwitchProgress{
		{Kind: ui.SwitchContacts, Expected: 3, Stored: 2, Skipped: 1}, {Kind: ui.SwitchSMS, Note: ui.SwitchNoteRefused}}}
	s = p.switchScreen()
	if n := node(s, "sw_got:contacts"); n.Trailing != "3 of 3" || n.Subtitle != "1 already here" {
		t.Fatalf("contacts: %+v", n)
	}
	if n := node(s, "sw_got:sms"); n.Subtitle != "Not allowed on the old phone" {
		t.Fatalf("messages: %+v", n)
	}
	swState.SwitchState = ui.SwitchState{State: ui.SwitchIdle, Last: &ui.SwitchSummary{Ended: ui.SwitchEndedDone, Device: "iPhone 12",
		Added: []ui.SwitchProgress{{Kind: ui.SwitchContacts, Stored: 2}, {Kind: ui.SwitchSMS, Note: ui.SwitchNotePartial}}}}
	if n := node(p.switchScreen(), "sw_added:sms"); n.Trailing != "0" || n.Subtitle != "Only part of them came over Bluetooth" {
		t.Fatalf("summary: %+v", n)
	}
}
