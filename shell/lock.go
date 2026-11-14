package shell

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// The lock screen (31-lock-and-biometrics.md). Without a lock-screen PIN
// the lock is Nokia's keypad lock: a key sequence unlocks it and it hides
// nothing. With one it is a secure lock, as on Android and iOS: the phone
// locks when the screen goes off (after the grace period the user chose)
// and when it starts; the lock screen hides the app that was in front and
// what notifications say; only the PIN, checked by phone-keystored, or an
// enrolled fingerprint unlocks it. Emergency calls, answering a call and
// stopping an alarm work locked (calls and alarms are overlays, which get
// the keys first), and other features add lock-screen shortcuts.
//
// c.keylock is the one lock state, c.locked() its predicate; the PIN only
// decides how it is undone.

// lockShortcut is a key the lock screen answers without unlocking, for
// system UI meant to work locked (a torch). Run sees the phone
// still locked and must show only an overlay: everything else stays behind
// the lock. Keys the lock screen keeps for itself: the digits, OK, the left
// soft key (Tab), *, the call key, the end key and Back; free are the right
// soft key (KeyF2) and the arrows.
type lockShortcut struct {
	// Key is a key name (KeyF2, KeyRight) or one character ("#").
	Key string
	// Label is English, translated where the lock screen's hint shows it.
	Label string
	Run   func(c *Core)
}

// lockShortcuts lists them. A feature adds its own with one line, here or
// from an init function in its own file:
//
//	lockShortcuts = append(lockShortcuts, lockShortcut{Key: KeyF2, Label: "Torch", Run: (*Core).torch})
//
// The quick card view needs none: the power key's double press opens it
// locked or not (cards.go), and door cards answer readers by themselves.
var lockShortcuts []lockShortcut

func (s lockShortcut) matches(k Key) bool {
	return k.Name == s.Key || (k.Name == KeyRunes && string(k.Runes) == s.Key)
}

// lockGraceChoices: seconds the screen may be off before the phone locks.
var lockGraceChoices = []int{0, 5, 30, 60, 300}

// lockPrefs are Settings › Security's choices (state/shell/lock.json).
type lockPrefs struct {
	Grace        int  `json:"grace,omitempty"`
	ShowContent  bool `json:"show_content,omitempty"`
	FingerUnlock bool `json:"finger_unlock"`
	FingerPay    bool `json:"finger_pay"`
	// Secure: a PIN was set when last heard, so a start begins locked
	// before phone-keystored has answered.
	Secure bool `json:"secure,omitempty"`
}

type lockState struct {
	prefs lockPrefs
	// pin: a lock-screen PIN is set (phone-keystored's word, kept); heard:
	// phone-keystored has answered since the start.
	pin   bool
	heard bool
	st    keystore.LockStatus
	// What the lock screen hides, given back on unlock: the app in front
	// and the system screens above standby when it locked.
	behind      *App
	behindStack []*View
	// armed: a wait for a fingerprint to unlock is out (armGen tells its
	// answer from a cancelled one's); not again before armAfter.
	armed    bool
	armGen   int
	armAfter time.Time
	// fgSeen came to the front at fgAt while locked: the dialer or a VoIP
	// app gets callStarts to show its call before it is hidden again.
	fgSeen *App
	fgAt   time.Time
	// The PIN token that opened Settings › Security › Fingerprints, passed
	// on to phone-keystored to enrol or delete until pageUntil.
	pageToken string
	pageUntil time.Time
}

func (c *Core) locked() bool { return c.keylock }

// secureLocked: locked with a PIN; only the PIN or a fingerprint undoes it.
func (c *Core) secureLocked() bool { return c.keylock && c.lock.pin }

// Locked reports the lock (selftest).
func (c *Core) Locked() bool { return c.keylock }

func (c *Core) lockPath() string { return filepath.Join(c.cfg.StateDir, "shell", "lock.json") }

func (c *Core) saveLockPrefs() {
	c.lock.prefs.Secure = c.lock.pin
	raw, _ := json.MarshalIndent(c.lock.prefs, "", "  ")
	_ = os.WriteFile(c.lockPath(), raw, 0o600)
}

// startLock reads the choices and, if a PIN was set, starts locked: the
// first unlock after a start needs the PIN (phone-keystored refuses a
// fingerprint until then).
func (c *Core) startLock() {
	c.lock.prefs = lockPrefs{FingerUnlock: true}
	if raw, err := os.ReadFile(c.lockPath()); err == nil {
		_ = json.Unmarshal(raw, &c.lock.prefs)
	}
	c.lock.pin = c.lock.prefs.Secure
	if c.lock.pin {
		c.lockNow("start")
	}
	c.lockRefresh(nil)
}

