// Chat is a WeChat-style sample app: conversation list, chat with a message
// context menu (the long-press menu), forwarding, photos, and video calls in
// both directions through the platform call and media capabilities.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/apps/chat/bot"
	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/audio/voice"
	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/web/reader"
)

type message struct {
	id    int
	from  string
	text  string
	img   string
	voice string // Ogg Opus in the app's data dir
	at    time.Time
}

// recorder is a voice message being recorded: samples arrive in the shared
// ring the system handed over; the app copies them out every 100 ms.
type recorder struct {
	cv          *convo
	s           *sdk.AudioStream
	pcm         []int16
	interrupted bool
	done        chan struct{}
}

const maxVoice = 60 * audio.Rate

type convo struct {
	id, name, icon string
	msgs           []*message
	unread         int
	pinned         bool
}

type call struct {
	id, peer      string
	local, remote string
	mic, cam      bool
	swapped       bool
	started       time.Time
	// The call's sound: the peer's voice played, the microphone read; both
	// are "call" streams (the system makes an app's streams call audio
	// while it is in a call).
	speaker, micIn *sdk.AudioStream
	stop           chan struct{}
}

type chat struct {
	share   *message // content from the share sheet, until a conversation is picked
	app     *sdk.App
	convos  []*convo
	open    string
	fg      bool
	nextID  int
	call    *call
	ringing map[string]string
	rec     *recorder
	srv     string // the chat server, "" to run the bot locally
	http    *http.Client
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	c := &chat{app: app, ringing: map[string]string{}, srv: os.Getenv(ui.EnvChatServer),
		http: &http.Client{Timeout: 10 * time.Second}}
	c.seed()
	app.OnStart = func(arg string) {
		app.Reset(c.listScreen())
		var saved struct {
			Open string `json:"open"`
		}
		if app.Restore(&saved) {
			if cv := c.find(saved.Open); cv != nil {
				c.open = cv.id
				app.Push(c.chatScreen(cv))
			}
		}
		c.shared(arg)
		c.register()
	}
	app.OnSave = func() any { return map[string]string{"open": c.open} }
	app.OnAction = c.action
	app.OnPopped = c.popped
	app.OnLifecycle = func(s string) { c.fg = s == ui.StateForeground }
	app.OnNotif = c.notifAction
	app.OnNotifText = c.notifReply
	app.OnEvent = func(name, value string) {
		switch {
		case name == ui.EventCallResume && c.call != nil && c.call.id == value:
			app.Push(c.callScreen())
		case name == "push":
			c.onPush(value)
		case name == ui.EventPushKeyChanged:
			c.register()
		case name == "open":
			c.shared(value)
		case name == "audio":
			var st struct{ ID, State string }
			if json.Unmarshal([]byte(value), &st) == nil && c.rec != nil && c.rec.s.ID == st.ID && st.State == "ended" {
				// The system stopped the recording (the app left the foreground).
				c.pumpRec()
				c.rec.interrupted = true
				c.app.Update(c.recScreen())
			}
		}
	}
	go func() {
		for range time.Tick(time.Second) {
			app.Post(c.tick)
		}
	}()
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}

func (c *chat) seed() {
	now := time.Now()
	add := func(cv *convo, from, text, img string, ago time.Duration) {
		c.nextID++
		cv.msgs = append(cv.msgs, &message{id: c.nextID, from: from, text: text, img: img, at: now.Add(-ago)})
	}
	zs := &convo{id: "zhangsan", name: tr.T("Zhang San"), icon: tr.T("Z")}
	add(zs, "zhangsan", tr.T("Hiking this weekend?"), "", 50*time.Minute)
	add(zs, "me", tr.T("Sure, what time do we leave?"), "", 45*time.Minute)
	add(zs, "zhangsan", tr.T("Eight in the morning. Look at this photo, from last time"), "gen:mountain", 40*time.Minute)
	ls := &convo{id: "lisi", name: tr.T("Li Si"), icon: tr.T("L"), unread: 2}
	add(ls, "lisi", tr.T("I emailed you the contract"), "", 3*time.Hour)
	add(ls, "lisi", tr.T("Remember to reply before you leave work today"), "", 2*time.Hour)
	grp := &convo{id: "team", name: tr.T("Product team (5)"), icon: tr.T("G"), unread: 5}
	add(grp, "wangwu", tr.T("Wang Wu: the new version ships tonight"), "", 20*time.Minute)
	fh := &convo{id: "filehelper", name: tr.T("File Transfer"), icon: tr.T("F")}
	add(fh, "me", tr.T("invoice.pdf"), "", 26*time.Hour)
	c.convos = []*convo{zs, ls, grp, fh}
}

