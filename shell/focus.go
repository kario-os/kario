package shell

import "github.com/kario-os/kario/ui"

// updateFocus tells the app in front when a system dialog or menu, or the
// screen going off, takes the keys from it, and when it gets them back.
func (c *Core) updateFocus() {
	a := c.fg
	if a != c.focusApp {
		c.focusApp = a
		if a != nil {
			a.focusLost = false
		}
	}
	if a == nil || a.system || !a.running {
		return
	}
	lost := c.screen.off || len(c.overlays) > 0
	if lost == a.focusLost {
		return
	}
	a.focusLost = lost
	a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventFocus, Value: map[bool]string{true: ui.FocusLost, false: ui.FocusGained}[lost]})
}
