// Messages: the system's SMS app and, unless the user picked another, the
// default SMS app (32-messaging.md). Conversations (groups too) with their
// unread counts, a thread view with each message's delivery and a
// multimedia message's pictures, pictures to attach, a new-message form
// with the SMS part counter, search, deleting messages and conversations,
// the system's block list (numbers, prefixes, hidden and unknown callers,
// words) with the blocked folder, verification codes to copy, contact
// cards to add to Contacts, and the emergency alerts received; each
// conversation's draft, messages scheduled for later, and backups to and
// from Files. Messages live in phone-smsd; incoming ones become
// notifications the system posts for the default SMS app, and arrive here
// as "sms" events.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"slices"
	"sort"
	"strconv"
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
	// draftInit: the text the thread's input started with (its saved
	// draft); savedDraft what phone-smsd keeps as its draft. newTo and
	// newToInit: the new-message form's recipients, as typed and as given.
	draftInit, savedDraft string
	newTo, newToInit      string
	// intent: the message.compose request the open thread answers.
	intent string
	// attach: the pictures picked for the open thread's next message.
	attach []ui.MediaItem
	// blocks: the system's block list (the Blocked page).
	blocks ui.BlockList
	// later: what the Send later page schedules.
	later *later
}

// later is a message to schedule: to whom, what, and the page it came
// from (thread or new).
type later struct {
	to, text, from string
	media          []ui.MediaItem
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

func (m *messages) who(t ui.SMSThread) string { return firstNonEmpty(t.Name, peerText(t.Peer)) }

// peerText is a number, or a group's numbers, as the phone shows them.
func peerText(peer string) string {
	var out []string
	for _, p := range strings.FieldsFunc(peer, func(r rune) bool { return r == ',' || r == ';' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, ui.NumberText(p))
		}
	}
	return strings.Join(out, ", ")
}

// group: the peer is several numbers.
func group(peer string) bool { return strings.ContainsAny(peer, ",;") }

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func preview(s ui.SMS) string {
	text := body(s)
	switch {
	case s.Dir == "out" && s.State == ui.SMSFailed:
		return tr.T("Not sent: %s", text)
	case s.State == ui.SMSScheduled:
		return tr.T("Scheduled: %s", text)
	case s.Dir == "out":
		return tr.T("Me: %s", text)
	}
	return text
}

// threadPreview is a conversation's line in the list: its draft, else its
// last message.
func threadPreview(t ui.SMSThread) string {
	if t.Draft != "" {
		return tr.T("Draft: %s", strings.Join(strings.Fields(t.Draft), " "))
	}
	return preview(t.Last)
}

