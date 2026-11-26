package shell

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kario-os/kario/obex/btphone"
	"github.com/kario-os/kario/ui"
)

// btOldPhone pairs a simulated old phone of that shape with the phone and
// gives its address.
func (st *switchTest) btOldPhone(shape, name string) (*btphone.Phone, string) {
	st.t.Helper()
	ph, err := btphone.New(shape)
	if err != nil {
		st.t.Fatal(err)
	}
	b, ok := st.c.bt.(*simBT)
	if !ok {
		b = &simBT{enabled: true, confirm: func(string, uint32) bool { return true }, serve: st.c.obexServe, peerBox: &st.c.radios.obex}
		st.c.bt = b
	}
	b.addOldPhone(name, ph)
	addr := b.devices[len(b.devices)-1].Address
	if err := b.pair(addr); err != nil {
		st.t.Fatal(err)
	}
	return ph, addr
}

// moveBT starts a move over Bluetooth and waits until it has ended.
func (st *switchTest) moveBT(addr string, kinds []string) ui.SwitchState {
	st.t.Helper()
	s, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: kinds, Bluetooth: addr})
	if err != nil {
		st.t.Fatalf("start: %v", err)
	}
	if s.Bluetooth == "" || s.Code != "" || st.c.hotspot.on {
		st.t.Fatalf("started: %+v (hotspot %v)", s, st.c.hotspot.on)
	}
	st.moveEnded()
	s, _ = st.call(ui.CapSwitchState, nil)
	return s
}

// moveEnded runs the loop until the move over Bluetooth has ended: a
// whole move is many trips through it, longer than pump waits under the
// race detector on a busy machine.
func (st *switchTest) moveEnded() {
	st.t.Helper()
	end := time.After(30 * time.Second)
	for st.c.sw.bt != nil {
		select {
		case f := <-st.c.ev:
			f()
		case <-time.After(5 * time.Millisecond):
		case <-end:
			st.t.Fatal("the move did not end")
		}
	}
}

func added(s ui.SwitchState) map[string]ui.SwitchProgress {
	out := map[string]ui.SwitchProgress{}
	if s.Last != nil {
		for _, p := range s.Last.Added {
			out[p.Kind] = p
		}
	}
	return out
}

