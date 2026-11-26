package shell

import (
	"context"
	"errors"
	"time"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/family"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wipe"
)

// Factory reset (31-lock-and-biometrics.md §11). Settings › More settings ›
// Factory reset erases the phone after the lock-screen PIN (the parental
// PIN in minor mode) and a last confirmation; the lock screen offers to
// erase it without the PIN once forgotAfter wrong ones were entered in a
// row, and then the anti-theft lock holds (antitheft.go). Either way the
// Shell leaves a request in the persistent area (package wipe) and
// restarts the phone; the next boot erases the data before the services
// start.

// forgotAfter: wrong lock-screen PINs in a row after which the lock screen
// offers to erase the phone: its first wait (iOS offers it on the lockout
// screen).
const forgotAfter = 5

// resetWait bounds how long a reset waits for the account server to hear
// of it before the phone restarts anyway.
const resetWait = 5 * time.Second

type resetShell struct {
	// busy: erasing; the phone restarts soon.
	busy bool
}

// resetSetting is settings.set factory_reset; false for another key.
func (c *Core) resetSetting(args ui.SettingArgs, done func(error)) bool {
	if args.Key != ui.SettingFactoryReset {
		return false
	}
	if c.cfg.PersistDir == "" {
		done(errors.New(tr.T("This device cannot be reset here")))
		return true
	}
	keep := args.Value == ui.ResetKeepLock
	c.resetAuth(func(err error) {
		if err != nil {
			done(err)
			return
		}
		c.confirmReset(wipe.FromSettings, keep, done)
	})
	return true
}

// resetAuth asks for what guards a reset from Settings: in minor mode the
// parental PIN, every time (the minutes a PIN entered in Settings counts
// for do not reach a reset); else the lock-screen PIN, if one is set.
func (c *Core) resetAuth(done func(error)) {
	switch {
	case c.minorOn():
		c.askParentPIN("verify", tr.T("A parent's PIN is needed to erase this phone in minor mode."), done)
	case c.lock.pin:
		c.askLockPIN("erase", 0, func(_ string, err error) { done(err) })
	default:
		done(nil)
	}
}

// confirmReset is the last confirmation, the system's own: keys from a
// computer cannot answer it.
func (c *Core) confirmReset(why string, keep bool, done func(error)) {
	lock := keep || why == wipe.FromLockScreen
	c.pushOverlay(&confirmOverlay{text: c.resetText(why, lock), trust: true, yes: tr.T("Erase everything"),
		onYes: func() {
			done(nil)
			c.factoryReset(why, lock)
		},
		onNo: func() { done(errors.New(ui.ErrCancelled)) }})
}

func (c *Core) resetText(why string, lock bool) string {
	text := tr.T("Erase everything on this phone?") + "\n" +
		tr.T("All apps and their data and settings, the accounts and the passwords the system keeps for them, contacts, messages, photos and files, Wi-Fi networks, the lock-screen PIN and fingerprints, and minor mode go. The system and its updates stay. This cannot be undone.")
	if why == wipe.FromLockScreen {
		text = tr.T("Erase this phone without its PIN?") + "\n" +
			tr.T("Everything on it goes: apps and their data, accounts, contacts, messages, photos and files. This cannot be undone.")
	}
	if rec := c.theft.rec; rec != nil {
		if lock {
			text += "\n" + tr.T("The anti-theft lock holds: afterwards the phone asks for the Kario account %s before it can be used.", rec.Email)
		} else {
			text += "\n" + tr.T("The anti-theft lock comes off: the Kario account is not asked for afterwards.")
		}
	}
	return text
}

// forgotPIN is the lock screen's way out of a forgotten PIN: erasing the
// phone, after the parental PIN in minor mode (else a child could wipe
// minor mode away). The anti-theft lock holds.
func (c *Core) forgotPIN() {
	confirm := func() { c.confirmReset(wipe.FromLockScreen, true, func(error) {}) }
	if c.cfg.PersistDir == "" {
		c.showToast(tr.T("This device cannot be reset here"))
		return
	}
	if c.minorOn() {
		c.askParentPIN("verify", tr.T("A parent's PIN is needed to erase this phone in minor mode."), func(err error) {
			if err == nil {
				confirm()
			}
		})
		return
	}
	confirm()
}

