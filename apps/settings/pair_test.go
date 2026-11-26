package main

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wire"
)

// fakeBT plays the Shell's Bluetooth and page stack for the app: bt.state
// answers st, bt.set's discover goes into searches, bt.pair waits in pairs
// for the test to answer, the rest gets an empty answer; pages are pushed
// and popped as the Shell would (back: the user's Back).
type fakeBT struct {
	shell    *wire.Conn
	pairs    chan ui.Msg
	mu       sync.Mutex
	st       ui.BTState
	searches []bool
	sets     []ui.BTArgs
	stack    []string
	toasts   []string
}

func (f *fakeBT) answer(m ui.Msg, v any, errText string) {
	data, _ := json.Marshal(v)
	_ = f.shell.Send(ui.Msg{T: ui.MsgRes, Req: m.Req, OK: errText == "", Err: errText, Data: data})
}

func (f *fakeBT) serve(reqs <-chan ui.Msg) {
	for m := range reqs {
		switch m.Cap {
		case ui.CapBTState:
			f.mu.Lock()
			st := f.st
			f.mu.Unlock()
			f.answer(m, st, "")
		case ui.CapBTSet:
			var a ui.BTArgs
			_ = json.Unmarshal(m.Args, &a)
			f.mu.Lock()
			f.sets = append(f.sets, a)
			if a.Enabled != nil {
				f.st.Enabled = *a.Enabled
			}
			if a.Discover != nil {
				f.searches = append(f.searches, *a.Discover)
				f.st.Discovering = *a.Discover
			}
			f.mu.Unlock()
			f.answer(m, map[string]bool{"ok": true}, "")
		case ui.CapBTPair:
			f.pairs <- m
		default:
			f.answer(m, struct{}{}, "")
		}
	}
}

func (f *fakeBT) pages(others <-chan ui.Msg) {
	for m := range others {
		f.mu.Lock()
		switch m.T {
		case ui.MsgReset:
			f.stack = []string{m.Screen.ID}
		case ui.MsgPush:
			f.stack = append(f.stack, m.Screen.ID)
		case ui.MsgPop:
			f.popLocked()
		case ui.MsgToast:
			f.toasts = append(f.toasts, m.Text)
		}
		f.mu.Unlock()
	}
}

func (f *fakeBT) popLocked() {
	if n := len(f.stack); n > 1 {
		id := f.stack[n-1]
		f.stack = f.stack[:n-1]
		_ = f.shell.Send(ui.Msg{T: ui.MsgPopped, ScreenID: id})
	}
}

func (f *fakeBT) back() {
	f.mu.Lock()
	f.popLocked()
	f.mu.Unlock()
}

func (f *fakeBT) top() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stack[len(f.stack)-1]
}

