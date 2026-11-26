package shell

import (
	"errors"
	"fmt"

	"github.com/kario-os/kario/ui"
)

// ShowDevPage shows s as the developer's app's only page without starting
// the app (kario preview: a page its preview function returned, drawn
// without the app's process). The page only shows, as a tombstoned app's
// old pages do: there is no process to run its actions.
func (c *Core) ShowDevPage(s ui.Screen) error {
	for _, a := range c.apps {
		if !c.isDev(a) {
			continue
		}
		if a.running {
			return fmt.Errorf("%s is running: a preview shows pages without the app", a.M.ID)
		}
		a.stack, a.stale = []*View{newView(s)}, true
		c.setForeground(a)
		c.toast = nil
		return nil
	}
	return errors.New("no developer's app (--dev-app) to show the page as")
}
