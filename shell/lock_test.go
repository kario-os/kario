package shell

import (
	"io"
	"log"
	"strings"
	"testing"

	"github.com/kario-os/kario/ui"
)

// lockCore is a bare Core for the lock's tests; other features' lock-screen
// shortcuts are set aside while it is in use.
func lockCore(t *testing.T) (*Core, *App) {
	saved := lockShortcuts
	lockShortcuts = nil
	t.Cleanup(func() { lockShortcuts = saved })
	app := &App{M: &Manifest{ID: "com.example.wallet", Name: "W", Permissions: []string{ui.PermNotify}}}
	c := &Core{log: log.New(io.Discard, "", 0), sys: &App{system: true, M: &Manifest{ID: "system"}}}
	c.sys.stack = []*View{newView(ui.Screen{ID: "sys.idle"})}
	return c, app
}

func overlayOf[T overlay](c *Core) (T, bool) {
	var zero T
	if len(c.overlays) == 0 {
		return zero, false
	}
	o, ok := c.overlays[len(c.overlays)-1].(T)
	return o, ok
}

// Without a PIN the keypad lock is as it was: digits only explain how to
// unlock, and nothing is hidden.
func TestKeypadLockWithoutAPIN(t *testing.T) {
	c, app := lockCore(t)
	c.notify(app, ui.Notification{ID: "1", Title: "Paid", Body: "66"})
	c.lockNow("test")
	if !c.locked() || c.secureLocked() {
		t.Fatalf("locked %v, secure %v", c.locked(), c.secureLocked())
	}
	if got := c.idleNotifLines(); len(got) != 1 || got[0] != tr.T("%s: %s", "W", "Paid") {
		t.Fatalf("lines %q", got)
	}
	if h := c.lockHint(); h != "" {
		t.Fatalf("hint %q", h)
	}
	c.Key(Runes("5"))
	if len(c.overlays) != 0 || c.toast == nil || c.toast.body != tr.T("Press “Unlock”, then *") {
		t.Fatalf("overlays %d, toast %+v", len(c.overlays), c.toast)
	}
}

// With a PIN, the lock screen hides what notifications say, a digit opens
// the PIN pad with that digit, and the call key there dials only emergency
// numbers.
func TestSecureLockScreen(t *testing.T) {
	c, app := lockCore(t)
	c.lock.pin = true
	c.notify(app, ui.Notification{ID: "1", Title: "Paid", Body: "66"})
	c.notify(app, ui.Notification{ID: "2", Title: "Paid again", Body: "77"})
	c.lockNow("test")
	if !c.secureLocked() {
		t.Fatal("not securely locked")
	}
	if got := c.idleNotifLines(); len(got) != 1 || got[0] != tr.T("%s: %d notifications", "W", 2) {
		t.Fatalf("lines %q", got)
	}
	if b := c.notifBanner(app, ui.Notification{Title: "Paid", Body: "66"}); b.title != "W" || strings.Contains(b.body, "66") {
		t.Fatalf("banner %+v", b)
	}
	c.lock.prefs.ShowContent = true
	if got := c.idleNotifLines(); got[0] != tr.T("%s: %s", "W", "Paid again") {
		t.Fatalf("lines with contents allowed %q", got)
	}
	c.lock.prefs.ShowContent = false
	c.Key(Runes("1"))
	pad, ok := overlayOf[*lockPadOverlay](c)
	if !ok || pad.digits != "1" {
		t.Fatalf("pad %+v", pad)
	}
	c.Key(Runes("2"))
	c.Key(Named(KeyCall))
	if pad.note == "" || !pad.bad {
		t.Fatalf("calling 12 locked: %+v", pad)
	}
	c.Key(Named(KeyEnter))
	if pad.note != tr.T("The PIN is %d to %d digits", 4, 16) || pad.digits != "" {
		t.Fatalf("a short PIN: %+v", pad)
	}
	c.Key(Named(KeyEsc))
	if len(c.overlays) != 0 || !c.locked() {
		t.Fatalf("after Back: %d overlays, locked %v", len(c.overlays), c.locked())
	}
}

// Lock-screen shortcuts answer before the lock's own keys, in both kinds
// of lock, and the hint names them.
func TestLockShortcuts(t *testing.T) {
	ran := 0
	for _, pin := range []bool{false, true} {
		c, _ := lockCore(t)
		lockShortcuts = []lockShortcut{{Key: KeyF2, Label: "Torch", Run: func(*Core) { ran++ }}}
		c.lock.pin = pin
		c.lockNow("test")
		c.Key(Named(KeyF2))
		if !strings.Contains(c.lockHint(), "f2 Torch") {
			t.Fatalf("pin %v: hint %q", pin, c.lockHint())
		}
		if len(c.overlays) != 0 {
			t.Fatalf("pin %v: the lock took the shortcut's key", pin)
		}
	}
	if ran != 2 {
		t.Fatalf("the shortcut ran %d times", ran)
	}
}

func TestLockedCallKeys(t *testing.T) {
	for _, k := range []Key{Named(KeyEnter), Named(KeyF4), Named(KeyCall), Named(KeyUp), Runes("5"), Runes("#")} {
		if !lockedCallKey(k) {
			t.Errorf("%+v is kept from the call screen", k)
		}
	}
	for _, k := range []Key{Named(KeyTab), Named(KeyF3), Named(KeyEsc), Named(KeyBackspace), Named(KeyLeft), Runes("/")} {
		if lockedCallKey(k) {
			t.Errorf("%+v leaves the call screen while locked", k)
		}
	}
}
