package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

func chinaMobile() ui.APNState {
	return ui.APNState{MCC: "460", MNC: "00", Carrier: "China Mobile", Selected: "cmnet", APNs: []ui.APN{
		{ID: "cmnet", Name: "CMNET", APN: "cmnet", Types: []string{"default", "ia", "supl", "xcap"}, IPType: "ipv4v6", Auth: "none"},
		{ID: "cmwap", Name: "CMWAP", APN: "cmwap", Types: []string{"mms"}, IPType: "ipv4v6", Auth: "none", MMSC: "http://mmsc.monternet.com", MMSProxy: "10.0.0.172", MMSPort: 80},
		{ID: "u1", Name: "Office", APN: "corp.example", Types: []string{"default"}, IPType: "ipv4", Auth: "chap", User: "me", PasswordSet: true, Edited: true},
	}}
}

func actionIDs(n ui.Node) []string {
	var out []string
	for _, a := range n.Actions {
		id := a.ID
		if a.Primary {
			id = "*" + id
		}
		out = append(out, id)
	}
	return out
}

// The list shows the carrier and the SIM's network, marks the APN in use,
// offers another internet APN to use, and reset only for the user's list;
// an unknown SIM and an error say so.
func TestAPNPage(t *testing.T) {
	defer func(saved ui.APNState, loaded bool) {
		apnState.APNState, apnState.loaded, apnState.err = saved, loaded, ""
	}(apnState.APNState, apnState.loaded)
	i18n.SetLang("en")
	var p app
	apnState.APNState, apnState.loaded = chinaMobile(), true
	s := p.apnScreen()
	if !strings.Contains(texts(s), "China Mobile · SIM network 460 00") {
		t.Fatalf("head: %q", texts(s))
	}
	if n := node(s, "apn:cmnet"); n.Trailing != "In use" || strings.Join(actionIDs(n), " ") != "*edit delete" || n.Subtitle != "cmnet · Internet · IPv4/IPv6" {
		t.Fatalf("in use: %+v", n)
	}
	if n := node(s, "apn:cmwap"); n.Trailing != "" || strings.Join(actionIDs(n), " ") != "*edit delete" || n.Subtitle != "cmwap · MMS · IPv4/IPv6" {
		t.Fatalf("MMS: %+v", n)
	}
	if n := node(s, "apn:u1"); strings.Join(actionIDs(n), " ") != "*use edit delete" {
		t.Fatalf("another internet APN: %+v", n)
	}
	if node(s, "apn-add").ID == "" || node(s, "apn-reset").ID != "" {
		t.Fatalf("add, no reset: %+v", s.Body)
	}
	apnState.Edited = true
	if r := node(p.apnScreen(), "apn-reset"); r.ID == "" || r.Actions[0].Confirm == "" {
		t.Fatalf("reset: %+v", r)
	}
	apnState.APNState = ui.APNState{MCC: "334", MNC: "020"}
	if !strings.Contains(texts(p.apnScreen()), "the network's default") {
		t.Fatalf("no APNs: %q", texts(p.apnScreen()))
	}
	apnState.APNState = ui.APNState{}
	if !strings.Contains(texts(p.apnScreen()), "not known yet") {
		t.Fatalf("no SIM: %q", texts(p.apnScreen()))
	}
	apnState.err = "No service"
	if texts(p.apnScreen()) != "No service\n" {
		t.Fatalf("error: %q", texts(p.apnScreen()))
	}

	p.radio = ui.RadioState{Cellular: "on", MobileData: true, Data: "connected", APN: "cmnet", Address: "100.80.12.34"}
	if p.apnItem().Subtitle != "cmnet" || p.mobileDataItem().Subtitle != "Connected · cmnet · 100.80.12.34" {
		t.Fatalf("rows: %q, %q", p.apnItem().Subtitle, p.mobileDataItem().Subtitle)
	}
	p.radio.APN, p.radio.Address = "", ""
	if p.apnItem().Subtitle != "The network's default" || p.mobileDataItem().Subtitle != "Connected" {
		t.Fatalf("the network's default: %q, %q", p.apnItem().Subtitle, p.mobileDataItem().Subtitle)
	}
}

