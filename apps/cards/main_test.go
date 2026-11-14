package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ui"
)

// testdata/hours.json: the line each language shows for these hours.
func TestUsualHoursLine(t *testing.T) {
	door := ui.NFCCardInfo{Kind: ui.NFCDoor, UID: "C3", Locked: true, Usual: []ui.NFCHours{
		{From: 8 * 60, To: 10 * 60}, {From: 12 * 60, To: 14 * 60}, {Weekend: true, From: 10*60 + 30, To: 11*60 + 30}}}
	raw, err := os.ReadFile("testdata/hours.json")
	if err != nil {
		t.Fatal(err)
	}
	var lines map[string]string
	if err := json.Unmarshal(raw, &lines); err != nil || len(lines) != 4 {
		t.Fatalf("testdata/hours.json: %v", err)
	}
	for lang, want := range lines {
		i18n.SetLang(lang)
		if got := cardLine(door); got != want {
			t.Errorf("%s: %q, want %q", lang, got, want)
		}
	}
	i18n.SetLang("en")
	door.Usual = nil
	if got := cardLine(door); got != "Door card · UID …C3" {
		t.Errorf("a door card never tapped: %q", got)
	}
	door.Locked = false
	if got := cardLine(door); got != "Door card · unlocked only · UID …C3" {
		t.Errorf("a door card kept for the phone unlocked: %q", got)
	}
	if got := cardLine(ui.NFCCardInfo{Kind: ui.NFCPayment, AppName: "Feiqian", LockedFixed: true}); got != "Payment card · Feiqian" {
		t.Errorf("a payment card: %q", got)
	}
	if got := cardLine(ui.NFCCardInfo{Kind: ui.NFCOther, AppName: "Feiqian", Locked: true}); got != "Feiqian · works with the screen off" {
		t.Errorf("the express transit card: %q", got)
	}
}

func TestHomeMarksAndActions(t *testing.T) {
	i18n.SetLang("en")
	p := &cards{loaded: true, st: ui.NFCState{Enabled: true, Express: "w/transit", DefaultPayment: "w", Cards: []ui.NFCCardInfo{
		{ID: "door:1", Name: "Home", Kind: ui.NFCDoor, UID: "C3"},
		{ID: "w/transit", Name: "Transit", Kind: ui.NFCOther, App: "w", AppName: "W", CanExpress: true},
		{ID: "w/pay", Name: "Pay", Kind: ui.NFCPayment, App: "w", AppName: "W", LockedFixed: true},
	}, PaymentApps: []ui.NFCApp{{ID: "w", Name: "W"}}}}
	s := p.home()
	acts := map[string][]string{}
	marks := map[string]string{}
	for _, n := range s.Body {
		for _, a := range n.Actions {
			acts[n.ID] = append(acts[n.ID], a.ID)
		}
		marks[n.ID] = n.Trailing
	}
	if marks["card:w/transit"] != "Express" || marks["card:w/pay"] != "Default" || marks["pay"] != "W" || marks["express"] != "Transit" {
		t.Fatalf("marks: %v", marks)
	}
	has := func(node, act string) bool {
		for _, a := range acts[node] {
			if a == act {
				return true
			}
		}
		return false
	}
	if !has("card:door:1", "rename") || !has("card:door:1", "delete") || has("card:w/pay", "delete") || !has("card:w/transit", "express") ||
		!has("card:door:1", "locked") || has("card:w/pay", "locked") {
		t.Fatalf("actions: %v", acts)
	}
	if p.loaded = false; len(p.home().Actions) != 0 {
		t.Fatal("a phone without NFC offers to add a door card")
	}
}
