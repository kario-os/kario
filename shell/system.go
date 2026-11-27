package shell

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/ui"
)

// System screens use the same phone.Ui model as apps; their actions are
// handled inside the shell.

func (c *Core) unread(a *App) int {
	n := 0
	for _, x := range c.notifs {
		if x.app == a {
			n++
		}
	}
	return n
}

// idleInfo is what the idle screen shows, for both renderers.
type idleInfo struct {
	Time, Date, Operator string
	Lines                []string
}

func (c *Core) idleInfo() idleInfo {
	now := c.shownNow()
	in := idleInfo{Time: c.clockText(now), Date: dateText(now),
		Operator: tr.T("No service")}
	if n := c.simNet(); n != "" {
		in.Operator = n
	} else if c.tel != nil && c.tel.Registered && c.tel.Operator != "" {
		in.Operator = c.tel.Operator
	}
	in.Lines = append(in.Lines, c.idleNotifLines()...)
	var next *alarmEntry
	var alarms []*alarmEntry
	if c.alarms != nil {
		alarms = c.alarms.list
	}
	for _, e := range alarms {
		if e.On && !e.Next.IsZero() && (next == nil || e.Next.Before(next.Next)) {
			next = e
		}
	}
	if next != nil {
		line := tr.T("Alarm %s", next.Time)
		if next.Snoozed {
			line = tr.T("Alarm %s (snoozed)", next.Next.In(c.zone()).Format("15:04"))
		}
		if next.Label != "" {
			line += " " + next.Label
		}
		in.Lines = append(in.Lines, line)
	}
	for _, st := range c.streams {
		if st.Category == audio.Media && st.State == ui.PlayPlaying {
			in.Lines = append(in.Lines, tr.T("Playing %s", st.Name))
			break
		}
	}
	in.Lines = append(in.Lines, c.widgetLines()...)
	return in
}

// idleScreen is the Nokia standby screen: OK opens the menu, digits dial,
// the right softkey opens contacts. In the text renderer it is ordinary
// nodes; the pixel renderer draws it from Frame.Idle.
func (c *Core) idleScreen() ui.Screen {
	in := c.idleInfo()
	s := ui.Screen{ID: "sys.idle", Title: in.Operator, Center: "menu", Right: "contacts",
		Actions: []ui.Action{
			{ID: "menu", Label: tr.T("Menu")},
			{ID: "contacts", Label: tr.T("Contacts")},
			{ID: "notifs", Label: tr.T("Notification center")},
			{ID: "tasks", Label: tr.T("Switch apps")},
			{ID: "lock", Label: tr.T("Lock keypad"), Key: "*"},
		}}
	s.Body = append(s.Body,
		ui.Node{Kind: ui.KindText, Text: " "},
		ui.Node{Kind: ui.KindText, Style: ui.StyleBig, Text: in.Time},
		ui.Node{Kind: ui.KindText, Style: ui.StyleBig, Text: in.Date},
		ui.Node{Kind: ui.KindText, Text: " "},
	)
	for _, l := range in.Lines {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleAccent, Text: l})
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleDim, Text: c.idleHint()})
	return s
}

func (c *Core) idleHint() string {
	if c.keylock {
		return c.lockHint()
	}
	if c.liveCell() != nil || len(c.calls) > 0 {
		return tr.T("Call key: back to the call") + " · " + tr.T("OK menu")
	}
	if c.ime.Keypad() == ime.T9Pad {
		return tr.T("OK menu · digit keys dial · ↑ notifications")
	}
	return tr.T("OK menu · digits dial · letters search · ↑ notifications")
}

