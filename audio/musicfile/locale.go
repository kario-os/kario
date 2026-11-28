package musicfile

import (
	"embed"

	"github.com/kario-os/kario/i18n"
)

//go:embed locale
var localeFS embed.FS

// tr names tracks that have no title of their own (locale/zh-Hans.json).
var tr = i18n.New(localeFS)
