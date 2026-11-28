package nfcd

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kario-os/kario/hal/nfc"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

const (
	transitAID = "F04651545241"
	payAID     = "F0465150415901"
)

// world is nfcd with a simulated controller and a stand-in for the shell
// and the apps: the wallet's transit card and payment card answer the
// way apps/wallet does.
type world struct {
	t       *testing.T
	sim     *nfc.Sim
	e       *engine
	mu      sync.Mutex
	results []svc.NFCEvent
	fields  []bool
	// left: the apps told the reader is done with their card; ended: the
	// reader sessions that ended.
	left    []svc.NFCEvent
	ended   []svc.NFCEvent
	balance int
	silent  bool
	cmds    int
}

func newWorld(t *testing.T, dir string) *world {
	sim := nfc.NewSim()
	return newWorldOn(t, dir, sim, sim)
}

// newWorldOn runs the engine on ctl, which is sim or wraps it.
func newWorldOn(t *testing.T, dir string, sim *nfc.Sim, ctl nfc.Controller) *world {
	w := &world{t: t, sim: sim, balance: 2000}
	events := make(chan svc.NFCEvent, 256)
	w.e = newEngine(ctl, dir, func(ev svc.NFCEvent) { events <- ev })
	go w.e.run()
	go func() {
		for ev := range events {
			switch ev.Event {
			case svc.NFCEventResult:
				w.mu.Lock()
				w.results = append(w.results, ev)
				w.mu.Unlock()
				continue
			case svc.NFCEventField:
				w.mu.Lock()
				w.fields = append(w.fields, ev.On)
				w.mu.Unlock()
				continue
			case svc.NFCEventDeactivated:
				w.mu.Lock()
				w.left = append(w.left, ev)
				w.mu.Unlock()
				continue
			case svc.NFCEventReaderEnd:
				w.mu.Lock()
				w.ended = append(w.ended, ev)
				w.mu.Unlock()
				continue
			}
			w.mu.Lock()
			w.cmds++
			silent := w.silent
			w.mu.Unlock()
			if silent {
				continue
			}
			apdu, _ := hex.DecodeString(ev.APDU)
			resp := w.card(ev.Card, apdu)
			w.e.mu.Lock()
			if err := w.e.reply(ev.Session, ev.App, strings.ToUpper(hex.EncodeToString(resp))); err != nil {
				t.Errorf("reply: %v", err)
			}
			w.e.mu.Unlock()
		}
	}()
	return w
}

func (w *world) card(card string, apdu []byte) []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	if nfc.SelectAID(apdu) != nil {
		if hex.EncodeToString(nfc.SelectAID(apdu)) == strings.ToLower(ui.PPSE) {
			aid, _ := hex.DecodeString(payAID)
			entry := append([]byte{0x61, byte(2 + len(aid)), 0x4F, byte(len(aid))}, aid...)
			return append(append([]byte{0xA5, byte(len(entry))}, entry...), nfc.SWOK...)
		}
		return nfc.SWOK
	}
	switch {
	case bytes.Equal(apdu, nfc.GetBalance):
		return append(binary.BigEndian.AppendUint32(nil, uint32(w.balance)), nfc.SWOK...)
	case apdu[1] == 0x54:
		fare := int(binary.BigEndian.Uint32(apdu[5:9]))
		if fare > w.balance {
			return nfc.SWInsufficient
		}
		w.balance -= fare
		return append(binary.BigEndian.AppendUint32(nil, uint32(w.balance)), nfc.SWOK...)
	case apdu[1] == 0x70:
		return append(bytes.Repeat([]byte{1}, 8), nfc.SWOK...)
	}
	return nfc.SWUnknown
}

func (w *world) policy(locked, screenOn bool, cards ...svc.NFCAppCard) {
	w.e.mu.Lock()
	defer w.e.mu.Unlock()
	p := w.e.pol
	p.Locked, p.ScreenOn, p.Cards = locked, screenOn, cards
	w.e.setPolicy(p)
}

func (w *world) call(method string, params any) (any, error) {
	raw, _ := json.Marshal(params)
	return w.e.handle(method, raw)
}

