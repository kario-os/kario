package main

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// Two presses before the system answers the first (a slow emulator): the
// system gets the changes in the order pressed, and an answer to an older
// change does not undo a newer one kept at once, which would also make the
// next press start from the old value. The fake system applies requests in
// the order they arrive and answers each with the state it left, as the
// Shell does, but answers the pending ones newest first.
func TestQuickPressesKeepTheirOrder(t *testing.T) {
	p, shell, reqs := fakeShell(t)
	a := p.a
	p.s.Settings.Motion = ui.MotionOff
	system := ui.MotionOff
	type answer struct {
		req    int
		motion string
	}
	var pending []answer
	reply := func() {
		for _, r := range slices.Backward(pending) {
			var st settings
			st.Settings.Motion = r.motion
			data, _ := json.Marshal(st)
			if err := shell.Send(ui.Msg{T: ui.MsgRes, Req: r.req, OK: true, Data: data}); err != nil {
				t.Fatal(err)
			}
		}
		pending = nil
	}
	for range 2 {
		a.Post(func() {
			p.action(sdk.ActionEvent{Screen: groupPrefix + "display", Node: ui.SettingMotion, Action: "prev"})
		})
	}
	for sent := 0; sent < 2; {
		select {
		case m := <-reqs:
			var args ui.SettingArgs
			if err := json.Unmarshal(m.Args, &args); err != nil || m.Cap != ui.CapSettingsSet || args.Key != ui.SettingMotion {
				t.Fatalf("request %s %s: %v", m.Cap, m.Args, err)
			}
			system = ui.MotionLevels[int(args.Value)]
			pending = append(pending, answer{m.Req, system})
			sent++
		case <-time.After(300 * time.Millisecond):
			reply()
		}
	}
	reply()
	time.Sleep(300 * time.Millisecond)
	got := make(chan string)
	a.Post(func() { got <- p.s.Settings.Motion })
	if m := <-got; system != "" || m != "" {
		t.Fatalf("two presses back from off: the system has %q, the page shows %q; want both standard", system, m)
	}
}

