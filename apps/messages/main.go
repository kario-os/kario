// Messages: the system's SMS app and the default SMS app (32-messaging.md).
// Conversations with their unread counts, a thread view with each
// message's delivery, a new-message form with the SMS part counter,
// search, deleting messages and conversations, the block list with the
// blocked folder, verification codes to copy, and the emergency alerts
// received. Messages live in phone-smsd; incoming ones become
// notifications the system posts for this app, and arrive here as "sms"
// events.
package main

import (
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

type messages struct {
	app     *sdk.App
	threads []ui.SMSThread
	msgs    []ui.SMS // the open thread's
	thread  string   // the open thread's peer, "" on the list
	folder  string   // the open thread's folder ("" or blocked)
	name    string   // the open thread's contact
	blocked []string
	query   string
	// blockedOpen: the Blocked page is on the stack.
	blockedOpen bool
	// What the open compose inputs hold, for the part counter, and the
	// text the new-message form started with.
	draft, newBody, newInit string
}

func stamp(at int64) string {
	t := time.Unix(at, 0)
	if time.Since(t) < 24*time.Hour {
		return t.Format("15:04")
	}
	return t.Format("01-02")
}

func initial(s string) string {
	if r, _ := utf8.DecodeRuneInString(s); r != utf8.RuneError {
		return string(r)
	}
	return "?"
}

// counter is the compose field's part counter: characters left in the
// current SMS part and how many parts the text takes (GSM 7-bit 160 a part,
// 153 when split; UCS-2 for Chinese and emoji, 70 and 67).
func counter(text string) ui.Node {
	parts, left, _ := ui.SMSSegments(text)
	n := sdk.Styled(ui.StyleDim, "")
	n.ID = "counter"
	if parts > 0 {
		n.Text = tr.T("%d left · %d SMS", left, parts)
	}
	return n
}

func (m *messages) loadThreads() {
	m.threads = nil
	_ = m.app.Call(ui.CapSMSThreads, ui.SMSListArgs{}, &m.threads)
}

func (m *messages) isBlocked(number string) bool {
	for _, b := range m.blocked {
		if b == number {
			return true
		}
	}
	return false
}

func (m *messages) who(t ui.SMSThread) string { return firstNonEmpty(t.Name, t.Peer) }

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func preview(s ui.SMS) string {
	switch {
	case s.Dir == "out" && s.State == ui.SMSFailed:
		return tr.T("Not sent: %s", s.Text)
	case s.Dir == "out":
		return tr.T("Me: %s", s.Text)
	}
	return s.Text
}

func threadActions(who string) []ui.Action {
	return []ui.Action{
		{ID: ui.ActionOpen, Label: tr.T("Open"), Primary: true},
		{ID: "delete-thread", Label: tr.T("Delete conversation"), Key: "d", Group: ui.GroupDanger,
			Confirm: tr.T("Delete the whole conversation with %s?", who)},
		{ID: "block", Label: tr.T("Block number"), Key: "b",
			Confirm: tr.T("Block %s? Its messages then go to Blocked, without a notification.", who)},
	}
}

func (m *messages) listScreen() ui.Screen {
	s := ui.Screen{ID: "list", Title: tr.T("Messages"), Right: "new", Actions: []ui.Action{
		{ID: "new", Label: tr.T("New message"), Key: "n"},
		{ID: "search", Label: tr.T("Search"), Key: "s"},
		{ID: "blocked", Label: tr.T("Blocked"), Key: "k"},
		{ID: "alerts", Label: tr.T("Emergency alerts"), Key: "e"},
	}}
	for _, t := range m.threads {
		n := ui.Node{Kind: ui.KindItem, ID: "t:" + t.Peer, Icon: initial(m.who(t)), Title: m.who(t), Subtitle: preview(t.Last),
			Trailing: stamp(t.Last.At), Actions: threadActions(m.who(t))}
		if t.Unread > 0 {
			n.Badge = fmt.Sprint(t.Unread)
		}
		s.Body = append(s.Body, n)
	}
	if len(m.threads) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No messages. Press the right soft key to write one.")))
	}
	return s
}