// threadKey is a conversation's key as phone-smsd makes it: a number's
// ui.NumberKey, a group's keys sorted and joined.
func threadKey(peer string) string {
	var keys []string
	for _, p := range strings.FieldsFunc(peer, func(r rune) bool { return r == ',' || r == ';' }) {
		if k := ui.NumberKey(strings.TrimSpace(p)); k != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// draftOf is the saved draft of the conversation with peer.
func (m *messages) draftOf(peer string) string {
	k := threadKey(peer)
	for _, t := range m.threads {
		if t.Thread == k || t.Peer == peer {
			return t.Draft
		}
	}
	return ""
}

// saveDraft hands phone-smsd what the open thread's input holds, when it
// changed.
func (m *messages) saveDraft() {
	if m.thread == "" || m.folder != "" || m.draft == m.savedDraft {
		return
	}
	m.savedDraft = m.draft
	m.app.Go(ui.CapSMSDraft, ui.SMSDraftArgs{Peer: m.thread, Text: m.draft}, nil, nil)
}

// zoneName is the phone's time zone (the system keeps the app's clock in
// it), "" when the app does not know its name.
func zoneName() string {
	if z := time.Local.String(); z != "Local" && z != "UTC" {
		return z
	}
	return ""
}

// dueText is when a scheduled message goes, on the phone's clock now and,
// when the phone has moved to another zone since, on the clock it was
// picked on.
func dueText(x ui.SMS) string {
	t := time.Unix(x.Due, 0)
	s := t.Format("2006-01-02 15:04")
	if x.Zone != "" && x.Zone != zoneName() {
		if loc, err := time.LoadLocation(x.Zone); err == nil {
			s += " (" + t.In(loc).Format("15:04") + " " + x.Zone + ")"
		}
	}
	return s
}

// body is a message's text in one line: a multimedia message's subject
// and text, or what it holds.
func body(s ui.SMS) string {
	text := strings.Join(strings.Fields(strings.TrimSpace(s.Subject+" "+s.Text)), " ")
	if s.Kind != ui.SMSKindMMS {
		return s.Text
	}
	switch s.MMS {
	case ui.MMSPending, ui.MMSDownloading:
		return tr.T("Multimedia message, downloading…")
	case ui.MMSFailed:
		return tr.T("Multimedia message, not downloaded")
	case ui.MMSExpired:
		return tr.T("Multimedia message, expired at the operator")
	}
	if text != "" {
		return text
	}
	for _, p := range s.Parts {
		if strings.HasPrefix(p.Type, "image/") {
			return tr.T("Picture")
		}
	}
	return tr.T("Multimedia message")
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
		{ID: "backup", Label: tr.T("Back up to Files"), Key: "f"},
		{ID: "restore", Label: tr.T("Restore from Files"), Key: "r"},
	}}
	for _, t := range m.threads {
		n := ui.Node{Kind: ui.KindItem, ID: "t:" + t.Peer, Icon: initial(m.who(t)), Title: m.who(t), Subtitle: threadPreview(t),
			Trailing: stamp(max(t.Last.At, t.DraftAt)), Actions: threadActions(m.who(t))}
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
	case ui.SMSScheduled:
		return tr.T("Scheduled · %s", dueText(x))
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

// shownPeer is the open thread's number as its messages carry it (the
// thread may have been opened by its key, the E.164 form).
func (m *messages) shownPeer() string {
	if group(m.thread) {
		return m.thread
	}
	for i := len(m.msgs) - 1; i >= 0; i-- {
		if m.msgs[i].Dir == "in" {
			return m.msgs[i].Peer
		}
	}
	if n := len(m.msgs); n > 0 {
		return m.msgs[n-1].Peer
	}
	return m.thread
}

// peerOf is the peer of the conversation with that key (a notification
// names its conversation by the key), as the conversation list has it.
func (m *messages) peerOf(key string) string {
	m.loadThreads()
	for _, t := range m.threads {
		if t.Thread == key {
			return t.Peer
		}
	}
	return key
}

func (m *messages) threadScreen() ui.Screen {
	who := firstNonEmpty(m.name, peerText(m.shownPeer()))
	s := ui.Screen{ID: "thread", Title: who, Bottom: true, Focus: "text", Actions: []ui.Action{
		{ID: "delete-thread", Label: tr.T("Delete conversation"), Group: ui.GroupDanger,
			Confirm: tr.T("Delete the whole conversation with %s?", who)},
	}}
	if m.folder == "" {
		s.Actions = append([]ui.Action{{ID: "attach", Label: tr.T("Attach a picture"), Key: "p"}, {ID: "later", Label: tr.T("Send later"), Key: "t"}}, s.Actions...)
	}
	if !group(m.thread) {
		s.Actions = append([]ui.Action{{ID: ui.ActionCall, Label: tr.T("Call"), Key: "l"}}, s.Actions...)
		if len(m.msgs) > 0 && m.isBlocked(m.msgs[0].Thread) {
			s.Actions = append(s.Actions, ui.Action{ID: "unblock", Label: tr.T("Unblock number")})
		} else {
			s.Actions = append(s.Actions, ui.Action{ID: "block", Label: tr.T("Block number"),
				Confirm: tr.T("Block %s? Its messages then go to Blocked, without a notification.", who)})
		}
		if m.name == "" {
			s.Actions = append(s.Actions, ui.Action{ID: "save-number", Label: tr.T("Save number")})
		}
	}
	// Scheduled messages come after the others, in the order they go.
	var sched []ui.SMS
	for _, x := range m.msgs {
		if x.State == ui.SMSScheduled {
			sched = append(sched, x)
			continue
		}
		s.Body = append(s.Body, m.bubble(x)...)
	}
	sort.SliceStable(sched, func(i, j int) bool { return sched[i].Due < sched[j].Due })
	for _, x := range sched {
		s.Body = append(s.Body, m.bubble(x)...)
	}
	if m.folder == "" {
		if len(m.attach) > 0 {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "attached", Icon: "+", Title: tr.T("%d pictures attached", len(m.attach)),
				Actions: []ui.Action{{ID: "unattach", Label: tr.T("Remove the pictures"), Primary: true}}})
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindInput, ID: "text", Placeholder: tr.T("Message text"), Submit: "send", Clear: true, Live: true, Value: m.draftInit})
		if len(m.attach) > 0 || group(m.thread) {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Multimedia message")))
		} else {
			s.Body = append(s.Body, counter(m.draft))
		}
	}
	return s
}

