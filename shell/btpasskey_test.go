package shell

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/ui"
)

// agentTest is the Shell with BlueZ's pairing agent on a radio without a
// bus: the agent's who is the test's (peer, asked), and a pairing the
// user cancels is noted in cancelled.
type agentTest struct {
	*switchTest
	r           *bluezRadio
	a           *btAgent
	peer, asked bool
	cancelled   chan dbus.ObjectPath
}

// kbd is the keyboard the user asked to pair with.
const (
	kbd     = dbus.ObjectPath("/org/bluez/hci0/dev_02_4B_41_52_49_09")
	kbdAddr = "02:4B:41:52:49:09"
)

func newAgentTest(t *testing.T) *agentTest {
	st := newSwitchTest(t)
	c := st.c
	c.ime = &imeClient{keypad: ime.QWERTY}
	c.cfg.W, c.cfg.H = 40, 24
	at := &agentTest{switchTest: st, asked: true, cancelled: make(chan dbus.ObjectPath, 4)}
	at.r = &bluezRadio{confirm: c.confirmPairing, show: c.showPairCode, hide: c.hidePairCode, ask: c.askPairCode, logf: c.log.Printf}
	at.a = &btAgent{r: at.r, who: func(dbus.ObjectPath) (bool, bool, string) { return at.peer, at.asked, "K380" },
		cancel: func(dev dbus.ObjectPath) { at.cancelled <- dev }}
	return at
}

// passkeyScreen waits for the code to type to be up.
func (at *agentTest) passkeyScreen() *pairCodeOverlay {
	at.t.Helper()
	var o *pairCodeOverlay
	at.pump("the code to type", func() bool { o = at.c.pairCodeUp(); return o != nil })
	return o
}

// settled runs what was posted to the loop so far.
func (at *agentTest) settled() {
	at.t.Helper()
	done := false
	at.c.Post(func() { done = true })
	at.pump("the loop", func() bool { return done })
}

// textScreen is the screen as the text renderer draws it.
func (at *agentTest) textScreen(o overlay) string {
	can := cv.New(40, 24, cv.Style{})
	o.render(at.c, can)
	return can.Plain()
}