// An Android phone over Bluetooth: its contacts without its own card, its
// calls (a withheld number fails), its inbox and sent messages (an MMS's
// text) land in the stores through the switch's sink, the record keeps
// IDs only, and undo takes them out; moved again, what Kario has is not
// added twice.
func TestSwitchBluetoothAndroid(t *testing.T) {
	st := newSwitchTest(t)
	c := st.c
	ph, addr := st.btOldPhone("android", "Redmi Note 9")
	before := c.contactBook().Len()
	s := st.moveBT(addr, ui.SwitchKinds)
	a := added(s)
	if s.State != ui.SwitchIdle || s.Bluetooth != "" || s.Last == nil || s.Last.Device != "Redmi Note 9" || s.Last.Ended != ui.SwitchEndedDone ||
		a[ui.SwitchContacts].Stored != 3 || a[ui.SwitchCallLog].Stored != 3 || a[ui.SwitchSMS].Stored != 4 || s.Last.Failed != 1 || len(a) != 3 {
		t.Fatalf("after: %+v %+v", s, s.Last)
	}
	book := c.contactBook()
	if x, ok := book.ByNumber("13655550202"); !ok || x.Name != "\u4e0a\u5b98\u71d5" || !strings.Contains(x.Notes, "\u676d\u5dde\u897f\u6e56\u533a") || book.Len() != before+3 {
		t.Fatalf("contact: %+v %v (%d)", x, ok, book.Len()-before)
	}
	if _, ok := book.ByNumber("+8613800138099"); ok {
		t.Fatal("the old phone's own card came in")
	}
	got := st.svcs.got()
	at := func(s string) int64 { x, _ := time.ParseInLocation("20060102T150405", s, c.zone()); return x.Unix() }
	calls := map[string]string{}
	for _, e := range got.calls {
		calls[e.Number] = e.Dir + " " + e.Result
		if e.Number == "+8613655550201" && e.At != at("20261116T201500") {
			t.Errorf("call time %d", e.At)
		}
	}
	if len(got.calls) != 3 || calls["+8613655550201"] != "in answered" || calls["10086"] != "out outgoing" || calls["13655550202"] != "in missed" {
		t.Fatalf("calls: %+v", got.calls)
	}
	sms := map[string]ui.SMS{}
	for _, m := range got.sms {
		sms[m.Dir+" "+m.Peer+" "+strings.SplitN(m.Text, "\n", 2)[0]] = m
	}
	in := sms["in +8613655550201 \u5468\u65e5\u4e00\u8d77\u53bb\u897f\u6e56\u5417\uff1f"]
	if len(got.sms) != 4 || in.Text != "\u5468\u65e5\u4e00\u8d77\u53bb\u897f\u6e56\u5417\uff1f\n\u65e9\u4e0a\u516b\u70b9\u51fa\u53d1" || !in.Read || in.At != at("20261116T193000") {
		t.Fatalf("sms: %+v", got.sms)
	}
	if m, ok := sms["in 1065555000 \u5feb\u9012\u5230\u4e86\uff0c\u653e\u5728\u95e8\u536b"]; !ok || m.Read || m.At != time.Date(2026, 11, 16, 2, 15, 0, 0, time.UTC).Unix() {
		t.Errorf("unread, with a UTC offset: %+v", got.sms)
	}
	if _, ok := sms["in 136-5555-0202 \u770b\u770b\u8fd9\u5f20\uff0c\u53bb\u5e74\u5728\u676d\u5dde\u62cd\u7684"]; !ok {
		t.Errorf("the MMS's text: %+v", got.sms)
	}
	if m, ok := sms["out +8613655550201 \u597d\u7684\uff0c\u4e5d\u70b9\u897f\u6e56\u8fb9\u89c1"]; !ok || !m.Read {
		t.Errorf("sent: %+v", got.sms)
	}
	if !slices.Equal(ph.Asked()[:1], []string{"pbap CONNECT"}) || !slices.Contains(ph.Asked(), "map CONNECT") {
		t.Errorf("asked %v", ph.Asked())
	}
	if l := st.log.String(); strings.Contains(l, "13655550202") || strings.Contains(l, "\u4e0a\u5b98") || strings.Contains(l, "\u897f\u6e56") {
		t.Fatalf("content in the log:\n%s", l)
	}

	s, err := st.call(ui.CapSwitchUndo, nil)
	if err != nil || s.Last == nil || !s.Last.Undone || c.contactBook().Len() != before {
		t.Fatalf("undo: %+v %v", s, err)
	}
	if got := st.svcs.got(); len(got.smsGone) != 4 || len(got.callsGone) != 3 {
		t.Fatalf("undone: sms %v, calls %v", got.smsGone, got.callsGone)
	}

	s = st.moveBT(addr, []string{ui.SwitchContacts})
	if a := added(s); a[ui.SwitchContacts].Stored != 3 {
		t.Fatalf("again after the undo: %+v", s.Last)
	}
	s = st.moveBT(addr, []string{ui.SwitchContacts})
	if s.Last == nil || s.Last.Undone || added(s)[ui.SwitchContacts].Stored != 3 || !strings.Contains(s.Error, "Redmi Note 9") {
		t.Fatalf("a third time, all there already: %+v %+v", s, s.Last)
	}
}

// An iPhone gives its contacts (no own card to leave out) and calls; its
// message server lists nothing and has no sent folder, which the summary
// says.
func TestSwitchBluetoothIPhone(t *testing.T) {
	st := newSwitchTest(t)
	_, addr := st.btOldPhone("iphone", "iPhone 12")
	s := st.moveBT(addr, ui.SwitchKinds)
	a := added(s)
	if a[ui.SwitchContacts].Stored != 2 || a[ui.SwitchCallLog].Stored != 3 || a[ui.SwitchSMS].Stored != 0 || a[ui.SwitchSMS].Note != ui.SwitchNotePartial {
		t.Fatalf("after: %+v", s.Last)
	}
	if x, ok := st.c.contactBook().ByNumber("+14085550302"); !ok || x.Name != "Mia Wong" || len(x.Numbers) != 2 {
		t.Fatalf("contact %+v %v", x, ok)
	}
}

