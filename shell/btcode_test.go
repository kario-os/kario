package shell

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/obex"
	"github.com/kario-os/kario/ui"
)

// codeScreen waits for the Bluetooth code screen.
func (st *switchTest) codeScreen() *codeOverlay {
	st.t.Helper()
	var o *codeOverlay
	st.pump("the code screen", func() bool {
		if n := len(st.c.overlays); n > 0 {
			o, _ = st.c.overlays[n-1].(*codeOverlay)
		}
		return o != nil
	})
	return o
}

// typeCode types code on the screen and confirms it; "" is Esc.
func (st *switchTest) typeCode(code string) {
	if code == "" {
		st.c.key(Key{Name: KeyEsc})
		return
	}
	st.c.key(Key{Name: KeyRunes, Runes: []rune(code)})
	st.c.key(Key{Name: KeyEnter})
}

// An old phone that asks for a code (OBEX authentication) before its
// user decides: the system's code screen, which injected keys cannot
// type on, asks for it for the phone book and again for the messages,
// takes letters and digits only and no empty code; with the code the
// move goes as without one.
func TestSwitchBluetoothCode(t *testing.T) {
	st := newSwitchTest(t)
	c := st.c
	c.ime = &imeClient{keypad: ime.QWERTY}
	ph, addr := st.btOldPhone("android", "Redmi Note 9")
	ph.Password = "0000"
	if _, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: ui.SwitchKinds, Bluetooth: addr}); err != nil {
		t.Fatal(err)
	}
	o := st.codeScreen()
	f := o.frame(c)
	if f.Kind != "auth" || f.PIN != [2]int{0, btCodeMax} || !strings.Contains(f.Lines[0], "Redmi Note 9") || !refusesInjected(o) || c.secureScreen() != "a PIN entry" {
		t.Fatalf("the screen: %+v", f)
	}
	c.key(Key{Name: KeyEnter})
	if o.err == "" || c.overlays[len(c.overlays)-1] != o {
		t.Fatal("an empty code confirmed")
	}
	c.key(Key{Name: KeyRunes, Runes: []rune("0#0")})
	c.key(Key{Name: KeyBackspace})
	c.key(Key{Name: KeyRunes, Runes: []rune("000")})
	if o.code != "0000" {
		t.Fatalf("typed %q", o.code)
	}
	c.key(Key{Name: KeyRunes, Runes: []rune("1"), Injected: true})
	if o.code != "0000" {
		t.Fatal("an injected key typed")
	}
	c.key(Key{Name: KeyEnter})
	if next := st.codeScreen(); next == o {
		t.Fatal("the phone book's screen stayed")
	}
	st.typeCode("0000")
	st.moveEnded()
	s, _ := st.call(ui.CapSwitchState, nil)
	a := added(s)
	if a[ui.SwitchContacts].Stored != 3 || a[ui.SwitchCallLog].Stored != 3 || a[ui.SwitchSMS].Stored != 4 {
		t.Fatalf("after: %+v", s.Last)
	}
	if asked := ph.Asked(); asked[0] != "pbap CONNECT" || !slices.Contains(asked, "map CONNECT") {
		t.Errorf("asked %v", asked)
	}
	if l := st.log.String(); strings.Contains(l, "0000") {
		t.Errorf("the code in the log:\n%s", l)
	}
}

