package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Digital wellbeing: Screen time (each app's time on the screen
// today and this week, daily limits, downtime) and Minor mode (19-core-apps.md).
// The system keeps and enforces both; in minor mode it asks for the
// parental PIN on its own screen before any change, so this app never sees
// the PIN. Minor mode › Remote management links a parent's account, which
// then sets the same rules from the account server's web page, and
// confirms there a reset of a parental PIN the parent forgot.

type stApp struct {
	App   string `json:"app"`
	Name  string `json:"name"`
	Today int64  `json:"today_ms"`
	Week  int64  `json:"week_ms"`
	Limit int    `json:"limit"`
	Fixed bool   `json:"fixed"`
}

type minorContact struct {
	Name   string `json:"name"`
	Number string `json:"number"`
}

// wellbeingInfo is settings.get's "screen_time" and "minor".
type wellbeingInfo struct {
	ScreenTime struct {
		Days []struct {
			Date  string `json:"date"`
			Label string `json:"label"`
			MS    int64  `json:"ms"`
		} `json:"days"`
		Apps         []stApp `json:"apps"`
		LimitChoices []int   `json:"limit_choices"`
		Downtime     struct {
			On        bool     `json:"on"`
			Start     int      `json:"start"`
			End       int      `json:"end"`
			StartText string   `json:"start_text"`
			EndText   string   `json:"end_text"`
			Apps      []string `json:"apps"`
			Active    bool     `json:"active"`
			Until     string   `json:"until"`
		} `json:"downtime"`
		// Guarded: minor mode is on, every change takes the parental PIN.
		Guarded bool `json:"guarded"`
	} `json:"screen_time"`
	Minor struct {
		On   bool `json:"on"`
		Apps []struct {
			App     string `json:"app"`
			Name    string `json:"name"`
			Allowed bool   `json:"allowed"`
			Fixed   bool   `json:"fixed"`
		} `json:"apps"`
		Contacts []minorContact `json:"contacts"`
		// Others are the contacts' numbers not allowed.
		Others []minorContact `json:"others"`
		Remote remoteInfo     `json:"remote"`
	} `json:"minor"`
}

// remoteInfo is a parent's account managing minor mode: State none,
// pairing (Code on show, URL in the QR code, until Until), claimed (Parent
// waits to be allowed) or bound; Available: the phone has an account
// server to be managed through. Reset: a forgotten parental PIN's reset
// asked of the parent (at ResetAt) or ready (the new PIN to be set before
// ResetUntil).
type remoteInfo struct {
	Available  bool   `json:"available"`
	State      string `json:"state"`
	Code       string `json:"code"`
	URL        string `json:"url"`
	Until      string `json:"until"`
	Parent     string `json:"parent"`
	Since      string `json:"since"`
	Version    int    `json:"version"`
	Applied    string `json:"applied"`
	Local      string `json:"local"`
	Located    string `json:"located"`
	Error      string `json:"error"`
	Reset      string `json:"reset"`
	ResetAt    string `json:"reset_at"`
	ResetUntil string `json:"reset_until"`
}

// remotePolling: the remote management page reloads while it is open, as
// the parent's side moves on.
var remotePolling bool

// dtStep: Earlier and Later move downtime's start and end by this many
// minutes.
const dtStep = 30

func durLabel(ms int64) string {
	m := int(ms / 60000)
	switch {
	case m < 1 && ms > 0:
		return tr.T("Under a minute")
	case m < 60:
		return tr.T("%d min", m)
	case m%60 == 0:
		return tr.T("%d h", m/60)
	}
	return tr.T("%d h %d min", m/60, m%60)
}

func limitLabel(m int) string {
	if m <= 0 {
		return tr.T("No limit")
	}
	return tr.T("%s a day", durLabel(int64(m)*60000))
}

func (p *app) stToday() int64 {
	var sum int64
	for _, a := range p.s.ScreenTime.Apps {
		sum += a.Today
	}
	return sum
}

func (p *app) wellbeingRow(id string) ui.Node {
	open := []ui.Action{sdk.Open(tr.T("Open"))}
	if id == "screentime" {
		return ui.Node{Kind: ui.KindItem, ID: id, Title: tr.T("Screen time"), Subtitle: tr.T("Today %s", durLabel(p.stToday())), Actions: open}
	}
	m := p.s.Minor
	sub := tr.T("For a child: only the contacts and apps a parent allows")
	switch {
	case m.On && m.Remote.State == "bound":
		sub = tr.T("Managed by %s", m.Remote.Parent)
	case m.On:
		sub = tr.T("%d contacts · %d apps allowed", len(m.Contacts), p.minorAllowed())
	}
	return ui.Node{Kind: ui.KindItem, ID: id, Title: tr.T("Minor mode"), Subtitle: sub, Trailing: onOff(m.On), Actions: open}
}

func (p *app) minorAllowed() int {
	n := 0
	for _, a := range p.s.Minor.Apps {
		if a.Allowed {
			n++
		}
	}
	return n
}

func (p *app) guardNote() []ui.Node {
	if !p.s.ScreenTime.Guarded {
		return nil
	}
	return []ui.Node{sdk.Styled(ui.StyleDim, tr.T("Minor mode is on: changes need the parental PIN, asked for on the system's screen."))}
}

func (p *app) screenTimeScreen() ui.Screen {
	st := p.s.ScreenTime
	var week int64
	for _, d := range st.Days {
		week += d.MS
	}
	dt := tr.T("Off")
	switch {
	case st.Downtime.Active:
		dt = tr.T("Now, until %s", st.Downtime.Until)
	case st.Downtime.On:
		dt = tr.T("%s–%s", st.Downtime.StartText, st.Downtime.EndText)
	}
	s := ui.Screen{ID: "screentime", Title: tr.T("Screen time"), Body: []ui.Node{
		{Kind: ui.KindItem, ID: "st-today", Title: tr.T("Today"), Trailing: durLabel(p.stToday())},
		{Kind: ui.KindItem, ID: "st-week", Title: tr.T("This week"), Trailing: durLabel(week),
			Subtitle: tr.T("Each day and each app"), Actions: []ui.Action{sdk.Open(tr.T("Open"))}},
		{Kind: ui.KindItem, ID: "downtime", Title: tr.T("Downtime"), Subtitle: dt, Actions: []ui.Action{sdk.Open(tr.T("Open"))}},
		sdk.Section(tr.T("Apps today")),
	}}
	for _, a := range st.Apps {
		n := ui.Node{Kind: ui.KindItem, ID: "st:" + a.App, Title: a.Name, Trailing: durLabel(a.Today),
			Subtitle: tr.T("This week %s · %s", durLabel(a.Week), limitLabel(a.Limit))}
		if a.Fixed {
			n.Subtitle = tr.T("This week %s · always allowed", durLabel(a.Week))
		} else {
			n.Actions = []ui.Action{{ID: ui.ActionOpen, Label: tr.T("Daily limit"), Primary: true}}
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, p.guardNote()...)
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Time counts while an app is on the screen. Past its limit an app does not open: the system says so and offers more time. Calls always work.")))
	return s
}

func (p *app) weekScreen() ui.Screen {
	st := p.s.ScreenTime
	s := ui.Screen{ID: "st-week", Title: tr.T("This week")}
	for i := len(st.Days) - 1; i >= 0; i-- {
		d := st.Days[i]
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "day:" + d.Date, Title: d.Label, Trailing: durLabel(d.MS)})
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Apps this week")))
	for _, a := range st.Apps {
		if a.Week > 0 {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "wk:" + a.App, Title: a.Name, Trailing: durLabel(a.Week)})
		}
	}
	return s
}

