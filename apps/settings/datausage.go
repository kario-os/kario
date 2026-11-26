package main

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Mobile network › Data usage (settings.get's data_usage): this
// billing cycle's mobile data against the plan, what each app used of it,
// most first, and Wi-Fi apart; the plan's page: the monthly allowance,
// the day the cycle starts, the warning and the hard limit (settings.set's
// data_* keys); Data Saver with every app that uses the network, and each
// app's page: its background data, unrestricted data and its own limit
// (the app_* keys). The system counts, warns and blocks
// (shell/datausage.go, shell/netpolicy.go).

// warnChoices are the warning's shares of the allowance the page steps
// through, 0 for none.
var warnChoices = []int{0, 50, 75, 80, 90}

func (p *app) trafficSummary() string {
	d := p.s.Data
	if d.Plan.Limit > 0 {
		return tr.T("Mobile %s of %s this cycle · Wi-Fi %s", bytesLabel(d.Mobile), bytesLabel(d.Plan.Limit), bytesLabel(d.Wifi))
	}
	return tr.T("Mobile %s this cycle · Wi-Fi %s", bytesLabel(d.Mobile), bytesLabel(d.Wifi))
}

// dayLabel is a date (2006-01-02) by its month and day; last: the day
// before it (a cycle's last day, its end being the next one's first).
func dayLabel(date string, last bool) string {
	t, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	if last {
		t = t.AddDate(0, 0, -1)
	}
	return strings.NewReplacer("{m}", strconv.Itoa(int(t.Month())), "{day}", strconv.Itoa(t.Day())).Replace(tr.T("{m}/{day}"))
}

func lineName(d ui.TrafficUsage, l ui.TrafficLine) string {
	switch l.ID {
	case ui.TrafficLineHotspot:
		return tr.T("Hotspot devices")
	case ui.TrafficLineSystem:
		if d.Source == ui.TrafficSourceLinks {
			return tr.T("Apps and the system")
		}
		return tr.T("System and other")
	case ui.TrafficLineRemoved:
		return tr.T("Removed apps")
	}
	return firstOf(l.Name, l.ID)
}

func planSummary(pl ui.DataPlan) string {
	parts := []string{tr.T("No monthly limit"), tr.T("from day %d", pl.CycleDay)}
	if pl.Limit > 0 {
		parts[0] = tr.T("%s a month", bytesLabel(pl.Limit))
	}
	if pl.Warning > 0 {
		parts = append(parts, tr.T("warning at %s", bytesLabel(pl.Warning)))
	}
	if pl.LimitOff && pl.Limit > 0 {
		parts = append(parts, tr.T("mobile data off at the limit"))
	}
	return strings.Join(parts, " · ")
}

func (p *app) trafficScreen() ui.Screen {
	d := p.s.Data
	s := ui.Screen{ID: "traffic", Title: tr.T("Data usage"), Actions: []ui.Action{{ID: "refresh", Label: tr.T("Refresh"), Key: "r"},
		{ID: "plan", Label: tr.T("Data plan"), Key: "p"}}}
	head := tr.T("Mobile data %s", bytesLabel(d.Mobile))
	if d.Plan.Limit > 0 {
		head = tr.T("Mobile data %s of %s", bytesLabel(d.Mobile), bytesLabel(d.Plan.Limit))
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleTitle, head))
	if d.Plan.Limit > 0 {
		used := min(1, float64(d.Mobile)/float64(d.Plan.Limit))
		s.Body = append(s.Body, sdk.Progress("", used, pct(used)))
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("%s to %s · today %s", dayLabel(d.Start, false), dayLabel(d.End, true), bytesLabel(d.MobileToday))))
	switch {
	case d.Held:
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, tr.T("Mobile data is off: this cycle's limit is used up. Turning it on in Mobile network keeps it on until the cycle ends or the limit is raised.")))
	case d.Override:
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Mobile data stays on past the limit until %s, or until the limit is raised", dayLabel(d.End, true))))
	}
	s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "plan", Title: tr.T("Data plan"), Subtitle: planSummary(d.Plan), Actions: []ui.Action{sdk.Open(tr.T("Open"))}},
		ui.Node{Kind: ui.KindItem, ID: "datasaver", Title: tr.T("Data Saver"), Subtitle: saverSummary(d), Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
	if d.Roaming > 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Roaming abroad this cycle: %s, not counted against the plan", bytesLabel(d.Roaming))))
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Mobile data by app")))
	shown := map[string]bool{}
	for _, l := range d.Lines {
		m, abroad := l.MobileRx+l.MobileTx+l.RoamRx+l.RoamTx, l.RoamRx+l.RoamTx
		if m == 0 {
			continue
		}
		sub := fmt.Sprintf("↓%s ↑%s", bytesLabel(l.MobileRx), bytesLabel(l.MobileTx))
		if abroad > 0 {
			sub += " · " + tr.T("abroad %s", bytesLabel(abroad))
		}
		s.Body = append(s.Body, appRow(d, ui.Node{Kind: ui.KindItem, ID: "m:" + l.ID, Title: lineName(d, l), Trailing: bytesLabel(m), Subtitle: sub}, l.ID))
		shown[l.ID] = true
	}
	for _, ap := range d.Apps {
		if !shown[ap.ID] && (ap.Limit > 0 || ap.BackgroundOff) {
			s.Body = append(s.Body, appRow(d, ui.Node{Kind: ui.KindItem, ID: "m:" + ap.ID, Title: ap.Name, Trailing: bytesLabel(0)}, ap.ID))
			shown[ap.ID] = true
		}
	}
	if len(shown) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Nothing used mobile data this cycle")))
	}
	s.Body = append(s.Body, sdk.Section(tr.T("Wi-Fi this cycle: %s", bytesLabel(d.Wifi))))
	wifi := slices.DeleteFunc(slices.Clone(d.Lines), func(l ui.TrafficLine) bool { return l.WifiRx+l.WifiTx == 0 })
	slices.SortStableFunc(wifi, func(a, b ui.TrafficLine) int { return cmp.Compare(b.WifiRx+b.WifiTx, a.WifiRx+a.WifiTx) })
	for _, l := range wifi {
		n := ui.Node{Kind: ui.KindItem, ID: "w:" + l.ID, Title: lineName(d, l), Trailing: bytesLabel(l.WifiRx + l.WifiTx),
			Subtitle: fmt.Sprintf("↓%s ↑%s", bytesLabel(l.WifiRx), bytesLabel(l.WifiTx))}
		if _, ok := appOf(d, l.ID); ok {
			n.Actions = []ui.Action{sdk.Open(tr.T("Open"))}
		}
		s.Body = append(s.Body, n)
	}
	if len(wifi) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Nothing used Wi-Fi this cycle")))
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Last cycle: mobile data %s", bytesLabel(d.PrevMobile))), sdk.Styled(ui.StyleDim, sourceNote(d.Source)))
	return s
}

