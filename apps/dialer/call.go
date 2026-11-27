package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// The call screen (19-core-apps.md §5). It shows at once when a call is
// dialled, before the modem has answered the dial, and stays the top page
// while a call goes on: Back and the right soft key put it in the
// background (ui.ActionHide, done by the system), never off the stack.
// Digits on it are keypad tones; a second call starts from its "New call"
// page, or from digits typed on the standby screen, which open that page.

// pages is what the dialer asks of the system: sdk.App, a fake in tests.
type pages interface {
	Push(ui.Screen)
	Replace(ui.Screen)
	Update(ui.Screen)
	Reset(ui.Screen)
	Pop()
	Toast(string)
	Call(capName string, args, out any) error
	Go(capName string, args, out any, done func(error))
}

const (
	dtmfKeys   = "0123456789*#"
	dtmfPrefix = "dtmf:"
	// screenAdd is the second call's number page, over the call screen.
	screenAdd = "add"
)

var stateLabel = map[string]string{"alerting": "Ringing…", "incoming": "Incoming call", "active": "In call", "held": "On hold", "ended": "Call ended"}

// rank orders the calls for the call screen: one being placed or answered,
// then the connected one, then a held one.
var rank = map[string]int{"held": 1, "active": 2, "dialing": 3, "alerting": 3, "incoming": 3}

func (d *dialer) inCall() bool { return d.pending != nil || len(d.calls) > 0 }

// shown is the call the call screen is about (nil without one).
func (d *dialer) shown() *ui.TelCall {
	if d.pending != nil {
		return d.pending
	}
	var best *ui.TelCall
	for _, c := range d.calls {
		if best == nil || rank[c.State] > rank[best.State] || rank[c.State] == rank[best.State] && c.ID > best.ID {
			c := c
			best = &c
		}
	}
	return best
}

// others are the calls beside the shown one.
func (d *dialer) others() []ui.TelCall {
	s := d.shown()
	var out []ui.TelCall
	for _, c := range d.calls {
		if s == nil || c.ID != s.ID {
			out = append(out, c)
		}
	}
	return out
}

func (d *dialer) held() *ui.TelCall {
	for _, c := range d.others() {
		if c.State == "held" {
			return &c
		}
	}
	return nil
}

// secondBlocked says why no other call can be dialled now, "" when one can
// (telephonyd then holds the connected call).
func (d *dialer) secondBlocked() string {
	if d.pending != nil {
		return tr.T("The call is still connecting")
	}
	connected := 0
	for _, c := range d.calls {
		switch c.State {
		case "dialing", "alerting", "incoming":
			return tr.T("The call is still connecting")
		case "active", "held":
			if c.Emergency {
				return tr.T("An emergency call is in progress")
			}
			connected++
		}
	}
	if connected >= 2 {
		return tr.T("Two calls already")
	}
	return ""
}

func who(c ui.TelCall) string {
	if c.Name != "" {
		return c.Name
	}
	return c.Number
}

func (d *dialer) nameOf(number string) string {
	for _, c := range d.contacts {
		for _, n := range ui.NumbersOf(c) {
			if ui.NumberKey(n.Number) == ui.NumberKey(number) {
				return c.Name
			}
		}
	}
	return ""
}

// status is a call's state as the call screen words it.
func status(c ui.TelCall, now int64) string {
	s := tr.T(stateLabel[c.State])
	switch c.State {
	case "dialing":
		s = tr.T("Calling %s…", c.Number)
	case "incoming":
		s = tr.T("Answering…")
	case "active", "held":
		if c.Connected > 0 {
			s += " " + clock(now-c.Connected)
		}
	}
	return s
}