func (w *world) must(method string, params any) any {
	w.t.Helper()
	out, err := w.call(method, params)
	if err != nil {
		w.t.Fatalf("%s %+v: %v", method, params, err)
	}
	return out
}

// read holds a card to the phone and reads it, as the card pack does.
func (w *world) read(uid string, encrypted, cpu bool) ui.NFCRead {
	w.t.Helper()
	b, _ := hex.DecodeString(uid)
	t := &nfc.Tag{UID: b, SAK: 0x08, Encrypted: encrypted}
	if cpu {
		t.SAK, t.ISODep = 0x20, true
	}
	w.sim.Hold(t)
	st := w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetRead, Seconds: 1}).(ui.NFCState)
	w.sim.Hold(nil)
	if st.Read == nil {
		w.t.Fatal("no read in the answer")
	}
	return *st.Read
}

// door adds a door card by reading it.
func (w *world) door(name, uid string) ui.NFCCardInfo {
	w.t.Helper()
	if r := w.read(uid, false, false); r.Result != ui.NFCReadOK {
		w.t.Fatalf("read %s: %+v", uid, r)
	}
	st := w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetAddDoor, Name: name}).(ui.NFCState)
	for _, c := range st.Cards {
		if c.Name == name {
			return c
		}
	}
	w.t.Fatalf("%s is not in %+v", name, st.Cards)
	return ui.NFCCardInfo{}
}

func (w *world) tap(t *testing.T, a svc.NFCTapArgs, ok bool, name string) svc.NFCEvent {
	t.Helper()
	w.mu.Lock()
	n := len(w.results)
	w.mu.Unlock()
	a.PatienceMS = 500
	out := w.must(svc.NFCSimTap, a).(svc.NFCTapRes)
	if out.OK != ok || (name != "" && out.Name != name) {
		t.Fatalf("tap %s: ok %v at %q (%s), want ok %v at %q", a.Name, out.OK, out.Name, out.Note, ok, name)
	}
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		w.mu.Lock()
		if len(w.results) > n {
			r := w.results[len(w.results)-1]
			w.mu.Unlock()
			return r
		}
		w.mu.Unlock()
	}
	return svc.NFCEvent{}
}

// at sets the shell's clock to t through the policy (its skew and zone).
func (w *world) at(t time.Time) {
	w.e.mu.Lock()
	defer w.e.mu.Unlock()
	p := w.e.pol
	_, off := t.Zone()
	p.ClockSkewMS, p.ZoneOffset = t.Sub(w.e.now()).Milliseconds(), off
	w.e.setPolicy(p)
}

var walletCards = []svc.NFCAppCard{
	{App: "com.phone.wallet", AppName: "Feiqian", Card: "transit", Name: "Feiqian transit card", Category: ui.NFCOther, AIDs: []string{transitAID}, Express: true},
	{App: "com.phone.wallet", AppName: "Feiqian", Card: "pay", Name: "Feiqian pay", Category: ui.NFCPayment, AIDs: []string{ui.PPSE, payAID}},
}

var beijing = time.FixedZone("CST", 8*3600)

func TestReadAndAddDoorCards(t *testing.T) {
	w := newWorld(t, t.TempDir())
	w.policy(false, true, walletCards...)
	if r := w.read("04112233", true, false); r.Result != ui.NFCReadEncrypted {
		t.Fatalf("encrypted card: %+v", r)
	}
	if r := w.read("04223344", false, true); r.Result != ui.NFCReadSmart {
		t.Fatalf("CPU card: %+v", r)
	}
	if _, err := w.call(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetAddDoor, Name: "x"}); err == nil {
		t.Fatal("added a card that could not be copied")
	}
	st := w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetRead, Seconds: 1}).(ui.NFCState)
	if st.Read == nil || st.Read.Result != ui.NFCReadNone {
		t.Fatalf("no card held: %+v", st.Read)
	}
	home := w.door("Home", "04A1B2C3")
	if home.UID != "C3" || home.ID != "door:1" {
		t.Fatalf("home: %+v", home)
	}
	if r := w.read("04a1b2c3", false, false); r.Result != ui.NFCReadDuplicate || r.Name != "Home" {
		t.Fatalf("the same card again: %+v", r)
	}
	office := w.door("Office", "04D5E6F7")
	w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetRename, Card: office.ID, Name: "Work"})
	if _, err := w.call(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetRename, Card: "com.phone.wallet/transit", Name: "x"}); err == nil {
		t.Fatal("renamed an app's card")
	}
	st = w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetDelete, Card: home.ID}).(ui.NFCState)
	if len(st.Cards) != 3 || st.Cards[0].Name != "Work" {
		t.Fatalf("after deleting Home: %+v", st.Cards)
	}
}