func appOf(d ui.TrafficUsage, id string) (ui.TrafficApp, bool) {
	for _, ap := range d.Apps {
		if ap.ID == id {
			return ap, true
		}
	}
	return ui.TrafficApp{ID: id}, false
}

func lineOf(d ui.TrafficUsage, id string) ui.TrafficLine {
	for _, l := range d.Lines {
		if l.ID == id {
			return l
		}
	}
	return ui.TrafficLine{ID: id}
}

// appRow is a line of the page that is an app's: it says the app's own
// rules and opens its page.
func appRow(d ui.TrafficUsage, n ui.Node, id string) ui.Node {
	ap, ok := appOf(d, id)
	if !ok {
		return n
	}
	if note := appNote(d, ap); note != "" {
		n.Subtitle = strings.TrimPrefix(n.Subtitle+" · "+note, " · ")
	}
	n.Actions = []ui.Action{sdk.Open(tr.T("Open"))}
	return n
}

// appNote is an app's own rules in a few words.
func appNote(d ui.TrafficUsage, ap ui.TrafficApp) string {
	var parts []string
	switch {
	case ap.WifiOnly:
		parts = append(parts, tr.T("Wi-Fi only"))
	case ap.Blocked == ui.NetBlockedLimit:
		parts = append(parts, tr.T("limit used up"))
	case ap.Limit > 0:
		parts = append(parts, tr.T("limit %s", bytesLabel(ap.Limit)))
	}
	switch {
	case ap.BackgroundOff:
		parts = append(parts, tr.T("background data off"))
	case d.DataSaver && ap.Unrestricted:
		parts = append(parts, tr.T("unrestricted"))
	case d.DataSaver:
		parts = append(parts, tr.T("held back by Data Saver"))
	}
	return strings.Join(parts, " · ")
}