// bubble is a message in the thread: its text (in a group, who sent it),
// then its pictures and other parts.
func (m *messages) bubble(x ui.SMS) []ui.Node {
	sub := m.stateLine(x)
	if x.Dir == "in" && len(x.Members) > 1 {
		sub = firstNonEmpty(x.Name, ui.NumberText(x.Peer)) + " · " + sub
	}
	n := ui.Node{Kind: ui.KindItem, ID: "m:" + x.ID, Style: ui.StyleBubbleIn, Title: body(x), Subtitle: sub}
	if x.Dir == "out" {
		n.Style = ui.StyleBubbleOut
	}
	if x.State == ui.SMSFailed {
		n.Actions = append(n.Actions, ui.Action{ID: "resend", Label: tr.T("Resend"), Primary: true})
	}
	if x.State == ui.SMSScheduled {
		n.Actions = append(n.Actions, ui.Action{ID: "send-now", Label: tr.T("Send now"), Primary: true},
			ui.Action{ID: "delete", Label: tr.T("Cancel sending"), Key: "x", Group: ui.GroupDanger, Confirm: tr.T("Cancel this scheduled message?")})
	}
	if x.MMS == ui.MMSFailed || (x.MMS == ui.MMSPending && x.Folder != "") {
		n.Actions = append(n.Actions, ui.Action{ID: "download", Label: tr.T("Download"), Primary: true})
	}
	if x.Code != "" {
		n.Actions = append(n.Actions, ui.Action{ID: "copy-code", Label: tr.T("Copy code %s", x.Code), Key: "c", Primary: x.State != ui.SMSFailed})
	}
	if x.Flash {
		n.Badge = tr.T("Flash")
	}
	n.Actions = append(n.Actions, ui.Action{ID: "copy", Label: tr.T("Copy text"), Key: "y"},
		ui.Action{ID: "delete", Label: tr.T("Delete"), Key: "d", Group: ui.GroupDanger, Confirm: tr.T("Delete this message?")})
	out := []ui.Node{n}
	for i, p := range x.Parts {
		switch {
		case p.Media != "":
			out = append(out, ui.Node{Kind: ui.KindImage, ID: fmt.Sprintf("img:%s:%d", x.ID, i), Src: p.Media, Rows: 8, Label: tr.T("Picture · %s", p.Name)})
		case vcard(p) && p.Local:
			out = append(out, ui.Node{Kind: ui.KindItem, ID: fmt.Sprintf("card:%s:%d", x.ID, i), Icon: "+", Title: tr.T("Contact card: %s", firstNonEmpty(p.Name, p.Type)),
				Actions: []ui.Action{{ID: "add-card", Label: tr.T("Add to Contacts"), Primary: true}}})
		case p.Text == "":
			out = append(out, sdk.Styled(ui.StyleDim, tr.T("Attachment: %s (%s, %d KB)", firstNonEmpty(p.Name, p.Type), p.Type, (p.Size+1023)/1024)))
		}
	}
	return out
}

