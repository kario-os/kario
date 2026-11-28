package main

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

// Settings › Accessibility, grouped as Android's and Xiaomi's are: the
// screen reader, seeing, the keys, hearing, the shortcut. The system keeps
// and applies the settings (shell/a11y.go); this page shows and changes
// them.

// a11yInfo is settings.get's "accessibility" (in "settings").
type a11yInfo struct {
	HighContrast  bool    `json:"high_contrast_text"`
	BoldText      bool    `json:"bold_text"`
	Invert        bool    `json:"color_inversion"`
	Correction    string  `json:"color_correction"`
	Magnification int     `json:"magnification"`
	Reader        bool    `json:"screen_reader"`
	OnDemand      bool    `json:"screen_reader_on_demand"`
	RepeatDelay   int     `json:"key_repeat_delay"`
	RepeatRate    int     `json:"key_repeat_rate"`
	Sticky        bool    `json:"sticky_keys"`
	SlowKeys      int     `json:"slow_keys"`
	LongPress     int     `json:"long_press"`
	KeyFeedback   string  `json:"key_feedback"`
	Mono          bool    `json:"mono_audio"`
	Balance       float64 `json:"audio_balance"`
	Shortcut      string  `json:"shortcut"`
}

const a11yGroup = groupPrefix + "a11y"

// a11yRows are the page's rows, each under the heading before it.
func a11yRows() []string {
	return []string{ui.SettingScreenReader, ui.SettingReaderOnDemand,
		ui.SettingLargeText, ui.SettingHighContrast, ui.SettingBoldText, ui.SettingColorInversion, ui.SettingColorCorrection, ui.SettingMagnification,
		ui.SettingKeyRepeatDelay, ui.SettingKeyRepeatRate, ui.SettingLongPress, ui.SettingPowerEndsCall, ui.SettingSlowKeys, ui.SettingStickyKeys,
		ui.SettingKeyFeedback, ui.SettingMonoAudio, ui.SettingAudioBalance,
		ui.SettingA11yShortcut}
}

func a11yHeading(row string) string {
	switch row {
	case ui.SettingScreenReader:
		return tr.T("Screen reader")
	case ui.SettingLargeText:
		return tr.T("Seeing")
	case ui.SettingKeyRepeatDelay:
		return tr.T("Keys")
	case ui.SettingMonoAudio:
		return tr.T("Hearing")
	case ui.SettingA11yShortcut:
		return tr.T("Shortcut")
	}
	return ""
}

// a11yNote is what the page says after a section: what it cannot do yet.
func a11yNote(row string) string {
	switch row {
	case ui.SettingReaderOnDemand:
		return tr.T("No voice yet: the phone has no speech engine, so the reader writes what it would say into the system log")
	case ui.SettingKeyFeedback:
		return tr.T("The timing takes effect on the phone's own keypad; in a computer's terminal the terminal repeats keys")
	case ui.SettingAudioBalance:
		return tr.T("Captions: nothing the phone plays has text to show yet")
	}
	return ""
}

// a11yPage is the group's page: its rows under headings, with the notes.
func (p *app) a11yPage(s ui.Screen) ui.Screen {
	s.Body = nil
	for _, id := range a11yRows() {
		if h := a11yHeading(id); h != "" {
			s.Body = append(s.Body, sdk.Section(h))
		}
		if n, ok := p.row(id); ok {
			s.Body = append(s.Body, n)
		}
		if note := a11yNote(id); note != "" {
			s.Body = append(s.Body, sdk.Styled(ui.StyleDim, note))
		}
	}
	return s
}

// a11ySummary is the group's line on the top: what is on.
func (p *app) a11ySummary() string {
	a := p.s.Settings.A11y
	var on []string
	for _, f := range []struct {
		on    bool
		label string
	}{{a.Reader, tr.T("Screen reader")}, {a.HighContrast, tr.T("High-contrast text")}, {a.BoldText, tr.T("Bold text")},
		{a.Invert, tr.T("Colour inversion")}, {a.Correction != "", tr.T("Colour correction")}, {a.Sticky, tr.T("Sticky keys")},
		{a.SlowKeys > 0, tr.T("Slow keys")}, {a.Mono, tr.T("Mono audio")}} {
		if f.on {
			on = append(on, f.label)
		}
	}
	if len(on) == 0 {
		return tr.T("Screen reader, colours, magnifier, keys, sound")
	}
	return strings.Join(on, " · ")
}

func secs(ms int) string {
	return tr.T("%s s", strconv.FormatFloat(float64(ms)/1000, 'f', -1, 64))
}

