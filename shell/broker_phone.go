package shell

import (
	"encoding/json"
	"errors"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Telephony, messages, contacts and alarm capabilities. Telephony goes to
// phone-telephonyd, messages to phone-smsd (messaging.go); contacts and
// alarms are kept by the shell itself.

// peerLabel is a number as the confirmations show it: with the contact's
// name when there is one.
func (c *Core) peerLabel(number string) string {
	if n := c.contactName(number); n != "" {
		return tr.T("%s (%s)", n, number)
	}
	return number
}

// mmiIMEI is the code every phone answers with its IMEI, from the handset
// itself: nothing goes to the network.
const mmiIMEI = "*#06#"

var telMethods = map[string]string{
	ui.CapTelAnswer: "answer", ui.CapTelHangup: "hangup", ui.CapTelHold: "hold", ui.CapTelMute: "mute",
}

func (b *broker) dispatchPhone(a *App, m ui.Msg, reply func(any, error)) {
	c := b.c
	switch m.Cap {
	case ui.CapTelDial:
		var args ui.DialArgs
		_ = json.Unmarshal(m.Args, &args)
		if c.simLocked() {
			c.askSimPin()
			reply(nil, errors.New(tr.T("The SIM card is locked; enter its PIN first")))
			return
		}
		if c.settings.Airplane && !emergencyNumbers[args.Number] {
			reply(nil, errors.New(tr.T("Airplane mode is on; turn it off to call")))
			return
		}
		dial := func() {
			var res map[string]string
			b.call(svc.Telephonyd, svc.TelDial, args, &res, func(err error) {
				if err == nil && !a.M.Has(ui.PermPhone) {
					// The call screen belongs to the dialer, whoever asked.
					// Launch hands over the call: the start argument of a new
					// dialer, an "open" event for a running one.
					if d := c.appByID(appDialer); d != nil {
						c.Launch(d, "call:"+res["call_id"])
					}
				}
				reply(res, err)
			})
		}
		// A store app never calls on its own, phone permission or not:
		// premium-rate numbers are the oldest phone fraud.
		if a.M.Has(ui.PermPhone) && !a.M.installed {
			dial()
			return
		}
		c.pushOverlay(&confirmOverlay{text: tr.T("“%s” asks to call %s", a.M.Name, c.peerLabel(args.Number)), onYes: dial,
			onNo: func() { reply(nil, errors.New("cancelled by user")) }})
	case ui.CapTelUSSD:
		var args ui.USSDArgs
		_ = json.Unmarshal(m.Args, &args)
		var res ui.USSDReply
		done := func(err error) { reply(res, err) }
		switch {
		case a.M.installed:
			reply(nil, errors.New(tr.T("Only the system's phone app can send USSD codes")))
		case args.Code == mmiIMEI:
			res = ui.USSDReply{State: "done", Text: tr.T("IMEI: unknown")}
			if c.tel != nil && c.tel.IMEI != "" {
				res.Text = tr.T("IMEI: %s", c.tel.IMEI)
			}
			reply(res, nil)
		case c.simLocked() && !args.Cancel:
			c.askSimPin()
			reply(nil, errors.New(tr.T("The SIM card is locked; enter its PIN first")))
		case args.Cancel:
			b.call(svc.Telephonyd, svc.TelUSSDCancel, struct{}{}, nil, func(err error) { reply(map[string]bool{"ok": err == nil}, err) })
		case args.Code != "":
			b.call(svc.Telephonyd, svc.TelUSSDInitiate, map[string]string{"code": args.Code}, &res, done)
		default:
			b.call(svc.Telephonyd, svc.TelUSSDRespond, map[string]string{"text": args.Text}, &res, done)
		}
	case ui.CapTelAnswer, ui.CapTelHangup, ui.CapTelHold, ui.CapTelMute:
		var args ui.CallIDArgs
		_ = json.Unmarshal(m.Args, &args)
		b.call(svc.Telephonyd, telMethods[m.Cap], args, nil, func(err error) { reply(map[string]bool{"ok": err == nil}, err) })
	case ui.CapTelCalls:
		var calls []ui.TelCall
		b.call(svc.Telephonyd, svc.TelCalls, nil, &calls, func(err error) {
			for i := range calls {
				calls[i].Name = c.contactName(calls[i].Number)
			}
			reply(calls, err)
		})
	case ui.CapTelLog:
		var log []ui.TelLogEntry
		b.call(svc.Telephonyd, svc.TelLog, nil, &log, func(err error) {
			for i := range log {
				log[i].Name = c.contactName(log[i].Number)
			}
			reply(log, err)
		})
	case ui.CapSMSSend, ui.CapSMSList, ui.CapSMSRead, ui.CapSMSThreads, ui.CapSMSAlerts, ui.CapSMSDelete, ui.CapSMSBlock, ui.CapSMSBlocked:
		b.dispatchSMS(a, m, reply)
	case ui.CapContacts:
		reply(c.contacts(), nil)
	case ui.CapAlarmSet:
		var args ui.Alarm
		_ = json.Unmarshal(m.Args, &args)
		reply(c.alarms.set(a.M.ID, args, c.now()))
	case ui.CapAlarmCancel:
		var args ui.Alarm
		_ = json.Unmarshal(m.Args, &args)
		if !c.alarms.cancel(a.M.ID, args.ID) {
			reply(nil, errors.New("no such alarm"))
			return
		}
		reply(map[string]bool{"ok": true}, nil)
	case ui.CapAlarmList:
		reply(c.alarms.of(a.M.ID), nil)
	}
}
