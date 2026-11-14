package ui

import (
	"strings"
	"testing"
)

func TestCheckNFCCards(t *testing.T) {
	lang := func(l string) bool { return l == "en" || l == "es" || l == "zh-Hant" || l == "zh-Hans" }
	transit := NFCCard{ID: "transit", Category: NFCOther, Description: "Feiqian transit card", AIDs: []string{"F0465154524E53"}, Express: true,
		Locales: map[string]string{"en": "Feiqian transit card"}}
	pay := NFCCard{ID: "pay", Category: NFCPayment, Description: "Feiqian pay", AIDs: []string{PPSE, "F0465150415901"}}
	perms := []string{PermNFC}
	if err := CheckNFCCards([]NFCCard{transit, pay}, perms, lang); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		cards []NFCCard
		perms []string
		want  string
	}{
		{"no permission", []NFCCard{transit}, nil, "permission"},
		{"bad id", []NFCCard{{ID: "Transit!", Category: NFCOther, Description: "x", AIDs: transit.AIDs}}, perms, "unique id"},
		{"same id", []NFCCard{transit, transit}, perms, "unique id"},
		{"category", []NFCCard{{ID: "a", Category: "transit", Description: "x", AIDs: transit.AIDs}}, perms, "category"},
		{"express payment", []NFCCard{{ID: "a", Category: NFCPayment, Description: "x", AIDs: pay.AIDs, Express: true}}, perms, "express"},
		{"short AID", []NFCCard{{ID: "a", Category: NFCOther, Description: "x", AIDs: []string{"F001"}}}, perms, "5 to 16 bytes"},
		{"PPSE", []NFCCard{{ID: "a", Category: NFCOther, Description: "x", AIDs: []string{PPSE}}}, perms, "PPSE"},
		{"shared AID", []NFCCard{transit, {ID: "b", Category: NFCOther, Description: "x", AIDs: transit.AIDs}}, perms, "also in"},
		{"no description", []NFCCard{{ID: "a", Category: NFCOther, AIDs: transit.AIDs}}, perms, "description"},
		{"locale", []NFCCard{{ID: "a", Category: NFCOther, Description: "x", AIDs: transit.AIDs, Locales: map[string]string{"fr": "x"}}}, perms, "not offered"},
	} {
		err := CheckNFCCards(c.cards, c.perms, lang)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error about %q", c.name, err, c.want)
		}
	}
}
