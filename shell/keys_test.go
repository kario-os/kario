package shell

import (
	"testing"
	"time"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// powerCore is a bare Core with NFC up and two door cards known; the
// quick view's state request fails (no nfcd) and keeps them.
func powerCore(t *testing.T) *Core {
	c, _ := lockCore(t)
	c.ev = make(chan func(), 16)
	c.nfc.up, c.nfc.out = true, make(chan svc.NFCPolicyArgs, 1)
	c.ime = &imeClient{}
	c.nfc.state = ui.NFCState{DoorFirst: "door:2", Cards: []ui.NFCCardInfo{
		{ID: "door:1", Name: "Home", Kind: ui.NFCDoor}, {ID: "door:2", Name: "Office", Kind: ui.NFCDoor}}}
	return c
}

// quickView runs what the shell's loop got and reports the quick view's
// entries, if it is open.
func quickView(t *testing.T, c *Core) []string {
	t.Helper()
	for {
		select {
		case f := <-c.ev:
			f()
			continue
		case <-time.After(100 * time.Millisecond):
		}
		break
	}
	m, ok := overlayOf[*menuOverlay](c)
	if !ok || m.title != tr.T("Cards") {
		return nil
	}
	var out []string
	for _, e := range m.entries {
		out = append(out, e.label)
	}
	return out
}

func TestPowerKey(t *testing.T) {
	c := powerCore(t)
	c.PowerKey(false)
	if !c.ScreenOff() {
		t.Fatal("a press left the screen on")
	}
	c.nfc.powerAt = time.Time{}
	c.PowerKey(false)
	if c.ScreenOff() {
		t.Fatal("a press left the screen off")
	}
	c.nfc.powerAt = time.Time{}
	c.PowerKey(false)
	c.PowerKey(false)
	got := quickView(t, c)
	if c.ScreenOff() || len(got) != 2 || got[0] != "Home · "+tr.T("Door card") || got[1] != "Office · "+tr.T("Door card · auto") {
		t.Fatalf("a double press: screen off %v, quick view %q", c.ScreenOff(), got)
	}
	c.Key(Runes("1"))
	if quickView(t, c) != nil {
		t.Fatal("choosing a card left the quick view open")
	}
	c.PowerKey(true)
	if m, ok := overlayOf[*menuOverlay](c); !ok || m.title != tr.T("Power") {
		t.Fatal("a long press opened no power menu")
	}
	c.Key(Runes("1"))
	if c.toast == nil || c.toast.body != tr.T("Not a phone: it stays on") {
		t.Fatalf("power off on a developer machine: %+v", c.toast)
	}
}

// The end key is the power key too: its double press on standby after a
// pause opens the quick view, a burst of presses does not, a long press
// opens the power menu.
func TestEndKeyAsPowerKey(t *testing.T) {
	c := powerCore(t)
	c.Key(Named(KeyF4))
	c.Key(Named(KeyF4))
	if got := quickView(t, c); len(got) != 2 {
		t.Fatalf("a double press on standby: %q", got)
	}
	c.Key(Named(KeyEsc))
	// The red key pressed again and again, from just after a press (as
	// from an app: that one went back to standby).
	c.nfc.endAt = time.Now()
	for i := 0; i < 3; i++ {
		c.Key(Named(KeyF4))
	}
	if got := quickView(t, c); got != nil {
		t.Fatalf("a burst of presses opened the quick view: %q", got)
	}
	c.SetEndKeyPicker(false)
	c.Key(Named(KeyF4))
	c.Key(Named(KeyF4))
	if got := quickView(t, c); got != nil {
		t.Fatalf("the double press while turned off: %q", got)
	}
	c.Key(Key{Name: KeyF4, Long: true})
	if m, ok := overlayOf[*menuOverlay](c); !ok || m.title != tr.T("Power") {
		t.Fatal("holding the end key opened no power menu")
	}
}
