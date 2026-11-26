package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// fakePages records what the dialer does; Go requests wait in reqs until
// the test answers them (a slow modem is one never answered in time).
// during is the page on top at each blocking Call, while the system goes
// on handling keys against it; tops is the page on top at each toast.
type fakePages struct {
	stack  []ui.Screen
	toasts []string
	reqs   []*fakeReq
	during []string
	tops   []string
	called []fakeReq
}

type fakeReq struct {
	cap  string
	args any
	out  any
	done func(error)
}

func (f *fakePages) Push(s ui.Screen) { f.stack = append(f.stack, s) }
func (f *fakePages) Replace(s ui.Screen) {
	f.stack[len(f.stack)-1] = s
}
func (f *fakePages) Update(s ui.Screen) {
	for i := range f.stack {
		if f.stack[i].ID == s.ID {
			f.stack[i] = s
		}
	}
}
func (f *fakePages) Reset(s ui.Screen) { f.stack = []ui.Screen{s} }
func (f *fakePages) Pop() {
	if len(f.stack) > 1 {
		f.stack = f.stack[:len(f.stack)-1]
	}
}
func (f *fakePages) Toast(s string) {
	f.toasts = append(f.toasts, s)
	if len(f.stack) > 0 {
		f.tops = append(f.tops, f.top().ID)
	}
}
func (f *fakePages) Call(capName string, args, _ any) error {
	if len(f.stack) > 0 {
		f.during = append(f.during, f.top().ID)
	}
	f.called = append(f.called, fakeReq{cap: capName, args: args})
	return nil
}
func (f *fakePages) Go(capName string, args, out any, done func(error)) {
	f.reqs = append(f.reqs, &fakeReq{cap: capName, args: args, out: out, done: done})
}

// answer answers the oldest request for capName with result (JSON) or err.
func (f *fakePages) answer(t *testing.T, capName string, result any, err error) *fakeReq {
	t.Helper()
	for i, r := range f.reqs {
		if r.cap != capName {
			continue
		}
		f.reqs = append(f.reqs[:i], f.reqs[i+1:]...)
		if result != nil && r.out != nil {
			raw, _ := json.Marshal(result)
			_ = json.Unmarshal(raw, r.out)
		}
		if r.done != nil {
			r.done(err)
		}
		return r
	}
	t.Fatalf("no %s request (have %d)", capName, len(f.reqs))
	return nil
}

func (f *fakePages) top() ui.Screen { return f.stack[len(f.stack)-1] }

func sdkAction(screen, action string) sdk.ActionEvent {
	return sdk.ActionEvent{Screen: screen, Action: action, Fields: map[string]string{}}
}

func text(s ui.Screen) string {
	var b strings.Builder
	for _, n := range s.Body {
		b.WriteString(n.Text + n.Title + "|")
	}
	return b.String()
}

func hasAction(s ui.Screen, id string) bool {
	for _, a := range s.Actions {
		if a.ID == id {
			return true
		}
	}
	return false
}

func newTest() (*dialer, *fakePages) {
	f := &fakePages{}
	d := newDialer(f)
	d.reload(f.Reset)
	return d, f
}

// The call screen shows at once, saying whom it calls, whatever the modem
// takes to answer the dial; hung up before that answer, the call is ended
// as soon as it comes.
func TestDialShowsTheCallAtOnce(t *testing.T) {
	d, f := newTest()
	d.dial("152")
	if top := f.top(); top.ID != "call" || !strings.Contains(text(top), tr.T("Calling %s…", "152")) {
		t.Fatalf("before the modem answered: %q %q", top.ID, text(top))
	}
	if top := f.top(); top.Right != ui.ActionHide || !hasAction(top, ui.ActionHide) || !hasAction(top, ui.ActionHangup) {
		t.Errorf("call screen actions %+v, right %q", top.Actions, top.Right)
	}
	f.answer(t, ui.CapTelDial, map[string]string{"call_id": "c1"}, nil)
	d.callEvent(ui.TelCall{ID: "c1", Number: "152", Dir: "out", State: "alerting"})
	if !strings.Contains(text(f.top()), tr.T("Ringing…")) {
		t.Errorf("alerting: %q", text(f.top()))
	}

	d, f = newTest()
	d.dial("152")
	d.callAction(ui.ActionHangup)
	if f.top().ID != "main" {
		t.Fatalf("hung up before the modem answered: top %q", f.top().ID)
	}
	f.answer(t, ui.CapTelDial, map[string]string{"call_id": "c1"}, nil)
	if r := f.answer(t, ui.CapTelHangup, nil, nil); r.args.(ui.CallIDArgs).CallID != "c1" {
		t.Errorf("hung up %+v", r.args)
	}
	toasts := len(f.toasts)
	d.callEvent(ui.TelCall{ID: "c1", Number: "152", Dir: "out", State: "dialing"})
	d.callEvent(ui.TelCall{ID: "c1", Number: "152", Dir: "out", State: "ended", Reason: "hangup"})
	if len(d.calls) != 0 || f.top().ID != "main" || len(f.toasts) != toasts {
		t.Errorf("the hung-up call came back: calls %v, top %q, toasts %q", d.calls, f.top().ID, f.toasts[toasts:])
	}

	d, f = newTest()
	d.dial("152")
	f.answer(t, ui.CapTelDial, nil, errors.New("the radio is off"))
	if f.top().ID != "main" || len(f.toasts) == 0 || !strings.Contains(f.toasts[len(f.toasts)-1], "the radio is off") {
		t.Errorf("a failed dial: top %q, toasts %q", f.top().ID, f.toasts)
	}
}

