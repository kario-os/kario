package shell

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ui"
)

// The system's date and time fields (Node.Keyboard "date" and "time"):
// the app gives and gets "YYYY-MM-DD" or "HH:MM"; the user sees the date in
// the region's order with its weekday, the time on the clock they chose,
// and edits one part at a time. Arriving on the field nothing is selected
// and ↑ ↓ move on as from any field; ← → select a part (→ the first, ←
// the last), ↑ ↓ then step it (a day carries into the month and the year,
// a month or year keeps the day inside the month, minutes go by five and
// carry into the hour), digits type it (two, four for a year; a first
// digit no second one can follow completes it) and the next part is
// selected, the last one done ends the editing; OK or Back end it too.
// Backspace takes back a digit typed, else clears the field. The digits
// come straight from a 9-key pad and from a full keyboard's number layer
// without Fn, as in a number field.

func dateField(keyboard string) bool {
	return keyboard == ui.KeyboardDate || keyboard == ui.KeyboardTime
}

// dateEdit is the part being edited in the view's focused date or time
// field: its index in the field's parts and the digits typed into it.
type dateEdit struct {
	node  string
	part  int
	typed string
}

const (
	partYear = iota
	partMonth
	partDay
	partHour
	partMinute
	partAMPM
)

// sundayFirst and saturdayFirst: the regions whose week starts on Sunday
// or Saturday (CLDR's weekData); the rest start on Monday (ISO 8601).
var (
	sundayFirst   = "AG AS BD BR BS BT BW BZ CA CO DM DO ET GT GU HK HN ID IL IN JM JP KE KH KR LA MH MM MO MT MX MZ NI NP PA PE PH PK PR PT PY SA SG SV TH TT TW UM US VE VI WS YE ZA ZW"
	saturdayFirst = "AE AF BH DJ DZ EG IQ IR JO KW LY OM QA SD SY"
)

func firstWeekday(region string) int {
	switch {
	case region != "" && strings.Contains(sundayFirst, region):
		return 0
	case region != "" && strings.Contains(saturdayFirst, region):
		return 6
	}
	return 1
}

// dateOrder: Chinese writes year, month, day everywhere; so do the regions
// of East Asia and a few others; the United States month first; the rest
// day first.
func dateOrder(lang, region string) string {
	switch {
	case strings.HasPrefix(lang, "zh") || region != "" && strings.Contains("CN TW HK MO JP KR KP MN HU LT", region):
		return ui.DateYMD
	case region == "US" || region == "PH" || region == "FM":
		return ui.DateMDY
	}
	return ui.DateDMY
}

func (c *Core) localeInfo() ui.LocaleInfo {
	r := c.region()
	return ui.LocaleInfo{Lang: i18n.Lang(), Region: r, Zone: c.zoneName(), Clock24: !c.settings.Clock12,
		FirstWeekday: firstWeekday(r), DateOrder: dateOrder(i18n.Lang(), r)}
}

var weekdayShort = [...]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

// fieldParts are a field's parts in the order the user sees them.
func (c *Core) fieldParts(keyboard string) []int {
	if keyboard == ui.KeyboardTime {
		if !c.settings.Clock12 {
			return []int{partHour, partMinute}
		}
		if strings.Index(tr.T("{h}:{min} {ampm}"), "{ampm}") < strings.Index(tr.T("{h}:{min} {ampm}"), "{h}") {
			return []int{partAMPM, partHour, partMinute}
		}
		return []int{partHour, partMinute, partAMPM}
	}
	switch dateOrder(i18n.Lang(), c.region()) {
	case ui.DateYMD:
		return []int{partYear, partMonth, partDay}
	case ui.DateMDY:
		return []int{partMonth, partDay, partYear}
	}
	return []int{partDay, partMonth, partYear}
}

// fieldValue reads a field's value; ok is false for an empty or broken one.
func fieldValue(keyboard, v string) (time.Time, bool) {
	layout := time.DateOnly
	if keyboard == ui.KeyboardTime {
		layout = "15:04"
	}
	t, err := time.Parse(layout, v)
	return t, err == nil
}