func (c *chat) find(id string) *convo {
	for _, cv := range c.convos {
		if cv.id == id {
			return cv
		}
	}
	return nil
}

func (cv *convo) last() *message {
	if len(cv.msgs) == 0 {
		return &message{}
	}
	return cv.msgs[len(cv.msgs)-1]
}

func preview(m *message) string {
	if m.img != "" && m.text == "" {
		return tr.T("[Photo]")
	}
	return m.text
}

func (c *chat) listScreen() ui.Screen {
	sorted := append([]*convo{}, c.convos...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].pinned != sorted[j].pinned {
			return sorted[i].pinned
		}
		return sorted[i].last().at.After(sorted[j].last().at)
	})
	s := ui.Screen{ID: "list", Title: tr.T("Hongyan"), Right: "new", Actions: []ui.Action{
		{ID: "new", Label: tr.T("New chat"), Key: "n"},
		{ID: "simcall", Label: tr.T("Simulate an incoming call (debug)"), Key: "i"},
	}}
	for _, cv := range sorted {
		pin := ui.Action{ID: "pin", Label: tr.T("Pin chat"), Key: "p"}
		if cv.pinned {
			pin.Label = tr.T("Unpin")
		}
		n := ui.Node{Kind: ui.KindItem, ID: "c:" + cv.id, Icon: cv.icon, Title: cv.name,
			Subtitle: preview(cv.last()), Trailing: cv.last().at.Format("15:04"),
			Actions: []ui.Action{sdk.Open(tr.T("Open")), pin,
				{ID: "unread", Label: tr.T("Mark as unread"), Key: "u"},
				{ID: "delete", Label: tr.T("Delete chat"), Group: ui.GroupDanger}}}
		if cv.pinned {
			n.Title = "▲ " + n.Title
		}
		if cv.unread > 0 {
			n.Badge = fmt.Sprint(cv.unread)
		}
		s.Body = append(s.Body, n)
	}
	if c.call != nil {
		s.Actions = append([]ui.Action{{ID: "resume", Label: tr.T("Back to the call"), Key: "r"}}, s.Actions...)
	}
	return s
}

func (c *chat) chatScreen(cv *convo) ui.Screen {
	s := ui.Screen{ID: "chat:" + cv.id, Title: cv.name, Right: "video", Bottom: true, Actions: []ui.Action{
		{ID: "video", Label: tr.T("Video call"), Key: "v"},
		{ID: "voice", Label: tr.T("Voice call"), Key: "a"},
		{ID: "photo", Label: tr.T("Send a photo"), Key: "p"},
		{ID: "vmsg", Label: tr.T("Voice message"), Key: "r"},
		{ID: "transfer", Label: tr.T("Transfer"), Key: "t"},
	}}
	for _, m := range cv.msgs {
		n := ui.Node{Kind: ui.KindItem, ID: fmt.Sprintf("m:%d", m.id), Title: m.text, Src: m.img, Rows: 4,
			Subtitle: m.at.Format("15:04"), Style: ui.StyleBubbleIn,
			Actions: []ui.Action{
				{ID: "copy", Label: tr.T("Copy"), Key: "c"},
				{ID: "forward", Label: tr.T("Forward"), Key: "f"},
				{ID: "quote", Label: tr.T("Quote"), Key: "q"},
				{ID: "delete", Label: tr.T("Delete"), Group: ui.GroupDanger},
			}}
		if m.from == "me" {
			n.Style = ui.StyleBubbleOut
		} else if cv.id == "team" {
			n.Subtitle = tr.T("Wang Wu %s", n.Subtitle)
		}
		if m.img != "" {
			n.Actions = append([]ui.Action{{ID: "view", Label: tr.T("View full size"), Primary: true}}, n.Actions...)
			// A photo from the album is there already.
			if !strings.HasPrefix(m.img, ui.MediaPrefix) {
				n.Actions = append(n.Actions, ui.Action{ID: "save", Label: tr.T("Save to the album"), Key: "s"})
			}
		}
		if m.voice != "" {
			n.Actions = append([]ui.Action{{ID: "play", Label: tr.T("Play"), Primary: true}}, n.Actions...)
		}
		if webLink(m.text) != "" {
			n.Actions = append([]ui.Action{{ID: "read", Label: tr.T("Reader mode"), Primary: true}, {ID: "web", Label: tr.T("Web page"), Key: "w"}}, n.Actions...)
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindInput, ID: "compose", Placeholder: tr.T("Type a message…"), Submit: "send", Clear: true})
	return s
}

