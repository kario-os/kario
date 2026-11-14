package shell

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// A locked SIM asks for its PIN (or, after three wrong ones, its PUK and a
// new PIN) on a system screen: the digits go from this overlay to telephonyd
// and the SIM, no app sees them. Esc puts it off; it comes back when the SIM
// state changes or something tries to dial.

func (c *Core) simLocked() bool {
	return c.tel != nil && (c.tel.Sim == "pin" || c.tel.Sim == "puk")
}

// simNet is the status bar's network text when the SIM is the reason there is
// no service, "" otherwise.
func (c *Core) simNet() string {
	if c.tel == nil {
		return ""
	}
	switch c.tel.Sim {
	case "pin", "puk", "locked":
		return tr.T("SIM locked")
	case "absent":
		return tr.T("No SIM card")
	case "error":
		return tr.T("SIM card fault")
	}
	return ""
}

func (c *Core) simOverlay() *simPinOverlay {
	for _, o := range c.overlays {
		if s, ok := o.(*simPinOverlay); ok {
			return s
		}
	}
	return nil
}

// simCheck follows a status change: raise the PIN screen when the SIM asks,
// drop it when the SIM no longer does.
func (c *Core) simCheck() {
	o := c.simOverlay()
	state := ""
	if c.tel != nil {
		state = c.tel.Sim
	}
	if state != c.simSeen {
		c.simSeen, c.simLater = state, false
	}
	switch {
	case !c.simLocked() && o != nil:
		c.popOverlay(o)
	case c.simLocked() && o == nil && !c.simLater:
		c.pushOverlay(&simPinOverlay{puk: c.tel.Sim == "puk"})
	case c.simLocked() && o != nil && o.puk != (c.tel.Sim == "puk"):
		*o = simPinOverlay{puk: c.tel.Sim == "puk"}
	}
}

// askSimPin brings the PIN screen back (a dial while the SIM is locked).
func (c *Core) askSimPin() {
	c.simLater = false
	c.simCheck()
}

func (c *Core) simUnlocked(ok bool) {
	o := c.simOverlay()
	if o == nil {
		return
	}
	o.busy = false
	if !ok {
		o.wrong = true
		if o.puk {
			o.pukDigits, o.digits = "", ""
		}
	}
}

type simPinOverlay struct {
	puk       bool   // the SIM wants its PUK, then a new PIN
	pukDigits string // the PUK, once entered
	digits    string
	wrong     bool // the last attempt was refused
	busy      bool
	err       string
}

const simPinMax = 8

func (o *simPinOverlay) asking() string {
	switch {
	case !o.puk:
		return tr.T("SIM card PIN (4–8 digits)")
	case o.pukDigits == "":
		return tr.T("PUK (8 digits, from your operator)")
	}
	return tr.T("New PIN (4–8 digits)")
}

func (o *simPinOverlay) key(c *Core, k Key) {
	if o.busy {
		return
	}
	switch k.Name {
	case KeyEsc:
		c.simLater = true
		c.popOverlay(o)
	case KeyBackspace:
		if o.digits != "" {
			o.digits = o.digits[:len(o.digits)-1]
		}
	case KeyRunes:
		for _, r := range k.Runes {
			if unicode.IsDigit(r) && len(o.digits) < simPinMax {
				o.digits += string(r)
			}
		}
	case KeyEnter:
		o.submit(c)
	}
}

func (o *simPinOverlay) submit(c *Core) {
	o.err = ""
	if o.puk && o.pukDigits == "" {
		if len(o.digits) != 8 {
			o.err = tr.T("The PUK is 8 digits")
			return
		}
		o.pukDigits, o.digits, o.wrong = o.digits, "", false
		return
	}
	if len(o.digits) < 4 {
		o.err = tr.T("The PIN is 4–8 digits")
		return
	}
	args := map[string]string{"pin": o.digits}
	if o.puk {
		args["puk"] = o.pukDigits
	}
	o.digits, o.busy, o.wrong = "", true, false
	go func() {
		err := c.telRPC(svc.TelSimUnlock, args, nil)
		c.Post(func() {
			if err != nil && o.busy {
				o.busy, o.err = false, tr.T("Could not hand it to the SIM card: %v", err)
			}
		})
	}()
}

