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

const wellbeingGet = `{"screen_time": {"days": [{"date": "2026-11-16", "label": "Mon", "ms": 600000}, {"date": "2026-11-17", "label": "Tue", "ms": 4500000}],
	"apps": [{"app": "com.phone.calc", "name": "Calculator", "today_ms": 4500000, "week_ms": 5100000, "limit": 60},
		{"app": "com.phone.dialer", "name": "Phone", "today_ms": 0, "week_ms": 0, "fixed": true}],
	"limit_choices": [15, 30, 60],
	"downtime": {"on": true, "start": 1320, "end": 420, "start_text": "22:00", "end_text": "07:00", "apps": ["com.phone.calc"]}},
	"minor": {"on": true, "apps": [{"app": "com.phone.calc", "name": "Calculator", "allowed": true}, {"app": "com.phone.snake", "name": "Snake"}],
		"contacts": [{"name": "Mum", "number": "13900001111"}], "others": [{"name": "Li", "number": "13800000002"}]}}`

func nodeOf(s ui.Screen, id string) (ui.Node, bool) {
	for _, n := range s.Body {
		if n.ID == id {
			return n, true
		}
	}
	return ui.Node{}, false
}

// The pages show the system's numbers: today's and this week's time, each
// app's with its limit, downtime's times, minor mode's lists; the phone has
// no limit to choose, and minor mode off shows only its switch.
func TestWellbeingPages(t *testing.T) {
	defer i18n.SetLang("en")
	i18n.SetLang("en")
	var p app
	if err := json.Unmarshal([]byte(wellbeingGet), &p.s); err != nil {
		t.Fatal(err)
	}
	s := p.screenTimeScreen()
	if n, _ := nodeOf(s, "st-today"); n.Trailing != "1 h 15 min" {
		t.Errorf("today %q", n.Trailing)
	}
	if n, _ := nodeOf(s, "st-week"); n.Trailing != "1 h 25 min" {
		t.Errorf("week %q", n.Trailing)
	}
	if n, _ := nodeOf(s, "st:com.phone.calc"); n.Subtitle != "This week 1 h 25 min · 1 h a day" || len(n.Actions) != 1 {
		t.Errorf("calculator %+v", n)
	}
	if n, _ := nodeOf(s, "st:com.phone.dialer"); len(n.Actions) != 0 {
		t.Error("a limit for the phone")
	}
	if n, _ := nodeOf(p.limitScreen("com.phone.calc"), "lim:60"); n.Badge == "" {
		t.Error("the limit in force not marked")
	}
	d := p.downtimeScreen()
	if n, _ := nodeOf(d, "dt-start"); n.Trailing != "22:00" {
		t.Errorf("start %+v", n)
	}
	if n, _ := nodeOf(d, "dta:com.phone.calc"); n.Trailing != "Allowed" {
		t.Errorf("allowed in downtime %+v", n)
	}
	if n, _ := nodeOf(p.minorAppsScreen(), "ma:com.phone.snake"); n.Trailing != "Hidden" {
		t.Errorf("snake %+v", n)
	}
	c := p.minorContactsScreen()
	if _, ok := nodeOf(c, "mc:13900001111"); !ok {
		t.Error("the allowed number")
	}
	if _, ok := nodeOf(c, "mo:13800000002"); !ok {
		t.Error("another contact")
	}
	p.s.Minor.On = false
	if m := p.minorScreen(); len(m.Body) != 2 {
		t.Errorf("minor mode off: %d rows", len(m.Body))
	}
	if dayStep(23*60+30, 30) != 0 || dayStep(0, -30) != 23*60+30 {
		t.Error("round the clock")
	}
}