func (m *messages) stateLine(x ui.SMS) string {
	at := stamp(x.At)
	switch x.State {
	case ui.SMSSending:
		return tr.T("Sending…")
	case ui.SMSSent:
		return tr.T("Sent · %s", at)
	case ui.SMSDelivered:
		return tr.T("Delivered · %s", at)
	case ui.SMSFailed:
		return tr.T("Not sent · %s", at)
	}
	return at
}

func (m *messages) threadScreen() ui.Screen {
	who := firstNonEmpty(m.name, m.thread)
	s := ui.Screen{ID: "thread", Title: who, Bottom: true, Focus: "text", Actions: []ui.Action{
		{ID: ui.ActionCall, Label: tr.T("Call"), Key: "l"},
		{ID: "delete-thread", Label: tr.T("Delete conversation"), Group: ui.GroupDanger,
			Confirm: tr.T("Delete the whole conversation with %s?", who)},
	}}
	if len(m.msgs) > 0 && m.isBlocked(m.msgs[0].Thread) {
		s.Actions = append(s.Actions, ui.Action{ID: "unblock", Label: tr.T("Unblock number")})
	} else {
		s.Actions = append(s.Actions, ui.Action{ID: "block", Label: tr.T("Block number"),
			Confirm: tr.T("Block %s? Its messages then go to Blocked, without a notification.", who)})
	}
	for _, x := range m.msgs {
		n := ui.Node{Kind: ui.KindItem, ID: "m:" + x.ID, Style: ui.StyleBubbleIn, Title: x.Text, Subtitle: m.stateLine(x)}
		if x.Dir == "out" {
			n.Style = ui.StyleBubbleOut
		}
		if x.State == ui.SMSFailed {
			n.Actions = append(n.Actions, ui.Action{ID: "resend", Label: tr.T("Resend"), Primary: true})
		}
		if x.Code != "" {
			n.Actions = append(n.Actions, ui.Action{ID: "copy-code", Label: tr.T("Copy code %s", x.Code), Key: "c", Primary: x.State != ui.SMSFailed})
		}
		n.Actions = append(n.Actions, ui.Action{ID: "copy", Label: tr.T("Copy text"), Key: "y"},
			ui.Action{ID: "delete", Label: tr.T("Delete"), Key: "d", Group: ui.GroupDanger, Confirm: tr.T("Delete this message?")})
		s.Body = append(s.Body, n)
	}
	if m.folder == "" {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindInput, ID: "text", Placeholder: tr.T("Message text"), Submit: "send", Clear: true, Live: true},
			counter(m.draft))
	}
	return s
}

func (m *messages) loadThread() {
	m.msgs = nil
	_ = m.app.Call(ui.CapSMSList, ui.SMSListArgs{Peer: m.thread, Folder: m.folder}, &m.msgs)
	m.name = ""
	if len(m.msgs) > 0 {
		m.name = m.msgs[len(m.msgs)-1].Name
	}
	m.loadBlocked()
}

func (m *messages) loadBlocked() {
	m.blocked = nil
	_ = m.app.Call(ui.CapSMSBlocked, nil, &m.blocked)
}

func (m *messages) openThread(peer, folder string, push bool) {
	m.thread, m.folder, m.draft = peer, folder, ""
	_ = m.app.Call(ui.CapSMSRead, ui.PeerArgs{Peer: peer}, nil)
	m.loadThread()
	if push {
		m.app.Push(m.threadScreen())
	} else {
		m.app.Update(m.threadScreen())
	}
}

func (m *messages) send(to, text string) bool {
	to, text = strings.TrimSpace(to), strings.TrimSpace(text)
	if to == "" || text == "" {
		m.app.Toast(tr.T("Enter a number and a message"))
		return false
	}
	m.app.Go(ui.CapSMSSend, ui.SMSSendArgs{To: to, Text: text}, nil, func(err error) {
		if err != nil {
			m.app.Toast(tr.T("Sending failed: %v", err))
		}
	})
	return true
}

func (m *messages) openNew(body string) {
	m.newInit, m.newBody = body, body
	m.app.Push(m.newScreen())
}

