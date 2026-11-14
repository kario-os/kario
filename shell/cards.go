package shell

import (
	"fmt"
	"strings"
	"time"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The quick card view and the countdown, system UI because they show over
// the lock screen. A double press of the power key (PowerKey), or of the
// end key on standby where the board has no power key, opens the view
// over the lock or standby screen, as the side key's double press opens
// the wallet on Xiaomi's phones and the iPhone: for the rare time the
// card chosen by itself is wrong, choosing one makes it the one readers
// see for presentFor, with a countdown. The card pack itself is a system
// app (apps/cards): its "present" comes here too.

const presentFor = 60 * time.Second

// doublePress: a second press within it makes a double press.
const doublePress = 400 * time.Millisecond

// cardPickKey sees every key first (Core.Key), once the key has lit the
// screen. The end key is the power key too where the board has none:
// held, it opens the power menu; pressed twice within doublePress on the
// standby or lock screen, after a pause, it opens the quick card view.
// There its first press keeps its meaning (it lights the screen, or is
// on standby already); a press from an app or a call goes back or hangs
// up and starts nothing, and a burst of presses (the red key pressed to
// get out of something) never makes a double press.
func (c *Core) cardPickKey(k Key) bool {
	if k.Name != KeyF4 || c.powerKey {
		return false
	}
	if k.Long && !c.endKeyBusy() {
		c.nfc.endAt, c.nfc.endStart = time.Time{}, false
		c.openPowerMenu()
		return true
	}
	now := time.Now()
	gap := now.Sub(c.nfc.endAt)
	start := c.nfc.endStart
	c.nfc.endAt, c.nfc.endStart = now, false
	if k.Long || c.nfc.noPicker || !c.cardViewOK() || !c.onStandby() {
		return false
	}
	if start && gap <= doublePress {
		c.openCardPick()
		return true
	}
	c.nfc.endStart = gap > doublePress
	return false
}

// endKeyBusy: a call, a ringing call or an alarm, where the end key means
// something of its own.
func (c *Core) endKeyBusy() bool {
	if n := len(c.overlays); n > 0 && c.endKeyOwn(c.overlays[n-1]) {
		return true
	}
	return c.anyCall() || c.ringing()
}

// cardViewOK: the quick view may open (NFC is there, nothing busy).
func (c *Core) cardViewOK() bool { return c.nfc.up && !c.endKeyBusy() }

// onStandby: the standby or lock screen with nothing over it.
func (c *Core) onStandby() bool {
	return c.fg == nil && len(c.overlays) == 0 && len(c.sys.stack) == 1 && c.sys.stack[0].S.ID == "sys.idle"
}

// SetEndKeyPicker turns the end key's double press on or off (selftest:
// scripts start with it off, as their end keys come faster than a
// person's, one case's last right before the next case's first).
func (c *Core) SetEndKeyPicker(on bool) {
	c.nfc.noPicker, c.nfc.endAt, c.nfc.endStart = !on, time.Time{}, false
}

// refreshCards fetches the cards from nfcd for the quick view.
func (c *Core) refreshCards() {
	go func() {
		var st ui.NFCState
		if err := c.nfcRPC(svc.NFCState, nil, &st); err != nil {
			return
		}
		c.Post(func() { c.nfc.state = st })
	}()
}

func (c *Core) cardByID(id string) (ui.NFCCardInfo, bool) {
	for _, cd := range c.nfc.state.Cards {
		if cd.ID == id {
			return cd, true
		}
	}
	return ui.NFCCardInfo{}, false
}

// openCardPick is the quick view, in the card pack's order, the door
// card a door gets by itself now marked: choosing a card presents it. It
// asks nfcd first (the mark moves with the time of use), and opens with
// the cards it knew when nfcd does not answer.
func (c *Core) openCardPick() {
	go func() {
		var st ui.NFCState
		err := c.nfcRPC(svc.NFCState, nil, &st)
		c.Post(func() {
			if err == nil {
				c.nfc.state = st
			}
			c.showCardPick()
		})
	}()
}

func (c *Core) showCardPick() {
	title := tr.T("Cards")
	if n := len(c.overlays); n > 0 {
		if m, ok := c.overlays[n-1].(*menuOverlay); ok && m.title == title {
			return
		}
	}
	m := &menuOverlay{title: title}
	for i, cd := range c.nfc.state.Cards {
		cd := cd
		what := cd.AppName
		switch {
		case cd.Kind == ui.NFCDoor && cd.ID == c.nfc.state.DoorFirst:
			what = tr.T("Door card · auto")
		case cd.Kind == ui.NFCDoor:
			what = tr.T("Door card")
		case cd.Kind == ui.NFCPayment && c.locked():
			what = tr.T("unlock to pay")
		}
		key := ""
		if i < 9 {
			key = fmt.Sprint(i + 1)
		}
		m.entries = append(m.entries, menuEntry{label: cd.Name + " · " + what, find: cd.Name, name: cd.Name, key: key,
			run: func() { c.presentCard(cd.ID, nil) }})
	}
	if len(m.entries) == 0 {
		c.showToast(tr.T("No cards in the card pack yet"))
		return
	}
	c.pushOverlay(m)
}

// presentCard presents a card for presentFor with the countdown ("" stops
// presenting); done, when not nil, hears how it went (the card pack's
// nfc.set "present").
func (c *Core) presentCard(id string, done func(error)) {
	if done == nil {
		done = func(error) {}
	}
	if id == "" {
		if o := c.nfc.present; o != nil {
			c.stopPresenting(o)
		}
		done(nil)
		return
	}
	go func() {
		var st ui.NFCState
		stErr := c.nfcRPC(svc.NFCState, nil, &st)
		err := c.nfcRPC(svc.NFCPresent, map[string]any{"card": id, "seconds": int(presentFor / time.Second)}, nil)
		c.Post(func() {
			if stErr == nil {
				c.nfc.state = st
			}
			cd, _ := c.cardByID(id)
			switch {
			case err == nil:
				if c.nfc.present != nil {
					c.popOverlay(c.nfc.present)
				}
				c.nfc.present = &presentOverlay{card: id, name: cd.Name, until: time.Now().Add(presentFor)}
				c.pushOverlay(c.nfc.present)
			case strings.HasSuffix(err.Error(), "locked"):
				c.showToast(tr.T("Unlock the phone to use “%s”", cd.Name))
			case strings.HasSuffix(err.Error(), "off"):
				c.showToast(tr.T("NFC is off · Settings › NFC"))
			case strings.HasSuffix(err.Error(), "default"):
				c.showToast(tr.T("“%s” is not the default payment app's card · choose it in Cards", cd.Name))
			default:
				c.showToast(err.Error())
			}
			done(err)
		})
	}()
}

// checkPresent ends the presentation when its minute is up, or a moment
// after a reader took the card.
func (c *Core) checkPresent() {
	o := c.nfc.present
	if o == nil {
		return
	}
	if time.Now().After(o.until) || (!o.done.IsZero() && time.Since(o.done) > 1500*time.Millisecond) {
		c.stopPresenting(o)
	}
}

func (c *Core) stopPresenting(o *presentOverlay) {
	c.popOverlay(o)
	if c.nfc.present == o {
		c.nfc.present = nil
	}
	if o.done.IsZero() {
		go func() { _ = c.nfcRPC(svc.NFCPresent, map[string]any{"card": ""}, nil) }()
	}
}

// presentOverlay: the card readers see now, and for how long.
type presentOverlay struct {
	card, name string
	until      time.Time
	done       time.Time
}

func (o *presentOverlay) left() int {
	return max(0, int((time.Until(o.until)+time.Second-1)/time.Second))
}

func (o *presentOverlay) lines() []string {
	if !o.done.IsZero() {
		return []string{o.name, tr.T("Done ✓")}
	}
	return []string{o.name, tr.T("Hold near the reader"), tr.T("%d s left", o.left())}
}

func (o *presentOverlay) key(c *Core, k Key) {
	if k.Name == KeyEsc {
		c.stopPresenting(o)
	}
}

func (o *presentOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	lines := o.lines()
	h := len(lines) + 5
	y0 := (H - h) / 2
	can.Box(2, y0, W-4, h, st(colTrusted, colMenu), true)
	can.Text(4, y0+1, tr.T("Cards"), bold(st(colTrusted, colMenu)), W-8)
	for i, l := range lines {
		s := st(colFG, colMenu)
		if i == 0 {
			s = bold(s)
		}
		can.Text(4, y0+2+i, cv.Truncate(l, W-8), s, W-8)
	}
	can.Text(4, y0+h-2, tr.T("Esc stop"), st(colDim, colMenu), W-8)
}

func (o *presentOverlay) frame(c *Core) *FrameOverlay {
	return &FrameOverlay{Kind: "confirm", Title: tr.T("Cards"), Lines: o.lines(), Footer: []string{tr.T("Back stop")}}
}
