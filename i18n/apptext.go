package i18n

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// An app manifest's text is in the language its "lang" names (DefaultLang
// when it names none); its "locales" member gives the same text in other
// languages, each AppText leaving out what it does not translate:
//
//	"lang": "en", "name": "Notes",
//	"locales": {"es": {"name": "Notas", "description": "…",
//	    "settings": {"sort": {"title": "Orden", "choices": ["Fecha", "Nombre"]}}}}
//
// The system shows each piece in the device's language by its fallback
// chain (Chain), and the manifest's own text when no language on the chain
// has it.
type AppText struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// Settings by key: the title and the choices' labels, in the order of
	// the setting's choices (the values the app stores stay as declared).
	Settings map[string]SettingText `json:"settings,omitempty"`
	// Purposes by runtime permission; the labels of commands by id, of
	// intents by action, of notification actions by id.
	Purposes      map[string]string `json:"purposes,omitempty"`
	Commands      map[string]string `json:"commands,omitempty"`
	Intents       map[string]string `json:"intents,omitempty"`
	NotifyActions map[string]string `json:"notify_actions,omitempty"`
}

type SettingText struct {
	Title   string   `json:"title,omitempty"`
	Choices []string `json:"choices,omitempty"`
}

// AppTextRules is what a manifest's locales are checked against: what the
// manifest declares (the number of choices by setting key, the
// permissions, the ids of commands and notification actions, the intents'
// actions) and how long each text may be, in characters.
type AppTextRules struct {
	Settings      map[string]int
	Permissions   []string
	Commands      []string
	Intents       []string
	NotifyActions []string

	MaxName, MaxDescription, MaxTitle, MaxChoice, MaxPurpose, MaxCommand, MaxIntent, MaxNotifyAction int
}

// DefaultAppTextRules are the installer's and the store's limits.
func DefaultAppTextRules() AppTextRules {
	return AppTextRules{MaxName: 32, MaxDescription: 200, MaxTitle: 32, MaxChoice: 24, MaxPurpose: 80, MaxCommand: 16,
		MaxIntent: 8, MaxNotifyAction: 16}
}

// CheckAppText checks a manifest's lang and locales: offered languages
// only, every text within its limit, and nothing that translates what the
// manifest does not declare.
func CheckAppText(base string, locales map[string]AppText, r AppTextRules) error {
	if base != "" && !offeredID(base) {
		return fmt.Errorf("lang %q is not one of %s", base, offeredIDs())
	}
	has := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	length := func(what, s string, limit int) error {
		if n := utf8.RuneCountInString(strings.TrimSpace(s)); n == 0 || n > limit || strings.ContainsAny(s, "\n\t") {
			return fmt.Errorf("%s: 1–%d characters on one line", what, limit)
		}
		return nil
	}
	labels := func(l, kind string, m map[string]string, declared []string, limit int) error {
		for k, v := range m {
			if !has(declared, k) {
				return fmt.Errorf("locales.%s.%s: %q is not declared", l, kind, k)
			}
			if err := length(fmt.Sprintf("locales.%s.%s.%s", l, kind, k), v, limit); err != nil {
				return err
			}
		}
		return nil
	}
	for l, t := range locales {
		if !offeredID(l) {
			return fmt.Errorf("locales: %q is not one of %s", l, offeredIDs())
		}
		if t.Name != "" {
			if err := length("locales."+l+".name", t.Name, r.MaxName); err != nil {
				return err
			}
		}
		if utf8.RuneCountInString(t.Description) > r.MaxDescription {
			return fmt.Errorf("locales.%s.description: at most %d characters", l, r.MaxDescription)
		}
		for key, st := range t.Settings {
			n, ok := r.Settings[key]
			if !ok {
				return fmt.Errorf("locales.%s.settings: %q is not declared", l, key)
			}
			if st.Title != "" {
				if err := length(fmt.Sprintf("locales.%s.settings.%s.title", l, key), st.Title, r.MaxTitle); err != nil {
					return err
				}
			}
			if st.Choices != nil && len(st.Choices) != n {
				return fmt.Errorf("locales.%s.settings.%s.choices: %d labels for %d choices", l, key, len(st.Choices), n)
			}
			for _, c := range st.Choices {
				if err := length(fmt.Sprintf("locales.%s.settings.%s.choices", l, key), c, r.MaxChoice); err != nil {
					return err
				}
			}
		}
		for _, x := range []struct {
			kind     string
			m        map[string]string
			declared []string
			limit    int
		}{{"purposes", t.Purposes, r.Permissions, r.MaxPurpose}, {"commands", t.Commands, r.Commands, r.MaxCommand},
			{"intents", t.Intents, r.Intents, r.MaxIntent}, {"notify_actions", t.NotifyActions, r.NotifyActions, r.MaxNotifyAction}} {
			if err := labels(l, x.kind, x.m, x.declared, x.limit); err != nil {
				return err
			}
		}
	}
	return nil
}

// PickText is a manifest's text in lang: the first language of lang's
// chain that has it, the manifest's own (in base) where the chain reaches
// base, and own when nothing on the chain has it.
func PickText(lang, base string, locales map[string]AppText, own string, get func(AppText) string) string {
	if base == "" {
		base = DefaultLang
	}
	for _, l := range Chain(lang) {
		if l == base {
			return own
		}
		if t, ok := locales[l]; ok {
			if s := get(t); s != "" {
				return s
			}
		}
	}
	return own
}

func offeredID(id string) bool {
	_, ok := find(id)
	return ok
}

func offeredIDs() string {
	ids := make([]string, len(offered.Languages))
	for i, l := range offered.Languages {
		ids[i] = l.ID
	}
	return strings.Join(ids, ", ")
}