// vcard: a part is a contact card.
func vcard(p ui.SMSPart) bool {
	t := strings.ToLower(p.Type)
	return t == ui.SMSTypeVCard || t == ui.SMSTypeXVCard || strings.HasSuffix(strings.ToLower(p.Name), ".vcf")
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

// openThread shows the thread with show (the app's Push, Replace or Update)
// once it is loaded: the loading takes round trips to the SMS service, and
// the page under it must not show meanwhile.
func (m *messages) openThread(peer, folder string, show func(ui.Screen)) {
	m.thread, m.folder = peer, folder
	m.draft = ""
	if folder == "" {
		m.draft = m.draftOf(peer)
	}
	m.draftInit, m.savedDraft = m.draft, m.draft
	_ = m.app.Call(ui.CapSMSRead, ui.PeerArgs{Peer: peer}, nil)
	m.loadThread()
	show(m.threadScreen())
}

// send sends a message now or, with at (Unix seconds), at that time.
func (m *messages) send(to, text string, media []ui.MediaItem, at int64, done func(error)) bool {
	to, text = strings.TrimSpace(to), strings.TrimSpace(text)
	if to == "" || (text == "" && len(media) == 0) {
		m.app.Toast(tr.T("Enter a number and a message"))
		return false
	}
	args := ui.SMSSendArgs{To: to, Text: text}
	if at != 0 {
		args.At, args.Zone = at, zoneName()
	}
	for _, it := range media {
		args.Media = append(args.Media, it.ID)
	}
	m.app.Go(ui.CapSMSSend, args, nil, func(err error) {
		if err != nil {
			m.app.Toast(tr.T("Sending failed: %v", err))
		}
		if done != nil {
			done(err)
		}
	})
	return true
}

// openLater shows the Send later page for a message.
func (m *messages) openLater(l *later) {
	if strings.TrimSpace(l.to) == "" || (strings.TrimSpace(l.text) == "" && len(l.media) == 0) {
		m.app.Toast(tr.T("Enter a number and a message"))
		return
	}
	m.later = l
	t := sdk.Now().Add(time.Hour).Truncate(5 * time.Minute)
	s := ui.Screen{ID: "schedule", Title: tr.T("Send later"), Focus: "date", Right: "schedule-save",
		Actions: []ui.Action{{ID: "schedule-save", Label: tr.T("Schedule")}}, Body: []ui.Node{
			sdk.Styled(ui.StyleDim, tr.T("To %s: %s", peerText(l.to), strings.Join(strings.Fields(l.text), " "))),
			{Kind: ui.KindInput, ID: "date", Placeholder: tr.T("Date"), Keyboard: ui.KeyboardDate, Value: t.Format("2006-01-02")},
			{Kind: ui.KindInput, ID: "time", Placeholder: tr.T("Time"), Keyboard: ui.KeyboardTime, Value: t.Format("15:04")},
		}}
	if z := zoneName(); z != "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("On the clock of %s; it goes at that moment even if the phone is in another time zone by then", z)))
	}
	s.Body = append(s.Body, sdk.Button("schedule", tr.T("Schedule"), "schedule-save"))
	m.app.Push(s)
}

// schedule sends the Send later page's message at the time picked on it.
func (m *messages) schedule(fields map[string]string) {
	l := m.later
	if l == nil {
		return
	}
	at, err := time.ParseInLocation("2006-01-02 15:04", strings.TrimSpace(fields["date"])+" "+strings.TrimSpace(fields["time"]), time.Local)
	switch {
	case err != nil:
		m.app.Toast(tr.T("Pick a date and a time"))
		return
	case !at.After(sdk.Now()):
		m.app.Toast(tr.T("That time has passed"))
		return
	}
	m.send(l.to, l.text, l.media, at.Unix(), func(err error) {
		if err != nil {
			return
		}
		m.app.Toast(tr.T("Scheduled for %s", at.Format("2006-01-02 15:04")))
		m.later = nil
		m.app.Pop()
		if l.from == "new" {
			m.newBody = ""
			m.openThread(l.to, "", m.app.Replace)
			return
		}
		m.draft, m.draftInit, m.savedDraft, m.attach = "", "", "", nil
		m.loadThread()
		m.app.Replace(m.threadScreen())
	})
}

// pick lets the user choose a picture in the system's picker for the open
// thread's next message.
func (m *messages) pick() {
	if len(m.attach) >= ui.MaxMMSMedia {
		m.app.Toast(tr.T("At most %d pictures a message", ui.MaxMMSMedia))
		return
	}
	go func() {
		var it ui.MediaItem
		_, err := m.app.CallFile(ui.CapMediaPick, ui.MediaArgs{Kind: ui.MediaImage}, &it, func(f *os.File) (int64, error) { return 0, nil })
		m.app.Post(func() {
			if err != nil {
				if !ui.Cancelled(err) {
					m.app.Toast(tr.T("Failed: %v", err))
				}
				return
			}
			m.attach = append(m.attach, it)
			if m.thread != "" {
				m.app.Update(m.threadScreen())
			}
		})
	}()
}

