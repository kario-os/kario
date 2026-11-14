// Dialer: a number field that searches contacts as you type (by number or
// by the 9-key digits of the name's pinyin), the call log and the in-call
// screen. Calls come from phone-telephonyd through the shell; an incoming
// call rings on the system call screen and arrives here once answered.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

type dialer struct {
	app      *sdk.App
	contacts []ui.Contact
	log      []ui.TelLogEntry
	query    string
	call     *ui.TelCall
	onCall   bool
	speaker  bool
	// showLog puts focus on the latest call: the green key on an empty
	// number shows recent calls, and pressed again redials (Nokia).
	showLog bool
	// ussdState: the network message page is up, with the session's last
	// state.
	ussdState string
}

var t9 = map[rune]rune{}

func init() {
	for d, letters := range []string{"", "", "abc", "def", "ghi", "jkl", "mno", "pqrs", "tuv", "wxyz"} {
		for _, r := range letters {
			t9[r] = rune('0' + d)
		}
	}
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if d, ok := t9[r]; ok {
			b.WriteRune(d)
		}
	}
	return b.String()
}

// matches implements smart dialling: the typed digits occur in the number,
// or start the 9-key spelling of the full pinyin or of its initials.
func matches(c ui.Contact, q string) bool {
	if q == "" {
		return true
	}
	if strings.Contains(c.Number, q) {
		return true
	}
	syl := strings.Fields(c.Pinyin)
	initials := ""
	for _, s := range syl {
		initials += s[:1]
	}
	return strings.HasPrefix(digits(strings.Join(syl, "")), q) || strings.HasPrefix(digits(initials), q)
}

var resultLabel = map[string]string{"answered": "Answered", "missed": "Missed", "rejected": "Declined", "busy": "Busy", "outgoing": "Dialled"}

func clock(sec int64) string { return fmt.Sprintf("%02d:%02d", sec/60, sec%60) }

func (d *dialer) mainScreen() ui.Screen {
	s := ui.Screen{ID: "main", Title: tr.T("Phone"), Actions: []ui.Action{
		{ID: "dial", Label: tr.T("Call"), Key: "d"},
		{ID: "refresh", Label: tr.T("Refresh"), Key: "r"},
	}}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindInput, ID: "number", Placeholder: tr.T("Number, or the keypad digits of a name's pinyin"),
		Keyboard: "phone", Live: true, Submit: "dial", Value: d.query})
	var hits []ui.Node
	for _, c := range d.contacts {
		if d.query != "" && matches(c, d.query) {
			hits = append(hits, ui.Node{Kind: ui.KindItem, ID: "c:" + c.Number, Icon: string([]rune(c.Name)[:1]),
				Title: c.Name, Subtitle: c.Number, Actions: []ui.Action{{ID: "call", Label: tr.T("Call"), Primary: true}}})
		}
	}
	if d.query != "" {
		s.Body = append(s.Body, sdk.Section(tr.T("Contacts (%d)", len(hits))))
		s.Body = append(s.Body, hits...)
		if len(hits) == 0 {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No matching contacts; press OK to call the number")))
		}
		return s
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Recent calls")))
	if d.showLog && len(d.log) > 0 {
		s.Focus, d.showLog = "log:0", false
	}
	for i, e := range d.log {
		if i >= 20 {
			break
		}
		sub := tr.T(resultLabel[e.Result])
		if e.Duration > 0 {
			sub += " · " + clock(e.Duration)
		}
		title := e.Number
		if e.Name != "" {
			title, sub = e.Name, sub+" · "+e.Number
		}
		n := ui.Node{Kind: ui.KindItem, ID: fmt.Sprintf("log:%d", i), Icon: map[string]string{"in": tr.T("In"), "out": tr.T("Out")}[e.Dir],
			Title: title, Subtitle: sub, Trailing: time.Unix(e.At, 0).Format("15:04"),
			Actions: []ui.Action{{ID: "call", Label: tr.T("Call back"), Primary: true}}}
		if e.Result == "missed" {
			n.Badge = tr.T("Missed")
		}
		s.Body = append(s.Body, n)
	}
	if len(d.log) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No calls yet")))
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Contacts")))
	for _, c := range d.contacts {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "c:" + c.Number, Icon: string([]rune(c.Name)[:1]),
			Title: c.Name, Subtitle: c.Number, Actions: []ui.Action{{ID: "call", Label: tr.T("Call"), Primary: true}}})
	}
	return s
}

var stateLabel = map[string]string{"dialing": "Calling…", "alerting": "Ringing…", "incoming": "Incoming call", "active": "In call", "held": "On hold", "ended": "Call ended"}

