package main

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Adding a bank card: the bank texts a code to the phone number on the
// card and the wallet gets it through sms.otp, which shows the user the
// message and asks before handing over the code; the app never reads the
// user's messages.

// bankCode is the code the mock bank sends for a card. A real bank picks
// it at random and keeps it on its server; deriving it from the card lets
// the selftest, standing in for the bank's SMS gateway, send the right one.
func bankCode(card string) string {
	h := fnv.New32a()
	h.Write([]byte(card))
	return fmt.Sprintf("%06d", h.Sum32()%1000000)
}

// cardNumber is the digits of a bank card number as typed (spaces
// allowed), or "" if it is not 16 to 19 of them.
func cardNumber(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if len(s) < 16 || len(s) > 19 {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}

func (w *wallet) cardsScreen() ui.Screen {
	s := ui.Screen{ID: "cards", Title: tr.T("Bank cards"), Actions: []ui.Action{{ID: "add", Label: tr.T("Add a bank card"), Key: "a"}}}
	for _, c := range w.cards {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: c, Icon: tr.T("K"), Title: tr.T("Card ending %s", c[len(c)-4:]), Subtitle: tr.T("Verified by a code the bank sent by SMS")})
	}
	if len(w.cards) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No bank cards yet")))
	}
	return s
}

func (w *wallet) addCardScreen() ui.Screen {
	body := []ui.Node{
		{Kind: ui.KindInput, ID: "card", Placeholder: tr.T("Card number"), Keyboard: "number", Value: w.adding},
		sdk.Button("otp", tr.T("Get the SMS code"), "otp"),
		sdk.Styled(ui.StyleDim, tr.T("The bank texts a code to the phone number the card was opened with. The system asks you before giving Feiqian that code, and Feiqian sees nothing else of your messages.")),
	}
	if w.otpWait {
		body = append(body, sdk.Styled(ui.StyleWarn, tr.T("Waiting for the bank's message…")))
	}
	return ui.Screen{ID: "addcard", Title: tr.T("Add a bank card"), Body: body}
}

func (w *wallet) cardAction(e sdk.ActionEvent) bool {
	switch {
	case e.Screen == "home" && e.Node == "cards" && e.Action == ui.ActionOpen:
		w.app.Push(w.cardsScreen())
	case e.Screen == "cards" && e.Action == "add":
		w.adding, w.otpWait = "", false
		w.app.Push(w.addCardScreen())
	case e.Screen == "addcard" && e.Action == "otp":
		card := cardNumber(e.Fields["card"])
		if card == "" {
			w.app.Toast(tr.T("A card number has 16 to 19 digits"))
			return true
		}
		w.adding, w.otpWait = card, true
		w.app.Update(w.addCardScreen())
		var otp ui.SMSOTP
		w.app.Go(ui.CapSMSOTP, ui.SMSOTPArgs{Seconds: ui.SMSOTPWait}, &otp, func(err error) {
			w.otpWait = false
			switch {
			case ui.Cancelled(err):
				w.app.Toast(tr.T("The code was not given; the card was not added"))
			case err != nil:
				w.app.Toast(err.Error())
			case otp.Code != bankCode(card):
				w.app.Toast(tr.T("The bank did not accept the code"))
			default:
				w.cards = append(w.cards, card)
				w.app.Pop()
				w.app.Update(w.cardsScreen())
				w.app.Update(w.homeScreen())
				w.app.Toast(tr.T("Card ending %s added", card[len(card)-4:]))
				return
			}
			w.app.Update(w.addCardScreen())
		})
	default:
		return false
	}
	return true
}
