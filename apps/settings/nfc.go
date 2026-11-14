package main

import (
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › NFC (30-nfc.md): NFC on or off (nfc.state, nfc.set) and a
// line that opens the card pack (apps/cards), where the cards are.

// nfcState is phone-nfcd's answer; tried: asked once for the root page's
// line, which shows it from the next refresh on (an update of its own could
// land between the user's keys; a phone without nfcd answers late).
var nfcState struct {
	ui.NFCState
	loaded, loading, tried bool
}

func (p *app) loadNFC(then func()) {
	nfcState.loading, nfcState.tried = true, true
	p.a.Go(ui.CapNFCState, nil, &nfcState.NFCState, func(err error) {
		nfcState.loaded, nfcState.loading = err == nil, false
		if then != nil {
			then()
		}
	})
}

func (p *app) nfcSummary() string {
	st := nfcState
	switch {
	case !st.loaded && st.loading:
		return ""
	case !st.loaded:
		return tr.T("No NFC")
	case !st.Enabled:
		return tr.T("Off")
	}
	return tr.T("On · %d cards", len(st.Cards))
}

// nfcItem is the NFC page's line on the root page.
func (p *app) nfcItem() ui.Node {
	if !nfcState.tried {
		p.loadNFC(nil)
	}
	return ui.Node{Kind: ui.KindItem, ID: "nfc", Title: "NFC", Subtitle: p.nfcSummary(), Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
}

func (p *app) nfcScreen() ui.Screen {
	st := nfcState
	s := ui.Screen{ID: "nfc", Title: "NFC"}
	if !st.loaded {
		s.Body = []ui.Node{sdk.Styled(ui.StyleDim, tr.T("This phone has no NFC"))}
		return s
	}
	s.Body = append(s.Body,
		ui.Node{Kind: ui.KindItem, ID: "nfc_power", Title: "NFC", Subtitle: tr.T("Pay, ride and open doors by holding the phone to a reader"),
			Trailing: onOff(st.Enabled), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}},
		ui.Node{Kind: ui.KindItem, ID: "nfc_cards", Title: tr.T("Cards"), Subtitle: tr.T("Door, transit and payment cards: add, order, the express transit card, the payment app"),
			Trailing: tr.T("%d cards", len(st.Cards)), Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
	if st.Simulated {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Simulated controller: this machine has no NFC chip")))
	} else {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Controller: %s", st.Controller)))
	}
	return s
}

// nfcAction handles the NFC page; false for the rest.
func (p *app) nfcAction(e sdk.ActionEvent) bool {
	switch {
	case e.Screen == "root" && e.Node == "nfc":
		p.loadNFC(func() { p.push(p.nfcScreen()) })
	case e.Screen == "nfc" && e.Node == "nfc_power":
		p.a.Go(ui.CapNFCSet, ui.NFCSetArgs{Key: ui.NFCSetEnabled, On: !nfcState.Enabled}, &nfcState.NFCState, func(err error) {
			if err != nil {
				p.a.Toast(err.Error())
			}
			p.a.Update(p.nfcScreen())
			p.a.Update(p.root())
		})
	case e.Screen == "nfc" && e.Node == "nfc_cards":
		p.a.Go(ui.CapNFCSet, ui.NFCSetArgs{Key: ui.NFCSetOpen}, nil, func(err error) {
			if err != nil {
				p.a.Toast(err.Error())
			}
		})
	default:
		return false
	}
	return true
}
