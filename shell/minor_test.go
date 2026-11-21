package shell

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// minorCore is stCore with phone-keystored's parental PIN.
func minorCore(t *testing.T) (*locTest, map[string]*App, *keystore.Store) {
	lt, apps := stCore(t)
	c := lt.c
	c.cfg.RunDir = t.TempDir()
	c.broker.clients = map[string]*wire.Client{}
	ks, err := keystore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	go wire.Serve(filepath.Join(c.cfg.RunDir, svc.KeystoredSock), func(method string, params json.RawMessage) (any, error) {
		var p struct{ Old, PIN string }
		_ = json.Unmarshal(params, &p)
		switch method {
		case keystore.MethodParentStatus:
			return ks.ParentStatus(), nil
		case keystore.MethodParentSetPIN:
			return ks.SetParentPIN(p.Old, p.PIN)
		case keystore.MethodParentClearPIN:
			return ks.ClearParentPIN(p.Old)
		case keystore.MethodParentVerify:
			return ks.VerifyParentPIN(p.PIN)
		}
		return nil, errors.New("unknown method " + method)
	})
	return lt, apps, ks
}

// enterPIN types a PIN on the parental PIN screen and waits for the key
// service's answer.
func enterPIN(lt *locTest, pin string) *parentPadOverlay {
	lt.t.Helper()
	o, ok := overlayOf[*parentPadOverlay](lt.c)
	if !ok {
		lt.t.Fatal("no parental PIN screen")
	}
	for _, r := range pin {
		o.key(lt.c, Runes(string(r)))
	}
	o.key(lt.c, Named(KeyEnter))
	lt.pump("the key service", func() bool { return !o.busy })
	return o
}

func noPINScreen(t *testing.T, c *Core) {
	t.Helper()
	if _, ok := overlayOf[*parentPadOverlay](c); ok {
		t.Fatal("the parental PIN was asked for")
	}
}

// Setting a parental PIN turns minor mode on: the apps it does not allow
// leave the menu and the palette and do not start; every change, developer
// mode and turning it off take the PIN, which only phone-keystored keeps.
// A PIN entered in Settings counts while Settings stays in front.
func TestMinorModeParentPIN(t *testing.T) {
	lt, apps, ks := minorCore(t)
	c, snake := lt.c, apps["com.phone.snake"]
	var got []error
	done := func(err error) { got = append(got, err) }
	last := func() error {
		t.Helper()
		if len(got) == 0 {
			t.Fatal("no answer")
		}
		err := got[len(got)-1]
		got = nil
		return err
	}
	c.minorSetting(ui.SettingArgs{Key: ui.SettingMinorMode, Value: 1}, done)
	enterPIN(lt, "1357")
	enterPIN(lt, "1357")
	if err := last(); err != nil || !c.minorOn() || !ks.ParentStatus().PIN {
		t.Fatalf("on: %v, minor %v", err, c.minorOn())
	}
	_ = filepath.WalkDir(c.cfg.StateDir, func(path string, d fs.DirEntry, err error) error {
		if raw, _ := os.ReadFile(path); err == nil && !d.IsDir() && strings.Contains(string(raw), "1357") {
			t.Errorf("%s holds the PIN", path)
		}
		return nil
	})
	if !c.minorHidden(snake) || c.minorHidden(apps["com.phone.calc"]) || c.minorHidden(apps[appDialer]) || c.minorHidden(apps[appSettings]) {
		t.Fatal("hidden apps")
	}
	for _, n := range c.menuScreen().Body {
		if n.ID == "app:"+snake.M.ID {
			t.Fatal("a hidden app on the menu")
		}
	}
	c.openMenu(c.sys, c.sys.top(), true)
	m, _ := overlayOf[*menuOverlay](c)
	for _, e := range m.entries {
		if e.label == snake.M.Name {
			t.Fatal("a hidden app in the palette")
		}
	}
	c.popOverlay(m)
	if err := c.spawn(snake); err == nil || !strings.Contains(err.Error(), "snake") {
		t.Fatalf("a hidden app started: %v", err)
	}
	if c.Launch(snake, ""); c.fg != nil || c.sysTop() != "sys.held" {
		t.Fatal("a hidden app opened")
	}

	c.parental.openUntil = c.parental.openUntil.AddDate(-1, 0, 0)
	c.setForeground(apps[appSettings])
	c.minorSetting(ui.SettingArgs{Key: ui.SettingMinorApp, Text: snake.M.ID, Value: 1}, done)
	if o := enterPIN(lt, "0000"); !o.bad || ks.ParentStatus().Failures != 1 {
		t.Fatalf("a wrong PIN: %+v", o)
	}
	o, _ := overlayOf[*parentPadOverlay](c)
	o.key(c, Named(KeyEsc))
	if err := last(); !ui.Cancelled(err) || !c.minorHidden(snake) {
		t.Fatalf("cancelled: %v", err)
	}
	c.minorSetting(ui.SettingArgs{Key: ui.SettingMinorApp, Text: snake.M.ID, Value: 1}, done)
	enterPIN(lt, "1357")
	if err := last(); err != nil || c.minorHidden(snake) {
		t.Fatalf("allowed: %v", err)
	}
	c.minorSetting(ui.SettingArgs{Key: ui.SettingMinorContact, Text: "13900001111", Value: 1}, done)
	noPINScreen(t, c)
	if err := last(); err != nil || c.minorRefuses("13900001111") {
		t.Fatalf("a contact allowed in the same visit: %v", err)
	}
	c.screenTimeSetting(ui.SettingArgs{Key: ui.SettingScreenLimit, Text: snake.M.ID, Value: 30}, done)
	noPINScreen(t, c)
	_ = last()

	c.setForeground(apps["com.phone.calc"])
	c.setForeground(apps[appSettings])
	c.minorSetting(ui.SettingArgs{Key: ui.SettingDeveloperMode, Value: 1}, done)
	if _, ok := overlayOf[*parentPadOverlay](c); !ok || c.settings.DeveloperMode {
		t.Fatal("developer mode without the PIN after Settings left")
	}
	enterPIN(lt, "1357")
	if err := last(); err != nil || !c.settings.DeveloperMode {
		t.Fatalf("developer mode: %v", err)
	}
	c.settings.DeveloperMode = false
	c.screenTimeSetting(ui.SettingArgs{Key: ui.SettingDowntime, Value: 1}, done)
	_ = last()
	c.screenTime().Days[day(c.now())] = map[string]int64{snake.M.ID: 60000}

	c.minorSetting(ui.SettingArgs{Key: ui.SettingMinorMode, Value: 0}, done)
	if o, ok := overlayOf[*parentPadOverlay](c); !ok || o.op != "clear" {
		t.Fatal("turned off without the PIN")
	}
	if enterPIN(lt, "2468"); !c.minorOn() {
		t.Fatal("turned off with a wrong PIN")
	}
	enterPIN(lt, "1357")
	if err := last(); err != nil || c.minorOn() || ks.ParentStatus().PIN || c.minorHidden(snake) {
		t.Fatalf("off: %v", err)
	}
	if _, err := os.Stat(c.minorPath()); !os.IsNotExist(err) {
		t.Fatalf("the policy stays: %v", err)
	}
	st := c.screenTime()
	if len(st.Limits) != 0 || st.Downtime.On || c.held(snake) != "" {
		t.Fatalf("the parent's limits stay: %v, downtime %v", st.Limits, st.Downtime.On)
	}
	if st.Days[day(c.now())][snake.M.ID] != 60000 {
		t.Fatal("the time counted went")
	}
}