func (c *chat) callScreen() ui.Screen {
	k := c.call
	peer := c.find(k.peer)
	remote, local := k.remote, k.local
	if k.swapped {
		remote, local = local, remote
	}
	mic := ui.Action{ID: "mute", Label: tr.T("Mute"), Key: "m"}
	if !k.mic {
		mic.Label = tr.T("Unmute")
	}
	cam := ui.Action{ID: "cam", Label: tr.T("Camera off"), Key: "c"}
	if !k.cam {
		cam.Label = tr.T("Camera on")
	}
	status := tr.T("%s · end-to-end encrypted (simulated)", clock(time.Since(k.started)))
	if !k.mic {
		status += " · " + tr.T("Muted")
	}
	return ui.Screen{ID: "call", Title: tr.T("Video call with %s", peer.name), Right: "hangup",
		Actions: []ui.Action{{ID: "hangup", Label: tr.T("Hang up"), Key: "h"}, mic, cam, {ID: "speaker", Label: tr.T("Speaker"), Key: "s"}},
		Body: []ui.Node{
			{Kind: ui.KindMedia, ID: "video", Stream: remote, Pip: local, Rows: 15, Label: peer.name,
				Actions: []ui.Action{{ID: "swap", Label: tr.T("Swap views"), Primary: true}}},
			sdk.Styled(ui.StyleOK, status),
			sdk.Styled(ui.StyleDim, tr.T("The system composes the picture: the app only gets stream handles")),
		}}
}