// The pages' presses go to the system as settings.set with the app or the
// number as the text.
func TestWellbeingActionsSetTheSystem(t *testing.T) {
	p, _, reqs := fakeShell(t)
	if err := json.Unmarshal([]byte(wellbeingGet), &p.s); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		e    sdk.ActionEvent
		want ui.SettingArgs
	}{
		{sdk.ActionEvent{Screen: "stl:com.phone.calc", Node: "lim:30", Action: "choose"}, ui.SettingArgs{Key: ui.SettingScreenLimit, Value: 30, Text: "com.phone.calc"}},
		{sdk.ActionEvent{Screen: "downtime", Node: "dt-start", Action: "later"}, ui.SettingArgs{Key: ui.SettingDowntimeStart, Value: 1350}},
		{sdk.ActionEvent{Screen: "downtime", Node: "dt-end", Action: "earlier"}, ui.SettingArgs{Key: ui.SettingDowntimeEnd, Value: 390}},
		{sdk.ActionEvent{Screen: "downtime", Node: "dta:com.phone.calc", Action: "toggle"}, ui.SettingArgs{Key: ui.SettingDowntimeApp, Value: 0, Text: "com.phone.calc"}},
		{sdk.ActionEvent{Screen: "minor-apps", Node: "ma:com.phone.snake", Action: "toggle"}, ui.SettingArgs{Key: ui.SettingMinorApp, Value: 1, Text: "com.phone.snake"}},
		{sdk.ActionEvent{Screen: "minor-contacts", Node: "mo:13800000002", Action: "allow"}, ui.SettingArgs{Key: ui.SettingMinorContact, Value: 1, Text: "13800000002"}},
		{sdk.ActionEvent{Screen: "minor-contacts", Node: "mc:13900001111", Action: "remove"}, ui.SettingArgs{Key: ui.SettingMinorContact, Value: 0, Text: "13900001111"}},
		{sdk.ActionEvent{Screen: "minor", Node: "minor-on", Action: "toggle"}, ui.SettingArgs{Key: ui.SettingMinorMode, Value: 0}},
	} {
		p.a.Post(func() { p.action(c.e) })
		select {
		case m := <-reqs:
			var args ui.SettingArgs
			if err := json.Unmarshal(m.Args, &args); err != nil || m.Cap != ui.CapSettingsSet || args != c.want {
				t.Errorf("%+v: %s %s", c.e, m.Cap, m.Args)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%+v: no request", c.e)
		}
	}
}