func saverSummary(d ui.TrafficUsage) string {
	if !d.DataSaver {
		return tr.T("Off")
	}
	n := 0
	for _, ap := range d.Apps {
		if ap.Unrestricted && !ap.BackgroundOff {
			n++
		}
	}
	return tr.T("On · %d with unrestricted data", n)
}

// dataSaverScreen is Data Saver's switch and every app that uses the
// network, each opening its page.
func (p *app) dataSaverScreen() ui.Screen {
	d := p.s.Data
	s := ui.Screen{ID: "datasaver", Title: tr.T("Data Saver"), Body: []ui.Node{
		{Kind: ui.KindItem, ID: "ds_on", Title: tr.T("Data Saver"), Trailing: onOff(d.DataSaver),
			Subtitle: tr.T("Apps use mobile data only while they are on the screen, but those with unrestricted data"),
			Actions:  []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}},
		sdk.Section(tr.T("Apps")),
	}}
	for _, ap := range d.Apps {
		s.Body = append(s.Body, ui.Node{Kind: ui.KindItem, ID: "da:" + ap.ID, Title: firstOf(ap.Name, ap.ID), Subtitle: appNote(d, ap),
			Trailing: bytesLabel(ap.Used), Actions: []ui.Action{sdk.Open(tr.T("Open"))}})
	}
	if len(d.Apps) == 0 {
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("No app uses the network")))
	}
	return s
}

// appDataScreen is an app's data usage this cycle and its own rules:
// background data, unrestricted data (Data Saver) and its limit.
func (p *app) appDataScreen(id string) ui.Screen {
	d := p.s.Data
	ap, _ := appOf(d, id)
	line := lineOf(d, id)
	s := ui.Screen{ID: "appdata:" + id, Title: firstOf(ap.Name, id)}
	m := line.MobileRx + line.MobileTx + line.RoamRx + line.RoamTx
	s.Body = append(s.Body, sdk.Styled(ui.StyleTitle, tr.T("Mobile data %s this cycle", bytesLabel(m))))
	if ap.Limit > 0 {
		used := min(1, float64(ap.Used)/float64(ap.Limit))
		s.Body = append(s.Body, sdk.Progress("", used, pct(used)))
	}
	s.Body = append(s.Body, sdk.Styled(ui.StyleDim, fmt.Sprintf("↓%s ↑%s · ", bytesLabel(line.MobileRx), bytesLabel(line.MobileTx))+
		tr.T("Wi-Fi this cycle: %s", bytesLabel(line.WifiRx+line.WifiTx))))
	switch {
	case ap.WifiOnly:
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, tr.T("Wi-Fi only in App permissions: it uses no mobile data at all")))
	case ap.Blocked == ui.NetBlockedLimit:
		s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, tr.T("Its mobile data is blocked: this cycle's limit is used up, until %s or until the limit is raised", dayLabel(d.End, true))))
	case ap.BackgroundOff || d.DataSaver && !ap.Unrestricted:
		s.Body = append(s.Body, sdk.Styled(ui.StyleDim, tr.T("Mobile data only while it is on the screen: in the background, for its jobs and push messages, Wi-Fi only")))
	}
	bg := tr.T("Mobile data also while the app is not on the screen")
	if ap.BackgroundOff {
		bg = tr.T("Off: mobile data only while the app is on the screen; its jobs that need a network wait for Wi-Fi")
	}
	free := tr.T("Background data even while Data Saver is on")
	if ap.BackgroundOff {
		free = tr.T("Has no effect while its background data is off")
	}
	toggle := []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}
	limit := ui.Node{Kind: ui.KindItem, ID: "ad_limit", Title: tr.T("Mobile data limit"), Trailing: tr.T("Not set"),
		Subtitle: tr.T("A notice at %d%%, then its mobile data is blocked until the cycle ends or the limit is raised", ui.AppDataWarnPercent),
		Actions:  []ui.Action{{ID: "edit", Label: tr.T("Change"), Primary: true}}}
	if ap.Limit > 0 {
		limit.Trailing = bytesLabel(ap.Limit)
	}
	if d.Source == ui.TrafficSourceLinks {
		limit.Subtitle, limit.Actions = tr.T("This phone counts no app's own data (its kernel has no per-app counters), so no limit can hold"), nil
	}
	s.Body = append(s.Body,
		ui.Node{Kind: ui.KindItem, ID: "ad_bg", Title: tr.T("Background data"), Trailing: onOff(!ap.BackgroundOff), Subtitle: bg, Actions: toggle},
		ui.Node{Kind: ui.KindItem, ID: "ad_free", Title: tr.T("Unrestricted data"), Trailing: onOff(ap.Unrestricted), Subtitle: free, Actions: toggle},
		limit, sdk.Styled(ui.StyleDim, blockNote(d.Source)))
	return s
}