// A wrong code leaves the phone book out, saying so (a toast, refused in
// the summary), the old phone's user not asked for it; Esc on both
// screens is a no, which the page says. Stopping the move takes a code
// screen still up away.
func TestSwitchBluetoothWrongCode(t *testing.T) {
	st := newSwitchTest(t)
	c := st.c
	c.ime = &imeClient{keypad: ime.QWERTY}
	ph, addr := st.btOldPhone("android", "Redmi Note 9")
	ph.Password = "0000"
	var toasts []string
	c.onToast = func(s string) { toasts = append(toasts, s) }
	start := func() {
		t.Helper()
		if _, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: ui.SwitchKinds, Bluetooth: addr}); err != nil {
			t.Fatal(err)
		}
	}
	start()
	st.codeScreen()
	st.typeCode("1234")
	st.codeScreen()
	st.typeCode("0000")
	st.moveEnded()
	s, _ := st.call(ui.CapSwitchState, nil)
	a := added(s)
	if a[ui.SwitchContacts].Note != ui.SwitchNoteRefused || a[ui.SwitchCallLog].Note != ui.SwitchNoteRefused || a[ui.SwitchSMS].Stored != 4 || slices.Contains(ph.Asked(), "pbap CONNECT") {
		t.Fatalf("after: %+v, asked %v", s.Last, ph.Asked())
	}
	if !slices.Contains(toasts, tr.T("“%s” did not take the code", "Redmi Note 9")) {
		t.Errorf("toasts %q", toasts)
	}

	start()
	st.codeScreen()
	st.typeCode("")
	st.codeScreen()
	st.typeCode("")
	st.moveEnded()
	if s, _ := st.call(ui.CapSwitchState, nil); s.Error != tr.T("“%s” did not allow it. Start again and allow it on that phone.", "Redmi Note 9") {
		t.Fatalf("cancelled: %+v", s)
	}

	start()
	o := st.codeScreen()
	if s, _ := st.call(ui.CapSwitchStop, nil); s.State != ui.SwitchIdle {
		t.Fatalf("stopped: %+v", s)
	}
	st.pump("the code screen gone", func() bool { return !slices.Contains(c.overlays, overlay(o)) })
}

// A car kit that challenges the phone when it reads the phone book: its
// user's code, typed on the code screen after the phone book is allowed,
// proves the phone to it; a wrong code or Esc does not.
func TestBluetoothCarKitCode(t *testing.T) {
	st := newSwitchTest(t)
	c := st.c
	c.ime = &imeClient{keypad: ime.QWERTY}
	b := &simBT{enabled: true, confirm: func(string, uint32) bool { return true }, serve: c.obexServe, peerBox: &c.radios.obex}
	c.bt = b
	b.addPeer("Car kit", false)
	addr := b.devices[0].Address
	if err := b.pair(addr); err != nil {
		t.Fatal(err)
	}
	c.settings.BTPhonebook = map[string]bool{addr: true}
	for _, tc := range []struct {
		code string
		want error
	}{{"4711", nil}, {"1234", obex.ErrNotProven}, {"", obex.ErrNotProven}} {
		var link io.ReadWriteCloser
		st.pump("the car kit's link", func() bool {
			var err error
			link, _, err = b.peerLink(uuidPBAP)
			return err == nil
		})
		res := make(chan error, 1)
		go func() {
			defer link.Close()
			cl, err := obex.DialWith(link, 0, obex.Connect{Target: obex.PBAPTarget, Wait: 10 * time.Second, Timeout: 5 * time.Second, Auth: obex.Auth{Password: "4711"}})
			if err == nil {
				_, _, err = cl.Get("x-bt/phonebook", "telecom/pb.vcf", nil)
				_ = cl.Close()
			}
			res <- err
		}()
		if o := st.codeScreen(); o.name != "Car kit" {
			t.Errorf("asked for %q's code", o.name)
		}
		st.typeCode(tc.code)
		var err error
		st.pump("the car kit's pull", func() bool {
			select {
			case err = <-res:
				return true
			default:
				return false
			}
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("%q: %v, want %v", tc.code, err, tc.want)
		}
	}
}

// codeDevice is a paired device that asks for its code (OBEX
// authentication) when a file is sent to it, keeps the file and notes the
// requests it got; drop: it ends the link once it has asked.
type codeDevice struct {
	*simBT
	code string
	drop bool
	box  obexState
	mu   sync.Mutex
	ops  []string
}

func (d *codeDevice) obexDial(addr, uuid string) (io.ReadWriteCloser, int, error) {
	if uuid != uuidOPP {
		return d.simBT.obexDial(addr, uuid)
	}
	phone, dev := net.Pipe()
	go func() {
		defer dev.Close()
		_ = (&obex.Server{Inbox: peerInbox{&d.box}, Auth: obex.Auth{Password: d.code}}).Serve(&opsLink{Conn: dev, d: d}, 0)
	}()
	return phone, 0, nil
}

func (d *codeDevice) requests() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.ops, " ")
}