func (m *messages) openNew(body string) {
	m.newInit, m.newBody = body, body
	m.newTo, m.newToInit = "", ""
	m.app.Push(m.newScreen())
}

// newScreen keeps the inputs' starting text as their values: an update
// with another value would replace what the user typed.
func (m *messages) newScreen() ui.Screen {
	return ui.Screen{ID: "new", Title: tr.T("New message"), Focus: "to", Actions: []ui.Action{
		{ID: "pick", Label: tr.T("Choose a contact"), Key: "c"},
		{ID: "later", Label: tr.T("Send later"), Key: "t"},
	}, Body: []ui.Node{
		{Kind: ui.KindInput, ID: "to", Placeholder: tr.T("Recipient numbers, commas between them"), Keyboard: "phone", Value: m.newToInit, Live: true},
		{Kind: ui.KindInput, ID: "body", Placeholder: tr.T("Message text"), Value: m.newInit, Live: true},
		counter(m.newBody),
		sdk.Button("send", tr.T("Send"), "send-new"),
	}}
}

// pickRecipient adds the number the user chooses in the system's contact
// picker to the new message's recipients.
func (m *messages) pickRecipient() {
	var c ui.Contact
	m.app.Go(ui.CapContactsPick, nil, &c, func(err error) {
		if err != nil {
			if !ui.Cancelled(err) {
				m.app.Toast(tr.T("Failed: %v", err))
			}
			return
		}
		to := strings.TrimRight(strings.TrimSpace(m.newTo), ",; ")
		if to != "" {
			to += ", "
		}
		m.newTo, m.newToInit = to+c.Number, to+c.Number
		m.app.Update(m.newScreen())
	})
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
	m.blocks = ui.BlockList{}
	_ = m.app.Call(ui.CapBlockList, nil, &m.blocks)
	m.blocked = m.blocks.Numbers
	onOff := map[bool]string{true: tr.T("On"), false: tr.T("Off")}
	s := ui.Screen{ID: "blocked", Title: tr.T("Blocked"), Actions: []ui.Action{
		{ID: "block-number", Label: tr.T("Block a number"), Key: "a"},
		{ID: "block-word", Label: tr.T("Block a word"), Key: "w"},
		{ID: "toggle-hidden", Label: tr.T("Block hidden numbers: %s", onOff[m.blocks.Hidden]), Key: "h"},
		{ID: "toggle-unknown", Label: tr.T("Block numbers not in Contacts: %s", onOff[m.blocks.Unknown]), Key: "u"},
	}}
	s.Body = append(s.Body, sdk.Section(tr.T("Blocked numbers (%d)", len(m.blocked))))
	for _, b := range m.blocked {
		title := ui.NumberText(b)
		if p, ok := strings.CutSuffix(b, "*"); ok {
			title = tr.T("Numbers starting with %s", p)
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "b:" + b, Title: title,
			Actions: []ui.Action{{ID: "unblock", Label: tr.T("Unblock"), Primary: true}}})
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Blocked words (%d)", len(m.blocks.Words))))
	for _, w := range m.blocks.Words {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "w:" + w, Title: w, Subtitle: tr.T("Messages holding it go here"),
			Actions: []ui.Action{{ID: "unblock-word", Label: tr.T("Unblock"), Primary: true}}})
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
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Block hidden numbers: %s", onOff[m.blocks.Hidden])+" · "+
		tr.T("Block numbers not in Contacts: %s", onOff[m.blocks.Unknown])))
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
		{Kind: ui.KindInput, ID: "number", Placeholder: tr.T("A number, or a prefix ending in *"), Submit: "block-save"},
		sdk.Button("save", tr.T("Block"), "block-save"),
	}}
}