func (p *app) stApp(id string) (stApp, bool) {
	for _, a := range p.s.ScreenTime.Apps {
		if a.App == id {
			return a, true
		}
	}
	return stApp{}, false
}

func (p *app) limitScreen(id string) ui.Screen {
	a, _ := p.stApp(id)
	s := ui.Screen{ID: "stl:" + id, Title: tr.T("Daily limit · %s", a.Name)}
	for _, m := range append([]int{0}, p.s.ScreenTime.LimitChoices...) {
		n := ui.Node{Kind: ui.KindItem, ID: fmt.Sprint("lim:", m), Title: limitLabel(m), Actions: []ui.Action{{ID: "choose", Label: tr.T("Choose"), Primary: true}}}
		if m == a.Limit {
			n.Badge = tr.T("Current")
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, p.guardNote()...)
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Used today: %s. Five minutes before the limit the system says so; then the app does not open until tomorrow, unless more time is given.", durLabel(a.Today))))
	return s
}

func (p *app) downtimeScreen() ui.Screen {
	dt := p.s.ScreenTime.Downtime
	step := []ui.Action{{ID: "later", Label: tr.T("Later"), Primary: true, Key: "right"}, {ID: "earlier", Label: tr.T("Earlier"), Key: "left"}}
	now := ui.Node{Kind: ui.KindItem, ID: "dt-now", Title: tr.T("Start downtime now"), Subtitle: tr.T("Until %s", dt.EndText),
		Actions: []ui.Action{{ID: "toggle", Label: tr.T("Start"), Primary: true}}}
	if dt.Active {
		now = ui.Node{Kind: ui.KindItem, ID: "dt-now", Title: tr.T("End downtime now"), Subtitle: tr.T("In force until %s", dt.Until),
			Actions: []ui.Action{{ID: "toggle", Label: tr.T("End"), Primary: true}}}
	}
	s := ui.Screen{ID: "downtime", Title: tr.T("Downtime"), Body: []ui.Node{
		{Kind: ui.KindItem, ID: "dt-on", Title: tr.T("Every day"), Trailing: onOff(dt.On), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}},
		{Kind: ui.KindItem, ID: "dt-start", Title: tr.T("Starts"), Trailing: dt.StartText, Actions: step},
		{Kind: ui.KindItem, ID: "dt-end", Title: tr.T("Ends"), Trailing: dt.EndText, Actions: step},
		now,
		sdk.Section(tr.T("Allowed during downtime")),
	}}
	for _, a := range p.s.ScreenTime.Apps {
		n := ui.Node{Kind: ui.KindItem, ID: "dta:" + a.App, Title: a.Name, Trailing: tr.T("Always")}
		if !a.Fixed {
			on := slices.Contains(dt.Apps, a.App)
			n.Trailing = map[bool]string{true: tr.T("Allowed"), false: tr.T("Not allowed")}[on]
			n.Actions = []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, p.guardNote()...)
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("During downtime only the apps allowed then work; calls always do, emergency calls too. ← earlier · → later, in steps of 30 minutes.")))
	return s
}

