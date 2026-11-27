package shell

import (
	"strings"
	"time"
	"unicode"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/obex"
)

// A Bluetooth device whose OBEX authentication asks for a code (an old
// phone Kario moves from, a car kit reading the phone book, a device a
// file is sent to) has the user type it on a system screen like the SIM
// PIN's: the code the device shows, or the same code its user types
// there. Only its hash goes to the device; Esc, or no code in time,
// leaves the device's Unauthorized.
//
// A code is ASCII letters and digits, kept as typed: the digest takes its
// bytes, and IrOBEX names no character set for them, so a capital makes
// another code, and a letter beyond ASCII, whose bytes the other device
// may write otherwise, is not taken. The 9-key pad types digits first, as
// most codes are, and # steps to multi-tap letters, small and capital; a
// keyboard types what its keys do (with an Fn layer, digits as in a text
// field: Fn, or the key held). The realm the device's challenge names is
// shown as its words, not the system's.

// btCodeMax is a code's length at most (a Bluetooth PIN's, an Android
// session key's).
const btCodeMax = 16

// btCodeWait is how long a code screen waits for the code.
var btCodeWait = offerTimeout

// The 9-key pad's modes on the screen, as the status bar names them.
const (
	codeDigits = "123"
	codeSmall  = "abc"
	codeCaps   = "ABC"
)

// How wide the screen shows the device's name and its realm at most.
const (
	peerNameMax  = 24
	peerRealmMax = 32
)

// btAuth answers the challenges of the device named name; closing gone
// (nil: never) takes a code screen still up away.
func (c *Core) btAuth(name string, gone <-chan struct{}) obex.Auth {
	return obex.Auth{Ask: func(ch obex.Challenge) (string, string, bool) {
		code, _ := c.askCode(name, ch.Realm, gone)
		return "", code, code != ""
	}}
}

// askCode asks the user for the code from a session's goroutine: none
// ("") when the user cancels, gone closes or the time is up (late).
func (c *Core) askCode(name, realm string, gone <-chan struct{}) (code string, late bool) {
	answer := make(chan string, 1)
	o := newCodeOverlay(name, realm, func(code string) { answer <- code })
	c.Post(func() { c.pushOverlay(o) })
	select {
	case typed := <-answer:
		return typed, false
	case <-gone:
	case <-time.After(btCodeWait):
		late = true
	}
	c.Post(func() { c.popOverlay(o) })
	return "", late
}

type codeOverlay struct {
	name, realm string
	code        string
	err         string
	// The 9-key pad's mode, and the letter mode a long # goes back to.
	mode, back string
	// The pad's key whose letter is pending (0: none), pressed taps times
	// more, last at tapAt; armed counts the timers that make it stand.
	tap   rune
	taps  int
	tapAt time.Time
	armed int
	done  func(code string)
}

func newCodeOverlay(name, realm string, done func(code string)) *codeOverlay {
	return &codeOverlay{name: peerText(name, peerNameMax), realm: peerText(realm, peerRealmMax), mode: codeDigits, done: done}
}

// codeOnTop is the code screen when it is the top overlay.
func (c *Core) codeOnTop() *codeOverlay {
	if n := len(c.overlays); n > 0 {
		o, _ := c.overlays[n-1].(*codeOverlay)
		return o
	}
	return nil
}

func nineKey(c *Core) bool { return c.ime.Keypad() == ime.T9Pad }

