package shell

import (
	"os"
	"strings"
	"time"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ui"
)

// The language in force is the user's choice (Settings › Language), else
// the region's (i18n.ForRegion; PHONE_LANG in the shell's environment
// takes its place on a developer machine). The shell switches at once: its
// catalogs follow i18n.SetLang, its screens are drawn again and apps' names
// come from their manifests' locales. Apps restart in the new language, as
// iOS restarts them after a language change: each is ended with its
// tombstone (tomb.go), so it comes back on its pages when opened, and the
// one on the screen comes back at once; a process gets PHONE_LANG and TZ
// when it starts. The renderer starts again for its own few words, and the
// web processes go with their apps. An app in a call or working in the
// background (playing, VoIP) keeps running in the old language until it
// stops.

// localeState is the language, region and time in force.
type localeState struct {
	lang string
	// net: what telephonyd last said (netKnown: it has said something).
	net      netInfo
	netKnown bool
	zone     *time.Location
	zoneFrom string
	timeSync string // detectTimeSync
}

// langSaveWait is how long the app on the screen has to hand over its
// state (ui.MsgState, sent when it leaves the foreground) before it is
// ended for a language change.
const langSaveWait = 1500 * time.Millisecond

// autoLanguage is the language left to the system.
func (c *Core) autoLanguage() string {
	if l := i18n.Canonical(os.Getenv("PHONE_LANG")); l != "" {
		return l
	}
	return i18n.ForRegion(c.region())
}

func (c *Core) languageInForce() string {
	if l := i18n.Canonical(c.settings.Language); l != "" {
		return l
	}
	return c.autoLanguage()
}

// startLocale puts the saved language, region and time in force at boot.
func (c *Core) startLocale() {
	c.locale.timeSync = detectTimeSync()
	c.applyLocale()
	c.applyTimeSync()
	c.watchNetwork()
}

// applyLocale puts the language and zone in force, after a setting or
// what the network says changed.
func (c *Core) applyLocale() {
	c.applyZone()
	lang := c.languageInForce()
	if lang == c.locale.lang {
		return
	}
	boot := c.locale.lang == ""
	c.locale.lang = i18n.SetLang(lang)
	c.log.Printf("language %s", c.locale.lang)
	c.retranslate()
	if !boot {
		c.restartForLanguage()
	}
}

// retranslate draws the shell's own text again in the language in force.
func (c *Core) retranslate() {
	c.sys.M.Name = tr.T("System")
	for _, a := range c.apps {
		a.M.localize(c.locale.lang)
	}
	c.refreshSystemScreens()
}

// restartForLanguage ends the apps (with their tombstones) and the
// renderer so that they start again in the new language.
func (c *Core) restartForLanguage() {
	for _, a := range c.apps {
		if !a.running || a.proc == nil || a.ending || c.inCall(a) || c.runsInBackground(a) {
			continue
		}
		if a != c.fg {
			a.ending = true
			c.kill(a)
			continue
		}
		a.relaunch = true
		a.send(ui.Msg{T: ui.MsgLifecycle, State: ui.StateBackground})
		time.AfterFunc(langSaveWait, func() { c.Post(func() { c.endForRelaunch(a) }) })
	}
	if c.gui != nil {
		c.gui.Recycle()
	}
}

// stateHandedOver: the app sent its state; one waiting to restart for a
// language change is ended now.
func (c *Core) stateHandedOver(a *App) {
	if a.relaunch {
		c.endForRelaunch(a)
	}
}

func (c *Core) endForRelaunch(a *App) {
	if a.relaunch && a.running && !a.ending {
		a.ending = true
		c.kill(a)
	}
}

// relaunched starts again, at once, an app that was ended on the screen
// for a language change (appExited).
func (c *Core) relaunched(a *App) bool {
	if !a.relaunch {
		return false
	}
	a.relaunch = false
	if c.fg != a {
		return false
	}
	c.Launch(a, "")
	return true
}

