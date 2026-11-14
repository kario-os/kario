package shellcmd

import (
	"embed"

	"github.com/kario-os/kario/i18n"
)

//go:embed locale
var localeFS embed.FS

// tr translates the terminal's key line and the preview page
// (locale/zh-Hans.json).
var tr = i18n.New(localeFS)
