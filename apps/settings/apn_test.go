package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
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
// an unknown SIM says so.
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

	p.radio = ui.RadioState{Cellular: "on", MobileData: true, Data: "connected", APN: "cmnet", Address: "100.80.12.34"}
	if p.apnItem().Subtitle != "cmnet" || p.mobileDataItem().Subtitle != "Connected · cmnet · 100.80.12.34" {
		t.Fatalf("rows: %q, %q", p.apnItem().Subtitle, p.mobileDataItem().Subtitle)
	}
	p.radio.APN, p.radio.Address = "", ""
	if p.apnItem().Subtitle != "The network's default" || p.mobileDataItem().Subtitle != "Connected" {
		t.Fatalf("the network's default: %q, %q", p.apnItem().Subtitle, p.mobileDataItem().Subtitle)
	}
}

// A list that does not load says why in the user's language, the Shell's
// own refusal in the Shell's words, and the page stays usable: Try again
// reads the list again, and a list read before stays under the warning.
func TestAPNListError(t *testing.T) {
	saved, loaded := apnState.APNState, apnState.loaded
	t.Cleanup(func() { apnState.APNState, apnState.loaded, apnState.err = saved, loaded, "" })
	others := make(chan ui.Msg, 64)
	p, shell, reqs := fakeShellWith(t, others)
	i18n.SetLang("zh-Hans")
	t.Cleanup(func() { i18n.SetLang("en") })
	p.a.Post(func() { apnState.APNState, apnState.loaded, apnState.err = ui.APNState{}, false, "" })
	pages := make(chan ui.Screen, 16)
	go func() {
		for m := range others {
			if (m.T == ui.MsgPush || m.T == ui.MsgUpdate) && m.Screen != nil && m.Screen.ID == "apn" {
				pages <- *m.Screen
			}
		}
	}()
	act := func(e sdk.ActionEvent) { p.a.Post(func() { p.action(e) }) }
	list := func(e sdk.ActionEvent, why string) ui.Screen {
		t.Helper()
		act(e)
		select {
		case m := <-reqs:
			if m.Cap != ui.CapAPNList {
				t.Fatalf("%s: request %s", why, m.Cap)
			}
			res := ui.Msg{T: ui.MsgRes, Req: m.Req, Err: why}
			if why == "" {
				res.OK = true
				res.Data, _ = json.Marshal(chinaMobile())
			}
			_ = shell.Send(res)
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no request", why)
		}
		select {
		case s := <-pages:
			return s
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no page", why)
		}
		return ui.Screen{}
	}
	warning := func(s ui.Screen) string {
		if len(s.Body) < 2 || s.Body[0].Style != ui.StyleWarn || strings.Join(actionIDs(s.Body[1]), " ") != "*retry" {
			return ""
		}
		return s.Body[0].Text
	}
	open := sdk.ActionEvent{Screen: "g.mobile", Node: "apn", Action: ui.ActionOpen}
	retry := sdk.ActionEvent{Screen: "apn", Node: "apn-retry", Action: "retry"}

	noService := shellCatalog(t).In("zh-Hans").T(ui.ErrNoService)
	if s := list(open, noService); warning(s) != noService || noService == ui.ErrNoService || len(s.Body) != 2 {
		t.Errorf("the Shell's refusal: %+v", s.Body)
	}
	if s := list(retry, "phone-telephonyd unavailable: connection refused"); warning(s) != tr.T("The APNs could not be read") || warning(s) == "The APNs could not be read" {
		t.Errorf("telephonyd away: %+v", s.Body)
	}
	if s := list(retry, ""); len(s.Body) == 0 || s.Body[0].Style == ui.StyleWarn || node(s, "apn-retry").ID != "" || node(s, "apn:cmnet").ID == "" {
		t.Errorf("read again: %+v", s.Body)
	}
	s := list(open, "service did not answer in time")
	if warning(s) != tr.T("The APNs could not be read") || strings.Join(actionIDs(node(s, "apn:u1")), " ") != "*use edit delete" || node(s, "apn-add").ID == "" {
		t.Errorf("the list read before: %+v", s.Body)
	}
}

