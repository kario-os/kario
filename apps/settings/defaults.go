package main

import (
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Apps › Default apps (32-messaging.md §8): the app that holds
// the sms role, the system's Messages unless the user picks another app
// with the sms permission; the system asks before giving it to one from
// outside the system image.

type rolesInfo struct {
	Roles struct {
		SMS struct {
			App     string `json:"app"`
			Choices []struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				System bool   `json:"system"`
			} `json:"choices"`
		} `json:"sms"`
	} `json:"roles"`
}

func (p *app) smsAppName() string {
	for _, c := range p.s.Roles.SMS.Choices {
		if c.ID == p.s.Roles.SMS.App {
			return c.Name
		}
	}
	return tr.T("None")
}

func (p *app) defaultsItem() ui.Node {
	return ui.Node{Kind: ui.KindItem, ID: "defaults", Title: tr.T("Default apps"), Subtitle: tr.T("SMS: %s", p.smsAppName()),
		Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
}

func (p *app) defaultsScreen() ui.Screen {
	s := ui.Screen{ID: "defaults", Title: tr.T("Default apps"), Body: []ui.Node{sdk.Section(tr.T("SMS app"))}}
	for _, c := range p.s.Roles.SMS.Choices {
		n := ui.Node{Kind: ui.KindItem, ID: "sms:" + c.ID, Title: c.Name, Actions: []ui.Action{{ID: "pick", Label: tr.T("Choose"), Primary: true}}}
		if c.ID == p.s.Roles.SMS.App {
			n.Trailing = "✓"
		}
		if !c.System {
			n.Subtitle = tr.T("Not from the system")
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("The default SMS app reads, sends, deletes and blocks messages without asking, and shows the notifications of new ones. Other apps with the SMS permission ask before each message they send.")))
	return s
}

func (p *app) defaultsAction(e sdk.ActionEvent) bool {
	switch {
	case onMenu(e.Screen) && e.Node == "defaults":
		p.push(p.defaultsScreen())
	case e.Screen == "defaults" && e.Action == "pick":
		id := e.Node[len("sms:"):]
		p.a.Go(ui.CapSettingsSet, ui.SettingArgs{Key: ui.SettingSMSApp, Text: id}, &p.s, func(err error) {
			if err != nil && !ui.Cancelled(err) {
				p.a.Toast(err.Error())
			}
			p.a.Update(p.defaultsScreen())
			p.updateMenu()
		})
	default:
		return false
	}
	return true
}
