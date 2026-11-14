package shell

import (
	"errors"
	"time"

	"github.com/kario-os/kario/ui"
)

// Location (simulated fix; production reads the GNSS of the modem). Only the
// foreground app may ask, with the location runtime permission, and the status
// bar shows that it did. Apps get coordinates only; turning them into a
// place name is the app's (or its server's) business.

const locShown = 3 * time.Second

func (c *Core) location(a *App) (ui.Location, error) {
	if c.fg != a {
		return ui.Location{}, errors.New("location is for the foreground app only")
	}
	c.locAt = time.Now()
	c.log.Printf("%s: location", a.M.ID)
	return ui.Location{Lat: 39.9087, Lon: 116.3975, AccuracyM: 30, At: c.now().Unix()}, nil
}

func (c *Core) locating() bool { return !c.locAt.IsZero() && time.Since(c.locAt) < locShown }