// Workdays the owner taps the office card around 9:00, his home card
// around 20:00; the cards are chosen by the time of use, then the order.
func TestDoorByTimeOfUse(t *testing.T) {
	dir := t.TempDir()
	w := newWorld(t, dir)
	w.policy(true, false, walletCards...)
	w.door("Home", "04A1B2C3")
	office := w.door("Office", "04D5E6F7")
	officeDoor := svc.NFCTapArgs{Kind: "door", Name: "office door", Accept: []string{"04D5E6F7"}}
	homeDoor := svc.NFCTapArgs{Kind: "door", Name: "home door", Accept: []string{"04A1B2C3"}}
	tue := time.Date(2026, 11, 10, 9, 0, 0, 0, beijing)
	day := 24 * time.Hour

	// No history: the order's first card; the refusal switches.
	w.at(tue)
	if r := w.tap(t, officeDoor, false, "Home"); r.NextName != "Office" {
		t.Fatalf("after the refusal: %+v", r)
	}
	w.tap(t, officeDoor, true, "Office")
	// 40 minutes on the 9:00 tap counts; three and a half hours on it does not.
	w.at(tue.Add(40 * time.Minute))
	w.tap(t, officeDoor, true, "Office")
	w.at(tue.Add(210 * time.Minute))
	w.tap(t, officeDoor, false, "Home")
	w.tap(t, officeDoor, true, "Office")
	// The office card first in the order: at 20:00 nothing is near, the
	// order decides; the next day the home card's 20:00 beats the order.
	w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetMove, Card: office.ID, By: -1})
	w.at(tue.Add(11 * time.Hour))
	w.tap(t, homeDoor, false, "Office")
	w.tap(t, homeDoor, true, "Home")
	w.at(tue.Add(day))
	w.tap(t, officeDoor, true, "Office")
	w.at(tue.Add(day + 8*time.Hour))
	w.tap(t, homeDoor, false, "Office")
	w.tap(t, homeDoor, true, "Home")
	w.at(tue.Add(day + 11*time.Hour))
	w.tap(t, homeDoor, true, "Home")
	// A restart keeps the taps.
	w2 := newWorld(t, dir)
	w2.policy(true, false, walletCards...)
	w2.at(tue.Add(2*day + 5*time.Minute))
	w2.tap(t, officeDoor, true, "Office")
	w2.at(tue.Add(2*day + 11*time.Hour + 10*time.Minute))
	w2.tap(t, homeDoor, true, "Home")
	// Weekends apart: the first Saturday has no taps, the order decides;
	// the next one the home card goes first at 9:30, the Monday after the
	// office card at 9:00.
	sat := time.Date(2026, 11, 14, 9, 0, 0, 0, beijing)
	w2.at(sat)
	w2.tap(t, homeDoor, false, "Office")
	w2.tap(t, homeDoor, true, "Home")
	w2.at(sat.Add(7*day + 30*time.Minute))
	w2.tap(t, homeDoor, true, "Home")
	w2.at(sat.Add(9 * day))
	w2.tap(t, officeDoor, true, "Office")

	st := w2.must(svc.NFCState, nil).(ui.NFCState)
	hours := map[string][]ui.NFCHours{}
	for _, c := range st.Cards {
		hours[c.Name] = c.Usual
	}
	want := map[string][]ui.NFCHours{
		"Office": {{From: 8 * 60, To: 10*60 + 30}, {From: 11*60 + 30, To: 13*60 + 30}},
		"Home":   {{From: 16 * 60, To: 18 * 60}, {From: 19 * 60, To: 21 * 60}, {Weekend: true, From: 8 * 60, To: 10*60 + 30}},
	}
	for name, spans := range want {
		if len(hours[name]) != len(spans) {
			t.Fatalf("%s usually: %+v, want %+v", name, hours[name], spans)
		}
		for i := range spans {
			if hours[name][i] != spans[i] {
				t.Fatalf("%s usually: %+v, want %+v", name, hours[name], spans)
			}
		}
	}
	if st.DoorFirst != office.ID {
		t.Fatalf("Monday 9:00 a door gets %q first", st.DoorFirst)
	}
	// Taps older than 60 days count no more.
	w2.at(sat.Add(70 * day))
	w2.e.mu.Lock()
	scores, usual := w2.e.doorScores(w2.e.clock()), w2.e.usual(office.ID, w2.e.clock())
	w2.e.mu.Unlock()
	if len(scores) != 0 || len(usual) != 0 {
		t.Fatalf("taps 70 days old: scores %v, hours %+v", scores, usual)
	}
}

