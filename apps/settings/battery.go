package main

import (
	"fmt"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// The Battery page: the charge, and the CPU each app used over the last
// day, background first (the Shell counts it: shell/cpustats.go).

type batteryInfo struct {
	Level     int  `json:"level"`
	Charging  bool `json:"charging"`
	Simulated bool `json:"simulated"`
}

func (p *app) batterySummary() string {
	if p.s.Battery.Charging {
		return tr.T("%d%% · charging", p.s.Battery.Level)
	}
	return fmt.Sprintf("%d%%", p.s.Battery.Level)
}

func (p *app) batteryItem() ui.Node {
	return ui.Node{Kind: ui.KindItem, ID: "battery", Title: tr.T("Battery"), Subtitle: p.batterySummary(), Actions: []ui.Action{sdk.Open(tr.T("Open"))}}
}

// cpuLabel is a CPU time for people: seconds, minutes, hours.
func cpuLabel(ms int64) string {
	s := ms / 1000
	switch {
	case s < 1:
		return tr.T("under 1 s")
	case s < 60:
		return tr.T("%d s", s)
	case s < 3600:
		return tr.T("%d min %d s", s/60, s%60)
	}
	return tr.T("%d h %d min", s/3600, s%3600/60)
}

func (p *app) batteryScreen() ui.Screen {
	s := ui.Screen{ID: "battery", Title: tr.T("Battery"), Actions: []ui.Action{{ID: "refresh", Label: tr.T("Refresh"), Key: "r"}}}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindText, Style: ui.StyleTitle, Text: p.batterySummary()})
	if p.s.Battery.Simulated {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("This machine has no battery: the level is simulated")))
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindSection, Text: tr.T("CPU by app, last 24 hours")})
	for _, e := range p.s.Energy {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "e:" + e.App, Title: e.Name, Trailing: cpuLabel(e.BackgroundMS),
			Subtitle: tr.T("Background %s · foreground %s", cpuLabel(e.BackgroundMS), cpuLabel(e.ForegroundMS))})
	}
	if len(p.s.Energy) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No app has used the CPU yet")))
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Counted every 30 seconds. An app that is not in a call and not playing sound but uses more than 5 percent of a core in the background over 10 minutes gets a notification, and you can stop it from there")))
	return s
}

func (p *app) batteryAction(e sdk.ActionEvent) bool {
	switch {
	case e.Screen == "root" && e.Node == "battery":
		p.push(p.batteryScreen())
	case e.Screen == "battery" && e.Action == "refresh":
		p.a.Go(ui.CapSettingsGet, nil, &p.s, func(err error) {
			if err != nil {
				p.a.Toast(err.Error())
			}
			p.refresh()
		})
	default:
		return false
	}
	return true
}