func clock(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

func (c *chat) refresh() {
	c.app.Update(c.listScreen())
	if cv := c.find(c.open); cv != nil {
		c.app.Update(c.chatScreen(cv))
	}
}

func (c *chat) append(cv *convo, from, text, img string) *message {
	return c.add(cv, from, text, img, true)
}

func (c *chat) add(cv *convo, from, text, img string, notify bool) *message {
	c.nextID++
	m := &message{id: c.nextID, from: from, text: text, img: img, at: time.Now()}
	cv.msgs = append(cv.msgs, m)
	if from != "me" && !(c.fg && c.open == cv.id) {
		cv.unread++
		if notify {
			c.app.Notify(ui.Notification{ID: "msg:" + cv.id, Title: cv.name, Body: preview(m),
				Actions: []ui.Action{{ID: "reply", Label: tr.T("Reply"), Key: "r", Input: tr.T("Reply to %s", cv.name)}}})
		}
	}
	c.refresh()
	return m
}

func (c *chat) msgByNode(cv *convo, node string) *message {
	for _, m := range cv.msgs {
		if fmt.Sprintf("m:%d", m.id) == node {
			return m
		}
	}
	return nil
}

func (c *chat) action(e sdk.ActionEvent) {
	switch {
	case e.Screen == "list":
		cv := c.find(strings.TrimPrefix(e.Node, "c:"))
		switch e.Action {
		case ui.EventOpen:
			c.open, cv.unread = cv.id, 0
			c.app.Push(c.chatScreen(cv))
			c.app.Update(c.listScreen())
		case "pin":
			cv.pinned = !cv.pinned
			c.app.Update(c.listScreen())
		case "unread":
			cv.unread = max(1, cv.unread)
			c.app.Update(c.listScreen())
		case "delete":
			for i, x := range c.convos {
				if x == cv {
					c.convos = append(c.convos[:i], c.convos[i+1:]...)
				}
			}
			c.app.Update(c.listScreen())
		case "new":
			c.app.Toast(tr.T("Not in the prototype: new chat"))
		case "simcall":
			c.app.Toast(tr.T("Zhang San calls in 3 seconds; you can go back to the idle screen meanwhile"))
			c.tell(c.find("zhangsan"), bot.Message{Kind: "ring"})
		case "resume":
			if c.call != nil {
				c.app.Push(c.callScreen())
			}
		}
	case e.Screen == "rec":
		switch e.Action {
		case "sendvoice":
			c.sendVoice()
		case "cancel":
			c.stopRec()
			c.app.Pop()
		}
	case strings.HasPrefix(e.Screen, "transfer:"):
		if cv := c.find(strings.TrimPrefix(e.Screen, "transfer:")); cv != nil && e.Action == "transfer_ok" {
			c.transfer(cv, e.Value)
		}
	case strings.HasPrefix(e.Screen, "chat:"):
		cv := c.find(strings.TrimPrefix(e.Screen, "chat:"))
		if cv == nil {
			return
		}
		m := c.msgByNode(cv, e.Node)
		switch e.Action {
		case "transfer":
			c.app.Push(ui.Screen{ID: "transfer:" + cv.id, Title: tr.T("Transfer to %s", cv.name), Body: []ui.Node{
				sdk.Styled(ui.StyleDim, tr.T("The payment app (Feiqian) confirms it; Hongyan never sees your payment password.")),
				{Kind: ui.KindInput, ID: "amount", Placeholder: tr.T("Amount (yuan)"), Submit: "transfer_ok"},
			}})
		case "send":
			text := strings.TrimSpace(e.Value)
			c.append(cv, "me", text, "")
			c.tell(cv, bot.Message{Kind: "text", Text: text})
		case "vmsg":
			c.startRec(cv)
		case "play":
			if m != nil && m.voice != "" {
				c.playVoice(m)
			}
		case "photo":
			c.sendPhoto(cv)
		case "read":
			if m != nil {
				c.readPage(webLink(m.text))
			}
		case ui.EventWeb:
			// The page as it is, in the system's web view.
			if m != nil {
				c.app.Push(ui.Screen{ID: "web", Title: tr.T("Web page"), Body: []ui.Node{{Kind: ui.KindWeb, ID: "page", Src: webLink(m.text)}}})
			}
		case "copy":
			if m != nil {
				c.app.Go(ui.CapClipboardSet, ui.ClipArgs{Text: preview(m)}, nil, func(error) {})
			}
		case "quote":
			s := c.chatScreen(cv)
			s.Body[len(s.Body)-1].Value = tr.T("“%s”", preview(m)) + " "
			s.Focus = "compose"
			c.app.Update(s)
		case "delete":
			for i, x := range cv.msgs {
				if x == m {
					cv.msgs = append(cv.msgs[:i], cv.msgs[i+1:]...)
				}
			}
			c.refresh()
		case "forward":
			c.app.Push(c.forwardScreen(m))
		case "save":
			c.app.Go(ui.CapMediaSave, ui.MediaSaveArgs{Src: m.img, Name: tr.T("Hongyan · %s", cv.name)}, nil, func(err error) {
				if err != nil && !ui.Cancelled(err) {
					c.app.Toast(tr.T("Not saved: %v", err))
				}
			})
		case "view":
			c.app.Push(ui.Screen{ID: "photo", Title: tr.T("Photo"), Body: []ui.Node{
				{Kind: ui.KindImage, Src: m.img, Rows: 14, Label: tr.T("←→ another photo · +/- zoom (not in the prototype)")},
			}})
		case "video", "voice":
			c.startCall(cv.id, e.Action == "video")
		}
	case strings.HasPrefix(e.Screen, "fwd:"):
		var mid int
		fmt.Sscanf(e.Screen, "fwd:%d", &mid)
		target := c.find(strings.TrimPrefix(e.Node, "c:"))
		for _, cv := range c.convos {
			for _, m := range cv.msgs {
				if m.id == mid && target != nil {
					c.app.Pop()
					c.append(target, "me", m.text, m.img)
					c.app.Toast(tr.T("Forwarded to %s", target.name))
					return
				}
			}
		}
	case e.Screen == "share" && c.share != nil:
		if target := c.find(strings.TrimPrefix(e.Node, "c:")); target != nil {
			c.app.Pop()
			c.append(target, "me", c.share.text, c.share.img)
			c.tell(target, bot.Message{Kind: map[bool]string{true: "photo", false: "text"}[c.share.img != ""], Text: c.share.text})
			c.app.Toast(tr.T("Shared with %s", target.name))
			c.share = nil
		}
	case e.Screen == "call":
		c.callAction(e.Action)
	}
}

// shared handles a "share:text:<text>" / "share:image:<photo>" deep link
// from the system share sheet: the user picks the conversation. A shared
// photo has been granted to us, so its media ID displays.
func (c *chat) shared(link string) {
	rest, ok := strings.CutPrefix(link, "share:")
	if !ok {
		return
	}
	kind, payload, _ := strings.Cut(rest, ":")
	switch kind {
	case "text":
		c.share = &message{text: payload}
	case "image":
		c.share = &message{img: payload}
	case "link":
		url, title, _ := strings.Cut(payload, "\n")
		c.share = &message{text: strings.TrimSpace(title + " " + url)}
	case "file":
		// The file was granted to us; its name is in its item.
		var it ui.FileItem
		if _, err := c.app.CallFile(ui.CapFileOpen, ui.FileArgs{ID: payload}, &it, func(*os.File) (int64, error) { return 0, nil }); err != nil {
			c.app.Toast(tr.T("Cannot open the shared file: %v", err))
			return
		}
		c.share = &message{text: tr.T("[File] %s", it.Name)}
	default:
		return
	}
	s := c.forwardScreen(c.share)
	s.ID, s.Title = "share", tr.T("Share with…")
	c.app.Push(s)
}

func (c *chat) forwardScreen(m *message) ui.Screen {
	s := ui.Screen{ID: fmt.Sprintf("fwd:%d", m.id), Title: tr.T("Forward to…")}
	for _, cv := range c.convos {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "c:" + cv.id, Icon: cv.icon, Title: cv.name,
			Actions: []ui.Action{{ID: "send", Label: tr.T("Send"), Primary: true}}})
	}
	return s
}