// Data roaming: turning it on asks first, turning it off does not; the
// rows say what roaming does now.
func TestRoamingRow(t *testing.T) {
	i18n.SetLang("en")
	var p app
	p.radio = ui.RadioState{Cellular: "on", MobileData: true, Data: "connected"}
	r := p.roamingItem()
	if r.Trailing != "Off" || r.Actions[0].Confirm == "" || r.Subtitle != "Mobile data on other countries' networks" {
		t.Fatalf("off: %+v", r)
	}
	p.radio.Roaming, p.radio.Data = "international", "roaming"
	if r := p.roamingItem(); r.Subtitle != "Roaming abroad now · mobile data off" || !strings.Contains(p.mobileDataItem().Subtitle, "data roaming is off") {
		t.Fatalf("abroad: %q, %q", r.Subtitle, p.mobileDataItem().Subtitle)
	}
	p.radio.DataRoaming, p.radio.Data = true, "connected"
	if r := p.roamingItem(); r.Trailing != "On" || r.Actions[0].Confirm != "" || r.Subtitle != "Roaming abroad now · mobile data on" {
		t.Fatalf("on: %+v", r)
	}
	p.radio.Roaming, p.radio.NationalRoaming = "national", true
	if r, n := p.roamingItem(), p.nationalRoamingItem(); r.Subtitle != "Mobile data on other countries' networks" || n.Trailing != "On" ||
		n.Actions[0].Confirm != "" || n.Subtitle != "National roaming now: mobile data works as at home" {
		t.Fatalf("national: %+v, %+v", r, n)
	}
	if s := p.mobilePage(); s.ID != "g.mobile" || s.Focus != "nationalroaming" || node(s, "nationalroaming").ID == "" {
		t.Fatalf("the notice's page: %+v", s)
	}
	p.radio.NationalRoaming, p.radio.Data = false, "roaming"
	if n := p.nationalRoamingItem(); n.Trailing != "Off" || n.Subtitle != "National roaming now · mobile data off" ||
		p.mobileDataItem().Subtitle != "Off while roaming in your country: national roaming is off" {
		t.Fatalf("national roaming off: %+v, %q", n, p.mobileDataItem().Subtitle)
	}
	p.radio.Roaming = "international"
	if s := p.mobilePage(); s.Focus != "roaming" || p.nationalRoamingItem().Subtitle != "Mobile data on other networks in your country" {
		t.Fatalf("abroad: %+v", s)
	}
}

// The Mobile network page says the IMS (VoLTE) registration in words, as
// the system reports it. The row sets nothing: the top's summary of the
// page leaves it out. A phone without a modem has none.
func TestVoLTERow(t *testing.T) {
	defer i18n.SetLang("en")
	i18n.SetLang("en")
	var p app
	nfcState.tried, locState.tried, hsState.tried = true, true, true
	if _, ok := p.row("volte"); ok {
		t.Fatal("a VoLTE row without a modem")
	}
	p.radio = ui.RadioState{Cellular: "on"}
	subs := map[string]bool{}
	for _, c := range []struct{ ims, word string }{
		{"", "Unknown"}, {ui.IMSUnknown, "Unknown"}, {ui.IMSRegistered, "Registered"}, {ui.IMSRegistering, "Registering…"},
		{ui.IMSNotRegistered, "Not registered"}, {ui.IMSLimited, "Limited"},
	} {
		p.radio.IMS = c.ims
		page, _ := p.groupScreen("g.mobile")
		n := node(page, "volte")
		if n.Title != "VoLTE" || n.Trailing != c.word || n.Subtitle == "" || len(n.Actions) != 0 {
			t.Fatalf("%q: %+v", c.ims, n)
		}
		subs[n.Subtitle] = true
	}
	if len(subs) != 5 {
		t.Errorf("%d explanations for five states", len(subs))
	}
	p.radio.IMS = ui.IMSRegistered
	if g := node(p.root(), "g.mobile"); !strings.HasSuffix(g.Subtitle, "Data usage") {
		t.Errorf("the top's summary: %q", g.Subtitle)
	}
	i18n.SetLang("zh-Hans")
	if n := p.volteItem(); n.Trailing == "Registered" || n.Subtitle == "Calls go over 4G and 5G" {
		t.Errorf("zh-Hans: untranslated %+v", n)
	}
}