func (m *messages) blockWordScreen() ui.Screen {
	return ui.Screen{ID: "block-word", Title: tr.T("Block a word"), Focus: "word", Body: []ui.Node{
		{Kind: ui.KindInput, ID: "word", Placeholder: tr.T("A word or words; any message holding it goes to Blocked"), Submit: "word-save"},
		sdk.Button("save", tr.T("Block"), "word-save"),
	}}
}

func (m *messages) blockWord(word string, on bool) {
	if err := m.app.Call(ui.CapBlockSet, ui.BlockSetArgs{Word: word, On: on}, &m.blocks); err != nil {
		m.app.Toast(tr.T("Failed: %v", err))
		return
	}
	if on {
		m.app.Toast(tr.T("Messages with “%s” now go to Blocked", word))
	} else {
		m.app.Toast(tr.T("“%s” unblocked", word))
	}
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
		// Another app (Contacts) asks to write to a number: its
		// conversation, answered when the user leaves it.
		var in ui.Intent
		var to ui.Contact
		if raw, ok := strings.CutPrefix(link, ui.IntentPrefix); ok && json.Unmarshal([]byte(raw), &in) == nil &&
			in.Action == ui.IntentMessageCompose && json.Unmarshal([]byte(in.Payload), &to) == nil && strings.TrimSpace(to.Number) != "" {
			show := app.Push
			if m.thread != "" {
				show = app.Replace
			}
			if m.intent != "" {
				app.Go(ui.CapIntentReply, ui.IntentReplyArgs{ID: m.intent, OK: true}, nil, nil)
			}
			m.openThread(strings.TrimSpace(to.Number), "", show)
			m.intent = in.ID
			if m.name == "" && to.Name != "" {
				m.name = to.Name
				app.Update(m.threadScreen())
			}
		}
	}
	app.OnStart = func(arg string) {
		m.loadThreads()
		loaded = true
		app.Reset(m.listScreen())
		if pending != "" {
			m.openThread(m.peerOf(pending), "", app.Push)
			pending = ""
		}
		compose(arg)
	}
	app.OnNotif = func(id, action string) {
		key, ok := strings.CutPrefix(id, "sms:")
		if !ok {
			return
		}
		if !loaded {
			pending = key
			return
		}
		peer := m.peerOf(key)
		show := app.Update
		if m.thread == "" {
			show = app.Push
		}
		m.openThread(peer, "", show)
	}
	app.OnLifecycle = func(state string) {
		if state == ui.StateBackground {
			m.saveDraft()
		}
	}
	app.OnPopped = func(screen string) {
		switch screen {
		case "new":
			if to, text := strings.TrimSpace(m.newTo), strings.TrimSpace(m.newBody); to != "" && text != "" {
				app.Go(ui.CapSMSDraft, ui.SMSDraftArgs{Peer: to, Text: m.newBody}, nil, func(error) { m.refresh() })
			}
			m.newTo, m.newBody = "", ""
		case "schedule":
			m.later = nil
		case "thread":
			m.saveDraft()
			m.thread, m.folder, m.attach = "", "", nil
			if m.intent != "" {
				app.Go(ui.CapIntentReply, ui.IntentReplyArgs{ID: m.intent, OK: true}, nil, nil)
				m.intent = ""
			}
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
		case e.Action == "changed" && e.Node == "to":
			m.newTo = e.Value
		case e.Action == "pick":
			m.pickRecipient()
		case e.Action == "later" && e.Screen == "new":
			m.newTo, m.newBody = e.Fields["to"], e.Fields["body"]
			m.openLater(&later{to: m.newTo, text: m.newBody, from: "new"})
		case e.Action == "later" && e.Screen == "thread":
			m.draft = e.Fields["text"]
			m.openLater(&later{to: m.thread, text: m.draft, media: m.attach, from: "thread"})
		case e.Action == "schedule-save":
			m.schedule(e.Fields)
		case e.Action == "backup":
			var res ui.SMSFile
			app.Go(ui.CapSMSBackup, nil, &res, func(err error) {
				if err != nil && !ui.Cancelled(err) {
					app.Toast(err.Error())
				}
			})
		case e.Action == "restore":
			var res ui.SMSFile
			app.Go(ui.CapSMSRestore, nil, &res, func(err error) {
				if err != nil && !ui.Cancelled(err) {
					app.Toast(err.Error())
				}
				m.refresh()
			})
		case e.Action == "add-card":
			var id string
			var i int
			if k := strings.LastIndexByte(e.Node, ':'); strings.HasPrefix(e.Node, "card:") && k > 5 {
				id = e.Node[5:k]
				i, _ = strconv.Atoi(e.Node[k+1:])
			}
			var res ui.ContactsFile
			app.Go(ui.CapSMSVCard, ui.SMSPartArgs{ID: id, Index: i}, &res, func(err error) {
				if err != nil && !ui.Cancelled(err) {
					app.Toast(err.Error())
				}
			})
		case e.Action == "send-now":
			if x, ok := m.message(e.Node); ok {
				app.Go(ui.CapSMSSend, ui.SMSSendArgs{ID: x.ID}, nil, func(err error) {
					if err != nil {
						app.Toast(tr.T("Sending failed: %v", err))
					}
				})
			}
		case e.Action == "block-word":
			app.Push(m.blockWordScreen())
		case e.Action == "word-save":
			if w := strings.TrimSpace(e.Fields["word"]); w != "" {
				m.blockWord(w, true)
				app.Pop()
				m.refresh()
			}
		case e.Action == "unblock-word":
			m.blockWord(strings.TrimPrefix(e.Node, "w:"), false)
			app.Update(m.blockedScreen())
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
			if m.send(to, e.Fields["body"], nil, 0, nil) {
				m.newBody = ""
				m.openThread(to, "", app.Replace)
			}
		case e.Action == ui.ActionOpen && strings.HasPrefix(e.Node, "t:"):
			m.openThread(node, "", app.Push)
		case e.Action == ui.ActionOpen && strings.HasPrefix(e.Node, "bt:"):
			m.openThread(strings.TrimPrefix(e.Node, "bt:"), ui.SMSFolderBlocked, app.Push)
		case e.Screen == "thread" && e.Action == "send":
			if m.send(m.thread, e.Fields["text"], m.attach, 0, nil) {
				m.draft, m.draftInit, m.savedDraft, m.attach = "", "", "", nil
				app.Update(m.threadScreen())
			}
		case e.Action == "attach":
			m.pick()
		case e.Action == "unattach":
			m.attach = nil
			app.Update(m.threadScreen())
		case e.Action == "download":
			if x, ok := m.message(e.Node); ok {
				app.Go(ui.CapSMSDownload, ui.SMSIDArgs{ID: x.ID}, nil, func(err error) {
					if err != nil {
						app.Toast(tr.T("Failed: %v", err))
					}
				})
			}
		case e.Action == "toggle-hidden" || e.Action == "toggle-unknown":
			on := !m.blocks.Hidden
			args := ui.BlockSetArgs{Hidden: &on}
			if e.Action == "toggle-unknown" {
				on = !m.blocks.Unknown
				args = ui.BlockSetArgs{Unknown: &on}
			}
			if err := app.Call(ui.CapBlockSet, args, &m.blocks); err != nil {
				app.Toast(tr.T("Failed: %v", err))
			}
			app.Update(m.blockedScreen())
		case e.Screen == "thread" && e.Action == ui.ActionCall:
			_ = app.Call(ui.CapTelDial, ui.DialArgs{Number: m.thread}, nil)
		case e.Screen == "thread" && e.Action == "save-number":
			payload, _ := json.Marshal(ui.Contact{Number: m.thread})
			var res ui.IntentRes
			app.Go(ui.CapIntentInvoke, ui.IntentArgs{Action: ui.IntentContactSave, Payload: string(payload), What: tr.T("save %s as a contact", m.thread)}, &res, func(err error) {
				if err != nil && strings.HasSuffix(err.Error(), ui.ErrNoHandler) {
					app.Toast(tr.T("No app saves contacts"))
				}
				if m.thread != "" {
					m.loadThread()
					app.Update(m.threadScreen())
				}
			})
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
				m.draft = ""
				app.Pop()
			}
		case e.Action == "delete-thread":
			peer := strings.TrimPrefix(node, "bt:")
			if m.remove(ui.SMSDeleteArgs{Peer: peer}) {
				if m.draftOf(peer) != "" {
					_ = app.Call(ui.CapSMSDraft, ui.SMSDraftArgs{Peer: peer}, nil)
				}
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