// blockNote says how the blocks hold where the counts come from.
func blockNote(source string) string {
	switch source {
	case ui.TrafficSourceApps:
		return tr.T("Blocked connections fail at once; Wi-Fi is never blocked. The counts are read every 30 seconds, so an app may go a little past its limit")
	case ui.TrafficSourceLinks:
		return tr.T("This phone's kernel has no per-app firewall (nftables counter objects, Linux 4.10): the app is told it is blocked and its jobs wait for Wi-Fi, but its connections go through")
	}
	return tr.T("Simulated: on this machine the Shell's policy says when the app is blocked; its own connections go through")
}

// setApp sends an app's own setting, in turn with the others (set).
func (p *app) setApp(key, id string, v float64) {
	p.sets = append(p.sets, ui.SettingArgs{Key: key, Value: v, Text: id})
	if len(p.sets) == 1 {
		p.sendSet()
	}
}

// dataScreenByID builds Data Saver's and an app's page, for one restored
// after a restart.
func (p *app) dataScreenByID(id string) (ui.Screen, bool) {
	if id == "datasaver" {
		return p.dataSaverScreen(), true
	}
	if app, ok := strings.CutPrefix(id, "appdata:"); ok {
		return p.appDataScreen(app), true
	}
	return ui.Screen{}, false
}

func sourceNote(source string) string {
	switch source {
	case ui.TrafficSourceApps:
		return tr.T("Counted per app by the firewall, and for the system by the network interfaces: bytes at the IP layer, loopback not counted; the carrier's count may differ a little. In “App permissions” an app can be set to Wi-Fi only")
	case ui.TrafficSourceLinks:
		return tr.T("This phone's kernel has no per-app counters (nftables counter objects, Linux 4.10): only the totals by network are counted")
	}
	return tr.T("Simulated: this machine counts no traffic of its own; the selftest adds it")
}

func warnLabel(pl ui.DataPlan) string {
	switch {
	case pl.WarningPercent > 0 && pl.Limit > 0:
		return tr.T("%d%% · %s", pl.WarningPercent, bytesLabel(pl.Warning))
	case pl.WarningPercent > 0:
		return tr.T("%d%% of the limit", pl.WarningPercent)
	case pl.Warning > 0:
		return bytesLabel(pl.Warning)
	}
	return tr.T("Off")
}

func (p *app) planScreen() ui.Screen {
	pl := p.s.Data.Plan
	next := func(extra ...ui.Action) []ui.Action {
		return append([]ui.Action{{ID: "next", Label: tr.T("Next option"), Primary: true, Key: "right"}, {ID: "prev", Label: tr.T("Previous"), Key: "left"}}, extra...)
	}
	limit := tr.T("Not set")
	if pl.Limit > 0 {
		limit = bytesLabel(pl.Limit)
	}
	return ui.Screen{ID: "dataplan", Title: tr.T("Data plan"), Body: []ui.Node{
		{Kind: ui.KindItem, ID: "dp_limit", Title: tr.T("Monthly data"), Subtitle: tr.T("What the plan allows each cycle"), Trailing: limit,
			Actions: []ui.Action{{ID: "edit", Label: tr.T("Change"), Primary: true}}},
		{Kind: ui.KindItem, ID: "dp_day", Title: tr.T("Cycle starts on"), Subtitle: tr.T("The day the carrier starts counting again; in a shorter month, its last day"),
			Trailing: tr.T("Day %d", pl.CycleDay), Actions: next()},
		{Kind: ui.KindItem, ID: "dp_warn", Title: tr.T("Data warning"), Subtitle: tr.T("A notification once a cycle; c sets an amount of your own"),
			Trailing: warnLabel(pl), Actions: next(ui.Action{ID: "edit", Label: tr.T("Set an amount"), Key: "c"})},
		{Kind: ui.KindItem, ID: "dp_off", Title: tr.T("Turn mobile data off at the limit"), Subtitle: tr.T("Turning it on again asks first, and keeps it on until the cycle ends or the limit is raised"),
			Trailing: onOff(pl.LimitOff), Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}},
		sdk.Styled(ui.StyleDim, tr.T("Roaming abroad is counted apart, not against the plan. The system looks at the counts every 30 seconds, so mobile data may go a little past the limit before it turns off.")),
	}}
}