func fieldFormat(keyboard string, t time.Time) string {
	if keyboard == ui.KeyboardTime {
		return t.Format("15:04")
	}
	return t.Format(time.DateOnly)
}

// fieldStart is what editing an empty field starts from: today, or this
// hour.
func (c *Core) fieldStart(keyboard string) time.Time {
	now := c.now().In(time.Local)
	if keyboard == ui.KeyboardTime {
		return time.Date(0, 1, 1, now.Hour(), 0, 0, 0, time.UTC)
	}
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// inputText is what an input shows: a date or time field its value as the
// user reads it, the part being edited in brackets; others their text.
func (c *Core) inputText(v *View, n *ui.Node) string {
	val := v.inputs[n.ID]
	if !dateField(n.Keyboard) {
		return val
	}
	t, ok := fieldValue(n.Keyboard, val)
	if !ok {
		return val
	}
	edit := v.dateEdit
	if edit == nil || edit.node != n.ID || n.ID != v.focus {
		edit = nil
	}
	parts := c.fieldParts(n.Keyboard)
	show := func(i int, s string) string {
		if edit == nil || edit.part != i {
			return s
		}
		if edit.typed != "" {
			s = edit.typed + strings.Repeat("_", max(0, len(s)-len(edit.typed)))
		}
		return "[" + s + "]"
	}
	text := map[int]string{}
	for i, p := range parts {
		var s string
		switch p {
		case partYear:
			s = fmt.Sprintf("%04d", t.Year())
		case partMonth:
			s = fmt.Sprintf("%02d", int(t.Month()))
		case partDay:
			s = fmt.Sprintf("%02d", t.Day())
		case partHour:
			h := t.Hour()
			if c.settings.Clock12 {
				if h = h % 12; h == 0 {
					h = 12
				}
			}
			s = fmt.Sprintf("%02d", h)
		case partMinute:
			s = fmt.Sprintf("%02d", t.Minute())
		case partAMPM:
			s = map[bool]string{true: tr.T("AM"), false: tr.T("PM")}[t.Hour() < 12]
		}
		text[p] = show(i, s)
	}
	if n.Keyboard == ui.KeyboardTime {
		layout := "{h}:{min}"
		if c.settings.Clock12 {
			layout = tr.T("{h}:{min} {ampm}")
		}
		return strings.NewReplacer("{h}", text[partHour], "{min}", text[partMinute], "{ampm}", text[partAMPM]).Replace(layout)
	}
	sep := "/"
	if dateOrder(i18n.Lang(), c.region()) == ui.DateYMD {
		sep = "-"
	}
	var out []string
	for _, p := range parts {
		out = append(out, text[p])
	}
	return strings.Join(out, sep) + " " + tr.T(weekdayShort[t.Weekday()])
}

// daysIn is the number of days of y's month m.
func daysIn(y int, m time.Month) int { return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day() }

// withPart sets one part of t, keeping a date's day inside its month.
func withPart(t time.Time, part, val int) time.Time {
	y, m, d := t.Date()
	h, mi := t.Hour(), t.Minute()
	switch part {
	case partYear:
		y = min(max(val, 1), 9999)
	case partMonth:
		m = time.Month(min(max(val, 1), 12))
	case partDay:
		d = val
	case partHour:
		h = (val%24 + 24) % 24
	case partMinute:
		mi = min(max(val, 0), 59)
	}
	if part == partDay {
		d = min(max(d, 1), daysIn(y, m))
	} else {
		d = min(d, daysIn(y, m))
	}
	return time.Date(y, m, d, h, mi, 0, 0, time.UTC)
}

// stepPart moves a part by dir (±1).
func stepPart(t time.Time, part, dir int) time.Time {
	switch part {
	case partYear:
		return withPart(t, partYear, t.Year()+dir)
	case partMonth:
		y, m := t.Year(), int(t.Month())+dir
		if m < 1 {
			y, m = y-1, 12
		} else if m > 12 {
			y, m = y+1, 1
		}
		return withPart(withPart(t, partYear, y), partMonth, m)
	case partDay:
		return t.AddDate(0, 0, dir)
	case partHour:
		return withPart(t, partHour, t.Hour()+dir)
	case partMinute:
		mins := t.Hour()*60 + t.Minute()
		if dir > 0 {
			mins = (mins/5 + 1) * 5
		} else {
			mins = (mins+4)/5*5 - 5
		}
		mins = (mins%(24*60) + 24*60) % (24 * 60)
		return time.Date(0, 1, 1, mins/60, mins%60, 0, 0, time.UTC)
	case partAMPM:
		return withPart(t, partHour, t.Hour()+12)
	}
	return t
}

// partDigits is how many digits a part takes.
func partDigits(part int) int {
	if part == partYear {
		return 4
	}
	return 2
}

// roomForSecond: a part's first digit some second digit can follow (a
// day's 3, not its 4).
func roomForSecond(part int, first byte, clock12 bool) bool {
	limit := map[int]byte{partMonth: '1', partDay: '3', partHour: '2', partMinute: '5'}[part]
	if part == partHour && clock12 {
		limit = '1'
	}
	return first <= limit
}

// typedPart sets the part from its digits (a 12-hour hour on the half of
// the day the field shows).
func (c *Core) typedPart(t time.Time, part int, digits string) time.Time {
	n, _ := strconv.Atoi(digits)
	if part == partHour && c.settings.Clock12 {
		n = min(max(n, 1), 12) % 12
		if t.Hour() >= 12 {
			n += 12
		}
	}
	if part == partHour {
		n = min(n, 23)
	}
	return withPart(t, part, n)
}

// dateKey edits the focused date or time field; false leaves the key to
// the normal handling (focus moves, OK, Back, shortcuts).
func (c *Core) dateKey(v *View, n *ui.Node, k Key) bool {
	parts := c.fieldParts(n.Keyboard)
	edit := v.dateEdit
	if edit != nil && edit.node != n.ID {
		edit = nil
	}
	t, ok := fieldValue(n.Keyboard, v.inputs[n.ID])
	if !ok {
		t = c.fieldStart(n.Keyboard)
	}
	set := func(t time.Time) { v.inputs[n.ID] = fieldFormat(n.Keyboard, t) }
	selectPart := func(i int) { v.dateEdit = &dateEdit{node: n.ID, part: i} }
	switch k.Name {
	case KeyLeft, KeyRight:
		set(t)
		switch {
		case edit == nil && k.Name == KeyRight:
			selectPart(0)
		case edit == nil:
			selectPart(len(parts) - 1)
		case k.Name == KeyRight:
			selectPart(min(edit.part+1, len(parts)-1))
		default:
			selectPart(max(edit.part-1, 0))
		}
		return true
	case KeyUp, KeyDown:
		if edit == nil {
			return false
		}
		dir := map[string]int{KeyUp: 1, KeyDown: -1}[k.Name]
		set(stepPart(t, parts[edit.part], dir))
		edit.typed = ""
		return true
	case KeyEsc, KeyEnter:
		if edit == nil {
			return false
		}
		v.dateEdit = nil
		return k.Name == KeyEsc
	case KeyBackspace:
		switch {
		case edit != nil && edit.typed != "":
			edit.typed = edit.typed[:len(edit.typed)-1]
		case edit != nil:
			v.dateEdit = nil
		default:
			v.inputs[n.ID] = ""
		}
		return true
	case KeyRunes:
		rs := c.fnRunes(k.Runes)
		for _, r := range rs {
			if r < '0' || r > '9' {
				continue
			}
			if edit == nil {
				set(t)
				selectPart(0)
				edit = v.dateEdit
			}
			if parts[edit.part] == partAMPM {
				if edit.part+1 >= len(parts) {
					continue
				}
				selectPart(edit.part + 1)
				edit = v.dateEdit
			}
			p := parts[edit.part]
			edit.typed += string(r)
			if len(edit.typed) < partDigits(p) && (p == partYear || roomForSecond(p, edit.typed[0], c.settings.Clock12)) {
				continue
			}
			t = c.typedPart(t, p, edit.typed)
			set(t)
			next := edit.part + 1
			for next < len(parts) && parts[next] == partAMPM {
				next++
			}
			if next >= len(parts) {
				v.dateEdit, edit = nil, nil
				continue
			}
			selectPart(next)
			edit = v.dateEdit
		}
		return true
	}
	return false
}
