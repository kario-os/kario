// SMS Lite, a sample third-party SMS app: not in the system image, it is
// packaged, signed and installed through the store, and asks for the sms
// permission. Until the user makes it the default SMS app (Settings ›
// Apps › Default apps) it reads conversations and each message it sends
// waits for the user's yes; as the default it sends without asking and
// may delete (32-messaging.md §8). Without the contacts permission it
// still fills in a recipient through the system's contact picker
// (contacts.pick), and it shows how an app signing in gets a verification
// code the user agrees to hand over (sms.otp). A skeleton.
package main

import (
	"log"
	"strings"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	var threads []ui.SMSThread
	// to: the number the picker gave (the input's value); code: the last
	// verification code handed over.
	var to, code string
	screen := func() ui.Screen {
		threads = nil
		_ = app.Call(ui.CapSMSThreads, ui.SMSListArgs{}, &threads)
		s := ui.Screen{ID: "main", Title: tr.T("SMS Lite"), Actions: []ui.Action{
			{ID: "pick", Label: tr.T("Choose a contact"), Key: "c"},
			{ID: "otp", Label: tr.T("Wait for a verification code"), Key: "o"},
		}, Body: []ui.Node{
			{Kind: ui.KindInput, ID: "to", Placeholder: tr.T("Number"), Keyboard: ui.KeyboardPhone, Value: to},
			{Kind: ui.KindInput, ID: "text", Placeholder: tr.T("Message"), Submit: "send"},
		}}
		if code != "" {
			s.Body = append(s.Body, sdk.Styled(ui.StyleAccent, tr.T("Verification code: %s", code)))
		}
		for _, t := range threads {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "t:" + t.Peer, Title: firstNonEmpty(t.Name, t.Peer), Subtitle: t.Last.Text,
				Actions: []ui.Action{{ID: "delete", Label: tr.T("Delete conversation"), Key: "d", Group: ui.GroupDanger}}})
		}
		if len(threads) == 0 {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No messages")))
		}
		return s
	}
	app.OnStart = func(string) { app.Reset(screen()) }
	app.OnEvent = func(name, _ string) {
		if name == ui.EventSMS {
			app.Update(screen())
		}
	}
	app.OnAction = func(e sdk.ActionEvent) {
		switch e.Action {
		case "send":
			to, text := strings.TrimSpace(e.Fields["to"]), strings.TrimSpace(e.Fields["text"])
			if to == "" || text == "" {
				return
			}
			app.Go(ui.CapSMSSend, ui.SMSSendArgs{To: to, Text: text}, nil, func(err error) {
				if err != nil {
					app.Toast(tr.T("Not sent: %v", err))
					return
				}
				app.Toast(tr.T("Sent"))
				app.Update(screen())
			})
		case "pick":
			var c ui.Contact
			app.Go(ui.CapContactsPick, nil, &c, func(err error) {
				if err != nil {
					if !ui.Cancelled(err) {
						app.Toast(err.Error())
					}
					return
				}
				to = c.Number
				app.Update(screen())
			})
		case "otp":
			var otp ui.SMSOTP
			app.Toast(tr.T("Waiting for a message with a code…"))
			app.Go(ui.CapSMSOTP, ui.SMSOTPArgs{Seconds: 120}, &otp, func(err error) {
				if err != nil {
					if !ui.Cancelled(err) {
						app.Toast(err.Error())
					}
					return
				}
				code = otp.Code
				app.Update(screen())
			})
		case "delete":
			if err := app.Call(ui.CapSMSDelete, ui.SMSDeleteArgs{Peer: strings.TrimPrefix(e.Node, "t:")}, nil); err != nil {
				app.Toast(tr.T("Not deleted: %v", err))
				return
			}
			app.Update(screen())
		}
	}
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