func (p *app) minorScreen() ui.Screen {
	m := p.s.Minor
	s := ui.Screen{ID: "minor", Title: tr.T("Minor mode"), Body: []ui.Node{
		{Kind: ui.KindItem, ID: "minor-on", Title: tr.T("Minor mode"), Trailing: onOff(m.On),
			Subtitle: tr.T("Setting a parental PIN turns it on; turning it off takes the PIN"),
			Actions:  []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}},
	}}
	if m.On {
		open := []ui.Action{sdk.Open(tr.T("Open"))}
		s.Body = append(s.Body,
			ui.Node{Kind: ui.KindItem, ID: "minor-apps", Title: tr.T("Allowed apps"), Subtitle: tr.T("%d of %d apps", p.minorAllowed(), len(m.Apps)), Actions: open},
			ui.Node{Kind: ui.KindItem, ID: "minor-contacts", Title: tr.T("Allowed contacts"), Subtitle: tr.T("Calls and messages: %d numbers", len(m.Contacts)), Actions: open},
			ui.Node{Kind: ui.KindItem, ID: "minor-time", Title: tr.T("Screen time limits"), Subtitle: tr.T("Daily limits and downtime"), Actions: open})
		if r := m.Remote; r.Available || (r.State != "" && r.State != "none") {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "minor-remote", Title: tr.T("Remote management"), Subtitle: remoteLine(r), Actions: open})
		}
		s.Body = append(s.Body,
			ui.Node{Kind: ui.KindItem, ID: "minor-pin", Title: tr.T("Change the parental PIN"), Actions: []ui.Action{{ID: "change", Label: tr.T("Change"), Primary: true}}})
		if n, ok := forgotRow(m.Remote); ok {
			s.Body = append(s.Body, n)
		}
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("For a child's phone: calls and messages only with the contacts allowed (emergency numbers always work), only the apps allowed on the menu and running, screen time limits. Installing apps, developer mode, unlocking the device, the date and time and these settings need the parental PIN, which the system's key service keeps.")))
	return s
}

