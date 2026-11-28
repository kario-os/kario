package appstore

import (
	"encoding/json"
	"testing"

	"github.com/kario-os/kario/i18n"
	"github.com/kario-os/kario/ui"
)

// FuzzManifest is a submitted app's manifest as review reads it, past
// the package's signatures: every check ends in yes or no.
func FuzzManifest(f *testing.F) {
	f.Add([]byte(`{"id":"org.example.notes","name":"Notes","lang":"en","description":"Write things down","version":"1.0",
		"exec":"phone-app-x","permissions":["camera","nfc"],"purposes":{"camera":"Scan a page"},
		"settings":[{"key":"sort","choices":["date","name"]}],"commands":[{"id":"new"}],"intents":[{"action":"share"}],
		"locales":{"es":{"name":"Notas","purposes":{"camera":"Escanear"},"settings":{"sort":{"title":"Orden","choices":["a","b"]}}}},
		"nfc":[{"id":"bus","category":"other","description":"Bus card","aids":["A000000003101001"],"locales":{"es":"Bus"}}],
		"privacy":{"collects":[{"data":"location","purpose":"maps"}]}}`), "1.2.3", "1.10")
	f.Fuzz(func(t *testing.T, raw []byte, a, b string) {
		var m Manifest
		if json.Unmarshal(raw, &m) != nil {
			return
		}
		i18n.CheckAppText(m.Lang, m.Locales, m.textRules())
		ui.CheckNFCCards(m.NFC, m.Permissions, offeredLang)
		if ui.CheckPrivacy(m.Privacy, m.Permissions) == nil && m.Privacy != nil {
			for _, c := range m.Privacy.Collects {
				m.Privacy.Sends(c.Data)
			}
		}
		for l := range m.Locales {
			i18n.PickText(l, m.Lang, m.Locales, m.Name, func(t i18n.AppText) string { return t.Name })
		}
		if newer(a, b) && newer(b, a) {
			t.Fatalf("%q and %q are each newer than the other", a, b)
		}
	})
}