// codeChar: r may be in a code.
func codeChar(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

// codeLetters is what key r of the 9-key pad steps through on the screen:
// its letters, then its digit; key 1 has its digit only.
func codeLetters(r rune) string {
	return strings.Map(func(r rune) rune {
		if codeChar(r) {
			return r
		}
		return -1
	}, ime.MultiTap(r))
}

func (o *codeOverlay) key(c *Core, k Key) {
	switch k.Name {
	case KeyEsc:
		c.popOverlay(o)
		o.done("")
	case KeyBackspace:
		switch {
		case o.tap != 0:
			o.tap = 0
		case o.code != "":
			o.code = o.code[:len(o.code)-1]
		}
	case KeyRight:
		o.stand()
	case KeyRunes:
		o.err = ""
		at := k.At
		if at.IsZero() {
			at = time.Now()
		}
		for _, r := range k.Runes {
			if nineKey(c) && (r >= '0' && r <= '9' || r == '#' || r == '*') {
				o.pad(c, r, k.Long, at)
				continue
			}
			o.stand()
			o.add(r)
		}
	case KeyEnter:
		o.stand()
		if o.code == "" {
			o.err = tr.T("Type the code first")
			return
		}
		c.popOverlay(o)
		o.done(o.code)
	}
}

// pad is a key of the 9-key pad, as Nokia's multi-tap has it: in 123 its
// digit; in abc and ABC a key with letters starts its first, and pressed
// again within multi-tap's time steps to the next (its digit last); key 0,
// key 1 and a key held type their digit. # steps 123 → abc → ABC, held it
// goes to 123 and back.
func (o *codeOverlay) pad(c *Core, r rune, long bool, at time.Time) {
	letters := codeLetters(r)
	switch {
	case r == '#' && long:
		o.stand()
		if o.mode == codeDigits {
			o.mode = codeSmall
			if o.back != "" {
				o.mode = o.back
			}
		} else {
			o.back, o.mode = o.mode, codeDigits
		}
	case r == '#':
		o.stand()
		o.mode = map[string]string{codeDigits: codeSmall, codeSmall: codeCaps, codeCaps: codeDigits}[o.mode]
	case r == '*':
	case long || o.mode == codeDigits || len(letters) < 2:
		o.stand()
		o.add(r)
	case o.tap == r && at.Sub(o.tapAt) < ime.MultiTapTimeout:
		o.taps++
		o.tapAt = at
		o.arm(c)
	default:
		o.stand()
		if len(o.code) < btCodeMax {
			o.tap, o.taps, o.tapAt = r, 0, at
			o.arm(c)
		}
	}
}

// arm makes the pending letter stand once multi-tap's time has passed
// without its key.
func (o *codeOverlay) arm(c *Core) {
	o.armed++
	n := o.armed
	time.AfterFunc(ime.MultiTapTimeout, func() {
		c.Post(func() {
			if o.armed == n {
				o.stand()
			}
		})
	})
}

// tapped is the pending letter, "" for none.
func (o *codeOverlay) tapped() string {
	if o.tap == 0 {
		return ""
	}
	s := codeLetters(o.tap)
	r := rune(s[o.taps%len(s)])
	if o.mode == codeCaps {
		r = unicode.ToUpper(r)
	}
	return string(r)
}

// stand makes the pending letter part of the code.
func (o *codeOverlay) stand() {
	o.code += o.tapped()
	o.tap = 0
}

func (o *codeOverlay) add(r rune) {
	switch {
	case !codeChar(r):
		o.err = tr.T("Only digits and the letters A–Z")
	case len(o.code) < btCodeMax:
		o.code += string(r)
	}
}

// typed counts the code's characters, the pending letter's too.
func (o *codeOverlay) typed() int {
	if o.tap != 0 {
		return len(o.code) + 1
	}
	return len(o.code)
}

// shownMode is the status bar's input mode while the screen is up: the
// 9-key pad's, or on a keyboard Fn or Shift tapped or locked.
func (o *codeOverlay) shownMode(c *Core) string {
	switch {
	case nineKey(c):
		return o.mode
	case c.fnMode == fnOnce:
		return "Fn"
	case c.fnMode == fnLock:
		return "123"
	case c.shiftMode == fnOnce:
		return "Abc"
	case c.shiftMode == fnLock:
		return "ABC"
	}
	return ""
}

func (o *codeOverlay) lines() (title string, lines []string) {
	return tr.T("Bluetooth code"), []string{tr.T("“%s” asks for a code (OBEX authentication).", o.name),
		tr.T("Type the code it shows, or the same code you type on it."), tr.T("Digits and the letters A–Z; capitals count.")}
}

// realmLine is the realm, said to be the device's.
func (o *codeOverlay) realmLine() string {
	return tr.T("In that device's own words: “%s”", o.realm)
}

// note is what went wrong, or else the pending letter, which the pixel
// renderer's dots do not show.
func (o *codeOverlay) note() (string, bool) {
	if o.err != "" {
		return o.err, true
	}
	if t := o.tapped(); t != "" {
		return tr.T("“%s”: press %s again for the next", t, string(o.tap)), false
	}
	return "", false
}

// keysHint is the key line: OK and Back for the pixel renderer, Enter and
// Esc for the text renderer's.
func (o *codeOverlay) keysHint(c *Core, text bool) string {
	switch fn := len(c.cfg.SKU.Fn) > 0; {
	case nineKey(c) && text:
		return tr.T("# for 123 / abc / ABC · Enter confirm · Esc cancel")
	case nineKey(c):
		return tr.T("# for 123 / abc / ABC · OK confirm · Back cancel")
	case fn && text:
		return tr.T("Fn or a held key for digits · Enter confirm · Esc cancel")
	case fn:
		return tr.T("Fn or a held key for digits · OK confirm · Back cancel")
	case text:
		return tr.T("Letter and digit keys · Enter confirm · Esc cancel")
	}
	return tr.T("Letter and digit keys · OK confirm · Back cancel")
}

func (o *codeOverlay) frame(c *Core) *FrameOverlay {
	title, lines := o.lines()
	note, bad := o.note()
	f := &FrameOverlay{Kind: "auth", Title: title, Lines: lines, PIN: [2]int{o.typed(), btCodeMax}, Note: note, NoteBad: bad,
		Footer: []string{o.keysHint(c, false), tr.T("The system draws this screen; only a hash of the code goes to that device.")}}
	if o.realm != "" {
		f.Warn = o.realmLine()
	}
	return f
}

func (o *codeOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	title, lines := o.lines()
	bs := st(colFG, colTrustBG)
	can.Box(0, 1, W, H-1, st(colTrusted, colTrustBG), true)
	can.Text(2, 2, "■ "+title, bold(st(colTrusted, colTrustBG)), W-4)
	foot := append(cv.Wrap(o.keysHint(c, true), W-6),
		cv.Wrap(tr.T("The system draws this screen; only a hash of the code goes to that device."), W-6)...)
	end := H - 1 - len(foot)
	for i, l := range foot {
		can.Text(3, end+i, l, st(colDim, colTrustBG), W-6)
	}
	y := 4
	put := func(s string, sty cv.Style) {
		for _, w := range cv.Wrap(s, W-6) {
			if y < end {
				can.Text(3, y, w, sty, W-6)
			}
			y++
		}
	}
	for _, l := range lines {
		put(l, bs)
	}
	if o.realm != "" {
		y++
		put(o.realmLine(), st(colWarn, colTrustBG))
	}
	y++
	put(strings.Repeat("●", len(o.code))+o.tapped()+strings.Repeat("○", btCodeMax-o.typed()), bs)
	if note, bad := o.note(); note != "" {
		sty := st(colAccent, colTrustBG)
		if bad {
			sty = bold(st(colDanger, colTrustBG))
		}
		put(note, sty)
	}
}

// peerText is the other device's text as the screen shows it: one line at
// most w columns wide, without what could pass for the screen's own
// layout: control and format characters (line breaks, escapes,
// bidirectional overrides) go, a run of spaces is one, and a character
// takes two combining marks at most.
func peerText(s string, w int) string {
	var b strings.Builder
	marks, space := 0, false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r) || unicode.IsControl(r):
			space, marks = b.Len() > 0, 0
			continue
		case !unicode.IsPrint(r):
			continue
		case unicode.In(r, unicode.Mn, unicode.Me):
			if marks++; marks > 2 {
				continue
			}
		default:
			marks = 0
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return cv.Truncate(b.String(), w)
}

// peerName is a device's name as a screen or a toast shows it (peerText),
// its address when nothing of the name is left.
func peerName(name, addr string) string {
	return firstNonEmpty(peerText(name, peerNameMax), addr)
}