func (c *chat) later(d time.Duration, f func()) {
	time.AfterFunc(d, func() { c.app.Post(f) })
}

// transfer asks the app that pays (an intent, intent.invoke) to send cv's
// peer the amount; the payment app confirms with the user and answers.
func (c *chat) transfer(cv *convo, value string) {
	yuanAmount, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	cents := int(math.Round(yuanAmount * 100))
	if err != nil || cents <= 0 || cents > 5000000 {
		c.app.Toast(tr.T("The amount must be between 0.01 and 50000 yuan"))
		return
	}
	amount := fmt.Sprintf("¥%d.%02d", cents/100, cents%100)
	payload, _ := json.Marshal(map[string]any{"to": cv.name, "amount": cents, "memo": tr.T("Hongyan transfer")})
	var res ui.IntentRes
	c.app.Go(ui.CapIntentInvoke, ui.IntentArgs{Action: "pay", Payload: string(payload), What: tr.T("Transfer to %s: %s", cv.name, amount)}, &res, func(err error) {
		switch {
		case err != nil && strings.HasSuffix(err.Error(), ui.ErrNoHandler):
			c.app.Toast(tr.T("No app can make payments"))
		case err != nil || !res.OK:
			c.app.Toast(tr.T("Nothing was transferred"))
		default:
			c.app.Pop()
			c.append(cv, "me", tr.T("Transferred %s", amount), "")
		}
	})
}

// tell sends what the user did in a conversation to the chat server, whose
// answers come back as pushes (the app may be frozen meanwhile). Without a
// server (PHONE_CHAT_SERVER) the app plays the same bot itself.
func (c *chat) tell(cv *convo, m bot.Message) {
	if cv == nil {
		return
	}
	m.From, m.To = "me", cv.id
	if c.srv == "" {
		for _, r := range bot.Replies(m) {
			r := r
			c.later(r.After, func() {
				if r.Call {
					c.ring(cv.id)
				} else {
					c.append(cv, cv.id, r.Text, "")
				}
			})
		}
		return
	}
	c.post("/v1/send", m)
}

// register logs in to the server with the push token and the current push
// key: at start, and again when the system replaced the key.
func (c *chat) register() {
	var tok ui.PushToken
	c.app.Go(ui.CapPushRegister, nil, &tok, func(err error) {
		if err != nil {
			return
		}
		// The server seals message data to our key; the keystore keeps
		// the private half and opens the data for us (push.open).
		var pk ui.PushKeyRes
		c.app.Go(ui.CapPushKey, nil, &pk, func(err error) {
			if err != nil {
				log.Printf("push key: %v", err)
			}
			c.login(tok.Token, pk.Key)
		})
	})
}

// login gives the server the push token and the app's own push key, to
// which the server seals message data: the gateway and the system only see
// ciphertext.
func (c *chat) login(token, key string) {
	if c.srv == "" {
		return
	}
	body := map[string]string{"user": "me", "push_token": token}
	if key != "" {
		body["push_key"] = key // const-check: ok (the chat server's login field)
	}
	c.post("/v1/login", body)
}

func (c *chat) post(path string, body any) {
	raw, _ := json.Marshal(body)
	go func() {
		resp, err := c.http.Post(c.srv+path, "application/json", bytes.NewReader(raw))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = errors.New(resp.Status)
			}
		}
		if err != nil {
			c.app.Post(func() { c.app.Toast(tr.T("Cannot reach the Hongyan server: %v", err)) })
		}
	}()
}