// When the last call ends the call screen goes before anything else: the
// end key pressed again (to go to the standby screen) while the dialer
// reloads its lists, or as soon as "Call ended" shows, must not land on
// the ended call's Hang up and leave the user in the dialer.
func TestCallScreenGoesAtOnceWhenTheCallEnds(t *testing.T) {
	d, f := newTest()
	d.dial("13700001234")
	f.answer(t, ui.CapTelDial, map[string]string{"call_id": "c1"}, nil)
	d.callEvent(ui.TelCall{ID: "c1", Number: "13700001234", Dir: "out", State: "active", Connected: 100})
	f.during, f.tops = nil, nil
	d.callEvent(ui.TelCall{ID: "c1", Number: "13700001234", Dir: "out", State: "ended", Reason: "hangup", Connected: 100, Ended: 104})
	if len(f.during) == 0 {
		t.Fatal("the lists were not reloaded after the call")
	}
	for _, id := range append(append([]string{}, f.during...), f.tops...) {
		if id != "main" {
			t.Fatalf("the call screen was still up after the call ended: while reloading %q, at the toast %q", f.during, f.tops)
		}
	}
	if len(f.tops) != 1 || len(f.stack) != 1 || f.top().ID != "main" {
		t.Errorf("toasts at %q, stack of %d with %q on top", f.tops, len(f.stack), f.top().ID)
	}

	d, f = newTest()
	d.dial("152")
	f.tops = nil
	d.callAction(ui.ActionHangup)
	if len(f.tops) != 1 || f.tops[0] != "main" {
		t.Errorf("hung up before the modem answered: toast over %q", f.tops)
	}

	d, f = newTest()
	d.dial("152")
	d.callEvent(ui.TelCall{ID: "c2", Number: "152", Dir: "out", State: "ended", Reason: "busy"})
	f.tops = nil
	f.answer(t, ui.CapTelDial, map[string]string{"call_id": "c2"}, nil)
	if len(f.tops) != 1 || f.tops[0] != "main" {
		t.Errorf("ended before the dial's answer: toast over %q", f.tops)
	}
}

// A call answered on the system's incoming screen shows as being answered
// until the modem connects it, not the main page; one that fails, answered
// or dialled, says why.
func TestAnsweredAndFailedCalls(t *testing.T) {
	d, f := newTest()
	in := ui.TelCall{ID: "c3", Number: "13900001111", Dir: "in", State: "incoming"}
	d.callEvent(in)
	if len(d.calls) != 0 || f.top().ID != "main" {
		t.Fatalf("a ringing call is the system's: calls %v, top %q", d.calls, f.top().ID)
	}
	d.open("c3")
	f.answer(t, ui.CapTelCalls, []ui.TelCall{in}, nil)
	if top := f.top(); top.ID != "call" || !strings.Contains(text(top), tr.T("Answering…")) {
		t.Fatalf("answered, not connected yet: %q %q", top.ID, text(top))
	}
	in.State, in.Reason, in.Error = "ended", "failed", "answering failed: modem busy"
	d.callEvent(in)
	if f.top().ID != "main" || len(f.toasts) == 0 || f.toasts[len(f.toasts)-1] != tr.T("Call failed: %s", in.Error) {
		t.Errorf("the answer failed: top %q, toasts %q", f.top().ID, f.toasts)
	}
	// The id again after telephonyd restarted: a call ringing, not answered.
	d.callEvent(ui.TelCall{ID: "c3", Number: "13900002222", Dir: "in", State: "incoming"})
	if len(d.calls) != 0 || f.top().ID != "main" {
		t.Errorf("a new call with the failed one's id: calls %v, top %q", d.calls, f.top().ID)
	}

	d, f = newTest()
	in = ui.TelCall{ID: "c4", Number: "13900001111", Dir: "in", State: "incoming"}
	d.open("c4")
	f.answer(t, ui.CapTelCalls, []ui.TelCall{in}, nil)
	in.State, in.Connected = "active", 5
	d.callEvent(in)
	if top := f.top(); top.ID != "call" || !strings.Contains(text(top), tr.T("In call")) {
		t.Errorf("connected: %q %q", top.ID, text(top))
	}

	d, f = newTest()
	d.dial("152")
	f.answer(t, ui.CapTelDial, map[string]string{"call_id": "c5"}, nil)
	d.callEvent(ui.TelCall{ID: "c5", Number: "152", Dir: "out", State: "ended", Reason: "failed", Error: "the modem did not place the call: no service"})
	if f.top().ID != "main" || len(f.toasts) == 0 || !strings.Contains(f.toasts[len(f.toasts)-1], "the modem did not place the call: no service") {
		t.Errorf("a dial the modem failed: top %q, toasts %q", f.top().ID, f.toasts)
	}
}

