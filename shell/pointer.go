package shell

import (
	"fmt"
	"strings"
)

// Tap is a pointer's click on body node i of the frame for screen (in a
// grid, its tile): the node takes the focus and is chosen as OK would
// choose it. A click meant for a screen since replaced, under a dialog, on
// the locked keypad or on what cannot take the focus does nothing.
func (c *Core) Tap(screen string, node, tile int) {
	if c.Wake("key") {
		return
	}
	c.lastKey = c.now()
	a := c.current()
	if c.keylock || len(c.overlays) > 0 || a == nil {
		return
	}
	v := a.top()
	if v == nil || screen != v.frameScreen || node < 0 || node >= len(v.frameIDs) || tile < 0 || tile >= len(v.frameIDs[node]) {
		return
	}
	id := v.frameIDs[node][tile]
	i := v.indexOf(id)
	if i < 0 || !v.S.Body[i].Focusable() {
		return
	}
	v.focus, v.focusIdx, v.ensure, v.stick = id, i, true, false
	c.Key(Named(KeyEnter))
}

// tapText clicks the body node whose title holds text, at the indices the
// renderer would report for it (the selftest's "tap").
func (c *Core) tapText(text string) error {
	a := c.current()
	if a == nil || a.top() == nil {
		return fmt.Errorf("no screen")
	}
	v := a.top()
	if len(v.frameIDs) == 0 {
		return fmt.Errorf("no frame laid out (the case needs a pixel renderer: require-gui)")
	}
	for i, ids := range v.frameIDs {
		for j, id := range ids {
			if k := v.indexOf(id); k >= 0 && strings.Contains(v.S.Body[k].Title, text) {
				c.Tap(v.frameScreen, i, j)
				return nil
			}
		}
	}
	return fmt.Errorf("no node titled %q in the frame", text)
}