func (d *dialer) callScreen() ui.Screen {
	c := d.shown()
	if c == nil {
		c = &ui.TelCall{State: "ended"}
	}
	now := time.Now().Unix()
	connected := c.ID != "" && (c.State == "active" || c.State == "held")
	s := ui.Screen{ID: "call", Title: tr.T("Phone call"), Right: ui.ActionHide, Focus: "hangup", Actions: []ui.Action{
		{ID: ui.ActionHangup, Label: tr.T("Hang up")},
	}}
	// Letters that type a digit with Fn on a full keyboard (w e r s d f z x
	// c, q a) are left to the keypad tones: no shortcut takes them.
	if connected {
		s.Actions = append(s.Actions,
			ui.Action{ID: "mute", Label: map[bool]string{false: tr.T("Mute"), true: tr.T("Unmute")}[c.Muted], Key: "m"},
			ui.Action{ID: "hold", Label: map[bool]string{false: tr.T("Hold"), true: tr.T("Resume call")}[c.State == "held"], Key: "h"})
	}
	s.Actions = append(s.Actions, ui.Action{ID: "speaker", Label: map[bool]string{false: tr.T("Speaker"), true: tr.T("Speaker off")}[d.speaker], Key: "l"})
	if d.held() != nil && c.State == "active" {
		s.Actions = append(s.Actions, ui.Action{ID: "swap", Label: tr.T("Swap calls"), Key: "o"})
	}
	if d.secondBlocked() == "" {
		s.Actions = append(s.Actions, ui.Action{ID: "new", Label: tr.T("New call"), Key: "n"})
	}
	s.Actions = append(s.Actions, ui.Action{ID: ui.ActionHide, Label: tr.T("Minimize")})
	// The keypad's tones into the call: a voice menu's choices (10086, a bank).
	if c.ID != "" && c.State == "active" {
		for _, k := range dtmfKeys {
			s.Actions = append(s.Actions, ui.Action{ID: dtmfPrefix + string(k), Label: string(k), Key: string(k), Hidden: true})
		}
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleBig, Text: who(*c)})
	if c.Name != "" {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleDim, Text: c.Number})
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleAccent, Text: status(*c, now)})
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
	if c.DTMF != "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Keys sent: %s", c.DTMF)))
	}
	for _, o := range d.others() {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Also: %s, %s", who(o), status(o, now))))
	}
	s.Body = append(s.Body, sdk.Button("hangup", tr.T("Hang up"), ui.ActionHangup))
	for _, l := range strings.Split(d.hint, "\n") {
		if l != "" {
			s.Body = append(s.Body, sdk.Styled(ui.StyleAccent, l))
		}
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Back: the call goes on in the background; the call key brings it back")))
	return s
}

// addScreen is the second call's number page, over the call screen: the
// main page's search, and the way back.
func (d *dialer) addScreen() ui.Screen {
	s := d.numberScreen(screenAdd, d.addQuery)
	s.Title = tr.T("New call")
	note := sdk.Styled(ui.StyleDim, tr.T("Back to the call"))
	if c := d.shown(); c != nil {
		note = sdk.Styled(ui.StyleDim, tr.T("In a call with %s, held when you call · Back returns to it", who(*c)))
	}
	s.Body = append([]ui.Node{note}, s.Body...)
	return s
}

// showCall brings the call screen to the top, over the main page.
func (d *dialer) showCall() {
	if d.adding {
		d.adding = false
		d.app.Pop()
	}
	if d.onCall {
		d.app.Update(d.callScreen())
		return
	}
	d.onCall = true
	d.app.Push(d.callScreen())
}

// openAdd puts the second call's number page over the call screen, with
// digits already typed; when no other call can go out it says why and
// stays on the call.
func (d *dialer) openAdd(digits string) {
	if why := d.secondBlocked(); why != "" {
		d.showCall()
		d.app.Toast(why)
		return
	}
	d.addQuery = digits
	if !d.onCall {
		d.onCall = true
		d.app.Push(d.callScreen())
	}
	if d.adding {
		d.app.Update(d.addScreen())
		return
	}
	d.adding = true
	d.app.Push(d.addScreen())
}

// setHint takes the system's word on how a call is hung up on this phone
// and whether touch works now (ui.EventCallHint).
func (d *dialer) setHint(hint string) {
	if hint == d.hint {
		return
	}
	d.hint = hint
	if d.onCall {
		d.app.Update(d.callScreen())
	}
}