// A Bluetooth keyboard pairs by its user typing the code the phone shows
// (DisplayPasskey): a system screen over the pairing page names it and
// has the code, 6 digits with the zeros in front, large on a renderer
// that draws digits and a line of the dialog on one that does not. The
// digits typed there, as BlueZ says when it calls again (entered), are
// marked off on the same screen; none are while the keyboard has said
// nothing. BlueZ's Cancel takes the screen away, as does the end of the
// pairing (Pair answered), after which a late call shows nothing. A
// legacy keyboard's PIN (DisplayPinCode) shows the same way. Screenshots
// mask the screen, the screen reader says the code, and it is not logged.
func TestBluetoothKeyboardPasskey(t *testing.T) {
	at := newAgentTest(t)
	c := at.c
	p := at.r.begin(kbdAddr)
	if err := at.a.DisplayPasskey(kbd, 48213, 0); err != nil {
		t.Fatal(err)
	}
	o := at.passkeyScreen()
	f := o.frame(c)
	if o.code != "048213" || f.Kind != "auth" || f.Title != tr.T("Bluetooth pairing") || f.Lines[0] != tr.T("“%s” pairs with a code typed on it.", "K380") ||
		f.Lines[len(f.Lines)-1] != "0 4 8 2 1 3" || f.Digits != "" || f.PIN != [2]int{} || c.secureScreen() != "a pairing code" {
		t.Fatalf("the screen: %+v, secure %q", f, c.secureScreen())
	}
	if s := at.textScreen(o); !strings.Contains(s, "0  4  8  2  1  3") || strings.Contains(s, "○") || !strings.Contains(s, tr.T("Esc cancels the pairing")) {
		t.Fatalf("the text screen before any digit typed:\n%s", s)
	}
	c.renderer = &RendererInfo{Features: []string{"digits"}}
	if f := o.frame(c); f.Digits != "048213" || len(f.Lines) != 2 || f.Footer[0] != tr.T("Back cancels the pairing") {
		t.Fatalf("on a renderer that draws digits: %+v", f)
	}
	if _, title, _, _ := c.readOverlay(o); !strings.Contains(title, "0 4 8 2 1 3") {
		t.Errorf("the screen reader says %q", title)
	}

	if err := at.a.DisplayPasskey(kbd, 48213, 2); err != nil {
		t.Fatal(err)
	}
	at.pump("two digits marked", func() bool { return o.entered == 2 })
	if c.pairCodeUp() != o || o.frame(c).PIN != [2]int{2, 6} || !strings.Contains(at.textScreen(o), "●  ●  ○  ○  ○  ○") {
		t.Fatalf("two typed: another screen, or %+v\n%s", o.frame(c), at.textScreen(o))
	}
	_ = at.a.DisplayPasskey(kbd, 48213, 1)
	at.pump("a digit erased", func() bool { return o.entered == 1 })

	if err := at.a.Cancel(); err != nil {
		t.Fatal(err)
	}
	at.pump("the screen gone at BlueZ's Cancel", func() bool { return c.pairCodeUp() == nil })

	_ = at.a.DisplayPinCode(kbd, "730529")
	if o := at.passkeyScreen(); o.code != "730529" || o.counts {
		t.Fatalf("a legacy keyboard's PIN: %q, counts %v", o.code, o.counts)
	}
	p.end()
	at.pump("the screen gone with the pairing", func() bool { return c.pairCodeUp() == nil })
	if err := at.a.DisplayPasskey(kbd, 48213, 3); err != nil {
		t.Fatal(err)
	}
	at.settled()
	if c.pairCodeUp() != nil {
		t.Fatal("a call after Pair answered put the screen up again")
	}
	at.r.finish(p)
	if err := at.a.DisplayPasskey(kbd, 48213, 0); err == nil {
		t.Error("a code shown with no pairing under way")
	}
	at.settled()
	if c.pairCodeUp() != nil {
		t.Fatal("a screen with no pairing under way")
	}
	if l := at.log.String(); strings.Contains(l, "48213") || strings.Contains(l, "730529") {
		t.Errorf("the code in the log:\n%s", l)
	}
}

// Back (Esc) on the code to type closes it and cancels the pairing
// (Device1's CancelPairing on that device); its Pair failing then is a
// cancel, which Settings does not show, where a failure otherwise says
// so and a device paired already is none.
func TestBluetoothKeyboardPasskeyBack(t *testing.T) {
	at := newAgentTest(t)
	c := at.c
	p := at.r.begin(kbdAddr)
	defer at.r.finish(p)
	_ = at.a.DisplayPasskey(kbd, 999999, 0)
	at.passkeyScreen()
	if err := pairErr(p, dbus.Error{Name: bluezName + ".Error.AuthenticationFailed"}); err == nil || ui.Cancelled(err) {
		t.Fatalf("a failure: %v", err)
	}
	c.key(Key{Name: KeyEsc})
	if c.pairCodeUp() != nil {
		t.Fatal("the screen stayed at Esc")
	}
	if dev := <-at.cancelled; dev != kbd {
		t.Fatalf("cancelled the pairing with %s", dev)
	}
	if err := pairErr(p, dbus.Error{Name: bluezName + ".Error.AuthenticationCanceled"}); !ui.Cancelled(err) {
		t.Errorf("Pair after Back: %v", err)
	}
	if err := pairErr(p, dbus.Error{Name: bluezName + ".Error.AlreadyExists"}); err != nil {
		t.Errorf("paired already: %v", err)
	}
}

