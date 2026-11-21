package shell

import (
	"time"
)

// Keys reach an app one action at a time, as Android's InputDispatcher
// waits for a window to finish one event before it sends the next: after
// an action goes to the foreground app, the keys pressed before the app has
// handled it are held, and replayed in order once it has. Otherwise a key
// pressed right after an action (Back after a choice) acts on the old page
// in the Shell, and the app's answer to the action then lands on the wrong
// page (a "replace the top page" replacing the page the user went back to).
// The app has handled the action when it answers the ping sent after it
// (the SDKs answer pings from the loop that runs the handlers, anr.go). A
// slow app gets at most inputHoldMax, Android's wait for a key behind
// unfinished events (KEY_WAITING_FOR_EVENTS_TIMEOUT): then its keys go on as
// they are, and the ANR check takes over. Nothing is held while a system dialog is up or
// one of the app's requests may bring one up (a consent dialog needs the
// keys); its other requests do not count, as an SMS waiting for the network
// would let the keys through for its whole wait.
const inputHoldMax = 500 * time.Millisecond

func (c *Core) holdMax() time.Duration { return inputHoldMax * time.Duration(max(1, c.holdScale)) }

type inputBarrier struct {
	// up: an action is out whose ping has not been answered; again: the
	// ping out was sent before the action, so another must follow.
	up, again bool
	since     time.Time
	held      []Key
	gen       int
}

// actionSent notes an action the app is to handle (after a.send).
func (c *Core) actionSent(a *App) {
	if a.system || !a.running || !a.ready {
		return
	}
	b := &a.input
	if !a.ping.sent.IsZero() {
		b.again = true
	}
	c.probe(a)
	if !b.up {
		b.up, b.since = true, c.now()
		b.gen++
		gen := b.gen
		time.AfterFunc(c.holdMax(), func() {
			c.Post(func() {
				if a.input.up && a.input.gen == gen {
					c.log.Printf("%s: no answer to an action after %s; held keys go on", a.M.ID, c.holdMax())
					c.releaseInput(a)
				}
			})
		})
	}
}

// inputPong notes the answer to a ping.
func (c *Core) inputPong(a *App) {
	b := &a.input
	if !b.up {
		return
	}
	if b.again {
		b.again = false
		c.probe(a)
		return
	}
	c.releaseInput(a)
}

// holdKey keeps k for the foreground app while an action of its is out.
func (c *Core) holdKey(k Key) bool {
	a := c.fg
	if a == nil || a.system || !a.input.up {
		return false
	}
	switch k.Name {
	case KeyVolUp, KeyVolDown, KeyPlayPause, KeyNextTrack, KeyPrevTrack, KeyA11y:
		return false
	}
	if len(c.overlays) > 0 || a.guard.dialogs > 0 || len(a.input.held) >= maxHeldKeys || c.now().Sub(a.input.since) >= c.holdMax() {
		c.releaseInput(a)
		return false
	}
	a.input.held = append(a.input.held, k)
	return true
}

// releaseInput lifts the barrier and replays the keys held, in order (a key
// that sends another action holds the ones after it again), as long as the
// app is still in front: keys for it are not for another.
func (c *Core) releaseInput(a *App) {
	b := &a.input
	keys := b.held
	b.up, b.again, b.held = false, false, nil
	if c.fg != a {
		if len(keys) > 0 {
			c.log.Printf("%s: %d held keys dropped (no longer in the foreground)", a.M.ID, len(keys))
		}
		return
	}
	for _, k := range keys {
		c.Key(k)
	}
}

// resetInput forgets the barrier of a process that ended.
func (a *App) resetInput() {
	a.input = inputBarrier{gen: a.input.gen + 1}
}

// A system dialog that asks for a decision takes its answer only from keys
// pressed while it shows, and not in its first overlayArmDelay: a key
// pressed before it came up (held while an app started or answered, then
// replayed) was meant for what was under it, and one in the first moment is
// most likely typing that went on, so neither grants a permission, sends an
// SMS for an app, enters PIN digits or answers a call. The arrow keys move
// the choice at once; Esc (deny, cancel) is taken at once too, except one
// pressed before an incoming call rang, which would decline it. Browsers
// hold back their prompts' buttons the same way (Firefox's
// security.dialog_enable_delay).
const overlayArmDelay = 500 * time.Millisecond

