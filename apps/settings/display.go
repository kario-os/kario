package main

import (
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Display and brightness › Refresh rate (08-display-adaptive-
// refresh.md): automatic, the standard 60 Hz, or always the panel's
// highest, as phones offer it; the row is there only where the panel has
// more than one rate.

// displayInfo is settings.get's "display": phone-displayd's panel, its
// rates, the rate now and why, and the setting (its index in
// ui.RefreshRates).
type displayInfo struct {
	Display struct {
		Up      bool   `json:"up"`
		Panel   string `json:"panel"`
		Rates   []int  `json:"rates"`
		Hz      int    `json:"hz"`
		Reason  string `json:"reason"`
		Setting int    `json:"refresh_rate"`
	} `json:"display"`
}

// refreshStandard is the rate the standard choice fixes, as phone-displayd
// picks it: the highest at or under ui.StandardHz, or the lowest.
func (p *app) refreshStandard() int {
	r := p.s.Display.Rates
	best := 0
	for _, hz := range r {
		if hz <= ui.StandardHz {
			best = hz
		}
	}
	if best == 0 && len(r) > 0 {
		best = r[0]
	}
	return best
}

func (p *app) refreshHigh() int {
	r := p.s.Display.Rates
	if len(r) == 0 {
		return 0
	}
	return r[len(r)-1]
}

func (p *app) refreshName(i int) string {
	switch ui.RefreshRates[i] {
	case ui.RefreshStandard:
		return tr.T("Standard %d Hz", p.refreshStandard())
	case ui.RefreshHigh:
		return tr.T("High %d Hz", p.refreshHigh())
	}
	return tr.T("Automatic")
}

func (p *app) refreshHint(i int) string {
	d := p.s.Display
	switch d.Reason {
	case "cap:battery":
		return tr.T("Battery low: at most %d Hz now", d.Hz)
	case "cap:thermal":
		return tr.T("The phone is warm: at most %d Hz now", d.Hz)
	}
	switch ui.RefreshRates[i] {
	case ui.RefreshStandard:
		return tr.T("Always %d Hz; the battery lasts longer", p.refreshStandard())
	case ui.RefreshHigh:
		return tr.T("Always %d Hz; uses more battery", p.refreshHigh())
	}
	return tr.T("%d Hz while the screen scrolls or animates, lower when it is still", p.refreshHigh())
}

func (p *app) refreshItem() (ui.Node, bool) {
	d := p.s.Display
	if len(d.Rates) < 2 {
		return ui.Node{}, false
	}
	i := min(max(d.Setting, 0), len(ui.RefreshRates)-1)
	return ui.Node{Kind: ui.KindItem, ID: ui.SettingRefreshRate, Title: tr.T("Refresh rate"), Subtitle: p.refreshHint(i), Trailing: p.refreshName(i),
		Actions: []ui.Action{{ID: "next", Label: tr.T("Next"), Primary: true, Key: "right"}, {ID: "prev", Label: tr.T("Previous"), Key: "left"}}}, true
}

// refreshAction steps through the choices, kept at once as the motion
// setting is.
func (p *app) refreshAction(e sdk.ActionEvent) bool {
	if !onMenu(e.Screen) || e.Node != ui.SettingRefreshRate || e.Action != "next" && e.Action != "prev" || len(p.s.Display.Rates) < 2 {
		return false
	}
	n := len(ui.RefreshRates)
	i := min(max(p.s.Display.Setting, 0), n-1)
	if e.Action == "next" {
		i = (i + 1) % n
	} else {
		i = (i + n - 1) % n
	}
	p.s.Display.Setting = i
	p.set(ui.SettingRefreshRate, float64(i))
	return true
}