// factoryReset erases the phone: the request first (nothing changes if it
// cannot be written), then the anti-theft lock's record, then a few
// seconds for the account server to hear of it, then the restart.
func (c *Core) factoryReset(why string, lock bool) {
	if c.reset.busy {
		return
	}
	if err := wipe.Request(c.cfg.PersistDir, why); err != nil {
		c.log.Printf("factory reset: %v", err)
		c.showToast(tr.T("The phone could not be erased: %v", err))
		return
	}
	c.reset.busy = true
	c.log.Printf("factory reset from %s; the anti-theft lock holds: %v", why, lock && c.theft.rec != nil)
	c.pushOverlay(&wipingOverlay{})
	tell := append(c.theftForReset(why, lock), c.familyForReset()...)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), resetWait)
		defer cancel()
		for _, f := range tell {
			f(ctx)
		}
		c.Post(func() { c.reboot("factory reset") })
	}()
}

// familyForReset tells the account server that this phone no longer has a
// parent managing it, as unlinking would: the record of it goes with the
// data.
func (c *Core) familyForReset() []func(context.Context) {
	f := c.family()
	recs := append([]*familyRecord(nil), f.file.Leaving...)
	if f.file.Bound != nil {
		recs = append(recs, f.file.Bound)
	}
	var out []func(context.Context)
	for _, rec := range recs {
		out = append(out, func(ctx context.Context) {
			token, err := c.familyToken(rec)
			if err == nil {
				err = (&family.Client{Server: rec.Server, Token: token, HTTP: familyHTTP()}).Unbind(ctx)
			}
			if err != nil {
				c.Post(func() { c.log.Printf("factory reset: the account server did not hear of the unlinking: %v", err) })
			}
		})
	}
	return out
}

// reboot restarts the phone: phone-shell's own way on a developer machine
// (Config.Reboot), systemd on a phone.
func (c *Core) reboot(why string) {
	c.log.Printf("restarting: %s", why)
	switch {
	case c.cfg.Reboot != nil:
		c.cfg.Reboot()
	case c.cfg.SKU.ID != "":
		go func() {
			if err := systemdReboot(""); err != nil {
				c.Post(func() { c.log.Printf("restart: %v", err) })
			}
		}()
	default:
		c.showToast(tr.T("Not a phone: restart it yourself to finish the reset"))
	}
}

// resetView is settings.get's "reset": what the reset page says and asks.
func (c *Core) resetView() map[string]any {
	out := map[string]any{"supported": c.cfg.PersistDir != "", "minor": c.minorOn(), "pin": c.lock.pin}
	if rec := c.theft.rec; rec != nil && !c.theft.locked {
		out["theft"] = map[string]any{"email": rec.Email, "since": rec.Since}
	}
	return out
}

// wipingOverlay covers the screen while the phone gets ready to restart
// and erase itself; it takes no keys.
type wipingOverlay struct{}

func (o *wipingOverlay) key(c *Core, k Key) {}

func (o *wipingOverlay) lines() []string {
	return []string{tr.T("The phone restarts and erases its data before anything else starts. Do not turn it off.")}
}

func (o *wipingOverlay) frame(c *Core) *FrameOverlay {
	return &FrameOverlay{Kind: "confirm", Title: tr.T("Erasing the phone…"), Lines: o.lines()}
}

func (o *wipingOverlay) render(c *Core, can *cv.Canvas) {
	W, H := c.cfg.W, c.cfg.H
	bs := st(colFG, colTrustBG)
	can.Box(0, 1, W, H-1, st(colTrusted, colTrustBG), true)
	can.Text(2, 2, "■ "+tr.T("Erasing the phone…"), bold(st(colTrusted, colTrustBG)), W-4)
	y := 4
	for _, l := range o.lines() {
		for _, w := range cv.Wrap(l, W-6) {
			can.Text(3, y, w, bs, W-6)
			y++
		}
	}
}