// procEnv is env with the language and time zone in force (a variable
// set twice would leave the first to getenv).
func (c *Core) procEnv(env []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "PHONE_LANG=") && !strings.HasPrefix(kv, "TZ=") {
			out = append(out, kv)
		}
	}
	return append(out, "PHONE_LANG="+tr.Lang(), "TZ="+c.zoneName())
}

// Language is the language in force (selftest, dump).
func (c *Core) Language() string { return c.locale.lang }

// localize sets the manifest's text shown (name, description, settings'
// titles, purposes, the labels of commands, intents and notification
// actions) from its locales, in lang; base keeps the manifest's own.
func (m *Manifest) localize(lang string) {
	if m.base == nil {
		b := *m
		b.Purposes = map[string]string{}
		for k, v := range m.Purposes {
			b.Purposes[k] = v
		}
		b.Settings = append([]ui.AppSetting(nil), m.Settings...)
		b.Commands = append([]AppCommand(nil), m.Commands...)
		b.Intents = append([]IntentDecl(nil), m.Intents...)
		b.NotifyActions = append([]ui.Action(nil), m.NotifyActions...)
		m.base = &b
	}
	b := m.base
	pick := func(own string, get func(i18n.AppText) string) string {
		return i18n.PickText(lang, m.Lang, m.Locales, own, get)
	}
	m.Name = pick(b.Name, func(t i18n.AppText) string { return t.Name })
	m.Desc = pick(b.Desc, func(t i18n.AppText) string { return t.Description })
	for perm, own := range b.Purposes {
		m.Purposes[perm] = pick(own, func(t i18n.AppText) string { return t.Purposes[perm] })
	}
	for i := range m.Settings {
		if i >= len(b.Settings) || m.Settings[i].Key != b.Settings[i].Key {
			continue
		}
		key := b.Settings[i].Key
		m.Settings[i].Title = pick(b.Settings[i].Title, func(t i18n.AppText) string { return t.Settings[key].Title })
	}
	for i := range m.Commands {
		if i < len(b.Commands) {
			id := b.Commands[i].ID
			m.Commands[i].Label = pick(b.Commands[i].Label, func(t i18n.AppText) string { return t.Commands[id] })
		}
	}
	for i := range m.Intents {
		if i < len(b.Intents) {
			act := b.Intents[i].Action
			m.Intents[i].Label = pick(b.Intents[i].Label, func(t i18n.AppText) string { return t.Intents[act] })
		}
	}
	for i := range m.NotifyActions {
		if i < len(b.NotifyActions) {
			id := b.NotifyActions[i].ID
			m.NotifyActions[i].Label = pick(b.NotifyActions[i].Label, func(t i18n.AppText) string { return t.NotifyActions[id] })
		}
	}
}

// ChoiceLabels are the labels of a setting's choices in lang (the values
// stay as the manifest declares them), or nil.
func (m *Manifest) ChoiceLabels(key, lang string) []string {
	n := 0
	for _, s := range m.Settings {
		if s.Key == key {
			n = len(s.Choices)
		}
	}
	base := m.Lang
	if base == "" {
		base = i18n.DefaultLang
	}
	for _, l := range i18n.Chain(lang) {
		if l == base {
			return nil
		}
		if c := m.Locales[l].Settings[key].Choices; len(c) == n && n > 0 {
			return c
		}
	}
	return nil
}

// textRules are what an installed manifest's locales are checked against.
func (m *Manifest) textRules() i18n.AppTextRules {
	r := i18n.DefaultAppTextRules()
	r.MaxPurpose = ui.MaxPurpose
	r.Settings = map[string]int{}
	for _, s := range m.Settings {
		r.Settings[s.Key] = len(s.Choices)
	}
	r.Permissions = m.Permissions
	for _, cmd := range m.Commands {
		r.Commands = append(r.Commands, cmd.ID)
	}
	for _, d := range m.Intents {
		r.Intents = append(r.Intents, d.Action)
	}
	for _, a := range m.NotifyActions {
		r.NotifyActions = append(r.NotifyActions, a.ID)
	}
	return r
}
