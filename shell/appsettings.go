package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kario-os/kario/ui"
)

// Settings and standby widgets apps declare in their manifest: the system
// shows and keeps the settings, so every app's look alike in Settings and an
// app cannot dress up a page as system settings; a widget is one line of
// text an app keeps on the standby screen.

const maxWidgetRunes = 32

type appPrefs struct {
	Values     map[string]map[string]any `json:"values"`
	Widgets    map[string]string         `json:"widgets"`
	WidgetArgs map[string]string         `json:"widget_args"`
}

func (c *Core) prefsPath() string { return filepath.Join(c.cfg.StateDir, "shell", "app-settings.json") }

func (c *Core) loadPrefs() {
	c.prefs = appPrefs{Values: map[string]map[string]any{}, Widgets: map[string]string{}}
	if raw, err := os.ReadFile(c.prefsPath()); err == nil {
		_ = json.Unmarshal(raw, &c.prefs)
	}
	if c.prefs.Values == nil {
		c.prefs.Values = map[string]map[string]any{}
	}
	if c.prefs.Widgets == nil {
		c.prefs.Widgets = map[string]string{}
	}
	if c.prefs.WidgetArgs == nil {
		c.prefs.WidgetArgs = map[string]string{}
	}
}

func (c *Core) savePrefs() {
	raw, _ := json.MarshalIndent(c.prefs, "", "  ")
	_ = os.WriteFile(c.prefsPath(), raw, 0o600)
}

// appSettings are an app's declared settings with their values.
func (c *Core) appSettings(a *App) []ui.AppSetting {
	var out []ui.AppSetting
	for _, d := range a.M.Settings {
		s := d
		s.Value = d.Default
		if l := a.M.ChoiceLabels(d.Key, tr.Lang()); len(l) == len(d.Choices) {
			s.Labels = l
		}
		if v, ok := c.prefs.Values[a.M.ID][d.Key]; ok {
			s.Value = v
		}
		out = append(out, s)
	}
	return out
}

func settingValues(list []ui.AppSetting) map[string]any {
	out := map[string]any{}
	for _, s := range list {
		out[s.Key] = s.Value
	}
	return out
}

// setAppSetting stores a value the user chose in Settings and tells the app.
func (c *Core) setAppSetting(id, key string, v any) error {
	a := c.appByID(id)
	if a == nil {
		return fmt.Errorf("no app %s", id)
	}
	for _, d := range a.M.Settings {
		if d.Key != key {
			continue
		}
		switch d.Type {
		case "toggle":
			if _, ok := v.(bool); !ok {
				return errors.New("a toggle takes true or false")
			}
		case "choice":
			s, ok := v.(string)
			found := false
			for _, ch := range d.Choices {
				found = found || ch == s
			}
			if !ok || !found {
				return errors.New("not one of the choices")
			}
		case "number":
			n, ok := v.(float64)
			if !ok || n < d.Min || n > d.Max {
				return fmt.Errorf("a number from %g to %g", d.Min, d.Max)
			}
		case "text":
			s, ok := v.(string)
			limit := d.MaxLen
			if limit <= 0 {
				limit = 64
			}
			if !ok || len([]rune(s)) > limit || strings.ContainsAny(s, "\n\r\t") {
				return fmt.Errorf("one line of at most %d characters", limit)
			}
		}
		if c.prefs.Values[id] == nil {
			c.prefs.Values[id] = map[string]any{}
		}
		c.prefs.Values[id][key] = v
		c.savePrefs()
		// The content blocker is set when the web process starts; the
		// next page opens in a new one.
		if p := c.webprocs[id]; key == "content_blocking" && p != nil {
			c.stopWebProc(p)
		}
		raw, _ := json.Marshal(map[string]any{"key": key, "value": v})
		a.send(ui.Msg{T: ui.MsgEvent, Event: ui.EventSettings, Value: string(raw)})
		return nil
	}
	return fmt.Errorf("%s has no setting %q", id, key)
}

// checkSettingDecls refuses malformed declarations when manifests load.
func checkSettingDecls(m *Manifest) error {
	seen := map[string]bool{}
	for _, d := range m.Settings {
		switch {
		case d.Key == "" || seen[d.Key] || strings.TrimSpace(d.Title) == "":
			return fmt.Errorf("setting needs a unique key and a title")
		case d.Type == "toggle", d.Type == "text":
		case d.Type == "choice" && len(d.Choices) >= 2:
		case d.Type == "number" && d.Max > d.Min:
		default:
			return fmt.Errorf("setting %s: type must be toggle, choice (two or more), number (min < max) or text", d.Key)
		}
		seen[d.Key] = true
	}
	return nil
}

// setWidget keeps an app's standby line and the deep link it opens.
func (c *Core) setWidget(a *App, text, arg string) error {
	if !a.M.Widget {
		return errors.New("the manifest does not declare a widget")
	}
	text = strings.Join(strings.Fields(text), " ")
	if rs := []rune(text); len(rs) > maxWidgetRunes {
		text = string(rs[:maxWidgetRunes])
	}
	if len(arg) > 256 {
		return errors.New("the widget's link is at most 256 bytes")
	}
	if text == "" {
		delete(c.prefs.Widgets, a.M.ID)
		delete(c.prefs.WidgetArgs, a.M.ID)
	} else {
		c.prefs.Widgets[a.M.ID], c.prefs.WidgetArgs[a.M.ID] = text, arg
	}
	c.savePrefs()
	return nil
}

// widgetApps are the apps with a standby line, in menu order (three at most).
func (c *Core) widgetApps() []*App {
	var out []*App
	for _, a := range c.apps {
		if c.prefs.Widgets[a.M.ID] != "" && a.M.Widget {
			out = append(out, a)
		}
		if len(out) == 3 {
			break
		}
	}
	return out
}

// widgetLines are the standby screen's widgets; ↓ walks them (Nokia's
// active standby) and OK opens the one marked.
func (c *Core) widgetLines() []string {
	var out []string
	for i, a := range c.widgetApps() {
		mark := ""
		if c.idleWidget == i+1 {
			mark = "▶ "
		}
		out = append(out, mark+tr.T("%s: %s", a.M.Name, c.prefs.Widgets[a.M.ID]))
	}
	return out
}

// idleKey moves through the widgets on the standby screen; false leaves the
// key to the standby screen's usual meaning.
func (c *Core) idleKey(name string) bool {
	n := len(c.widgetApps())
	switch {
	case name == KeyDown && n > 0:
		c.idleWidget = c.idleWidget%n + 1
	case name == KeyUp && c.idleWidget > 0:
		c.idleWidget--
	case name == KeyEsc && c.idleWidget > 0:
		c.idleWidget = 0
	case name == KeyEnter && c.idleWidget > 0 && c.idleWidget <= n:
		a := c.widgetApps()[c.idleWidget-1]
		c.idleWidget = 0
		c.Launch(a, c.prefs.WidgetArgs[a.M.ID])
	default:
		return false
	}
	return true
}