func (p *app) minorAppsScreen() ui.Screen {
	s := ui.Screen{ID: "minor-apps", Title: tr.T("Allowed apps")}
	for _, a := range p.s.Minor.Apps {
		n := ui.Node{Kind: ui.KindItem, ID: "ma:" + a.App, Title: a.Name, Trailing: tr.T("Always")}
		if !a.Fixed {
			n.Trailing = map[bool]string{true: tr.T("Allowed"), false: tr.T("Hidden")}[a.Allowed]
			n.Actions = []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}
		}
		s.Body = append(s.Body, n)
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("A hidden app is not on the menu or in the search, and does not run. An app a parent installs with the PIN is allowed.")))
	return s
}

func (p *app) minorContactsScreen() ui.Screen {
	m := p.s.Minor
	s := ui.Screen{ID: "minor-contacts", Title: tr.T("Allowed contacts"), Actions: []ui.Action{{ID: "minor-add", Label: tr.T("Allow a number"), Key: "a"}},
		Body: []ui.Node{sdk.Section(tr.T("Allowed (%d)", len(m.Contacts)))}}
	for _, x := range m.Contacts {
		title := x.Name
		if title == "" {
			title = ui.NumberText(x.Number)
		}
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "mc:" + x.Number, Title: title, Subtitle: ui.NumberText(x.Number),
			Actions: []ui.Action{{ID: "remove", Label: tr.T("Remove"), Primary: true}}})
	}
	if len(m.Others) > 0 {
		s.Body = append(s.Body, sdk.Section(tr.T("Other contacts")))
	}
	for _, x := range m.Others {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "mo:" + x.Number, Title: x.Name, Subtitle: ui.NumberText(x.Number),
			Actions: []ui.Action{{ID: "allow", Label: tr.T("Allow"), Primary: true}}})
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Calls and messages from other numbers are blocked as blocked numbers are, and calls and messages to them do not go out. Emergency numbers always work.")))
	return s
}

// forgotRow is minor mode's "Forgot the parental PIN", only while a
// parent's account is linked (it confirms the reset): ask, withdraw the
// request, or set the new PIN once the parent confirmed.
func forgotRow(r remoteInfo) (ui.Node, bool) {
	if r.State != "bound" {
		return ui.Node{}, false
	}
	n := ui.Node{Kind: ui.KindItem, ID: "minor-forgot", Title: tr.T("Forgot the parental PIN"),
		Subtitle: tr.T("%s confirms on their page; then set a new one here", r.Parent),
		Actions:  []ui.Action{{ID: "forgot", Label: tr.T("Ask"), Primary: true}}}
	switch r.Reset {
	case "asked":
		n.Subtitle = tr.T("Asked at %s; waiting for %s to confirm on their page", r.ResetAt, r.Parent)
		n.Actions = []ui.Action{{ID: "withdraw", Label: tr.T("Withdraw"), Primary: true}}
	case "ready":
		n.Title, n.Subtitle = tr.T("Set a new parental PIN"), tr.T("Set it before %s: %s confirmed the reset", r.ResetUntil, r.Parent)
		n.Actions = []ui.Action{{ID: "reset", Label: tr.T("Set"), Primary: true}}
	}
	return n, true
}

