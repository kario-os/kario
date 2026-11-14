package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"path"
	"strconv"
	"strings"

	"github.com/dop251/goja"

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

// translate is phone.t(msg, ...args): msg's translation (msg itself when
// there is none), its verbs replaced by args in order: %s is the argument
// as a string (String(x)), %d as an integer (Number(x) truncated), %% a
// percent sign. A verb past the last argument stays as written, and so does
// the whole text when there are no arguments.
func translate(msgs map[string]string, msg string, args []goja.Value) string {
	if t, ok := msgs[msg]; ok {
		msg = t
	}
	if len(args) == 0 {
		return msg
	}
	var b strings.Builder
	for i := 0; i < len(msg); i++ {
		if msg[i] != '%' || i+1 == len(msg) {
			b.WriteByte(msg[i])
			continue
		}
		switch verb := msg[i+1]; {
		case verb == '%':
			b.WriteByte('%')
		case (verb == 's' || verb == 'd') && len(args) > 0:
			if verb == 's' {
				b.WriteString(args[0].String())
			} else {
				b.WriteString(integer(args[0]))
			}
			args = args[1:]
		default:
			b.WriteString(msg[i : i+2])
		}
		i++
	}
	return b.String()
}

func integer(v goja.Value) string {
	f := v.ToFloat()
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return v.ToNumber().String()
	}
	if f = math.Trunc(f); f == 0 {
		return "0"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