// onPush handles what the chat server sends through the system push: a
// message for a conversation, or (as a VoIP push) an incoming call.
func (c *chat) onPush(value string) {
	var ev ui.PushEvent
	if json.Unmarshal([]byte(value), &ev) != nil {
		return
	}
	if push.IsSealed(ev.Data) {
		var res ui.PushOpenRes
		c.app.Go(ui.CapPushOpen, ui.PushOpenArgs{Data: ev.Data}, &res, func(err error) {
			plain, derr := base64.StdEncoding.DecodeString(res.Data)
			if err != nil || derr != nil {
				log.Printf("push %s: cannot open: %v", ev.ID, err)
				return
			}
			c.pushData(ev, plain)
		})
		return
	}
	c.pushData(ev, []byte(ev.Data))
}

// pushData handles the (opened) data of a push.
func (c *chat) pushData(ev ui.PushEvent, data []byte) {
	var p struct {
		Type string `json:"type"`
		Peer string `json:"peer"`
		Text string `json:"text"`
	}
	if json.Unmarshal(data, &p) != nil {
		return
	}
	cv := c.find(p.Peer)
	if cv == nil {
		return
	}
	switch p.Type {
	case "call":
		c.ring(cv.id)
	case "msg":
		// When the system already showed the notification, do not add one.
		c.add(cv, cv.id, p.Text, "", !ev.Notified)
	}
}

// ring raises a system incoming-call screen; it works while the app is in
// the background because the shell, not the app, draws it.
func (c *chat) ring(peer string) {
	cv := c.find(peer)
	if cv == nil || c.call != nil {
		return
	}
	id := fmt.Sprintf("ring:%d", time.Now().UnixNano())
	c.ringing[id] = peer
	c.app.Notify(ui.Notification{ID: id, Kind: "call", Title: cv.name, Body: tr.T("Invites you to a video call")})
}

// notifReply sends a quick reply typed in the notification centre, as if
// typed in the conversation.
func (c *chat) notifReply(id, action, text string) {
	peerID, own := strings.CutPrefix(id, "msg:")
	if !own {
		peerID, own = strings.CutPrefix(id, "thread:")
	}
	cv := c.find(peerID)
	if !own || action != "reply" || cv == nil || strings.TrimSpace(text) == "" {
		return
	}
	text = strings.TrimSpace(text)
	c.append(cv, "me", text, "")
	c.tell(cv, bot.Message{Kind: "text", Text: text})
}

func (c *chat) notifAction(id, action string) {
	peerID, own := strings.CutPrefix(id, "msg:")
	if !own {
		peerID, own = strings.CutPrefix(id, "thread:")
	}
	if own && action == "open" {
		if cv := c.find(peerID); cv != nil {
			c.open, cv.unread = cv.id, 0
			c.app.Reset(c.listScreen())
			c.app.Push(c.chatScreen(cv))
		}
		return
	}
	peer, ok := c.ringing[id]
	if !ok {
		return
	}
	delete(c.ringing, id)
	cv := c.find(peer)
	if action == "answer" {
		c.startCall(peer, true)
		return
	}
	c.append(cv, "me", tr.T("[Video call declined]"), "")
}

func (c *chat) startCall(peer string, video bool) {
	if c.call != nil {
		c.app.Toast(tr.T("Already in a call"))
		return
	}
	var res ui.CallStartResult
	c.app.Go(ui.CapCallStart, ui.CallStartArgs{Peer: peer, Video: video}, &res, func(err error) {
		if err != nil {
			c.app.Toast(tr.T("Cannot call: %v", err))
			return
		}
		c.call = &call{id: res.CallID, peer: peer, local: res.Local, remote: res.Remote, mic: true, cam: video, started: time.Now(),
			stop: make(chan struct{})}
		c.callAudio(c.call)
		c.app.Push(c.callScreen())
		c.app.Update(c.listScreen())
	})
}

// callAudio opens the call's two streams. The prototype's peer is simulated:
// its voice is a speech-like rhythm, and what the microphone hears goes
// nowhere (a real app sends it to its server).
func (c *chat) callAudio(k *call) {
	c.app.GoAudio(ui.CapAudioStream, tr.T("Call: the other party"), func(s *sdk.AudioStream, err error) {
		if err != nil {
			log.Printf("call speaker: %v", err)
			return
		}
		k.speaker = s
		go func() {
			buf := make([]int16, audio.Rate/50)
			for n := 0; ; n++ {
				select {
				case <-k.stop:
					s.Close()
					return
				default:
				}
				peerVoice(buf, n)
				for off := 0; off < len(buf); {
					off += s.Ring.Write(buf[off:])
					if off < len(buf) {
						time.Sleep(10 * time.Millisecond)
					}
				}
			}
		}()
	})
	c.app.GoAudio(ui.CapAudioRecord, tr.T("Call: microphone"), func(s *sdk.AudioStream, err error) {
		if err != nil {
			log.Printf("call microphone: %v", err)
			return
		}
		k.micIn = s
		go func() {
			buf := make([]int16, audio.Rate/50)
			for {
				select {
				case <-k.stop:
					s.Close()
					return
				default:
				}
				if s.Ring.Read(buf) == 0 {
					time.Sleep(10 * time.Millisecond)
				}
			}
		}()
	})
}