// remoteLine is the remote management row's subtitle.
func remoteLine(r remoteInfo) string {
	switch r.State {
	case "bound":
		return tr.T("Managed by %s", r.Parent)
	case "claimed":
		return tr.T("%s is waiting to be allowed", r.Parent)
	case "pairing":
		return tr.T("Code %s, waiting for the parent", codeText(r.Code))
	}
	return tr.T("A parent manages this phone from their own, in WeChat")
}

// codeText is a pairing code in two groups of four.
func codeText(code string) string {
	if len(code) == 8 {
		return code[:4] + " " + code[4:]
	}
	return code
}

// remoteShares is what the parent's page shows of the phone, as the
// phone tells its user before binding and while bound.
func remoteShares() []ui.Node {
	return []ui.Node{sdk.Section(tr.T("What the parent sees")),
		sdk.Text(tr.T("• Today's time on the screen of each app, and the apps on this phone")),
		sdk.Text(tr.T("• Minor mode's allowed contacts and apps, daily limits and downtime")),
		sdk.Text(tr.T("• Where the phone is, only when the parent asks; the phone says so each time")),
		sdk.Text(tr.T("• Nothing else: no messages, calls, photos, browsing or what is in apps")),
		sdk.Styled(ui.StyleDim, tr.T("The account server keeps only what the parent's page shows; a location at most a day. Unlinking deletes it."))}
}

func (p *app) remoteScreen() ui.Screen {
	r := p.s.Minor.Remote
	s := ui.Screen{ID: "minor-remote", Title: tr.T("Remote management")}
	action := func(id, title, sub, label string) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: id, Title: title, Subtitle: sub, Actions: []ui.Action{{ID: id, Label: label, Primary: true}}}
	}
	switch r.State {
	case "pairing":
		page, _, _ := strings.Cut(r.URL, "#")
		s.Body = []ui.Node{{Kind: ui.KindImage, Src: "qr:" + r.URL, Secure: true},
			sdk.Styled(ui.StyleBig, codeText(r.Code)),
			sdk.Text(tr.T("On the parent's phone: scan this with WeChat, or open %s, sign in and enter the code. It holds until %s.", page, r.Until)),
			sdk.Styled(ui.StyleDim, tr.T("Waiting for the parent…")),
			action("remote-cancel", tr.T("Cancel"), "", tr.T("Cancel"))}
	case "claimed":
		s.Body = []ui.Node{sdk.Styled(ui.StyleBig, r.Parent), sdk.Text(tr.T("%s entered the code and wants to manage minor mode on this phone.", r.Parent))}
		s.Body = append(s.Body, remoteShares()...)
		s.Body = append(s.Body, action("remote-allow", tr.T("Allow"), tr.T("Needs the parental PIN"), tr.T("Allow")),
			action("remote-refuse", tr.T("Refuse"), tr.T("Not this account"), tr.T("Refuse")))
	case "bound":
		rules := tr.T("None yet")
		switch {
		case r.Local != "":
			rules = tr.T("Changed on this phone with the PIN at %s; the parent's page shows it", r.Local)
		case r.Applied != "":
			rules = tr.T("Version %d, applied %s", r.Version, r.Applied)
		}
		s.Body = []ui.Node{sdk.Text(tr.T("Managed by")), sdk.Styled(ui.StyleBig, r.Parent), sdk.Text(tr.T("Since %s", r.Since)),
			{Kind: ui.KindItem, ID: "remote-rules", Title: tr.T("Rules from the parent"), Subtitle: rules}}
		if r.Located != "" {
			s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "remote-located", Title: tr.T("Location last sent"), Trailing: r.Located})
		}
		s.Body = append(s.Body, remoteShares()...)
		s.Body = append(s.Body, action("remote-unlink", tr.T("Unlink"), tr.T("The parent no longer manages this phone; the rules stay. Needs the parental PIN"), tr.T("Unlink")))
	default:
		s.Body = []ui.Node{sdk.Text(tr.T("A parent signs in to the Kario account's web page on their own phone (it opens in WeChat) and sets minor mode's rules from there: allowed contacts and apps, daily limits and downtime."))}
		s.Body = append(s.Body, remoteShares()...)
		s.Body = append(s.Body, action("remote-link", tr.T("Link a parent's account"), tr.T("Shows a code for the parent; needs the parental PIN"), tr.T("Link")))
	}
	if r.Error != "" {
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, r.Error))
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("The account server signs every change and the phone takes only signed ones, each newer than the last. A parent's changes never turn minor mode off: that, the PIN and unlinking stay on this phone.")))
	return s
}