// "Forgot the parental PIN" is there only while a parent's account is
// linked; it asks, withdraws while the parent has not confirmed, and sets
// the new PIN once they have; its presses are parent_reset.
func TestWellbeingForgotPIN(t *testing.T) {
	defer i18n.SetLang("en")
	i18n.SetLang("en")
	p, _, reqs := fakeShell(t)
	if err := json.Unmarshal([]byte(wellbeingGet), &p.s); err != nil {
		t.Fatal(err)
	}
	p.s.Minor.Remote = remoteInfo{Available: true, State: "none"}
	if _, ok := nodeOf(p.minorScreen(), "minor-forgot"); ok {
		t.Error("a PIN reset with no parent's account linked")
	}
	for _, c := range []struct {
		reset, title, sub string
		want              float64
	}{
		{"", "Forgot the parental PIN", "mum@example.com confirms on their page", 1},
		{"asked", "Forgot the parental PIN", "Asked at 10:05; waiting for mum@example.com", 0},
		{"ready", "Set a new parental PIN", "Set it before 10:20: mum@example.com", 1},
	} {
		p.s.Minor.Remote = remoteInfo{Available: true, State: "bound", Parent: "mum@example.com", Reset: c.reset, ResetAt: "10:05", ResetUntil: "10:20"}
		n, ok := nodeOf(p.minorScreen(), "minor-forgot")
		if !ok || n.Title != c.title || !strings.Contains(n.Subtitle, c.sub) {
			t.Errorf("%q: %+v", c.reset, n)
		}
		p.a.Post(func() { p.action(sdk.ActionEvent{Screen: "minor", Node: "minor-forgot", Action: n.Actions[0].ID}) })
		select {
		case m := <-reqs:
			var args ui.SettingArgs
			if err := json.Unmarshal(m.Args, &args); err != nil || args != (ui.SettingArgs{Key: ui.SettingParentReset, Value: c.want}) {
				t.Errorf("%q: %s %s", c.reset, m.Cap, m.Args)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%q: no request", c.reset)
		}
	}
}

// Remote management shows what the parent sees before linking, the code
// and its QR code on a secure surface while pairing, who waits to be
// allowed, and who manages the phone; its presses are parent_link and
// parent_confirm.
func TestWellbeingRemote(t *testing.T) {
	defer i18n.SetLang("en")
	i18n.SetLang("en")
	p, _, reqs := fakeShell(t)
	if err := json.Unmarshal([]byte(wellbeingGet), &p.s); err != nil {
		t.Fatal(err)
	}
	has := func(s ui.Screen, text string) bool {
		for _, n := range s.Body {
			if strings.Contains(n.Text+n.Title+n.Subtitle, text) {
				return true
			}
		}
		return false
	}
	if _, ok := nodeOf(p.minorScreen(), "minor-remote"); ok {
		t.Error("remote management without an account server")
	}
	p.s.Minor.Remote = remoteInfo{Available: true, State: "none"}
	if n, ok := nodeOf(p.minorScreen(), "minor-remote"); !ok || n.Subtitle == "" {
		t.Fatal("no remote management row")
	}
	if s := p.remoteScreen(); !has(s, "Where the phone is, only when the parent asks") || !has(s, "Nothing else") {
		t.Error("what the parent sees is not shown before linking")
	}
	p.s.Minor.Remote = remoteInfo{Available: true, State: "pairing", Code: "12345678", URL: "https://account.example.cn/family/#code=12345678", Until: "10:10"}
	s := p.remoteScreen()
	if !has(s, "1234 5678") || !has(s, "open https://account.example.cn/family/,") {
		t.Errorf("pairing: %+v", s.Body)
	}
	qr := false
	for _, n := range s.Body {
		qr = qr || n.Kind == ui.KindImage && n.Src == "qr:https://account.example.cn/family/#code=12345678" && n.Secure
	}
	if !qr {
		t.Error("no QR code on a secure surface")
	}
	p.s.Minor.Remote = remoteInfo{Available: true, State: "claimed", Parent: "mum@example.com"}
	if s := p.remoteScreen(); !has(s, "mum@example.com entered the code") {
		t.Error("who waits is not shown")
	}
	p.s.Minor.Remote = remoteInfo{Available: true, State: "bound", Parent: "mum@example.com", Since: "Nov 20", Version: 2, Applied: "10:30"}
	if s := p.remoteScreen(); !has(s, "Version 2, applied 10:30") || !has(s, "mum@example.com") {
		t.Error("bound")
	}
	if p.wellbeingRow("minor").Subtitle != "Managed by mum@example.com" {
		t.Errorf("menu row: %q", p.wellbeingRow("minor").Subtitle)
	}
	for _, c := range []struct {
		node string
		want ui.SettingArgs
	}{
		{"remote-link", ui.SettingArgs{Key: ui.SettingParentLink, Value: 1}},
		{"remote-unlink", ui.SettingArgs{Key: ui.SettingParentLink, Value: 0}},
		{"remote-allow", ui.SettingArgs{Key: ui.SettingParentConfirm, Value: 1}},
		{"remote-refuse", ui.SettingArgs{Key: ui.SettingParentConfirm, Value: 0}},
	} {
		p.a.Post(func() { p.action(sdk.ActionEvent{Screen: "minor-remote", Node: c.node, Action: c.node}) })
		select {
		case m := <-reqs:
			var args ui.SettingArgs
			if err := json.Unmarshal(m.Args, &args); err != nil || m.Cap != ui.CapSettingsSet || args != c.want {
				t.Errorf("%s: %s %s", c.node, m.Cap, m.Args)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no request", c.node)
		}
	}
}