// The score is smooth over the time of day, across midnight too, newer
// taps weigh more, and workdays and weekends are apart.
func TestDoorScores(t *testing.T) {
	e := newEngine(nfc.NewSim(), t.TempDir(), func(svc.NFCEvent) {})
	mon := time.Date(2026, 11, 9, 0, 0, 0, 0, beijing)
	at := func(d, h, m int) time.Time { return mon.Add(time.Duration(d*24*60+h*60+m) * time.Minute) }
	e.st.Taps = []tap{{"door:1", at(0, 9, 0)}, {"door:2", at(0, 23, 50)}, {"door:3", at(-2, 9, 0)}}
	var last float64
	for _, m := range []int{0, 20, 40, 60, 90, 120, 149} {
		s := e.doorScores(at(1, 9, m))["door:1"]
		if s <= 0 || (m > 0 && s >= last) {
			t.Fatalf("9:%02d the next day scores %v after %v", m, s, last)
		}
		last = s
	}
	if s := e.doorScores(at(1, 11, 31)); s["door:1"] != 0 {
		t.Fatalf("two and a half hours on: %v", s)
	}
	if s := e.doorScores(at(1, 0, 30)); s["door:2"] == 0 {
		t.Fatalf("23:50 does not count at 0:30: %v", s)
	}
	// Sunday's 9:00 counts on Saturday, not on Monday.
	if s := e.doorScores(at(5, 9, 0)); s["door:3"] == 0 || s["door:1"] != 0 {
		t.Fatalf("Saturday 9:00: %v", s)
	}
	newer := e.doorScores(at(1, 9, 0))["door:1"]
	older := e.doorScores(at(15, 9, 0))["door:1"]
	if math.Abs(older/newer-0.5) > 0.01 {
		t.Fatalf("two weeks on a tap weighs %v of what it did", older/newer)
	}
}

func TestUsualHoursMerge(t *testing.T) {
	e := newEngine(nfc.NewSim(), t.TempDir(), func(svc.NFCEvent) {})
	day := time.Date(2026, 11, 9, 0, 0, 0, 0, beijing)
	at := func(d, h, m int) time.Time { return day.Add(time.Duration(d*24*60+h*60+m) * time.Minute) }
	e.st.Taps = []tap{
		{"door:1", at(0, 9, 0)}, {"door:1", at(1, 9, 20)}, {"door:1", at(2, 12, 30)},
		{"door:1", at(3, 13, 10)}, {"door:1", at(4, 23, 50)}, {"door:2", at(4, 9, 0)},
	}
	got := e.usual("door:1", at(5, 8, 0))
	want := []ui.NFCHours{{From: 8 * 60, To: 10*60 + 30}, {From: 11*60 + 30, To: 14 * 60}, {From: 23 * 60, To: 24 * 60}}
	if len(got) != len(want) {
		t.Fatalf("usual %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("usual %+v, want %+v", got, want)
		}
	}
}

