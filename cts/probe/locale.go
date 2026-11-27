package main

import (
	"embed"

	"github.com/kario-os/kario/i18n"
)

//go:embed locale
var localeFS embed.FS

// tr translates the probe's UI text (locale/zh-Hans.json).
var tr = i18n.New(localeFS)