// idleRunes handles a typed key on the idle screen: digits start a call,
// anything else a search, as on a Nokia (digits) and a BlackBerry (letters).
// During a call the digits start a second one (the dialer holds the first),
// or say why none can go out and how to get back to the call.
func (c *Core) idleRunes(v *View, r string) bool {
	if r == "/" || r == "" {
		return false
	}
	if strings.Trim(r, "0123456789*#+") == "" {
		if why := c.cellBlocked(); why != "" && c.liveCell() != nil {
			c.showToast(c.callBlockedHint(why))
			return true
		}
		if d := c.appByID(appDialer); d != nil {
			c.Launch(d, "dial:"+r)
		}
		return true
	}
	c.openMenu(c.sys, v, true)
	if m, ok := c.overlays[len(c.overlays)-1].(*menuOverlay); ok {
		m.query, m.dial = r, c.idleDial
	}
	return true
}

// idleDial is the standby palette's entry for the number its keys stand
// for on a keyboard with an Fn layer: as the digits typed on a 9-key pad
// do, it opens the dialer with the number.
func (c *Core) idleDial(query string) (menuEntry, bool) {
	d := c.dialFor(query)
	if d == "" {
		return menuEntry{}, false
	}
	return menuEntry{group: tr.T("Phone"), label: tr.T("Dial %s", d), key: tr.T("Call key"), run: func() {
		if a := c.appByID(appDialer); a != nil {
			c.Launch(a, "dial:"+d)
		}
	}}, true
}

// menuColumns follows the display: 3 on a portrait bar phone, more on the
// landscape keyboard phones (the E90's inner screen showed a wider grid).
func (c *Core) menuColumns() int {
	d := c.screenDev
	switch {
	case d.Height == 0:
		return 3
	case d.Width*10 >= d.Height*20:
		return 5
	case d.Width > d.Height:
		return 4
	}
	return 3
}

// Menu folders, as on Nokia's menu: the main menu keeps what is used every
// day, the rest goes in a folder. Apps name theirs in the manifest; apps
// from the store go in Apps unless they name another. A folder's icon is
// its name's first character.
// menuFolders' names are English, translated when shown (the language
// can change while the shell runs).
var menuFolders = []struct {
	ID, Name string
	Order    int
}{
	{"tools", "Tools", 40},
	{"apps", "Apps", 41},
}

// initial is the first character of s.
func initial(s string) string {
	_, n := utf8.DecodeRuneInString(s)
	return s[:n]
}

func (c *Core) appFolder(a *App) string {
	for _, f := range menuFolders {
		if a.M.Folder == f.ID {
			return f.ID
		}
	}
	if a.M.Folder != "" || a.M.installed {
		return "apps"
	}
	return ""
}

func (c *Core) appTile(a *App) ui.Node {
	n := ui.Node{Kind: ui.KindItem, Style: ui.StyleTile, ID: "app:" + a.M.ID, Icon: a.M.Icon, Title: a.M.Name,
		Subtitle: a.M.Desc, Actions: []ui.Action{{ID: ui.ActionOpen, Label: tr.T("Open"), Primary: true}}}
	if u := c.unread(a); u > 0 {
		n.Badge = fmt.Sprint(u)
	}
	if a.running {
		n.Subtitle = tr.T("Running · %s", a.M.Desc)
		n.Actions = append(n.Actions, ui.Action{ID: "kill", Label: tr.T("Stop running"), Key: "k", Group: ui.GroupDanger})
	}
	if why := heldLabel(c.held(a)); why != "" {
		n.Subtitle = why
	}
	return n
}

func (c *Core) gridScreen(id, title string) ui.Screen {
	return ui.Screen{ID: id, Title: title, Right: "exit", Columns: c.menuColumns(),
		Actions: []ui.Action{
			{ID: "exit", Label: tr.T("Exit")},
			{ID: "tasks", Label: tr.T("Switch apps"), Key: "t"},
			{ID: "notifs", Label: tr.T("Notification center"), Key: "n"},
		}}
}

