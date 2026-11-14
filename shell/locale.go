package shell

import (
	"embed"

	"github.com/kario-os/kario/i18n"
)

//go:embed locale
var localeFS embed.FS

// tr translates the shell's UI text (locale/zh-Hans.json).
var tr = i18n.New(localeFS)