// Events for the call may come before the dial's answer.
func TestEventsBeforeTheDialAnswer(t *testing.T) {
	d, f := newTest()
	d.dial("10086")
	d.callEvent(ui.TelCall{ID: "c7", Number: "10086", Dir: "out", State: "dialing"})
	d.callEvent(ui.TelCall{ID: "c7", Number: "10086", Dir: "out", State: "alerting"})
	f.answer(t, ui.CapTelDial, map[string]string{"call_id": "c7"}, nil)
	if c := d.shown(); c == nil || c.ID != "c7" || c.State != "alerting" || len(d.calls) != 1 {
		t.Fatalf("shown %+v, calls %v", c, d.calls)
	}
}

// Digits typed during a call (the system opens the dialer with them) start
// a second call's page over the call screen; dialling from it puts the
// call screen back with both calls, and Swap resumes the held one.
func TestSecondCall(t *testing.T) {
	d, f := newTest()
	d.open("c1")
	f.answer(t, ui.CapTelCalls, []ui.TelCall{{ID: "c1", Number: "152", Dir: "out", State: "active", Connected: 1}}, nil)
	if f.top().ID != "call" {
		t.Fatalf("open: top %q", f.top().ID)
	}
	if !hasAction(f.top(), "new") || !hasAction(f.top(), dtmfPrefix+"5") {
		t.Errorf("connected call's actions: %+v", f.top().Actions)
	}
	d.link("dial:5")
	if top := f.top(); top.ID != screenAdd || top.Body[1].Value != "5" || len(f.stack) != 3 {
		t.Fatalf("digits during the call: %d pages, top %q %+v", len(f.stack), top.ID, top.Body)
	}
	d.dial("10086")
	if len(f.stack) != 2 || f.top().ID != "call" || !strings.Contains(text(f.top()), tr.T("Calling %s…", "10086")) {
		t.Fatalf("second call: %d pages, top %q %q", len(f.stack), f.top().ID, text(f.top()))
	}
	f.answer(t, ui.CapTelDial, map[string]string{"call_id": "c2"}, nil)
	d.callEvent(ui.TelCall{ID: "c1", Number: "152", Dir: "out", State: "held", Connected: 1})
	d.callEvent(ui.TelCall{ID: "c2", Number: "10086", Dir: "out", State: "active", Connected: 2})
	if c := d.shown(); c.ID != "c2" || !hasAction(f.top(), "swap") || hasAction(f.top(), "new") {
		t.Fatalf("two calls: shown %+v, actions %+v", c, f.top().Actions)
	}
	if !strings.Contains(text(f.top()), "152") {
		t.Errorf("the held call is not on the call screen: %q", text(f.top()))
	}
	d.callAction("swap")
	if r := f.answer(t, ui.CapTelHold, nil, nil); r.args.(ui.CallIDArgs) != (ui.CallIDArgs{CallID: "c1"}) {
		t.Errorf("swap asked %+v", r.args)
	}
	// The active call ends: the held one stays on the call screen.
	d.callEvent(ui.TelCall{ID: "c2", Number: "10086", Dir: "out", State: "ended", Reason: "remote", Connected: 2, Ended: 9})
	if f.top().ID != "call" || d.shown().ID != "c1" || f.toasts[len(f.toasts)-1] != tr.T("Call with %s ended %s", "10086", "00:07") {
		t.Fatalf("one of two ended: top %q, shown %+v, toasts %q", f.top().ID, d.shown(), f.toasts)
	}
	d.callEvent(ui.TelCall{ID: "c1", Number: "152", Dir: "out", State: "ended", Reason: "remote", Connected: 1, Ended: 61})
	if f.top().ID != "main" || len(f.stack) != 1 || f.toasts[len(f.toasts)-1] != tr.T("The other party hung up %s", "01:00") {
		t.Errorf("both ended: %d pages, top %q, toasts %q", len(f.stack), f.top().ID, f.toasts)
	}
}