// newScreen keeps the body's starting text as its value: an update with
// another value would replace what the user typed.
func (m *messages) newScreen() ui.Screen {
	return ui.Screen{ID: "new", Title: tr.T("New message"), Focus: "to", Body: []ui.Node{
		{Kind: ui.KindInput, ID: "to", Placeholder: tr.T("Recipient number"), Keyboard: "phone"},
		{Kind: ui.KindInput, ID: "body", Placeholder: tr.T("Message text"), Value: m.newInit, Live: true},
		counter(m.newBody),
		sdk.Button("send", tr.T("Send"), "send-new"),
	}}
}

func (m *messages) searchScreen() ui.Screen {
	s := ui.Screen{ID: "search", Title: tr.T("Search"), Focus: "q", Body: []ui.Node{
		{Kind: ui.KindInput, ID: "q", Placeholder: tr.T("Words, a number or a name"), Live: true},
	}}
	q := strings.ToLower(strings.TrimSpace(m.query))
	if q == "" {
		return s
	}
	var hits []ui.Node
	for _, t := range m.threads {
		if strings.Contains(strings.ToLower(t.Name), q) {
			hits = append(hits, ui.Node{Kind: ui.KindItem, ID: "t:" + t.Peer, Icon: initial(m.who(t)), Title: m.who(t),
				Subtitle: preview(t.Last), Trailing: stamp(t.Last.At), Actions: []ui.Action{{ID: ui.ActionOpen, Label: tr.T("Open"), Primary: true}}})
		}
	}
	var found []ui.SMS
	_ = m.app.Call(ui.CapSMSList, ui.SMSListArgs{Query: q}, &found)
	for i := len(found) - 1; i >= 0; i-- {
		x := found[i]
		hits = append(hits, ui.Node{Kind: ui.KindItem, ID: "t:" + x.Peer, Icon: initial(firstNonEmpty(x.Name, x.Peer)),
			Title: firstNonEmpty(x.Name, x.Peer), Subtitle: preview(x), Trailing: stamp(x.At),
			Actions: []ui.Action{{ID: ui.ActionOpen, Label: tr.T("Open"), Primary: true}}})
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Found (%d)", len(hits))))
	s.Body = append(s.Body, hits...)
	return s
}

func (m *messages) blockedScreen() ui.Screen {
	s := ui.Screen{ID: "blocked", Title: tr.T("Blocked"), Actions: []ui.Action{{ID: "block-number", Label: tr.T("Block a number"), Key: "a"}}}
	m.loadBlocked()
	s.Body = append(s.Body, sdk.Section(tr.T("Blocked numbers (%d)", len(m.blocked))))
	for _, b := range m.blocked {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "b:" + b, Title: b,
			Actions: []ui.Action{{ID: "unblock", Label: tr.T("Unblock"), Primary: true}}})
	}
	var threads []ui.SMSThread
	_ = m.app.Call(ui.CapSMSThreads, ui.SMSListArgs{Folder: ui.SMSFolderBlocked}, &threads)
	s.Body = append(s.Body, sdk.Section(tr.T("Messages from blocked numbers")))
	for _, t := range threads {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "bt:" + t.Peer, Icon: initial(m.who(t)), Title: m.who(t),
			Subtitle: t.Last.Text, Trailing: stamp(t.Last.At), Actions: []ui.Action{
				{ID: ui.ActionOpen, Label: tr.T("Open"), Primary: true},
				{ID: "delete-thread", Label: tr.T("Delete"), Key: "d", Group: ui.GroupDanger, Confirm: tr.T("Delete the whole conversation with %s?", m.who(t))},
			}})
	}
	if len(threads) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("None")))
	}
	return s
}

// levelName is an emergency alert's level, as the system names it.
func levelName(level string) string {
	switch level {
	case ui.AlertPresidential:
		return tr.T("National alert")
	case ui.AlertExtreme:
		return tr.T("Extreme alert")
	case ui.AlertSevere:
		return tr.T("Severe alert")
	case ui.AlertAmber:
		return tr.T("AMBER alert")
	}
	return tr.T("Test alert")
}