func (o *simPinOverlay) lines(c *Core) (title string, lines []string, note string, bad bool) {
	title = tr.T("SIM card locked")
	if o.puk {
		title = tr.T("SIM card blocked")
		lines = append(lines, tr.T("The PIN was entered wrong too often; unblock with the PUK and set a new PIN."))
	}
	lines = append(lines, o.asking())
	tries := 0
	if c.tel != nil {
		tries = c.tel.SimTries
	}
	switch {
	case o.busy:
		note = tr.T("Verifying…")
	case o.err != "":
		note, bad = o.err, true
	case o.wrong && o.puk:
		note, bad = tr.T("Wrong PUK, %d tries left", tries), true
	case o.wrong:
		note, bad = tr.T("Wrong PIN, %d tries left", tries), true
	case tries > 0:
		note = tr.T("%d tries left", tries)
	}
	return
}

func (o *simPinOverlay) frame(c *Core) *FrameOverlay {
	title, lines, note, bad := o.lines(c)
	return &FrameOverlay{Kind: "auth", Title: title, Lines: lines, PIN: [2]int{len(o.digits), simPinMax},
		Note: note, NoteBad: bad,
		Footer: []string{tr.T("Digit keys to enter · OK confirm · Back later"), tr.T("The system draws this screen; the PIN goes only to the SIM card.")}}
}

func (o *simPinOverlay) render(c *Core, can *cv.Canvas) {
	title, lines, note, bad := o.lines(c)
	drawPinScreen(c, can, title, lines, len(o.digits), note, bad, tr.T("Digit keys to enter · Enter confirm · Esc later"))
}

// drawPinScreen is the text renderer's PIN screen.
func drawPinScreen(c *Core, can *cv.Canvas, title string, lines []string, digits int, note string, bad bool, keys string) {
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
	can.Text(3, y, strings.Repeat("● ", digits)+strings.Repeat("○ ", simPinMax-digits), bs, W-6)
	y += 2
	if note != "" {
		sty := st(colAccent, colTrustBG)
		if bad {
			sty = bold(st(colDanger, colTrustBG))
		}
		can.Text(3, y, note, sty, W-6)
	}
	can.Text(3, H-4, keys, st(colDim, colTrustBG), W-6)
	can.Text(3, H-3, tr.T("The system draws this screen; the PIN goes only to the SIM card."), st(colDim, colTrustBG), W-6)
}

// Settings › SIM lock: turning the PIN lock on or off, or changing the PIN,
// on a system screen like the one above: the settings app asks for it, the
// digits go only to telephonyd and the SIM, and the app hears how it went.
type simSetOverlay struct {
	op       string // on | off | change
	step     int    // change: 0 the PIN now, 1 the new one, 2 the new one again
	old, new string
	digits   string
	wrong    bool
	busy     bool
	err      string
	done     func(error)
}

// simPinSetting raises it; done gets nil once the SIM took the change, or
// why not (cancelled, wrong PIN given up on).
func (c *Core) simPinSetting(op string, done func(error)) {
	switch {
	case c.tel == nil:
		done(fmt.Errorf("no baseband"))
		return
	case c.tel.Sim != "ready":
		done(fmt.Errorf("the SIM is not ready (%s)", c.tel.Sim))
		return
	case op == "change" && !c.tel.PinLock:
		done(fmt.Errorf("turn the PIN lock on first"))
		return
	}
	c.pushOverlay(&simSetOverlay{op: op, done: done})
}

func (c *Core) simSetting() *simSetOverlay {
	for _, o := range c.overlays {
		if s, ok := o.(*simSetOverlay); ok {
			return s
		}
	}
	return nil
}