// National roaming's switch goes to the system as it is.
func TestNationalRoamingSwitch(t *testing.T) {
	p, _, reqs := fakeShell(t)
	p.a.Post(func() {
		p.radio = ui.RadioState{Cellular: "on", MobileData: true, NationalRoaming: true}
		p.action(sdk.ActionEvent{Screen: "g.mobile", Node: "nationalroaming", Action: "toggle"})
	})
	select {
	case m := <-reqs:
		var args ui.RadioArgs
		if m.Cap != ui.CapRadioSet || json.Unmarshal(m.Args, &args) != nil || args.NationalRoaming == nil || *args.NationalRoaming || args.DataRoaming != nil {
			t.Fatalf("request %s %s", m.Cap, m.Args)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no request")
	}
}

// The edit page keeps what was typed as its choices change, and saves an
// APN as typed: the password only when one was typed, the port checked.
func TestAPNEdit(t *testing.T) {
	defer func(saved ui.APNState, loaded bool) { apnState.APNState, apnState.loaded = saved, loaded }(apnState.APNState, apnState.loaded)
	p, _, reqs := fakeShell(t)
	apnState.APNState, apnState.loaded = chinaMobile(), true
	act := func(e sdk.ActionEvent) { p.a.Post(func() { p.action(e) }) }
	want := func(what string) ui.APNArgs {
		t.Helper()
		select {
		case m := <-reqs:
			var args ui.APNArgs
			if m.Cap != ui.CapAPNSet || json.Unmarshal(m.Args, &args) != nil {
				t.Fatalf("%s: request %s %s", what, m.Cap, m.Args)
			}
			return args
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no request", what)
		}
		return ui.APNArgs{}
	}
	none := func(what string) {
		t.Helper()
		select {
		case m := <-reqs:
			t.Fatalf("%s: request %s %s", what, m.Cap, m.Args)
		case <-time.After(100 * time.Millisecond):
		}
	}

	act(sdk.ActionEvent{Screen: "apn", Node: "apn:cmnet", Action: "edit"})
	act(sdk.ActionEvent{Screen: "apnedit:cmnet", Node: "iptype", Action: "cycle", Fields: map[string]string{"apn": "cmnet.kario"}})
	act(sdk.ActionEvent{Screen: "apnedit:cmnet", Node: "types", Action: "cycle", Fields: map[string]string{"apn": "cmnet.kario"}})
	act(sdk.ActionEvent{Screen: "apnedit:cmnet", Action: "save", Fields: map[string]string{"name": "CMNET", "apn": "cmnet.kario", "pass": ""}})
	args := want("save")
	a := args.APN
	if args.Op != ui.APNSave || a == nil || a.ID != "cmnet" || a.APN != "cmnet.kario" || a.IPType != "ipv4" || a.Password != nil ||
		strings.Join(a.Types, ",") != "default,supl" {
		t.Fatalf("saved %+v %+v", args, a)
	}
	s := make(chan ui.Screen, 1)
	p.a.Post(func() { s <- apnEditScreen() })
	if sc := <-s; node(sc, "apn").Value != "cmnet.kario" || node(sc, "iptype").Trailing != "IPv4" || node(sc, "types").Trailing != "Internet, SUPL" ||
		apnTypeLabel([]string{"default", "ia", "supl", "xcap"}) != "Internet, LTE attach, SUPL, XCAP" || apnUse([]string{"ia", "supl"}) != "LTE attach, SUPL" {
		t.Fatalf("the page after the changes: %+v", sc.Body)
	}

	act(sdk.ActionEvent{Screen: "apn", Node: "apn:u1", Action: "edit"})
	act(sdk.ActionEvent{Screen: "apnedit:u1", Action: "save", Fields: map[string]string{"name": "Office", "apn": "corp.example", "user": "me", "pass": "new-secret", "mmsport": "8080"}})
	if a := want("a new password").APN; a.Password == nil || *a.Password != "new-secret" || a.MMSPort != 8080 || a.Auth != "chap" {
		t.Fatalf("saved %+v", a)
	}
	act(sdk.ActionEvent{Screen: "apnedit:u1", Action: "save", Fields: map[string]string{"name": "Office", "apn": "corp.example", "mmsport": "x"}})
	none("a bad port")
	act(sdk.ActionEvent{Screen: "apnedit:u1", Action: "save", Fields: map[string]string{"name": "Office", "apn": "", "mmsport": ""}})
	none("no APN")
	act(sdk.ActionEvent{Screen: "apnedit:u1", Action: "save", Fields: map[string]string{"name": "Office", "apn": "corp.example", "user": ""}})
	none("CHAP without a user")

	act(sdk.ActionEvent{Screen: "apn", Node: "apn-add", Action: "add"})
	act(sdk.ActionEvent{Screen: "apnedit:", Action: "save", Fields: map[string]string{"name": "Test", "apn": "test.apn"}})
	if a := want("a new APN").APN; a.ID != "" || a.APN != "test.apn" || a.IPType != "ipv4v6" || a.Auth != "none" {
		t.Fatalf("added %+v", a)
	}
	act(sdk.ActionEvent{Screen: "apn", Node: "apn:u1", Action: "use"})
	if args := want("use"); args.Op != ui.APNSelect || args.ID != "u1" {
		t.Fatalf("use %+v", args)
	}
	act(sdk.ActionEvent{Screen: "apn", Node: "apn-reset", Action: "reset"})
	if args := want("reset"); args.Op != ui.APNReset {
		t.Fatalf("reset %+v", args)
	}
}