// phone-keystored's word decides at start: a parental PIN set is minor
// mode on even when the Shell's file is gone, and none is minor mode off.
func TestMinorModeFollowsTheKeyService(t *testing.T) {
	lt, apps, ks := minorCore(t)
	c := lt.c
	ks.SetParentPIN("", "1357")
	c.parental.asked = false
	c.minorStart()
	lt.pump("the key service", c.minorOn)
	if !c.minorHidden(apps["com.phone.snake"]) {
		t.Fatal("not the first apps allowed")
	}
	ks.ClearParentPIN("1357")
	c.parental.asked = false
	c.minorStart()
	lt.pump("the key service", func() bool { return !c.minorOn() })
}

// Calls and messages go only to and from the numbers allowed, however
// written; emergency numbers always; the dialer's call to another number
// is refused before it reaches telephonyd.
func TestMinorModeCallsAndMessages(t *testing.T) {
	lt, apps := stCore(t)
	c := lt.c
	c.parental.p = &minorPolicy{On: true, Contacts: []minorContact{{Name: "Mum", Number: "13900001111"}}}
	for n, want := range map[string]string{"13700000088": ui.BlockedMinor, "+86 139 0000 1111": "", "112": "", "unknown": ui.BlockedMinor} {
		if got := c.callBlocked(n); got != want {
			t.Errorf("call from %s: %q, want %q", n, got, want)
		}
	}
	if c.minorRecipients("13900001111") != nil || c.minorRecipients("13900001111;13700000088") == nil {
		t.Error("message recipients")
	}
	d := apps[appDialer]
	lt.req(d, ui.CapTelDial, ui.DialArgs{Number: "13700000088"})
	if r := lt.reply(d); r.OK || !strings.Contains(r.Err, tr.T("Minor mode: only the contacts a parent allowed can be called")) {
		t.Fatalf("dial: %+v", r)
	}
	c.parental.p.On = false
	if c.callBlocked("13700000088") != "" {
		t.Error("blocked with minor mode off")
	}
}

// In minor mode an install from the store waits for the parental PIN, and
// the app it installs is allowed.
func TestMinorModeInstallTakesTheParentPIN(t *testing.T) {
	lt, _, ks := minorCore(t)
	c := lt.c
	ks.SetParentPIN("", "1357")
	c.setMinor(true)
	installed := 0
	install := func(reply func(any, error)) {
		installed++
		reply(ui.PkgInfo{ID: "com.example.game"}, nil)
	}
	var errs []error
	reply := func(_ any, err error) { errs = append(errs, err) }
	c.minorInstall("com.example.game", reply, install)
	o, _ := overlayOf[*parentPadOverlay](c)
	o.key(c, Named(KeyEsc))
	if installed != 0 || len(errs) != 1 || !ui.Cancelled(errs[0]) {
		t.Fatalf("installed %d without the PIN: %v", installed, errs)
	}
	c.minorInstall("com.example.game", reply, install)
	enterPIN(lt, "1357")
	if installed != 1 || len(errs) != 2 || errs[1] != nil || !strings.Contains(strings.Join(c.minor().Apps, " "), "com.example.game") {
		t.Fatalf("installed %d: %v, allowed %v", installed, errs, c.minor().Apps)
	}
}
