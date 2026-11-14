package shell

import (
	"errors"
	"strings"
	"time"
	"unicode"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The lock's PIN screens. The digits go from these overlays to
// phone-keystored and are dropped once sent: the shell keeps no PIN.

// pinWrong reads a refused PIN: what to say, and until when the next try
// must wait.
func pinWrong(r keystore.PINResult) (string, time.Time) {
	if r.WaitMS > 0 {
		return "", time.Now().Add(time.Duration(r.WaitMS) * time.Millisecond)
	}
	if r.Left > 0 {
		return tr.T("Wrong PIN: %d more tries before a wait", r.Left), time.Time{}
	}
	return tr.T("Wrong PIN"), time.Time{}
}

// pinNote is the note to show now: a wait counts down.
func pinNote(note string, bad bool, until time.Time) (string, bool) {
	if w := time.Until(until); w > 0 {
		return tr.T("Too many wrong PINs: try again in %d s", int((w+time.Second-1)/time.Second)), true
	}
	return note, bad
}

func pinDigit(digits string, k Key) string {
	for _, r := range k.Runes {
		if unicode.IsDigit(r) && len(digits) < keystore.MaxPIN {
			digits += string(r)
		}
	}
	return digits
}

// pinSlots is how many places the PIN line shows: the PIN's length is not
// known before it is typed.
func pinSlots(n int) int { return max(keystore.MinPIN, n) }

// drawLockPin is the text renderer's PIN screen of the lock.
func drawLockPin(c *Core, can *cv.Canvas, title string, lines []string, digits, slots int, note string, bad bool, foot []string) {
	W, H := c.cfg.W, c.cfg.H
	bs := st(colFG, colTrustBG)
	can.Box(0, 1, W, H-1, st(colTrusted, colTrustBG), true)
	can.Text(2, 2, "■ "+title, bold(st(colTrusted, colTrustBG)), W-4)
	y := 4
	for _, l := range lines {
		for _, w := range cv.Wrap(l, W-6) {
			can.Text(3, y, w, bs, W-6)
			y++
		}
	}
	y++
	can.Text(3, y, strings.Repeat("● ", digits)+strings.Repeat("○ ", max(0, slots-digits)), bs, W-6)
	y += 2
	if note != "" {
		sty := st(colAccent, colTrustBG)
		if bad {
			sty = bold(st(colDanger, colTrustBG))
		}
		for _, w := range cv.Wrap(note, W-6) {
			can.Text(3, y, w, sty, W-6)
			y++
		}
	}
	for i, f := range foot {
		can.Text(3, H-2-len(foot)+i, cv.Truncate(f, W-6), st(colDim, colTrustBG), W-6)
	}
}

// lockPadOverlay is the lock screen's PIN pad. The call key dials what is
// typed if it is an emergency number, as the keypad lock always allowed;
// the digits are never shown, so a PIN starting like one gives nothing away.
type lockPadOverlay struct {
	digits    string
	emergency bool // opened by the call key
	busy      bool
	note      string
	bad       bool
	waitUntil time.Time
}

func (c *Core) openLockPad(first string, emergency bool) {
	for _, o := range c.overlays {
		if p, ok := o.(*lockPadOverlay); ok {
			p.digits = first
			return
		}
	}
	c.pushOverlay(&lockPadOverlay{digits: first, emergency: emergency})
}

func (o *lockPadOverlay) key(c *Core, k Key) {
	if o.busy {
		return
	}
	switch k.Name {
	case KeyEsc:
		o.digits = ""
		c.popOverlay(o)
	case KeyBackspace:
		if o.digits != "" {
			o.digits = o.digits[:len(o.digits)-1]
		}
	case KeyRunes:
		o.digits = pinDigit(o.digits, k)
	case KeyEnter:
		o.submit(c)
	case KeyCall:
		if emergencyNumbers[o.digits] {
			n := o.digits
			o.digits = ""
			c.popOverlay(o)
			c.emergencyCall(n)
			return
		}
		o.note, o.bad = tr.T("Only emergency numbers can be called while locked"), true
	}
}

func (o *lockPadOverlay) submit(c *Core) {
	if time.Until(o.waitUntil) > 0 {
		return
	}
	if len(o.digits) < keystore.MinPIN {
		o.digits = ""
		o.note, o.bad = tr.T("The PIN is %d to %d digits", keystore.MinPIN, keystore.MaxPIN), true
		return
	}
	pin := o.digits
	o.digits, o.busy, o.note, o.bad = "", true, "", false
	var r keystore.PINResult
	c.broker.call(svc.Keystored, keystore.MethodLockVerify, map[string]any{"pin": pin}, &r, func(err error) {
		o.busy = false
		switch {
		case err != nil:
			o.note, o.bad = tr.T("Could not check the PIN: %v", err), true
		case r.OK:
			c.unlockNow("PIN")
			c.lockRefresh(nil)
		default:
			o.note, o.waitUntil = pinWrong(r)
			o.bad = true
		}
	})
}

func (o *lockPadOverlay) lines(c *Core) (lines []string, note string, bad bool) {
	lines = append(lines, tr.T("The phone is locked; the lock-screen PIN unlocks it."))
	if c.fingerUnlockReady() {
		lines = append(lines, tr.T("A fingerprint unlocks it too."))
	}
	if o.emergency {
		lines = append(lines, tr.T("For an emergency call, type the number and press Call."))
	}
	note, bad = pinNote(o.note, o.bad, o.waitUntil)
	if o.busy {
		note, bad = tr.T("Verifying…"), false
	}
	return
}

func (o *lockPadOverlay) frame(c *Core) *FrameOverlay {
	lines, note, bad := o.lines(c)
	return &FrameOverlay{Kind: "auth", Title: tr.T("Enter the PIN"), Lines: lines, PIN: [2]int{len(o.digits), pinSlots(len(o.digits))},
		Note: note, NoteBad: bad,
		Footer: []string{tr.T("Digit keys to enter · OK unlock · Back cancel"), tr.T("Call key: emergency numbers only")}}
}

func (o *lockPadOverlay) render(c *Core, can *cv.Canvas) {
	lines, note, bad := o.lines(c)
	drawLockPin(c, can, tr.T("Enter the PIN"), lines, len(o.digits), pinSlots(len(o.digits)), note, bad,
		[]string{tr.T("Digit keys to enter · Enter unlock · Esc cancel"), tr.T("Call key: emergency numbers only")})
}

// lockSetOverlay is Settings › Security's PIN screen: setting, changing or
// removing the lock-screen PIN, or confirming it to manage fingerprints
// (its token answers challenge). The settings app hears only how it went.
type lockSetOverlay struct {
	op        string // set | change | remove | confirm
	step      int    // 0 the PIN in force, 1 the new one, 2 the new one again
	new       string
	old       string
	digits    string
	busy      bool
	note      string
	bad       bool
	waitUntil time.Time
	challenge uint64
	done      func(token string, err error)
}

func (c *Core) askLockPIN(op string, challenge uint64, done func(token string, err error)) {
	o := &lockSetOverlay{op: op, challenge: challenge, done: done}
	if op == "set" {
		o.step = 1
	}
	c.pushOverlay(o)
}

func (o *lockSetOverlay) key(c *Core, k Key) {
	if o.busy {
		return
	}
	switch k.Name {
	case KeyEsc:
		o.digits, o.old, o.new = "", "", ""
		c.popOverlay(o)
		o.done("", errors.New(ui.ErrCancelled))
	case KeyBackspace:
		if o.digits != "" {
			o.digits = o.digits[:len(o.digits)-1]
		}
	case KeyRunes:
		o.digits = pinDigit(o.digits, k)
	case KeyEnter:
		o.submit(c)
	}
}

func (o *lockSetOverlay) submit(c *Core) {
	if time.Until(o.waitUntil) > 0 {
		return
	}
	if len(o.digits) < keystore.MinPIN {
		o.digits = ""
		o.note, o.bad = tr.T("The PIN is %d to %d digits", keystore.MinPIN, keystore.MaxPIN), true
		return
	}
	d := o.digits
	o.digits, o.note, o.bad = "", "", false
	switch {
	case o.step == 0 && o.op == "change":
		o.old, o.step = d, 1
		return
	case o.step == 1:
		o.new, o.step = d, 2
		return
	case o.step == 2 && d != o.new:
		o.new, o.step = "", 1
		o.note, o.bad = tr.T("The two PINs differ; enter the new PIN again"), true
		return
	}
	method, args := keystore.MethodLockVerify, map[string]any{"pin": d, "challenge": o.challenge}
	switch o.op {
	case "set", "change":
		method, args = keystore.MethodLockSetPIN, map[string]any{"old": o.old, "pin": o.new}
	case "remove":
		method, args = keystore.MethodLockClearPIN, map[string]any{"old": d}
	}
	o.old, o.new, o.busy = "", "", true
	var r keystore.PINResult
	c.broker.call(svc.Keystored, method, args, &r, func(err error) {
		o.busy = false
		switch {
		case err != nil:
			o.step = map[bool]int{true: 1, false: 0}[o.op == "set"]
			o.note, o.bad = tr.T("Could not set the PIN: %v", err), true
		case r.OK:
			c.popOverlay(o)
			switch o.op {
			case "set":
				c.showToast(tr.T("Lock-screen PIN set: the phone locks when the screen goes off"))
			case "change":
				c.showToast(tr.T("Lock-screen PIN changed"))
			case "remove":
				c.showToast(tr.T("Lock-screen PIN removed, and the fingerprints with it"))
			}
			o.done(r.Token, nil)
		default:
			o.step = 0
			o.note, o.waitUntil = pinWrong(r)
			o.bad = true
		}
	})
}

func (o *lockSetOverlay) title() string {
	return map[string]string{"set": tr.T("Set a lock-screen PIN"), "change": tr.T("Change the lock-screen PIN"),
		"remove": tr.T("Remove the lock-screen PIN"), "confirm": tr.T("Confirm your PIN")}[o.op]
}

func (o *lockSetOverlay) lines() (lines []string, note string, bad bool) {
	switch o.op {
	case "set":
		lines = append(lines, tr.T("With a PIN the phone locks when the screen goes off; the PIN or a fingerprint unlocks it."))
	case "remove":
		lines = append(lines, tr.T("Without a PIN the phone no longer locks itself, and the fingerprints are deleted too."))
	case "confirm":
		lines = append(lines, tr.T("Enter the lock-screen PIN to manage fingerprints."))
	}
	switch o.step {
	case 1:
		lines = append(lines, tr.T("New PIN (%d to %d digits)", keystore.MinPIN, keystore.MaxPIN))
	case 2:
		lines = append(lines, tr.T("The new PIN once more"))
	default:
		lines = append(lines, tr.T("The lock-screen PIN in force"))
	}
	note, bad = pinNote(o.note, o.bad, o.waitUntil)
	if o.busy {
		note, bad = tr.T("Verifying…"), false
	}
	return
}

func (o *lockSetOverlay) frame(c *Core) *FrameOverlay {
	lines, note, bad := o.lines()
	return &FrameOverlay{Kind: "auth", Title: o.title(), Lines: lines, PIN: [2]int{len(o.digits), pinSlots(len(o.digits))},
		Note: note, NoteBad: bad,
		Footer: []string{tr.T("Digit keys to enter · OK confirm · Back cancel"), tr.T("The system draws this screen; the PIN goes only to the key service.")}}
}

func (o *lockSetOverlay) render(c *Core, can *cv.Canvas) {
	lines, note, bad := o.lines()
	drawLockPin(c, can, o.title(), lines, len(o.digits), pinSlots(len(o.digits)), note, bad,
		[]string{tr.T("Digit keys to enter · Enter confirm · Esc cancel"), tr.T("The system draws this screen; the PIN goes only to the key service.")})
}