// The simulated keyboard (the selftest's bt-keyboard) pairs as the agent
// has a real one pair: the code on the phone, the digits its user types
// marked, paired at Enter with the screen gone; Back on the phone cancels
// its pairing. Taken out of range (bt-gone), it is no longer listed, and
// a device added later does not get its address.
func TestSimBluetoothKeyboard(t *testing.T) {
	at := newAgentTest(t)
	c := at.c
	b := &simBT{enabled: true, show: c.showPairCode}
	b.addKeyboard("K380")
	_ = b.discover(true)
	addr := b.devices[0].Address
	paired := make(chan error, 1)
	go func() { paired <- b.pair(addr) }()
	o := at.passkeyScreen()
	if o.code != "482913" || o.name != "K380" {
		t.Fatalf("shown %q for %q", o.code, o.name)
	}
	if err := b.typeOnKeyboard(3); err != nil {
		t.Fatal(err)
	}
	at.pump("three digits marked", func() bool { return o.entered == 3 })
	if err := b.typeOnKeyboard(-1); err != nil {
		t.Fatal(err)
	}
	if err := <-paired; err != nil {
		t.Fatal(err)
	}
	at.pump("the screen gone once paired", func() bool { return c.pairCodeUp() == nil })
	if st, _ := b.state(); !st.Devices[0].Paired {
		t.Fatalf("not paired: %+v", st.Devices[0])
	}

	_ = b.forget(addr)
	go func() { paired <- b.pair(addr) }()
	at.passkeyScreen()
	c.key(Key{Name: KeyEsc})
	if err := <-paired; !ui.Cancelled(err) {
		t.Fatalf("Back on the phone: %v", err)
	}
	if st, _ := b.state(); st.Devices[0].Paired || c.pairCodeUp() != nil {
		t.Fatalf("after Back: %+v, screen %v", st.Devices[0], c.pairCodeUp())
	}
	if err := b.typeOnKeyboard(1); err == nil {
		t.Error("typed on a keyboard that is not pairing")
	}

	b.addPeer("Kario earbuds", false)
	if err := b.removePeer("K380"); err != nil {
		t.Fatal(err)
	}
	b.addPeer("Car kit", true)
	st, _ := b.state()
	if len(st.Devices) != 2 || st.Devices[0].Name != "Kario earbuds" || st.Devices[1].Address == st.Devices[0].Address || st.Devices[1].Address == addr || b.keyboards[addr] {
		t.Fatalf("the keyboard gone: %+v", st.Devices)
	}
	if err := b.removePeer("K380"); err == nil {
		t.Error("a device gone twice")
	}
}

// The selftest peer's own side is answered as before (shown nothing,
// a code refused), and a device the user did not ask to pair with is
// refused: no screen for it.
func TestBluetoothPasskeyOnlyAsked(t *testing.T) {
	at := newAgentTest(t)
	c := at.c
	p := at.r.begin(kbdAddr)
	defer at.r.finish(p)
	for _, tc := range []struct {
		name        string
		peer, asked bool
		display     bool // DisplayPasskey answered without an error
	}{{"the peer's side", true, false, true}, {"not asked", false, false, false}} {
		at.peer, at.asked = tc.peer, tc.asked
		if err := at.a.DisplayPasskey(kbd, 123456, 0); (err == nil) != tc.display {
			t.Errorf("%s: DisplayPasskey %v", tc.name, err)
		}
		if err := at.a.DisplayPinCode(kbd, "123456"); (err == nil) != tc.display {
			t.Errorf("%s: DisplayPinCode %v", tc.name, err)
		}
		if _, err := at.a.RequestPasskey(kbd); err == nil {
			t.Errorf("%s: a passkey given", tc.name)
		}
		if _, err := at.a.RequestPinCode(kbd); err == nil {
			t.Errorf("%s: a PIN given", tc.name)
		}
		at.settled()
		if len(c.overlays) != 0 {
			t.Errorf("%s: %T up", tc.name, c.overlays[0])
		}
	}
}

