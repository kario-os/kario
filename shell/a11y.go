package shell

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Settings › Accessibility (19-core-apps.md §1.4). The shell keeps the
// settings; what they change is done where it happens: phone-paint draws
// the frame's high contrast, bold text, colour filters and magnifier,
// times and combines the keys it reads as the "keys" message says
// (23-renderer-protocol.md), phone-audiod plays mono and balanced and
// runs the speech service; the screen reader is reader.go.

type a11ySettings struct {
	HighContrast bool   `json:"high_contrast_text,omitempty"`
	BoldText     bool   `json:"bold_text,omitempty"`
	Invert       bool   `json:"color_inversion,omitempty"`
	Correction   string `json:"color_correction,omitempty"`
	// Magnification: how much the magnifier zooms (ui.MagnificationLevels).
	Magnification int  `json:"magnification"`
	Reader        bool `json:"screen_reader,omitempty"`
	OnDemand      bool `json:"screen_reader_on_demand,omitempty"`
	// The keys (milliseconds; repeats a second, 0 none): defaults are the
	// kernel's repeat and the long press there was before.
	RepeatDelay int    `json:"key_repeat_delay"`
	RepeatRate  int    `json:"key_repeat_rate"`
	Sticky      bool   `json:"sticky_keys,omitempty"`
	SlowKeys    int    `json:"slow_keys,omitempty"`
	LongPress   int    `json:"long_press"`
	KeyFeedback string `json:"key_feedback,omitempty"`
	Mono        bool   `json:"mono_audio,omitempty"`
	// Balance: -1 left only … 1 right only.
	Balance  float64 `json:"audio_balance,omitempty"`
	Shortcut string  `json:"shortcut,omitempty"`
	// PowerEndsCall: the power key hangs up a call; nil: as the board
	// has it by default (powerEndsCall).
	PowerEndsCall *bool `json:"power_ends_call,omitempty"`
}

func defaultA11y() a11ySettings {
	return a11ySettings{Magnification: ui.DefaultMagnification, RepeatDelay: ui.DefaultKeyRepeatDelay,
		RepeatRate: ui.DefaultKeyRepeatRate, LongPress: ui.DefaultLongPress}
}

// a11yState is what the accessibility features keep while the shell runs.
type a11yState struct {
	// magnifying: the shortcut turned the magnifier on.
	magnifying bool
	// clicks counts the key clicks due (tests).
	clicks int
	reader readerState
	// speech: the screen reader's lines on their way to audiod, in order.
	speech chan map[string]any
}

// keyBuzz is a key press's vibration, short enough not to blur into the next.
const keyBuzz = 20