func (m *messages) alertsScreen() ui.Screen {
	s := ui.Screen{ID: "alerts", Title: tr.T("Emergency alerts")}
	var alerts []ui.SMSAlert
	_ = m.app.Call(ui.CapSMSAlerts, nil, &alerts)
	for _, a := range alerts {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "a:" + a.ID, Title: levelName(a.Level), Subtitle: a.Text,
			Trailing: time.Unix(a.At, 0).Format("01-02 15:04"), Actions: []ui.Action{{ID: "alert", Label: tr.T("Open"), Primary: true}}})
	}
	if len(alerts) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No emergency alerts received")))
	}
	return s
}

func (m *messages) alertScreen(id string) ui.Screen {
	var alerts []ui.SMSAlert
	_ = m.app.Call(ui.CapSMSAlerts, nil, &alerts)
	for _, a := range alerts {
		if a.ID == id {
			return ui.Screen{ID: "alert", Title: levelName(a.Level), Body: []ui.Node{
				sdk.Styled(ui.StyleDim, time.Unix(a.At, 0).Format("2006-01-02 15:04")),
				sdk.Text(a.Text),
				sdk.Styled(ui.StyleDim, tr.T("Channel %d", a.Channel)),
			}}
		}
	}
	return m.alertsScreen()
}

func (m *messages) blockScreen() ui.Screen {
	return ui.Screen{ID: "block-number", Title: tr.T("Block a number"), Focus: "number", Body: []ui.Node{
		{Kind: ui.KindInput, ID: "number", Placeholder: tr.T("Number"), Keyboard: "phone", Submit: "block-save"},
		sdk.Button("save", tr.T("Block"), "block-save"),
	}}
}

func (m *messages) block(number string, on bool) {
	var list []string
	if err := m.app.Call(ui.CapSMSBlock, ui.SMSBlockArgs{Number: number, On: on}, &list); err != nil {
		m.app.Toast(tr.T("Failed: %v", err))
		return
	}
	m.blocked = list
	if on {
		m.app.Toast(tr.T("%s blocked", number))
	} else {
		m.app.Toast(tr.T("%s unblocked", number))
	}
}

func (m *messages) remove(args ui.SMSDeleteArgs) bool {
	if err := m.app.Call(ui.CapSMSDelete, args, nil); err != nil {
		m.app.Toast(tr.T("Failed: %v", err))
		return false
	}
	m.app.Toast(tr.T("Deleted"))
	return true
}

func (m *messages) message(node string) (ui.SMS, bool) {
	for _, x := range m.msgs {
		if "m:"+x.ID == node {
			return x, true
		}
	}
	return ui.SMS{}, false
}

func (m *messages) copy(text string) {
	if err := m.app.Call(ui.CapClipboardSet, ui.ClipArgs{Text: text}, nil); err != nil {
		m.app.Toast(tr.T("Failed: %v", err))
	}
}