// Where a reader's polling loop tells it apart (observe mode, the screen
// on), the card it took last goes first when the time of use says
// nothing; the time of use comes first.
func TestReaderHint(t *testing.T) {
	w := newWorld(t, t.TempDir())
	w.policy(false, true, walletCards...)
	w.door("Home", "04A1B2C3")
	w.door("Office", "04D5E6F7")
	side := svc.NFCTapArgs{Kind: "door", Name: "side door", Accept: []string{"04D5E6F7"}}
	front := svc.NFCTapArgs{Kind: "door", Name: "front door", Accept: []string{"04A1B2C3"}}
	tue := time.Date(2026, 11, 10, 9, 0, 0, 0, beijing)
	w.at(tue)
	w.tap(t, side, false, "Home")
	w.tap(t, side, true, "Office")
	w.at(tue.Add(6 * time.Hour))
	w.tap(t, side, true, "Office")
	w.at(tue.Add(11 * time.Hour))
	w.tap(t, front, true, "Home")
	// The time of use first: at 9:10 the office card, whichever door.
	w.at(tue.Add(24*time.Hour + 10*time.Minute))
	w.tap(t, front, false, "Office")
	w.tap(t, front, true, "Home")
	// With the screen off the controller answers by itself: no loop, no hint.
	w.policy(false, false, walletCards...)
	w.at(tue.Add(27 * time.Hour))
	w.tap(t, side, false, "Home")
	w.tap(t, side, true, "Office")
}

// Each card answers locked with the screen off or not, as the user says;
// payment cards never.
func TestLockedChoice(t *testing.T) {
	w := newWorld(t, t.TempDir())
	w.policy(true, false, walletCards...)
	home := w.door("Home", "04A1B2C3")
	w.door("Office", "04D5E6F7")
	st := w.must(svc.NFCState, nil).(ui.NFCState)
	locked := map[string][2]bool{}
	for _, c := range st.Cards {
		locked[c.ID] = [2]bool{c.Locked, c.LockedFixed}
	}
	if locked[home.ID] != [2]bool{true, false} || locked["com.phone.wallet/transit"] != [2]bool{false, false} || locked["com.phone.wallet/pay"] != [2]bool{false, true} {
		t.Fatalf("defaults: %v", locked)
	}
	homeDoor := svc.NFCTapArgs{Kind: "door", Name: "home door", Accept: []string{"04A1B2C3"}}
	w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetLocked, Card: home.ID, On: false})
	w.tap(t, homeDoor, false, "Office")
	w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetLocked, Card: home.ID, On: true})
	w.tap(t, homeDoor, true, "Home")
	if _, err := w.call(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetLocked, Card: "com.phone.wallet/pay", On: true}); err == nil {
		t.Fatal("a payment card was let answer locked")
	}
	// The express transit card answers locked unless the user says no.
	gate := svc.NFCTapArgs{Kind: "gate", Name: "metro", AID: transitAID, Fare: 200}
	w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetExpress, Card: "com.phone.wallet/transit"})
	w.tap(t, gate, true, "Feiqian transit card")
	w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetLocked, Card: "com.phone.wallet/transit", On: false})
	if r := w.tap(t, gate, false, ""); r.Reason != "screen" {
		t.Fatalf("the express card the user keeps for the screen on: %+v", r)
	}
}

// Door cards answer with the phone locked and the screen off; the shell
// hears the field come and go (a suspended phone wakes for it).
func TestDoorScreenOffAndFieldEvents(t *testing.T) {
	w := newWorld(t, t.TempDir())
	w.policy(true, false, walletCards...)
	home := w.door("Home", "04A1B2C3")
	if r := w.tap(t, svc.NFCTapArgs{Kind: "door", Name: "d", Accept: []string{"04A1B2C3"}}, true, "Home"); !r.OK || r.Card != home.ID {
		t.Fatalf("door with the screen off: %+v", r)
	}
	w.mu.Lock()
	fields := append([]bool(nil), w.fields...)
	w.mu.Unlock()
	if len(fields) != 2 || !fields[0] || fields[1] {
		t.Fatalf("field events: %v", fields)
	}
	// The picker's choice goes first all the same.
	office := w.door("Office", "04D5E6F7")
	w.must(svc.NFCPresent, map[string]any{"card": office.ID, "seconds": 60})
	w.tap(t, svc.NFCTapArgs{Kind: "door", Name: "o", Accept: []string{"04D5E6F7"}}, true, "Office")
}