// opsLink notes the request of each packet read from it.
type opsLink struct {
	net.Conn
	d   *codeDevice
	buf []byte
}

func (l *opsLink) Read(p []byte) (int, error) {
	n, err := l.Conn.Read(p)
	l.buf = append(l.buf, p[:n]...)
	for len(l.buf) >= 3 {
		size := int(binary.BigEndian.Uint16(l.buf[1:]))
		if size < 3 || size > len(l.buf) {
			break
		}
		l.d.mu.Lock()
		l.d.ops = append(l.d.ops, map[byte]string{0x80: "CONNECT", 0x81: "DISCONNECT", 0x02: "PUT", 0x82: "PUT"}[l.buf[0]])
		l.d.mu.Unlock()
		l.buf = l.buf[size:]
	}
	return n, err
}

func (l *opsLink) Write(p []byte) (int, error) {
	n, err := l.Conn.Write(p)
	if l.d.drop && len(p) > 0 && p[0] == obex.Unauthorized {
		l.Conn.Close()
	}
	return n, err
}

// Sending a file (bt.send) to a device that asks for its code in its
// answer to the CONNECT: the code screen asks for it, once. The right code
// sends the file. A wrong code ends with that said, Esc with cancelled
// (which Settings does not show), no code in time with that said; each of
// them tells the device (DISCONNECT) and leaves no code screen up. The
// code is not logged.
func TestBluetoothSendCode(t *testing.T) {
	st := newSwitchTest(t)
	c := st.c
	c.ime = &imeClient{keypad: ime.QWERTY}
	sim := &simBT{enabled: true, confirm: func(string, uint32) bool { return true }, serve: c.obexServe, peerBox: &c.radios.obex}
	sim.addPeer("Nokia 6300", true)
	addr := sim.devices[0].Address
	if err := sim.pair(addr); err != nil {
		t.Fatal(err)
	}
	d := &codeDevice{simBT: sim, code: "4711"}
	c.bt = d
	if _, err := c.files.add("notes.txt", []byte("hello"), c.now()); err != nil {
		t.Fatal(err)
	}
	defer func(w time.Duration) { btCodeWait = w }(btCodeWait)
	for _, tc := range []struct {
		name string
		code string // typed ("": Esc, "-": nothing)
		want string // the reply's error ("": sent)
		ops  string // the requests the device got
	}{
		{"right", "4711", "", "CONNECT CONNECT PUT DISCONNECT"},
		{"wrong", "1234", tr.T("“%s” did not take the code", "Nokia 6300"), "CONNECT CONNECT DISCONNECT"},
		{"esc", "", ui.ErrCancelled, "CONNECT DISCONNECT"},
		{"none in time", "-", tr.T("No code typed in time: “%s” was not sent", "notes.txt"), "CONNECT DISCONNECT"},
	} {
		d.mu.Lock()
		d.ops = nil
		d.mu.Unlock()
		btCodeWait = time.Minute
		if tc.code == "-" {
			btCodeWait = 300 * time.Millisecond
		}
		st.req(st.app, ui.CapBTSend, ui.BTArgs{Address: addr})
		var m *menuOverlay
		st.pump("the files to send", func() bool {
			if n := len(c.overlays); n > 0 {
				m, _ = c.overlays[n-1].(*menuOverlay)
			}
			return m != nil
		})
		c.key(Key{Name: KeyEnter})
		if o := st.codeScreen(); o.name != "Nokia 6300" {
			t.Errorf("%s: asked for %q's code", tc.name, o.name)
		}
		if tc.code != "-" {
			st.typeCode(tc.code)
		}
		r := st.reply(st.app)
		if tc.want == "" && (!r.OK || !strings.Contains(string(r.Data), "notes.txt")) || tc.want != "" && r.Err != tc.want {
			t.Errorf("%s: %+v, want %q", tc.name, r, tc.want)
		}
		if ops := d.requests(); ops != tc.ops {
			t.Errorf("%s: the device got %s, want %s", tc.name, ops, tc.ops)
		}
		st.pump("the code screen gone", func() bool {
			return !slices.ContainsFunc(c.overlays, func(o overlay) bool { _, ok := o.(*codeOverlay); return ok })
		})
		d.box.mu.Lock()
		got := string(d.box.got["notes.txt"])
		d.box.got = nil
		d.box.mu.Unlock()
		if (tc.want == "") != (got == "hello") {
			t.Errorf("%s: the device got %q", tc.name, got)
		}
	}
	if strings.Contains(st.log.String(), "4711") {
		t.Errorf("the code in the log:\n%s", st.log.String())
	}
}

