// Dialer: a number field that searches contacts as you type (by number or
// by the 9-key digits of the name's pinyin), the call log (blocking a
// number from it: the system's block list, which declines its calls and
// files its messages apart) and the in-call screen (call.go). Calls come
// from phone-telephonyd through the shell; an incoming call rings on the
// system call screen and arrives here once answered.
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
	app      pages
	contacts []ui.Contact
	log      []ui.TelLogEntry
	query    string
	// calls: the calls the phone has up (connected, being set up, held;
	// not one still ringing in), by id. pending: the call being dialled
	// until telephonyd has answered the dial (dialSeq numbers the dials,
	// inflight counts those unanswered, cancelled the ones hung up before
	// the answer); early: events for calls dialled before that answer;
	// dropped: calls hung up that way, whose events are not the user's.
	calls     map[string]ui.TelCall
	pending   *ui.TelCall
	dialSeq   int
	inflight  int
	cancelled map[int]bool
	early     map[string]ui.TelCall
	dropped   map[string]bool
	// onCall: the call screen is in the page stack; adding: the second
	// call's number page (screenAdd, with addQuery typed) is over it.
	onCall   bool
	adding   bool
	addQuery string
	speaker  bool
	// showLog puts focus on the latest call: the green key on an empty
	// number shows recent calls, and pressed again redials (Nokia).
	showLog bool
	// ussdState: the network message page is up, with the session's last
	// state.
	ussdState string
	// blocks: the system's block list.
	blocks ui.BlockList
}

func newDialer(app pages) *dialer {
	return &dialer{app: app, calls: map[string]ui.TelCall{}, cancelled: map[int]bool{}, early: map[string]ui.TelCall{}, dropped: map[string]bool{}}
}

// hitNumber is the number smart dialling calls for a contact: the one the
// typed digits are in, else the first.
func hitNumber(c ui.Contact, q string) string {
	for _, n := range ui.NumbersOf(c) {
		if q != "" && strings.Contains(n.Number, q) {
			return n.Number
		}
	}
	return c.Number
}

// known: the number is one of a contact's.
func (d *dialer) known(number string) bool {
	return d.nameOf(number) != ""
}

// dialable: what a "Save number" can save (not a code like *#06#).
var dialable = regexp.MustCompile(`^\+?[0-9][0-9 ()-]{1,30}$`)

var resultLabel = map[string]string{"answered": "Answered", "missed": "Missed", "rejected": "Declined", "blocked": "Blocked", "busy": "Busy", "outgoing": "Dialled"}

// blocked: the number itself is on the block list.
func (d *dialer) blocked(number string) bool {
	k := ui.NumberKey(number)
	for _, e := range d.blocks.Numbers {
		if e == k {
			return true
		}
	}
	return false
}

func (d *dialer) mainScreen() ui.Screen {
	s := d.numberScreen("main", d.query)
	if dialable.MatchString(d.query) && !d.known(d.query) {
		s.Actions = append(s.Actions, ui.Action{ID: "save-number", Label: tr.T("Save number")})
	}
	return s
}

// numberScreen is the number field with what the typed digits find, or,
// with nothing typed, the recent calls and the contacts.
func (d *dialer) numberScreen(id, query string) ui.Screen {
	s := ui.Screen{ID: id, Title: tr.T("Phone"), Actions: []ui.Action{
		{ID: "dial", Label: tr.T("Call"), Key: "d"},
		{ID: "refresh", Label: tr.T("Refresh"), Key: "r"},
	}}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindInput, ID: "number", Placeholder: tr.T("Number, or the keypad digits of a name's pinyin"),
		Keyboard: "phone", Live: true, Submit: "dial", Value: query})
	var hits []ui.Node
	for _, c := range d.contacts {
		if query != "" && c.Number != "" && ui.ContactMatches(c, query) {
			n := hitNumber(c, query)
			hits = append(hits, ui.Node{Kind: ui.KindItem, ID: "c:" + n, Icon: string([]rune(c.Name)[:1]),
				Title: c.Name, Subtitle: n, Actions: []ui.Action{{ID: "call", Label: tr.T("Call"), Primary: true}}})
		}
	}
	if query != "" {
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
		if e.Name == "" && dialable.MatchString(e.Number) && id == "main" {
			n.Actions = append(n.Actions, ui.Action{ID: "save-number", Label: tr.T("Save number")})
		}
		switch {
		case id != "main" || !dialable.MatchString(e.Number):
		case d.blocked(e.Number):
			n.Actions = append(n.Actions, ui.Action{ID: "unblock", Label: tr.T("Unblock number")})
		default:
			n.Actions = append(n.Actions, ui.Action{ID: "block", Label: tr.T("Block number"), Key: "b",
				Confirm: tr.T("Block %s? Its calls are then declined without ringing, its messages go to Blocked.", title)})
		}
		s.Body = append(s.Body, n)
	}
	if len(d.log) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No calls yet")))
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Contacts")))
	for _, c := range d.contacts {
		if c.Number == "" {
			continue
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "c:" + c.Number, Icon: string([]rune(c.Name)[:1]),
			Title: c.Name, Subtitle: c.Number, Actions: []ui.Action{{ID: "call", Label: tr.T("Call"), Primary: true}}})
	}
	return s
}

