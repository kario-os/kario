package sandbox

import (
	"embed"

	"github.com/kario-os/kario/i18n"
)

//go:embed locale
var localeFS embed.FS

// tr translates the self-check names (locale/zh-Hans.json).
var tr = i18n.New(localeFS)