func (c *Core) menuScreen() ui.Screen {
	s := c.gridScreen("sys.menu", tr.T("Menu"))
	folders := menuFolders
	for _, a := range c.apps {
		for len(folders) > 0 && folders[0].Order <= a.M.Order {
			s.Body = append(s.Body, c.folderTile(folders[0].ID, tr.T(folders[0].Name)))
			folders = folders[1:]
		}
		if c.appFolder(a) == "" && !c.minorHidden(a) {
			s.Body = append(s.Body, c.appTile(a))
		}
	}
	for _, f := range folders {
		s.Body = append(s.Body, c.folderTile(f.ID, f.Name))
	}
	s.Body = append(s.Body,
		ui.Node{Kind: ui.KindText, Style: ui.StyleDim, Text: tr.T("Digits 1–9 open directly · / search · Back or F4 to standby")},
	)
	return s
}

// folderTile names what is inside and adds up its apps' unread counts.
func (c *Core) folderTile(id, name string) ui.Node {
	var names []string
	unread := 0
	for _, a := range c.apps {
		if c.appFolder(a) == id && !c.minorHidden(a) {
			names = append(names, a.M.Name)
			unread += c.unread(a)
		}
	}
	n := ui.Node{Kind: ui.KindItem, Style: ui.StyleTile, ID: "folder:" + id, Icon: initial(name), Title: name,
		Subtitle: strings.Join(names, tr.T(", ")), Actions: []ui.Action{{ID: ui.ActionOpen, Label: tr.T("Open"), Primary: true}}}
	if unread > 0 {
		n.Badge = fmt.Sprint(unread)
	}
	return n
}

func (c *Core) folderScreen(id string) ui.Screen {
	name := id
	for _, f := range menuFolders {
		if f.ID == id {
			name = tr.T(f.Name)
		}
	}
	s := c.gridScreen("sys.folder:"+id, name)
	for _, a := range c.apps {
		if c.appFolder(a) == id && !c.minorHidden(a) {
			s.Body = append(s.Body, c.appTile(a))
		}
	}
	if len(s.Body) == 0 {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleDim, Text: tr.T("No apps here yet")})
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleDim, Text: tr.T("Digits 1–9 open directly · Back to the menu")})
	return s
}

// menuGrid reports whether the screen is the main menu or one of its folders.
// menuGridKeys are the keys of the menu grid's positions, in order.
const menuGridKeys = "123456789*0#"

func menuGrid(v *View) bool {
	return v.S.ID == "sys.menu" || strings.HasPrefix(v.S.ID, "sys.folder:")
}

// systemRebuilt: the system screens systemScreen makes from state, which
// are redrawn as it changes. A page's prompt (sys.prompt) holds the text
// being typed; rebuilding it would fall back to the idle screen.
func systemRebuilt(id string) bool {
	switch id {
	case "sys.idle", "sys.tasks", "sys.notifs", "sys.menu":
		return true
	}
	return strings.HasPrefix(id, "sys.folder:")
}

func (c *Core) systemScreen(id string) ui.Screen {
	switch id {
	case "sys.tasks":
		s := ui.Screen{ID: id, Title: tr.T("Switch apps")}
		for _, a := range c.mru {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "app:" + a.M.ID, Icon: a.M.Icon, Title: a.M.Name,
				Subtitle: c.topTitle(a), Actions: []ui.Action{
					{ID: ui.ActionOpen, Label: tr.T("Switch to"), Primary: true},
					{ID: "kill", Label: tr.T("Stop running"), Key: "k", Group: ui.GroupDanger},
				}})
		}
		if len(c.mru) == 0 {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleDim, Text: tr.T("No apps are running")})
		}
		return s
	case "sys.notifs":
		s := ui.Screen{ID: id, Title: tr.T("Notification center"), Actions: []ui.Action{{ID: "clearall", Label: tr.T("Clear all"), Key: "x"}}}
		s.Body = append(s.Body, c.cellNotifNodes()...)
		for i, n := range c.notifs {
			acts := []ui.Action{{ID: ui.ActionOpen, Label: tr.T("Open"), Primary: true}}
			// The app's own actions (a quick reply) run without bringing it forward.
			for _, a := range n.n.Actions {
				acts = append(acts, ui.Action{ID: "do:" + a.ID, Label: a.Label, Key: a.Key})
			}
			acts = append(acts, ui.Action{ID: "clear", Label: tr.T("Clear"), Key: "d"})
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: fmt.Sprintf("n:%d", i), Icon: n.app.M.Icon,
				Title: n.app.M.Name + " · " + n.n.Title, Subtitle: n.n.Body, Trailing: c.clockText(n.at), Actions: acts})
		}
		if len(c.notifs) == 0 {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleDim, Text: tr.T("No notifications")})
		}
		return s
	case "sys.menu":
		return c.menuScreen()
	}
	if f, ok := strings.CutPrefix(id, "sys.folder:"); ok {
		return c.folderScreen(f)
	}
	return c.idleScreen()
}