// blind is a controller without observe mode (the NCI backend): the
// phone answers as its best guess before a reader comes.
type blind struct {
	*nfc.Sim
	mu  sync.Mutex
	uid []byte
}

func (b *blind) Info() nfc.Info { return nfc.Info{Name: "blind"} }

func (b *blind) Present(id *nfc.Identity) error {
	b.mu.Lock()
	b.uid = nil
	if id != nil {
		b.uid = append([]byte(nil), id.UID...)
	}
	b.mu.Unlock()
	return b.Sim.Present(id)
}

func TestIdleFollowsTimeOfDay(t *testing.T) {
	b := &blind{Sim: nfc.NewSim()}
	w := newWorldOn(t, t.TempDir(), b.Sim, b)
	w.policy(true, false)
	w.door("Home", "04A1B2C3")
	office := w.door("Office", "04D5E6F7")
	tue := time.Date(2026, 11, 10, 9, 0, 0, 0, beijing)
	w.e.mu.Lock()
	w.e.st.Taps = []tap{{office.ID, tue.Add(-24 * time.Hour)}, {"door:1", tue.Add(-13 * time.Hour)}}
	w.e.mu.Unlock()
	presented := func() string {
		w.e.mu.Lock()
		w.e.idle()
		w.e.mu.Unlock()
		b.mu.Lock()
		defer b.mu.Unlock()
		return strings.ToUpper(hex.EncodeToString(b.uid))
	}
	w.at(tue)
	if got := presented(); got != "04D5E6F7" {
		t.Fatalf("at 9:00 the phone answers as %s", got)
	}
	w.at(tue.Add(11 * time.Hour))
	if got := presented(); got != "04A1B2C3" {
		t.Fatalf("at 20:00 the phone answers as %s", got)
	}
}

func TestExpressTransitScreenOff(t *testing.T) {
	w := newWorld(t, t.TempDir())
	w.policy(true, false, walletCards...)
	w.door("Home", "04A1B2C3")
	gate := svc.NFCTapArgs{Kind: "gate", Name: "metro", AID: transitAID, Fare: 200}
	r := w.tap(t, gate, false, "")
	if r.Reason != "screen" || r.Name != "Feiqian transit card" {
		t.Fatalf("without express, the transit card waits for the screen: %+v", r)
	}
	if _, err := w.call(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetExpress, Card: "com.phone.wallet/pay"}); err == nil {
		t.Fatal("a payment card became the express card")
	}
	w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetExpress, Card: "com.phone.wallet/transit"})
	if r = w.tap(t, gate, true, "Feiqian transit card"); !r.OK || w.balance != 1800 {
		t.Fatalf("express: %+v, balance %d", r, w.balance)
	}
}

func TestPaymentNeedsUnlock(t *testing.T) {
	w := newWorld(t, t.TempDir())
	w.policy(true, true, walletCards...)
	w.door("Home", "04A1B2C3")
	till := svc.NFCTapArgs{Kind: "pay", Name: "till", AID: payAID, Amount: 1500}
	r := w.tap(t, till, false, "")
	if r.Reason != "locked" || r.Card != "com.phone.wallet/pay" {
		t.Fatalf("locked: %+v", r)
	}
	// Unlocked while the till is still polling: the payment card answers.
	go func() {
		time.Sleep(150 * time.Millisecond)
		w.policy(false, true, walletCards...)
	}()
	if r = w.tap(t, till, true, "Feiqian pay"); !r.OK {
		t.Fatalf("unlocked at the till: %+v", r)
	}
	if r = w.tap(t, till, true, "Feiqian pay"); !r.OK {
		t.Fatalf("unlocked: %+v", r)
	}
	// A second payment app: the default one pays, the one in front only
	// when the user allows it.
	other := svc.NFCAppCard{App: "com.example.bank", AppName: "Bank", Card: "debit", Name: "Debit", Category: ui.NFCPayment, AIDs: []string{ui.PPSE, payAID}}
	w.e.mu.Lock()
	w.e.setPolicy(svc.NFCPolicyArgs{ScreenOn: true, Foreground: "com.example.bank", Cards: append(append([]svc.NFCAppCard{}, walletCards...), other)})
	w.e.mu.Unlock()
	w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetPayment, App: "com.phone.wallet"})
	w.tap(t, till, true, "Feiqian pay")
	w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetForeground, On: true})
	w.tap(t, till, true, "Debit")
	st := w.must(svc.NFCState, nil).(ui.NFCState)
	if st.DefaultPayment != "com.phone.wallet" || len(st.PaymentApps) != 2 {
		t.Fatalf("state: %+v", st)
	}
}