// peerVoice fills 20 ms of the simulated peer: syllables of 600 and 900 Hz
// with pauses, a rhythm the device's audio check tells from the tones.
func peerVoice(buf []int16, chunk int) {
	syllable := chunk / 10 % 4
	f := []float64{600, 900, 600, 0}[syllable]
	for i := range buf {
		if f == 0 {
			buf[i] = 0
			continue
		}
		t := float64(chunk*len(buf)+i) / audio.Rate
		buf[i] = int16(5000 * math.Sin(2*math.Pi*f*t))
	}
}

func (c *chat) callAction(action string) {
	k := c.call
	if k == nil {
		return
	}
	switch action {
	case "hangup":
		dur := clock(time.Since(k.started))
		c.app.Go(ui.CapCallEnd, ui.CallControlArgs{CallID: k.id}, nil, func(err error) {
			if err != nil {
				log.Printf("call end: %v", err)
			}
		})
		c.call = nil
		close(k.stop)
		c.app.Pop()
		if cv := c.find(k.peer); cv != nil {
			c.append(cv, "me", tr.T("[Video call %s]", dur), "")
		}
	case "mute":
		k.mic = !k.mic
		c.app.Go(ui.CapCallControl, ui.CallControlArgs{CallID: k.id, Mic: &k.mic}, nil, func(error) {})
	case "cam":
		k.cam = !k.cam
		c.app.Go(ui.CapCallControl, ui.CallControlArgs{CallID: k.id, Cam: &k.cam}, nil, func(error) {})
	case "swap":
		k.swapped = !k.swapped
	case "speaker":
		c.app.Toast(tr.T("Switched to the speaker"))
	}
	if c.call != nil {
		c.app.Update(c.callScreen())
	}
}

func (c *chat) popped(id string) {
	if strings.HasPrefix(id, "chat:") {
		c.open = ""
	}
	if id == "rec" {
		c.stopRec()
	}
}

func voiceClock(frames int) string {
	s := frames / audio.Rate
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func (c *chat) recScreen() ui.Screen {
	r := c.rec
	s := ui.Screen{ID: "rec", Title: tr.T("Voice message"), Center: "sendvoice", Right: "cancel", Actions: []ui.Action{
		{ID: "sendvoice", Label: tr.T("Send")},
		{ID: "cancel", Label: tr.T("Cancel")},
	}}
	if r == nil {
		return s
	}
	status := sdk.Styled(ui.StyleAccent, tr.T("Recording…"))
	if r.interrupted {
		status = sdk.Styled(ui.StyleWarn, tr.T("Recording stopped: the system stops recording when the app leaves the foreground"))
	}
	s.Body = []ui.Node{
		sdk.Styled(ui.StyleBig, voiceClock(len(r.pcm))),
		status,
		sdk.Progress("", float64(len(r.pcm))/maxVoice, tr.T("At most 1:00")),
		sdk.Styled(ui.StyleDim, tr.T("OK sends · Back cancels · to %s", r.cv.name)),
	}
	return s
}

func (c *chat) startRec(cv *convo) {
	c.app.GoAudio(ui.CapAudioRecord, tr.T("Voice message"), func(s *sdk.AudioStream, err error) {
		if err != nil {
			c.app.Toast(tr.T("Cannot record: %v", err))
			return
		}
		c.stopRec()
		c.rec = &recorder{cv: cv, s: s, done: make(chan struct{})}
		c.app.Push(c.recScreen())
		done := c.rec.done
		go func() {
			t := time.NewTicker(100 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					c.app.Post(func() {
						if c.rec != nil && c.rec.done == done {
							before := len(c.rec.pcm) / audio.Rate
							c.pumpRec()
							if len(c.rec.pcm) >= maxVoice {
								c.sendVoice()
							} else if len(c.rec.pcm)/audio.Rate != before {
								c.app.Update(c.recScreen())
							}
						}
					})
				}
			}
		}()
	})
}

func (c *chat) pumpRec() {
	r := c.rec
	if r == nil || r.s.Ring == nil {
		return
	}
	buf := make([]int16, audio.Rate/5)
	for {
		n := r.s.Ring.Read(buf)
		if n == 0 {
			return
		}
		r.pcm = append(r.pcm, buf[:n]...)
	}
}