// Pair new device searches while it is on top in the foreground, as
// Android's page to pair a device does: from opening it, not in the
// background or under another page, again on coming back; a system dialog
// over it leaves the search on, and the keys coming back ask for it again
// (the system stops it with the screen off); picking a device stops it, a
// pairing that fails starts it again, one that works closes the page;
// leaving the page stops it. The Bluetooth page itself never searches;
// opening the page with Bluetooth off turns it on.
func TestPairPageSearchesWhileInFront(t *testing.T) {
	others := make(chan ui.Msg, 64)
	p, shell, reqs := fakeShellWith(t, others)
	f := &fakeBT{shell: shell, pairs: make(chan ui.Msg, 1), st: ui.BTState{Backend: "test", Enabled: true,
		Devices: []ui.BTDevice{{Address: "AA:01", Name: "Car kit"}, {Address: "BB:02", Name: "Earbuds", Paired: true}}}}
	go f.serve(reqs)
	go f.pages(others)
	p.a.OnPopped = p.onPopped
	on := func(do func()) {
		done := make(chan struct{})
		p.a.Post(func() { do(); close(done) })
		<-done
	}
	var want []bool
	expect := func(what string, more ...bool) {
		t.Helper()
		want = append(want, more...)
		for end := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			f.mu.Lock()
			got := slices.Clone(f.searches)
			f.mu.Unlock()
			if slices.Equal(got, want) {
				return
			}
			if time.Now().After(end) {
				t.Fatalf("%s: searches asked %v, want %v", what, got, want)
			}
		}
	}
	settle := func(what string) {
		t.Helper()
		time.Sleep(300 * time.Millisecond)
		expect(what)
	}

	on(func() {
		p.bt, p.radioPage = f.st, "bt"
		p.a.Reset(p.root())
		p.a.Push(p.btScreen())
		p.btFront(true)
	})
	settle("the Bluetooth page")
	var bt ui.Screen
	on(func() { bt = p.btScreen() })
	for _, n := range bt.Body {
		if n.ID == "scan" || n.ID == "dev:AA:01" {
			t.Fatalf("the Bluetooth page searches or lists a device nearby: %+v", n)
		}
	}
	if !slices.ContainsFunc(bt.Body, func(n ui.Node) bool { return n.ID == "pairnew" }) {
		t.Fatalf("no Pair new device: %+v", bt.Body)
	}

	on(func() { p.action(sdk.ActionEvent{Screen: "bt", Node: "pairnew", Action: ui.ActionOpen}) })
	expect("opened", true)
	var page ui.Screen
	on(func() { p.bt = f.st; page = p.btPairScreen() })
	if !slices.ContainsFunc(page.Body, func(n ui.Node) bool { return n.ID == "dev:AA:01" && len(n.Actions) == 1 && n.Actions[0].ID == "pair" }) ||
		slices.ContainsFunc(page.Body, func(n ui.Node) bool { return n.ID == "dev:BB:02" }) {
		t.Fatalf("Pair new device lists %+v", page.Body)
	}
	on(func() { p.btFocus(true) })
	settle("a system dialog over it")
	on(func() { p.btFocus(false) })
	expect("the keys back (the system may have stopped it with the screen off)", true)
	on(func() { p.btFront(false) })
	expect("the app in the background", false)
	on(func() { p.btFront(true) })
	expect("the app in front again", true)
	on(func() { p.a.Push(ui.Screen{ID: "perms"}); p.pairSearch() })
	expect("another page on top", false)
	f.back()
	expect("back on the page", true)

	on(func() { p.action(sdk.ActionEvent{Screen: btPairPage, Node: "dev:AA:01", Action: "pair"}) })
	expect("a device picked", false)
	pair := <-f.pairs
	f.answer(pair, nil, "Pairing failed: rejected")
	expect("the pairing failed", true)
	f.mu.Lock()
	toasts := slices.Clone(f.toasts)
	f.mu.Unlock()
	if !slices.ContainsFunc(toasts, func(s string) bool { return strings.Contains(s, "rejected") }) {
		t.Errorf("the failure was not shown: %q", toasts)
	}
	on(func() { p.action(sdk.ActionEvent{Screen: btPairPage, Node: "dev:AA:01", Action: "pair"}) })
	expect("picked again", false)
	f.mu.Lock()
	f.st.Devices[0].Paired = true
	f.mu.Unlock()
	f.answer(<-f.pairs, map[string]bool{"ok": true}, "")
	settle("paired")
	if top := f.top(); top != "bt" {
		t.Fatalf("paired, the top page is %q, want the Bluetooth page", top)
	}

	on(func() { p.action(sdk.ActionEvent{Screen: "bt", Node: "pairnew", Action: ui.ActionOpen}) })
	expect("opened again", true)
	f.back()
	expect("left with Back", false)

	f.mu.Lock()
	f.st.Enabled = false
	f.sets = nil
	f.mu.Unlock()
	on(func() {
		p.bt = f.st
		p.action(sdk.ActionEvent{Screen: "bt", Node: "pairnew", Action: ui.ActionOpen})
	})
	expect("opened with Bluetooth off", true)
	f.mu.Lock()
	sets := slices.Clone(f.sets)
	f.mu.Unlock()
	if len(sets) == 0 || sets[0].Enabled == nil || !*sets[0].Enabled || sets[0].Discover == nil || !*sets[0].Discover {
		t.Fatalf("opened with Bluetooth off: asked %+v, want it on and searching", sets)
	}
}