func decisionOverlay(o overlay) bool {
	switch o.(type) {
	case *permOverlay, *confirmOverlay, *authOverlay, *pickerOverlay, *captureOverlay, *shareOverlay,
		*incomingOverlay, *cellIncomingOverlay:
		return true
	}
	return false
}

func (c *Core) overlayUp(o overlay) {
	if c.overlayShown == nil {
		c.overlayShown = map[overlay]time.Time{}
	}
	c.overlayShown[o] = time.Now()
}

// overlayArmLeft is how long the top overlay still refuses answers.
func (c *Core) overlayArmLeft() time.Duration {
	n := len(c.overlays)
	if n == 0 || !decisionOverlay(c.overlays[n-1]) {
		return 0
	}
	return max(0, overlayArmDelay-time.Since(c.overlayShown[c.overlays[n-1]]))
}

// refusesInjected: a system dialog whose answer grants a permission or
// hands an app the user's data, pays, enters or sets a PIN, unlocks the
// device or trusts a computer or another device. Keys the computer injects
// (kario device key, type) may cancel it, never answer it or move its
// choice, as Android keeps injected input off its secure windows; the
// person holding the phone answers.
func refusesInjected(o overlay) bool {
	switch o := o.(type) {
	case *permOverlay, *authOverlay, *lockPadOverlay, *lockSetOverlay, *simPinOverlay, *simSetOverlay, *enrolOverlay,
		*pickerOverlay, *captureOverlay, *shareOverlay, *theftOverlay:
		return true
	case *confirmOverlay:
		return o.trust
	case *menuOverlay:
		return o.trust
	}
	return false
}

// injectedRefuser names a dialog that refuses injected keys, for the
// computer.
func injectedRefuser(o overlay) string {
	switch o.(type) {
	case *permOverlay:
		return "a permission dialog"
	case *authOverlay:
		return "a trusted confirmation"
	case *lockPadOverlay, *lockSetOverlay, *simPinOverlay, *simSetOverlay:
		return "a PIN entry"
	case *enrolOverlay:
		return "fingerprint enrolment"
	case *menuOverlay:
		return "the card view"
	case *theftOverlay:
		return "the anti-theft lock"
	case *confirmOverlay:
		return "a system confirmation that grants or trusts"
	}
	return "a dialog handing an app the user's data"
}

// refuseInjected turns an injected key away from what refuses it, and
// says so on the phone and to the computer (devInject).
func (c *Core) refuseInjected(k Key, what string) {
	name := k.Name
	if k.Name == KeyRunes {
		name = string(k.Runes)
	}
	c.dev.refused = what
	c.log.Printf("dev link: injected %q refused by %s (it takes the person's own keys)", name, what)
	c.showToast(tr.T("Keys from the computer cannot answer this; use the phone's own keys"))
}

// overlayTakes says whether k may go to the top overlay o.
func (c *Core) overlayTakes(o overlay, k Key) bool {
	if k.Injected && k.Name != KeyEsc && refusesInjected(o) {
		c.refuseInjected(k, injectedRefuser(o))
		return false
	}
	if !decisionOverlay(o) {
		return true
	}
	shown := c.overlayShown[o]
	_, call := o.(*incomingOverlay)
	_, cell := o.(*cellIncomingOverlay)
	switch {
	case !k.At.IsZero() && k.At.Before(shown) && (k.Name != KeyEsc || call || cell):
		c.log.Printf("%T: %s was pressed before it showed; not an answer", o, k.Name)
		return false
	case k.Name == KeyEsc:
		return true
	}
	switch k.Name {
	case KeyLeft, KeyRight, KeyUp, KeyDown, KeyTab:
		return true
	}
	if left := c.overlayArmLeft(); left > 0 && c.overlays[len(c.overlays)-1] == o {
		c.log.Printf("%T: %s in its first %s; not an answer", o, k.Name, overlayArmDelay)
		return false
	}
	return true
}