// lockRefresh asks phone-keystored for the PIN and the fingerprints.
func (c *Core) lockRefresh(done func()) {
	var st keystore.LockStatus
	c.broker.call(svc.Keystored, keystore.MethodLockStatus, nil, &st, func(err error) {
		if err != nil {
			c.log.Printf("lock: %v", err)
		} else {
			c.applyLockStatus(st)
		}
		if done != nil {
			done()
		}
	})
}

// applyLockStatus takes phone-keystored's word. Its first answer after a
// start corrects the remembered PIN either way; a PIN set later from
// Settings locks the phone only when the screen next goes off.
func (c *Core) applyLockStatus(st keystore.LockStatus) {
	was, first := c.lock.pin, !c.lock.heard
	c.lock.st, c.lock.pin, c.lock.heard = st, st.PIN, true
	if was != st.PIN {
		c.saveLockPrefs()
		switch {
		case st.PIN && first:
			c.lockNow("start")
		case !st.PIN && c.keylock:
			c.keylock = false
			c.lock.behind, c.lock.behindStack = nil, nil
		}
	}
	if !c.fingerUnlockReady() {
		c.disarmFinger()
	}
}

// lockNow locks the phone; with a PIN it also closes what was open over
// the screen and hides the rest.
func (c *Core) lockNow(why string) {
	if c.keylock {
		return
	}
	c.keylock, c.emergency = true, ""
	c.log.Printf("locked (%s)", why)
	if !c.lock.pin {
		return
	}
	c.lock.pageToken = ""
	c.closeForLock()
	c.lock.behind = c.fg
	if len(c.sys.stack) > 1 {
		c.lock.behindStack = append([]*View(nil), c.sys.stack[1:]...)
	}
	c.hideBehindLock()
	c.armFinger()
}

// closeForLock cancels the dialogs and menus over the screen; a call, an
// alarm, an emergency alert, the SIM's PIN and the lock's own screens stay.
func (c *Core) closeForLock() {
	for i := len(c.overlays) - 1; i >= 0; i-- {
		if i >= len(c.overlays) {
			continue
		}
		o := c.overlays[i]
		switch o.(type) {
		case *incomingOverlay, *cellIncomingOverlay, *alarmOverlay, *alertOverlay, *simPinOverlay, *lockPadOverlay:
			continue
		}
		o.key(c, Named(KeyEsc))
		c.popOverlay(o)
	}
}

// hideBehindLock leaves the standby screen in front.
func (c *Core) hideBehindLock() {
	c.setForeground(nil)
	if len(c.sys.stack) > 1 {
		c.sys.stack = c.sys.stack[:1]
	}
	if len(c.sys.stack) == 1 && c.sys.stack[0].S.ID != "sys.idle" {
		c.sys.stack[0] = newView(c.idleScreen())
	}
}

// unlockNow ends the lock and gives back what it hid.
func (c *Core) unlockNow(how string) {
	if !c.keylock {
		return
	}
	c.keylock, c.emergency = false, ""
	c.disarmFinger()
	for i := len(c.overlays) - 1; i >= 0; i-- {
		if o, ok := c.overlays[i].(*lockPadOverlay); ok {
			c.popOverlay(o)
		}
	}
	behind, stack := c.lock.behind, c.lock.behindStack
	c.lock.behind, c.lock.behindStack = nil, nil
	c.log.Printf("unlocked (%s)", how)
	if c.fg != nil {
		return
	}
	if len(stack) > 0 && len(c.sys.stack) == 1 {
		c.sys.stack = append(c.sys.stack, stack...)
	}
	if behind != nil && c.appByID(behind.M.ID) == behind {
		c.Launch(behind, "")
	}
}

// lockTick runs with the housekeeping, and before a key wakes the screen:
// the screen off for the grace period locks the phone; a secure lock keeps
// the standby screen in front once a call is over, and the fingerprint
// sensor armed.
func (c *Core) lockTick() {
	if c.lock.pin && !c.keylock && c.screen.off && c.now().Sub(c.screen.offAt) >= time.Duration(c.lock.prefs.Grace)*time.Second {
		c.lockNow("screen off")
	}
	if !c.secureLocked() {
		return
	}
	if c.fg != c.lock.fgSeen {
		c.lock.fgSeen, c.lock.fgAt = c.fg, time.Now()
	}
	starting := c.fg != nil && time.Since(c.lock.fgAt) < callStarts && (c.fg.M.ID == appDialer || c.fg.M.Has(ui.PermVoIP))
	if !c.anyCall() && !starting && (c.fg != nil || len(c.sys.stack) > 1) {
		c.hideBehindLock()
	}
	c.armFinger()
}

// callStarts: how long an emergency call or an answered call has to show
// up as a call before its app is hidden behind the lock again.
const callStarts = 5 * time.Second

