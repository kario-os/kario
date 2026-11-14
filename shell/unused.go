package shell

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kario-os/kario/ui"
)

// Grants of apps the user stopped using (Android 11's permission auto-reset):
// an installed app not opened for unusedAfter loses its runtime grants
// (camera, microphone, contacts, photos, location, its mini-programs' too);
// using one again asks again. One notification from Settings says which
// apps, and Settings › App permissions marks what was taken back until it is
// granted again.
// System apps keep theirs: they come with the image.

const (
	unusedAfter      = 90 * 24 * time.Hour
	unusedCheckEvery = time.Hour
)

type appUse struct {
	Opened map[string]int64            `json:"opened"`          // unix ms
	Reset  map[string]map[string]int64 `json:"reset,omitempty"` // key -> perm -> unix ms
}

func (c *Core) usePath() string { return filepath.Join(c.cfg.StateDir, "shell", "app-use.json") }

func (c *Core) appUse() *appUse {
	if c.use == nil {
		c.use = &appUse{}
		if raw, err := os.ReadFile(c.usePath()); err == nil {
			_ = json.Unmarshal(raw, c.use)
		}
		if c.use.Opened == nil {
			c.use.Opened = map[string]int64{}
		}
		if c.use.Reset == nil {
			c.use.Reset = map[string]map[string]int64{}
		}
	}
	return c.use
}

func (c *Core) saveUse() {
	raw, _ := json.Marshal(c.appUse())
	_ = os.MkdirAll(filepath.Dir(c.usePath()), 0o700)
	_ = os.WriteFile(c.usePath(), raw, 0o600)
}

// opened records that the user brought an installed app to the screen.
func (c *Core) opened(a *App) {
	if a == nil || !a.M.installed {
		return
	}
	c.appUse().Opened[a.M.ID] = c.now().UnixMilli()
	c.saveUse()
}

// checkUnused takes back the runtime grants of installed apps unused for
// unusedAfter; an app first seen here starts its count now.
func (c *Core) checkUnused() {
	now := c.now()
	if now.Sub(c.unusedAt) < unusedCheckEvery {
		return
	}
	c.unusedAt = now
	use := c.appUse()
	changed := false
	var names []string
	for _, a := range c.apps {
		if !a.M.installed {
			continue
		}
		last, ok := use.Opened[a.M.ID]
		if !ok {
			use.Opened[a.M.ID], changed = now.UnixMilli(), true
			continue
		}
		if now.Sub(time.UnixMilli(last)) < unusedAfter {
			continue
		}
		took := false
		for key, grants := range c.broker.perms {
			if key != a.M.ID && !strings.HasPrefix(key, a.M.ID+"/") {
				continue
			}
			for perm, state := range grants {
				if _, runtime := ui.RuntimePerms[perm]; !runtime || state != "granted" {
					continue
				}
				delete(grants, perm)
				if use.Reset[key] == nil {
					use.Reset[key] = map[string]int64{}
				}
				use.Reset[key][perm], took, changed = now.UnixMilli(), true, true
			}
		}
		if took {
			c.log.Printf("%s: not opened since %s; runtime grants taken back", a.M.ID, time.UnixMilli(last).Format(time.DateOnly))
			names = append(names, tr.T("“%s”", a.M.Name))
		}
	}
	if !changed {
		return
	}
	c.saveUse()
	if len(names) == 0 {
		return
	}
	c.broker.save()
	if s := c.appByID(healthApp); s != nil {
		c.addNotif(s, ui.Notification{ID: ui.NotifUnusedPerms, Title: tr.T("Permissions of long-unused apps taken back"),
			Body: tr.T("%s not opened for %d days: grants such as camera and location were taken back and will be asked again on next use", strings.Join(names, ""), int(unusedAfter.Hours()/24))})
	}
}

// resetAt: when the grant was taken back for disuse, 0 if it was not (or was
// granted or set since).
func (c *Core) resetAt(key, perm string) int64 { return c.appUse().Reset[key][perm] }

func (c *Core) clearReset(key, perm string) {
	if c.appUse().Reset[key][perm] == 0 {
		return
	}
	delete(c.use.Reset[key], perm)
	if len(c.use.Reset[key]) == 0 {
		delete(c.use.Reset, key)
	}
	c.saveUse()
}