func TestReaderKindAndOrder(t *testing.T) {
	w := newWorld(t, t.TempDir())
	w.policy(false, true, walletCards...)
	w.door("Home", "04A1B2C3")
	w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetMove, Card: "door:1", By: 1})
	st := w.must(svc.NFCState, nil).(ui.NFCState)
	if st.Cards[0].ID == "door:1" || st.Cards[1].ID != "door:1" {
		t.Fatalf("order after moving the door card down: %+v", st.Cards)
	}
	// A door says it is one: the door card goes first all the same.
	w.tap(t, svc.NFCTapArgs{Kind: "door", Name: "d", Accept: []string{"04A1B2C3"}}, true, "Home")
	w.tap(t, svc.NFCTapArgs{Kind: "gate", Name: "g", AID: transitAID, Fare: 100}, true, "Feiqian transit card")
	w.must(svc.NFCSet, ui.NFCSetArgs{Key: ui.NFCSetEnabled, On: false})
	if r := w.tap(t, svc.NFCTapArgs{Kind: "door", Name: "d", Accept: []string{"04A1B2C3"}}, false, ""); r.OK {
		t.Fatalf("NFC off: %+v", r)
	}
}

func TestSilentAppTimesOut(t *testing.T) {
	w := newWorld(t, t.TempDir())
	w.policy(false, true, walletCards...)
	w.mu.Lock()
	w.silent = true
	w.mu.Unlock()
	r := w.tap(t, svc.NFCTapArgs{Kind: "gate", Name: "g", AID: transitAID, Fare: 100}, false, "Feiqian transit card")
	if r.Reason != "timeout" {
		t.Fatalf("an app that never answers: %+v", r)
	}
	w.e.mu.Lock()
	err := w.e.reply("s1", "com.phone.wallet", "9000")
	w.e.mu.Unlock()
	if err == nil {
		t.Fatal("a late answer went to a reader that left")
	}
}

func TestConflictingAIDs(t *testing.T) {
	w := newWorld(t, t.TempDir())
	second := svc.NFCAppCard{App: "com.example.metro", AppName: "Metro", Card: "city", Name: "City card", Category: ui.NFCOther, AIDs: []string{transitAID}, Express: true}
	w.policy(false, true, append(append([]svc.NFCAppCard{}, walletCards...), second)...)
	w.mu.Lock()
	w.balance = 100
	w.mu.Unlock()
	// The wallet's card comes first and has too little; the other app's
	// card with the same AID goes first at the next tap.
	gate := svc.NFCTapArgs{Kind: "gate", Name: "g", AID: transitAID, Fare: 200}
	r := w.tap(t, gate, false, "Feiqian transit card")
	if r.NextName != "City card" {
		t.Fatalf("after the refusal: %+v", r)
	}
	var apps []string
	w.e.mu.Lock()
	_, id := w.e.identity(w.e.find(r.Next), 0, nil)
	for _, c := range id.AIDs {
		apps = append(apps, hex.EncodeToString(c))
	}
	owner := w.e.find(w.e.pending)
	w.e.mu.Unlock()
	if len(apps) != 3 || owner == nil || owner.App != "com.example.metro" {
		t.Fatalf("next identity %v, first %+v", apps, owner)
	}
}