func (d *dialer) callScreen() ui.Screen {
	c := d.call
	status := tr.T(stateLabel[c.State])
	if (c.State == "active" || c.State == "held") && c.Connected > 0 {
		status += " " + clock(time.Now().Unix()-c.Connected)
	}
	who := c.Number
	if c.Name != "" {
		who = c.Name
	}
	s := ui.Screen{ID: "call", Title: tr.T("Phone call"), Right: "hangup", Focus: "hangup", Actions: []ui.Action{
		{ID: "hangup", Label: tr.T("Hang up"), Key: "e"},
		{ID: "mute", Label: map[bool]string{false: tr.T("Mute"), true: tr.T("Unmute")}[c.Muted], Key: "m"},
		{ID: "hold", Label: map[bool]string{false: tr.T("Hold"), true: tr.T("Resume call")}[c.State == "held"], Key: "h"},
		{ID: "speaker", Label: map[bool]string{false: tr.T("Speaker"), true: tr.T("Speaker off")}[d.speaker], Key: "s"},
	}}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleBig, Text: who})
	if c.Name != "" {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleDim, Text: c.Number})
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleAccent, Text: status})
	var flags []string
	if c.Muted {
		flags = append(flags, tr.T("Muted"))
	}
	if d.speaker {
		flags = append(flags, tr.T("Speaker"))
	}
	if len(flags) > 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, strings.Join(flags, " · ")))
	}
	s.Body = append(s.Body, sdk.Button("hangup", tr.T("Hang up"), "hangup"))
	return s
}

func (d *dialer) reload(show func(ui.Screen)) {
	_ = d.app.Call(ui.CapContacts, nil, &d.contacts)
	_ = d.app.Call(ui.CapTelLog, nil, &d.log)
	show(d.mainScreen())
}

// ussdCode: what starts with * or # and ends with # goes to the network as
// USSD (*100#), not as a call.
var ussdCode = regexp.MustCompile(`^[*#][0-9*#]*#$`)

func (d *dialer) ussd(code string) {
	var r ui.USSDReply
	d.app.Go(ui.CapTelUSSD, ui.USSDArgs{Code: code}, &r, func(err error) { d.ussdDone(r, err) })
}

// ussdDone shows what the network (or the phone, for *#06#) answered; a
// question gets a reply box.
func (d *dialer) ussdDone(r ui.USSDReply, err error) {
	if err != nil {
		d.app.Toast(tr.T("No answer from the network: %v", err))
		return
	}
	if r.State == "done" && r.Text == "" {
		if d.ussdState != "" {
			d.ussdState = "done"
			d.app.Replace(ui.Screen{ID: "ussd", Title: tr.T("Network message"), Body: []ui.Node{sdk.Styled(ui.StyleDim, tr.T("Session ended"))}})
		}
		return
	}
	s := ui.Screen{ID: "ussd", Title: tr.T("Network message")}
	for _, l := range strings.Split(strings.TrimSpace(r.Text), "\n") {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Text: l})
	}
	if r.State == "input" {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindInput, ID: "reply", Placeholder: tr.T("Reply"), Keyboard: "phone", Submit: "ussd_reply"})
		s.Actions = []ui.Action{{ID: "ussd_cancel", Label: tr.T("End session"), Key: "x"}}
	}
	if d.ussdState == "" {
		d.app.Push(s)
	} else {
		d.app.Replace(s)
	}
	d.ussdState = r.State
}

func (d *dialer) dial(number string) {
	number = strings.TrimSpace(number)
	if ussdCode.MatchString(number) && len(number) >= 3 {
		d.ussd(number)
		return
	}
	if number == "" {
		if len(d.log) > 0 {
			d.showLog = true
			d.reload(d.app.Reset)
			return
		}
		d.app.Toast(tr.T("Enter a number"))
		return
	}
	var res map[string]string
	d.app.Go(ui.CapTelDial, ui.DialArgs{Number: number}, &res, func(err error) {
		if err != nil {
			d.app.Toast(tr.T("Cannot call: %v", err))
			return
		}
		d.call = &ui.TelCall{ID: res["call_id"], Number: number, Dir: "out", State: "dialing"}
		for _, c := range d.contacts {
			if c.Number == number {
				d.call.Name = c.Name
			}
		}
		d.showCall()
	})
}

func (d *dialer) showCall() {
	if d.onCall {
		d.app.Update(d.callScreen())
		return
	}
	d.onCall = true
	d.app.Push(d.callScreen())
}

// open shows the call screen for a call that is already up (answered from
// the system incoming screen, or placed by another app).
func (d *dialer) open(id string) {
	var calls []ui.TelCall
	if err := d.app.Call(ui.CapTelCalls, nil, &calls); err != nil {
		return
	}
	for i := range calls {
		if calls[i].ID == id {
			d.call = &calls[i]
			d.showCall()
		}
	}
}

