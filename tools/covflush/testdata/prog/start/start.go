// Package start starts covflush from a package's init, as the system's
// cover.go files do: before the runtime's coverage has its meta-data.
package start

import "github.com/kario-os/kario/tools/covflush"

func init() { covflush.Start() }