// lockShortcutKey runs the shortcut on the key, if one is.
func (c *Core) lockShortcutKey(k Key) bool {
	for _, s := range lockShortcuts {
		if s.matches(k) {
			s.Run(c)
			return true
		}
	}
	return false
}

// secureLockedKey: a key on the secure lock screen. A digit, OK or the
// left soft key opens the PIN pad (which also dials emergency numbers),
// the call key opens it for an emergency call.
func (c *Core) secureLockedKey(k Key) {
	switch {
	case k.Name == KeyRunes && len(k.Runes) == 1 && unicode.IsDigit(k.Runes[0]):
		c.openLockPad(string(k.Runes), false)
	case k.Name == KeyEnter || k.Name == KeyTab:
		c.openLockPad("", false)
	case k.Name == KeyCall:
		c.openLockPad("", true)
	case k.Name == KeyF4:
		// Back to the lock screen after an emergency call.
		if !c.anyCall() {
			c.hideBehindLock()
		}
	case k.Name == KeyEsc:
	default:
		c.showToast(tr.T("Locked: press OK to enter the PIN"))
	}
}

// callApp: the app shows a call in progress (a VoIP app's call, the dialer
// for a cellular one); the secure lock lets its call screen through.
func (c *Core) callApp(a *App) bool {
	if a == nil {
		return false
	}
	for _, ci := range c.calls {
		if ci.app == a {
			return true
		}
	}
	if a.M.ID == appDialer {
		for _, call := range c.cells {
			if call.State != "ended" {
				return true
			}
		}
	}
	return false
}

// lockedCallKey: a key the call screen gets while the phone is securely
// locked; the others would leave it for the rest of the phone.
func lockedCallKey(k Key) bool {
	switch k.Name {
	case KeyTab, KeyF3, KeyEsc, KeyBackspace, KeyLeft, KeyRight, KeyIME, KeySym:
		return false
	case KeyRunes:
		return string(k.Runes) != "/"
	}
	return true
}

// lockHides: the lock screen hides what notifications say.
func (c *Core) lockHides() bool { return c.secureLocked() && !c.lock.prefs.ShowContent }

// idleNotifLines are the standby screen's notification lines: the newest
// three, or, on a lock screen that hides them, each app with its count.
func (c *Core) idleNotifLines() []string {
	var lines []string
	if c.lockHides() {
		count := map[*App]int{}
		var apps []*App
		for _, n := range c.notifs {
			if count[n.app] == 0 {
				apps = append(apps, n.app)
			}
			count[n.app]++
		}
		for i, a := range apps {
			if i == 3 {
				lines = append(lines, tr.T("Notifications from %d more apps", len(apps)-3))
				break
			}
			lines = append(lines, tr.T("%s: %d notifications", a.M.Name, count[a]))
		}
		return lines
	}
	for i, n := range c.notifs {
		if i == 3 {
			lines = append(lines, tr.T("%d more notifications", len(c.notifs)-3))
			break
		}
		lines = append(lines, tr.T("%s: %s", n.app.M.Name, n.n.Title))
	}
	return lines
}

// notifBanner is the banner of a new notification: on a lock screen that
// hides them, the app's name only.
func (c *Core) notifBanner(a *App, n ui.Notification) *banner {
	b := &banner{title: a.M.Name + " · " + n.Title, body: n.Body, until: time.Now().Add(4 * time.Second)}
	if c.lockHides() {
		b.title, b.body = a.M.Name, tr.T("New notification")
	}
	return b
}

// lockHint is the standby screen's hint line while locked ("" for the
// keypad lock without shortcuts, as before).
func (c *Core) lockHint() string {
	var parts []string
	if c.lock.pin {
		parts = append(parts, tr.T("OK: enter the PIN"))
		switch {
		case c.fingerUnlockReady():
			parts = append(parts, tr.T("or touch the fingerprint sensor"))
		case c.lock.prefs.FingerUnlock && len(c.lock.st.Fingers) > 0:
			parts = append(parts, c.fingerBlockedText())
		}
	}
	for _, s := range lockShortcuts {
		parts = append(parts, keyLabel(s.Key)+" "+tr.T(s.Label))
	}
	return strings.Join(parts, " · ")
}

// fingerBlockedText says why a fingerprint cannot unlock yet.
func (c *Core) fingerBlockedText() string {
	switch c.lock.st.Blocked {
	case keystore.BlockedRestart:
		return tr.T("After a restart the PIN is needed first")
	case keystore.BlockedTimeout:
		return tr.T("The PIN is needed once every 48 hours")
	case keystore.BlockedLockout:
		return tr.T("Too many tries with a fingerprint: enter the PIN")
	}
	return ""
}