// settle redraws after a call changed; with none left the call screen
// goes and the main page is back. It goes before the lists reload: the end
// key pressed meanwhile would hang up a call that is over instead of going
// to the standby screen.
func (d *dialer) settle() {
	if !d.inCall() {
		if d.onCall || d.adding {
			d.onCall, d.adding, d.speaker = false, false, false
			d.query, d.addQuery = "", ""
			d.app.Reset(d.mainScreen())
			d.reload(d.app.Update)
		}
		return
	}
	if d.onCall {
		d.app.Update(d.callScreen())
	}
	if d.adding {
		d.app.Update(d.addScreen())
	}
}

// dial places a call and shows it at once: the modem may take its time
// to answer the dial (and a slow one used to leave the number page up,
// as if the phone had frozen).
func (d *dialer) dial(number string) {
	number = strings.TrimSpace(number)
	if ussdCode.MatchString(number) && len(number) >= 3 {
		d.ussd(number)
		return
	}
	if number == "" {
		if d.inCall() {
			d.showCall()
			return
		}
		if len(d.log) > 0 {
			d.showLog = true
			d.reload(d.app.Reset)
			return
		}
		d.app.Toast(tr.T("Enter a number"))
		return
	}
	if why := d.secondBlocked(); why != "" {
		d.app.Toast(why)
		return
	}
	d.dialSeq++
	seq := d.dialSeq
	d.pending = &ui.TelCall{Number: number, Name: d.nameOf(number), Dir: "out", State: "dialing"}
	d.query, d.addQuery = "", ""
	d.inflight++
	d.showCall()
	var res map[string]string
	d.app.Go(ui.CapTelDial, ui.DialArgs{Number: number}, &res, func(err error) { d.dialed(seq, number, res["call_id"], err) })
}

// dialed takes telephonyd's answer to a dial: the call's id, or why it did
// not go out. Events for it may have come first (d.early).
func (d *dialer) dialed(seq int, number, id string, err error) {
	d.inflight--
	early, seen := d.early[id]
	if d.inflight == 0 {
		d.early = map[string]ui.TelCall{}
	}
	if d.pending != nil && seq == d.dialSeq {
		d.pending = nil
	}
	cancelled := d.cancelled[seq]
	delete(d.cancelled, seq)
	toast := ""
	switch {
	case err != nil && cancelled:
	case err != nil:
		toast = tr.T("Cannot call: %v", err)
	case cancelled:
		d.dropped[id] = true
		if !seen || early.State != "ended" {
			d.app.Go(ui.CapTelHangup, ui.CallIDArgs{CallID: id}, nil, func(error) {})
		}
	case seen && early.State == "ended":
		toast = d.endMessage(early)
	case seen:
		d.calls[id] = early
	default:
		d.calls[id] = ui.TelCall{ID: id, Number: number, Name: d.nameOf(number), Dir: "out", State: "dialing"}
	}
	d.settle()
	if toast != "" {
		d.app.Toast(toast)
	}
}

// callEvent follows telephonyd's word on a call. A call ringing in is the
// system's until answered; any other the phone has is tracked, whoever
// placed it, so the call screen can show all of them.
func (d *dialer) callEvent(c ui.TelCall) {
	if c.State == "ended" {
		defer delete(d.answering, c.ID)
	}
	if c.State == "incoming" && !d.answering[c.ID] || d.dropped[c.ID] {
		if c.State == "ended" {
			delete(d.dropped, c.ID)
		}
		return
	}
	if _, known := d.calls[c.ID]; !known {
		switch {
		case d.inflight > 0 && c.Dir == "out":
			d.early[c.ID] = c
		case c.State != "ended":
			d.calls[c.ID] = c
			d.settle()
		}
		return
	}
	if c.State == "ended" {
		delete(d.calls, c.ID)
		msg := d.endMessage(c)
		d.settle()
		d.app.Toast(msg)
		return
	}
	if c.Name == "" {
		c.Name = d.calls[c.ID].Name
	}
	d.calls[c.ID] = c
	d.settle()
}