// setA11y is settings.set for Settings › Accessibility.
func (c *Core) setA11y(key string, v float64) error {
	a := &c.settings.A11y
	on := v != 0
	index := func(n int) (int, error) {
		i := int(v)
		if float64(i) != v || i < 0 || i >= n {
			return 0, fmt.Errorf("%s takes an index below %d", key, n)
		}
		return i, nil
	}
	choice := func(p *int, choices []int) error {
		if !slices.Contains(choices, int(v)) || float64(int(v)) != v {
			return fmt.Errorf("%s must be one of %v", key, choices)
		}
		*p = int(v)
		return nil
	}
	keys := false
	switch key {
	case ui.SettingHighContrast:
		a.HighContrast = on
		applyTheme(c.settings.Theme, on)
	case ui.SettingBoldText:
		a.BoldText = on
		textBold = on
		applyTheme(c.settings.Theme, a.HighContrast)
	case ui.SettingColorInversion:
		a.Invert = on
	case ui.SettingColorCorrection:
		i, err := index(len(ui.ColorCorrections))
		if err != nil {
			return err
		}
		a.Correction = ui.ColorCorrections[i]
	case ui.SettingMagnification:
		if err := choice(&a.Magnification, ui.MagnificationLevels); err != nil {
			return err
		}
	case ui.SettingScreenReader:
		c.setReader(on)
	case ui.SettingReaderOnDemand:
		a.OnDemand = on
	case ui.SettingKeyRepeatDelay:
		if err := choice(&a.RepeatDelay, ui.KeyRepeatDelays); err != nil {
			return err
		}
		keys = true
	case ui.SettingKeyRepeatRate:
		if err := choice(&a.RepeatRate, ui.KeyRepeatRates); err != nil {
			return err
		}
		keys = true
	case ui.SettingSlowKeys:
		if err := choice(&a.SlowKeys, ui.SlowKeyDelays); err != nil {
			return err
		}
		keys = true
	case ui.SettingLongPress:
		if err := choice(&a.LongPress, ui.LongPressDelays); err != nil {
			return err
		}
		keys = true
	case ui.SettingStickyKeys:
		a.Sticky, keys = on, true
	case ui.SettingKeyFeedback:
		i, err := index(len(ui.KeyFeedbacks))
		if err != nil {
			return err
		}
		a.KeyFeedback = ui.KeyFeedbacks[i]
	case ui.SettingMonoAudio:
		a.Mono = on
		c.applyChannels()
	case ui.SettingAudioBalance:
		if v < -1 || v > 1 || math.IsNaN(v) {
			return errors.New("audio_balance must be -1–1")
		}
		a.Balance = math.Round(v*10) / 10
		c.applyChannels()
	case ui.SettingA11yShortcut:
		i, err := index(len(ui.A11yShortcuts))
		if err != nil {
			return err
		}
		a.Shortcut = ui.A11yShortcuts[i]
		if a.Shortcut != "" {
			c.a11y.magnifying = false
		}
	case ui.SettingPowerEndsCall:
		a.PowerEndsCall = &on
		c.sendCallHint()
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
	if keys && c.gui != nil {
		c.gui.Keys(c.keySettings())
	}
	return nil
}

// a11yFrame puts the visual settings, and the magnifier while it is on,
// into a frame.
func (c *Core) a11yFrame(f *Frame) {
	a := c.settings.A11y
	f.Contrast, f.Bold, f.Invert, f.Daltonize = a.HighContrast, a.BoldText, a.Invert, a.Correction
	if c.a11y.magnifying {
		f.Magnify = float64(a.Magnification)
	}
}

// keySettings is the "keys" message: how a renderer that reads the keys
// (feature "keys") times and combines them.
func (c *Core) keySettings() map[string]any {
	a := c.settings.A11y
	return map[string]any{"t": "keys", "repeat_delay": a.RepeatDelay, "repeat_rate": a.RepeatRate,
		"long_press": a.LongPress, // const-check: ok (the renderer protocol's field, 23-renderer-protocol.md §3)
		"slow":       a.SlowKeys, "sticky": a.Sticky, "power_long": c.powerLong().Milliseconds()}
}

// applyChannels hands mono and the balance to audiod (when it comes up,
// and on change).
func (c *Core) applyChannels() {
	a := c.settings.A11y
	go func() {
		_ = c.audioRPC(svc.AudioChannels, map[string]any{"mono": a.Mono, "balance": a.Balance}, nil)
	}()
}

// a11yShortcut is both volume keys pressed together (Android's
// accessibility shortcut): the magnifier, the screen reader or colour
// inversion on or off, as Settings chose. Held, it reads what the focus
// is on, the screen reader's way of reading on demand.
func (c *Core) a11yShortcut(long bool) {
	a := &c.settings.A11y
	if long {
		if !a.Reader {
			c.showToast(tr.T("The screen reader is off: Settings › Accessibility"))
			return
		}
		c.readNow()
		return
	}
	switch a.Shortcut {
	case "":
		c.a11y.magnifying = !c.a11y.magnifying
		if c.a11y.magnifying {
			c.showToast(tr.T("Magnified %d×: the zoom follows the focus", a.Magnification))
		} else {
			c.showToast(tr.T("Magnification off"))
		}
	case ui.ShortcutReader:
		c.setReader(!a.Reader)
		c.saveSettings()
	case ui.ShortcutInvert:
		a.Invert = !a.Invert
		c.saveSettings()
		c.showToast(map[bool]string{true: tr.T("Colour inversion on"), false: tr.T("Colour inversion off")}[a.Invert])
	}
}

// keyFeedback answers a key press with a click, a short vibration or both
// (Settings › Accessibility › Key feedback). The click is a notification
// sound: silent mode keeps it quiet.
func (c *Core) keyFeedback() {
	fb := c.settings.A11y.KeyFeedback
	if (fb == ui.FeedbackSound || fb == ui.FeedbackBoth) && !c.silentSound(audio.Notify) {
		c.a11y.clicks++
		if c.audioUp {
			go func() {
				_ = c.audioRPC(svc.AudioPlay, map[string]string{"app": "system", "name": "click", "category": audio.Notify, "source": "tone:click"}, nil)
			}()
		}
	}
	if fb == ui.FeedbackVibrate || fb == ui.FeedbackBoth {
		c.buzz(keyBuzz)
	}
}
