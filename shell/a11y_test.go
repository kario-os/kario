package shell

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ui"
)

// Off, the accessibility settings add nothing to a frame (a renderer's
// pictures stay as they were); each one is checked, kept and read back.
func TestA11ySettings(t *testing.T) {
	dir := t.TempDir()
	c := loopCore(t, dir)
	dev, _ := DeviceByID("bar-t9")
	_ = c.onLoop(func() error {
		raw, _ := json.Marshal(c.Frame(dev))
		for _, k := range []string{"contrast", "bold", "invert", "daltonize", "magnify"} {
			if strings.Contains(string(raw), `"`+k+`"`) {
				t.Errorf("a default frame has %q", k)
			}
		}
		for _, bad := range []struct {
			key string
			v   float64
		}{
			{ui.SettingMagnification, 5}, {ui.SettingKeyRepeatDelay, 300}, {ui.SettingKeyRepeatRate, 7},
			{ui.SettingSlowKeys, 200}, {ui.SettingLongPress, 0}, {ui.SettingColorCorrection, 4},
			{ui.SettingColorCorrection, 1.5}, {ui.SettingKeyFeedback, -1}, {ui.SettingA11yShortcut, 4},
			{ui.SettingAudioBalance, 1.5},
		} {
			if err := c.settingsSet(bad.key, bad.v); err == nil {
				t.Errorf("%s %v was taken", bad.key, bad.v)
			}
		}
		for key, v := range map[string]float64{ui.SettingHighContrast: 1, ui.SettingBoldText: 1, ui.SettingColorInversion: 1,
			ui.SettingColorCorrection: 3, ui.SettingMagnification: 3, ui.SettingKeyRepeatDelay: 1000, ui.SettingKeyRepeatRate: 0,
			ui.SettingSlowKeys: 500, ui.SettingLongPress: 1500, ui.SettingStickyKeys: 1, ui.SettingKeyFeedback: 2,
			ui.SettingMonoAudio: 1, ui.SettingAudioBalance: -0.33, ui.SettingA11yShortcut: 2} {
			if err := c.settingsSet(key, v); err != nil {
				t.Errorf("%s %v: %v", key, v, err)
			}
		}
		f := c.Frame(dev)
		if !f.Contrast || !f.Bold || !f.Invert || f.Daltonize != ui.CorrectTritanomaly || f.Magnify != 0 {
			t.Errorf("frame %+v", f)
		}
		want := a11ySettings{HighContrast: true, BoldText: true, Invert: true, Correction: ui.CorrectTritanomaly, Magnification: 3,
			RepeatDelay: 1000, RepeatRate: 0, SlowKeys: 500, LongPress: 1500, Sticky: true, KeyFeedback: ui.FeedbackVibrate,
			Mono: true, Balance: -0.3, Shortcut: ui.ShortcutInvert}
		if c.settings.A11y != want {
			t.Errorf("settings %+v", c.settings.A11y)
		}
		if k := c.keySettings(); k["repeat_delay"] != 1000 || k["repeat_rate"] != 0 || k["slow"] != 500 || k["long_press"] != 1500 || k["sticky"] != true {
			t.Errorf("keys %v", k)
		}
		return nil
	})
	var saved struct {
		A11y a11ySettings `json:"accessibility"`
	}
	raw, _ := os.ReadFile(c.settingsPath())
	if err := json.Unmarshal(raw, &saved); err != nil || saved.A11y.LongPress != 1500 || saved.A11y.Correction != ui.CorrectTritanomaly {
		t.Fatalf("saved %+v: %v", saved.A11y, err)
	}
	applyTheme("", false)
	textBold = false
	// A settings file from before keeps the defaults.
	_ = os.WriteFile(c.settingsPath(), []byte(`{"ring_volume": 0.5}`), 0o600)
	c.loadSettings()
	if c.settings.A11y != defaultA11y() || c.settings.RingVolume != 0.5 {
		t.Fatalf("old file: %+v", c.settings.A11y)
	}
}