// The old phone's user decides: while it asks the move is waiting; a no
// to the phone book leaves contacts and calls out, saying so, and the
// messages still come.
func TestSwitchBluetoothConsent(t *testing.T) {
	st := newSwitchTest(t)
	ph, addr := st.btOldPhone("android", "Redmi Note 9")
	asked := make(chan string, 2)
	answers := make(chan bool, 2)
	ph.Ask = func(svc string) bool { asked <- svc; return <-answers }
	s, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: ui.SwitchKinds, Bluetooth: addr})
	if err != nil {
		t.Fatal(err)
	}
	st.pump("the question", func() bool { return len(asked) == 1 })
	st.pump("waiting", func() bool { s, _ = st.call(ui.CapSwitchState, nil); return s.State == ui.SwitchWaiting })
	if s.Bluetooth != "Redmi Note 9" || len(s.Progress) != 3 {
		t.Fatalf("waiting: %+v", s)
	}
	answers <- false
	answers <- true
	st.moveEnded()
	s, _ = st.call(ui.CapSwitchState, nil)
	a := added(s)
	if a[ui.SwitchContacts].Note != ui.SwitchNoteRefused || a[ui.SwitchCallLog].Note != ui.SwitchNoteRefused || a[ui.SwitchSMS].Stored != 4 {
		t.Fatalf("after: %+v", s.Last)
	}
}

// Stopping while the old phone asks ends the move at once: nothing came,
// so there is nothing to undo, and the link goes.
func TestSwitchBluetoothStop(t *testing.T) {
	st := newSwitchTest(t)
	c := st.c
	ph, addr := st.btOldPhone("android", "Redmi Note 9")
	release := make(chan bool)
	t.Cleanup(func() { close(release) })
	ph.Ask = func(string) bool { return <-release }
	if _, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: ui.SwitchKinds, Bluetooth: addr}); err != nil {
		t.Fatal(err)
	}
	st.pump("waiting", func() bool { s, _ := st.call(ui.CapSwitchState, nil); return s.State == ui.SwitchWaiting })
	s, _ := st.call(ui.CapSwitchStop, nil)
	if s.State != ui.SwitchIdle || s.Bluetooth != "" || s.Last != nil || c.sw.bt != nil {
		t.Fatalf("stopped: %+v", s)
	}
	b := c.bt.(*simBT)
	st.pump("the link closed", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.open) == 0
	})
	if _, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: []string{ui.SwitchCalendar}, Bluetooth: addr}); err == nil {
		t.Fatal("a move of what Bluetooth does not give")
	}
}

// A device without the services (a headset), one not paired.
func TestSwitchBluetoothNotOffered(t *testing.T) {
	st := newSwitchTest(t)
	_, addr := st.btOldPhone("android", "Redmi Note 9")
	b := st.c.bt.(*simBT)
	b.addPeer("Kario headset", false)
	headset := b.devices[len(b.devices)-1].Address
	if _, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: ui.SwitchKinds, Bluetooth: headset}); err == nil {
		t.Fatal("an unpaired device")
	}
	if err := b.pair(headset); err != nil {
		t.Fatal(err)
	}
	s := st.moveBT(headset, ui.SwitchKinds)
	if s.Last != nil || !strings.Contains(s.Error, "Kario headset") {
		t.Fatalf("headset: %+v", s)
	}
	st.c.sms.up = false
	s = st.moveBT(addr, ui.SwitchKinds)
	if a := added(s); a[ui.SwitchSMS].Kind != "" || a[ui.SwitchContacts].Stored != 3 {
		t.Fatalf("without phone-smsd: %+v", s.Last)
	}
}