// An old phone whose code has letters, typed on the 9-key pad as Nokia's
// multi-tap has it: digits first (123 in the status bar); # to ABC, 123
// and abc; a key pressed again within multi-tap's time steps through its
// letters and then its digit, the letter pending shown as itself (the
// dots, the note), standing after that time, at another key or at ▶;
// Back drops it; a key held types its digit. The code goes as typed,
// capitals and all, and the move reads both services with it; the
// messages' screen takes the letters a terminal types.
func TestSwitchBluetoothCodeLetters(t *testing.T) {
	st := newSwitchTest(t)
	c := st.c
	c.ime = &imeClient{keypad: ime.T9Pad}
	c.cfg.W, c.cfg.H = 40, 24
	ph, addr := st.btOldPhone("android", "Redmi Note 9")
	ph.Password = "Ab3x"
	if _, err := st.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: ui.SwitchKinds, Bluetooth: addr}); err != nil {
		t.Fatal(err)
	}
	o := st.codeScreen()
	t0 := time.Now()
	press := func(keys string, after time.Duration, long bool) {
		t.Helper()
		for _, r := range keys {
			c.key(Key{Name: KeyRunes, Runes: []rune{r}, Long: long, At: t0.Add(after)})
		}
	}
	shows := func(code, pending, mode string) {
		t.Helper()
		f := o.frame(c)
		can := cv.New(40, 24, cv.Style{})
		o.render(c, can)
		dots := strings.Repeat("●", len(code)) + pending + strings.Repeat("○", btCodeMax-len(code)-len(pending))
		if o.code != code || o.tapped() != pending || c.inputMode() != mode || f.PIN[0] != len(code)+len(pending) ||
			!strings.Contains(can.Plain(), dots) || (pending == "") != (f.Note == "") || !strings.Contains(f.Note, pending) {
			t.Fatalf("want %q, %q pending, %s: %q, %q pending, %s, %+v\n%s", code, pending, mode, o.code, o.tapped(), c.inputMode(), f, can.Plain())
		}
	}
	shows("", "", "123")
	press("2", 0, false)
	shows("2", "", "123")
	c.key(Named(KeyBackspace))
	press("##2", 0, false)
	shows("", "A", "ABC")
	press("##", 0, false)
	shows("A", "", "abc")
	press("22", 0, false)
	shows("A", "b", "abc")
	press("2", 2*time.Second, false)
	shows("Ab", "a", "abc")
	c.key(Named(KeyBackspace))
	shows("Ab", "", "abc")
	press("77777", 3*time.Second, false)
	shows("Ab", "7", "abc")
	c.key(Named(KeyRight))
	shows("Ab7", "", "abc")
	c.key(Named(KeyBackspace))
	press("3", 4*time.Second, true)
	shows("Ab3", "", "abc")
	press("99", 5*time.Second, false)
	shows("Ab3", "x", "abc")
	st.pump("the letter standing", func() bool { return o.tap == 0 })
	shows("Ab3x", "", "abc")
	c.key(Named(KeyEnter))
	if next := st.codeScreen(); next == o || next.mode != codeDigits {
		t.Fatal("the phone book's screen stayed, or the next one starts in letters")
	}
	st.typeCode("Ab3x")
	st.moveEnded()
	s, _ := st.call(ui.CapSwitchState, nil)
	a := added(s)
	if a[ui.SwitchContacts].Stored != 3 || a[ui.SwitchCallLog].Stored != 3 || a[ui.SwitchSMS].Stored != 4 {
		t.Fatalf("after: %+v", s.Last)
	}
}

