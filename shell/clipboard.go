package shell

import (
	"errors"
	"strings"
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

// copyText puts what the user copied from a field on the clipboard.
func (c *Core) copyText(text string) {
	if len(text) > clipMax {
		text = strings.ToValidUTF8(text[:clipMax], "")
	}
	c.clip = clipboard{text: text, at: c.now()}
	c.showToast(tr.T("Copied"))
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
	if n == nil || n.Kind != ui.KindInput || text == "" || dateField(n.Keyboard) {
		return menuEntry{}, false
	}
	id := n.ID
	return menuEntry{group: tr.T("System"), label: tr.T("Paste “%s”", cv.Truncate(text, 10)), key: "v", run: func() {
		v.insertText(id, text)
	}}, true
}

// editEntries offer what Ctrl + C, X and A do for a focused text field
// with text, for a keyboard without Ctrl and the 9-key pad: the E71's
// Options › Copy. The system does them, as it pastes.
func (c *Core) editEntries(v *View, n *ui.Node) []menuEntry {
	if n == nil || n.Kind != ui.KindInput || v.inputs[n.ID] == "" {
		return nil
	}
	id, group := n.ID, tr.T("System")
	do := func(key string) func() {
		return func() {
			if n := v.node(id); n != nil {
				c.editKey(v, n, Named(key))
			}
		}
	}
	var es []menuEntry
	if !n.Secret {
		es = append(es, menuEntry{group: group, label: tr.T("Copy"), run: do(KeyCopy)})
		if _, _, sel := v.edit(id).selection(); sel {
			es = append(es, menuEntry{group: group, label: tr.T("Cut"), run: do(KeyCut)})
		}
	}
	return append(es, menuEntry{group: group, label: tr.T("Select all"), run: do(KeySelectAll)})
}
