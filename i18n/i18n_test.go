package i18n

import (
	"strings"
	"testing"
	"testing/fstest"
)

// A translated message comes in the language, formatted with its
// arguments; an untranslated one, or a language without a catalog, stays
// English.
func TestCatalog(t *testing.T) {
	fsys := fstest.MapFS{"locale/zh-Hans.json": {Data: []byte(`{"Calling %s": "\u6b63\u5728\u547c\u53eb %s", "OK": "\u786e\u5b9a"}`)}}
	c := NewLang(fsys, "zh-Hans")
	if got := c.T("Calling %s", "10086"); got != "\u6b63\u5728\u547c\u53eb 10086" {
		t.Fatalf("got %q", got)
	}
	if got := c.T("Cancel"); got != "Cancel" {
		t.Fatalf("untranslated: %q", got)
	}
	if en := NewLang(fsys, "en"); en.T("OK") != "OK" || en.T("Calling %s", "1") != "Calling 1" {
		t.Fatal("English")
	}
	if !c.Has("OK") || c.Has("Cancel") || len(c.Messages()) != 2 {
		t.Fatal("Has / Messages")
	}
}

// A message zh-Hant lacks comes from zh-Hans, one Spanish lacks stays
// English; nothing falls back from Spanish to Chinese.
func TestFallbacks(t *testing.T) {
	fsys := fstest.MapFS{
		"locale/zh-Hans.json": {Data: []byte(`{"Settings": "\u8bbe\u7f6e", "Clock": "\u65f6\u949f"}`)},
		"locale/zh-Hant.json": {Data: []byte(`{"Settings": "\u8a2d\u5b9a"}`)},
		"locale/es.json":      {Data: []byte(`{"Settings": "Ajustes"}`)},
	}
	c := New(fsys)
	for _, x := range []struct{ lang, msg, want string }{
		{"zh-Hant", "Settings", "\u8a2d\u5b9a"}, {"zh-Hant", "Clock", "\u65f6\u949f"}, {"zh-Hant", "Back", "Back"},
		{"es", "Settings", "Ajustes"}, {"es", "Clock", "Clock"}, {"en", "Settings", "Settings"}, {"zh-TW", "Settings", "\u8a2d\u5b9a"},
	} {
		if got := c.In(x.lang).T(x.msg); got != x.want {
			t.Errorf("%s %q: %q, want %q", x.lang, x.msg, got, x.want)
		}
	}
	if got := strings.Join(Chain("zh-Hant"), " "); got != "zh-Hant zh-Hans en" {
		t.Errorf("chain %s", got)
	}
	if got := strings.Join(Chain("es"), " "); got != "es en" {
		t.Errorf("chain %s", got)
	}
}

// SetLang switches every catalog New made, at once; NewLang's stay put.
func TestSetLang(t *testing.T) {
	defer lang.Store(lang.Load())
	fsys := fstest.MapFS{"locale/es.json": {Data: []byte(`{"Yes": "S\u00ed"}`)}, "locale/zh-Hans.json": {Data: []byte(`{"Yes": "\u662f"}`)}}
	c, pinned := New(fsys), NewLang(fsys, "zh-Hans")
	if got := SetLang("es-MX"); got != Spanish || Lang() != Spanish {
		t.Fatalf("SetLang %q, Lang %q", got, Lang())
	}
	if c.T("Yes") != "S\u00ed" || c.Lang() != Spanish {
		t.Fatalf("after es: %q", c.T("Yes"))
	}
	SetLang("en")
	if c.T("Yes") != "Yes" || pinned.T("Yes") != "\u662f" {
		t.Fatalf("after en: %q, pinned %q", c.T("Yes"), pinned.T("Yes"))
	}
}

func TestCanonicalAndRegions(t *testing.T) {
	for in, want := range map[string]string{"zh": "zh-Hans", "zh-CN": "zh-Hans", "zh_TW": "zh-Hant", "zh-Hant-HK": "zh-Hant",
		"zh-MO": "zh-Hant", "en-GB": "en", "es-419": "es", "ES": "es", "fr": "en", "": ""} {
		if got := Canonical(in); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}
	for region, want := range map[string]string{"CN": "zh-Hans", "tw": "zh-Hant", "HK": "zh-Hant", "MO": "zh-Hant", "MX": "es",
		"ES": "es", "PR": "es", "GQ": "es", "US": "en", "BR": "en", "JP": "en", "": "en"} {
		if got := ForRegion(region); got != want {
			t.Errorf("ForRegion(%q) = %q, want %q", region, got, want)
		}
	}
	var names []string
	for _, l := range Languages() {
		names = append(names, l.Name)
	}
	if got := strings.Join(names, "|"); got != "\u7b80\u4f53\u4e2d\u6587|\u7e41\u9ad4\u4e2d\u6587|English|Espa\u00f1ol" {
		t.Errorf("languages %s", got)
	}
}

func TestAppText(t *testing.T) {
	locales := map[string]AppText{
		"en": {Name: "Weather", Description: "Forecasts", Settings: map[string]SettingText{"units": {Title: "Units", Choices: []string{"Celsius", "Fahrenheit"}}}},
		"es": {Name: "El tiempo"},
	}
	own := "\u5929\u6c14"
	name := func(t AppText) string { return t.Name }
	desc := func(t AppText) string { return t.Description }
	for _, x := range []struct{ lang, want string }{{"zh-Hans", own}, {"zh-Hant", own}, {"en", "Weather"}, {"es", "El tiempo"}} {
		if got := PickText(x.lang, "", locales, own, name); got != x.want {
			t.Errorf("%s: %q, want %q", x.lang, got, x.want)
		}
	}
	if got := PickText("es", "zh-Hans", locales, "\u9884\u62a5", desc); got != "Forecasts" {
		t.Errorf("Spanish description falls back to English: %q", got)
	}
	if got := PickText("zh-Hant", "en", locales, "Weather!", name); got != "Weather!" {
		t.Errorf("an English manifest's own name: %q", got)
	}
	rules := DefaultAppTextRules()
	rules.Settings = map[string]int{"units": 2}
	if err := CheckAppText("zh-Hans", locales, rules); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]AppText{
		{"fr": {Name: "x"}},
		{"es": {Name: strings.Repeat("x", 33)}},
		{"es": {Name: "a\nb"}},
		{"es": {Settings: map[string]SettingText{"units": {Choices: []string{"C"}}}}},
		{"es": {Settings: map[string]SettingText{"other": {Title: "x"}}}},
		{"es": {Purposes: map[string]string{"camera": "Scan"}}},
	} {
		if err := CheckAppText("", bad, rules); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if err := CheckAppText("fr", nil, rules); err == nil {
		t.Error("accepted lang fr")
	}
}