// stopRec ends a recording without sending it.
func (c *chat) stopRec() {
	if c.rec == nil {
		return
	}
	close(c.rec.done)
	c.rec.s.Close()
	c.rec = nil
}

func (c *chat) sendVoice() {
	r := c.rec
	if r == nil {
		return
	}
	c.pumpRec()
	c.stopRec()
	c.app.Pop()
	if len(r.pcm) < audio.Rate/2 {
		c.app.Toast(tr.T("Too short"))
		return
	}
	// Stored as it would be uploaded: Ogg Opus, about a fiftieth of the PCM.
	path := filepath.Join(c.app.DataDir, fmt.Sprintf("voice-%d.opus", c.nextID+1))
	raw, err := voice.Encode(r.pcm)
	if err == nil {
		err = os.WriteFile(path, raw, 0o600)
	}
	if err != nil {
		c.app.Toast(tr.T("Saving failed: %v", err))
		return
	}
	m := c.append(r.cv, "me", tr.T("[Voice %s]", voiceClock(len(r.pcm))), "")
	m.voice = path
	c.refresh()
	c.tell(r.cv, bot.Message{Kind: "voice", Secs: len(r.pcm) / audio.Rate})
}

// sendPhoto lets the user pick a photo in the system picker; the app gets
// that one file (read-only) to upload, and shows it by its media ID.
func (c *chat) sendPhoto(cv *convo) {
	go func() {
		var it ui.MediaItem
		n, err := c.app.CallFile(ui.CapMediaPick, ui.MediaArgs{Kind: ui.MediaImage}, &it, func(f *os.File) (int64, error) {
			return io.Copy(io.Discard, f)
		})
		c.app.Post(func() {
			if err != nil {
				if !ui.Cancelled(err) {
					c.app.Toast(tr.T("Cannot send the photo: %v", err))
				}
				return
			}
			c.append(cv, "me", "", it.ID)
			c.app.Toast(tr.T("Photo sent (%d KB)", (n+1023)/1024))
			c.tell(cv, bot.Message{Kind: "photo"})
		})
	}()
}

// playVoice writes the recording into a playback ring as fast as it frees up.
func (c *chat) playVoice(m *message) {
	raw, err := os.ReadFile(m.voice)
	var pcm []int16
	if err == nil {
		pcm, err = voice.Decode(raw)
	}
	if err != nil {
		c.app.Toast(tr.T("The voice file is not available"))
		return
	}
	c.app.GoAudio(ui.CapAudioStream, tr.T("Voice message"), func(s *sdk.AudioStream, err error) {
		if err != nil {
			c.app.Toast(tr.T("Cannot play: %v", err))
			return
		}
		go func() {
			for off := 0; off < len(pcm); {
				off += s.Ring.Write(pcm[off:])
				if off < len(pcm) {
					time.Sleep(20 * time.Millisecond)
				}
			}
			s.Close()
		}()
	})
}

func (c *chat) tick() {
	if c.call != nil {
		c.app.Update(c.callScreen())
	}
}

// webLink is the first web address in a message.
func webLink(text string) string {
	for _, f := range strings.Fields(text) {
		if i := strings.Index(f, "http://"); i >= 0 {
			return f[i:]
		}
		if i := strings.Index(f, "https://"); i >= 0 {
			return f[i:]
		}
	}
	return ""
}

// readPage opens a page in reading mode (the web view's text side): the
// page is fetched and its main content extracted here, in the app's own
// sandbox, and shown as a doc, whose links the system puts in the menu.
func (c *chat) readPage(link string) {
	c.app.Toast(tr.T("Opening…"))
	go func() {
		page, err := func() (*reader.Page, error) {
			base, err := url.Parse(link)
			if err != nil {
				return nil, err
			}
			res, err := c.http.Get(link)
			if err != nil {
				return nil, err
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				return nil, errors.New(tr.T("The page returned %d", res.StatusCode))
			}
			return reader.Extract(res.Body, base)
		}()
		c.app.Post(func() {
			if err != nil {
				c.app.Toast(tr.T("Cannot open it: %v", err))
				return
			}
			title := page.Title
			if title == "" {
				title = tr.T("Reader mode")
			}
			c.app.Push(ui.Screen{ID: "read", Title: title, Body: []ui.Node{
				{Kind: ui.KindDoc, Text: page.Markdown},
				sdk.Styled(ui.StyleDim, tr.T("Reader mode: only the main text; the links are in the menu")),
			}})
		})
	}()
}
