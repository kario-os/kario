package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"

	"github.com/kario-os/kario/i18n"
)

// A mini-program's text is written in English and translated by its own
// catalog, locale/<lang>.json in its directory or package ({"English
// message": "translation"}), in the device's language (i18n.Lang). An
// installed program's catalog is one of its signed files.

// readCatalog reads a program's translations into lang along the
// language's fallback chain (i18n.Chain: a message zh-Hant lacks comes from
// zh-Hans); a message no catalog on the chain has, or a program without
// catalogs, shows as written.
func readCatalog(fsys fs.FS, lang string) (map[string]string, error) {
	var msgs map[string]string
	chain := i18n.Chain(lang)
	for i := len(chain) - 1; i >= 0; i-- {
		raw, err := fs.ReadFile(fsys, path.Join("locale", chain[i]+".json"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var file map[string]string
		if err := json.Unmarshal(raw, &file); err != nil {
			return nil, fmt.Errorf("locale/%s.json: %w", chain[i], err)
		}
		if msgs == nil {
			msgs = map[string]string{}
		}
		maps.Copy(msgs, file)
	}
	return msgs, nil
}

// lookup is msg's translation, msg itself when there is none. phone.t,
// which also puts its arguments in, runs in the program's runtime
// (runtime.js's translate).
func lookup(msgs map[string]string, msg string) string {
	if t, ok := msgs[msg]; ok {
		return t
	}
	return msg
}