func (d *dialer) callEvent(c ui.TelCall) {
	if d.call == nil || c.ID != d.call.ID {
		return
	}
	if c.State != "ended" {
		d.call = &c
		d.showCall()
		return
	}
	dur := int64(0)
	if c.Connected > 0 {
		dur = c.Ended - c.Connected
	}
	msg := tr.T("Call ended %s", clock(dur))
	if c.Reason == "busy" {
		msg = tr.T("The other party is busy")
	}
	d.call = nil
	if d.onCall {
		d.onCall = false
		d.app.Pop()
	}
	d.app.Toast(msg)
	d.query = ""
	d.reload(d.app.Update)
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	d := &dialer{app: app}
	// Deep links: "call:<id>" shows a call, "dial:<digits>" starts typing a
	// number (digits pressed on the idle screen), "contacts" the main list.
	link := func(arg string) {
		if id, ok := strings.CutPrefix(arg, "call:"); ok {
			d.open(id)
			return
		}
		if num, ok := strings.CutPrefix(arg, "dial:"); ok && !d.onCall {
			d.query = num
			d.reload(app.Reset)
		}
		if arg == "log" && !d.onCall {
			d.query, d.showLog = "", true
			d.reload(app.Reset)
		}
	}
	app.OnStart = func(arg string) {
		if num, ok := strings.CutPrefix(arg, "dial:"); ok {
			d.query = num
		}
		d.showLog = arg == "log"
		d.reload(app.Reset)
		if id, ok := strings.CutPrefix(arg, "call:"); ok {
			d.open(id)
		}
	}
	app.OnLifecycle = func(state string) {
		if state == ui.StateForeground && !d.onCall {
			d.reload(app.Update)
		}
	}
	app.OnPopped = func(screen string) {
		if screen == "call" {
			d.onCall = false
		}
		if screen == "ussd" {
			// Leaving a menu the network waits on ends the session.
			if d.ussdState == "input" {
				app.Go(ui.CapTelUSSD, ui.USSDArgs{Cancel: true}, nil, func(error) {})
			}
			d.ussdState = ""
		}
	}
	app.OnEvent = func(name, value string) {
		switch name {
		case ui.EventTelCall:
			var c ui.TelCall
			if json.Unmarshal([]byte(value), &c) == nil {
				d.callEvent(c)
			}
		case ui.EventTelOpen:
			d.open(value)
		case ui.EventUSSD:
			var r ui.USSDReply
			if json.Unmarshal([]byte(value), &r) == nil {
				d.ussdDone(r, nil)
			}
		case ui.EventOpen:
			link(value)
		}
	}
	app.OnNotif = func(id, action string) { d.reload(app.Update) }
	app.OnAction = func(e sdk.ActionEvent) {
		switch {
		case e.Screen == "main" && e.Node == "number" && e.Action == "changed":
			d.query = strings.TrimSpace(e.Value)
			app.Update(d.mainScreen())
			// As on a Nokia: the IMEI shows as soon as the code is typed.
			if d.query == "*#06#" {
				d.ussd(d.query)
			}
		case e.Action == "ussd_reply":
			var r ui.USSDReply
			app.Go(ui.CapTelUSSD, ui.USSDArgs{Text: strings.TrimSpace(e.Value)}, &r, func(err error) { d.ussdDone(r, err) })
		case e.Action == "ussd_cancel":
			app.Go(ui.CapTelUSSD, ui.USSDArgs{Cancel: true}, nil, func(error) {})
			d.ussdState = ""
			app.Pop()
		case e.Action == "dial":
			d.dial(e.Fields["number"])
		case e.Action == "refresh":
			d.reload(app.Update)
		case e.Action == "call" && strings.HasPrefix(e.Node, "c:"):
			d.dial(strings.TrimPrefix(e.Node, "c:"))
		case e.Action == "call" && strings.HasPrefix(e.Node, "log:"):
			var i int
			fmt.Sscanf(e.Node, "log:%d", &i)
			if i < len(d.log) {
				d.dial(d.log[i].Number)
			}
		case e.Screen == "call" && d.call != nil:
			d.callAction(e.Action)
		}
	}
	// The call timer only runs while the call screen is up.
	go func() {
		for range time.Tick(time.Second) {
			app.Post(func() {
				if d.onCall && d.call != nil && d.call.State == "active" {
					app.Update(d.callScreen())
				}
			})
		}
	}()
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}

func (d *dialer) callAction(action string) {
	id := d.call.ID
	switch action {
	case "hangup":
		d.app.Go(ui.CapTelHangup, ui.CallIDArgs{CallID: id}, nil, func(err error) {
			if err != nil {
				d.app.Toast(err.Error())
			}
		})
	case "mute":
		d.app.Go(ui.CapTelMute, ui.CallIDArgs{CallID: id, On: !d.call.Muted}, nil, func(error) {})
	case "hold":
		d.app.Go(ui.CapTelHold, ui.CallIDArgs{CallID: id, On: d.call.State != "held"}, nil, func(error) {})
	case "speaker":
		d.speaker = !d.speaker
		route := map[bool]string{false: "earpiece", true: "speaker"}[d.speaker]
		d.app.Go(ui.CapAudioRoute, ui.AudioRouteArgs{Route: route}, nil, func(error) {})
		d.app.Update(d.callScreen())
	}
}