// endMessage says how a call ended.
func (d *dialer) endMessage(c ui.TelCall) string {
	dur := int64(0)
	if c.Connected > 0 {
		dur = c.Ended - c.Connected
	}
	switch {
	case c.Reason == "busy":
		return tr.T("The other party is busy")
	case c.Reason == "failed" && c.IMS != "":
		return tr.T("Call failed: VoLTE is not registered")
	case c.Reason == "failed" && c.Error != "":
		return tr.T("Call failed: %s", c.Error)
	case c.Reason == "failed":
		return tr.T("The call failed")
	case len(d.calls) > 0:
		return tr.T("Call with %s ended %s", who(c), clock(dur))
	case c.Reason == "remote":
		return tr.T("The other party hung up %s", clock(dur))
	}
	return tr.T("Call ended %s", clock(dur))
}

// open shows the call screen for a call that is up (answered on the
// system's incoming screen, placed by another app, or brought back from
// the background), and learns the calls telephonyd has.
func (d *dialer) open(id string) {
	d.answering[id] = true
	if _, ok := d.calls[id]; ok {
		d.showCall()
	}
	var calls []ui.TelCall
	d.app.Go(ui.CapTelCalls, nil, &calls, func(err error) {
		if err != nil {
			return
		}
		d.sync(calls)
		if d.inCall() {
			d.showCall()
		} else {
			d.settle()
		}
	})
}

// sync takes telephonyd's list of the calls up.
func (d *dialer) sync(calls []ui.TelCall) {
	up, listed := map[string]bool{}, map[string]bool{}
	for _, c := range calls {
		listed[c.ID] = true
	}
	for id := range d.answering {
		if !listed[id] {
			delete(d.answering, id)
		}
	}
	for _, c := range calls {
		_, known := d.calls[c.ID]
		switch {
		case c.State == "incoming" && !d.answering[c.ID] || c.State == "ended" || d.dropped[c.ID]:
		case !known && d.inflight > 0 && c.Dir == "out":
			d.early[c.ID] = c
		default:
			up[c.ID] = true
			d.calls[c.ID] = c
		}
	}
	for id := range d.calls {
		if !up[id] {
			delete(d.calls, id)
		}
	}
}

func (d *dialer) callAction(action string) {
	c := d.shown()
	if c == nil {
		return
	}
	id := c.ID
	toast := func(err error) {
		if err != nil {
			d.app.Toast(err.Error())
		}
	}
	if tones, ok := strings.CutPrefix(action, dtmfPrefix); ok {
		if id != "" {
			d.app.Go(ui.CapTelDTMF, ui.DTMFArgs{CallID: id, Tones: tones}, nil, toast)
		}
		return
	}
	switch action {
	case ui.ActionHangup:
		if id == "" {
			// Not answered by the modem yet: it is hung up once it is.
			d.cancelled[d.dialSeq] = true
			d.pending = nil
			d.settle()
			d.app.Toast(tr.T("Call ended %s", clock(0)))
			return
		}
		d.app.Go(ui.CapTelHangup, ui.CallIDArgs{CallID: id}, nil, toast)
	case "mute":
		d.app.Go(ui.CapTelMute, ui.CallIDArgs{CallID: id, On: !c.Muted}, nil, toast)
	case "hold":
		d.app.Go(ui.CapTelHold, ui.CallIDArgs{CallID: id, On: c.State != "held"}, nil, toast)
	case "swap":
		if h := d.held(); h != nil {
			d.app.Go(ui.CapTelHold, ui.CallIDArgs{CallID: h.ID}, nil, toast)
		}
	case "new":
		d.openAdd("")
	case "speaker":
		d.speaker = !d.speaker
		route := map[bool]string{false: ui.RouteEarpiece, true: ui.RouteSpeaker}[d.speaker]
		d.app.Go(ui.CapAudioRoute, ui.AudioRouteArgs{Route: route}, nil, func(error) {})
		d.app.Update(d.callScreen())
	}
}

func clock(sec int64) string { return fmt.Sprintf("%02d:%02d", sec/60, sec%60) }