// pollRemote reloads the remote management page every 2 seconds while it
// is open, and minor mode's while a PIN reset waits for the parent.
func (p *app) pollRemote() {
	if remotePolling {
		return
	}
	remotePolling = true
	go func() {
		for range time.Tick(2 * time.Second) {
			p.a.Post(func() {
				if slices.Contains(p.pages, "minor-remote") || slices.Contains(p.pages, "minor") && p.s.Minor.Remote.Reset == "asked" {
					p.a.Go(ui.CapSettingsGet, nil, &p.s, func(error) { p.refreshWellbeing() })
				}
			})
		}
	}()
}

func (p *app) minorAddScreen() ui.Screen {
	return ui.Screen{ID: "minor-add", Title: tr.T("Allow a number"), Focus: "number", Body: []ui.Node{
		{Kind: ui.KindInput, ID: "number", Keyboard: ui.KeyboardPhone, Placeholder: tr.T("Phone number"), Submit: "minor-add-save"},
		sdk.Button("save", tr.T("Allow"), "minor-add-save"),
	}}
}

func (p *app) wellbeingScreens() []ui.Screen {
	return []ui.Screen{p.screenTimeScreen(), p.weekScreen(), p.downtimeScreen(), p.minorScreen(), p.minorAppsScreen(), p.minorContactsScreen(), p.remoteScreen()}
}

func (p *app) refreshWellbeing() {
	for _, s := range p.wellbeingScreens() {
		p.a.Update(s)
	}
	for _, id := range p.pages {
		if a, ok := strings.CutPrefix(id, "stl:"); ok {
			p.a.Update(p.limitScreen(a))
		}
	}
}

// wellbeingScreenByID builds one of these pages by its ID (restored after
// a restart).
func (p *app) wellbeingScreenByID(id string) (ui.Screen, bool) {
	for _, s := range p.wellbeingScreens() {
		if s.ID == id {
			if id == "minor-remote" || id == "minor" {
				p.pollRemote()
			}
			return s, true
		}
	}
	if a, ok := strings.CutPrefix(id, "stl:"); ok {
		return p.limitScreen(a), true
	}
	return ui.Screen{}, false
}

// wbSet changes a setting of these pages; the system may ask for the
// parental PIN first, and the answer waits for it.
func (p *app) wbSet(key string, v float64, text string) {
	p.a.Go(ui.CapSettingsSet, ui.SettingArgs{Key: key, Value: v, Text: text}, &p.s, func(err error) {
		if err != nil && !ui.Cancelled(err) {
			p.a.Toast(err.Error())
		}
		p.refreshWellbeing()
		p.updateMenu()
	})
}

// dayStep moves a time of day by delta minutes, round the clock.
func dayStep(m, delta int) float64 { return float64(((m+delta)%(24*60) + 24*60) % (24 * 60)) }

