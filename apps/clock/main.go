// Clock: the time, and alarms kept by the system (alarm.* capabilities) so
// they ring on time even when this app is frozen or not running.
package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

type clockApp struct {
	app    *sdk.App
	alarms []ui.Alarm
	onMain bool
}

// weekday is the date line by day of the week, given the formatted date.
var weekday = []string{"Sunday, %s", "Monday, %s", "Tuesday, %s", "Wednesday, %s", "Thursday, %s", "Friday, %s", "Saturday, %s"}

// normalize accepts "0730", "730", "7:30", "07:30", and "+10" for ten
// minutes from now; a full-width colon counts as a colon.
func normalize(s string) (string, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\uff1a", ":"))
	var h, m int
	switch {
	case strings.HasPrefix(s, "+"):
		var mins int
		if _, err := fmt.Sscanf(s[1:], "%d", &mins); err != nil || mins < 1 || mins > 24*60 {
			return "", false
		}
		return time.Now().Add(time.Duration(mins) * time.Minute).Format("15:04"), true
	case strings.Contains(s, ":"):
		if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
			return "", false
		}
	case len(s) == 3 || len(s) == 4:
		if _, err := fmt.Sscanf(s[:len(s)-2]+" "+s[len(s)-2:], "%d %d", &h, &m); err != nil {
			return "", false
		}
	default:
		return "", false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", h, m), true
}

func (c *clockApp) mainScreen() ui.Screen {
	now := time.Now()
	s := ui.Screen{ID: "main", Title: tr.T("Alarms"), Right: "new", Actions: []ui.Action{{ID: "new", Label: tr.T("New alarm"), Key: "n"}}}
	s.Body = append(s.Body,
		ui.Node{Kind: ui.KindText, Style: ui.StyleBig, Text: now.Format("15:04")},
		ui.Node{Kind: ui.KindText, Style: ui.StyleDim, Text: tr.T(weekday[now.Weekday()], now.Format(tr.T("January 2, 2006")))},
		sdk.Section(tr.T("Alarms")))
	for _, a := range c.alarms {
		state, toggle := tr.T("Off"), tr.T("Turn on")
		if a.On {
			state, toggle = tr.T("On"), tr.T("Turn off")
		}
		repeat := tr.T("Once")
		if a.Daily {
			repeat = tr.T("Daily")
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "a:" + a.ID, Icon: tr.T("A"), Title: a.Time,
			Subtitle: strings.TrimSpace(a.Label + " · " + repeat), Trailing: state,
			Actions: []ui.Action{
				{ID: "toggle", Label: toggle, Primary: true},
				{ID: "delete", Label: tr.T("Delete"), Key: "d", Group: ui.GroupDanger},
			}})
	}
	if len(c.alarms) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No alarms. Press the right soft key to add one.")))
	}
	return s
}

func (c *clockApp) reload(show func(ui.Screen)) {
	_ = c.app.Call(ui.CapAlarmList, nil, &c.alarms)
	show(c.mainScreen())
}

func (c *clockApp) find(node string) *ui.Alarm {
	for i := range c.alarms {
		if "a:"+c.alarms[i].ID == node {
			return &c.alarms[i]
		}
	}
	return nil
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	c := &clockApp{app: app}
	app.OnStart = func(string) { c.onMain = true; c.reload(app.Reset) }
	app.OnLifecycle = func(state string) {
		if state == ui.StateForeground && c.onMain {
			c.reload(app.Update)
		}
	}
	app.OnPopped = func(string) { c.onMain = true; c.reload(app.Update) }
	app.OnEvent = func(name, _ string) {
		if name == ui.EventAlarm && c.onMain {
			c.reload(app.Update)
		}
	}
	save := func(e sdk.ActionEvent, daily bool) {
		t, ok := normalize(e.Fields["time"])
		if !ok {
			app.Toast(tr.T("Time format: 07:30, 0730 or +10"))
			return
		}
		a := ui.Alarm{Time: t, Label: strings.TrimSpace(e.Fields["label"]), Daily: daily, On: true}
		app.Go(ui.CapAlarmSet, a, &a, func(err error) {
			if err != nil {
				app.Toast(err.Error())
				return
			}
			app.Toast(tr.T("Alarm set for %s", t))
			app.Pop()
		})
	}
	app.OnAction = func(e sdk.ActionEvent) {
		switch e.Action {
		case "new":
			c.onMain = false
			app.Push(ui.Screen{ID: "new", Title: tr.T("New alarm"), Body: []ui.Node{
				{Kind: ui.KindInput, ID: "time", Placeholder: tr.T("Time: 07:30, 0730, or +10 (in 10 minutes)"), Keyboard: "number"},
				{Kind: ui.KindInput, ID: "label", Placeholder: tr.T("Label (optional)")},
				sdk.Button("once", tr.T("Save (once)"), "save"),
				sdk.Button("daily", tr.T("Save (daily)"), "save-daily"),
			}})
		case "save":
			save(e, false)
		case "save-daily":
			save(e, true)
		case "toggle":
			if a := c.find(e.Node); a != nil {
				a.On = !a.On
				app.Go(ui.CapAlarmSet, *a, nil, func(error) { c.reload(app.Update) })
			}
		case "delete":
			if a := c.find(e.Node); a != nil {
				app.Go(ui.CapAlarmCancel, ui.Alarm{ID: a.ID}, nil, func(error) { c.reload(app.Update) })
			}
		}
	}
	go func() {
		for range time.Tick(20 * time.Second) {
			app.Post(func() {
				if c.onMain {
					app.Update(c.mainScreen())
				}
			})
		}
	}()
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}