func correctionName(c string) string {
	switch c {
	case ui.CorrectDeuteranomaly:
		return tr.T("Deuteranomaly (red-green)")
	case ui.CorrectProtanomaly:
		return tr.T("Protanomaly (red-green)")
	case ui.CorrectTritanomaly:
		return tr.T("Tritanomaly (blue-yellow)")
	}
	return tr.T("Off")
}

func feedbackName(f string) string {
	switch f {
	case ui.FeedbackSound:
		return tr.T("Sound")
	case ui.FeedbackVibrate:
		return tr.T("Vibration")
	case ui.FeedbackBoth:
		return tr.T("Sound and vibration")
	}
	return tr.T("None")
}

func shortcutName(s string) string {
	switch s {
	case ui.ShortcutReader:
		return tr.T("Screen reader")
	case ui.ShortcutInvert:
		return tr.T("Colour inversion")
	case ui.ShortcutOff:
		return tr.T("Off")
	}
	return tr.T("Magnification")
}

func balanceName(b float64) string {
	switch n := int(math.Round(math.Abs(b) * 100)); {
	case n == 0:
		return tr.T("Centre")
	case b < 0:
		return tr.T("Left %d%%", n)
	default:
		return tr.T("Right %d%%", n)
	}
}

// a11yRow is the page's row of that ID.
func (p *app) a11yRow(id string) (ui.Node, bool) {
	a := p.s.Settings.A11y
	toggle := func(title, sub string, on bool) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: id, Title: title, Subtitle: sub, Trailing: onOff(on), Role: ui.RoleSwitch, Checked: on,
			Actions: []ui.Action{{ID: "toggle", Label: tr.T("Switch"), Primary: true}}}
	}
	choice := func(title, sub, value string) ui.Node {
		return ui.Node{Kind: ui.KindItem, ID: id, Title: title, Subtitle: sub, Trailing: value,
			Actions: []ui.Action{{ID: "next", Label: tr.T("Next"), Primary: true, Key: "right"}, {ID: "prev", Label: tr.T("Previous"), Key: "left"}}}
	}
	switch id {
	case ui.SettingScreenReader:
		return toggle(tr.T("Screen reader"), tr.T("Says what the focus is on and what changes; the keys move as always"), a.Reader), true
	case ui.SettingReaderOnDemand:
		mode, sub := tr.T("When the focus moves"), tr.T("Hold both volume keys to hear the item again")
		if a.OnDemand {
			mode, sub = tr.T("On demand"), tr.T("Silent until you hold both volume keys: then it reads the item")
		}
		return choice(tr.T("Reading"), sub, mode), true
	case ui.SettingHighContrast:
		return toggle(tr.T("High-contrast text"), tr.T("Text in black or white, the colours of states darker or lighter"), a.HighContrast), true
	case ui.SettingBoldText:
		return toggle(tr.T("Bold text"), tr.T("All text in the bold face"), a.BoldText), true
	case ui.SettingColorInversion:
		return toggle(tr.T("Colour inversion"), tr.T("Light becomes dark and dark light; colours keep their hue"), a.Invert), true
	case ui.SettingColorCorrection:
		return choice(tr.T("Colour correction"), tr.T("Shifts colours so that a weak cone's differences show"), correctionName(a.Correction)), true
	case ui.SettingMagnification:
		sub := tr.T("Press both volume keys at once to zoom around the focus; again to stop")
		if a.Shortcut != "" {
			sub = tr.T("The accessibility shortcut does something else now (Shortcut, below)")
		}
		return choice(tr.T("Magnification"), sub, tr.T("%d×", max(a.Magnification, ui.DefaultMagnification))), true
	case ui.SettingKeyRepeatDelay:
		return choice(tr.T("Key repeat delay"), tr.T("How long a key is held before it repeats"), secs(a.RepeatDelay)), true
	case ui.SettingKeyRepeatRate:
		rate := tr.T("No repeat")
		if a.RepeatRate > 0 {
			rate = tr.T("%d a second", a.RepeatRate)
		}
		return choice(tr.T("Key repeat rate"), tr.T("How fast a held key repeats"), rate), true
	case ui.SettingLongPress:
		return choice(tr.T("Long press"), tr.T("How long a key is held for its second meaning (a digit, the power menu)"), secs(a.LongPress)), true
	case ui.SettingPowerEndsCall:
		if p.s.PowerEndsCall == nil {
			return ui.Node{}, false
		}
		return toggle(tr.T("Power key ends call"), tr.T("In a call, with the screen on, a press of the power key hangs up"), *p.s.PowerEndsCall), true
	case ui.SettingSlowKeys:
		v := tr.T("Off")
		if a.SlowKeys > 0 {
			v = secs(a.SlowKeys)
		}
		return choice(tr.T("Slow keys"), tr.T("A key counts only once held this long; brushed keys do not"), v), true
	case ui.SettingStickyKeys:
		return toggle(tr.T("Sticky keys"), tr.T("Shift, Fn, Ctrl and Alt need not be held: press one, then the key; twice locks Shift and Fn"), a.Sticky), true
	case ui.SettingKeyFeedback:
		return choice(tr.T("Key feedback"), tr.T("A click, a short vibration or both at each key"), feedbackName(a.KeyFeedback)), true
	case ui.SettingMonoAudio:
		return toggle(tr.T("Mono audio"), tr.T("Both channels together, for one ear or one earphone"), a.Mono), true
	case ui.SettingAudioBalance:
		return ui.Node{Kind: ui.KindItem, ID: id, Title: tr.T("Left/right balance"), Subtitle: tr.T("← towards the left · → towards the right, in steps of 10%"),
			Trailing: balanceName(a.Balance), Actions: []ui.Action{{ID: "right", Label: tr.T("Towards the right"), Primary: true, Key: "right"},
				{ID: "left", Label: tr.T("Towards the left"), Key: "left"}}}, true
	case ui.SettingA11yShortcut:
		return choice(tr.T("Accessibility shortcut"), tr.T("Both volume keys at once; held, they read the focused item"), shortcutName(a.Shortcut)), true
	}
	return ui.Node{}, false
}