// The text renderer's high contrast: its text is the black or white
// farthest from the theme's background; bold text is bold.
func TestTextRendererContrast(t *testing.T) {
	t.Cleanup(func() { applyTheme("", false); textBold = false })
	for theme, want := range map[string]cv.RGB{"": {R: 255, G: 255, B: 255}, "light": {}, "retro": {}, "amber": {R: 255, G: 255, B: 255}} {
		applyTheme(theme, true)
		if colFG != want || colDim != want {
			t.Errorf("%q: text %v, secondary %v", theme, colFG, colDim)
		}
	}
	applyTheme("light", false)
	if colDim == colFG {
		t.Error("without high contrast secondary text is dimmer")
	}
	textBold = true
	if !st(colFG, colBG).Bold {
		t.Error("bold text is not bold")
	}
}

// readerCore is a Core with the screen reader on and a page of the
// system's in front: a switch, a value, a field, a button.
func readerCore(t *testing.T) (*Core, *View) {
	c := loopCore(t, t.TempDir())
	v := newView(ui.Screen{ID: "test", Title: "Display", Body: []ui.Node{
		{Kind: ui.KindSection, Text: "Seeing"},
		{Kind: ui.KindItem, ID: "bold", Title: "Bold text", Role: ui.RoleSwitch, Checked: true, Actions: []ui.Action{{ID: "toggle", Label: "Switch", Primary: true}}},
		{Kind: ui.KindItem, ID: "vol", Title: "Media", Subtitle: "Music and videos", Trailing: "70%", Actions: []ui.Action{{ID: "up", Label: "Louder", Primary: true}}},
		{Kind: ui.KindInput, ID: "pin", Placeholder: "PIN", Secret: true},
		{Kind: ui.KindButton, ID: "save", Text: "Save", A11yLabel: "Save the changes", Actions: []ui.Action{{ID: "save", Label: "Save"}}},
	}})
	v.focus = "bold"
	_ = c.onLoop(func() error {
		c.booted = time.Now().Add(-bootDuration)
		c.sys.stack = append(c.sys.stack, v)
		c.Render()
		return nil
	})
	return c, v
}

func said(c *Core, from int) []string {
	lines, _ := c.spokenSince(from)
	return lines
}