// On a keyboard the screen types letters as they come, capitals as
// capitals, 16 at most, and refuses, saying so, what is not an ASCII
// letter or digit: an accented letter's bytes would be a guess at the
// other device's. With an Fn layer a letter stays a letter (the
// digits-only screen typed its Fn digit), and the key held types the
// digit, as in a text field; the computer's long press is refused like
// its other keys. The status bar shows Fn and Shift as they are.
func TestBluetoothCodeKeyboard(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		c.SetKeypad("qwerty")
		var got string
		o := newCodeOverlay("Car kit", "", func(code string) { got = code })
		c.pushOverlay(o)
		if f := o.frame(c); f.Footer[0] != tr.T("Letter and digit keys · OK confirm · Back cancel") || c.inputMode() != "" {
			t.Errorf("footer %q, %q", f.Footer, c.inputMode())
		}
		c.Press(Runes("aB3"))
		c.Press(Runes("é-"))
		if o.code != "aB3" || o.err != tr.T("Only digits and the letters A–Z") {
			t.Errorf("typed %q, %q", o.code, o.err)
		}
		c.SetFnLayer(testFnLayer)
		c.Press(Runes("w"))
		c.Press(Key{Name: KeyRunes, Runes: []rune("w"), Long: true, Retype: true})
		c.Press(LongRunes("e"))
		c.Press(Key{Name: KeyRunes, Runes: []rune("r"), Long: true, Retype: true, Injected: true})
		if o.code != "aB312" || o.err != "" {
			t.Errorf("with an Fn layer: %q, %q", o.code, o.err)
		}
		if f := o.frame(c); f.Footer[0] != tr.T("Fn or a held key for digits · OK confirm · Back cancel") {
			t.Errorf("footer %q", f.Footer)
		}
		for _, m := range []struct{ key, off, want string }{
			{KeyFnOnce, KeyFnOff, "Fn"}, {KeyFnLock, KeyFnOff, "123"}, {KeyShiftOnce, KeyShiftOff, "Abc"}, {KeyShiftLock, KeyShiftOff, "ABC"},
		} {
			c.Press(Named(m.key))
			if got := c.inputMode(); got != m.want {
				t.Errorf("%s: %q", m.key, got)
			}
			c.Press(Named(m.off))
		}
		c.Press(Runes(strings.Repeat("z", 20)))
		c.Press(Named(KeyEnter))
		if want := "aB312" + strings.Repeat("z", btCodeMax-5); got != want {
			t.Errorf("answered %q, want %q", got, want)
		}
		return nil
	})
}