// No second call while the first is still connecting, with two already or
// during an emergency call: the dialer says why and stays on the call.
func TestNoSecondCall(t *testing.T) {
	for _, tc := range []struct {
		calls []ui.TelCall
		want  string
	}{
		{[]ui.TelCall{{ID: "c1", Dir: "out", State: "alerting"}}, tr.T("The call is still connecting")},
		{[]ui.TelCall{{ID: "c1", State: "held"}, {ID: "c2", State: "active"}}, tr.T("Two calls already")},
		{[]ui.TelCall{{ID: "c1", Number: "112", State: "active", Emergency: true}}, tr.T("An emergency call is in progress")},
	} {
		d, f := newTest()
		d.open("c1")
		f.answer(t, ui.CapTelCalls, tc.calls, nil)
		d.link("dial:5")
		if f.top().ID != "call" || len(f.toasts) == 0 || f.toasts[len(f.toasts)-1] != tc.want {
			t.Errorf("%v: top %q, toasts %q", tc.calls, f.top().ID, f.toasts)
		}
		d.dial("10086")
		if len(f.reqs) != 0 {
			t.Errorf("%v: dialled anyway (%+v)", tc.calls, f.reqs[0])
		}
	}
}

// Keypad tones go into the connected call; the call screen's letters leave
// the full keyboard's Fn digits (w e r s d f z x c, q a) to the tones.
func TestCallScreenKeys(t *testing.T) {
	d, f := newTest()
	d.open("c1")
	f.answer(t, ui.CapTelCalls, []ui.TelCall{{ID: "c1", Number: "10086", State: "active", Connected: 1}}, nil)
	for _, a := range f.top().Actions {
		if a.Key != "" && !a.Hidden && strings.ContainsAny(a.Key, "wersdfzxcqa") {
			t.Errorf("%s takes %q, a digit with Fn", a.ID, a.Key)
		}
	}
	d.action(sdkAction("call", dtmfPrefix+"1"))
	if r := f.answer(t, ui.CapTelDTMF, nil, nil); r.args.(ui.DTMFArgs) != (ui.DTMFArgs{CallID: "c1", Tones: "1"}) {
		t.Errorf("tones %+v", r.args)
	}
	// The green key on the empty number field of the main page during a
	// call (the system's fallback) goes back to the call.
	d.onCall = false
	f.stack = f.stack[:1]
	d.dial("")
	if f.top().ID != "call" {
		t.Errorf("empty number during a call: top %q", f.top().ID)
	}
}

// A call from the log can be blocked (the system's block list); a blocked
// call reads as blocked, and its number can be unblocked.
func TestBlockFromTheLog(t *testing.T) {
	f := &fakePages{}
	d := newDialer(f)
	d.log = []ui.TelLogEntry{{Number: "13700000001", Dir: "in", Result: "blocked", At: 1}, {Number: "13700000002", Dir: "in", Result: "missed", At: 1}}
	d.blocks = ui.BlockList{Numbers: []string{ui.NumberKey("13700000001")}}
	s := d.mainScreen()
	var blockedRow, otherRow ui.Node
	for _, n := range s.Body {
		switch n.ID {
		case "log:0":
			blockedRow = n
		case "log:1":
			otherRow = n
		}
	}
	has := func(n ui.Node, id string) bool {
		for _, a := range n.Actions {
			if a.ID == id {
				return true
			}
		}
		return false
	}
	if !strings.HasPrefix(blockedRow.Subtitle, tr.T("Blocked")) || !has(blockedRow, "unblock") || has(blockedRow, "block") {
		t.Fatalf("blocked row %+v", blockedRow)
	}
	if !has(otherRow, "block") {
		t.Fatalf("other row %+v", otherRow)
	}
	f.Reset(s)
	d.action(sdk.ActionEvent{Screen: "main", Node: "log:1", Action: "block"})
	var set *fakeReq
	for i := range f.called {
		if f.called[i].cap == ui.CapBlockSet {
			set = &f.called[i]
		}
	}
	if set == nil || set.args.(ui.BlockSetArgs).Number != "13700000002" || !set.args.(ui.BlockSetArgs).On {
		t.Fatalf("block.set %+v", f.called)
	}
}