// simPinSet follows telephonyd's pin event.
func (c *Core) simPinSet(ok bool, why string) {
	o := c.simSetting()
	if o == nil {
		return
	}
	switch {
	case ok:
		if o.op != "change" && c.tel != nil {
			c.tel.PinLock = o.op == "on"
		}
		c.popOverlay(o)
		c.showToast(map[string]string{"on": tr.T("SIM lock on: the PIN is asked at power-on"), "off": tr.T("SIM lock off"), "change": tr.T("PIN changed")}[o.op])
		o.done(nil)
	case c.simLocked():
		// Out of tries: the SIM wants its PUK now, on the unlock screen.
		c.popOverlay(o)
		o.done(fmt.Errorf("the SIM locked itself: %s", why))
	default:
		*o = simSetOverlay{op: o.op, done: o.done, wrong: true}
	}
}

func (o *simSetOverlay) asking() string {
	switch o.step {
	case 1:
		return tr.T("New PIN (4–8 digits)")
	case 2:
		return tr.T("The new PIN once more")
	}
	return tr.T("The SIM card's current PIN (4–8 digits)")
}

func (o *simSetOverlay) key(c *Core, k Key) {
	if o.busy {
		return
	}
	switch k.Name {
	case KeyEsc:
		c.popOverlay(o)
		o.done(errors.New(ui.ErrCancelled))
	case KeyBackspace:
		if o.digits != "" {
			o.digits = o.digits[:len(o.digits)-1]
		}
	case KeyRunes:
		for _, r := range k.Runes {
			if unicode.IsDigit(r) && len(o.digits) < simPinMax {
				o.digits += string(r)
			}
		}
	case KeyEnter:
		o.submit(c)
	}
}

func (o *simSetOverlay) submit(c *Core) {
	o.err = ""
	if len(o.digits) < 4 {
		o.err = tr.T("The PIN is 4–8 digits")
		return
	}
	digits := o.digits
	o.digits, o.wrong = "", false
	method, args := svc.TelSimPinLock, map[string]any{"pin": digits, "on": o.op == "on"}
	if o.op == "change" {
		switch o.step {
		case 0:
			o.old, o.step = digits, 1
			return
		case 1:
			o.new, o.step = digits, 2
			return
		}
		if digits != o.new {
			o.err, o.step, o.new = tr.T("The two new PINs differ; enter it again"), 1, ""
			return
		}
		method, args = svc.TelSimChangePin, map[string]any{"pin": o.old, "new_pin": o.new}
	}
	o.busy = true
	go func() {
		err := c.telRPC(method, args, nil)
		c.Post(func() {
			if err != nil && o.busy {
				o.busy, o.step, o.err = false, 0, tr.T("Could not hand it to the SIM card: %v", err)
			}
		})
	}()
}

func (o *simSetOverlay) lines() (title string, lines []string, note string, bad bool) {
	title = map[string]string{"on": tr.T("Turn the SIM lock on"), "off": tr.T("Turn the SIM lock off"), "change": tr.T("Change the PIN")}[o.op]
	switch o.op {
	case "on":
		lines = append(lines, tr.T("Once on, the PIN is asked at every power-on, and a lost SIM card is useless to others."))
	case "off":
		lines = append(lines, tr.T("Once off, no PIN is asked at power-on."))
	}
	lines = append(lines, o.asking())
	switch {
	case o.busy:
		note = tr.T("Handing it to the SIM card…")
	case o.err != "":
		note, bad = o.err, true
	case o.wrong:
		note, bad = tr.T("Wrong PIN; three wrong in a row and the PUK is needed to unblock"), true
	}
	return
}

func (o *simSetOverlay) frame(c *Core) *FrameOverlay {
	title, lines, note, bad := o.lines()
	return &FrameOverlay{Kind: "auth", Title: title, Lines: lines, PIN: [2]int{len(o.digits), simPinMax},
		Note: note, NoteBad: bad,
		Footer: []string{tr.T("Digit keys to enter · OK confirm · Back cancel"), tr.T("The system draws this screen; the PIN goes only to the SIM card.")}}
}

func (o *simSetOverlay) render(c *Core, can *cv.Canvas) {
	title, lines, note, bad := o.lines()
	drawPinScreen(c, can, title, lines, len(o.digits), note, bad, tr.T("Digit keys to enter · Enter confirm · Esc cancel"))
}
