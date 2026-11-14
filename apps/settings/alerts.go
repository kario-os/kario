package main

import (
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Emergency alerts (32-messaging.md): which levels of cell
// broadcast alerts the phone shows. The highest level cannot be switched
// off; tests are off until switched on. The alerts received are in
// Messages.

type alertsInfo struct {
	Alerts []struct {
		Level  string `json:"level"`
		Key    string `json:"key"`
		Name   string `json:"name"`
		On     bool   `json:"on"`
		Locked bool   `json:"locked"`
	} `json:"alerts"`
}

func (p *app) alertsItem() ui.Node {
	on := 0
	for _, a := range p.s.Alerts {
		if a.On {
			on++
		}
	}
	return ui.Node{Kind: ui.KindItem, ID: "alerts", Title: tr.T("Emergency alerts"), Subtitle: tr.T("%d of %d levels shown", on, len(p.s.Alerts)),
		Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
}

func (p *app) alertsScreen() ui.Screen {
	s := ui.Screen{ID: "alerts", Title: tr.T("Emergency alerts")}
	for _, a := range p.s.Alerts {
		n := ui.Node{Kind: ui.KindItem, ID: "alert:" + a.Level, Title: a.Name, Trailing: onOff(a.On)}
		if a.Locked {
			n.Subtitle = tr.T("Always shown")
		} else {
			n.Actions = []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Alerts the network sends every phone in an area. They show over everything, the lock screen too, with their own sound, even in silent mode. The alerts received are in Messages.")))
	return s
}

func (p *app) alertsAction(e sdk.ActionEvent) bool {
	switch {
	case e.Screen == "root" && e.Node == "alerts":
		p.push(p.alertsScreen())
	case e.Screen == "alerts" && e.Action == "toggle":
		for _, a := range p.s.Alerts {
			if "alert:"+a.Level != e.Node || a.Locked {
				continue
			}
			p.a.Go(ui.CapSettingsSet, ui.SettingArgs{Key: a.Key, Value: onOffValue(a.On)}, &p.s, func(err error) {
				if err != nil {
					p.a.Toast(err.Error())
				}
				p.a.Update(p.alertsScreen())
				p.a.Update(p.root())
			})
		}
	default:
		return false
	}
	return true
}