// step moves through choices from cur, by +1 or -1, wrapping.
func step[T comparable](choices []T, cur T, by int) (T, int) {
	i := slices.Index(choices, cur)
	i = (max(i, 0) + by + len(choices)) % len(choices)
	return choices[i], i
}

// a11yAction handles a press on the page's rows.
func (p *app) a11yAction(e sdk.ActionEvent) bool {
	if !onMenu(e.Screen) || !slices.Contains(a11yRows(), e.Node) || e.Node == ui.SettingLargeText {
		return false
	}
	a := &p.s.Settings.A11y
	by := map[string]int{"next": 1, "prev": -1, "right": 1, "left": -1}[e.Action]
	flag := map[string]*bool{ui.SettingScreenReader: &a.Reader, ui.SettingHighContrast: &a.HighContrast, ui.SettingBoldText: &a.BoldText,
		ui.SettingColorInversion: &a.Invert, ui.SettingStickyKeys: &a.Sticky, ui.SettingMonoAudio: &a.Mono}[e.Node]
	// Kept at once, as the volumes are: the next press steps on from it.
	switch {
	case flag != nil:
		*flag = !*flag
		p.set(e.Node, map[bool]float64{true: 1, false: 0}[*flag])
	case e.Node == ui.SettingPowerEndsCall:
		if p.s.PowerEndsCall == nil {
			return true
		}
		on := !*p.s.PowerEndsCall
		p.s.PowerEndsCall = &on
		p.set(e.Node, map[bool]float64{true: 1, false: 0}[on])
	case by == 0:
		return true
	case e.Node == ui.SettingReaderOnDemand:
		a.OnDemand = !a.OnDemand
		p.set(e.Node, map[bool]float64{true: 1, false: 0}[a.OnDemand])
	case e.Node == ui.SettingColorCorrection:
		var i int
		a.Correction, i = step(ui.ColorCorrections, a.Correction, by)
		p.set(e.Node, float64(i))
	case e.Node == ui.SettingKeyFeedback:
		var i int
		a.KeyFeedback, i = step(ui.KeyFeedbacks, a.KeyFeedback, by)
		p.set(e.Node, float64(i))
	case e.Node == ui.SettingA11yShortcut:
		var i int
		a.Shortcut, i = step(ui.A11yShortcuts, a.Shortcut, by)
		p.set(e.Node, float64(i))
	case e.Node == ui.SettingAudioBalance:
		a.Balance = math.Max(-1, math.Min(1, math.Round((a.Balance+0.1*float64(by))*10)/10))
		p.set(e.Node, a.Balance)
	default:
		ints := map[string]struct {
			p       *int
			choices []int
		}{ui.SettingMagnification: {&a.Magnification, ui.MagnificationLevels}, ui.SettingKeyRepeatDelay: {&a.RepeatDelay, ui.KeyRepeatDelays},
			ui.SettingKeyRepeatRate: {&a.RepeatRate, ui.KeyRepeatRates}, ui.SettingLongPress: {&a.LongPress, ui.LongPressDelays},
			ui.SettingSlowKeys: {&a.SlowKeys, ui.SlowKeyDelays}}[e.Node]
		if ints.p == nil {
			return false
		}
		*ints.p, _ = step(ints.choices, *ints.p, by)
		p.set(e.Node, float64(*ints.p))
	}
	p.updateMenu()
	return true
}