// A restored edit page (the process the system ended had it open) reads
// its APN from the list: it has no fields until then, so a save cannot
// write a blank APN over it. A list that does not load says why as the
// list page does, with Try again; the APN read fills the page. An APN no
// longer in the list goes back to the list, unless the user left already.
func TestAPNRestoredEdit(t *testing.T) {
	saved, loaded := apnState.APNState, apnState.loaded
	others := make(chan ui.Msg, 64)
	p, shell, reqs := fakeShellWith(t, others)
	t.Cleanup(func() {
		done := make(chan bool)
		p.a.Post(func() {
			apnState.APNState, apnState.loaded, apnState.err = saved, loaded, ""
			apnDraftOf("")
			close(done)
		})
		<-done
	})
	p.a.OnPopped = p.onPopped
	i18n.SetLang("zh-Hans")
	t.Cleanup(func() { i18n.SetLang("en") })
	edits, said := make(chan ui.Screen, 16), make(chan string, 16)
	go func() {
		for m := range others {
			switch {
			case (m.T == ui.MsgPush || m.T == ui.MsgUpdate) && m.Screen != nil && strings.HasPrefix(m.Screen.ID, "apnedit:"):
				edits <- *m.Screen
			case m.T == ui.MsgPop:
				said <- "pop"
			case m.T == ui.MsgToast:
				said <- m.Text
			}
		}
	}()
	page := func(what string) ui.Screen {
		t.Helper()
		select {
		case s := <-edits:
			return s
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no edit page", what)
		}
		return ui.Screen{}
	}
	answer := func(why string) {
		t.Helper()
		select {
		case m := <-reqs:
			if m.Cap != ui.CapAPNList {
				t.Fatalf("%s: request %s", why, m.Cap)
			}
			res := ui.Msg{T: ui.MsgRes, Req: m.Req, OK: why == "", Err: why}
			if why == "" {
				res.Data, _ = json.Marshal(chinaMobile())
			}
			_ = shell.Send(res)
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no request", why)
		}
	}
	// The list page and the edit page each read the list; the pages come
	// after the two requests.
	start := func(id string) {
		done := make(chan bool)
		p.a.Post(func() {
			apnState.APNState, apnState.loaded, apnState.err, p.pages = ui.APNState{}, false, "", nil
			p.startShown(ui.Screen{ID: "root", Title: "Settings"}, "", true, savedPages{Pages: []string{"apn", "apnedit:" + id}})
			close(done)
		})
		<-done
	}
	restore := func(id string, why string) ui.Screen {
		t.Helper()
		start(id)
		answer(why)
		answer(why)
		return page(id + " restored")
	}
	warning := func(s ui.Screen) string {
		if len(s.Body) != 2 || s.Body[0].Style != ui.StyleWarn || s.Body[1].ID != "apn-retry" || strings.Join(actionIDs(s.Body[1]), " ") != "*retry" {
			return ""
		}
		return s.Body[0].Text
	}
	act := func(e sdk.ActionEvent) { p.a.Post(func() { p.action(e) }) }
	retry := sdk.ActionEvent{Screen: "apnedit:u1", Node: "apn-retry", Action: "retry"}

	noService := shellCatalog(t).In("zh-Hans").T(ui.ErrNoService)
	if s := restore("u1", noService); s.ID != "apnedit:u1" || len(s.Body) != 0 || len(s.Actions) != 0 {
		t.Errorf("before the list: %+v", s)
	}
	if s := page("the Shell's refusal"); warning(s) != noService || s.Title != tr.T("Edit the APN") || len(s.Actions) != 0 {
		t.Errorf("the Shell's refusal: %+v", s)
	}
	act(retry)
	answer("phone-telephonyd unavailable: connection refused")
	if s := page("telephonyd away"); warning(s) != tr.T("The APNs could not be read") || warning(s) == "The APNs could not be read" {
		t.Errorf("telephonyd away: %+v", s.Body)
	}
	act(retry)
	answer("")
	s := page("read again")
	if node(s, "name").Value != "Office" || node(s, "apn").Value != "corp.example" || node(s, "auth").Trailing != "CHAP" ||
		node(s, "pass").Placeholder != tr.T("Password (kept unless you type a new one)") || node(s, "apn-retry").ID != "" || len(s.Actions) != 1 {
		t.Errorf("read again: %+v", s)
	}
	act(sdk.ActionEvent{Screen: "apnedit:u1", Node: "iptype", Action: "cycle", Fields: map[string]string{"name": "Office 2"}})
	if s := page("typed"); node(s, "name").Value != "Office 2" || node(s, "iptype").Trailing != "IPv6" {
		t.Errorf("typed: %+v", s.Body)
	}

	restore("gone", "")
	for _, want := range []string{"pop", tr.T("This APN is no longer in the list")} {
		select {
		case got := <-said:
			if got != want {
				t.Errorf("gone: %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("gone: no %q", want)
		}
	}

	start("left")
	_ = shell.Send(ui.Msg{T: ui.MsgPopped, ScreenID: "apnedit:left"})
	for deadline := time.Now().Add(2 * time.Second); ; {
		top := make(chan string)
		p.a.Post(func() { top <- p.a.Top() })
		if <-top == "apn" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("left: the edit page still on top")
		}
		time.Sleep(10 * time.Millisecond)
	}
	answer("")
	answer("")
	page("left restored")
	select {
	case got := <-said:
		t.Errorf("left before the answer: %q", got)
	case <-time.After(200 * time.Millisecond):
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
	saved, loaded := apnState.APNState, apnState.loaded
	t.Cleanup(func() { apnState.APNState, apnState.loaded = saved, loaded })
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

// A refused apn.set is a toast in the user's language: telephonyd's reason
// in Settings' words, the Shell's own refusal in the Shell's, any other
// refusal the APN not saved or the list not changed; a cancelled one is
// none. The edit page stays for another try.
func TestAPNRefusalToast(t *testing.T) {
	saved, loaded := apnState.APNState, apnState.loaded
	t.Cleanup(func() { apnState.APNState, apnState.loaded = saved, loaded })
	others := make(chan ui.Msg, 64)
	p, shell, reqs := fakeShellWith(t, others)
	i18n.SetLang("zh-Hans")
	t.Cleanup(func() { i18n.SetLang("en") })
	p.a.Post(func() { apnState.APNState, apnState.loaded = chinaMobile(), true })
	toasts := make(chan string, 16)
	go func() {
		for m := range others {
			if m.T == ui.MsgToast {
				toasts <- m.Text
			}
		}
	}()
	act := func(e sdk.ActionEvent) { p.a.Post(func() { p.action(e) }) }
	refuse := func(e sdk.ActionEvent, why string) string {
		t.Helper()
		act(e)
		select {
		case m := <-reqs:
			if m.Cap != ui.CapAPNSet {
				t.Fatalf("%s: request %s", why, m.Cap)
			}
			_ = shell.Send(ui.Msg{T: ui.MsgRes, Req: m.Req, Err: why})
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no request", why)
		}
		select {
		case s := <-toasts:
			return s
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no toast", why)
		}
		return ""
	}

	act(sdk.ActionEvent{Screen: "apn", Node: "apn-add", Action: "add"})
	add := sdk.ActionEvent{Screen: "apnedit:", Action: "save", Fields: map[string]string{"name": "Test", "apn": "test.apn"}}
	full := tr.T("A SIM has at most 32 APNs: delete some first")
	if got := refuse(add, ui.ErrAPNFull); got != full || got == "A SIM has at most 32 APNs: delete some first" {
		t.Errorf("a full list: %q, want %q", got, full)
	}
	if got := refuse(add, "phone-telephonyd unavailable: connection refused"); got != tr.T("The APN could not be saved") || got == "The APN could not be saved" {
		t.Errorf("telephonyd away: %q", got)
	}
	noService := shellCatalog(t).In("zh-Hans").T(ui.ErrNoService)
	if got := refuse(add, noService); got != noService || noService == ui.ErrNoService {
		t.Errorf("the Shell's refusal: %q, want its words %q", got, noService)
	}
	if got := refuse(sdk.ActionEvent{Screen: "apn", Node: "apn:cmwap", Action: "delete"}, noService); got != noService {
		t.Errorf("the Shell's refusal of a delete: %q", got)
	}
	act(sdk.ActionEvent{Screen: "apn", Node: "apn:u1", Action: "edit"})
	edit := sdk.ActionEvent{Screen: "apnedit:u1", Action: "save", Fields: map[string]string{"name": "Office", "apn": "corp.example", "user": "me"}}
	if got := refuse(edit, "user: "+ui.ErrAPNControl); got != tr.T("An APN cannot have line breaks or other control characters") {
		t.Errorf("a control character: %q", got)
	}
	if got := refuse(sdk.ActionEvent{Screen: "apn", Node: "apn:u1", Action: "use"}, ui.ErrAPNGone); got != tr.T("This APN is no longer in the list") {
		t.Errorf("gone: %q", got)
	}
	if got := refuse(sdk.ActionEvent{Screen: "apn", Node: "apn:cmwap", Action: "delete"}, "service did not answer in time"); got != tr.T("The APNs could not be changed") {
		t.Errorf("no answer: %q", got)
	}
	act(edit)
	select {
	case m := <-reqs:
		_ = shell.Send(ui.Msg{T: ui.MsgRes, Req: m.Req, Err: ui.ErrCancelled})
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled: no request")
	}
	select {
	case s := <-toasts:
		t.Errorf("cancelled: toast %q", s)
	case <-time.After(200 * time.Millisecond):
	}
}

// Every reason has its own words in every language Settings has; one
// reason does not end another (they are told apart by the error's end).
func TestAPNRefusalTexts(t *testing.T) {
	defer i18n.SetLang("en")
	i18n.SetLang("en")
	en := apnRefusals()
	for _, lang := range []string{"en", "zh-Hans", "zh-Hant", "es"} {
		i18n.SetLang(lang)
		texts := apnRefusals()
		for reason, text := range texts {
			if got := apnRefusal(ui.APNSave, errors.New(ui.CapAPNSet+": "+reason)); got != text {
				t.Errorf("%s %q: %q, want %q", lang, reason, got, text)
			}
			if lang != "en" && text == en[reason] {
				t.Errorf("%s %q: untranslated %q", lang, reason, text)
			}
		}
		if lang != "en" && (apnRefusal(ui.APNSave, errors.New("x")) == "The APN could not be saved" || apnRefusal(ui.APNDelete, errors.New("x")) == "The APNs could not be changed") {
			t.Errorf("%s: the other refusals untranslated", lang)
		}
	}
	for reason := range apnShellRefusals() {
		en[reason] = ""
	}
	for a := range en {
		for b := range en {
			if a != b && strings.HasSuffix(a, b) {
				t.Errorf("%q ends in %q", a, b)
			}
		}
	}
}

// shellCatalog is the Shell's own translations, shell/locale/<language>.json.
func shellCatalog(t *testing.T) *i18n.Catalog {
	t.Helper()
	files, _ := filepath.Glob("../../shell/locale/*.json")
	if len(files) == 0 {
		t.Fatal("no catalogs in ../../shell/locale")
	}
	fsys := fstest.MapFS{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		fsys["locale/"+filepath.Base(f)] = &fstest.MapFile{Data: b}
	}
	return i18n.New(fsys)
}

// Settings knows the Shell's own refusals by the words the Shell sends:
// its text for each is the Shell's catalog's in every language, and is
// what the toast and the page say.
func TestAPNShellRefusalsAreTheShells(t *testing.T) {
	defer i18n.SetLang("en")
	shell := shellCatalog(t)
	for _, lang := range []string{"en", "zh-Hans", "zh-Hant", "es"} {
		i18n.SetLang(lang)
		for reason, text := range apnShellRefusals() {
			if want := shell.T(reason); text != want || lang != "en" && text == reason {
				t.Errorf("%s %q: Settings says %q, the Shell %q", lang, reason, text, want)
			}
			for _, words := range []string{reason, text} {
				if got := apnRefusal(ui.APNSave, errors.New(ui.CapAPNSet+": "+words)); got != words {
					t.Errorf("%s: apn.set refused with %q says %q", lang, words, got)
				}
				if got := apnListError(errors.New(ui.CapAPNList + ": " + words)); got != words {
					t.Errorf("%s: apn.list refused with %q says %q", lang, words, got)
				}
			}
		}
	}
}

// The reasons Settings knows are telephonyd's refusals, word for word.
func TestAPNRefusalsAreTelephonyds(t *testing.T) {
	raw, err := os.ReadFile("../../telephonyd/src/apn.hpp")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)namespace refused \{(.*?)\}  // namespace refused`).FindSubmatch(raw)
	if block == nil {
		t.Fatal("no namespace refused in apn.hpp")
	}
	var tel []string
	for _, m := range regexp.MustCompile(`= "([^"\\]*)";`).FindAllSubmatch(block[1], -1) {
		tel = append(tel, string(m[1]))
	}
	var known []string
	for reason := range apnRefusals() {
		known = append(known, reason)
	}
	slices.Sort(tel)
	slices.Sort(known)
	if !slices.Equal(tel, known) {
		t.Errorf("telephonyd refuses with %q,\nSettings knows %q", tel, known)
	}
}