// amountPage edits the allowance ("dplimit"), the warning's own amount
// ("dpwarn") or an app's limit ("adlimit:<app>").
func amountPage(id string, cur uint64) ui.Screen {
	title, note := tr.T("Monthly data"), tr.T("In GB, such as 10 or 0.5; 500 MB works too. Empty or 0: no limit")
	switch {
	case id == "dpwarn":
		title, note = tr.T("Data warning"), tr.T("In GB, such as 8 or 0.5; 500 MB works too. Empty or 0: a share of the limit again")
	case strings.HasPrefix(id, "adlimit:"):
		title, note = tr.T("Mobile data limit"), tr.T("In GB, such as 1 or 0.5; 500 MB works too. Empty or 0: no limit")
	}
	v := ""
	if cur > 0 {
		v = amountText(cur)
	}
	return ui.Screen{ID: id, Title: title, Actions: []ui.Action{{ID: "save", Label: tr.T("Save")}}, Body: []ui.Node{
		sdk.Styled(ui.StyleDim, note), {Kind: ui.KindInput, ID: "v", Value: v, Placeholder: tr.T("Amount"), Keyboard: ui.KeyboardNumber, Submit: "save"}}}
}

// parseAmount reads an amount in GB, or with a unit (GB, G, MB, M); empty
// is none.
func parseAmount(s string) (uint64, bool) {
	s = strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), " ", ""), ",", "."))
	unit := float64(1 << 30)
	for _, u := range []struct {
		suffix string
		bytes  float64
	}{{"GB", 1 << 30}, {"G", 1 << 30}, {"MB", 1 << 20}, {"M", 1 << 20}} {
		if strings.HasSuffix(s, u.suffix) {
			s, unit = strings.TrimSuffix(s, u.suffix), u.bytes
			break
		}
	}
	if s == "" {
		return 0, true
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) || v*unit > 1<<50 {
		return 0, false
	}
	return uint64(math.Round(v * unit)), true
}

// amountText is an amount as parseAmount reads it back.
func amountText(n uint64) string {
	if n%(1<<20) == 0 && n < 1<<30 {
		return fmt.Sprintf("%d MB", n>>20)
	}
	return strconv.FormatFloat(math.Round(float64(n)/(1<<30)*100)/100, 'f', -1, 64)
}

// refreshData shows the plan's page, Data Saver's and the apps' pages open
// as the system now has them.
func (p *app) refreshData() {
	p.a.Update(p.planScreen())
	p.a.Update(p.dataSaverScreen())
	for _, id := range p.pages {
		if app, ok := strings.CutPrefix(id, "appdata:"); ok {
			p.a.Update(p.appDataScreen(app))
		}
	}
}

