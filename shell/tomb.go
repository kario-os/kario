package shell

import (
	"encoding/json"
	"time"

	"github.com/kario-os/kario/ui"
)

// Tombstones (iOS shows a suspended app's snapshot and lets it restore its
// state after jetsam ended it; Android recreates the activities from their
// saved bundles): when the low-memory killer ends a background app, the
// shell keeps the state the app last handed over (ui.MsgState) and its pages
// as they were. The app stays in Switch apps; opening it shows those pages at
// once, the new process starts with the state (start's Data) and rebuilds
// its pages, and a rebuilt page with the same screen ID gets back what the
// user had typed into it and where the focus was. Until the first rebuilt
// page arrives the old ones only show: their actions belonged to the process
// that is gone.

// tombKeep: a day later the user has moved on; the app starts afresh.
const tombKeep = 24 * time.Hour

type tombstone struct {
	state json.RawMessage
	views []*View
	at    time.Time
}

// bury keeps what an app the low-memory killer ended needs to come back; any
// other exit leaves nothing.
func (c *Core) bury(a *App) {
	a.tomb = nil
	if a.ending && len(a.stack) > 0 {
		a.tomb = &tombstone{state: a.saved, views: a.stack, at: c.now()}
		c.log.Printf("%s: tombstone with %d pages, %d bytes of state", a.M.ID, len(a.stack), len(a.saved))
	}
	a.saved = nil
}

// exhume shows a tombstoned app's pages while its new process starts, and
// returns the state to start it with.
func (c *Core) exhume(a *App) json.RawMessage {
	t := a.tomb
	a.tomb = nil
	if t == nil || c.now().Sub(t.at) > tombKeep {
		return nil
	}
	a.stack, a.stale = t.views, true
	a.revive = map[string]*View{}
	for _, v := range t.views {
		a.revive[v.S.ID] = v
	}
	return t.state
}

// page is the view of a page the app sent; one it rebuilt after a tombstone
// takes over the old page's typing and focus. The first page ends the
// showing of the old ones.
func (a *App) page(s ui.Screen) *View {
	if a.stale {
		a.stack, a.stale = nil, false
	}
	v := newView(s)
	if old := a.revive[s.ID]; old != nil {
		delete(a.revive, s.ID)
		v.adopt(old)
	}
	return v
}

func (v *View) adopt(old *View) {
	for id, text := range old.inputs {
		if _, ok := v.inputs[id]; ok && text != old.appVals[id] {
			v.inputs[id] = text
		}
	}
	if i := v.indexOf(old.focus); i >= 0 && v.S.Body[i].Focusable() {
		v.focus, v.focusIdx, v.ensure = old.focus, i, true
	}
}

// ForgetTombstone drops what was kept of a stopped app, as Stop running in
// Switch apps does: it starts afresh (selftest).
func (c *Core) ForgetTombstone(id string) {
	if a := c.appByID(id); a != nil && !a.running {
		a.tomb = nil
		c.removeMRU(a)
	}
}

// HasSavedState: the app has handed over its state (selftest).
func (c *Core) HasSavedState(id string) bool {
	a := c.appByID(id)
	return a != nil && len(a.saved) > 0
}

// Revived: the app runs, ready, and shows its own pages again (selftest).
func (c *Core) Revived(id string) bool {
	a := c.appByID(id)
	return a != nil && a.ready && !a.stale && len(a.stack) > 0
}

// Keys pressed while an app has no live page yet (it is starting, or its old
// pages only show) are kept, up to maxHeldKeys, and replayed once its new
// pages have settled: keysSettle after the last one arrived. Someone who
// opens an app and types at once loses nothing.
const (
	maxHeldKeys = 32
	keysSettle  = 150 * time.Millisecond
)

func (c *Core) replayKeysSoon(a *App) {
	if len(a.heldKeys) == 0 {
		return
	}
	a.keysGen++
	gen := a.keysGen
	time.AfterFunc(keysSettle, func() {
		c.Post(func() {
			if a.keysGen != gen || a.stale || !a.running {
				return
			}
			keys := a.heldKeys
			a.heldKeys = nil
			if c.current() != a {
				return
			}
			for _, k := range keys {
				c.Key(k)
			}
		})
	})
}

// saveState keeps what the app handed over for a new process.
func (c *Core) saveState(a *App, data json.RawMessage) {
	if len(data) > ui.MaxSavedState || !json.Valid(data) {
		c.log.Printf("%s: saved state of %d bytes dropped (at most %d bytes of JSON)", a.M.ID, len(data), ui.MaxSavedState)
		return
	}
	a.saved = append(json.RawMessage(nil), data...)
}