func (d *dialer) reload(show func(ui.Screen)) {
	_ = d.app.Call(ui.CapContacts, nil, &d.contacts)
	_ = d.app.Call(ui.CapTelLog, nil, &d.log)
	_ = d.app.Call(ui.CapBlockList, nil, &d.blocks)
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

// saveNumber has the contacts app save the number as a new contact.
func (d *dialer) saveNumber(number string) {
	payload, _ := json.Marshal(ui.Contact{Number: number})
	var res ui.IntentRes
	d.app.Go(ui.CapIntentInvoke, ui.IntentArgs{Action: ui.IntentContactSave, Payload: string(payload), What: tr.T("save %s as a contact", number)}, &res, func(err error) {
		if err != nil && strings.HasSuffix(err.Error(), ui.ErrNoHandler) {
			d.app.Toast(tr.T("No app saves contacts"))
		}
		d.reload(d.app.Update)
	})
}

// link follows a deep link: "call:<id>" shows a call, "dial:<digits>"
// starts typing a number (digits pressed on the idle screen; during a call
// a second one), "log" the recent calls.
func (d *dialer) link(arg string) {
	if id, ok := strings.CutPrefix(arg, "call:"); ok {
		d.open(id)
		return
	}
	num, dial := strings.CutPrefix(arg, "dial:")
	switch {
	case d.inCall() && (dial || arg == "log"):
		d.openAdd(num)
	case dial:
		d.query = num
		d.reload(d.app.Reset)
	case arg == "log":
		d.query, d.showLog = "", true
		d.reload(d.app.Reset)
	}
}

func (d *dialer) action(e sdk.ActionEvent) {
	switch {
	case (e.Screen == "main" || e.Screen == screenAdd) && e.Node == "number" && e.Action == "changed":
		if e.Screen == screenAdd {
			d.addQuery = strings.TrimSpace(e.Value)
			d.app.Update(d.addScreen())
			return
		}
		d.query = strings.TrimSpace(e.Value)
		d.app.Update(d.mainScreen())
		// As on a Nokia: the IMEI shows as soon as the code is typed.
		if d.query == "*#06#" {
			d.ussd(d.query)
		}
	case e.Action == "ussd_reply":
		var r ui.USSDReply
		d.app.Go(ui.CapTelUSSD, ui.USSDArgs{Text: strings.TrimSpace(e.Value)}, &r, func(err error) { d.ussdDone(r, err) })
	case e.Action == "ussd_cancel":
		d.app.Go(ui.CapTelUSSD, ui.USSDArgs{Cancel: true}, nil, func(error) {})
		d.ussdState = ""
		d.app.Pop()
	case e.Action == "dial":
		d.dial(e.Fields["number"])
	case e.Action == "refresh":
		d.reload(d.app.Update)
	case e.Action == "call" && strings.HasPrefix(e.Node, "c:"):
		d.dial(strings.TrimPrefix(e.Node, "c:"))
	case e.Action == "call" && strings.HasPrefix(e.Node, "log:"):
		var i int
		fmt.Sscanf(e.Node, "log:%d", &i)
		if i < len(d.log) {
			d.dial(d.log[i].Number)
		}
	case e.Action == "block" || e.Action == "unblock":
		var i int
		if _, err := fmt.Sscanf(e.Node, "log:%d", &i); err != nil || i >= len(d.log) {
			return
		}
		number := d.log[i].Number
		if err := d.app.Call(ui.CapBlockSet, ui.BlockSetArgs{Number: number, On: e.Action == "block"}, &d.blocks); err != nil {
			d.app.Toast(tr.T("Failed: %v", err))
		} else if e.Action == "block" {
			d.app.Toast(tr.T("%s blocked", number))
		} else {
			d.app.Toast(tr.T("%s unblocked", number))
		}
		d.reload(d.app.Update)
	case e.Action == "save-number":
		number := d.query
		var i int
		if _, err := fmt.Sscanf(e.Node, "log:%d", &i); err == nil && i < len(d.log) {
			number = d.log[i].Number
		}
		d.saveNumber(strings.TrimSpace(number))
	case e.Screen == "call":
		d.callAction(e.Action)
	}
}

func (d *dialer) popped(screen string) {
	switch screen {
	case "call":
		d.onCall = false
	case screenAdd:
		d.adding, d.addQuery = false, ""
	case "ussd":
		// Leaving a menu the network waits on ends the session.
		if d.ussdState == "input" {
			d.app.Go(ui.CapTelUSSD, ui.USSDArgs{Cancel: true}, nil, func(error) {})
		}
		d.ussdState = ""
	}
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	d := newDialer(app)
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
	app.OnPopped = d.popped
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
			d.link(value)
		}
	}
	app.OnNotif = func(id, action string) {
		if d.inCall() {
			d.showCall()
			return
		}
		d.showLog = true
		d.reload(app.Reset)
	}
	app.OnAction = d.action
	// The call timer only runs while the call screen is up.
	go func() {
		for range time.Tick(time.Second) {
			app.Post(func() {
				if c := d.shown(); d.onCall && c != nil && (c.State == "active" || c.State == "held") {
					app.Update(d.callScreen())
				}
			})
		}
	}()
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}