// The realm a device's challenge names shows on the screen as that
// device's words, apart from the system's lines: the pixel renderer's
// warning line, the text renderer's own line. A hostile one (escapes, line
// breaks, bidirectional overrides and zero-width characters, stacked
// marks, too long) is one line cut to its width; broken UTF-16's U+FFFD
// stays to show it; one with nothing left to show, or none, is no line.
// The device's name is shown the same way.
func TestBluetoothCodeRealm(t *testing.T) {
	st := newSwitchTest(t)
	c := st.c
	c.ime = &imeClient{keypad: ime.QWERTY}
	c.cfg.W, c.cfg.H = 40, 24
	for _, tc := range []struct{ name, realm, shownName, shown string }{
		{"Car kit", "Phone book", "Car kit", "Phone book"},
		{"Car kit", "", "Car kit", ""},
		{"Car\nkit\x1b[2J", "Kario:\n  type 0000\x1b[2J\r\nnow", "Car kit [2J", "Kario: type 0000 [2J now"},
		{"\u202eCar kit", "\u202eedoc\u202c \u200bruoy\u200d\ufeff", "Car kit", "edoc ruoy"},
		{"Car kit", "a\u0301\u0301\u0301\u0301b", "Car kit", "a\u0301\u0301b"},
		{"Car kit", "\u2028line\u2029para\u0085next\u00a0nb\t\t", "Car kit", "line para next nb"},
		{"Car kit", "\uFFFDB\uFFFD", "Car kit", "\uFFFDB\uFFFD"},
		{"Car kit", "\x00\x07\n\u200e", "Car kit", ""},
		{strings.Repeat("N", 40), strings.Repeat("Ж", 200), strings.Repeat("N", peerNameMax-1) + "…", strings.Repeat("Ж", peerRealmMax-1) + "…"},
		{"Car kit", strings.Repeat("\u8f66", 40), "Car kit", strings.Repeat("\u8f66", peerRealmMax/2-1) + "…"},
	} {
		res := make(chan bool, 1)
		go func() {
			_, _, ok := c.btAuth(tc.name, nil).Ask(obex.Challenge{Realm: tc.realm})
			res <- ok
		}()
		o := st.codeScreen()
		f := o.frame(c)
		want := ""
		if tc.shown != "" {
			want = tr.T("In that device's own words: “%s”", tc.shown)
		}
		if f.Warn != want || f.Lines[0] != tr.T("“%s” asks for a code (OBEX authentication).", tc.shownName) ||
			tc.shown != "" && slices.ContainsFunc(f.Lines, func(l string) bool { return strings.Contains(l, tc.shown) }) {
			t.Errorf("%q from %q: %q, %q", tc.realm, tc.name, f.Warn, f.Lines)
		}
		c.key(Runes("é"))
		can := cv.New(40, 24, cv.Style{})
		o.render(c, can)
		rows := strings.Split(can.Plain(), "\n")
		flat := func(s string) string {
			return strings.Map(func(r rune) rune {
				if r == ' ' || r == '║' || cv.RuneWidth(r) == 0 {
					return -1
				}
				return r
			}, s)
		}
		all := flat(strings.Join(rows, ""))
		lines, realm := strings.Index(all, flat(strings.Join(f.Lines, ""))), strings.Index(all, flat(want))
		dots, note := strings.Index(all, "○○○○"), strings.Index(all, flat(tr.T("Only digits and the letters A–Z")))
		foot := strings.Index(all, flat(tr.T("Letter and digit keys · Enter confirm · Esc cancel")+
			tr.T("The system draws this screen; only a hash of the code goes to that device.")))
		if lines < 0 || dots < lines || note < dots || foot < note || want != "" && (realm < lines || realm > dots) {
			t.Errorf("%q: the text screen\n%s", tc.realm, can.Plain())
		}
		for i, r := range rows {
			if cv.StringWidth(r) > 40 {
				t.Errorf("%q: row %d is %d wide", tc.realm, i, cv.StringWidth(r))
			}
		}
		st.typeCode("")
		st.pump("the answer", func() bool {
			select {
			case ok := <-res:
				if ok {
					t.Errorf("%q: Esc answered", tc.realm)
				}
				return true
			default:
				return false
			}
		})
	}
}

// The code screen on the three form factors as the text renderer draws
// it, in the default language, against testdata/btcode/<form>.txt
// (KARIO_UPDATE_TESTDATA=1 writes them anew): a device's realm and two
// digits and a letter typed, on the 9-key pad the letter pending and ABC
// in the status bar.
func TestBluetoothCodeSnapshots(t *testing.T) {
	for _, d := range Devices {
		c := snapCore(t)
		if c.locale.lang != i18n.DefaultLang {
			t.Fatalf("drawn in %s", c.locale.lang)
		}
		c.booted = time.Now().Add(-bootDuration)
		c.SetKeypad(d.Keypad)
		c.pushOverlay(newCodeOverlay("Redmi Note 9", "Phone book", func(string) {}))
		if d.Keypad == "t9" {
			for _, k := range []string{"1", "2", "#", "#", "3", "3"} {
				c.key(Runes(k))
			}
		} else {
			c.key(Runes("12E"))
		}
		c.snapping = &snapView{}
		got := c.Render().Plain()
		c.snapping = nil
		file := filepath.Join("testdata", "btcode", d.ID+".txt")
		if os.Getenv("KARIO_UPDATE_TESTDATA") != "" {
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%v (KARIO_UPDATE_TESTDATA=1 records it)", err)
		}
		if string(want) != got {
			t.Errorf("%s\n%s", d.ID, textDiff(file, d.ID, string(want), got))
		}
	}
}

