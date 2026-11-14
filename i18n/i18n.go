// Package i18n is the text kario's programs show. Code is written in
// English, the source language, which needs no file; each component keeps
// the translations of its messages beside it, in locale/<language>.json
// ({"English message": "translation"}), embedded in its binary, and T gives
// a message in the process's language. A message a language's file lacks
// comes from the next language of its fallback chain (Chain: zh-Hant, then
// zh-Hans, then English), so a catalog translated in part still shows.
//
// The process's language is PHONE_LANG (DefaultLang when unset). The shell
// follows the user's choice while it runs: SetLang changes the language of
// the process, and every catalog made with New follows at once.
//
// A message with arguments is a fmt format, and its translation keeps the
// same verbs in the same order. One English message has one translation
// everywhere in a component: where two places need different words, they
// get different English.
package i18n

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
)

// The languages the system offers (languages.json).
const (
	SimplifiedChinese  = "zh-Hans"
	TraditionalChinese = "zh-Hant"
	English            = "en"
	Spanish            = "es"
)

// DefaultLang is the language of a process started without PHONE_LANG.
const DefaultLang = SimplifiedChinese

// Language is one the system offers: its tag, its name written in itself,
// the languages a message missing from it comes from, and the regions
// (ISO 3166 codes) that speak it when the user leaves the choice to the
// system.
type Language struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Fallbacks []string `json:"fallbacks"`
	Regions   []string `json:"regions"`
}

//go:embed languages.json
var languagesJSON []byte

var offered = func() (out struct {
	Languages []Language `json:"languages"`
	Other     string     `json:"other_regions"`
}) {
	if err := json.Unmarshal(languagesJSON, &out); err != nil {
		panic("i18n: languages.json: " + err.Error())
	}
	return out
}()

// Languages are the languages offered, in the order a language list shows them.
func Languages() []Language { return append([]Language(nil), offered.Languages...) }

func find(id string) (Language, bool) {
	for _, l := range offered.Languages {
		if l.ID == id {
			return l, true
		}
	}
	return Language{}, false
}

// Name is the language's name written in itself ("Español"), or the tag.
func Name(lang string) string {
	if l, ok := find(lang); ok {
		return l.Name
	}
	return lang
}

// Canonical is the offered language a tag stands for: zh, zh-CN and zh-SG
// are zh-Hans; zh-TW, zh-HK and zh-MO are zh-Hant; en-GB is en, es-MX is es.
// A language not offered is English, the source; "" stays "".
func Canonical(tag string) string {
	t := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-"))
	if t == "" {
		return ""
	}
	base, rest, _ := strings.Cut(t, "-")
	if base == "zh" {
		for _, p := range strings.Split(rest, "-") {
			if p == "hant" || p == "tw" || p == "hk" || p == "mo" {
				return TraditionalChinese
			}
		}
		return SimplifiedChinese
	}
	for _, l := range offered.Languages {
		if strings.ToLower(l.ID) == base || strings.ToLower(l.ID) == t {
			return l.ID
		}
	}
	return English
}

// Chain is where lang's messages come from, in order: itself, then its
// fallbacks (zh-Hant: zh-Hant, zh-Hans, en). It ends in English.
func Chain(lang string) []string {
	l, ok := find(Canonical(lang))
	if !ok {
		return []string{English}
	}
	out := append([]string{l.ID}, l.Fallbacks...)
	if out[len(out)-1] != English {
		out = append(out, English)
	}
	return out
}

// ForRegion is the language a region (ISO 3166 code) speaks, when the user
// leaves the language to the system: CN zh-Hans, TW HK MO zh-Hant, the
// Spanish-speaking countries es, every other region English.
func ForRegion(region string) string {
	r := strings.ToUpper(strings.TrimSpace(region))
	for _, l := range offered.Languages {
		for _, x := range l.Regions {
			if x == r {
				return l.ID
			}
		}
	}
	return offered.Other
}

// lang is the process's language once SetLang set it.
var lang atomic.Pointer[string]

// Lang is the process's language: the one SetLang set, else PHONE_LANG,
// else DefaultLang.
func Lang() string {
	if l := lang.Load(); l != nil {
		return *l
	}
	if l := Canonical(os.Getenv("PHONE_LANG")); l != "" {
		return l
	}
	return DefaultLang
}

// SetLang makes tag the process's language (canonical, returned): every
// catalog New made translates into it from now on.
func SetLang(tag string) string {
	l := Canonical(tag)
	if l == "" {
		l = DefaultLang
	}
	lang.Store(&l)
	return l
}

// Catalog is one component's translations into the languages offered.
type Catalog struct {
	src *source
	// fixed is the language of a catalog that does not follow the
	// process's ("" follows it).
	fixed string
}

type source struct {
	files  map[string]map[string]string // each language's own file
	mu     sync.Mutex
	merged map[string]map[string]string // by language: its chain, merged
}

// New reads fsys's translations (locale/<lang>.json, every language
// offered): T gives messages in the process's language, whichever it is
// at the time. A catalog that does not parse is a build mistake: New panics.
func New(fsys fs.FS) *Catalog { return &Catalog{src: load(fsys)} }

// NewLang is New for a given language, whatever the process's.
func NewLang(fsys fs.FS, tag string) *Catalog { return New(fsys).In(tag) }

// In is the catalog in a given language, sharing what was read.
func (c *Catalog) In(tag string) *Catalog {
	l := Canonical(tag)
	if l == "" {
		l = DefaultLang
	}
	return &Catalog{src: c.src, fixed: l}
}

func load(fsys fs.FS) *source {
	s := &source{files: map[string]map[string]string{}, merged: map[string]map[string]string{}}
	for _, l := range offered.Languages {
		raw, err := fs.ReadFile(fsys, path.Join("locale", l.ID+".json"))
		if err != nil {
			continue
		}
		msgs := map[string]string{}
		if err := json.Unmarshal(raw, &msgs); err != nil {
			panic(fmt.Sprintf("i18n: locale/%s.json: %v", l.ID, err))
		}
		s.files[l.ID] = msgs
	}
	return s
}

func (s *source) table(lang string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.merged[lang]; ok {
		return m
	}
	m := map[string]string{}
	chain := Chain(lang)
	for i := len(chain) - 1; i >= 0; i-- {
		for k, v := range s.files[chain[i]] {
			m[k] = v
		}
	}
	s.merged[lang] = m
	return m
}

// Lang is the language the catalog translates into now.
func (c *Catalog) Lang() string {
	if c.fixed != "" {
		return c.fixed
	}
	return Lang()
}

// T is msg in the catalog's language, formatted with args when there are
// any.
func (c *Catalog) T(msg string, args ...any) string {
	if t, ok := c.src.table(c.Lang())[msg]; ok {
		msg = t
	}
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

// Has reports whether msg has a translation in the catalog's language or a
// language it falls back to (the catalog checks).
func (c *Catalog) Has(msg string) bool {
	_, ok := c.src.table(c.Lang())[msg]
	return ok
}

// Messages are the messages translated in the catalog's language or its
// fallbacks (the catalog checks).
func (c *Catalog) Messages() []string {
	m := c.src.table(c.Lang())
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