// The reader says the page and the focus (role, state, place in the
// list), then what changes; keys move as before.
func TestScreenReader(t *testing.T) {
	c, v := readerCore(t)
	line := func(parts ...string) string {
		out := parts[0]
		for _, p := range parts[1:] {
			out = tr.T("%s, %s", out, p)
		}
		return out
	}
	_ = c.onLoop(func() error {
		if len(said(c, 0)) != 0 {
			t.Fatalf("off, it said %q", said(c, 0))
		}
		if err := c.settingsSet(ui.SettingScreenReader, 1); err != nil {
			t.Fatal(err)
		}
		c.Render()
		want := []string{tr.T("Screen reader on"), line("Display", "Bold text", tr.T("switch"), tr.T("on"), tr.T("item %d of %d", 1, 4))}
		if got := said(c, 0); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("turned on: %q, want %q", got, want)
		}
		c.Render()
		if len(said(c, 2)) != 0 {
			t.Fatalf("nothing changed, it said %q", said(c, 2))
		}
		c.moveFocus(v, 1)
		c.Render()
		if got := said(c, 2); len(got) != 1 || got[0] != line("Media", "Music and videos", "70%", tr.T("item %d of %d", 2, 4)) {
			t.Fatalf("down: %q", got)
		}
		v.S.Body[2].Trailing = "80%"
		c.Render()
		if got := said(c, 3); len(got) != 1 || got[0] != line("80%", tr.T("item %d of %d", 2, 4)) {
			t.Fatalf("its value changed: %q", got)
		}
		c.moveFocus(v, 1)
		v.inputs["pin"] = "123"
		c.Render()
		if got := said(c, 4); len(got) != 1 || got[0] != line("PIN", tr.T("edit box"), tr.T("%d characters entered", 3), tr.T("item %d of %d", 3, 4)) {
			t.Fatalf("a PIN field: %q", got)
		}
		c.moveFocus(v, 1)
		c.Render()
		if got := said(c, 5); len(got) != 1 || got[0] != line("Save the changes", tr.T("button"), tr.T("item %d of %d", 4, 4)) {
			t.Fatalf("a button with its own label: %q", got)
		}
		c.showToast("Saved")
		c.Render()
		if got := said(c, 6); len(got) != 1 || got[0] != "Saved" {
			t.Fatalf("a toast: %q", got)
		}
		yes, no := (&confirmOverlay{}).labels()
		c.pushOverlay(&confirmOverlay{text: "Discard the changes?"})
		c.Render()
		if got := said(c, 7); len(got) != 1 || got[0] != line("Discard the changes?", tr.T("OK: %s; Back: %s", yes, no)) {
			t.Fatalf("a dialog: %q", got)
		}
		m := &menuOverlay{title: "Options", entries: []menuEntry{{label: "Copy"}, {label: "Forward"}}}
		c.pushOverlay(m)
		c.Render()
		m.sel = 1
		c.Render()
		if got := said(c, 8); len(got) != 2 || got[0] != line("Options", "Copy", tr.T("item %d of %d", 1, 2)) ||
			got[1] != line("Forward", tr.T("item %d of %d", 2, 2)) {
			t.Fatalf("a menu: %q", got)
		}
		c.popOverlay(m)
		c.overlays = nil
		c.Render()
		// On demand: nothing by itself; the shortcut held reads.
		if err := c.settingsSet(ui.SettingReaderOnDemand, 1); err != nil {
			t.Fatal(err)
		}
		n := len(said(c, 0))
		c.moveFocus(v, -1)
		c.showToast("Another")
		c.Render()
		if got := said(c, 0)[n:]; len(got) != 0 {
			t.Fatalf("on demand, it said %q", got)
		}
		c.Key(Key{Name: KeyA11y, Long: true})
		if got := said(c, 0)[n:]; len(got) != 1 || got[0] != line("Display", "PIN", tr.T("edit box"), tr.T("%d characters entered", 3), tr.T("item %d of %d", 3, 4)) {
			t.Fatalf("read on demand: %q", got)
		}
		if err := c.settingsSet(ui.SettingScreenReader, 0); err != nil {
			t.Fatal(err)
		}
		if got := said(c, 0); got[len(got)-1] != tr.T("Screen reader off") {
			t.Fatalf("turned off: %q", got)
		}
		n = len(said(c, 0))
		c.Key(Key{Name: KeyA11y, Long: true})
		c.moveFocus(v, 1)
		c.Render()
		if len(said(c, 0)) != n || c.toast == nil || c.toast.body != tr.T("The screen reader is off: Settings › Accessibility") {
			t.Fatalf("off: said %q, toast %+v", said(c, 0)[n:], c.toast)
		}
		return nil
	})
}

// The shortcut does what Settings chose: the magnifier (the frame zooms
// around the focus), the screen reader or colour inversion on and off.
func TestA11yShortcut(t *testing.T) {
	c := loopCore(t, t.TempDir())
	dev, _ := DeviceByID("bar-t9")
	_ = c.onLoop(func() error {
		c.booted = time.Now().Add(-bootDuration)
		c.Key(Named(KeyA11y))
		if f := c.Frame(dev); f.Magnify != 2 {
			t.Fatalf("magnifier: %v", f.Magnify)
		}
		_ = c.settingsSet(ui.SettingMagnification, 3)
		if f := c.Frame(dev); f.Magnify != 3 {
			t.Fatalf("3×: %v", f.Magnify)
		}
		c.Key(Named(KeyA11y))
		if f := c.Frame(dev); f.Magnify != 0 {
			t.Fatalf("magnifier off: %v", f.Magnify)
		}
		c.Key(Named(KeyA11y))
		_ = c.settingsSet(ui.SettingA11yShortcut, 2)
		if f := c.Frame(dev); f.Magnify != 0 {
			t.Fatal("another shortcut left the magnifier on")
		}
		c.Key(Named(KeyA11y))
		if !c.Frame(dev).Invert {
			t.Fatal("the shortcut did not invert the colours")
		}
		c.Key(Named(KeyA11y))
		if c.Frame(dev).Invert {
			t.Fatal("again did not stop it")
		}
		_ = c.settingsSet(ui.SettingA11yShortcut, 1)
		c.Key(Named(KeyA11y))
		if !c.settings.A11y.Reader {
			t.Fatal("the shortcut did not turn the screen reader on")
		}
		c.Key(Named(KeyA11y))
		_ = c.settingsSet(ui.SettingA11yShortcut, 3)
		c.Key(Named(KeyA11y))
		if a := c.settings.A11y; a.Reader || a.Invert || c.a11y.magnifying {
			t.Fatalf("off, the shortcut did something: %+v", a)
		}
		return nil
	})
}