// refresh redraws what is open after a change: an update only reaches a
// screen the app has on its stack.
func (m *messages) refresh() {
	m.loadThreads()
	m.app.Update(m.listScreen())
	if m.thread != "" {
		m.loadThread()
		m.app.Update(m.threadScreen())
	}
	if m.blockedOpen {
		m.app.Update(m.blockedScreen())
	}
	if m.query != "" {
		m.app.Update(m.searchScreen())
	}
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	m := &messages{app: app}
	loaded, pending := false, ""
	// A "share:text:<text>" deep link from the share sheet opens a new
	// message with the text filled in.
	compose := func(link string) {
		if text, ok := strings.CutPrefix(link, "share:text:"); ok {
			m.openNew(text)
		}
	}
	app.OnStart = func(arg string) {
		m.loadThreads()
		loaded = true
		app.Reset(m.listScreen())
		if pending != "" {
			m.openThread(pending, "", true)
			pending = ""
		}
		compose(arg)
	}
	app.OnNotif = func(id, action string) {
		peer, ok := strings.CutPrefix(id, "sms:")
		if !ok {
			return
		}
		if !loaded {
			pending = peer
			return
		}
		m.openThread(peer, "", m.thread == "")
	}
	// "popped" arrives after whatever the app did next: closing the new-message
	// form is followed by opening the thread, so only a closed thread resets.
	app.OnPopped = func(screen string) {
		switch screen {
		case "thread":
			m.thread, m.folder = "", ""
			m.refresh()
		case "search":
			m.query = ""
		case "blocked":
			m.blockedOpen = false
		}
	}
	app.OnEvent = func(name, value string) {
		if name == ui.EventOpen {
			compose(value)
		}
		if name != ui.EventSMS {
			return
		}
		if m.thread != "" {
			_ = m.app.Call(ui.CapSMSRead, ui.PeerArgs{Peer: m.thread}, nil)
		}
		m.refresh()
	}
	app.OnAction = func(e sdk.ActionEvent) {
		node, _ := strings.CutPrefix(e.Node, "t:")
		switch {
		case e.Action == "changed" && e.Node == "text":
			m.draft = e.Value
			app.Update(m.threadScreen())
		case e.Action == "changed" && e.Node == "body":
			m.newBody = e.Value
			app.Update(m.newScreen())
		case e.Action == "changed" && e.Node == "q":
			m.query = e.Value
			app.Update(m.searchScreen())
		case e.Action == "new":
			m.openNew("")
		case e.Action == "search":
			m.query = ""
			app.Push(m.searchScreen())
		case e.Action == "blocked":
			m.blockedOpen = true
			app.Push(m.blockedScreen())
		case e.Action == "alerts":
			app.Push(m.alertsScreen())
		case e.Action == "alert":
			app.Push(m.alertScreen(strings.TrimPrefix(e.Node, "a:")))
		case e.Action == "send-new":
			to := strings.TrimSpace(e.Fields["to"])
			if m.send(to, e.Fields["body"]) {
				app.Pop()
				m.openThread(to, "", true)
			}
		case e.Action == ui.ActionOpen && strings.HasPrefix(e.Node, "t:"):
			m.openThread(node, "", true)
		case e.Action == ui.ActionOpen && strings.HasPrefix(e.Node, "bt:"):
			m.openThread(strings.TrimPrefix(e.Node, "bt:"), ui.SMSFolderBlocked, true)
		case e.Screen == "thread" && e.Action == "send":
			if m.send(m.thread, e.Fields["text"]) {
				m.draft = ""
			}
		case e.Screen == "thread" && e.Action == ui.ActionCall:
			_ = app.Call(ui.CapTelDial, ui.DialArgs{Number: m.thread}, nil)
		case e.Action == "resend":
			if x, ok := m.message(e.Node); ok {
				app.Go(ui.CapSMSSend, ui.SMSSendArgs{ID: x.ID}, nil, func(err error) {
					if err != nil {
						app.Toast(tr.T("Sending failed: %v", err))
					}
				})
				app.Toast(tr.T("Sent again"))
			}
		case e.Action == "copy-code":
			if x, ok := m.message(e.Node); ok {
				m.copy(x.Code)
			}
		case e.Action == "copy":
			if x, ok := m.message(e.Node); ok {
				m.copy(x.Text)
			}
		case e.Action == "delete":
			if x, ok := m.message(e.Node); ok && m.remove(ui.SMSDeleteArgs{IDs: []string{x.ID}}) {
				m.refresh()
			}
		case e.Action == "delete-thread" && e.Screen == "thread":
			if m.remove(ui.SMSDeleteArgs{Peer: m.thread}) {
				app.Pop()
			}
		case e.Action == "delete-thread":
			if m.remove(ui.SMSDeleteArgs{Peer: strings.TrimPrefix(node, "bt:")}) {
				m.refresh()
			}
		case e.Action == "block" || e.Action == "unblock":
			number := m.thread
			if e.Screen != "thread" {
				number = strings.TrimPrefix(node, "b:")
			}
			m.block(number, e.Action == "block")
			m.refresh()
		case e.Action == "block-number":
			app.Push(m.blockScreen())
		case e.Action == "block-save":
			if n := strings.TrimSpace(e.Fields["number"]); n != "" {
				m.block(n, true)
				app.Pop()
				m.refresh()
			}
		}
	}
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}