// newCodeSend is a switchTest whose one paired device, named name, asks
// for the code 4711 before it takes a file; Files holds notes.txt.
func newCodeSend(t *testing.T, name string) (*switchTest, *codeDevice, string) {
	st := newSwitchTest(t)
	c := st.c
	c.ime = &imeClient{keypad: ime.QWERTY}
	sim := &simBT{enabled: true, confirm: func(string, uint32) bool { return true }, serve: c.obexServe, peerBox: &c.radios.obex}
	sim.addPeer(name, true)
	addr := sim.devices[0].Address
	if err := sim.pair(addr); err != nil {
		t.Fatal(err)
	}
	d := &codeDevice{simBT: sim, code: "4711"}
	c.bt = d
	if _, err := c.files.add("notes.txt", []byte("hello"), c.now()); err != nil {
		t.Fatal(err)
	}
	return st, d, addr
}

// sendFile has bt.send list the files for the device and picks the
// newest; it gives the list.
func (st *switchTest) sendFile(addr string) *menuOverlay {
	st.t.Helper()
	st.req(st.app, ui.CapBTSend, ui.BTArgs{Address: addr})
	var m *menuOverlay
	st.pump("the files to send", func() bool {
		if n := len(st.c.overlays); n > 0 {
			m, _ = st.c.overlays[n-1].(*menuOverlay)
		}
		return m != nil
	})
	st.c.key(Key{Name: KeyEnter})
	return m
}

// confirmScreen waits for a yes-or-no question on top.
func (st *switchTest) confirmScreen() *confirmOverlay {
	st.t.Helper()
	var o *confirmOverlay
	st.pump("the question", func() bool {
		if n := len(st.c.overlays); n > 0 {
			o, _ = st.c.overlays[n-1].(*confirmOverlay)
		}
		return o != nil
	})
	return o
}

// A device that ends the link while the code screen asks for its code
// takes the screen away at once, not when the user answers or the time
// is up, and the send ends as a link that ends at another moment does:
// failed, not cancelled by the user.
func TestBluetoothSendCodeDeviceGone(t *testing.T) {
	st, d, addr := newCodeSend(t, "Nokia 6300")
	d.drop = true
	defer func(w time.Duration) { btCodeWait = w }(btCodeWait)
	btCodeWait = time.Minute
	st.sendFile(addr)
	o := st.codeScreen()
	st.pump("the code screen gone", func() bool { return !slices.Contains(st.c.overlays, overlay(o)) })
	r := st.reply(st.app)
	if want := fmt.Errorf(tr.T("Sending failed: %w"), io.EOF).Error(); r.OK || r.Err != want {
		t.Errorf("%+v, want %q", r, want)
	}
	if ops := d.requests(); ops != "CONNECT" {
		t.Errorf("the device got %s", ops)
	}
}

// The link read while the code is typed: the deadline the client's last
// read left does not end the watch, what comes is the client's next reads,
// a deadline the client sets meanwhile bounds the read as it would its
// own, and the link ending closes ended at once.
func TestWatchedLink(t *testing.T) {
	phone, dev := net.Pipe()
	defer dev.Close()
	l := newWatchedLink(phone)
	_ = l.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	ended := l.watch()
	time.Sleep(60 * time.Millisecond)
	go dev.Write([]byte("answer"))
	_ = l.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, 4)
	for _, want := range []string{"answ", "er"} {
		if n, err := l.Read(got); string(got[:n]) != want || err != nil {
			t.Fatalf("read %q, %v; want %q", got[:n], err, want)
		}
	}

	if l.watch() != ended {
		t.Fatal("another channel")
	}
	_ = l.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := l.Read(got); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("past the client's deadline: %v", err)
	}
	select {
	case <-ended:
		t.Fatal("a deadline ended the link")
	default:
	}

	l.watch()
	dev.Close()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the end not seen")
	}
	if _, err := l.Read(got); !errors.Is(err, io.EOF) || !errors.Is(l.end, io.EOF) {
		t.Fatalf("after the end: %v, %v", err, l.end)
	}
}

