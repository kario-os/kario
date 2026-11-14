package shell

import (
	"errors"
	"fmt"
	"strings"
	"time"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/hal/fingerprint"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Fingerprints, from the shell's side (31-lock-and-biometrics.md): the
// sensor and the templates are phone-keystored's (the TEE's stand-in);
// the shell asks it to wait for a touch, hears "matched" with a token, and
// passes tokens on. It keeps no template and no PIN.

// fingerPageFor is how long the PIN that opened the fingerprints lasts
// (phone-keystored's limit on its token too).
const fingerPageFor = 10 * time.Minute

// fingerUnlockReady: a fingerprint can unlock the phone now.
func (c *Core) fingerUnlockReady() bool {
	st := c.lock.st
	return c.lock.pin && c.lock.prefs.FingerUnlock && st.Sensor != "" && len(st.Fingers) > 0 && st.Blocked == ""
}

// armFinger has phone-keystored wait for a finger to unlock the phone. A
// confirmation dialog on the screen has the sensor to itself.
func (c *Core) armFinger() {
	if c.lock.armed || !c.secureLocked() || !c.fingerUnlockReady() || time.Now().Before(c.lock.armAfter) {
		return
	}
	for _, o := range c.overlays {
		if _, ok := o.(*authOverlay); ok {
			return
		}
	}
	c.lock.armed = true
	c.lock.armGen++
	gen := c.lock.armGen
	var r keystore.AuthResult
	c.broker.call(svc.Keystored, keystore.MethodFPAuth, map[string]string{}, &r, func(err error) {
		if gen != c.lock.armGen {
			return
		}
		c.lock.armed = false
		switch {
		case err != nil && ui.Cancelled(err):
		case err != nil:
			c.log.Printf("fingerprint: %v", err)
			c.lock.armAfter = time.Now().Add(2 * time.Second)
			c.lockRefresh(nil)
		case r.Match:
			c.unlockNow("fingerprint")
		case r.Disabled:
			c.lock.st.Blocked = keystore.BlockedLockout
			c.showToast(tr.T("Too many tries with a fingerprint: enter the PIN"))
		default:
			c.showToast(tr.T("Fingerprint not recognized"))
			c.armFinger()
		}
	})
}

// disarmFinger takes the wait back.
func (c *Core) disarmFinger() {
	if !c.lock.armed {
		return
	}
	c.lock.armed = false
	c.lock.armGen++
	c.broker.call(svc.Keystored, keystore.MethodFPCancel, nil, nil, func(error) {})
}

// TouchFinger puts a named finger on the simulated sensor (selftest); like
// a real sensor's touch it wakes the screen.
func (c *Core) TouchFinger(name string) error {
	c.lockTick()
	c.Wake("fingerprint")
	return c.broker.rpc(svc.Keystored, keystore.MethodFPSimTouch, map[string]string{"finger": name}, nil)
}

// fingerPageOpen: the PIN that opened the fingerprints still counts.
func (c *Core) fingerPageOpen() bool {
	return c.lock.pageToken != "" && time.Now().Before(c.lock.pageUntil)
}

// fingerAuth asks for the PIN to manage fingerprints, unless it was given
// lately; its token is phone-keystored's leave to enrol and delete.
func (c *Core) fingerAuth(done func(error)) {
	switch {
	case !c.lock.pin:
		done(errors.New(tr.T("Set a lock-screen PIN first")))
		return
	case c.fingerPageOpen():
		done(nil)
		return
	}
	var res struct {
		Challenge uint64 `json:"challenge"`
	}
	c.broker.call(svc.Keystored, keystore.MethodFPChallenge, nil, &res, func(err error) {
		if err != nil {
			done(err)
			return
		}
		c.askLockPIN("confirm", res.Challenge, func(token string, err error) {
			if err == nil {
				c.lock.pageToken, c.lock.pageUntil = token, time.Now().Add(fingerPageFor)
			}
			c.lockRefresh(func() { done(err) })
		})
	})
}

// securitySetting is settings.set for Settings › Security; it reports
// whether the key was one of them. done gets the outcome, after the
// system's own screen when there is one.
func (c *Core) securitySetting(args ui.SettingArgs, done func(error)) bool {
	saved := func() {
		c.saveLockPrefs()
		done(nil)
	}
	switch args.Key {
	case ui.SettingLockPIN:
		op := "remove"
		switch {
		case args.Value != 0 && c.lock.pin:
			op = "change"
		case args.Value != 0:
			op = "set"
		case !c.lock.pin:
			done(errors.New(tr.T("No lock-screen PIN is set")))
			return true
		}
		c.askLockPIN(op, 0, func(_ string, err error) {
			c.lock.pageToken = ""
			c.lockRefresh(func() { done(err) })
		})
	case ui.SettingLockGrace:
		for _, g := range lockGraceChoices {
			if float64(g) == args.Value {
				c.lock.prefs.Grace = g
				saved()
				return true
			}
		}
		done(errors.New("lock_grace must be one of the choices"))
	case ui.SettingLockShowContent:
		c.lock.prefs.ShowContent = args.Value != 0
		saved()
	case ui.SettingFingerprintUnlock:
		c.lock.prefs.FingerUnlock = args.Value != 0
		if !c.fingerUnlockReady() {
			c.disarmFinger()
		}
		saved()
	case ui.SettingFingerprintPay:
		c.lock.prefs.FingerPay = args.Value != 0
		saved()
	case ui.SettingFingerprintAuth:
		c.fingerAuth(done)
	case ui.SettingFingerprintEnroll:
		c.fingerAuth(func(err error) {
			if err != nil {
				done(err)
				return
			}
			c.enrollFinger(done)
		})
	case ui.SettingFingerprintRename, ui.SettingFingerprintDelete:
		if !c.fingerPageOpen() {
			done(errors.New(tr.T("Confirm your PIN again to manage fingerprints")))
			return true
		}
		method, params := keystore.MethodFPRename, map[string]any{"id": int(args.Value), "name": strings.TrimSpace(args.Text)}
		if args.Key == ui.SettingFingerprintDelete {
			method, params = keystore.MethodFPRemove, map[string]any{"id": int(args.Value), "token": c.lock.pageToken}
		}
		c.broker.call(svc.Keystored, method, params, nil, func(err error) {
			c.lockRefresh(func() { done(err) })
		})
	default:
		return false
	}
	return true
}

// securityInfo is settings.get's "security".
func (c *Core) securityInfo() map[string]any {
	st := c.lock.st
	fingers := st.Fingers
	if fingers == nil {
		fingers = []keystore.Finger{}
	}
	return map[string]any{
		"pin": c.lock.pin, "pin_min": keystore.MinPIN, "pin_max": keystore.MaxPIN,
		"grace": c.lock.prefs.Grace, "grace_choices": lockGraceChoices, "show_content": c.lock.prefs.ShowContent,
		"sensor": st.Sensor, "fingers": fingers, "max_fingers": keystore.MaxFingers,
		"finger_unlock": c.lock.prefs.FingerUnlock, "finger_pay": c.lock.prefs.FingerPay,
		"blocked": st.Blocked, "fingers_open": c.fingerPageOpen(),
	}
}

// enrolOverlay is the system's enrolment screen: touch after touch, with
// the progress as dots.
type enrolOverlay struct {
	steps, done int
	note        string
	bad         bool
	finish      func(error)
}

func (c *Core) enrollFinger(finish func(error)) {
	switch {
	case c.lock.st.Sensor == "":
		finish(errors.New(tr.T("This phone has no fingerprint sensor it can use")))
		return
	case len(c.lock.st.Fingers) >= keystore.MaxFingers:
		finish(errors.New(tr.T("At most %d fingerprints", keystore.MaxFingers)))
		return
	}
	o := &enrolOverlay{finish: finish}
	name := tr.T("Fingerprint %d", len(c.lock.st.Fingers)+1)
	var res struct {
		Steps int `json:"steps"`
	}
	c.broker.call(svc.Keystored, keystore.MethodFPEnroll, map[string]string{"token": c.lock.pageToken, "name": name}, &res, func(err error) {
		if err != nil {
			finish(err)
			return
		}
		o.steps = res.Steps
		c.pushOverlay(o)
		c.enrolNext(o)
	})
}

func (c *Core) showing(o overlay) bool {
	for _, x := range c.overlays {
		if x == o {
			return true
		}
	}
	return false
}

func (c *Core) enrolNext(o *enrolOverlay) {
	var st keystore.EnrollStep
	c.broker.call(svc.Keystored, keystore.MethodFPEnrollNext, nil, &st, func(err error) {
		if !c.showing(o) {
			return
		}
		if err != nil {
			c.popOverlay(o)
			o.finish(err)
			return
		}
		o.done, o.note, o.bad = st.Done, "", false
		switch st.Rejected {
		case "":
		case fingerprint.RejectEnrolled:
			o.note, o.bad = tr.T("This finger is added already; use another"), true
		default:
			o.note, o.bad = tr.T("That was another finger; keep to the first one"), true
		}
		if st.Finger == nil {
			c.enrolNext(o)
			return
		}
		c.popOverlay(o)
		c.showToast(tr.T("Fingerprint added: “%s”", st.Finger.Name))
		c.lockRefresh(func() { o.finish(nil) })
	})
}

func (o *enrolOverlay) key(c *Core, k Key) {
	if k.Name != KeyEsc {
		return
	}
	c.popOverlay(o)
	c.broker.call(svc.Keystored, keystore.MethodFPCancel, nil, nil, func(error) {})
	o.finish(errors.New(ui.ErrCancelled))
}

func (o *enrolOverlay) lines() []string {
	return []string{tr.T("Touch the sensor with the same finger, lifting it between touches, so all of it is read."),
		tr.T("%d of %d touches", o.done, o.steps)}
}

func (o *enrolOverlay) frame(c *Core) *FrameOverlay {
	return &FrameOverlay{Kind: "auth", Title: tr.T("Add a fingerprint"), Lines: o.lines(), PIN: [2]int{o.done, o.steps},
		Note: o.note, NoteBad: o.bad,
		Footer: []string{tr.T("Back cancel"), tr.T("The fingerprint stays in the key service; no app gets it.")}}
}

func (o *enrolOverlay) render(c *Core, can *cv.Canvas) {
	drawLockPin(c, can, tr.T("Add a fingerprint"), o.lines(), o.done, o.steps, o.note, o.bad,
		[]string{tr.T("Esc cancel"), tr.T("The fingerprint stays in the key service; no app gets it.")})
}

// payBioKey is the biometric key auth.confirm may use: the app's, if the
// user turned fingerprints on for payments and one is enrolled.
func (c *Core) payBioKey(args ui.AuthConfirmArgs) string {
	if args.BioKey == "" || args.BioKey == args.Key || !c.lock.prefs.FingerPay || !c.lock.pin ||
		c.lock.st.Sensor == "" || len(c.lock.st.Fingers) == 0 {
		return ""
	}
	return args.BioKey
}

// startBioConfirm offers a fingerprint in the confirmation dialog: a match
// signs with the biometric-bound key; whatever stands in the way leaves
// the payment PIN, with the reason shown.
func (c *Core) startBioConfirm(o *authOverlay, app string, args ui.AuthConfirmArgs, reply func(any, error)) {
	o.bio = true
	var r keystore.AuthResult
	c.broker.call(svc.Keystored, keystore.MethodFPAuth, map[string]string{"app": app, "key": args.BioKey}, &r, func(err error) {
		if o.done || !c.showing(o) {
			return
		}
		switch {
		case err != nil && ui.Cancelled(err):
		case err != nil:
			o.bio, o.bioNote = false, bioRefusal(err)
		case r.Match:
			if o.busy {
				return
			}
			o.busy = true
			var proof ui.KeyProof
			c.broker.call(svc.Keystored, keystore.MethodConfirmBio, map[string]string{"app": app, "key": args.BioKey, "alg": args.Alg,
				"prompt": args.Prompt, "payload": args.Payload, "token": r.Token}, &proof, func(err error) {
				o.busy = false
				if o.done {
					return
				}
				if err != nil {
					o.bio, o.bioNote = false, bioRefusal(err)
					return
				}
				o.done = true
				c.popOverlay(o)
				reply(proof, nil)
			})
		case r.Disabled:
			o.bio, o.bioNote = false, tr.T("Too many tries with a fingerprint: confirm with the payment PIN")
		default:
			o.err = tr.T("Fingerprint not recognized; try again")
			c.startBioConfirm(o, app, args, reply)
		}
	})
}

// bioLines are what the confirmation dialog says about the fingerprint.
func (o *authOverlay) bioLines() []string {
	switch {
	case o.bio:
		return []string{tr.T("Touch the fingerprint sensor to confirm, or enter the payment PIN")}
	case o.bioNote != "":
		return []string{o.bioNote}
	}
	return nil
}

// stopBioConfirm frees the sensor once the dialog is done.
func (c *Core) stopBioConfirm(o *authOverlay) {
	o.done = true
	if o.bio {
		o.bio = false
		c.broker.call(svc.Keystored, keystore.MethodFPCancel, nil, nil, func(error) {})
	}
}

// bioRefusal says why the dialog fell back to the payment PIN.
func bioRefusal(err error) string {
	e := err.Error()
	switch {
	case strings.HasPrefix(e, keystore.ErrKeyInvalidated.Error()):
		return tr.T("The fingerprints changed since fingerprint payment was set up: confirm with the payment PIN to set it up again")
	case strings.HasPrefix(e, keystore.ErrNoBioKey.Error()):
		return tr.T("Confirm with the payment PIN once to start paying with a fingerprint")
	case strings.HasPrefix(e, keystore.ErrPINRequired.Error()):
		return tr.T("Fingerprints need the lock-screen PIN first: confirm with the payment PIN")
	}
	return fmt.Sprint(tr.T("Fingerprint unavailable: confirm with the payment PIN"), " (", e, ")")
}