func (p *app) wellbeingAction(e sdk.ActionEvent) bool {
	st, m := p.s.ScreenTime, p.s.Minor
	switch {
	case onMenu(e.Screen) && e.Node == "screentime", e.Screen == "minor" && e.Node == "minor-time":
		p.push(p.screenTimeScreen())
	case onMenu(e.Screen) && e.Node == "minor":
		p.push(p.minorScreen())
		p.pollRemote()
	case e.Screen == "screentime" && e.Node == "st-week":
		p.push(p.weekScreen())
	case e.Screen == "screentime" && e.Node == "downtime":
		p.push(p.downtimeScreen())
	case e.Screen == "screentime" && strings.HasPrefix(e.Node, "st:"):
		if a, ok := p.stApp(strings.TrimPrefix(e.Node, "st:")); ok && !a.Fixed {
			p.push(p.limitScreen(a.App))
		}
	case strings.HasPrefix(e.Screen, "stl:") && e.Action == "choose":
		var lim int
		fmt.Sscanf(e.Node, "lim:%d", &lim)
		p.wbSet(ui.SettingScreenLimit, float64(lim), strings.TrimPrefix(e.Screen, "stl:"))
	case e.Screen == "downtime" && e.Node == "dt-on":
		p.wbSet(ui.SettingDowntime, onOffValue(st.Downtime.On), "")
	case e.Screen == "downtime" && (e.Node == "dt-start" || e.Node == "dt-end"):
		key, cur := ui.SettingDowntimeStart, st.Downtime.Start
		if e.Node == "dt-end" {
			key, cur = ui.SettingDowntimeEnd, st.Downtime.End
		}
		delta := dtStep
		if e.Action == "earlier" {
			delta = -dtStep
		}
		p.wbSet(key, dayStep(cur, delta), "")
	case e.Screen == "downtime" && e.Node == "dt-now":
		p.wbSet(ui.SettingDowntimeNow, onOffValue(st.Downtime.Active), "")
	case e.Screen == "downtime" && strings.HasPrefix(e.Node, "dta:"):
		id := strings.TrimPrefix(e.Node, "dta:")
		p.wbSet(ui.SettingDowntimeApp, onOffValue(slices.Contains(st.Downtime.Apps, id)), id)
	case e.Screen == "minor" && e.Node == "minor-on":
		p.wbSet(ui.SettingMinorMode, onOffValue(m.On), "")
	case e.Screen == "minor" && e.Node == "minor-apps":
		p.push(p.minorAppsScreen())
	case e.Screen == "minor" && e.Node == "minor-contacts":
		p.push(p.minorContactsScreen())
	case e.Screen == "minor" && e.Node == "minor-pin":
		p.wbSet(ui.SettingParentPIN, 1, "")
	case e.Screen == "minor" && e.Node == "minor-forgot":
		p.wbSet(ui.SettingParentReset, onOffValue(m.Remote.Reset == "asked"), "")
	case e.Screen == "minor" && e.Node == "minor-remote":
		p.push(p.remoteScreen())
		p.pollRemote()
	case e.Screen == "minor-remote" && e.Node == "remote-link":
		p.wbSet(ui.SettingParentLink, 1, "")
	case e.Screen == "minor-remote" && (e.Node == "remote-cancel" || e.Node == "remote-unlink"):
		p.wbSet(ui.SettingParentLink, 0, "")
	case e.Screen == "minor-remote" && (e.Node == "remote-allow" || e.Node == "remote-refuse"):
		p.wbSet(ui.SettingParentConfirm, onOffValue(e.Node == "remote-refuse"), "")
	case e.Screen == "minor-apps" && strings.HasPrefix(e.Node, "ma:"):
		id := strings.TrimPrefix(e.Node, "ma:")
		for _, a := range m.Apps {
			if a.App == id && !a.Fixed {
				p.wbSet(ui.SettingMinorApp, onOffValue(a.Allowed), id)
			}
		}
	case e.Screen == "minor-contacts" && strings.HasPrefix(e.Node, "mc:"):
		p.wbSet(ui.SettingMinorContact, 0, strings.TrimPrefix(e.Node, "mc:"))
	case e.Screen == "minor-contacts" && strings.HasPrefix(e.Node, "mo:"):
		p.wbSet(ui.SettingMinorContact, 1, strings.TrimPrefix(e.Node, "mo:"))
	case e.Action == "minor-add":
		p.a.Push(p.minorAddScreen())
	case e.Action == "minor-add-save":
		if n := strings.TrimSpace(e.Fields["number"]); n != "" {
			p.a.Pop()
			p.wbSet(ui.SettingMinorContact, 1, n)
		}
	default:
		return false
	}
	return true
}
