package appstore

import "github.com/kario-os/kario/i18n"

// offeredLang: a language the system offers (an nfc card's locales).
func offeredLang(l string) bool {
	for _, x := range i18n.Languages() {
		if x.ID == l {
			return true
		}
	}
	return false
}
