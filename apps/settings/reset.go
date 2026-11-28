package main

import (
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › More settings › Factory reset (31-lock-and-biometrics.md §11):
// what goes and what stays, the anti-theft lock, and the button. The system
// asks for the PIN and the last confirmation on its own screens
// (settings.set factory_reset), then restarts the phone to erase it.

// resetInfo is settings.get's "reset".
type resetInfo struct {
	Reset struct {
		Supported bool `json:"supported"`
		Minor     bool `json:"minor"`
		PIN       bool `json:"pin"`
		// Theft: the anti-theft lock is on, for this account (masked).
		Theft *struct {
			Email string `json:"email"`
		} `json:"theft"`
	} `json:"reset"`
}

func (p *app) resetScreen() ui.Screen {
	r := p.s.Reset
	s := ui.Screen{ID: "reset", Title: tr.T("Factory reset")}
	s.Body = append(s.Body, sdk.Section(tr.T("What is erased")))
	for _, l := range []string{
		tr.T("Every app's data and settings, and the apps installed from the store"),
		tr.T("Accounts, and the passwords the system keeps for them"),
		tr.T("Contacts, messages and the call log"),
		tr.T("Photos, videos, music and the files in Files"),
		tr.T("Wi-Fi networks and Bluetooth pairings"),
		tr.T("The lock-screen PIN and the fingerprints"),
		tr.T("Minor mode, with its parental PIN"),
	} {
		s.Body = append(s.Body, sdk.Text("· "+l))
	}
	s.Body = append(s.Body, sdk.Section(tr.T("What stays")))
	s.Body = append(s.Body, sdk.Text("· "+tr.T("The system and the updates installed")),
		sdk.Text("· "+tr.T("The phone's identity from the factory, so services still know it is a genuine phone")))
	if !r.Supported {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("This device cannot be reset here: it has no place that the erasing does not reach.")))
		return s
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Anti-theft lock")))
	if t := r.Theft; t != nil {
		sub := tr.T("Off: after the reset the phone does not ask for %s", t.Email)
		if p.keepLock {
			sub = tr.T("On: after the reset the phone asks for %s before it can be used", t.Email)
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "keeplock", Title: tr.T("Keep the anti-theft lock"), Subtitle: sub,
			Trailing: onOff(p.keepLock), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}})
	} else {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No Kario account is signed in: after the reset no account is asked for.")))
	}
	how := tr.T("No lock-screen PIN is set: only a last confirmation is asked for, so whoever holds the phone unlocked can erase it.")
	switch {
	case r.Minor:
		how = tr.T("Minor mode is on: a parent's PIN is needed, then a last confirmation.")
	case r.PIN:
		how = tr.T("The lock-screen PIN is needed, then a last confirmation.")
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, how),
		ui.Node{Kind: ui.KindItem, ID: "erase", Title: tr.T("Erase everything"), Subtitle: tr.T("The phone restarts and erases its data"),
			Actions: []ui.Action{{ID: "erase", Label: tr.T("Erase everything"), Primary: true}}})
	return s
}

// resetAction handles the reset page's actions; false for the rest.
func (p *app) resetAction(e sdk.ActionEvent) bool {
	switch {
	case onMenu(e.Screen) && e.Node == "reset":
		p.keepLock = false
		p.push(p.resetScreen())
	case e.Screen == "reset" && e.Node == "keeplock":
		p.keepLock = !p.keepLock
		p.a.Update(p.resetScreen())
	case e.Screen == "reset" && e.Node == "erase":
		v := 1.0
		if p.keepLock && p.s.Reset.Theft != nil {
			v = ui.ResetKeepLock
		}
		p.a.Go(ui.CapSettingsSet, ui.SettingArgs{Key: ui.SettingFactoryReset, Value: v}, &p.s, func(err error) {
			if err != nil && !ui.Cancelled(err) {
				p.a.Toast(err.Error())
			}
			p.a.Update(p.resetScreen())
		})
	default:
		return false
	}
	return true
}