// The install's call is answered when it ends; a refresh the system
// answered before it took the install up says it is not installing, and
// shown, it stopped the refreshing with the page on Available while the
// phone installed (the GitHub runner, 2026-11-17). Refreshes go on while
// the install waits, and the install's answer has the last word.
func TestStatusDoesNotUndoInstall(t *testing.T) {
	p, shell, reqs := fakeShell(t)
	p.updatePage = true
	p.upd.State = "idle"
	answer := func(m ui.Msg, state string) {
		data, _ := json.Marshal(map[string]string{"state": state})
		if err := shell.Send(ui.Msg{T: ui.MsgRes, Req: m.Req, OK: true, Data: data}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	state := func() string {
		got := make(chan string)
		p.a.Post(func() { got <- p.upd.State })
		return <-got
	}
	p.a.Post(func() { p.action(sdk.ActionEvent{Screen: "update", Node: "available", Action: ui.UpdateInstall}) })
	install := <-reqs
	p.a.Post(p.pollUpdate)
	answer(<-reqs, "idle")
	if s := state(); s != "installing" {
		t.Fatalf("a refresh answered before the install began: the page has %q, want installing", s)
	}
	p.a.Post(p.pollUpdate)
	select {
	case m := <-reqs:
		answer(m, "installing")
	case <-time.After(time.Second):
		t.Fatal("no refresh while installing")
	}
	answer(install, "installed")
	if s := state(); s != "installed" {
		t.Fatalf("after the install's answer the page has %q, want installed", s)
	}
}

// fakeShell starts the app against a socket the test plays the Shell on:
// reqs gets the app's capability requests.
func fakeShell(t *testing.T) (*app, *wire.Conn, <-chan ui.Msg) {
	t.Helper()
	return fakeShellWith(t, nil)
}

// fakeShellWith also hands the app's other messages (pages, toasts) to
// others.
func fakeShellWith(t *testing.T, others chan<- ui.Msg) (*app, *wire.Conn, <-chan ui.Msg) {
	t.Helper()
	i18n.SetLang("en")
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	shell := wire.New(os.NewFile(uintptr(fds[1]), "shell"))
	t.Cleanup(func() { shell.Close() })
	t.Setenv("PHONE_UI_FD", strconv.Itoa(fds[0]))
	a, err := sdk.New()
	if err != nil {
		t.Fatal(err)
	}
	p := &app{a: a}
	a.OnAction = p.action
	ran := make(chan struct{})
	go func() {
		a.Run()
		close(ran)
	}()
	// The package's state is the app loop's: a test's cleanups registered
	// before this one, and the next test, touch it only once the loop has
	// stopped (the socket in between is no happens-before edge for -race).
	t.Cleanup(func() {
		shell.Close()
		select {
		case <-ran:
		case <-time.After(10 * time.Second):
			t.Error("the app's loop did not stop when the Shell closed its connection")
		}
	})
	reqs := make(chan ui.Msg)
	go func() {
		for {
			var m ui.Msg
			if shell.Recv(&m) != nil {
				close(reqs)
				if others != nil {
					close(others)
				}
				return
			}
			switch {
			case m.T == ui.MsgReq:
				reqs <- m
			case others != nil:
				others <- m
			}
		}
	}()
	return p, shell, reqs
}

// A notification that starts Settings arrives while the start still loads:
// its pages go up after the start's reset, which used to take them away
// when its answers came last (the app's data limit page on the dev machine).
func TestNotifOpenedDuringTheStart(t *testing.T) {
	others := make(chan ui.Msg, 16)
	p, _, _ := fakeShellWith(t, others)
	early := make(chan bool, 1)
	p.a.Post(func() {
		p.whenStarted(func() { p.push(ui.Screen{ID: "traffic", Title: "Data usage"}) })
		early <- len(p.pages) > 0
		p.startShown(ui.Screen{ID: "root", Title: "Settings"}, "", false, savedPages{})
	})
	if <-early {
		t.Fatal("the notification's page went up before the start's")
	}
	var got []string
	for len(got) < 2 {
		select {
		case m := <-others:
			if (m.T == ui.MsgReset || m.T == ui.MsgPush) && m.Screen != nil {
				got = append(got, m.T+" "+m.Screen.ID)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("pages sent: %v", got)
		}
	}
	if want := []string{ui.MsgReset + " root", ui.MsgPush + " traffic"}; !slices.Equal(got, want) {
		t.Fatalf("pages sent %v, want %v", got, want)
	}
}

// The Wi-Fi and Bluetooth switches are kept at once, as airplane mode and
// mobile data are: a second press before the system answers the first
// switches back instead of sending the first press's value again (Bluetooth
// stayed off on QEMU when the page was turned off and on quickly).
func TestRadioSwitchesKeptAtOnce(t *testing.T) {
	p, _, reqs := fakeShell(t)
	ready := make(chan struct{})
	p.a.Post(func() { p.bt.Enabled, p.wifi.Enabled = true, true; close(ready) })
	<-ready
	for _, c := range []struct {
		screen, node, cap string
		value             func(json.RawMessage) *bool
	}{
		{"bt", "power", ui.CapBTSet, func(raw json.RawMessage) *bool { var a ui.BTArgs; _ = json.Unmarshal(raw, &a); return a.Enabled }},
		{"wifi", "power", ui.CapWifiSet, func(raw json.RawMessage) *bool { var a ui.WifiArgs; _ = json.Unmarshal(raw, &a); return a.Enabled }},
	} {
		for range 2 {
			p.a.Post(func() { p.action(sdk.ActionEvent{Screen: c.screen, Node: c.node, Action: "toggle"}) })
		}
		var got []bool
		for len(got) < 2 {
			select {
			case m := <-reqs:
				if m.Cap != c.cap {
					continue
				}
				if v := c.value(m.Args); v != nil {
					got = append(got, *v)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s %s: only %v sent", c.screen, c.node, got)
			}
		}
		if !slices.Equal(got, []bool{false, true}) {
			t.Errorf("%s %s pressed twice from on: sent %v, want [false true]", c.screen, c.node, got)
		}
	}
}

// A refresh of the radios asked for before a switch was pressed, answered
// after it, does not put the old state back (QEMU: the answer to turning
// Bluetooth off came after it was turned on again, and the page went back
// to off under the next key).
func TestOldRefreshDoesNotUndoASwitch(t *testing.T) {
	p, shell, reqs := fakeShell(t)
	ready := make(chan struct{})
	p.a.Post(func() { p.bt.Enabled = true; close(ready) })
	<-ready
	p.a.Post(func() { p.loadRadios(func() {}) })
	var old ui.Msg
	for old.Cap != ui.CapWifiState {
		old = <-reqs
	}
	p.a.Post(func() { p.action(sdk.ActionEvent{Screen: "bt", Node: "power", Action: "toggle"}) })
	p.a.Post(func() { p.action(sdk.ActionEvent{Screen: "bt", Node: "power", Action: "toggle"}) })
	answer := func(m ui.Msg, v any) {
		data, _ := json.Marshal(v)
		if err := shell.Send(ui.Msg{T: ui.MsgRes, Req: m.Req, OK: true, Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	answer(old, ui.WifiState{})
	for m := range reqs {
		if m.Cap == ui.CapBTState {
			answer(m, ui.BTState{Enabled: false})
			break
		}
	}
	time.Sleep(200 * time.Millisecond)
	got := make(chan bool)
	p.a.Post(func() { got <- p.bt.Enabled })
	if !<-got {
		t.Fatal("pressed off and on again: an answer asked for before the presses turned the page off")
	}
}

// answerer answers the Shell's side of fakeShell: next waits for a request
// for cap, answering the others with an empty object.
type answerer struct {
	t     *testing.T
	shell *wire.Conn
	reqs  <-chan ui.Msg
}

func (r answerer) answer(m ui.Msg, v any) {
	data, _ := json.Marshal(v)
	if err := r.shell.Send(ui.Msg{T: ui.MsgRes, Req: m.Req, OK: true, Data: data}); err != nil {
		r.t.Fatal(err)
	}
}

func (r answerer) next(cap string) ui.Msg {
	r.t.Helper()
	for {
		select {
		case m := <-r.reqs:
			if m.Cap == cap {
				return m
			}
			r.answer(m, struct{}{})
		case <-time.After(2 * time.Second):
			r.t.Fatalf("no %s asked for", cap)
		}
	}
}

func btOf(p *app) ui.BTState {
	got := make(chan ui.BTState)
	p.a.Post(func() { got <- p.bt })
	return <-got
}

// Two refreshes whose answers cross: the older one, answered last, does
// not replace what the newer one showed.
func TestLateRefreshDoesNotReplaceANewerOne(t *testing.T) {
	p, shell, reqs := fakeShell(t)
	r := answerer{t, shell, reqs}
	p.a.Post(func() { p.loadRadios(func() {}) })
	older := r.next(ui.CapWifiState)
	p.a.Post(func() { p.loadRadios(func() {}) })
	r.answer(r.next(ui.CapWifiState), ui.WifiState{})
	r.answer(r.next(ui.CapBTState), ui.BTState{Enabled: true, Discovering: true})
	r.answer(r.next(ui.CapRadioState), ui.RadioState{})
	r.answer(older, ui.WifiState{})
	r.answer(r.next(ui.CapBTState), ui.BTState{Enabled: true})
	r.answer(r.next(ui.CapRadioState), ui.RadioState{})
	time.Sleep(200 * time.Millisecond)
	if bt := btOf(p); !bt.Enabled || !bt.Discovering {
		t.Fatalf("the older refresh stopped the search: %+v", bt)
	}
}

// A refresh asked for while a switch is unanswered (the system still on)
// does not switch the page back; the one after the answer is shown.
func TestRefreshDuringASwitch(t *testing.T) {
	p, shell, reqs := fakeShell(t)
	r := answerer{t, shell, reqs}
	ready := make(chan struct{})
	p.a.Post(func() { p.bt.Enabled = true; close(ready) })
	<-ready
	p.a.Post(func() { p.action(sdk.ActionEvent{Screen: "bt", Node: "power", Action: "toggle"}) })
	set := r.next(ui.CapBTSet)
	p.a.Post(func() { p.loadRadios(func() {}) })
	r.answer(r.next(ui.CapWifiState), ui.WifiState{})
	r.answer(r.next(ui.CapBTState), ui.BTState{Enabled: true})
	r.answer(r.next(ui.CapRadioState), ui.RadioState{})
	time.Sleep(200 * time.Millisecond)
	if btOf(p).Enabled {
		t.Fatal("a refresh before the system's answer turned Bluetooth back on")
	}
	r.answer(set, map[string]bool{"ok": true})
	r.answer(r.next(ui.CapWifiState), ui.WifiState{})
	r.answer(r.next(ui.CapBTState), ui.BTState{Enabled: false, Name: "after"})
	r.answer(r.next(ui.CapRadioState), ui.RadioState{})
	time.Sleep(200 * time.Millisecond)
	if bt := btOf(p); bt.Enabled || bt.Name != "after" {
		t.Fatalf("the refresh after the answer was not shown: %+v", bt)
	}
}