// A device's name reaches the screens and toasts of sending a file,
// receiving one, a car kit's phone book and a move from an old phone as
// the code screen shows it: no control or format characters (line
// breaks, escapes, bidirectional overrides, zero-width ones), one line
// cut to its width.
func TestBluetoothPeerNameShown(t *testing.T) {
	const hostile = "Nokia\n6300\x1b[2J\u202e\u200bNNNNNNNNNNNNNNNNNNNNNNNNNNNNNN"
	shown := peerText(hostile, peerNameMax)
	if !strings.HasPrefix(shown, "Nokia 6300 [2J") || strings.ContainsFunc(shown, func(r rune) bool { return !unicode.IsPrint(r) }) || cv.StringWidth(shown) > peerNameMax {
		t.Fatalf("shown as %q", shown)
	}
	firstLine := func(s string) string { line, _, _ := strings.Cut(s, "\n"); return line }

	st, _, addr := newCodeSend(t, hostile)
	c := st.c
	var toasts []string
	c.onToast = func(s string) { toasts = append(toasts, s) }
	defer func(w time.Duration) { btCodeWait = w }(btCodeWait)
	btCodeWait = time.Minute
	for _, tc := range []struct{ code, err, toast string }{
		{"1234", tr.T("“%s” did not take the code", shown), ""},
		{"4711", "", tr.T("Sent “%s” to “%s”", "notes.txt", shown)},
	} {
		toasts = nil
		if m := st.sendFile(addr); m.title != tr.T("Send over Bluetooth · to “%s”", shown) {
			t.Errorf("the list %q", m.title)
		}
		if o := st.codeScreen(); o.name != shown {
			t.Errorf("the code screen names %q", o.name)
		}
		st.typeCode(tc.code)
		if r := st.reply(st.app); r.Err != tc.err {
			t.Errorf("%s: %q, want %q", tc.code, r.Err, tc.err)
		}
		want := []string{tr.T("Sending “%s” to “%s”…", "notes.txt", shown)}
		if tc.toast != "" {
			want = append(want, tc.toast)
		}
		if !slices.Equal(toasts, want) {
			t.Errorf("%s: toasts %q, want %q", tc.code, toasts, want)
		}
	}

	if err := c.peerRun(false, "card.txt", "hi"); err != nil {
		t.Fatal(err)
	}
	offer := st.confirmScreen()
	if got, want := firstLine(offer.text), firstLine(tr.T("“%s” is sending you a file over Bluetooth:\n%s · %s\nAccept it into Files?", shown, "", "")); got != want {
		t.Errorf("the offer %q, want %q", got, want)
	}
	c.key(Key{Name: KeyEsc})
	st.pump("the push refused", func() bool { return c.peerDone() != nil })
	if err := c.peerRun(true, "", ""); err != nil {
		t.Fatal(err)
	}
	book := st.confirmScreen()
	if got, want := firstLine(book.text), firstLine(tr.T("“%s” asks for your contacts and call log over Bluetooth (a car kit shows them and calls by name).\nAllow it for this device?", shown)); got != want {
		t.Errorf("the phone book's question %q, want %q", got, want)
	}
	c.key(Key{Name: KeyEsc})
	st.pump("the pull refused", func() bool { return c.peerDone() != nil })

	mv := newSwitchTest(t)
	mv.c.ime = &imeClient{keypad: ime.QWERTY}
	var said []string
	mv.c.onToast = func(s string) { said = append(said, s) }
	ph, old := mv.btOldPhone("android", hostile)
	ph.Password = "0000"
	if _, err := mv.call(ui.CapSwitchStart, ui.SwitchStartArgs{Kinds: ui.SwitchKinds, Bluetooth: old}); err != nil {
		t.Fatal(err)
	}
	mv.codeScreen()
	mv.typeCode("1234")
	mv.codeScreen()
	mv.typeCode("")
	mv.moveEnded()
	if !slices.Contains(said, tr.T("“%s” did not take the code", shown)) {
		t.Errorf("moving: toasts %q", said)
	}
	if s, _ := mv.call(ui.CapSwitchState, nil); s.Error != tr.T("“%s” did not allow it. Start again and allow it on that phone.", shown) {
		t.Errorf("moving: %q", s.Error)
	}
}
