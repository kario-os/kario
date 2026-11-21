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

// The reset page says what goes and what stays, and what will be asked
// for; the anti-theft lock's switch is there only with the lock on, and a
// device that cannot be reset has no button.
func TestResetPage(t *testing.T) {
	defer i18n.SetLang("en")
	i18n.SetLang("en")
	var p app
	if err := json.Unmarshal([]byte(`{"reset": {"supported": true, "pin": true, "theft": {"email": "l***i@kario.test"}}}`), &p.s); err != nil {
		t.Fatal(err)
	}
	s := p.resetScreen()
	var text []string
	for _, n := range s.Body {
		text = append(text, n.Text, n.Title, n.Subtitle)
	}
	all := strings.Join(text, "\n")
	for _, want := range []string{"Contacts, messages and the call log", "The system and the updates installed", "The lock-screen PIN is needed",
		"does not ask for l***i@kario.test"} {
		if !strings.Contains(all, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if _, ok := nodeOf(s, "erase"); !ok {
		t.Error("no button")
	}
	p.s.Reset.Theft, p.s.Reset.Minor = nil, true
	s = p.resetScreen()
	if _, ok := nodeOf(s, "keeplock"); ok {
		t.Error("a switch for a lock that is off")
	}
	p.s.Reset.Supported = false
	if _, ok := nodeOf(p.resetScreen(), "erase"); ok {
		t.Error("a button on a device that cannot be reset")
	}
}

// Erasing asks the system with factory_reset: 1, or 2 with the anti-theft
// lock kept.
func TestResetKeepsTheLockWhenAsked(t *testing.T) {
	p, _, reqs := fakeShell(t)
	ready := make(chan struct{})
	p.a.Post(func() {
		p.s.Reset.Supported = true
		p.s.Reset.Theft = &struct {
			Email string `json:"email"`
		}{"l***i@kario.test"}
		close(ready)
	})
	<-ready
	value := func() float64 {
		t.Helper()
		for {
			select {
			case m := <-reqs:
				var args ui.SettingArgs
				if m.Cap != ui.CapSettingsSet || json.Unmarshal(m.Args, &args) != nil || args.Key != ui.SettingFactoryReset {
					continue
				}
				return args.Value
			case <-time.After(2 * time.Second):
				t.Fatal("no factory_reset")
			}
		}
	}
	p.a.Post(func() { p.action(sdk.ActionEvent{Screen: "root", Node: "reset", Action: ui.ActionOpen}) })
	p.a.Post(func() { p.action(sdk.ActionEvent{Screen: "reset", Node: "erase", Action: "erase"}) })
	if v := value(); v != 1 {
		t.Fatalf("erase: %v", v)
	}
	p.a.Post(func() { p.action(sdk.ActionEvent{Screen: "reset", Node: "keeplock", Action: "toggle"}) })
	p.a.Post(func() { p.action(sdk.ActionEvent{Screen: "reset", Node: "erase", Action: "erase"}) })
	if v := value(); v != ui.ResetKeepLock {
		t.Fatalf("erase keeping the lock: %v", v)
	}
}