// appAction handles Data Saver's page and the apps' pages; false for the
// rest.
func (p *app) appAction(e sdk.ActionEvent) bool {
	d := &p.s.Data
	app, onApp := strings.CutPrefix(e.Screen, "appdata:")
	find := func(id string) *ui.TrafficApp {
		for i := range d.Apps {
			if d.Apps[i].ID == id {
				return &d.Apps[i]
			}
		}
		return nil
	}
	on := map[bool]float64{true: 1, false: 0}
	switch {
	case e.Screen == "traffic" && e.Node == "datasaver" && e.Action == ui.ActionOpen:
		p.push(p.dataSaverScreen())
	case e.Screen == "traffic" && e.Action == ui.ActionOpen && (strings.HasPrefix(e.Node, "m:") || strings.HasPrefix(e.Node, "w:")) && find(e.Node[2:]) != nil:
		p.push(p.appDataScreen(e.Node[2:]))
	case e.Screen == "datasaver" && strings.HasPrefix(e.Node, "da:") && e.Action == ui.ActionOpen:
		p.push(p.appDataScreen(strings.TrimPrefix(e.Node, "da:")))
	case e.Screen == "datasaver" && e.Node == "ds_on" && e.Action == "toggle":
		d.DataSaver = !d.DataSaver
		p.a.Update(p.dataSaverScreen())
		p.set(ui.SettingDataSaver, on[d.DataSaver])
	case onApp && find(app) == nil:
		return false
	case onApp && e.Node == "ad_bg" && e.Action == "toggle":
		ap := find(app)
		ap.BackgroundOff = !ap.BackgroundOff
		p.a.Update(p.appDataScreen(app))
		p.setApp(ui.SettingAppBackgroundData, app, on[!ap.BackgroundOff])
	case onApp && e.Node == "ad_free" && e.Action == "toggle":
		ap := find(app)
		ap.Unrestricted = !ap.Unrestricted
		p.a.Update(p.appDataScreen(app))
		p.setApp(ui.SettingAppDataUnrestricted, app, on[ap.Unrestricted])
	case onApp && e.Node == "ad_limit" && e.Action == "edit":
		p.push(amountPage("adlimit:"+app, find(app).Limit))
	case strings.HasPrefix(e.Screen, "adlimit:") && e.Action == "save":
		v, ok := parseAmount(firstOf(e.Fields["v"], e.Value))
		if !ok {
			p.a.Toast(tr.T("Enter an amount such as 10 or 0.5"))
			return true
		}
		p.a.Pop()
		p.setApp(ui.SettingAppDataLimit, strings.TrimPrefix(e.Screen, "adlimit:"), float64(v))
	default:
		return false
	}
	return true
}

// dataAction handles the data usage and plan pages; false for the rest.
func (p *app) dataAction(e sdk.ActionEvent) bool {
	pl := &p.s.Data.Plan
	step := 1
	if e.Action == "prev" {
		step = -1
	}
	// The choices are kept at once, as the volumes are.
	switch {
	case onMenu(e.Screen) && e.Node == "traffic" && e.Action == ui.ActionOpen:
		// The counts move while Settings sits in the background, and the
		// plan's hold with them: show the current ones.
		p.a.Go(ui.CapSettingsGet, nil, &p.s, func(err error) {
			if err != nil {
				p.a.Toast(err.Error())
			}
			p.push(p.trafficScreen())
		})
	case e.Screen == "traffic" && (e.Node == "plan" && e.Action == ui.ActionOpen || e.Action == "plan"):
		p.push(p.planScreen())
	case e.Screen == "dataplan" && e.Node == "dp_limit":
		p.push(amountPage("dplimit", pl.Limit))
	case e.Screen == "dataplan" && e.Node == "dp_day":
		pl.CycleDay = (max(pl.CycleDay, 1)-1+step+31)%31 + 1
		p.a.Update(p.planScreen())
		p.set(ui.SettingDataCycleDay, float64(pl.CycleDay))
	case e.Screen == "dataplan" && e.Node == "dp_warn" && e.Action == "edit":
		own := uint64(0)
		if pl.WarningPercent == 0 {
			own = pl.Warning
		}
		p.push(amountPage("dpwarn", own))
	case e.Screen == "dataplan" && e.Node == "dp_warn":
		// An amount of its own steps on as none does.
		i := max(slices.Index(warnChoices, pl.WarningPercent), 0)
		pl.WarningPercent = warnChoices[(i+step+len(warnChoices))%len(warnChoices)]
		pl.Warning = pl.Limit * uint64(pl.WarningPercent) / 100
		p.a.Update(p.planScreen())
		p.set(ui.SettingDataWarningPercent, float64(pl.WarningPercent))
	case e.Screen == "dataplan" && e.Node == "dp_off":
		pl.LimitOff = !pl.LimitOff
		p.a.Update(p.planScreen())
		p.set(ui.SettingDataLimitOff, map[bool]float64{true: 1, false: 0}[pl.LimitOff])
	case (e.Screen == "dplimit" || e.Screen == "dpwarn") && e.Action == "save":
		v, ok := parseAmount(firstOf(e.Fields["v"], e.Value))
		if !ok {
			p.a.Toast(tr.T("Enter an amount such as 10 or 0.5"))
			return true
		}
		key := ui.SettingDataLimit
		if e.Screen == "dpwarn" {
			key = ui.SettingDataWarning
		}
		p.a.Pop()
		p.set(key, float64(v))
	default:
		return p.appAction(e)
	}
	return true
}
