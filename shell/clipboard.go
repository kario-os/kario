package shell

import (
	"errors"
	"time"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ui"
)

// The clipboard is write-only for apps: clipboard.set stores text, and there
// is no way to read it back. Pasting is the user's act, offered by the
// system in the options menu of a focused input field, which then receives
// the text like typing. So no app can watch the clipboard (Android 10 and
// iOS 16 only restrict that), and copied text expires after 10 minutes.

const (
	clipMax  = 16 << 10
	clipLife = 10 * time.Minute
)

type clipboard struct {
	text string
	app  string
	at   time.Time
}

func (c *Core) clipSet(a *App, text string) error {
	if len(text) > clipMax {
		return errors.New("too much text for the clipboard")
	}
	c.clip = clipboard{text: text, app: a.M.ID, at: c.now()}
	c.showToast(tr.T("Copied"))
	return nil
}

// clipText is the clipboard content while it is fresh.
func (c *Core) clipText() string {
	if c.clip.text == "" || c.now().Sub(c.clip.at) > clipLife {
		c.clip = clipboard{}
		return ""
	}
	return c.clip.text
}

// pasteEntry offers Paste for a focused input field.
func (c *Core) pasteEntry(v *View, n *ui.Node) (menuEntry, bool) {
	text := c.clipText()
	if n == nil || n.Kind != ui.KindInput || text == "" {
		return menuEntry{}, false
	}
	return menuEntry{group: tr.T("System"), label: tr.T("Paste “%s”", cv.Truncate(text, 10)), key: "v", run: func() {
		v.inputs[n.ID] += text
	}}, true
}