// ScreenID names the screen on top: "<app-id>/<screen-id>" ("system/sys.idle").
func (c *Core) ScreenID() string {
	a := c.current()
	if v := a.top(); v != nil {
		return a.M.ID + "/" + v.S.ID
	}
	return a.M.ID + "/"
}

func (c *Core) topTitle(a *App) string {
	if v := a.top(); v != nil {
		return v.S.Title
	}
	if t := a.tomb; t != nil && len(t.views) > 0 {
		return t.views[len(t.views)-1].S.Title
	}
	return ""
}

func (c *Core) openSystem(id string) {
	c.setForeground(nil)
	if id == "sys.notifs" {
		c.markSeen(nil)
	}
	if top := c.sys.top(); top != nil && top.S.ID == id {
		// A screen such as the task list stays under the app opened from it;
		// shown again it is rebuilt first, or keys pressed at once land on a
		// row of the old one.
		if systemRebuilt(id) {
			top.replace(c.systemScreen(id))
		}
		return
	}
	c.sys.stack = append(c.sys.stack, newView(c.systemScreen(id)))
}

func (c *Core) systemAction(v *View, node *ui.Node, act ui.Action) {
	id := ""
	if node != nil {
		id = node.ID
	}
	if c.heldAction(v, node, act) {
		return
	}
	if appAct, ok := strings.CutPrefix(act.ID, "do:"); ok {
		c.notifAction(id, appAct)
		return
	}
	if call, ok := strings.CutPrefix(id, "call:"); ok {
		c.cellRowAction(call, act.ID)
		return
	}
	switch act.ID {
	case "prompt_ok":
		c.endPrompt(true, v.inputs["text"])
	case "prompt_cancel":
		c.endPrompt(false, "")
	case "reply_ok":
		if strings.TrimSpace(v.inputs["text"]) == "" {
			c.showToast(tr.T("Nothing typed yet"))
			return
		}
		c.endReply(v.inputs["text"])
	case "reply_cancel":
		c.endReply("")
	case "menu":
		c.openSystem("sys.menu")
	case "exit":
		c.back()
	case "lock":
		c.lockNow("keypad")
		if c.lock.pin {
			c.showToast(tr.T("Locked"))
		} else {
			c.showToast(tr.T("Keypad locked"))
		}
	case "contacts":
		// The Contacts app, or the Phone app's list in a product without it.
		if d := firstApp(c.appByID(appContacts), c.appByID(appDialer)); d != nil {
			c.sys.stack = c.sys.stack[:1]
			c.Launch(d, "contacts")
		}
	case "tasks":
		c.openSystem("sys.tasks")
	case "notifs":
		c.openSystem("sys.notifs")
	case "clearall":
		c.notifs = nil
		v.replace(c.systemScreen(v.S.ID))
	case "open", "kill", "clear":
		if f, ok := strings.CutPrefix(id, "folder:"); ok {
			c.openSystem("sys.folder:" + f)
			return
		}
		if strings.HasPrefix(id, "app:") {
			a := c.appByID(strings.TrimPrefix(id, "app:"))
			if a == nil {
				return
			}
			if act.ID == "kill" {
				if a.running {
					c.endApp(a, endGrace)
				} else {
					a.tomb = nil
					c.removeMRU(a)
				}
				v.replace(c.systemScreen(v.S.ID))
				return
			}
			c.Launch(a, "")
			return
		}
		var i int
		if _, err := fmt.Sscanf(id, "n:%d", &i); err != nil || i >= len(c.notifs) {
			return
		}
		n := c.notifs[i]
		c.notifs = append(c.notifs[:i], c.notifs[i+1:]...)
		if act.ID == ui.ActionOpen {
			c.Launch(n.app, n.arg)
			c.deliver(n.app, ui.Msg{T: ui.MsgNotifAction, Value: n.n.ID, Action: ui.ActionOpen})
			return
		}
		v.replace(c.systemScreen(v.S.ID))
	}
}