// A device being paired that asks for its passkey (RequestPasskey) has
// it typed on the code screen: digits only, 6 at most, the number typed
// is the answer (the zeros in front count for nothing); on a keyboard
// with an Fn layer a letter key types its digit, on the 9-key pad # is
// no mode. One asking for a PIN (RequestPinCode, an old car kit) gets
// letters and digits as typed. Esc refuses and is the user's cancel; BlueZ's
// Cancel, or the pairing ending, takes the screen away and refuses. The
// codes are not logged.
func TestBluetoothRequestPasskey(t *testing.T) {
	at := newAgentTest(t)
	c := at.c
	p := at.r.begin(kbdAddr)
	defer at.r.finish(p)
	type answer struct {
		code string
		err  error
	}
	ask := func(pin bool) (*codeOverlay, chan answer) {
		t.Helper()
		res := make(chan answer, 1)
		go func() {
			if pin {
				s, err := at.a.RequestPinCode(kbd)
				res <- answer{s, errOf(err)}
				return
			}
			n, err := at.a.RequestPasskey(kbd)
			res <- answer{strconv.FormatUint(uint64(n), 10), errOf(err)}
		}()
		return at.codeScreen(), res
	}
	got := func(res chan answer) answer {
		t.Helper()
		var a answer
		at.pump("the agent's answer", func() bool {
			select {
			case a = <-res:
				return true
			default:
				return false
			}
		})
		return a
	}

	o, res := ask(false)
	f := o.frame(c)
	if f.Title != tr.T("Bluetooth pairing") || f.Lines[0] != tr.T("“%s” asks for a passkey to pair.", "K380") || f.PIN != [2]int{0, btPasskeyMax} ||
		f.Footer[1] != tr.T("The system draws this screen; the code goes to this pairing only, no app sees it.") || c.secureScreen() != "a PIN entry" {
		t.Fatalf("the passkey screen: %+v", f)
	}
	c.key(Runes("0a4"))
	if o.code != "04" || o.err != tr.T("Only digits") {
		t.Fatalf("typed %q, %q", o.code, o.err)
	}
	c.key(Runes("82139"))
	if o.code != "048213" {
		t.Fatalf("typed %q", o.code)
	}
	c.key(Key{Name: KeyEnter})
	if a := got(res); a.code != "48213" || a.err != nil {
		t.Fatalf("answered %+v", a)
	}

	c.SetFnLayer(testFnLayer)
	o, res = ask(false)
	if c.inputMode() != "123" {
		t.Errorf("with an Fn layer the status bar says %q", c.inputMode())
	}
	c.key(Runes("wer"))
	c.key(Key{Name: KeyEnter})
	if a := got(res); a.code != "123" || a.err != nil || o.err != "" {
		t.Fatalf("Fn digits: %+v, %q", a, o.err)
	}
	c.SetFnLayer(nil)
	c.ime = &imeClient{keypad: ime.T9Pad}
	_, res = ask(false)
	c.key(Runes("7#0*"))
	c.key(Key{Name: KeyRunes, Runes: []rune("1"), Long: true})
	c.key(Key{Name: KeyEnter})
	if a := got(res); a.code != "701" || a.err != nil {
		t.Fatalf("on the 9-key pad: %+v", a)
	}
	c.ime = &imeClient{keypad: ime.QWERTY}

	_, res = ask(false)
	c.key(Key{Name: KeyEsc})
	if a := got(res); a.err == nil || !p.back.Load() {
		t.Fatalf("Esc: %+v, the user's cancel %v", a, p.back.Load())
	}
	p.back.Store(false)

	o, res = ask(true)
	if f := o.frame(c); f.Lines[0] != tr.T("“%s” asks for a PIN to pair.", "K380") || f.PIN != [2]int{0, btCodeMax} {
		t.Fatalf("the PIN screen: %+v", f)
	}
	c.key(Runes("7Kq2"))
	c.key(Key{Name: KeyEnter})
	if a := got(res); a.code != "7Kq2" || a.err != nil {
		t.Fatalf("the PIN: %+v", a)
	}

	o, res = ask(true)
	_ = at.a.Cancel()
	at.pump("the code screen gone at BlueZ's Cancel", func() bool { return c.codeOnTop() != o })
	if a := got(res); a.err == nil || p.back.Load() {
		t.Fatalf("cancelled by BlueZ: %+v, the user's cancel %v", a, p.back.Load())
	}
	o, res = ask(false)
	p.end()
	at.pump("the code screen gone with the pairing", func() bool { return c.codeOnTop() != o })
	if a := got(res); a.err == nil {
		t.Fatalf("after the pairing: %+v", a)
	}
	if l := at.log.String(); strings.Contains(l, "48213") || strings.Contains(l, "7Kq2") {
		t.Errorf("a code in the log:\n%s", l)
	}
}

func errOf(err *dbus.Error) error {
	if err == nil {
		return nil
	}
	return errors.New(err.Name)
}