// A key pressed clicks, vibrates or both; a key held for an app and
// replayed does not again, nor does one in silent mode click.
func TestKeyFeedback(t *testing.T) {
	c := loopCore(t, t.TempDir())
	_ = c.onLoop(func() error {
		c.booted = time.Now().Add(-bootDuration)
		c.Press(Named(KeyDown))
		if c.a11y.clicks != 0 || c.vibrations != 0 {
			t.Fatal("feedback by default")
		}
		_ = c.settingsSet(ui.SettingKeyFeedback, 3)
		c.Press(Named(KeyDown))
		c.Press(Runes("5"))
		if c.a11y.clicks != 2 || c.vibrations != 2 {
			t.Fatalf("both: %d clicks, %d vibrations", c.a11y.clicks, c.vibrations)
		}
		c.Key(Named(KeyDown))
		if c.a11y.clicks != 2 || c.vibrations != 2 {
			t.Fatal("a replayed key gave feedback")
		}
		c.Press(Named(KeyFnOnce))
		if c.a11y.clicks != 2 {
			t.Fatal("Fn's state is not a key press")
		}
		c.settings.Silent = true
		c.Press(Named(KeyDown))
		if c.a11y.clicks != 2 || c.vibrations != 3 {
			t.Fatalf("silent mode: %d clicks, %d vibrations", c.a11y.clicks, c.vibrations)
		}
		_ = c.settingsSet(ui.SettingKeyFeedback, 1)
		c.settings.Silent = false
		c.Press(Named(KeyDown))
		if c.a11y.clicks != 3 || c.vibrations != 3 {
			t.Fatalf("sound: %d clicks, %d vibrations", c.a11y.clicks, c.vibrations)
		}
		return nil
	})
}

// A renderer that reads the keys gets their timing at start and again on
// each change; one that does not is not sent it.
func TestRendererKeySettings(t *testing.T) {
	c := loopCore(t, t.TempDir())
	dir := t.TempDir()
	got := filepath.Join(dir, "got.jsonl")
	renderer := func(features string) string {
		p := filepath.Join(dir, "renderer-"+features)
		script := "#!/bin/sh\nread hello\necho '{\"t\":\"ready\",\"proto\":\"phone.frame/1\",\"renderer\":{\"name\":\"x\"},\"features\":[" + features + "]}'\ncat >> " + got + "\n"
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	dev, _ := DeviceByID("bar-t9")
	keys := func(n int) []map[string]any {
		t.Helper()
		var out []map[string]any
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			out = nil
			raw, _ := os.ReadFile(got)
			for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var m map[string]any
				if json.Unmarshal([]byte(l), &m) == nil && m["t"] == "keys" {
					out = append(out, m)
				}
			}
			if len(out) >= n {
				break
			}
		}
		return out
	}
	err := c.onLoop(func() error {
		g, err := StartGUI(c, renderer(`"snap"`), dev)
		if err != nil {
			return err
		}
		g.Close()
		_, err = StartGUI(c, renderer(`"keys"`), dev)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.onLoop(func() error { c.gui.Close(); return nil }) })
	if k := keys(1); len(k) != 1 || k[0]["repeat_delay"] != float64(250) || k[0]["repeat_rate"] != float64(30) || k[0]["long_press"] != float64(600) || k[0]["slow"] != float64(0) || k[0]["sticky"] != false {
		t.Fatalf("at start: %v", k)
	}
	_ = c.onLoop(func() error { return c.settingsSet(ui.SettingSlowKeys, 300) })
	if k := keys(2); len(k) != 2 || k[1]["slow"] != float64(300) {
		t.Fatalf("on change: %v", k)
	}
}