// kill ends an app at once, where a grace (endApp) would leave running
// what has to stop now: an app that broke the protocol or a limit
// (guard.go), one the store took down, one stalled at its memory limit
// (it would only stall on), one whose network was just taken away.
func (c *Core) kill(a *App) {
	if a.running && a.proc != nil {
		a.killed = true
		if a.adopted {
			_ = a.proc.Kill()
		} else {
			killTree(a.proc)
		}
	}
}

// replyState is a notification action waiting for its text (sys.reply).
type replyState struct {
	n      *notif
	action string
}

// notifAction runs an app's own action from the notification centre, in
// the background; one that asks for text (a quick reply) first shows a
// system screen naming the app, with the system input method.
func (c *Core) notifAction(node, action string) {
	var i int
	if _, err := fmt.Sscanf(node, "n:%d", &i); err != nil || i >= len(c.notifs) {
		return
	}
	n := c.notifs[i]
	for _, a := range n.n.Actions {
		if a.ID != action {
			continue
		}
		if a.Input == "" {
			c.sendNotifAction(n, action, "")
			return
		}
		c.reply = &replyState{n: n, action: action}
		c.sys.stack = append(c.sys.stack, newView(ui.Screen{ID: "sys.reply", Title: n.app.M.Name + " · " + a.Label,
			Focus: "text", Right: "reply_cancel",
			Actions: []ui.Action{{ID: "reply_ok", Label: tr.T("Send")}, {ID: "reply_cancel", Label: tr.T("Cancel")}},
			Body: []ui.Node{
				{Kind: ui.KindText, Style: ui.StyleDim, Text: n.n.Title},
				{Kind: ui.KindText, Text: n.n.Body},
				{Kind: ui.KindInput, ID: "text", Placeholder: a.Input, Submit: "reply_ok"},
			}}))
		return
	}
}

// endReply sends the typed text ("" cancels) and goes back to the centre.
func (c *Core) endReply(text string) {
	r := c.reply
	c.reply = nil
	if top := c.sys.top(); top != nil && top.S.ID == "sys.reply" && len(c.sys.stack) > 1 {
		c.sys.stack = c.sys.stack[:len(c.sys.stack)-1]
	}
	if r == nil || text == "" {
		return
	}
	c.sendNotifAction(r.n, r.action, strings.TrimSpace(text))
	c.showToast(tr.T("Sent to “%s”", r.n.app.M.Name))
}

// sendNotifAction hands the action to the app, waking or starting it in
// the background (the user asked: no wake budget), and clears the
// notification.
func (c *Core) sendNotifAction(n *notif, action, text string) {
	for i, x := range c.notifs {
		if x == n {
			c.notifs = append(c.notifs[:i], c.notifs[i+1:]...)
			break
		}
	}
	if top := c.sys.top(); top != nil && top.S.ID == "sys.notifs" {
		top.replace(c.systemScreen("sys.notifs"))
	}
	if n.app == c.sys {
		c.sysNotifAction(n.n.ID, action)
		return
	}
	if c.smsNotifAction(n, action) || c.locNotifAction(n, action) {
		return
	}
	c.deliverBackground(n.app, ui.Msg{T: ui.MsgNotifAction, Value: n.n.ID, Action: action, Text: text}, pushWakeBudget, true,
		"notification action "+action)
}
